package worker

import "time"

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
	if w == nil || w.modem == nil {
		return RuntimeModemState{}, false
	}

	m := *w.modem
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
