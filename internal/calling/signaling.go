package calling

import (
	"encoding/json"
	"errors"

	"github.com/pion/webrtc/v4"
)

type SignalMessage struct {
	Type      string                     `json:"type"`
	Offer     *webrtc.SessionDescription `json:"offer,omitempty"`
	Answer    *webrtc.SessionDescription `json:"answer,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
	Text      string                     `json:"text,omitempty"`
	Data      any                        `json:"data,omitempty"`
}

func ParseSignalMessage(raw []byte) (*SignalMessage, error) {
	var msg SignalMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	if msg.Type == "" {
		return nil, errors.New("missing signal type")
	}
	return &msg, nil
}
