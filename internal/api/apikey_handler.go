package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

type APIKeyHandler struct {
	db *gorm.DB
}

func NewAPIKeyHandler(db *gorm.DB) *APIKeyHandler {
	return &APIKeyHandler{db: db}
}

func (h *APIKeyHandler) ListMyAPIKeys(c *gin.Context) {
	actor, ok := getActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var keys []model.APIKey
	if err := h.db.Where("user_id = ?", actor.User.ID).Order("id desc").Find(&keys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, keys)
}

func (h *APIKeyHandler) CreateMyAPIKey(c *gin.Context) {
	actor, ok := getActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req struct {
		Name        string `json:"name"`
		CanMakeCall *bool  `json:"can_make_call"`
		CanViewSMS  *bool  `json:"can_view_sms"`
		CanSendSMS  *bool  `json:"can_send_sms"`
		CanSendAT   *bool  `json:"can_send_at"`
		ExpiresAt   string `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		req.Name = "default"
	}

	baseMakeCall := userHasAnyPermission(h.db, actor.User, PermMakeCall)
	baseViewSMS := userHasAnyPermission(h.db, actor.User, PermViewSMS)
	baseSendSMS := userHasAnyPermission(h.db, actor.User, PermSendSMS)
	baseSendAT := userHasAnyPermission(h.db, actor.User, PermSendAT)

	if actor.User.Role == "admin" {
		baseMakeCall, baseViewSMS, baseSendSMS, baseSendAT = true, true, true, true
	}

	canMakeCall := baseMakeCall
	canViewSMS := baseViewSMS
	canSendSMS := baseSendSMS
	canSendAT := baseSendAT
	if req.CanMakeCall != nil {
		canMakeCall = *req.CanMakeCall && baseMakeCall
	}
	if req.CanViewSMS != nil {
		canViewSMS = *req.CanViewSMS && baseViewSMS
	}
	if req.CanSendSMS != nil {
		canSendSMS = *req.CanSendSMS && baseSendSMS
	}
	if req.CanSendAT != nil {
		canSendAT = *req.CanSendAT && baseSendAT
	}

	if !(canMakeCall || canViewSMS || canSendSMS || canSendAT) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "API key must include at least one permission"})
		return
	}

	var expiresAt *time.Time
	if strings.TrimSpace(req.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339"})
			return
		}
		expiresAt = &t
	}

	rawKey, err := randomAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate API key"})
		return
	}

	rec := model.APIKey{
		UserID:      actor.User.ID,
		Name:        strings.TrimSpace(req.Name),
		KeyPrefix:   makeAPIKeyPrefix(rawKey),
		KeyHash:     hashAPIKey(rawKey),
		CanMakeCall: canMakeCall,
		CanViewSMS:  canViewSMS,
		CanSendSMS:  canSendSMS,
		CanSendAT:   canSendAT,
		IsActive:    true,
		ExpiresAt:   expiresAt,
	}

	if err := h.db.Create(&rec).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"api_key": rawKey,
		"record":  rec,
	})
}

func (h *APIKeyHandler) RotateMyAPIKey(c *gin.Context) {
	actor, ok := getActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key id"})
		return
	}

	var key model.APIKey
	if err := h.db.Where("id = ? AND user_id = ?", id, actor.User.ID).First(&key).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}

	rawKey, err := randomAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate API key"})
		return
	}

	key.KeyPrefix = makeAPIKeyPrefix(rawKey)
	key.KeyHash = hashAPIKey(rawKey)
	key.IsActive = true
	if err := h.db.Save(&key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"api_key": rawKey, "record": key})
}

func (h *APIKeyHandler) DeleteMyAPIKey(c *gin.Context) {
	actor, ok := getActor(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key id"})
		return
	}

	if err := h.db.Where("id = ? AND user_id = ?", id, actor.User.ID).Delete(&model.APIKey{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
