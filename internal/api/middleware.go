package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/auth"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/pkg/logger"
	"gorm.io/gorm"
)

func AuthMiddleware(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
			if token := strings.TrimSpace(c.Query("token")); token != "" {
				c.Request.Header.Set("Authorization", "Bearer "+token)
			}
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			logger.Log.Warn("Auth Middleware: Missing Authorization header")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			return
		}

		tokenRaw := normalizeAuthBearer(authHeader)
		if tokenRaw == "" {
			logger.Log.Warnf("Auth Middleware: Invalid header format: %s", authHeader)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header format must be Bearer {token}"})
			return
		}

		if isSMSIEAPIKey(tokenRaw) {
			keyHash := hashAPIKey(tokenRaw)
			var key model.APIKey
			if err := db.Where("key_hash = ? AND is_active = ?", keyHash, true).First(&key).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
				return
			}

			now := time.Now()
			if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "API key expired"})
				return
			}

			var user model.User
			if err := db.First(&user, key.UserID).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
				return
			}

			_ = db.Model(&model.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", now).Error

			c.Set("user", &user)
			c.Set("userID", user.ID)
			c.Set("role", user.Role)
			c.Set("auth_type", "api_key")
			c.Set("api_key", &key)
			c.Next()
			return
		}

		claims, err := auth.ValidateToken(tokenRaw)
		if err != nil {
			logger.Log.Warnf("Auth Middleware: Token validation failed: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token: " + err.Error()})
			return
		}

		// Optionally fetch full user from DB if we need up-to-date fields like AllowedModems
		var user model.User
		if err := db.First(&user, claims.UserID).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
			return
		}

		// Set user in context
		c.Set("user", &user)
		c.Set("userID", claims.UserID)
		c.Set("role", claims.Role)
		c.Set("auth_type", "jwt")

		c.Next()
	}
}

func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, exists := c.Get("role")
		if !exists || role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			return
		}
		c.Next()
	}
}

func APIKeyAllowedOnly() gin.HandlerFunc {
	allowed := map[string]bool{
		"GET /api/v1/modems":                     true,
		"GET /api/v1/modems/:iccid":              true,
		"GET /api/v1/sms":                        true,
		"POST /api/v1/modems/:iccid/send":        true,
		"POST /api/v1/modems/:iccid/at":          true,
		"POST /api/v1/modems/:iccid/input":       true,
		"GET /api/v1/modems/:iccid/call/state":   true,
		"POST /api/v1/modems/:iccid/call/dial":   true,
		"POST /api/v1/modems/:iccid/call/hangup": true,
		"POST /api/v1/modems/:iccid/call/dtmf":   true,
		"GET /api/v1/modems/:iccid/ws":           true,
	}

	return func(c *gin.Context) {
		authType, ok := c.Get("auth_type")
		if !ok || authType != "api_key" {
			c.Next()
			return
		}

		key := c.Request.Method + " " + c.FullPath()
		if !allowed[key] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "API key not allowed for this endpoint"})
			return
		}

		c.Next()
	}
}
