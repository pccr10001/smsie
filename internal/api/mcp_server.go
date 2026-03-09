package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/worker"
	"github.com/pccr10001/smsie/pkg/logger"
	"github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/tpdu"
	"gorm.io/gorm"
)

const (
	defaultMCPPageSize       = 20
	maxMCPPageSize           = 100
	defaultMCPMaxRecords     = 100
	maxMCPMaxRecords         = 500
	defaultMCPWaitTimeoutSec = 30
	maxMCPWaitTimeoutSec     = 120
	defaultMCPWaitMaxRecords = 20
	maxMCPWaitMaxRecords     = 100
)

type mcpActorContextKey struct{}

type MCPHTTPServer struct {
	db      *gorm.DB
	wm      *worker.Manager
	server  *sdkmcp.Server
	handler http.Handler
}

type mcpListModemsInput struct {
	Permission string `json:"permission,omitempty" jsonschema:"permission filter: any, make_call, view_sms, send_sms, send_at"`
}

type mcpModemPermissions struct {
	CanViewSMS  bool `json:"can_view_sms"`
	CanSendSMS  bool `json:"can_send_sms"`
	CanSendAT   bool `json:"can_send_at"`
	CanMakeCall bool `json:"can_make_call"`
}

type mcpModemItem struct {
	ICCID          string              `json:"iccid"`
	Name           string              `json:"name,omitempty"`
	IMEI           string              `json:"imei,omitempty"`
	Operator       string              `json:"operator,omitempty"`
	SignalStrength int                 `json:"signal_strength"`
	PortName       string              `json:"port_name,omitempty"`
	Status         string              `json:"status"`
	WorkerExists   bool                `json:"worker_exists"`
	Busy           bool                `json:"busy"`
	UACReady       bool                `json:"uac_ready"`
	Permissions    mcpModemPermissions `json:"permissions"`
}

type mcpListModemsOutput struct {
	Data       []mcpModemItem `json:"data"`
	Permission string         `json:"permission"`
	Total      int            `json:"total"`
}

type mcpListSMSInput struct {
	ICCID      string `json:"iccid,omitempty" jsonschema:"optional ICCID filter"`
	Page       int    `json:"page,omitempty" jsonschema:"page number starting from 1"`
	PageSize   int    `json:"page_size,omitempty" jsonschema:"records per page, max 100"`
	MaxRecords int    `json:"max_records,omitempty" jsonschema:"maximum visible result window, max 500"`
	Type       string `json:"type,omitempty" jsonschema:"message type filter: all, received, sent"`
}

type mcpListSMSOutput struct {
	Data           []model.SMS `json:"data"`
	Page           int         `json:"page"`
	PageSize       int         `json:"page_size"`
	MaxRecords     int         `json:"max_records"`
	Total          int         `json:"total"`
	TotalAvailable int64       `json:"total_available"`
	Returned       int         `json:"returned"`
	HasMore        bool        `json:"has_more"`
	ICCID          string      `json:"iccid,omitempty"`
	Type           string      `json:"type,omitempty"`
}

type mcpWaitSMSInput struct {
	ICCID      string `json:"iccid,omitempty" jsonschema:"optional ICCID filter"`
	AfterID    *int   `json:"after_id,omitempty" jsonschema:"return messages with id greater than this value; omit to wait for messages newer than the current latest visible SMS"`
	TimeoutSec int    `json:"timeout_sec,omitempty" jsonschema:"wait timeout in seconds, max 120"`
	MaxRecords int    `json:"max_records,omitempty" jsonschema:"maximum number of SMS records to return, max 100"`
	Type       string `json:"type,omitempty" jsonschema:"message type filter: all, received, sent; defaults to received"`
}

type mcpWaitSMSOutput struct {
	Data        []model.SMS `json:"data"`
	Returned    int         `json:"returned"`
	Timeout     bool        `json:"timeout"`
	AfterID     int         `json:"after_id"`
	NextAfterID int         `json:"next_after_id"`
	TimeoutSec  int         `json:"timeout_sec"`
	ICCID       string      `json:"iccid,omitempty"`
	Type        string      `json:"type,omitempty"`
}

type mcpSendSMSInput struct {
	ICCID   string `json:"iccid" jsonschema:"ICCID of the modem that should send the SMS"`
	Phone   string `json:"phone" jsonschema:"destination phone number"`
	Message string `json:"message" jsonschema:"SMS body"`
}

type mcpSendSMSOutput struct {
	Status  string `json:"status"`
	ICCID   string `json:"iccid"`
	Phone   string `json:"phone"`
	Message string `json:"message"`
}

func NewMCPHTTPServer(db *gorm.DB, wm *worker.Manager) *MCPHTTPServer {
	s := &MCPHTTPServer{db: db, wm: wm}
	s.server = sdkmcp.NewServer(&sdkmcp.Implementation{Name: "smsie", Version: "v2"}, &sdkmcp.ServerOptions{
		Instructions: "Use the provided SMS and modem tools. All results are automatically constrained by the authenticated API key and modem permissions.",
	})

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "list_modems",
		Description: "List active online modems visible to the authenticated API key. Use the permission filter to limit the results to modems that can perform a specific action.",
	}, s.toolListModems)
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "list_sms",
		Description: "List SMS messages visible to the authenticated API key with pagination and max_records bounds.",
	}, s.toolListSMS)
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "wait_sms",
		Description: "Wait for new SMS messages visible to the authenticated API key. Use after_id to continue from the previous result.",
	}, s.toolWaitSMS)
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "send_sms",
		Description: "Send an SMS through a specific modem that the authenticated API key is allowed to use.",
	}, s.toolSendSMS)

	baseHandler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		return s.server
	}, &sdkmcp.StreamableHTTPOptions{
		SessionTimeout: 10 * time.Minute,
	})

	attachActor := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenInfo := sdkauth.TokenInfoFromContext(r.Context())
		if tokenInfo == nil || tokenInfo.Extra == nil {
			http.Error(w, "missing token info", http.StatusUnauthorized)
			return
		}
		actor, ok := tokenInfo.Extra["actor"].(*authActor)
		if !ok || actor == nil || actor.User == nil || actor.APIKey == nil {
			http.Error(w, "missing authenticated actor", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), mcpActorContextKey{}, actor)
		baseHandler.ServeHTTP(w, r.WithContext(ctx))
	})

	s.handler = sdkauth.RequireBearerToken(s.verifyAPIKeyToken, nil)(attachActor)
	return s
}

func (s *MCPHTTPServer) Handler() http.Handler {
	return s.handler
}

func (s *MCPHTTPServer) verifyAPIKeyToken(ctx context.Context, token string, req *http.Request) (*sdkauth.TokenInfo, error) {
	token = strings.TrimSpace(token)
	if token == "" || !isSMSIEAPIKey(token) {
		return nil, fmt.Errorf("%w: smsie api key required", sdkauth.ErrInvalidToken)
	}

	var key model.APIKey
	if err := s.db.Where("key_hash = ? AND is_active = ?", hashAPIKey(token), true).First(&key).Error; err != nil {
		return nil, fmt.Errorf("%w: invalid API key", sdkauth.ErrInvalidToken)
	}

	now := time.Now()
	if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
		return nil, fmt.Errorf("%w: API key expired", sdkauth.ErrInvalidToken)
	}

	var user model.User
	if err := s.db.First(&user, key.UserID).Error; err != nil {
		return nil, fmt.Errorf("%w: user not found", sdkauth.ErrInvalidToken)
	}

	_ = s.db.Model(&model.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", now).Error

	expiresAt := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	if key.ExpiresAt != nil {
		expiresAt = *key.ExpiresAt
	}

	actor := &authActor{User: &user, APIKey: &key}
	return &sdkauth.TokenInfo{
		UserID:     fmt.Sprintf("apikey:%d", key.ID),
		Expiration: expiresAt,
		Extra: map[string]any{
			"actor": actor,
		},
	}, nil
}

func getMCPActor(ctx context.Context) (*authActor, error) {
	actor, ok := ctx.Value(mcpActorContextKey{}).(*authActor)
	if !ok || actor == nil || actor.User == nil || actor.APIKey == nil {
		return nil, errors.New("authenticated API key context missing")
	}
	return actor, nil
}

func clampInt(value, fallback, minValue, maxValue int) int {
	if value <= 0 {
		value = fallback
	}
	if value < minValue {
		value = minValue
	}
	if maxValue > 0 && value > maxValue {
		value = maxValue
	}
	return value
}

func normalizeMCPPermission(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "any":
		return "", nil
	case PermMakeCall:
		return PermMakeCall, nil
	case PermViewSMS:
		return PermViewSMS, nil
	case PermSendSMS:
		return PermSendSMS, nil
	case PermSendAT:
		return PermSendAT, nil
	default:
		return "", fmt.Errorf("permission must be one of any, %s, %s, %s, %s", PermMakeCall, PermViewSMS, PermSendSMS, PermSendAT)
	}
}

func normalizeSMSType(raw string, defaultValue string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return defaultValue, nil
	}
	switch value {
	case "all":
		return "", nil
	case "sent", "received":
		return value, nil
	default:
		return "", fmt.Errorf("type must be one of all, sent, received")
	}
}

func hydrateSMSContent(db *gorm.DB, smsList []model.SMS) {
	for i, s := range smsList {
		if s.Content != "" || s.Phone != "" {
			continue
		}

		d, _ := hex.DecodeString(s.RawPDU)
		if len(d) > 0 {
			smscLen := int(d[0])
			if len(d) > smscLen+1 {
				d = d[smscLen+1:]
			}
		}

		msg, err := sms.Unmarshal(d)
		if err != nil {
			logger.Log.Warnf("Failed to unmarshal sms pdu: %v", err)
			continue
		}

		content := ""
		alphabet, alphaErr := msg.DCS.Alphabet()
		var udContent []byte
		var decErr error
		if alphaErr != nil {
			decErr = alphaErr
		} else {
			udContent, decErr = tpdu.DecodeUserData(msg.UD, msg.UDH, alphabet)
		}

		if decErr == nil {
			content = string(udContent)
		} else {
			logger.Log.Warnf("Failed to decode UD: %v. DCS: %02X.", decErr, msg.DCS)
			content = fmt.Sprintf("Decode Failed (DCS: 0x%02X)", msg.DCS)
		}
		if content == "" && len(msg.UD) > 0 {
			content = fmt.Sprintf("UD Hex: %X", msg.UD)
		}

		smsList[i].Content = content
		smsList[i].Phone = msg.OA.Number()
		_ = db.Updates(&smsList[i]).Error
	}
}

func (s *MCPHTTPServer) scopedSMSQuery(actor *authActor, iccid, smsType string) (*gorm.DB, error) {
	query := s.db.Model(&model.SMS{})
	if smsType != "" {
		query = query.Where("type = ?", smsType)
	}

	if iccid != "" {
		allowed, _, message := actorCanAccessICCIDPermission(s.db, actor, iccid, PermViewSMS)
		if !allowed {
			return nil, errors.New(message)
		}
		return query.Where("iccid = ?", iccid), nil
	}

	allowedICCIDs, err := allowedICCIDsForPermission(s.db, actor.User, PermViewSMS)
	if err != nil {
		return nil, fmt.Errorf("permission check failed: %w", err)
	}

	if actor.User.Role != "admin" {
		if len(allowedICCIDs) == 0 {
			query = query.Where("1 = 0")
		} else if !hasWildcardICCID(allowedICCIDs) {
			query = query.Where("iccid IN ?", allowedICCIDs)
		}
	}
	return query, nil
}

func (s *MCPHTTPServer) toolListModems(ctx context.Context, req *sdkmcp.CallToolRequest, input mcpListModemsInput) (*sdkmcp.CallToolResult, mcpListModemsOutput, error) {
	actor, err := getMCPActor(ctx)
	if err != nil {
		return nil, mcpListModemsOutput{}, err
	}

	perm, err := normalizeMCPPermission(input.Permission)
	if err != nil {
		return nil, mcpListModemsOutput{}, err
	}
	if !permissionFlagFromKey(actor.APIKey, perm) {
		return nil, mcpListModemsOutput{}, errors.New("API key permission denied")
	}

	allowedICCIDs, err := allowedICCIDsForPermission(s.db, actor.User, perm)
	if err != nil {
		return nil, mcpListModemsOutput{}, fmt.Errorf("permission check failed: %w", err)
	}

	query := s.db.Model(&model.Modem{})
	if actor.User.Role != "admin" {
		if len(allowedICCIDs) == 0 {
			query = query.Where("1 = 0")
		} else if !hasWildcardICCID(allowedICCIDs) {
			query = query.Where("iccid IN ?", allowedICCIDs)
		}
	}

	var modems []model.Modem
	if err := query.Order("iccid asc").Find(&modems).Error; err != nil {
		return nil, mcpListModemsOutput{}, err
	}

	out := mcpListModemsOutput{Permission: perm}
	for _, modem := range modems {
		w := s.wm.GetWorkerByICCID(modem.ICCID)
		if w == nil {
			continue
		}
		rt, hasRuntime := w.RuntimeModemState()
		modem = modemWithRuntimeState(modem, rt, hasRuntime)
		if !hasRuntime || strings.ToLower(strings.TrimSpace(modem.Status)) != "online" {
			continue
		}
		if perm == PermMakeCall && !w.IsUACReady() {
			continue
		}

		canViewSMS, _, _ := actorCanAccessICCIDPermission(s.db, actor, modem.ICCID, PermViewSMS)
		canSendSMS, _, _ := actorCanAccessICCIDPermission(s.db, actor, modem.ICCID, PermSendSMS)
		canSendAT, _, _ := actorCanAccessICCIDPermission(s.db, actor, modem.ICCID, PermSendAT)
		canMakeCall, _, _ := actorCanAccessICCIDPermission(s.db, actor, modem.ICCID, PermMakeCall)

		out.Data = append(out.Data, mcpModemItem{
			ICCID:          modem.ICCID,
			Name:           modem.Name,
			IMEI:           modem.IMEI,
			Operator:       modem.Operator,
			SignalStrength: modem.SignalStrength,
			PortName:       modem.PortName,
			Status:         modem.Status,
			WorkerExists:   true,
			Busy:           w.IsBusy(),
			UACReady:       w.IsUACReady(),
			Permissions: mcpModemPermissions{
				CanViewSMS:  canViewSMS,
				CanSendSMS:  canSendSMS,
				CanSendAT:   canSendAT,
				CanMakeCall: canMakeCall,
			},
		})
	}
	out.Total = len(out.Data)
	return nil, out, nil
}

func (s *MCPHTTPServer) toolListSMS(ctx context.Context, req *sdkmcp.CallToolRequest, input mcpListSMSInput) (*sdkmcp.CallToolResult, mcpListSMSOutput, error) {
	actor, err := getMCPActor(ctx)
	if err != nil {
		return nil, mcpListSMSOutput{}, err
	}
	if !actor.APIKey.CanViewSMS {
		return nil, mcpListSMSOutput{}, errors.New("API key permission denied")
	}

	smsType, err := normalizeSMSType(input.Type, "")
	if err != nil {
		return nil, mcpListSMSOutput{}, err
	}
	page := clampInt(input.Page, 1, 1, 1000000)
	pageSize := clampInt(input.PageSize, defaultMCPPageSize, 1, maxMCPPageSize)
	maxRecords := clampInt(input.MaxRecords, defaultMCPMaxRecords, 1, maxMCPMaxRecords)

	query, err := s.scopedSMSQuery(actor, strings.TrimSpace(input.ICCID), smsType)
	if err != nil {
		return nil, mcpListSMSOutput{}, err
	}

	var totalAvailable int64
	if err := query.Count(&totalAvailable).Error; err != nil {
		return nil, mcpListSMSOutput{}, err
	}

	total := int(totalAvailable)
	if total > maxRecords {
		total = maxRecords
	}
	offset := (page - 1) * pageSize
	smsList := []model.SMS{}
	if offset < total {
		effectiveLimit := pageSize
		remaining := total - offset
		if effectiveLimit > remaining {
			effectiveLimit = remaining
		}
		if err := query.Order("timestamp desc").Order("id desc").Limit(effectiveLimit).Offset(offset).Find(&smsList).Error; err != nil {
			return nil, mcpListSMSOutput{}, err
		}
		hydrateSMSContent(s.db, smsList)
	}

	return nil, mcpListSMSOutput{
		Data:           smsList,
		Page:           page,
		PageSize:       pageSize,
		MaxRecords:     maxRecords,
		Total:          total,
		TotalAvailable: totalAvailable,
		Returned:       len(smsList),
		HasMore:        offset+len(smsList) < total,
		ICCID:          strings.TrimSpace(input.ICCID),
		Type:           smsType,
	}, nil
}

func (s *MCPHTTPServer) toolWaitSMS(ctx context.Context, req *sdkmcp.CallToolRequest, input mcpWaitSMSInput) (*sdkmcp.CallToolResult, mcpWaitSMSOutput, error) {
	actor, err := getMCPActor(ctx)
	if err != nil {
		return nil, mcpWaitSMSOutput{}, err
	}
	if !actor.APIKey.CanViewSMS {
		return nil, mcpWaitSMSOutput{}, errors.New("API key permission denied")
	}

	smsType, err := normalizeSMSType(input.Type, "received")
	if err != nil {
		return nil, mcpWaitSMSOutput{}, err
	}
	timeoutSec := clampInt(input.TimeoutSec, defaultMCPWaitTimeoutSec, 1, maxMCPWaitTimeoutSec)
	maxRecords := clampInt(input.MaxRecords, defaultMCPWaitMaxRecords, 1, maxMCPWaitMaxRecords)
	iccid := strings.TrimSpace(input.ICCID)

	query, err := s.scopedSMSQuery(actor, iccid, smsType)
	if err != nil {
		return nil, mcpWaitSMSOutput{}, err
	}

	afterID := 0
	if input.AfterID != nil {
		if *input.AfterID < 0 {
			return nil, mcpWaitSMSOutput{}, errors.New("after_id must be >= 0")
		}
		afterID = *input.AfterID
	} else {
		var latest model.SMS
		err := query.Order("id desc").Limit(1).Take(&latest).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, mcpWaitSMSOutput{}, err
		}
		if err == nil {
			afterID = int(latest.ID)
		}
	}

	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		pollQuery, err := s.scopedSMSQuery(actor, iccid, smsType)
		if err != nil {
			return nil, mcpWaitSMSOutput{}, err
		}

		smsList := []model.SMS{}
		if err := pollQuery.Where("id > ?", afterID).Order("id asc").Limit(maxRecords).Find(&smsList).Error; err != nil {
			return nil, mcpWaitSMSOutput{}, err
		}
		if len(smsList) > 0 {
			hydrateSMSContent(s.db, smsList)
			nextAfterID := afterID
			if lastID := int(smsList[len(smsList)-1].ID); lastID > nextAfterID {
				nextAfterID = lastID
			}
			return nil, mcpWaitSMSOutput{
				Data:        smsList,
				Returned:    len(smsList),
				Timeout:     false,
				AfterID:     afterID,
				NextAfterID: nextAfterID,
				TimeoutSec:  timeoutSec,
				ICCID:       iccid,
				Type:        smsType,
			}, nil
		}

		if time.Now().After(deadline) {
			return nil, mcpWaitSMSOutput{
				Data:        []model.SMS{},
				Returned:    0,
				Timeout:     true,
				AfterID:     afterID,
				NextAfterID: afterID,
				TimeoutSec:  timeoutSec,
				ICCID:       iccid,
				Type:        smsType,
			}, nil
		}

		select {
		case <-ctx.Done():
			return nil, mcpWaitSMSOutput{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *MCPHTTPServer) toolSendSMS(ctx context.Context, req *sdkmcp.CallToolRequest, input mcpSendSMSInput) (*sdkmcp.CallToolResult, mcpSendSMSOutput, error) {
	actor, err := getMCPActor(ctx)
	if err != nil {
		return nil, mcpSendSMSOutput{}, err
	}
	if !actor.APIKey.CanSendSMS {
		return nil, mcpSendSMSOutput{}, errors.New("API key permission denied")
	}

	iccid := strings.TrimSpace(input.ICCID)
	if iccid == "" {
		return nil, mcpSendSMSOutput{}, errors.New("iccid is required")
	}
	allowed, _, message := actorCanAccessICCIDPermission(s.db, actor, iccid, PermSendSMS)
	if !allowed {
		return nil, mcpSendSMSOutput{}, errors.New(message)
	}
	if strings.TrimSpace(input.Phone) == "" {
		return nil, mcpSendSMSOutput{}, errors.New("phone is required")
	}
	if strings.TrimSpace(input.Message) == "" {
		return nil, mcpSendSMSOutput{}, errors.New("message is required")
	}

	w := s.wm.GetWorkerByICCID(iccid)
	if w == nil {
		return nil, mcpSendSMSOutput{}, errors.New("modem not active (worker not found)")
	}
	if w.IsBusy() {
		return nil, mcpSendSMSOutput{}, errors.New("modem is busy")
	}
	if err := w.SendSMS(input.Phone, input.Message); err != nil {
		return nil, mcpSendSMSOutput{}, fmt.Errorf("send SMS failed: %w", err)
	}

	return nil, mcpSendSMSOutput{
		Status:  "ok",
		ICCID:   iccid,
		Phone:   input.Phone,
		Message: "SMS sent successfully",
	}, nil
}
