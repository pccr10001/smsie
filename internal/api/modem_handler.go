package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/worker"
	"gorm.io/gorm"
)

type ModemHandler struct {
	db *gorm.DB
	wm *worker.Manager
}

func NewModemHandler(db *gorm.DB, wm *worker.Manager) *ModemHandler {
	return &ModemHandler{db: db, wm: wm}
}

func (h *ModemHandler) ListModems(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	isAdmin := (user.Role == "admin")
	var allowed []string
	if !isAdmin {
		allowed = splitAllowed(user.AllowedModems)
	}

	var modems []model.Modem
	db := h.db
	if !isAdmin {
		if len(allowed) == 0 {
			db = db.Where("1 = 0") // No access
		} else {
			db = db.Where("iccid IN ?", allowed)
		}
	}

	if err := db.Find(&modems).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, modems)
}

func splitAllowed(s string) []string {
	if s == "" {
		return []string{}
	}
	if s == "*" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func (h *ModemHandler) GetModem(c *gin.Context) {
	iccid := c.Param("iccid")
	var modem model.Modem
	if err := h.db.First(&modem, "iccid = ?", iccid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not found"})
		return
	}
	c.JSON(http.StatusOK, modem)
}

func (h *ModemHandler) UpdateModem(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)
	if user.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	iccid := c.Param("iccid")
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var modem model.Modem
	if err := h.db.First(&modem, "iccid = ?", iccid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not found"})
		return
	}

	modem.Name = req.Name
	if err := h.db.Save(&modem).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update modem"})
		return
	}
	c.JSON(http.StatusOK, modem)
}

func (h *ModemHandler) ScanNetworks(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

	// Auth check
	if user.Role != "admin" {
		allowed := splitAllowed(user.AllowedModems)
		allow := false
		for _, a := range allowed {
			if a == iccid || a == "*" {
				allow = true
				break
			}
		}
		if !allow {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for this modem"})
			return
		}
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	if w.IsBusy() {
		c.JSON(http.StatusConflict, gin.H{"error": "Modem is busy"})
		return
	}

	networks, err := w.ScanNetworks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Scan failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"networks": networks})
}

func (h *ModemHandler) SetOperator(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

	// Auth check
	if user.Role != "admin" {
		allowed := splitAllowed(user.AllowedModems)
		allow := false
		for _, a := range allowed {
			if a == iccid || a == "*" {
				allow = true
				break
			}
		}
		if !allow {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for this modem"})
			return
		}
	}

	var req struct {
		Operator string `json:"operator"` // "AUTO" or numeric ID
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	if w.IsBusy() {
		c.JSON(http.StatusConflict, gin.H{"error": "Modem is busy"})
		return
	}

	err := w.SetOperator(req.Operator)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Set operator failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *ModemHandler) ExecuteAT(c *gin.Context) {
	h.executeCommand(c, 10*time.Second)
}

func (h *ModemHandler) ExecuteInput(c *gin.Context) {
	h.executeCommand(c, 5*time.Second)
}

func (h *ModemHandler) executeCommand(c *gin.Context, timeout time.Duration) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

	// Auth check
	if user.Role != "admin" {
		allowed := splitAllowed(user.AllowedModems)
		allow := false
		for _, a := range allowed {
			if a == iccid || a == "*" {
				allow = true
				break
			}
		}
		if !allow {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for this modem"})
			return
		}
	}

	var req struct {
		Cmd     string `json:"cmd"`
		Timeout int    `json:"timeout"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	// For simple AT commands, we set occupied to prevent polling overlap
	// But if we are in input mode (waiting for >), we need to allow continuation.
	// The worker's IsBusy handles simple locking.
	// We might want to EXPLICITLY set/unset occupied if this is a complex flow?
	// User asked to "avoid colliding with polling".
	// Simple ExecuteAT handles one command.
	// If the user wants to type commands manually, they might want to "Open Session".
	// But sticking to the stateless API request:
	w.SetOccupied(true)
	defer w.SetOccupied(false)

	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Millisecond
	}

	resp, err := w.ExecuteAT(req.Cmd, timeout)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"response": resp})
}

func (h *ModemHandler) SendSMS(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

	// Auth check
	if user.Role != "admin" {
		allowed := splitAllowed(user.AllowedModems)
		allow := false
		for _, a := range allowed {
			if a == iccid || a == "*" {
				allow = true
				break
			}
		}
		if !allow {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied for this modem"})
			return
		}
	}

	var req struct {
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Phone == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Phone number is required"})
		return
	}
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is required"})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	if w.IsBusy() {
		c.JSON(http.StatusConflict, gin.H{"error": "Modem is busy"})
		return
	}

	err := w.SendSMS(req.Phone, req.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Send SMS failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "SMS sent successfully"})
}
