//go:build !nouac

package calling

import (
	"errors"
	"log"
	"sync"

	"github.com/pion/webrtc/v4"
)

type Session struct {
	Peer   *WebRTCPeer
	Bridge *AudioBridge
	Target ModemTarget
}

type Manager struct {
	logger *log.Logger
	cfg    Config

	api       *webrtc.API
	webrtcCfg webrtc.Configuration

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(cfg Config, logger *log.Logger) (*Manager, error) {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		PayloadType:        0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000, Channels: 1},
		PayloadType:        8,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}

	setting := webrtc.SettingEngine{}
	if cfg.UDPPortMin > 0 || cfg.UDPPortMax > 0 {
		if err := setting.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			return nil, err
		}
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(setting))

	iceServers := make([]webrtc.ICEServer, 0, len(cfg.STUNServers))
	if len(cfg.STUNServers) > 0 {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: cfg.STUNServers})
	}

	return &Manager{
		logger:    logger,
		cfg:       cfg,
		api:       api,
		webrtcCfg: webrtc.Configuration{ICEServers: iceServers},
		sessions:  map[string]*Session{},
	}, nil
}

func (m *Manager) EnsureSession(iccid string, target ModemTarget) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[iccid]; ok {
		s.Target = target
		return s, nil
	}

	peer, err := NewWebRTCPeer(m.api, m.webrtcCfg, m.logger)
	if err != nil {
		return nil, err
	}

	s := &Session{Peer: peer, Target: target}
	m.sessions[iccid] = s
	return s, nil
}

func (m *Manager) EnsureAudio(iccid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[iccid]
	if !ok || s == nil || s.Peer == nil {
		return errors.New("webrtc session not initialized")
	}
	if s.Bridge != nil {
		return nil
	}

	bridge, err := NewAudioBridge(m.cfg.Audio, s.Target, m.logger)
	if err != nil {
		return err
	}
	if err := bridge.Start(); err != nil {
		_ = bridge.Close()
		return err
	}

	s.Bridge = bridge
	s.Peer.SetAudioSink(bridge.PushFromWebRTC)
	go m.uplinkAudioLoop(iccid, s)
	return nil
}

func (m *Manager) uplinkAudioLoop(iccid string, s *Session) {
	var timestamp uint32 = 0
	var seq uint16 = 1

	for frame := range s.Bridge.CaptureFrames() {
		if frame == nil {
			continue
		}
		if s.Peer == nil || s.Peer.PeerConnection() == nil {
			continue
		}
		if s.Peer.PeerConnection().ConnectionState() != webrtc.PeerConnectionStateConnected {
			continue
		}
		if err := s.Peer.SendPCMToBrowser(frame, &timestamp, &seq); err != nil {
			m.logger.Printf("[%s] send PCM to browser failed: %v", iccid, err)
		}
	}
}

func (m *Manager) GetSession(iccid string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[iccid]
}

func (m *Manager) CloseSession(iccid string) error {
	m.mu.Lock()
	s := m.sessions[iccid]
	delete(m.sessions, iccid)
	m.mu.Unlock()

	if s == nil {
		return nil
	}

	var closeErr error
	if err := s.Peer.Close(); err != nil {
		closeErr = err
	}
	if s.Bridge != nil {
		if err := s.Bridge.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if s.Bridge == nil && closeErr == nil {
		closeErr = nil
	}
	return closeErr
}

func (m *Manager) CloseAll() error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		keys = append(keys, k)
	}
	m.mu.Unlock()

	var closeErr error
	for _, k := range keys {
		if err := m.CloseSession(k); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (m *Manager) IsConnected(iccid string) bool {
	s := m.GetSession(iccid)
	if s == nil || s.Peer == nil || s.Peer.PeerConnection() == nil {
		return false
	}
	state := s.Peer.PeerConnection().ConnectionState()
	return state == webrtc.PeerConnectionStateConnected
}

func (m *Manager) RequireConnected(iccid string) error {
	if !m.IsConnected(iccid) {
		return errors.New("webrtc not connected")
	}
	return nil
}
