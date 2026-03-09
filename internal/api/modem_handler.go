package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
	WorkerExists         bool      `json:"worker_exists"`
	CallSupported        bool      `json:"call_supported"`
	SIPAvailable         bool      `json:"sip_available"`
	SIPLineID            string    `json:"sip_line_id,omitempty"`
	SIPActive            bool      `json:"sip_listener_active"`
	SIPTransport         string    `json:"sip_listener_transport,omitempty"`
	SIPRegisterState     string    `json:"sip_register_state,omitempty"`
	SIPRegisterReason    string    `json:"sip_register_reason,omitempty"`
	SIPRegisterUpdatedAt time.Time `json:"sip_register_updated_at,omitempty"`
}

func NewModemHandler(db *gorm.DB, wm *worker.Manager, callMgr *calling.Manager) *ModemHandler {
	return &ModemHandler{db: db, wm: wm, callMgr: callMgr}
}

func (h *ModemHandler) sipModeEnabled() bool {
	return h.callMgr != nil && h.callMgr.SIPEnabled()
}

func (h *ModemHandler) sipAvailableForICCID(iccid string) (calling.SIPInboundLineInfo, bool) {
	if h.callMgr == nil {
		return calling.SIPInboundLineInfo{}, false
	}
	info, ok := h.callMgr.SIPInboundLineInfo(iccid)
	if !ok || !info.Active {
		return calling.SIPInboundLineInfo{}, false
	}
	return info, true
}

func normalizeCallVia(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "sip":
		return "sip"
	default:
		return "modem"
	}
}

func modemWithRuntimeState(base model.Modem, rt worker.RuntimeModemState, hasRuntime bool) model.Modem {
	m := base
	if !hasRuntime {
		m.Status = "offline"
		m.SignalStrength = 0
		m.Operator = ""
		m.Registration = "Unknown"
		m.LastSeen = time.Time{}
		return m
	}

	if rt.ICCID != "" {
		m.ICCID = rt.ICCID
	}
	if rt.IMEI != "" {
		m.IMEI = rt.IMEI
	}
	if rt.PortName != "" {
		m.PortName = rt.PortName
	}
	m.Status = rt.Status
	m.SignalStrength = rt.SignalStrength
	m.Operator = rt.Operator
	m.Registration = rt.Registration
	m.LastSeen = rt.LastSeen
	return m
}

func removeICCIDFromAllowedList(raw, iccid string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return raw
	}

	target := strings.TrimSpace(iccid)
	if target == "" {
		return raw
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" || strings.EqualFold(v, target) {
			continue
		}
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

func (h *ModemHandler) ListModems(c *gin.Context) {
	actor, exists := getActor(c)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if actor.APIKey != nil {
		if !(actor.APIKey.CanMakeCall || actor.APIKey.CanViewSMS || actor.APIKey.CanSendSMS || actor.APIKey.CanSendAT) {
			c.JSON(http.StatusForbidden, gin.H{"error": "API key permission denied"})
			return
		}
	}

	isAdmin := actor.User.Role == "admin"
	allowed, err := allowedICCIDsForPermission(h.db, actor.User, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "permission check failed"})
		return
	}

	var modems []model.Modem
	db := h.db
	if !isAdmin {
		if len(allowed) == 0 {
			db = db.Where("1 = 0") // No access
		} else if hasWildcardICCID(allowed) {
			// keep full set under wildcard
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

func (h *ModemHandler) DTMF(c *gin.Context) {
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermMakeCall) {
		return
	}

	var req struct {
		Tone string `json:"tone"`
		Via  string `json:"via"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tone := strings.TrimSpace(req.Tone)
	if len(tone) != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tone must be one of 0-9,*,#"})
		return
	}
	if !strings.Contains("0123456789*#", tone) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tone must be one of 0-9,*,#"})
		return
	}

	requestedVia := normalizeCallVia(req.Via)
	routeSIP := requestedVia == "sip"
	if req.Via == "" && h.callMgr != nil && h.callMgr.HasActiveSIPCall(iccid) {
		routeSIP = true
	}

	if routeSIP {
		if _, ok := h.sipAvailableForICCID(iccid); !ok {
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": "sip client not enabled"})
			return
		}
		if err := h.callMgr.SendSIPDTMF(iccid, tone); err != nil {
			if calling.IsSIPNoActiveCallError(err) {
				c.JSON(http.StatusConflict, gin.H{"error": "no active call"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "DTMF failed: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "tone": tone})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	if _, err := w.ExecuteAT(`AT+VTS="`+tone+`"`, 5*time.Second); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "DTMF failed: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok", "tone": tone})
}

func (h *ModemHandler) GetModem(c *gin.Context) {
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, "") {
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	rt, hasRuntime := worker.RuntimeModemState{}, false
	if w != nil {
		rt, hasRuntime = w.RuntimeModemState()
	}

	var modem model.Modem
	if err := h.db.First(&modem, "iccid = ?", iccid).Error; err != nil {
		if hasRuntime {
			c.JSON(http.StatusOK, h.modemWithWorkerState(modemWithRuntimeState(model.Modem{ICCID: iccid}, rt, true)))
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not found"})
		return
	}
	c.JSON(http.StatusOK, h.modemWithWorkerState(modemWithRuntimeState(modem, rt, hasRuntime)))
}

func (h *ModemHandler) modemWithWorkerState(modem model.Modem) modemWithWorker {
	w := h.wm.GetWorkerByICCID(modem.ICCID)
	workerExists := w != nil
	if w != nil {
		if rt, ok := w.RuntimeModemState(); ok {
			modem = modemWithRuntimeState(modem, rt, true)
		}
	} else {
		modem = modemWithRuntimeState(modem, worker.RuntimeModemState{}, false)
	}

	callSupported := w != nil && w.IsUACReady()
	modem.SIPHasPassword = strings.TrimSpace(modem.SIPPassword) != ""
	sipLineID := ""
	sipActive := false
	sipTransport := strings.ToUpper(strings.TrimSpace(modem.SIPTransport))
	if sipTransport == "" {
		sipTransport = "UDP"
	}
	sipRegisterState := ""
	sipRegisterReason := ""
	sipRegisterUpdatedAt := time.Time{}
	sipAvailable := callSupported && modem.SIPEnabled && strings.TrimSpace(modem.SIPUsername) != "" && strings.TrimSpace(modem.SIPProxy) != ""
	if h.callMgr != nil && h.callMgr.SIPEnabled() {
		if info, ok := h.callMgr.SIPInboundLineInfo(modem.ICCID); ok {
			sipLineID = info.LineID
			sipActive = info.Active
			sipRegisterState = info.RegisterState
			sipRegisterReason = info.RegisterReason
			sipRegisterUpdatedAt = info.UpdatedAt
			if info.Transport != "" {
				sipTransport = strings.ToUpper(info.Transport)
			}
		}
	}

	return modemWithWorker{
		Modem:                modem,
		WorkerExists:         workerExists,
		CallSupported:        callSupported,
		SIPAvailable:         sipAvailable,
		SIPLineID:            sipLineID,
		SIPActive:            sipActive,
		SIPTransport:         sipTransport,
		SIPRegisterState:     sipRegisterState,
		SIPRegisterReason:    sipRegisterReason,
		SIPRegisterUpdatedAt: sipRegisterUpdatedAt,
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
		Name              string `json:"name"`
		SIPEnabled        bool   `json:"sip_enabled"`
		SIPUsername       string `json:"sip_username"`
		SIPPassword       string `json:"sip_password"`
		SIPProxy          string `json:"sip_proxy"`
		SIPPort           int    `json:"sip_port"`
		SIPDomain         string `json:"sip_domain"`
		SIPTransport      string `json:"sip_transport"`
		SIPRegister       bool   `json:"sip_register"`
		SIPTLSSkipVerify  bool   `json:"sip_tls_skip_verify"`
		SIPListenPort     int    `json:"sip_listen_port"`
		SIPAcceptIncoming bool   `json:"sip_accept_incoming"`
		SIPInviteTarget   string `json:"sip_invite_target"`
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

	transport := strings.ToLower(strings.TrimSpace(req.SIPTransport))
	switch transport {
	case "", "udp", "tcp", "tls":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "sip transport must be udp, tcp or tls"})
		return
	}
	if transport == "" {
		transport = "udp"
	}
	if req.SIPPort < 0 || req.SIPPort > 65535 || req.SIPListenPort < 0 || req.SIPListenPort > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sip ports must be between 0 and 65535"})
		return
	}

	if req.SIPListenPort > 0 {
		var conflict int64
		h.db.Model(&model.Modem{}).Where("iccid <> ? AND sip_listen_port = ?", iccid, req.SIPListenPort).Count(&conflict)
		if conflict > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "sip listener port already assigned to another modem"})
			return
		}
	}

	updates := map[string]interface{}{
		"name":                req.Name,
		"sip_enabled":         req.SIPEnabled,
		"sip_username":        strings.TrimSpace(req.SIPUsername),
		"sip_proxy":           strings.TrimSpace(req.SIPProxy),
		"sip_port":            req.SIPPort,
		"sip_domain":          strings.TrimSpace(req.SIPDomain),
		"sip_transport":       transport,
		"sip_register":        req.SIPRegister,
		"sip_tls_skip_verify": req.SIPTLSSkipVerify,
		"sip_listen_port":     req.SIPListenPort,
		"sip_accept_incoming": req.SIPAcceptIncoming,
		"sip_invite_target":   strings.TrimSpace(req.SIPInviteTarget),
	}
	if strings.TrimSpace(req.SIPPassword) != "" {
		updates["sip_password"] = req.SIPPassword
	}

	if err := h.db.Model(&model.Modem{}).Where("iccid = ?", iccid).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update modem"})
		return
	}
	if err := h.db.First(&modem, "iccid = ?", iccid).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload modem"})
		return
	}
	c.JSON(http.StatusOK, h.modemWithWorkerState(modem))
}

func (h *ModemHandler) DeleteModem(c *gin.Context) {
	actor, exists := getActor(c)
	if !exists || actor.User == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if actor.User.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	iccid := strings.TrimSpace(c.Param("iccid"))
	if iccid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "iccid is required"})
		return
	}

	if h.callMgr != nil {
		_ = h.callMgr.CloseSession(iccid)
		_ = h.callMgr.StopSIPInboundLine("sip-line-" + iccid)
	}
	if h.wm != nil {
		h.wm.RemoveWorkerByICCID(iccid)
	}

	tx := h.db.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": tx.Error.Error()})
		return
	}

	if err := tx.Where("iccid = ?", iccid).Delete(&model.SMS{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("iccid = ?", iccid).Delete(&model.Webhook{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("iccid = ?", iccid).Delete(&model.UserModemPermission{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := tx.Where("iccid = ?", iccid).Delete(&model.Modem{}).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var users []model.User
	if err := tx.Find(&users).Error; err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, u := range users {
		clean := removeICCIDFromAllowedList(u.AllowedModems, iccid)
		if clean == u.AllowedModems {
			continue
		}
		if err := tx.Model(&model.User{}).Where("id = ?", u.ID).Update("allowed_modems", clean).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted", "iccid": iccid})
}

func (h *ModemHandler) ScanNetworks(c *gin.Context) {
	actor, exists := getActor(c)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if actor.User == nil || actor.User.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	iccid := c.Param("iccid")

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
	actor, exists := getActor(c)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if actor.User == nil || actor.User.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	iccid := c.Param("iccid")

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
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermSendAT) {
		return
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
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermSendSMS) {
		return
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
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermMakeCall) {
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if w == nil && !(h.callMgr != nil && h.callMgr.SIPEnabled() && h.callMgr.HasActiveSIPCall(iccid)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}

	modemState := worker.CallState{}
	uacReady := false
	uacVID, uacPID := "", ""
	usbDevices := []calling.USBDeviceInfo{}
	if w != nil {
		state := w.CallState()
		modemState = state
		uacReady = w.IsUACReady()
		uacVID = state.UACVID
		uacPID = state.UACPID
		if state.UACVID != "" && state.UACPID != "" {
			if devs, err := calling.EnumerateByVIDPID(state.UACVID, state.UACPID); err == nil {
				usbDevices = devs
			}
		}
	}

	sipState, sipReason, sipUpdatedAt := "idle", "init", time.Now()
	sipActive := false
	sipAvailable := false
	sipRegisterState := ""
	sipRegisterReason := ""
	sipRegisterUpdatedAt := time.Time{}
	sipTransport := ""
	sipListenPort := 0
	if h.callMgr != nil && h.callMgr.SIPEnabled() {
		if st, rs, up, ok := h.callMgr.SIPCallState(iccid); ok && st != "" {
			sipState, sipReason, sipUpdatedAt = st, rs, up
		}
		sipActive = h.callMgr.HasActiveSIPCall(iccid)
		if info, ok := h.callMgr.SIPInboundLineInfo(iccid); ok {
			sipAvailable = info.Active
			sipRegisterState = info.RegisterState
			sipRegisterReason = info.RegisterReason
			sipRegisterUpdatedAt = info.UpdatedAt
			sipTransport = strings.ToUpper(info.Transport)
			sipListenPort = info.LocalPort
		}
	}

	callMode := "modem"
	state := modemState.State
	reason := modemState.Reason
	updatedAt := modemState.UpdatedAt
	if state == "" {
		state = "idle"
		reason = "init"
		updatedAt = time.Now()
	}
	if sipActive {
		callMode = "sip"
		state = sipState
		reason = sipReason
		updatedAt = sipUpdatedAt
	}

	c.JSON(http.StatusOK, gin.H{
		"state":                  state,
		"reason":                 reason,
		"updated_at":             updatedAt,
		"number":                 modemState.Number,
		"direction":              modemState.Direction,
		"stat":                   modemState.Stat,
		"mode":                   modemState.Mode,
		"incoming":               modemState.Incoming,
		"voice":                  modemState.Voice,
		"incoming_ringing":       modemState.IncomingRinging,
		"uac_ready":              uacReady,
		"uac_vid":                uacVID,
		"uac_pid":                uacPID,
		"usb_devices":            usbDevices,
		"call_mode":              callMode,
		"sip_available":          sipAvailable,
		"sip_listener_transport": sipTransport,
		"sip_listen_port":        sipListenPort,
		"sip_state": gin.H{
			"state":               sipState,
			"reason":              sipReason,
			"updated_at":          sipUpdatedAt,
			"active":              sipActive,
			"register_state":      sipRegisterState,
			"register_reason":     sipRegisterReason,
			"register_updated_at": sipRegisterUpdatedAt,
		},
	})
}

func (h *ModemHandler) Dial(c *gin.Context) {
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermMakeCall) {
		return
	}

	var req struct {
		Number string `json:"number"`
		Via    string `json:"via"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	w := h.wm.GetWorkerByICCID(iccid)
	if h.callMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "calling manager not initialized"})
		return
	}

	if w == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Modem not active (worker not found)"})
		return
	}
	if !w.IsUACReady() {
		c.JSON(http.StatusConflict, gin.H{"error": "UAC is not enabled on modem (QCFG USBCFG check failed)"})
		return
	}

	via := normalizeCallVia(req.Via)
	if via == "sip" {
		if _, ok := h.sipAvailableForICCID(iccid); !ok {
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": "sip client not enabled"})
			return
		}
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
	if err := h.callMgr.EnsureAudio(iccid); err != nil {
		if h.callMgr != nil {
			_ = h.callMgr.CloseSession(iccid)
		}
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "Audio init failed: " + err.Error()})
		return
	}

	if via == "sip" {
		if err := h.callMgr.DialSIP(iccid, req.Number); err != nil {
			if calling.IsSIPInvalidDialNumberError(err) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dial number"})
				return
			}
			if calling.IsSIPCallInProgressError(err) {
				c.JSON(http.StatusConflict, gin.H{"error": "call already in progress"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Dial failed: " + err.Error()})
			return
		}
		state, reason, updatedAt, _ := h.callMgr.SIPCallState(iccid)
		c.JSON(http.StatusOK, gin.H{"status": "ok", "call_mode": "sip", "call_state": gin.H{
			"state":      state,
			"reason":     reason,
			"updated_at": updatedAt,
		}})
		return
	}

	if err := h.callMgr.RequireConnected(iccid); err != nil {
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "WebRTC not ready. Please complete signaling first."})
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

	c.JSON(http.StatusOK, gin.H{"status": "ok", "call_mode": "modem", "call_state": w.CallState()})
}

func (h *ModemHandler) Hangup(c *gin.Context) {
	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermMakeCall) {
		return
	}

	var req struct {
		Via string `json:"via"`
	}
	_ = c.ShouldBindJSON(&req)

	if h.callMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "calling manager not initialized"})
		return
	}

	via := normalizeCallVia(req.Via)
	routeSIP := via == "sip"
	if req.Via == "" && h.callMgr.HasActiveSIPCall(iccid) {
		routeSIP = true
	}
	if routeSIP {
		if _, ok := h.sipAvailableForICCID(iccid); !ok {
			c.JSON(http.StatusPreconditionFailed, gin.H{"error": "sip client not enabled"})
			return
		}
		err := h.callMgr.HangupSIP(iccid)
		if err != nil && !calling.IsSIPNoActiveCallError(err) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Hangup failed: " + err.Error()})
			return
		}
		state, reason, updatedAt, _ := h.callMgr.SIPCallState(iccid)
		if state == "" {
			state = "idle"
			reason = "hangup"
			updatedAt = time.Now()
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "call_mode": "sip", "call_state": gin.H{
			"state":      state,
			"reason":     reason,
			"updated_at": updatedAt,
		}})
		return
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

	c.JSON(http.StatusOK, gin.H{"status": "ok", "call_mode": "modem", "call_state": w.CallState()})
}

func (h *ModemHandler) Reboot(c *gin.Context) {
	actor, exists := getActor(c)
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if actor.User == nil || actor.User.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
		return
	}

	iccid := c.Param("iccid")

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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	iccid := c.Param("iccid")
	if !enforceICCIDPermission(c, h.db, iccid, PermMakeCall) {
		return
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
