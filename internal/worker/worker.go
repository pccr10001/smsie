package worker

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/internal/logic"
	"github.com/pccr10001/smsie/internal/mccmnc"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/repository"
	"github.com/pccr10001/smsie/pkg/logger"
	"go.bug.st/serial"
	"gorm.io/gorm"
)

type ModemWorker struct {
	PortName string
	port     serial.Port
	stop     chan struct{}
	stopOnce sync.Once

	// Command handling
	cmdChan    chan commandRequest
	currentCmd *commandRequest
	mu         sync.Mutex // protects access to port write if needed

	busyMu sync.Mutex
	busy   bool

	// Data
	repo           *repository.ModemRepository
	smsRepo        *repository.SMSRepository
	webhookService *logic.WebhookService
	modem          *model.Modem
	manager        *Manager

	// Internal
	rxChan      chan rxMsg
	triggerChan chan struct{}
}

type rxMsg struct {
	Data string
	Err  error
}

type commandRequest struct {
	cmd      string
	respChan chan string
	errChan  chan error
	timeout  time.Duration
	silent   bool
}

func NewModemWorker(portName string, db *gorm.DB, manager *Manager) *ModemWorker {
	return &ModemWorker{
		PortName:       portName,
		stop:           make(chan struct{}),
		cmdChan:        make(chan commandRequest, 10),
		repo:           repository.NewModemRepository(db),
		smsRepo:        repository.NewSMSRepository(db),
		webhookService: logic.NewWebhookService(repository.NewWebhookRepository(db)),
		manager:        manager,
		rxChan:         make(chan rxMsg, 100), // Buffer to prevent blocking reader
		triggerChan:    make(chan struct{}, 1),
	}
}

func (w *ModemWorker) Start() {
	go w.runLoop()
	go w.logicLoop()
}

func (w *ModemWorker) runLoop() {
	logger.Log.Infof("Worker for %s running", w.PortName)

	mode := &serial.Mode{
		BaudRate: 115200,
	}

	var err error
	w.port, err = serial.Open(w.PortName, mode)
	if err != nil {
		logger.Log.Errorf("Failed to open port %s: %v", w.PortName, err)
		return
	}
	defer w.port.Close()

	// Set read timeout to ensure readLoop wakes up
	w.port.SetReadTimeout(100 * time.Millisecond)

	// Start Read Loop
	go w.readLoop()

	w.initModem()

	// Main Event Loop
	for {
		select {
		case <-w.stop:
			logger.Log.Infof("Worker for %s stopped", w.PortName)
			return

		case req := <-w.cmdChan:
			// Execute Command
			w.currentCmd = &req
			if !req.silent {
				logger.Log.Debugf("[%s] TX: %s", w.PortName, req.cmd)
			}
			if _, err := w.port.Write([]byte(req.cmd + "\r\n")); err != nil {
				req.errChan <- err
				w.currentCmd = nil
				continue
			}

			// Wait for response with timeout
			fullResponse := []string{}
			timeoutTimer := time.NewTimer(req.timeout)

			// Inner loop for reading response (from rxChan)
		RespLoop:
			for {
				select {
				case <-timeoutTimer.C:
					req.errChan <- errors.New("timeout")
					break RespLoop

				case msg := <-w.rxChan:
					if msg.Err != nil {
						req.errChan <- msg.Err
						logger.Log.Errorf("[%s] Port read error during cmd: %v. Stopping.", w.PortName, msg.Err)
						w.Stop()
						return
					}

					line := msg.Data
					if !req.silent {
						logger.Log.Debugf("[%s] RX: %s", w.PortName, line)
					}
					fullResponse = append(fullResponse, line)

					if line == "OK" {
						req.respChan <- strings.Join(fullResponse, "\n")
						break RespLoop
					} else if strings.Contains(line, "ERROR") {
						req.errChan <- fmt.Errorf("modem error: %s", strings.Join(fullResponse, "\n"))
						break RespLoop
					} else if line == "> " {
						req.respChan <- "> "
						break RespLoop
					} else if w.isURC(line) {
						w.handleURC(line)
					}
				}
			}
			timeoutTimer.Stop()
			w.currentCmd = nil // Ready for next command

		case msg := <-w.rxChan:
			// Idle processing
			if msg.Err != nil {
				logger.Log.Errorf("[%s] Port read error (idle): %v. Stopping.", w.PortName, msg.Err)
				w.Stop()
				return
			}

			line := msg.Data
			if w.isURC(line) {
				w.handleURC(line)
			}
		}
	}
}

// dedicated read loop
func (w *ModemWorker) readLoop() {
	reader := bufio.NewReader(w.port)
	for {
		// This will block until data or error
		// SetReadTimeout is still active on the port, so it will wake up periodically
		// but we can just treat timeout as emptiness
		line, err := reader.ReadString('\n')
		if err != nil {
			// Check if we are stopped
			select {
			case <-w.stop:
				return
			default:
			}

			// Handle Timeout
			errMsg := err.Error()
			if strings.Contains(errMsg, "timeout") {
				continue
			}

			// Real Error
			w.rxChan <- rxMsg{Err: err}
			return
		}

		// Got Data
		line = strings.TrimSpace(line)
		if line != "" {
			// Note: If buffer fills up this might block reader, but Buffer is 100
			// and runLoop should consume fast.
			select {
			case w.rxChan <- rxMsg{Data: line}:
			case <-w.stop:
				return
			}
		}
	}
}

func (w *ModemWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stop)
	})
}

func (w *ModemWorker) initModem() {
	go func() {
		// Wait for loop start
		time.Sleep(2 * time.Second)

		// 0. Probe Check
		// Send "AT" to check if device is responsive as a modem
		_, err := w.ExecuteATSilent("AT", 2*time.Second)
		if err != nil {
			logger.Log.Warnf("[%s] Probe failed (AT timeout/error): %v. Skipping port.", w.PortName, err)
			w.Stop() // Optional: Stop the worker to release resources
			return
		}

		// 1. Basic Setup

		for _, cmd := range config.AppConfig.Serial.InitATCommands {
			w.ExecuteAT(cmd, 5*time.Second)
		}

		// 2. Identify Modem
		resp, err := w.ExecuteAT("ATI", 2*time.Second)
		if err != nil {
			logger.Log.Errorf("[%s] Failed to ATI: %v", w.PortName, err)
			return
		}

		// 3. Get ICCID
		var iccid string
		if strings.Contains(resp, "Quectel") {
			resp, err = w.ExecuteAT("AT+QCCID", 5*time.Second)
			if err == nil {
				// Parse +QCCID: <iccid>
				iccid = parseID(resp, "+QCCID:")
			}
		} else {
			// Assume Air or Generic
			resp, err = w.ExecuteAT("AT+ICCID", 5*time.Second)
			if err == nil {
				iccid = parseID(resp, "+ICCID:")
			}
		}

		if iccid != "" {
			iccid = strings.TrimRight(strings.ToUpper(iccid), "F")
		}

		if iccid == "" {
			logger.Log.Errorf("[%s] Failed to get ICCID", w.PortName)
			return
		}

		// Deduplication Check
		if !w.manager.RegisterICCID(w.PortName, iccid) {
			logger.Log.Warnf("[%s] ICCID %s is already managed by another worker. Stopping duplicate.", w.PortName, iccid)
			w.Stop() // This stops the worker loop
			return
		}

		logger.Log.Infof("[%s] Found ICCID: %s", w.PortName, iccid)

		// 4. Get IMEI
		var imei string
		resp, err = w.ExecuteAT("AT+CGSN", 2*time.Second) // or AT+GSN
		if err == nil {
			// IMEI is usually just a number line
			lines := strings.Split(resp, "\n")
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if len(l) > 10 && !strings.Contains(l, "OK") {
					imei = l
					break
				}
			}
		}

		// 5. Get Signal Strength
		var signal int
		resp, err = w.ExecuteAT("AT+CSQ", 2*time.Second)
		if err == nil {
			// Parse +CSQ: <rssi>,<ber>
			// rssi: 0-31, 99
			parts := strings.Split(parseID(resp, "+CSQ:"), ",")
			if len(parts) > 0 {
				var rssi int
				if _, err := fmt.Sscanf(parts[0], "%d", &rssi); err == nil {
					if rssi == 99 {
						signal = 0
					} else {
						// Convert 0-31 to 0-100%
						signal = int(float64(rssi) / 31.0 * 100.0)
					}
				}
			}
		}

		// 6. Get Operator
		var operator string
		resp, err = w.ExecuteAT("AT+COPS?", 2*time.Second)
		if err == nil {
			// +COPS: 0,0,"Chunghwa Telecom",7
			// Parse content inside quotes
			if strings.Contains(resp, "\"") {
				splitted := strings.Split(resp, "\"")
				if len(splitted) >= 2 {
					op := splitted[1]
					// Check if numeric (MCCMNC)
					// If numeric string length is 5 or 6, try to resolve
					isNumeric := true
					for _, c := range op {
						if c < '0' || c > '9' {
							isNumeric = false
							break
						}
					}

					if isNumeric && (len(op) == 5 || len(op) == 6) {
						modemName := mccmnc.GetOperatorName(op[:3], op[3:])
						if modemName != "" {
							op = modemName
						}
					}
					operator = op
				}
			}
		}

		// 7. Get Registration Status
		var regStatus string
		resp, err = w.ExecuteAT("AT+CREG?", 2*time.Second)
		if err == nil {
			// +CREG: 0,1
			// Status: 0=Not reg, 1=Home, 2=Search, 3=Denied, 4=Unknown, 5=Roaming
			parts := strings.Split(parseID(resp, "+CREG:"), ",")
			if len(parts) >= 2 {
				stat := strings.TrimSpace(parts[1])
				switch stat {
				case "1":
					regStatus = "Home Network"
				case "5":
					regStatus = "Roaming"
				case "2":
					regStatus = "Searching..."
				case "3":
					regStatus = "Denied"
				case "4":
					regStatus = "Unknown"
				default:
					regStatus = "Not Registered"
				}
			}
		}

		// 8. Register in DB
		modem := &model.Modem{
			ICCID:          iccid,
			IMEI:           imei,
			PortName:       w.PortName,
			Status:         "online",
			SignalStrength: signal,
			Operator:       operator,
			Registration:   regStatus,
			LastSeen:       time.Now(),
		}

		if err := w.repo.Upsert(modem); err != nil {
			logger.Log.Errorf("Failed to save modem %s: %v", iccid, err)
		} else {
			w.modem = modem
			logger.Log.Infof("Modem registered: %s (%s) Op: %s Sig: %d%%", iccid, w.PortName, operator, signal)
		}

	}()
}

func parseID(resp, prefix string) string {
	lines := strings.Split(resp, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return ""
}

func (w *ModemWorker) ExecuteAT(cmd string, timeout time.Duration) (string, error) {
	respChan := make(chan string)
	errChan := make(chan error)
	w.cmdChan <- commandRequest{
		cmd:      cmd,
		respChan: respChan,
		errChan:  errChan,
		timeout:  timeout,
	}

	select {
	case resp := <-respChan:
		return resp, nil
	case err := <-errChan:
		return "", err
	case <-time.After(timeout + 1*time.Second): // Safety buffer
		return "", errors.New("command enqueue timeout")
	}
}

func (w *ModemWorker) ExecuteATSilent(cmd string, timeout time.Duration) (string, error) {
	respChan := make(chan string)
	errChan := make(chan error)
	w.cmdChan <- commandRequest{
		cmd:      cmd,
		respChan: respChan,
		errChan:  errChan,
		timeout:  timeout,
		silent:   true,
	}

	select {
	case resp := <-respChan:
		return resp, nil
	case err := <-errChan:
		return "", err
	case <-time.After(timeout + 1*time.Second): // Safety buffer
		return "", errors.New("command enqueue timeout")
	}
}

func (w *ModemWorker) isURC(line string) bool {
	// List of known URCs
	if strings.HasPrefix(line, "+CMTI:") || strings.HasPrefix(line, "+CREG:") {
		return true
	}
	return false
}

func (w *ModemWorker) handleURC(line string) {
	logger.Log.Infof("[%s] URC: %s", w.PortName, line)
	if strings.HasPrefix(line, "+CMTI:") {
		// Trigger immediate scan
		select {
		case w.triggerChan <- struct{}{}:
			logger.Log.Debugf("[%s] Triggered immediate SMS scan", w.PortName)
		default:
			// Already triggered
		}
	}
}

func (w *ModemWorker) SetBusy(b bool) {
	w.busyMu.Lock()
	w.busy = b
	w.busyMu.Unlock()
}

func (w *ModemWorker) IsBusy() bool {
	w.busyMu.Lock()
	defer w.busyMu.Unlock()
	return w.busy
}

// SetOccupied prevents the polling loop from running while Manual AT commands are active
func (w *ModemWorker) SetOccupied(occupied bool) {
	w.SetBusy(occupied)
}

func (w *ModemWorker) ScanNetworks() ([]string, error) {
	w.SetBusy(true)
	defer w.SetBusy(false)

	// Long timeout for network scan
	// AT+COPS=? resp example: +COPS: (2,"Chunghwa Telecom","CHT","46692",7),(1,"Far EasTone","FET","46601",7),,,(0-4),(0-2)
	resp, err := w.ExecuteAT("AT+COPS=?", 120*time.Second)
	if err != nil {
		return nil, err
	}

	// Simple parser to extract info
	// We want to return a list of formatted strings: "Name (MCCMNC) [Status]"
	// Or better: "Operator Name (46692)" and let frontend handle it.
	// But since we want to look up names if missing...

	// Remove +COPS: prefix
	raw := parseID(resp, "+COPS:")

	// Split by ),(
	// This is a bit tricky with regex or string split.
	// Approximate approach:
	var networks []string

	// Remove outer parens logic isn't perfect given the structure.
	// Regex: \(([^)]+)\)
	re := regexp.MustCompile(`\(([^)]+)\)`)
	matches := re.FindAllStringSubmatch(raw, -1)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		// inner: 2,"Chunghwa Telecom","CHT","46692",7
		parts := strings.Split(match[1], ",")
		if len(parts) < 4 {
			continue // skip status bits like (0-4)
		}

		// Stat: parts[0]
		// Long alphanumeric: parts[1]
		// Short alphanumeric: parts[2]
		// Numeric: parts[3] -> "MCCMNC"

		stat := strings.Trim(parts[0], " ")
		long := strings.Trim(parts[1], "\"")
		// short := strings.Trim(parts[2], "\"")
		numeric := strings.Trim(parts[3], "\"")

		// If long name is empty, lookup using numeric
		if long == "" && len(numeric) >= 5 {
			mcc := numeric[:3]
			mnc := numeric[3:]
			name := mccmnc.GetOperatorName(mcc, mnc)
			if name != "" {
				long = name
			}
		}

		if long == "" {
			long = "Unknown"
		}

		// Decode Status
		// 0: unknown, 1: available, 2: current, 3: forbidden
		statStr := ""
		switch stat {
		case "1":
			statStr = "Available"
		case "2":
			statStr = "Current"
		case "3":
			statStr = "Forbidden"
		default:
			statStr = "Unknown"
		}

		networks = append(networks, fmt.Sprintf("%s (%s) [%s]", long, numeric, statStr))
	}

	if len(networks) == 0 {
		// Fallback to raw if parsing fails
		return []string{resp}, nil
	}

	return networks, nil
}

func (w *ModemWorker) SetOperator(oper string) error {
	w.SetBusy(true)
	defer w.SetBusy(false)

	// Format: AT+COPS=1,0,"oper" (Manual, alphanumeric)
	// Or auto: AT+COPS=0
	cmd := "AT+COPS=0"
	if oper != "" && oper != "AUTO" {
		cmd = fmt.Sprintf("AT+COPS=1,0,\"%s\"", oper)
	}

	_, err := w.ExecuteAT(cmd, 60*time.Second)
	return err
}
