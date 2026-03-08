//go:build !nouac

package calling

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var (
	errSIPClientDisabled  = errors.New("sip client disabled")
	errSIPInvalidNumber   = errors.New("invalid dial number")
	errSIPCallInProgress  = errors.New("call already in progress")
	errSIPNoActiveCall    = errors.New("no active sip call")
	errSIPAudioNotReady   = errors.New("audio bridge not initialized")
	errSIPInboundNotReady = errors.New("sip inbound gateway not initialized")
	sipDialNumberPattern  = regexp.MustCompile(`^[0-9*#+]+$`)
	defaultDTMFDurationMs = 160
)

func IsSIPCallInProgressError(err error) bool {
	return errors.Is(err, errSIPCallInProgress)
}

func IsSIPInvalidDialNumberError(err error) bool {
	return errors.Is(err, errSIPInvalidNumber)
}

func IsSIPNoActiveCallError(err error) bool {
	return errors.Is(err, errSIPNoActiveCall)
}

type sipClientCall struct {
	cfg             SIPConfig
	logger          *log.Logger
	peer            *WebRTCPeer
	bridge          *AudioBridge
	onEnded         func(reason string)
	transport       string
	proxyHost       string
	proxyPort       int
	proxyAddr       string
	domain          string
	localSignalHost string
	localSignalPort int
	localMediaIP    string

	ua         *sipgo.UserAgent
	client     *sipgo.Client
	server     *sipgo.Server
	listener   io.Closer
	dialogPool *sipgo.DialogClientCache
	dialog     *sipgo.DialogClientSession
	dialogUAS  *sipgo.DialogServerSession

	rtpConn        *net.UDPConn
	rtpRemote      *net.UDPAddr
	rtpPayloadType uint8
	rtpCodec       string
	rtpSSRC        uint32
	rtpSeq         uint16
	rtpTimestamp   uint32

	writeMu   sync.Mutex
	closeOnce sync.Once
}

type SIPInboundHooks struct {
	ICCID        string
	ResolveModem func() (iccid string, target ModemTarget, err error)
	DialModem    func(iccid, number string) error
	HangupModem  func(iccid string) error
	SendDTMF     func(iccid, tone string) error
}

type SIPInboundLineInfo struct {
	LineID         string
	ICCID          string
	LocalPort      int
	Transport      string
	Active         bool
	RegisterState  string
	RegisterReason string
	UpdatedAt      time.Time
}

type sipInboundLeg struct {
	dialogID string
	iccid    string
	call     *sipClientCall
}

type sipInboundGateway struct {
	lineID    string
	lineICCID string
	manager   *Manager
	cfg       SIPConfig
	logger    *log.Logger
	hooks     SIPInboundHooks

	transport       string
	proxyHost       string
	proxyPort       int
	proxyAddr       string
	domain          string
	localSignalHost string
	localSignalPort int
	localMediaIP    string

	ua         *sipgo.UserAgent
	client     *sipgo.Client
	server     *sipgo.Server
	listener   io.Closer
	dialogPool *sipgo.DialogServerCache

	registerCancel context.CancelFunc
	registerState  string
	registerReason string
	registerAt     time.Time

	mu     sync.Mutex
	active *sipInboundLeg
}

func (m *Manager) SIPEnabled() bool {
	return m != nil
}

func (m *Manager) SIPTransport() string {
	if m == nil {
		return ""
	}
	return normalizeSIPTransport(m.cfg.SIP.Transport)
}

func (m *Manager) StartSIPInbound(lineID string, localPort int, hooks SIPInboundHooks) error {
	if m == nil || !m.SIPEnabled() {
		return errSIPClientDisabled
	}
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("sip inbound line id is required")
	}
	if hooks.ResolveModem == nil || hooks.DialModem == nil {
		return errors.New("sip inbound hooks are incomplete")
	}

	m.sipGatewayMu.Lock()
	defer m.sipGatewayMu.Unlock()
	if _, ok := m.sipGateways[lineID]; ok {
		return nil
	}

	cfg := m.cfg.SIP
	if localPort > 0 {
		cfg.LocalPort = localPort
	}

	gw, err := newSIPInboundGateway(lineID, m, cfg, m.logger, hooks)
	if err != nil {
		return err
	}
	m.sipGateways[lineID] = gw
	return nil
}

func (m *Manager) SyncSIPInbound(lineID string, cfg SIPConfig, hooks SIPInboundHooks) error {
	if m == nil {
		return errSIPClientDisabled
	}
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return errors.New("sip inbound line id is required")
	}

	m.sipGatewayMu.Lock()
	existing := m.sipGateways[lineID]
	if existing != nil && sipInboundGatewayMatches(existing, cfg, hooks) {
		m.sipGatewayMu.Unlock()
		return nil
	}
	if existing != nil {
		delete(m.sipGateways, lineID)
	}
	m.sipGatewayMu.Unlock()

	if existing != nil {
		_ = existing.Close()
	}

	gw, err := newSIPInboundGateway(lineID, m, cfg, m.logger, hooks)
	if err != nil {
		return err
	}

	m.sipGatewayMu.Lock()
	m.sipGateways[lineID] = gw
	m.sipGatewayMu.Unlock()
	return nil
}

func (m *Manager) StopSIPInboundLine(lineID string) error {
	if m == nil {
		return nil
	}
	lineID = strings.TrimSpace(lineID)
	if lineID == "" {
		return nil
	}

	m.sipGatewayMu.Lock()
	gw := m.sipGateways[lineID]
	delete(m.sipGateways, lineID)
	m.sipGatewayMu.Unlock()
	if gw == nil {
		return nil
	}
	return gw.Close()
}

func (m *Manager) PruneSIPInboundLines(active map[string]struct{}) error {
	if m == nil {
		return nil
	}

	m.sipGatewayMu.Lock()
	stale := make([]*sipInboundGateway, 0)
	for lineID, gw := range m.sipGateways {
		if _, ok := active[lineID]; ok {
			continue
		}
		stale = append(stale, gw)
		delete(m.sipGateways, lineID)
	}
	m.sipGatewayMu.Unlock()

	var closeErr error
	for _, gw := range stale {
		if gw == nil {
			continue
		}
		if err := gw.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (m *Manager) StopSIPInbound() error {
	if m == nil {
		return nil
	}

	m.sipGatewayMu.Lock()
	gateways := make([]*sipInboundGateway, 0, len(m.sipGateways))
	for key, gw := range m.sipGateways {
		gateways = append(gateways, gw)
		delete(m.sipGateways, key)
	}
	m.sipGatewayMu.Unlock()

	var closeErr error
	for _, gw := range gateways {
		if gw == nil {
			continue
		}
		if err := gw.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (m *Manager) SIPCallState(iccid string) (state, reason string, updatedAt time.Time, ok bool) {
	s := m.GetSession(iccid)
	if s == nil {
		return "", "", time.Time{}, false
	}
	state, reason, updatedAt = s.getCallState()
	return state, reason, updatedAt, true
}

func (m *Manager) SIPInboundLineInfo(iccid string) (SIPInboundLineInfo, bool) {
	if m == nil {
		return SIPInboundLineInfo{}, false
	}

	m.sipGatewayMu.Lock()
	defer m.sipGatewayMu.Unlock()
	for _, gateway := range m.sipGateways {
		if gateway == nil || gateway.lineICCID != iccid {
			continue
		}
		return SIPInboundLineInfo{
			LineID:         gateway.lineID,
			ICCID:          gateway.lineICCID,
			LocalPort:      gateway.localSignalPort,
			Transport:      gateway.transport,
			Active:         gateway.listener != nil,
			RegisterState:  gateway.registerState,
			RegisterReason: gateway.registerReason,
			UpdatedAt:      gateway.registerAt,
		}, true
	}
	return SIPInboundLineInfo{}, false
}

func (m *Manager) SIPConfigForICCID(iccid string) (SIPConfig, bool) {
	if m == nil {
		return SIPConfig{}, false
	}
	m.sipGatewayMu.Lock()
	defer m.sipGatewayMu.Unlock()
	for _, gateway := range m.sipGateways {
		if gateway == nil || gateway.lineICCID != iccid {
			continue
		}
		return gateway.cfg, true
	}
	return SIPConfig{}, false
}

func (m *Manager) DialSIP(iccid, number string) error {
	if m == nil || !m.SIPEnabled() {
		return errSIPClientDisabled
	}

	s := m.GetSession(iccid)
	if s == nil {
		return errors.New("calling session not initialized")
	}
	if err := m.EnsureAudio(iccid); err != nil {
		return fmt.Errorf("audio init failed: %w", err)
	}

	number = strings.TrimSpace(number)
	if number == "" || !sipDialNumberPattern.MatchString(number) {
		return errSIPInvalidNumber
	}

	s.sipMu.Lock()
	if s.sipCall != nil {
		s.sipMu.Unlock()
		return errSIPCallInProgress
	}

	sipCfg, ok := m.SIPConfigForICCID(iccid)
	if !ok {
		return errors.New("sip client not configured for modem")
	}
	m.sipGatewayMu.Lock()
	if len(m.sipGateways) > 0 && sipCfg.LocalPort > 0 {
		sipCfg.LocalPort = sipCfg.LocalPort + len(m.sipGateways) + 1
	}
	m.sipGatewayMu.Unlock()

	var created *sipClientCall
	call, err := newSIPClientCall(sipCfg, m.logger, s, func(reason string) {
		s.sipMu.Lock()
		if s.sipCall == created {
			s.sipCall = nil
		}
		s.sipMu.Unlock()
		s.setCallState("idle", reason)
	})
	if err != nil {
		s.sipMu.Unlock()
		return err
	}
	created = call
	s.sipCall = call
	s.sipMu.Unlock()

	s.setCallState("dialing", "sip_invite")

	inviteTimeout := time.Duration(m.cfg.SIP.InviteTimeoutSec) * time.Second
	if inviteTimeout <= 0 {
		inviteTimeout = 30 * time.Second
	}

	if err := call.Dial(number, inviteTimeout); err != nil {
		call.Close("dial_error")
		return err
	}

	s.setCallState("in_call", "sip_connected")
	return nil
}

func (m *Manager) HangupSIP(iccid string) error {
	if m == nil || !m.SIPEnabled() {
		return errSIPClientDisabled
	}

	s := m.GetSession(iccid)
	if s == nil {
		return errSIPNoActiveCall
	}

	s.sipMu.Lock()
	call := s.sipCall
	s.sipMu.Unlock()
	if call == nil {
		return errSIPNoActiveCall
	}

	if err := call.Hangup(); err != nil {
		return err
	}
	return nil
}

func (m *Manager) SendSIPDTMF(iccid, tone string) error {
	if m == nil || !m.SIPEnabled() {
		return errSIPClientDisabled
	}

	s := m.GetSession(iccid)
	if s == nil {
		return errSIPNoActiveCall
	}

	s.sipMu.Lock()
	call := s.sipCall
	s.sipMu.Unlock()
	if call == nil {
		return errSIPNoActiveCall
	}

	return call.SendDTMF(strings.TrimSpace(tone))
}

func (m *Manager) HasActiveSIPCall(iccid string) bool {
	if m == nil {
		return false
	}
	s := m.GetSession(iccid)
	if s == nil {
		return false
	}

	s.sipMu.Lock()
	defer s.sipMu.Unlock()
	return s.sipCall != nil
}

func (s *Session) closeSIP(reason string) {
	s.sipMu.Lock()
	call := s.sipCall
	s.sipCall = nil
	s.sipMu.Unlock()
	if call != nil {
		call.Close(reason)
	}
}

func newSIPInboundGateway(lineID string, manager *Manager, cfg SIPConfig, logger *log.Logger, hooks SIPInboundHooks) (*sipInboundGateway, error) {
	transport := normalizeSIPTransport(cfg.Transport)
	proxyHost, proxyPort, err := parseProxyHostPort(cfg.Proxy, cfg.Port, transport)
	if err != nil {
		return nil, err
	}

	localSignalHost := strings.TrimSpace(cfg.LocalHost)
	if localSignalHost == "" {
		localSignalHost = detectLocalIP(proxyHost)
	}
	if localSignalHost == "" {
		localSignalHost = "127.0.0.1"
	}

	localSignalPort := cfg.LocalPort
	if localSignalPort <= 0 {
		localSignalPort = 5062
	}

	localMediaIP := strings.TrimSpace(cfg.RTPBindIP)
	if localMediaIP == "" || localMediaIP == "0.0.0.0" {
		localMediaIP = localSignalHost
	}
	if net.ParseIP(localMediaIP) == nil {
		localMediaIP = "127.0.0.1"
	}

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = proxyHost
	}

	uaOpts := []sipgo.UserAgentOption{}
	if transport == "tls" {
		uaOpts = append(uaOpts, sipgo.WithUserAgenTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify,
		}))
	}

	ua, err := sipgo.NewUA(uaOpts...)
	if err != nil {
		return nil, err
	}

	clientAddr := net.JoinHostPort(localSignalHost, strconv.Itoa(localSignalPort))
	client, err := sipgo.NewClient(ua, sipgo.WithClientAddr(clientAddr))
	if err != nil {
		_ = ua.Close()
		return nil, err
	}

	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = client.Close()
		_ = ua.Close()
		return nil, err
	}

	contact := sip.ContactHeader{
		Address: sip.Uri{
			User: cfg.Username,
			Host: localSignalHost,
			Port: localSignalPort,
		},
	}

	gw := &sipInboundGateway{
		lineID:          lineID,
		lineICCID:       strings.TrimSpace(hooks.ICCID),
		manager:         manager,
		cfg:             cfg,
		logger:          logger,
		hooks:           hooks,
		transport:       transport,
		proxyHost:       proxyHost,
		proxyPort:       proxyPort,
		proxyAddr:       net.JoinHostPort(proxyHost, strconv.Itoa(proxyPort)),
		domain:          domain,
		localSignalHost: localSignalHost,
		localSignalPort: localSignalPort,
		localMediaIP:    localMediaIP,
		ua:              ua,
		client:          client,
		server:          server,
		dialogPool:      sipgo.NewDialogServerCache(client, contact),
		registerState:   "idle",
		registerReason:  "init",
		registerAt:      time.Now(),
	}

	server.OnInvite(gw.onInvite)
	server.OnAck(gw.onAck)
	server.OnBye(gw.onBye)
	server.OnInfo(gw.onInfo)

	if err := gw.startServerListener(); err != nil {
		gw.Close()
		return nil, fmt.Errorf("start sip inbound listener [%s] failed: %w", lineID, err)
	}

	if cfg.Register {
		gw.startRegisterLoop()
	}

	logger.Printf("sip inbound line [%s] started on %s:%d (%s)", lineID, localSignalHost, localSignalPort, transport)
	return gw, nil
}

func sipInboundGatewayMatches(gw *sipInboundGateway, cfg SIPConfig, hooks SIPInboundHooks) bool {
	if gw == nil {
		return false
	}
	return gw.lineICCID == strings.TrimSpace(hooks.ICCID) && sipConfigSignature(gw.cfg) == sipConfigSignature(cfg)
}

func sipConfigSignature(cfg SIPConfig) string {
	return strings.Join([]string{
		strings.TrimSpace(cfg.Username),
		strings.TrimSpace(cfg.Password),
		strings.TrimSpace(cfg.Proxy),
		strconv.Itoa(cfg.Port),
		strings.TrimSpace(cfg.Domain),
		normalizeSIPTransport(cfg.Transport),
		strconv.FormatBool(cfg.TLSSkipVerify),
		strconv.FormatBool(cfg.Register),
		strconv.Itoa(cfg.RegisterExpires),
		strings.TrimSpace(cfg.LocalHost),
		strconv.Itoa(cfg.LocalPort),
		strings.TrimSpace(cfg.RTPBindIP),
		strconv.Itoa(cfg.RTPPortMin),
		strconv.Itoa(cfg.RTPPortMax),
		strconv.Itoa(cfg.InviteTimeoutSec),
		strings.TrimSpace(cfg.DTMFMethod),
		strconv.Itoa(cfg.DTMFDurationMillis),
	}, "|")
}

func (g *sipInboundGateway) startRegisterLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	g.registerCancel = cancel
	g.setRegisterStatus("connecting", "registering")

	go func() {
		for {
			if err := g.register(ctx); err != nil {
				g.setRegisterStatus("error", err.Error())
				g.logger.Printf("sip register failed: %v", err)
			} else {
				g.setRegisterStatus("registered", "ok")
			}

			waitSec := g.cfg.RegisterExpires - 30
			if waitSec < 30 {
				waitSec = 30
			}

			timer := time.NewTimer(time.Duration(waitSec) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (g *sipInboundGateway) setRegisterStatus(state, reason string) {
	g.mu.Lock()
	g.registerState = strings.TrimSpace(state)
	g.registerReason = strings.TrimSpace(reason)
	g.registerAt = time.Now()
	g.mu.Unlock()
}

func (g *sipInboundGateway) register(ctx context.Context) error {
	recipient := sip.Uri{
		Scheme: "sip",
		User:   g.cfg.Username,
		Host:   g.domain,
	}

	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport(strings.ToUpper(g.transport))
	req.SetDestination(g.proxyAddr)
	req.AppendHeader(&sip.ContactHeader{
		Address: sip.Uri{
			User: g.cfg.Username,
			Host: g.localSignalHost,
			Port: g.localSignalPort,
		},
	})

	expires := g.cfg.RegisterExpires
	if expires <= 0 {
		expires = 300
	}
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := g.client.Do(callCtx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return err
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		res, err = g.client.DoDigestAuth(callCtx, req, res, sipgo.DigestAuth{
			Username: g.cfg.Username,
			Password: g.cfg.Password,
		})
		if err != nil {
			return err
		}
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("register failed: %d %s", res.StatusCode, strings.TrimSpace(res.Reason))
	}
	return nil
}

func (g *sipInboundGateway) startServerListener() error {
	listenAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(g.localSignalPort))

	switch g.transport {
	case "udp":
		pc, err := net.ListenPacket("udp", listenAddr)
		if err != nil {
			return err
		}
		g.listener = pc
		go func() {
			if err := g.server.ServeUDP(pc); err != nil && !isNetClosedError(err) {
				g.logger.Printf("sip inbound udp listener stopped: %v", err)
			}
		}()
	case "tcp":
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return err
		}
		g.listener = ln
		go func() {
			if err := g.server.ServeTCP(ln); err != nil && !isNetClosedError(err) {
				g.logger.Printf("sip inbound tcp listener stopped: %v", err)
			}
		}()
	case "tls":
		ln, err := newSIPTLSListener(listenAddr, g.localSignalHost, g.localMediaIP)
		if err != nil {
			return err
		}
		g.listener = ln
		go func() {
			if err := g.server.ServeTLS(ln); err != nil && !isNetClosedError(err) {
				g.logger.Printf("sip inbound tls listener stopped: %v", err)
			}
		}()
	default:
		return fmt.Errorf("unsupported sip transport: %s", g.transport)
	}
	return nil
}

func (g *sipInboundGateway) Close() error {
	if g == nil {
		return nil
	}

	if g.registerCancel != nil {
		g.registerCancel()
		g.registerCancel = nil
	}

	g.mu.Lock()
	active := g.active
	g.active = nil
	g.mu.Unlock()
	if active != nil && active.call != nil {
		active.call.Close("sip_gateway_closed")
	}

	if g.listener != nil {
		_ = g.listener.Close()
		g.listener = nil
	}
	if g.server != nil {
		_ = g.server.Close()
		g.server = nil
	}
	if g.client != nil {
		_ = g.client.Close()
		g.client = nil
	}
	if g.ua != nil {
		_ = g.ua.Close()
		g.ua = nil
	}
	return nil
}

func (g *sipInboundGateway) onInvite(req *sip.Request, tx sip.ServerTransaction) {
	number := strings.TrimSpace(extractInviteNumber(req))
	if number == "" || !sipDialNumberPattern.MatchString(number) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Invalid dial number", nil))
		return
	}

	iccid, target, err := g.hooks.ResolveModem()
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusServiceUnavailable, "No modem route", nil))
		return
	}
	if g.lineICCID == "" {
		g.lineICCID = iccid
	}

	s := g.manager.GetSession(iccid)
	if s == nil {
		s, err = g.manager.EnsureSession(iccid, target)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "Session init failed", nil))
			return
		}
	} else {
		s.Target = target
	}
	if err := g.manager.EnsureAudio(iccid); err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusServiceUnavailable, "Audio init failed", nil))
		return
	}

	remoteAddr, payloadType, codec, err := parseRemoteSDP(req.Body())
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptableHere, "Unsupported SDP", nil))
		return
	}

	dialog, err := g.dialogPool.ReadInvite(req, tx)
	if err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Dialog failed", nil))
		return
	}

	var created *sipClientCall
	call := &sipClientCall{
		cfg:    g.cfg,
		logger: g.logger,
		peer:   s.Peer,
		bridge: s.Bridge,
		onEnded: func(reason string) {
			g.mu.Lock()
			if g.active != nil && g.active.call == created {
				g.active = nil
			}
			g.mu.Unlock()

			s.sipMu.Lock()
			if s.sipCall == created {
				s.sipCall = nil
			}
			s.sipMu.Unlock()

			s.setCallState("idle", reason)

			if g.hooks.HangupModem != nil {
				_ = g.hooks.HangupModem(iccid)
			}
		},
		localMediaIP: g.localMediaIP,
		dialogUAS:    dialog,
		rtpSSRC:      rand.Uint32(),
		rtpSeq:       1,
	}
	created = call

	if err := call.openRTPListener(); err != nil {
		_ = dialog.Respond(sip.StatusServiceUnavailable, "RTP unavailable", nil)
		call.Close("rtp_error")
		return
	}
	call.rtpRemote = remoteAddr
	call.rtpPayloadType = payloadType
	call.rtpCodec = codec

	localSDP, err := call.buildLocalSDP()
	if err != nil {
		_ = dialog.Respond(sip.StatusInternalServerError, "Local SDP failed", nil)
		call.Close("sdp_error")
		return
	}

	s.sipMu.Lock()
	if s.sipCall != nil {
		s.sipMu.Unlock()
		_ = dialog.Respond(sip.StatusBusyHere, "Call in progress", nil)
		call.Close("busy")
		return
	}
	s.sipCall = call
	s.sipMu.Unlock()

	g.mu.Lock()
	if g.active != nil {
		g.mu.Unlock()
		_ = dialog.Respond(sip.StatusBusyHere, "Call in progress", nil)
		call.Close("busy")
		return
	}
	g.active = &sipInboundLeg{
		dialogID: dialog.ID,
		iccid:    iccid,
		call:     call,
	}
	g.mu.Unlock()

	s.setCallState("dialing", "sip_inbound_invite")
	_ = dialog.Respond(sip.StatusTrying, "Trying", nil)
	_ = dialog.Respond(sip.StatusRinging, "Ringing", nil)

	if err := g.hooks.DialModem(iccid, number); err != nil {
		_ = dialog.Respond(sip.StatusServiceUnavailable, "Dial failed", nil)
		call.Close("dial_error")
		return
	}

	if err := dialog.RespondSDP([]byte(localSDP)); err != nil {
		call.Close("answer_error")
		return
	}

	s.setCallState("in_call", "sip_inbound_connected")
	go call.readRTPToLocal()
}

func (g *sipInboundGateway) onAck(req *sip.Request, tx sip.ServerTransaction) {
	if g.dialogPool == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "No call", nil))
		return
	}
	if err := g.dialogPool.ReadAck(req, tx); err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
	}
}

func (g *sipInboundGateway) onBye(req *sip.Request, tx sip.ServerTransaction) {
	if g.dialogPool == nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "No call", nil))
		return
	}
	if err := g.dialogPool.ReadBye(req, tx); err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "No call", nil))
		return
	}

	dialogID, err := sip.DialogIDFromRequestUAS(req)
	if err != nil {
		return
	}
	leg := g.activeByDialog(dialogID)
	if leg != nil && leg.call != nil {
		leg.call.Close("remote_bye")
	}
}

func (g *sipInboundGateway) onInfo(req *sip.Request, tx sip.ServerTransaction) {
	_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	if g.hooks.SendDTMF == nil {
		return
	}

	tone := parseDTMFRelayTone(req.Body())
	if tone == "" {
		return
	}

	dialogID, err := sip.DialogIDFromRequestUAS(req)
	if err != nil {
		return
	}
	leg := g.activeByDialog(dialogID)
	if leg == nil || leg.iccid == "" {
		return
	}

	if err := g.hooks.SendDTMF(leg.iccid, tone); err != nil {
		g.logger.Printf("sip dtmf relay to modem failed: %v", err)
	}
}

func (g *sipInboundGateway) activeByDialog(dialogID string) *sipInboundLeg {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		return nil
	}
	if g.active.dialogID != dialogID {
		return nil
	}
	return g.active
}

func newSIPClientCall(cfg SIPConfig, logger *log.Logger, session *Session, onEnded func(reason string)) (*sipClientCall, error) {
	if session == nil || session.Bridge == nil {
		return nil, errSIPAudioNotReady
	}

	transport := normalizeSIPTransport(cfg.Transport)
	proxyHost, proxyPort, err := parseProxyHostPort(cfg.Proxy, cfg.Port, transport)
	if err != nil {
		return nil, err
	}

	localSignalHost := strings.TrimSpace(cfg.LocalHost)
	if localSignalHost == "" {
		localSignalHost = detectLocalIP(proxyHost)
	}
	if localSignalHost == "" {
		localSignalHost = "127.0.0.1"
	}

	localSignalPort := cfg.LocalPort
	if localSignalPort <= 0 {
		localSignalPort = 5062
	}

	localMediaIP := strings.TrimSpace(cfg.RTPBindIP)
	if localMediaIP == "" || localMediaIP == "0.0.0.0" {
		localMediaIP = localSignalHost
	}
	if net.ParseIP(localMediaIP) == nil {
		localMediaIP = "127.0.0.1"
	}

	domain := strings.TrimSpace(cfg.Domain)
	if domain == "" {
		domain = proxyHost
	}

	uaOpts := []sipgo.UserAgentOption{}
	if transport == "tls" {
		uaOpts = append(uaOpts, sipgo.WithUserAgenTLSConfig(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSSkipVerify,
		}))
	}

	ua, err := sipgo.NewUA(uaOpts...)
	if err != nil {
		return nil, err
	}

	clientAddr := net.JoinHostPort(localSignalHost, strconv.Itoa(localSignalPort))
	client, err := sipgo.NewClient(ua, sipgo.WithClientAddr(clientAddr))
	if err != nil {
		_ = ua.Close()
		return nil, err
	}

	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = client.Close()
		_ = ua.Close()
		return nil, err
	}

	call := &sipClientCall{
		cfg:             cfg,
		logger:          logger,
		peer:            session.Peer,
		bridge:          session.Bridge,
		onEnded:         onEnded,
		transport:       transport,
		proxyHost:       proxyHost,
		proxyPort:       proxyPort,
		proxyAddr:       net.JoinHostPort(proxyHost, strconv.Itoa(proxyPort)),
		domain:          domain,
		localSignalHost: localSignalHost,
		localSignalPort: localSignalPort,
		localMediaIP:    localMediaIP,
		ua:              ua,
		client:          client,
		server:          server,
		rtpSSRC:         rand.Uint32(),
		rtpSeq:          1,
	}

	contact := sip.ContactHeader{
		Address: sip.Uri{
			User: cfg.Username,
			Host: localSignalHost,
			Port: localSignalPort,
		},
	}
	call.dialogPool = sipgo.NewDialogClientCache(client, contact)

	call.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if call.dialogPool == nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "No call", nil))
			return
		}
		if err := call.dialogPool.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "No call", nil))
			return
		}
		call.Close("remote_bye")
	})

	call.server.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})

	if err := call.startServerListener(); err != nil {
		call.Close("sip_listener_error")
		return nil, err
	}

	return call, nil
}

func (c *sipClientCall) Dial(number string, inviteTimeout time.Duration) error {
	number = strings.TrimSpace(number)
	if number == "" || !sipDialNumberPattern.MatchString(number) {
		return errSIPInvalidNumber
	}

	if err := c.openRTPListener(); err != nil {
		return err
	}

	if c.cfg.Register {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := c.register(ctx)
		cancel()
		if err != nil {
			return err
		}
	}

	localSDP, err := c.buildLocalSDP()
	if err != nil {
		return err
	}

	recipient := sip.Uri{
		Scheme: "sip",
		User:   number,
		Host:   c.domain,
	}
	recipient.UriParams = sip.NewParams()
	recipient.UriParams.Add("transport", c.transport)

	req := sip.NewRequest(sip.INVITE, recipient)
	req.SetTransport(strings.ToUpper(c.transport))
	req.SetDestination(c.proxyAddr)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte(localSDP))

	ctx, cancel := context.WithTimeout(context.Background(), inviteTimeout)
	defer cancel()

	dialog, err := c.dialogPool.WriteInvite(ctx, req)
	if err != nil {
		return err
	}
	c.dialog = dialog

	err = dialog.WaitAnswer(ctx, sipgo.AnswerOptions{
		Username: c.cfg.Username,
		Password: c.cfg.Password,
	})
	if err != nil {
		return err
	}
	if err := dialog.Ack(ctx); err != nil {
		return err
	}

	remoteAddr, payloadType, codec, err := parseRemoteSDP(dialog.InviteResponse.Body())
	if err != nil {
		return err
	}

	c.rtpRemote = remoteAddr
	c.rtpPayloadType = payloadType
	c.rtpCodec = codec

	go c.readRTPToLocal()
	return nil
}

func (c *sipClientCall) Hangup() error {
	var hangErr error
	if c.dialog != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hangErr = c.dialog.Bye(ctx)
		cancel()
	} else if c.dialogUAS != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hangErr = c.dialogUAS.Bye(ctx)
		cancel()
	}
	c.Close("hangup")
	return hangErr
}

func (c *sipClientCall) SendDTMF(tone string) error {
	if c.dialog == nil && c.dialogUAS == nil {
		return errSIPNoActiveCall
	}
	if len(tone) != 1 {
		return fmt.Errorf("invalid dtmf tone")
	}

	method := strings.ToLower(strings.TrimSpace(c.cfg.DTMFMethod))
	if method == "" {
		method = "info"
	}
	if method != "info" {
		return fmt.Errorf("unsupported dtmf method: %s", method)
	}

	duration := c.cfg.DTMFDurationMillis
	if duration <= 0 {
		duration = defaultDTMFDurationMs
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body := []byte(fmt.Sprintf("Signal=%s\r\nDuration=%d\r\n", tone, duration))

	var res *sip.Response
	var err error
	if c.dialog != nil {
		req := sip.NewRequest(sip.INFO, c.dialog.InviteRequest.Recipient)
		req.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf-relay"))
		req.SetBody(body)
		res, err = c.dialog.Do(ctx, req)
	} else {
		req := sip.NewRequest(sip.INFO, c.dialogUAS.InviteRequest.Recipient)
		req.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf-relay"))
		req.SetBody(body)
		res, err = c.dialogUAS.Do(ctx, req)
	}
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("dtmf rejected: %d %s", res.StatusCode, strings.TrimSpace(res.Reason))
	}
	return nil
}

func (c *sipClientCall) Close(reason string) {
	c.closeOnce.Do(func() {
		if c.dialog != nil {
			_ = c.dialog.Close()
			c.dialog = nil
		}
		if c.dialogUAS != nil {
			_ = c.dialogUAS.Close()
			c.dialogUAS = nil
		}
		if c.rtpConn != nil {
			_ = c.rtpConn.Close()
			c.rtpConn = nil
		}
		if c.listener != nil {
			_ = c.listener.Close()
			c.listener = nil
		}
		if c.client != nil {
			_ = c.client.Close()
			c.client = nil
		}
		if c.server != nil {
			_ = c.server.Close()
			c.server = nil
		}
		if c.ua != nil {
			_ = c.ua.Close()
			c.ua = nil
		}
		if c.onEnded != nil {
			c.onEnded(reason)
		}
	})
}

func (c *sipClientCall) startServerListener() error {
	listenAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(c.localSignalPort))

	switch c.transport {
	case "udp":
		pc, err := net.ListenPacket("udp", listenAddr)
		if err != nil {
			return err
		}
		c.listener = pc
		go func() {
			if err := c.server.ServeUDP(pc); err != nil && !isNetClosedError(err) {
				c.logger.Printf("sip udp listener stopped: %v", err)
			}
		}()
	case "tcp":
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return err
		}
		c.listener = ln
		go func() {
			if err := c.server.ServeTCP(ln); err != nil && !isNetClosedError(err) {
				c.logger.Printf("sip tcp listener stopped: %v", err)
			}
		}()
	case "tls":
		ln, err := newSIPTLSListener(listenAddr, c.localSignalHost, c.localMediaIP)
		if err != nil {
			return err
		}
		c.listener = ln
		go func() {
			if err := c.server.ServeTLS(ln); err != nil && !isNetClosedError(err) {
				c.logger.Printf("sip tls listener stopped: %v", err)
			}
		}()
	default:
		return fmt.Errorf("unsupported sip transport: %s", c.transport)
	}

	return nil
}

func (c *sipClientCall) register(ctx context.Context) error {
	recipient := sip.Uri{
		Scheme: "sip",
		User:   c.cfg.Username,
		Host:   c.domain,
	}

	req := sip.NewRequest(sip.REGISTER, recipient)
	req.SetTransport(strings.ToUpper(c.transport))
	req.SetDestination(c.proxyAddr)
	req.AppendHeader(&sip.ContactHeader{
		Address: sip.Uri{
			User: c.cfg.Username,
			Host: c.localSignalHost,
			Port: c.localSignalPort,
		},
	})

	expires := c.cfg.RegisterExpires
	if expires <= 0 {
		expires = 300
	}
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))

	res, err := c.client.Do(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return err
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		res, err = c.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: c.cfg.Username,
			Password: c.cfg.Password,
		})
		if err != nil {
			return err
		}
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("register failed: %d %s", res.StatusCode, strings.TrimSpace(res.Reason))
	}
	return nil
}

func (c *sipClientCall) openRTPListener() error {
	if c.rtpConn != nil {
		return nil
	}

	bindIP := net.ParseIP(c.cfg.RTPBindIP)
	if bindIP == nil {
		bindIP = net.ParseIP("0.0.0.0")
	}

	minPort := c.cfg.RTPPortMin
	maxPort := c.cfg.RTPPortMax
	if minPort <= 0 {
		minPort = 30000
	}
	if maxPort < minPort {
		maxPort = minPort
	}

	var lastErr error
	for port := minPort; port <= maxPort; port++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: port})
		if err != nil {
			lastErr = err
			continue
		}
		c.rtpConn = conn
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("no free RTP port")
	}
	return fmt.Errorf("open rtp listener failed (%d-%d): %w", minPort, maxPort, lastErr)
}

func (c *sipClientCall) buildLocalSDP() (string, error) {
	if c.rtpConn == nil {
		return "", errors.New("rtp not initialized")
	}
	addr, ok := c.rtpConn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", errors.New("invalid local rtp address")
	}
	port := addr.Port
	if port <= 0 {
		return "", errors.New("invalid local rtp port")
	}

	sessionID := time.Now().UnixNano()
	return fmt.Sprintf(
		"v=0\r\n"+
			"o=- %d %d IN IP4 %s\r\n"+
			"s=smsie-sip\r\n"+
			"c=IN IP4 %s\r\n"+
			"t=0 0\r\n"+
			"m=audio %d RTP/AVP 0 8 101\r\n"+
			"a=rtpmap:0 PCMU/8000\r\n"+
			"a=rtpmap:8 PCMA/8000\r\n"+
			"a=rtpmap:101 telephone-event/8000\r\n"+
			"a=fmtp:101 0-15\r\n"+
			"a=ptime:20\r\n"+
			"a=sendrecv\r\n",
		sessionID, sessionID, c.localMediaIP, c.localMediaIP, port,
	), nil
}

func (c *sipClientCall) readRTPToLocal() {
	if c.rtpConn == nil {
		return
	}

	buf := make([]byte, 2048)
	var browserTS uint32 = 0
	var browserSeq uint16 = 1

	for {
		n, _, err := c.rtpConn.ReadFromUDP(buf)
		if err != nil {
			if isNetClosedError(err) {
				return
			}
			c.logger.Printf("sip rtp read error: %v", err)
			return
		}

		var packet rtp.Packet
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue
		}

		pcm, ok := c.decodeSIPRTP(packet.PayloadType, packet.Payload)
		if !ok || len(pcm) == 0 {
			continue
		}

		if c.bridge != nil {
			c.bridge.PushFromWebRTC(pcm)
		}

		if c.peer != nil && c.peer.PeerConnection() != nil && c.peer.PeerConnection().ConnectionState() == webrtc.PeerConnectionStateConnected {
			if err := c.peer.SendPCMToBrowser(pcm, &browserTS, &browserSeq); err != nil {
				c.logger.Printf("sip rtp push to browser failed: %v", err)
			}
		}
	}
}

func (c *sipClientCall) decodeSIPRTP(payloadType uint8, payload []byte) ([]int16, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if payloadType == 101 {
		return nil, false
	}

	codec := strings.ToUpper(c.rtpCodec)
	switch codec {
	case "PCMA":
		if payloadType == c.rtpPayloadType || payloadType == 8 {
			return decodeALaw(payload), true
		}
	default:
		if payloadType == c.rtpPayloadType || payloadType == 0 {
			return decodeULaw(payload), true
		}
	}

	if payloadType == 0 {
		return decodeULaw(payload), true
	}
	if payloadType == 8 {
		return decodeALaw(payload), true
	}
	return nil, false
}

func (c *sipClientCall) SendPCMFromUAC(samples []int16) error {
	if len(samples) == 0 || c.rtpConn == nil || c.rtpRemote == nil {
		return nil
	}

	const frame = 160

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	for i := 0; i < len(samples); i += frame {
		end := i + frame
		if end > len(samples) {
			end = len(samples)
		}

		chunk := samples[i:end]
		var payload []byte
		if strings.EqualFold(c.rtpCodec, "PCMA") {
			payload = encodeALaw(chunk)
		} else {
			payload = encodeULaw(chunk)
		}

		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    c.rtpPayloadType,
				SequenceNumber: c.rtpSeq,
				Timestamp:      c.rtpTimestamp,
				SSRC:           c.rtpSSRC,
			},
			Payload: payload,
		}
		raw, err := packet.Marshal()
		if err != nil {
			continue
		}
		if _, err := c.rtpConn.WriteToUDP(raw, c.rtpRemote); err != nil {
			return err
		}

		c.rtpSeq++
		c.rtpTimestamp += uint32(len(chunk))
	}
	return nil
}

func parseRemoteSDP(body []byte) (*net.UDPAddr, uint8, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	sessionIP := ""
	audioIP := ""
	audioPort := 0
	audioPayloads := []int{}
	rtpMap := map[int]string{}
	inAudioMedia := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "m=") {
			inAudioMedia = false
			fields := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(fields) < 4 || !strings.EqualFold(fields[0], "audio") {
				continue
			}

			port, err := strconv.Atoi(fields[1])
			if err != nil || port <= 0 {
				continue
			}
			audioPort = port
			audioPayloads = audioPayloads[:0]
			for _, p := range fields[3:] {
				v, err := strconv.Atoi(p)
				if err == nil && v >= 0 && v <= 127 {
					audioPayloads = append(audioPayloads, v)
				}
			}
			inAudioMedia = true
			continue
		}

		if strings.HasPrefix(line, "c=") {
			ip := parseSDPConnectionIP(line)
			if ip == "" {
				continue
			}
			if inAudioMedia {
				audioIP = ip
			} else {
				sessionIP = ip
			}
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "a=rtpmap:") {
			rest := strings.TrimSpace(line[len("a=rtpmap:"):])
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) != 2 {
				continue
			}
			pt, err := strconv.Atoi(strings.TrimSpace(parts[0]))
			if err != nil {
				continue
			}
			codecPart := strings.TrimSpace(parts[1])
			codec := strings.ToUpper(strings.SplitN(codecPart, "/", 2)[0])
			rtpMap[pt] = codec
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, 0, "", err
	}
	if audioPort <= 0 {
		return nil, 0, "", errors.New("remote SDP missing audio port")
	}
	if audioIP == "" {
		audioIP = sessionIP
	}
	if audioIP == "" {
		return nil, 0, "", errors.New("remote SDP missing connection IP")
	}

	selectedPT := -1
	selectedCodec := ""
	for _, pt := range audioPayloads {
		codec := strings.ToUpper(rtpMap[pt])
		if codec == "" {
			if pt == 0 {
				codec = "PCMU"
			}
			if pt == 8 {
				codec = "PCMA"
			}
		}
		if codec == "PCMU" || codec == "PCMA" {
			selectedPT = pt
			selectedCodec = codec
			break
		}
	}

	if selectedPT < 0 {
		return nil, 0, "", errors.New("remote SDP has no PCMU/PCMA payload")
	}

	ip := net.ParseIP(audioIP)
	if ip == nil {
		ips, err := net.LookupIP(audioIP)
		if err != nil || len(ips) == 0 {
			return nil, 0, "", fmt.Errorf("invalid remote audio IP: %s", audioIP)
		}
		ip = ips[0]
	}

	return &net.UDPAddr{IP: ip, Port: audioPort}, uint8(selectedPT), selectedCodec, nil
}

func parseSDPConnectionIP(line string) string {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "c=")))
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func extractInviteNumber(req *sip.Request) string {
	if req == nil {
		return ""
	}
	if v := strings.TrimSpace(req.Recipient.User); v != "" {
		return v
	}
	if to := req.To(); to != nil {
		if v := strings.TrimSpace(to.Address.User); v != "" {
			return v
		}
	}
	return ""
}

func parseDTMFRelayTone(body []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for _, line := range lines {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "signal") {
			continue
		}
		v := strings.TrimSpace(parts[1])
		if len(v) == 1 && strings.Contains("0123456789*#", v) {
			return v
		}
	}
	return ""
}

func newSIPTLSListener(listenAddr string, hostCandidates ...string) (net.Listener, error) {
	cert, err := generateSelfSignedSIPTLSCertificate(hostCandidates...)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}), nil
}

func generateSelfSignedSIPTLSCertificate(hostCandidates ...string) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := crand.Int(crand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: "smsie-sip"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	seenDNS := map[string]struct{}{"localhost": {}}
	seenIP := map[string]struct{}{"127.0.0.1": {}}
	for _, candidate := range hostCandidates {
		host := strings.TrimSpace(candidate)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsUnspecified() {
				continue
			}
			key := ip.String()
			if _, ok := seenIP[key]; ok {
				continue
			}
			seenIP[key] = struct{}{}
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		if _, ok := seenDNS[host]; ok {
			continue
		}
		seenDNS[host] = struct{}{}
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}

	certDER, err := x509.CreateCertificate(crand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func normalizeSIPTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "tcp":
		return "tcp"
	case "tls":
		return "tls"
	default:
		return "udp"
	}
}

func parseProxyHostPort(proxy string, port int, transport string) (string, int, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return "", 0, errors.New("sip proxy is required")
	}

	if h, p, err := net.SplitHostPort(proxy); err == nil {
		proxy = h
		parsedPort, parseErr := strconv.Atoi(p)
		if parseErr != nil {
			return "", 0, parseErr
		}
		port = parsedPort
	}

	if port <= 0 {
		if transport == "tls" {
			port = 5061
		} else {
			port = 5060
		}
	}
	return proxy, port, nil
}

func detectLocalIP(targetHost string) string {
	if targetHost == "" {
		return ""
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(targetHost, "53"), 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return ""
	}
	return addr.IP.String()
}

func isNetClosedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "use of closed network connection")
}
