package worker

import (
	"strings"
	"sync"
	"testing"
	"time"

	"go.bug.st/serial"
)

type scriptedPort struct {
	worker *ModemWorker
	script func(string) []string

	mu     sync.Mutex
	writes []string
}

func (p *scriptedPort) SetMode(*serial.Mode) error                           { return nil }
func (p *scriptedPort) Drain() error                                         { return nil }
func (p *scriptedPort) ResetInputBuffer() error                              { return nil }
func (p *scriptedPort) ResetOutputBuffer() error                             { return nil }
func (p *scriptedPort) SetDTR(bool) error                                    { return nil }
func (p *scriptedPort) SetRTS(bool) error                                    { return nil }
func (p *scriptedPort) GetModemStatusBits() (*serial.ModemStatusBits, error) { return nil, nil }
func (p *scriptedPort) SetReadTimeout(time.Duration) error                   { return nil }
func (p *scriptedPort) Close() error                                         { return nil }
func (p *scriptedPort) Break(time.Duration) error                            { return nil }
func (p *scriptedPort) Read([]byte) (int, error)                             { return 0, nil }

func (p *scriptedPort) Write(data []byte) (int, error) {
	command := string(data)
	p.mu.Lock()
	p.writes = append(p.writes, command)
	p.mu.Unlock()
	for _, line := range p.script(command) {
		p.worker.rxChan <- rxMsg{Data: line}
	}
	return len(data), nil
}

func (p *scriptedPort) recordedWrites() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}

func startTransactionWorker(t *testing.T, w *ModemWorker) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-w.stop:
				return
			case tx := <-w.transactionChan:
				w.executeTransaction(tx)
			}
		}
	}()
	t.Cleanup(func() {
		w.Stop()
		<-done
	})
}

func waitForQueuedTransaction(t *testing.T, w *ModemWorker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(w.transactionChan) == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("second transaction was not queued")
}

func TestATTransactionDoesNotInterleavePromptFlow(t *testing.T) {
	initTestLogger()
	w := NewModemWorker("test", nil, nil)
	port := &scriptedPort{worker: w}
	port.script = func(command string) []string {
		switch {
		case strings.HasPrefix(command, "AT+CMGS="):
			return []string{">"}
		case command == "00\x1A":
			return []string{"+CMGS: 1", "OK"}
		case command == "AT+CSQ\r":
			return []string{"+CSQ: 20,99", "OK"}
		default:
			return []string{"OK"}
		}
	}
	w.port = port
	startTransactionWorker(t, w)

	promptReady := make(chan struct{})
	releasePDU := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- w.runATTransaction(func(session *atSession) error {
			if _, err := session.execute("AT+CMGS=1", time.Second, false); err != nil {
				return err
			}
			close(promptReady)
			<-releasePDU
			_, err := session.execute("00\x1A", time.Second, false)
			return err
		})
	}()

	<-promptReady
	secondDone := make(chan error, 1)
	go func() {
		_, err := w.ExecuteAT("AT+CSQ", time.Second)
		secondDone <- err
	}()
	waitForQueuedTransaction(t, w)
	close(releasePDU)

	if err := <-firstDone; err != nil {
		t.Fatalf("first transaction failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second transaction failed: %v", err)
	}

	writes := port.recordedWrites()
	want := []string{"AT+CMGS=1\r", "00\x1A", "AT+CSQ\r"}
	if len(writes) != len(want) {
		t.Fatalf("got writes %q, want %q", writes, want)
	}
	for i := range want {
		if writes[i] != want[i] {
			t.Fatalf("write %d = %q, want %q", i, writes[i], want[i])
		}
	}
}

func TestParseCMGLPDUsIgnoresURCs(t *testing.T) {
	pdus, hasMessages := parseCMGLPDUs(strings.Join([]string{
		"+CREG: 2,1",
		"+CMGL: 1,0,,23",
		"001122",
		"+CREG: 2,5",
		"OK",
	}, "\n"), func(line string) bool {
		return strings.HasPrefix(line, "+CREG:")
	})

	if !hasMessages {
		t.Fatal("expected CMGL header to be recorded")
	}
	if len(pdus) != 1 || pdus[0] != "001122" {
		t.Fatalf("got PDUs %q, want [001122]", pdus)
	}
}

func TestExecuteATRejectsInteractiveInput(t *testing.T) {
	w := NewModemWorker("test", nil, nil)
	if _, err := w.ExecuteAT("AT+CMGS=1", time.Second); err == nil {
		t.Fatal("expected interactive command to be rejected")
	}
	if _, err := w.ExecuteAT("00\x1A", time.Second); err == nil {
		t.Fatal("expected PDU input to be rejected")
	}
}
