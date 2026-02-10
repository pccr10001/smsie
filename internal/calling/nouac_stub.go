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
