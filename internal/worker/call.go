package worker

import (
	"errors"
	"time"
)

type CallState struct {
	State           string    `json:"state"`
	Reason          string    `json:"reason"`
	Number          string    `json:"number"`
	Direction       int       `json:"direction"`
	Stat            int       `json:"stat"`
	Mode            int       `json:"mode"`
	Incoming        bool      `json:"incoming"`
	Voice           bool      `json:"voice"`
	IncomingRinging bool      `json:"incoming_ringing"`
	UpdatedAt       time.Time `json:"updated_at"`
	UACVID          string    `json:"uac_vid,omitempty"`
	UACPID          string    `json:"uac_pid,omitempty"`
}

func (w *ModemWorker) CallState() CallState {
	return w.callStateFromSnapshot(w.GetCallState())
}

func (w *ModemWorker) callStateFromSnapshot(s callSnapshot) CallState {
	vid, pid := w.UACIdentity()
	return CallState{
		State:           s.State,
		Reason:          s.Reason,
		Number:          s.Number,
		Direction:       s.Direction,
		Stat:            s.Stat,
		Mode:            s.Mode,
		Incoming:        s.Incoming,
		Voice:           s.Voice,
		IncomingRinging: s.IncomingRinging,
		UpdatedAt:       s.UpdatedAt,
		UACVID:          vid,
		UACPID:          pid,
	}
}

func IsInvalidDialNumberError(err error) bool {
	return errors.Is(err, errInvalidDialNumber)
}

func IsCallInProgressError(err error) bool {
	return errors.Is(err, errCallInProgress)
}
