package worker

import "testing"

func TestCleanupStoppedWorkersRearmsDisconnectedPort(t *testing.T) {
	initTestLogger()
	m := NewManager(nil)
	w := NewModemWorker("/dev/ttyACM0", nil, m)
	w.requestReprobe()
	w.Stop()
	m.workers[w.PortName] = w
	m.probedPorts[w.PortName] = true

	m.cleanupStoppedWorkersLocked()

	if _, ok := m.workers[w.PortName]; ok {
		t.Fatal("expected stopped worker to be removed")
	}
	if m.probedPorts[w.PortName] {
		t.Fatal("expected disconnected port to be re-armed for probing")
	}
}

func TestCleanupStoppedWorkersKeepsNonATPortCached(t *testing.T) {
	initTestLogger()
	m := NewManager(nil)
	w := NewModemWorker("/dev/ttyUSB1", nil, m)
	w.Stop()
	m.workers[w.PortName] = w
	m.probedPorts[w.PortName] = true

	m.cleanupStoppedWorkersLocked()

	if !m.probedPorts[w.PortName] {
		t.Fatal("expected intentionally stopped non-AT port to stay cached")
	}
}
