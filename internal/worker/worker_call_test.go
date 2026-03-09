package worker

import (
	"testing"
	"time"

	"github.com/pccr10001/smsie/pkg/logger"
)

func initTestLogger() {
	if logger.Log == nil {
		logger.InitLogger("error")
	}
}

func TestParseCCInfoState(t *testing.T) {
	initTestLogger()
	info, ok := parseCCInfoState(`+QIND: "ccinfo",4,1,4,0,0,"0955452980",128`)
	if !ok {
		t.Fatal("expected ccinfo parse to succeed")
	}
	if info.Direction != 1 {
		t.Fatalf("expected direction 1, got %d", info.Direction)
	}
	if info.Stat != 4 {
		t.Fatalf("expected stat 4, got %d", info.Stat)
	}
	if info.Mode != 0 {
		t.Fatalf("expected mode 0, got %d", info.Mode)
	}
	if info.Number != "0955452980" {
		t.Fatalf("expected number 0955452980, got %q", info.Number)
	}
}

func TestHandleCallURCWaitsForNoCarrierOnRelease(t *testing.T) {
	initTestLogger()
	w := &ModemWorker{
		PortName: "test",
		call: callSnapshot{
			State:     callStateIdle,
			Reason:    "init",
			UpdatedAt: time.Now(),
		},
	}

	w.handleCallURC("RING")
	w.handleCallURC(`+QIND: "ccinfo",4,1,4,0,0,"0955452980",128`)

	state := w.GetCallState()
	if state.State != callStateDialing {
		t.Fatalf("expected dialing after incoming ring, got %q", state.State)
	}
	if !state.IncomingRinging {
		t.Fatal("expected incoming ringing to be true")
	}
	if state.Number != "0955452980" {
		t.Fatalf("expected caller number to be recorded, got %q", state.Number)
	}

	w.handleCallURC(`+QIND: "ccinfo",4,1,-1,0,0,"0955452980",128`)
	state = w.GetCallState()
	if state.State != callStateDialing {
		t.Fatalf("expected stat=-1 to keep dialing until NO CARRIER, got %q", state.State)
	}
	if state.Stat != -1 {
		t.Fatalf("expected stat=-1 to be retained, got %d", state.Stat)
	}

	w.handleCallURC("NO CARRIER")
	state = w.GetCallState()
	if state.State != callStateIdle {
		t.Fatalf("expected idle after NO CARRIER, got %q", state.State)
	}
	if state.Number != "" {
		t.Fatalf("expected caller number to be cleared after NO CARRIER, got %q", state.Number)
	}
}
