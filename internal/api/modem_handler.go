package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pccr10001/smsie/internal/auth"
	"github.com/pccr10001/smsie/internal/calling"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/worker"
	"gorm.io/gorm"
)

type ModemHandler struct {
	db      *gorm.DB
	wm      *worker.Manager
	callMgr *calling.Manager
}

type modemWithWorker struct {
	model.Modem
	WorkerExists  bool `json:"worker_exists"`
	CallSupported bool `json:"call_supported"`
}

func NewModemHandler(db *gorm.DB, wm *worker.Manager, callMgr *calling.Manager) *ModemHandler {
	return &ModemHandler{db: db, wm: wm, callMgr: callMgr}
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

	resp := make([]modemWithWorker, 0, len(modems))
	for _, m := range modems {
		resp = append(resp, h.modemWithWorkerState(m))
	}

	c.JSON(http.StatusOK, resp)
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
	c.JSON(http.StatusOK, h.modemWithWorkerState(modem))
}

func (h *ModemHandler) modemWithWorkerState(modem model.Modem) modemWithWorker {
	w := h.wm.GetWorkerByICCID(modem.ICCID)
	workerExists := w != nil
	callSupported := false
	if w != nil {
		callSupported = w.IsUACReady()
	}

	return modemWithWorker{
		Modem:         modem,
		WorkerExists:  workerExists,
		CallSupported: callSupported,
	}
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

func (h *ModemHandler) GetCallState(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

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

	state := w.CallState()
	usbDevices := []calling.USBDeviceInfo{}
	if state.UACVID != "" && state.UACPID != "" {
		if devs, err := calling.EnumerateByVIDPID(state.UACVID, state.UACPID); err == nil {
			usbDevices = devs
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"state":       state.State,
		"reason":      state.Reason,
		"updated_at":  state.UpdatedAt,
		"uac_ready":   w.IsUACReady(),
		"uac_vid":     state.UACVID,
		"uac_pid":     state.UACPID,
		"usb_devices": usbDevices,
	})
}

func (h *ModemHandler) Dial(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

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
		Number string `json:"number"`
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
	if !w.IsUACReady() {
		c.JSON(http.StatusConflict, gin.H{"error": "UAC is not enabled on modem (QCFG USBCFG check failed)"})
		return
	}
	if h.callMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "calling manager not initialized"})
		return
	}

	vid, pid := w.UACIdentity()
	target := calling.ModemTarget{
		PortName: w.PortName,
		VID:      vid,
		PID:      pid,
	}
	if _, err := h.callMgr.EnsureSession(iccid, target); err != nil {
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "WebRTC session init failed: " + err.Error()})
		return
	}
	if err := h.callMgr.RequireConnected(iccid); err != nil {
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "WebRTC not ready. Please complete signaling first."})
		return
	}
	if err := h.callMgr.EnsureAudio(iccid); err != nil {
		if h.callMgr != nil {
			_ = h.callMgr.CloseSession(iccid)
		}
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "Audio init failed: " + err.Error()})
		return
	}

	err := w.Dial(req.Number)
	if err != nil {
		if h.callMgr != nil {
			_ = h.callMgr.CloseSession(iccid)
		}
		if worker.IsInvalidDialNumberError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dial number"})
			return
		}
		if worker.IsCallInProgressError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "call already in progress"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Dial failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "call_state": w.CallState()})
}

func (h *ModemHandler) Hangup(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

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

	err := w.Hangup()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Hangup failed: " + err.Error()})
		return
	}
	if h.callMgr != nil {
		_ = h.callMgr.CloseSession(iccid)
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "call_state": w.CallState()})
}

func (h *ModemHandler) Reboot(c *gin.Context) {
	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

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

	if h.callMgr != nil {
		_ = h.callMgr.CloseSession(iccid)
	}

	if err := w.Reboot(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Reboot failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "Reboot command sent (AT+CFUN=1,1)"})
}

func (h *ModemHandler) WS(c *gin.Context) {
	if _, exists := c.Get("user"); !exists {
		token := c.Query("token")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		claims, err := auth.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		var user model.User
		if err := h.db.First(&user, claims.UserID).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
			return
		}
		c.Set("user", &user)
	}

	userObj, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	user := userObj.(*model.User)

	iccid := c.Param("iccid")

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

	if h.callMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "calling manager not initialized"})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	vid, pid := w.UACIdentity()
	target := calling.ModemTarget{
		PortName: w.PortName,
		VID:      vid,
		PID:      pid,
	}

	handleModemWS(c, h.callMgr, iccid, target)
}
