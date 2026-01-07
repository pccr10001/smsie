package worker

import (
	"sync"
	"time"

	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/pkg/logger"
	"go.bug.st/serial"
	"gorm.io/gorm"
)

type Manager struct {
	workers      map[string]*ModemWorker
	activeICCIDs map[string]string // iccid -> portName
	mu           sync.RWMutex
	stop         chan struct{}
	db           *gorm.DB
}

func NewManager(db *gorm.DB) *Manager {
	return &Manager{
		workers:      make(map[string]*ModemWorker),
		activeICCIDs: make(map[string]string),
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

	// 1. Add new workers
	for p := range validPorts {
		if _, exists := m.workers[p]; !exists {
			logger.Log.Infof("Found new port: %s. Starting worker...", p)
			w := NewModemWorker(p, m.db, m)
			m.workers[p] = w
			go w.Start()
		}
	}

	// 2. Remove workers for missing ports
	for p, w := range m.workers {
		if !validPorts[p] {
			logger.Log.Infof("Port %s gone. Stopping worker...", p)
			w.Stop()
			delete(m.workers, p)

			// If associated with an ICCID, unregister it
			if w.modem != nil && w.modem.ICCID != "" {
				delete(m.activeICCIDs, w.modem.ICCID)
			}
		}
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
