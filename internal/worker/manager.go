package worker

import (
	"sync"
	"time"

	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/pkg/logger"
	"go.bug.st/serial"
	"gorm.io/gorm"
)

type Manager struct {
	workers      map[string]*ModemWorker
	activeICCIDs map[string]string // iccid -> portName
	probedPorts  map[string]bool   // portName -> probed once while present
	mu           sync.RWMutex
	stop         chan struct{}
	db           *gorm.DB
}

func NewManager(db *gorm.DB) *Manager {
	return &Manager{
		workers:      make(map[string]*ModemWorker),
		activeICCIDs: make(map[string]string),
		probedPorts:  make(map[string]bool),
		stop:         make(chan struct{}),
		db:           db,
	}
}

func (m *Manager) Start() {
	scanInterval := 3 * time.Second
	// Use config interval if set, but ensure it's reasonable
	if d, err := time.ParseDuration(config.AppConfig.Serial.ScanInterval); err == nil && d > 0 {
		scanInterval = d
	}

	logger.Log.Info("Worker Manager started, scanning ports every ", scanInterval)

	// Initial scan
	m.ScanAndManage()

	go func() {
		ticker := time.NewTicker(scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.ScanAndManage()
			case <-m.stop:
				return
			}
		}
	}()
}

func (m *Manager) Stop() {
	close(m.stop)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		w.Stop()
	}
}

func (m *Manager) ScanAndManage() {
	ports, err := serial.GetPortsList()
	if err != nil {
		logger.Log.Errorf("Failed to list serial ports: %v", err)
		return
	}

	// Filter excluded ports
	validPorts := make(map[string]bool)
	for _, p := range ports {
		if !isExcluded(p) {
			validPorts[p] = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Remove workers and probed marks for missing ports.
	for p, w := range m.workers {
		if !validPorts[p] {
			logger.Log.Infof("Port %s gone. Stopping worker...", p)
			w.Stop()
			m.unregisterWorkerLocked(p, w)
		}
	}
	for p := range m.probedPorts {
		if !validPorts[p] {
			delete(m.probedPorts, p)
			logger.Log.Infof("Port %s removed from probed cache", p)
		}
	}

	// 2. Cleanup stopped workers so APIs won't route to dead workers.
	// Keep port in probed cache while still present to avoid repetitive probing
	// on non-AT sibling ports of the same modem.
	for p, w := range m.workers {
		if w.IsStopped() {
			logger.Log.Warnf("Worker on %s stopped. Removing active worker entry.", p)
			m.unregisterWorkerLocked(p, w)
		}
	}

	// 3. Probe only unprobed ports (newly appeared or previously removed).
	for p := range validPorts {
		if m.probedPorts[p] {
			continue
		}

		logger.Log.Infof("Found unprobed port: %s. Starting worker...", p)
		w := NewModemWorker(p, m.db, m)
		m.workers[p] = w
		m.probedPorts[p] = true
		go w.Start()
	}
}

func (m *Manager) unregisterWorkerLocked(port string, w *ModemWorker) {
	delete(m.workers, port)
	if w == nil {
		return
	}

	if w.modem != nil && w.modem.ICCID != "" {
		delete(m.activeICCIDs, w.modem.ICCID)
		w.modem.Status = "offline"
		w.modem.LastSeen = time.Now()
		_ = m.db.Model(&model.Modem{}).
			Where("iccid = ?", w.modem.ICCID).
			Updates(map[string]interface{}{
				"status":    "offline",
				"last_seen": w.modem.LastSeen,
			}).Error
	}
}

func isExcluded(port string) bool {
	for _, excluded := range config.AppConfig.Serial.ExcludePorts {
		if port == excluded {
			return true
		}
	}

	return false
}

func (m *Manager) GetWorkerByICCID(iccid string) *ModemWorker {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, w := range m.workers {
		if w.IsStopped() {
			continue
		}
		if w.modem != nil && w.modem.ICCID == iccid {
			return w
		}
	}
	return nil
}

func (m *Manager) RegisterICCID(port, iccid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existingPort, exists := m.activeICCIDs[iccid]; exists {
		if existingPort != port {
			return false // Already claimed by another port
		}
		// Same port, ok
		return true
	}

	m.activeICCIDs[iccid] = port
	return true
}

func (m *Manager) UnregisterICCID(iccid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeICCIDs, iccid)
}
