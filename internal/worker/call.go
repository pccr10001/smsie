package worker

import (
	"errors"
	"time"
)

type CallState struct {
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	UACVID    string    `json:"uac_vid,omitempty"`
	UACPID    string    `json:"uac_pid,omitempty"`
}

func (w *ModemWorker) CallState() CallState {
	s := w.GetCallState()
	vid, pid := w.UACIdentity()
	return CallState{
		State:     s.State,
		Reason:    s.Reason,
		UpdatedAt: s.UpdatedAt,
		UACVID:    vid,
		UACPID:    pid,
	}
}

func IsInvalidDialNumberError(err error) bool {
	return errors.Is(err, errInvalidDialNumber)
}

func IsCallInProgressError(err error) bool {
	return errors.Is(err, errCallInProgress)
}
