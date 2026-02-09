package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pccr10001/smsie/internal/calling"
	"github.com/pccr10001/smsie/pkg/logger"
	"github.com/pion/webrtc/v4"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func handleModemWS(c *gin.Context, callMgr *calling.Manager, iccid string, target calling.ModemTarget) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Log.Errorf("upgrade websocket failed: %v", err)
		return
	}
	defer conn.Close()

	session, err := callMgr.EnsureSession(iccid, target)
	if err != nil {
		_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
		return
	}
	if session == nil || session.Peer == nil {
		_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: "invalid calling session"})
		return
	}
	peer := session.Peer

	pc := peer.PeerConnection()
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			_ = callMgr.CloseSession(iccid)
		}
	})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		msg := calling.SignalMessage{Type: "candidate", Candidate: ptrICE(candidate.ToJSON())}
		if err := conn.WriteJSON(msg); err != nil {
			logger.Log.Warnf("write candidate failed: %v", err)
		}
	})

	_ = conn.WriteJSON(calling.SignalMessage{Type: "ready", Text: "server ready"})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		msg, err := calling.ParseSignalMessage(raw)
		if err != nil {
			_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
			continue
		}

		switch msg.Type {
		case "offer":
			if msg.Offer == nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: "offer is required"})
				continue
			}
			if err := pc.SetRemoteDescription(*msg.Offer); err != nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
				continue
			}
			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
				continue
			}
			if err := pc.SetLocalDescription(answer); err != nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
				continue
			}
			localDesc, err := calling.WaitForLocalDescription(pc, 10*time.Second)
			if err != nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
				continue
			}
			_ = conn.WriteJSON(calling.SignalMessage{Type: "answer", Answer: localDesc})
		case "candidate":
			if msg.Candidate == nil {
				continue
			}
			if err := pc.AddICECandidate(*msg.Candidate); err != nil {
				_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: err.Error()})
			}
		default:
			_ = conn.WriteJSON(calling.SignalMessage{Type: "error", Text: "unsupported signal type"})
		}
	}
}

func ptrICE(c webrtc.ICECandidateInit) *webrtc.ICECandidateInit {
	return &c
}
