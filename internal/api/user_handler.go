package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/auth"
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

type UserHandler struct {
	db *gorm.DB
}

func NewUserHandler(db *gorm.DB) *UserHandler {
	return &UserHandler{db: db}
}

type permissionInput struct {
	ICCID       string `json:"iccid"`
	CanMakeCall bool   `json:"can_make_call"`
	CanViewSMS  bool   `json:"can_view_sms"`
	CanSendSMS  bool   `json:"can_send_sms"`
	CanSendAT   bool   `json:"can_send_at"`
}

// Use bcrypt for secure hashing
func hashPassword(password string) (string, error) {
	return hashPasswordStrict(password)
}

func checkPasswordHash(password, hash string) bool {
	return checkPasswordStrict(password, hash)
}

func (h *UserHandler) Login(c *gin.Context) {
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&creds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user model.User
	// Find user by username first
	if err := h.db.Where("username = ?", creds.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// Be backward compatible optionally?
	// If current hash is SHA256 (64 hex chars), specific handling?
	// User didn't ask for migration, just improvements. Assuming we can break or mixed mode.
	// But "admin123" SHA256 is 64 chars. Bcrypt starts with $.
	// Let's just assume we check bcrypt. If we really wanted to support migration we would check length.
	// Since user considers SHA256 "too casual", let's strictly use bcrypt.
	// If the hash in DB is SHA256, verification will fail, which is expected for security upgrade unless we migrate.
	// We will assume "Invalid credentials" if it fails.

	if !checkPasswordHash(creds.Password, user.PasswordHash) {
		// Fallback check for SHA256 if we want to support legacy during transition?
		// For now, let's keep it strict.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	token, err := auth.GenerateToken(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  user,
	})
}

func (h *UserHandler) ListUsers(c *gin.Context) {
	var users []model.User
	if err := h.db.Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, users)
}

func (h *UserHandler) CreateUser(c *gin.Context) {
	var req struct {
		Username      string            `json:"username"`
		Password      string            `json:"password"`
		Role          string            `json:"role"`
		AllowedModems string            `json:"allowed_modems"`
		Permissions   []permissionInput `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user := model.User{
		Username:      req.Username,
		PasswordHash:  hash,
		Role:          req.Role,
		AllowedModems: req.AllowedModems,
	}
	if err := h.db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.replaceUserPermissions(user.ID, req.AllowedModems, req.Permissions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save permissions: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

func (h *UserHandler) UpdateUserPermissions(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if user.Role == "admin" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "admin permissions are always full"})
		return
	}

	var req struct {
		AllowedModems string            `json:"allowed_modems"`
		Permissions   []permissionInput `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user.AllowedModems = req.AllowedModems
	if err := h.db.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := h.replaceUserPermissions(user.ID, req.AllowedModems, req.Permissions); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save permissions: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *UserHandler) ListUserPermissions(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}

	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	var perms []model.UserModemPermission
	if err := h.db.Where("user_id = ?", user.ID).Order("iccid asc").Find(&perms).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id":        user.ID,
		"username":       user.Username,
		"allowed_modems": user.AllowedModems,
		"permissions":    perms,
	})
}

func (h *UserHandler) replaceUserPermissions(userID uint, allowedModems string, inputs []permissionInput) error {
	allowedSet := map[string]struct{}{}
	for _, iccid := range splitAndTrimAllowed(allowedModems) {
		allowedSet[iccid] = struct{}{}
	}
	_, hasWildcard := allowedSet["*"]

	if err := h.db.Where("user_id = ?", userID).Delete(&model.UserModemPermission{}).Error; err != nil {
		return err
	}

	for _, item := range inputs {
		iccid := strings.TrimSpace(item.ICCID)
		if iccid == "" {
			continue
		}
		if !hasWildcard {
			if _, ok := allowedSet[iccid]; !ok {
				continue
			}
		}

		rec := model.UserModemPermission{
			UserID:      userID,
			ICCID:       iccid,
			CanMakeCall: item.CanMakeCall,
			CanViewSMS:  item.CanViewSMS,
			CanSendSMS:  item.CanSendSMS,
			CanSendAT:   item.CanSendAT,
		}
		if err := h.db.Create(&rec).Error; err != nil {
			return err
		}
	}

	return nil
}

func (h *UserHandler) DeleteUser(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.Atoi(idStr)
	if err := h.db.Delete(&model.User{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *UserHandler) ChangePassword(c *gin.Context) {
	// Got User from Middleware
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify old password
	if !checkPasswordHash(req.OldPassword, user.PasswordHash) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Incorrect old password"})
		return
	}

	// Update
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user.PasswordHash = hash
	if err := h.db.Save(user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "Password updated"})
}
