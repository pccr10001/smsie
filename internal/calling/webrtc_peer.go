package calling

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type WebRTCPeer struct {
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticRTP
	logger *log.Logger

	sendMu sync.Mutex
	sinkMu sync.RWMutex
	sink   func([]int16)
}

func NewWebRTCPeer(api *webrtc.API, cfg webrtc.Configuration, logger *log.Logger) (*WebRTCPeer, error) {
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audio",
		"smsie-uac",
	)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}

	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}

	peer := &WebRTCPeer{pc: pc, track: track, logger: logger}

	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(rtcpBuf); readErr != nil {
				return
			}
		}
	}()

	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendrecv,
	}); err != nil {
		_ = pc.Close()
		return nil, err
	}

	pc.OnTrack(func(trackRemote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if trackRemote.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		peer.logger.Printf("remote audio track codec=%s", trackRemote.Codec().RTPCodecCapability.MimeType)
		for {
			pkt, _, readErr := trackRemote.ReadRTP()
			if readErr != nil {
				return
			}
			samples, decodeErr := decodeRemotePayload(trackRemote.Codec().RTPCodecCapability.MimeType, pkt.Payload)
			if decodeErr != nil {
				peer.logger.Printf("decode remote payload failed: %v", decodeErr)
				continue
			}
			peer.sinkMu.RLock()
			sink := peer.sink
			peer.sinkMu.RUnlock()
			if sink != nil {
				sink(samples)
			}
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		peer.logger.Printf("peer connection state: %s", state.String())
	})

	return peer, nil
}

func (p *WebRTCPeer) SetAudioSink(sink func([]int16)) {
	p.sinkMu.Lock()
	p.sink = sink
	p.sinkMu.Unlock()
}

func (p *WebRTCPeer) Close() error {
	if p.pc == nil {
		return nil
	}
	return p.pc.Close()
}

func (p *WebRTCPeer) PeerConnection() *webrtc.PeerConnection {
	return p.pc
}

func (p *WebRTCPeer) SendPCMToBrowser(samples []int16, timestamp *uint32, seq *uint16) error {
	if len(samples) == 0 {
		return nil
	}

	ulaw := encodeULaw(samples)

	p.sendMu.Lock()
	defer p.sendMu.Unlock()

	packet := &rtp.Packet{Header: rtp.Header{
		Version:        2,
		PayloadType:    0,
		SequenceNumber: *seq,
		Timestamp:      *timestamp,
		SSRC:           1,
	}, Payload: ulaw}

	if err := p.track.WriteRTP(packet); err != nil {
		return err
	}

	*seq = *seq + 1
	*timestamp = *timestamp + uint32(len(samples))
	return nil
}

func decodeRemotePayload(mimeType string, payload []byte) ([]int16, error) {
	switch mimeType {
	case webrtc.MimeTypePCMU:
		return decodeULaw(payload), nil
	case webrtc.MimeTypePCMA:
		return decodeALaw(payload), nil
	default:
		return nil, fmt.Errorf("unsupported incoming codec: %s", mimeType)
	}
}

func WaitForLocalDescription(pc *webrtc.PeerConnection, timeout time.Duration) (*webrtc.SessionDescription, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		if desc := pc.LocalDescription(); desc != nil {
			return desc, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return nil, errors.New("wait local description timeout")
		}
	}
}
