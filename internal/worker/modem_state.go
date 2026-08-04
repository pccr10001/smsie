package worker

import (
	"time"

	"github.com/pccr10001/smsie/internal/model"
)

type RuntimeModemState struct {
	ICCID          string
	IMEI           string
	Operator       string
	SignalStrength int
	PortName       string
	Status         string
	Registration   string
	LastSeen       time.Time
}

func (w *ModemWorker) RuntimeModemState() (RuntimeModemState, bool) {
	if w == nil {
		return RuntimeModemState{}, false
	}

	m, ok := w.modemSnapshot()
	if !ok {
		return RuntimeModemState{}, false
	}
	status := "offline"
	if !w.IsStopped() {
		status = "online"
	}

	return RuntimeModemState{
		ICCID:          m.ICCID,
		IMEI:           m.IMEI,
		Operator:       m.Operator,
		SignalStrength: m.SignalStrength,
		PortName:       m.PortName,
		Status:         status,
		Registration:   m.Registration,
		LastSeen:       m.LastSeen,
	}, true
}

func (w *ModemWorker) setModem(modem *model.Modem) {
	w.modemMu.Lock()
	w.modem = modem
	w.modemMu.Unlock()
}

func (w *ModemWorker) modemSnapshot() (model.Modem, bool) {
	w.modemMu.RLock()
	defer w.modemMu.RUnlock()
	if w.modem == nil {
		return model.Modem{}, false
	}
	return *w.modem, true
}

func (w *ModemWorker) updateModem(update func(*model.Modem)) bool {
	w.modemMu.Lock()
	defer w.modemMu.Unlock()
	if w.modem == nil {
		return false
	}
	update(w.modem)
	return true
}

func (w *ModemWorker) hasICCID(iccid string) bool {
	modem, ok := w.modemSnapshot()
	return ok && modem.ICCID == iccid
}

func (w *ModemWorker) modemICCID() string {
	modem, ok := w.modemSnapshot()
	if !ok {
		return ""
	}
	return modem.ICCID
}
