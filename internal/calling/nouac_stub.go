//go:build nouac

package calling

import (
	"errors"
	"log"
	"time"

	"github.com/pion/webrtc/v4"
)

var errUACDisabled = errors.New("UAC/calling disabled in this build")

type Config struct {
	STUNServers []string
	UDPPortMin  uint16
	UDPPortMax  uint16
	Audio       AudioConfig
	SIP         SIPConfig
}

type AudioConfig struct {
	DeviceKeyword    string
	OutputDeviceName string
	SampleRate       int
	Channels         int
	BitsPerSample    int
	CaptureChunkMs   int
	PlaybackChunkMs  int
}

type SIPConfig struct {
	Enabled            bool
	ModemICCID         string
	ModemICCIDs        []string
	Username           string
	Password           string
	Proxy              string
	Port               int
	Domain             string
	Transport          string
	TLSSkipVerify      bool
	Register           bool
	RegisterExpires    int
	LocalHost          string
	LocalPort          int
	RTPBindIP          string
	RTPPortMin         int
	RTPPortMax         int
	InviteTimeoutSec   int
	DTMFMethod         string
	DTMFDurationMillis int
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

func (a AudioConfig) CaptureSamples() int {
	if a.SampleRate <= 0 || a.CaptureChunkMs <= 0 {
		return 320
	}
	return a.SampleRate * a.CaptureChunkMs / 1000
}

func (a AudioConfig) PlaybackSamples() int {
	if a.SampleRate <= 0 || a.PlaybackChunkMs <= 0 {
		return 800
	}
	return a.SampleRate * a.PlaybackChunkMs / 1000
}

type ModemTarget struct {
	PortName string
	VID      string
	PID      string
	Serial   string
}

type USBIdentity struct {
	VID     string
	PID     string
	Serial  string
	Bus     int
	Address int
}

type USBDeviceInfo struct {
	Bus      int    `json:"bus"`
	Address  int    `json:"address"`
	VID      string `json:"vid"`
	PID      string `json:"pid"`
	Product  string `json:"product"`
	HasAudio bool   `json:"has_audio"`
}

func EnumerateByVIDPID(vidHex, pidHex string) ([]USBDeviceInfo, error) {
	_ = vidHex
	_ = pidHex
	return []USBDeviceInfo{}, nil
}

func ResolveUSBIdentityFromPort(target ModemTarget) (USBIdentity, error) {
	_ = target
	return USBIdentity{}, errUACDisabled
}

type SignalMessage struct {
	Type      string                     `json:"type"`
	Text      string                     `json:"text,omitempty"`
	Offer     *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer    *webrtc.SessionDescription `json:"answer,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

func ParseSignalMessage(raw []byte) (SignalMessage, error) {
	_ = raw
	return SignalMessage{}, errUACDisabled
}

func WaitForLocalDescription(pc *webrtc.PeerConnection, timeout time.Duration) (*webrtc.SessionDescription, error) {
	_ = pc
	_ = timeout
	return nil, errUACDisabled
}

type WebRTCPeer struct{}

func (p *WebRTCPeer) PeerConnection() *webrtc.PeerConnection {
	return nil
}

func (p *WebRTCPeer) Close() error {
	return nil
}

func (p *WebRTCPeer) SetAudioSink(fn func([]int16)) {
	_ = fn
}

func (p *WebRTCPeer) SendPCMToBrowser(samples []int16, timestamp *uint32, seq *uint16) error {
	_ = samples
	_ = timestamp
	_ = seq
	return errUACDisabled
}

type AudioBridge struct{}

func NewAudioBridge(cfg AudioConfig, target ModemTarget, logger *log.Logger) (*AudioBridge, error) {
	_ = cfg
	_ = target
	_ = logger
	return nil, errUACDisabled
}

func (b *AudioBridge) Start() error {
	_ = b
	return errUACDisabled
}

func (b *AudioBridge) CaptureFrames() <-chan []int16 {
	_ = b
	ch := make(chan []int16)
	close(ch)
	return ch
}

func (b *AudioBridge) Close() error {
	_ = b
	return nil
}

func (b *AudioBridge) PushFromWebRTC(samples []int16) {
	_ = b
	_ = samples
}

type Session struct {
	Peer   *WebRTCPeer
	Bridge *AudioBridge
	Target ModemTarget
}

type Manager struct{}

func NewManager(cfg Config, logger *log.Logger) (*Manager, error) {
	_ = cfg
	_ = logger
	return &Manager{}, nil
}

func (m *Manager) EnsureSession(iccid string, target ModemTarget) (*Session, error) {
	_ = m
	_ = iccid
	_ = target
	return nil, errUACDisabled
}

func (m *Manager) EnsureAudio(iccid string) error {
	_ = m
	_ = iccid
	return errUACDisabled
}

func (m *Manager) GetSession(iccid string) *Session {
	_ = m
	_ = iccid
	return nil
}

func (m *Manager) CloseSession(iccid string) error {
	_ = m
	_ = iccid
	return nil
}

func (m *Manager) CloseAll() error {
	_ = m
	return nil
}

func (m *Manager) IsConnected(iccid string) bool {
	_ = m
	_ = iccid
	return false
}

func (m *Manager) RequireConnected(iccid string) error {
	_ = m
	_ = iccid
	return errUACDisabled
}

func (m *Manager) SIPEnabled() bool {
	_ = m
	return false
}

func (m *Manager) SIPTransport() string {
	_ = m
	return ""
}

func (m *Manager) SIPCallState(iccid string) (state, reason string, updatedAt time.Time, ok bool) {
	_ = m
	_ = iccid
	return "", "", time.Time{}, false
}

func (m *Manager) SIPInboundLineInfo(iccid string) (SIPInboundLineInfo, bool) {
	_ = m
	_ = iccid
	return SIPInboundLineInfo{}, false
}

func (m *Manager) StartSIPInbound(lineID string, localPort int, hooks SIPInboundHooks) error {
	_ = m
	_ = lineID
	_ = localPort
	_ = hooks
	return errUACDisabled
}

func (m *Manager) StopSIPInbound() error {
	_ = m
	return nil
}

func (m *Manager) SyncSIPInbound(lineID string, cfg SIPConfig, hooks SIPInboundHooks) error {
	_ = m
	_ = lineID
	_ = cfg
	_ = hooks
	return errUACDisabled
}

func (m *Manager) StopSIPInboundLine(lineID string) error {
	_ = m
	_ = lineID
	return nil
}

func (m *Manager) PruneSIPInboundLines(active map[string]struct{}) error {
	_ = m
	_ = active
	return nil
}

func (m *Manager) SIPConfigForICCID(iccid string) (SIPConfig, bool) {
	_ = m
	_ = iccid
	return SIPConfig{}, false
}

func (m *Manager) DialSIP(iccid, number string) error {
	_ = m
	_ = iccid
	_ = number
	return errUACDisabled
}

func (m *Manager) HangupSIP(iccid string) error {
	_ = m
	_ = iccid
	return errUACDisabled
}

func (m *Manager) SendSIPDTMF(iccid, tone string) error {
	_ = m
	_ = iccid
	_ = tone
	return errUACDisabled
}

func (m *Manager) HasActiveSIPCall(iccid string) bool {
	_ = m
	_ = iccid
	return false
}
