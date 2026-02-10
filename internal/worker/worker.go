package worker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/internal/logic"
	"github.com/pccr10001/smsie/internal/mccmnc"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/repository"
	"github.com/pccr10001/smsie/pkg/logger"
	"github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/tpdu"
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

	callOpMu sync.Mutex
	callMu   sync.RWMutex
	call     callSnapshot
	uacMu    sync.RWMutex
	uacReady bool
	uacVID   string
	uacPID   string

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

type callSnapshot struct {
	State     string
	Reason    string
	UpdatedAt time.Time
}

const (
	callStateIdle    = "idle"
	callStateDialing = "dialing"
	callStateInCall  = "in_call"
)

var (
	errInvalidDialNumber = errors.New("invalid dial number")
	errCallInProgress    = errors.New("call already in progress")
)

var dialNumberPattern = regexp.MustCompile(`^[0-9*#+]+$`)

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
		call: callSnapshot{
			State:     callStateIdle,
			Reason:    "init",
			UpdatedAt: time.Now(),
		},
		uacReady: false,
		uacVID:   "",
		uacPID:   "",
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
			payload := req.cmd + "\r"
			if strings.HasSuffix(req.cmd, "\x1A") || strings.HasSuffix(req.cmd, "\x1B") {
				payload = req.cmd
			}
			if _, err := w.port.Write([]byte(payload)); err != nil {
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
					} else if strings.HasPrefix(line, ">") {
						if strings.HasPrefix(req.cmd, "AT+CMGS=") {
							req.respChan <- line
							break RespLoop
						}
						continue
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
	buf := make([]byte, 256)
	lineBuf := make([]byte, 0, 256)

	emitLine := func(line string) bool {
		select {
		case w.rxChan <- rxMsg{Data: line}:
			return true
		case <-w.stop:
			return false
		}
	}

	flushLine := func() bool {
		if len(lineBuf) == 0 {
			return true
		}

		line := strings.TrimSpace(string(lineBuf))
		lineBuf = lineBuf[:0]
		if line == "" {
			return true
		}

		return emitLine(line)
	}

	for {
		n, err := w.port.Read(buf)
		if err != nil {
			// Check if we are stopped
			select {
			case <-w.stop:
				return
			default:
			}

			// Handle recoverable read states
			errMsg := strings.ToLower(err.Error())
			if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "no data or error") {
				continue
			}

			// Real Error
			w.rxChan <- rxMsg{Err: err}
			return
		}

		if n == 0 {
			continue
		}

		for _, b := range buf[:n] {
			switch b {
			case '\r':
				continue
			case '\n':
				if !flushLine() {
					return
				}
			case '>':
				if strings.TrimSpace(string(lineBuf)) == "" {
					if !emitLine(">") {
						return
					}
					lineBuf = lineBuf[:0]
					continue
				}
				lineBuf = append(lineBuf, b)
			default:
				lineBuf = append(lineBuf, b)
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

		// Probe UAC status by QCFG USBCFG
		if ok, probeErr := w.probeUACEnabled(); probeErr != nil {
			logger.Log.Warnf("[%s] UAC probe failed: %v", w.PortName, probeErr)
			w.setUACReady(false)
		} else {
			w.setUACReady(ok)
			logger.Log.Infof("[%s] UAC ready: %v", w.PortName, ok)
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

		// 6. Get Registration Status (source of truth)
		var regStatus string
		var regCode string
		resp, err = w.ExecuteAT("AT+CREG?", 2*time.Second)
		if err == nil {
			if code, text, perr := parseCREGStatus(resp); perr == nil {
				regCode = code
				regStatus = text
			} else {
				regStatus = "Unknown"
			}
		}

		// 7. Get Operator only when registered (home/roaming)
		var operator string
		if regCode == "1" || regCode == "5" {
			resp, err = w.ExecuteAT("AT+COPS?", 2*time.Second)
			if err == nil {
				if strings.Contains(resp, "\"") {
					splitted := strings.Split(resp, "\"")
					if len(splitted) >= 2 {
						op := splitted[1]
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
	if w.shouldHandleCallURC(line) {
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
		return
	}

	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "+CREG:") {
		if code, text, err := parseCREGStatus(line); err == nil {
			if w.modem == nil {
				logger.Log.Debugf("[%s] Ignore CREG URC before modem init: %s", w.PortName, line)
			} else {
				w.modem.Registration = text
				if code != "1" && code != "5" {
					w.modem.Operator = ""
				}
				if upsertErr := w.repo.Upsert(w.modem); upsertErr != nil {
					logger.Log.Warnf("[%s] Failed to upsert modem after CREG URC: %v", w.PortName, upsertErr)
				}
			}
		}
	}

	if w.shouldHandleCallURC(line) {
		w.handleCallURC(line)
	}
}

func (w *ModemWorker) shouldHandleCallURC(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	if upper == "" {
		return false
	}

	if upper == "NO CARRIER" || upper == "BUSY" || upper == "NO ANSWER" || upper == "NO DIALTONE" || upper == "RING" {
		return true
	}

	if strings.HasPrefix(upper, "+CLCC:") || strings.HasPrefix(upper, "+QIND:") {
		return true
	}

	return false
}

func (w *ModemWorker) handleCallURC(line string) {
	upper := strings.ToUpper(strings.TrimSpace(line))

	switch {
	case upper == "RING":
		if w.GetCallState().State == callStateIdle {
			w.setCallState(callStateDialing, "ring")
		}
	case upper == "NO CARRIER":
		w.setCallState(callStateIdle, "no_carrier")
	case upper == "BUSY":
		w.setCallState(callStateIdle, "busy")
	case upper == "NO ANSWER":
		w.setCallState(callStateIdle, "no_answer")
	case upper == "NO DIALTONE":
		w.setCallState(callStateIdle, "no_dialtone")
	case strings.HasPrefix(upper, "+CLCC:"):
		state, reason := parseCLCCState(line)
		if state != "" {
			w.setCallState(state, reason)
		}
	}
}

func parseCLCCState(line string) (string, string) {
	idx := strings.Index(line, ":")
	if idx < 0 || idx+1 >= len(line) {
		return "", ""
	}
	body := strings.TrimSpace(line[idx+1:])
	parts := strings.Split(body, ",")
	if len(parts) < 3 {
		return "", ""
	}

	stat := strings.TrimSpace(parts[2])
	// 3GPP TS 27.007: 0 active, 1 held, 2 dialing, 3 alerting, 4 incoming, 5 waiting
	switch stat {
	case "0", "1":
		return callStateInCall, "clcc_active"
	case "2", "3", "4", "5":
		return callStateDialing, "clcc_progress"
	default:
		return "", ""
	}
}

func (w *ModemWorker) GetCallState() callSnapshot {
	w.callMu.RLock()
	defer w.callMu.RUnlock()
	return w.call
}

func (w *ModemWorker) setCallState(state, reason string) {
	if state == "" {
		return
	}

	w.callMu.Lock()
	prev := w.call
	if prev.State == state {
		w.callMu.Unlock()
		return
	}
	w.call.State = state
	w.call.Reason = reason
	w.call.UpdatedAt = time.Now()
	next := w.call
	w.callMu.Unlock()

	logger.Log.Infof("[%s] Call state changed: %s -> %s (%s)", w.PortName, prev.State, next.State, reason)
}

func (w *ModemWorker) Dial(number string) error {
	number = strings.TrimSpace(number)
	if number == "" || !dialNumberPattern.MatchString(number) {
		return errInvalidDialNumber
	}

	w.callOpMu.Lock()
	defer w.callOpMu.Unlock()

	current := w.GetCallState().State
	if current != callStateIdle {
		return errCallInProgress
	}

	w.SetBusy(true)
	defer w.SetBusy(false)

	if _, err := w.ExecuteAT(`AT+QPCMV=1,2`, 5*time.Second); err != nil {
		return fmt.Errorf("enable UAC voice failed: %w", err)
	}

	w.setCallState(callStateDialing, "dial")
	if _, err := w.ExecuteAT("ATD"+number+";", 15*time.Second); err != nil {
		_, _ = w.ExecuteATSilent(`AT+QPCMV=0`, 3*time.Second)
		w.setCallState(callStateIdle, "dial_error")
		return err
	}

	w.setCallState(callStateInCall, "dial_ok")
	return nil
}

func (w *ModemWorker) Hangup() error {
	w.callOpMu.Lock()
	defer w.callOpMu.Unlock()

	w.SetBusy(true)
	defer w.SetBusy(false)

	if _, err := w.ExecuteAT("ATH", 10*time.Second); err != nil {
		return err
	}
	_, _ = w.ExecuteATSilent(`AT+QPCMV=0`, 3*time.Second)

	w.setCallState(callStateIdle, "hangup")
	return nil
}

func (w *ModemWorker) SetBusy(b bool) {
	w.busyMu.Lock()
	w.busy = b
	w.busyMu.Unlock()
}

func (w *ModemWorker) setUACReady(v bool) {
	w.uacMu.Lock()
	w.uacReady = v
	w.uacMu.Unlock()
}

func (w *ModemWorker) setUACIdentity(vid, pid string) {
	w.uacMu.Lock()
	w.uacVID = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(vid, "0x")))
	w.uacPID = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(pid, "0x")))
	w.uacMu.Unlock()
}

func (w *ModemWorker) UACIdentity() (string, string) {
	w.uacMu.RLock()
	defer w.uacMu.RUnlock()
	return w.uacVID, w.uacPID
}

func (w *ModemWorker) IsUACReady() bool {
	w.uacMu.RLock()
	defer w.uacMu.RUnlock()
	return w.uacReady
}

func (w *ModemWorker) probeUACEnabled() (bool, error) {
	resp, err := w.ExecuteAT(`AT+QCFG="usbcfg"`, 5*time.Second)
	if err != nil {
		return false, err
	}

	line := parseID(resp, "+QCFG:")
	if line == "" {
		return false, errors.New("missing +QCFG response")
	}

	if !strings.Contains(strings.ToUpper(line), `"USBCFG"`) {
		return false, errors.New("unexpected QCFG payload")
	}

	idx := strings.Index(line, `,`)
	if idx < 0 || idx+1 >= len(line) {
		return false, errors.New("invalid QCFG format")
	}

	parts := strings.Split(line[idx+1:], ",")
	if len(parts) < 9 {
		return false, errors.New("insufficient QCFG columns")
	}

	vid := strings.TrimSpace(parts[0])
	pid := strings.TrimSpace(parts[1])
	w.setUACIdentity(vid, pid)

	partsLen := len(parts)
	if partsLen < 7 {
		return false, errors.New("insufficient trailing groups")
	}
	trailing := parts[partsLen-7:]
	last := strings.TrimSpace(trailing[6])
	v, parseErr := parseHexOrInt(last)
	if parseErr != nil {
		return false, parseErr
	}

	return v == 1, nil
}

func parseHexOrInt(s string) (int64, error) {
	v := strings.TrimSpace(strings.ToLower(s))
	v = strings.Trim(v, `"`)
	if v == "" {
		return 0, errors.New("empty value")
	}
	if strings.HasPrefix(v, "0x") {
		n, err := strconv.ParseInt(strings.TrimPrefix(v, "0x"), 16, 64)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
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

// SendSMS sends an SMS message using PDU format
func (w *ModemWorker) SendSMS(phoneNumber, message string) error {
	w.SetBusy(true)
	defer w.SetBusy(false)

	if w.modem == nil {
		return errors.New("modem not initialized")
	}

	// Clean phone number (remove spaces, ensure + prefix for international)
	phoneNumber = strings.TrimSpace(phoneNumber)
	if phoneNumber == "" {
		return errors.New("phone number is required")
	}
	if message == "" {
		return errors.New("message is required")
	}

	if _, err := w.ExecuteAT("AT+CMGF=0", 5*time.Second); err != nil {
		return fmt.Errorf("failed to set PDU mode: %w", err)
	}

	// Encode the SMS to PDU format using warthog618/sms library
	// We need to create a SUBMIT TPDU (Mobile Originated)
	tpdus, err := sms.Encode([]byte(message), sms.AsSubmit, sms.To(phoneNumber))
	if err != nil {
		return fmt.Errorf("failed to encode SMS: %w", err)
	}

	logger.Log.Infof("[%s] Sending SMS to %s: %s (PDUs: %d)", w.PortName, phoneNumber, message, len(tpdus))

	// Send each PDU segment
	for i, t := range tpdus {
		// Marshal TPDU to bytes (without SMSC address)
		pduBytes, err := t.MarshalBinary()
		if err != nil {
			return fmt.Errorf("failed to marshal PDU %d: %w", i+1, err)
		}

		// Create PDU with empty SMSC (let modem use default)
		// SMSC length = 0 means use modem's default SMSC
		fullPDU := append([]byte{0x00}, pduBytes...)
		pduHex := strings.ToUpper(hex.EncodeToString(fullPDU))

		// Length for AT+CMGS is the TPDU length excluding SMSC (in bytes)
		tpduLen := len(pduBytes)
		pduCmd := pduHex + "\x1A"

		logger.Log.Debugf("[%s] PDU %d/%d: len=%d, hex=%s", w.PortName, i+1, len(tpdus), tpduLen, pduHex)

		// Step 1: Send AT+CMGS=<length> and wait for ">" prompt
		cmd := fmt.Sprintf("AT+CMGS=%d", tpduLen)
		resp, err := w.ExecuteAT(cmd, 20*time.Second)
		promptReady := false
		if err == nil && strings.Contains(resp, ">") {
			promptReady = true
		}

		if err != nil {
			errText := strings.ToLower(err.Error())
			if strings.Contains(errText, "timeout") {
				logger.Log.Warnf("[%s] CMGS prompt timeout, trying blind PDU submit", w.PortName)
			} else {
				return fmt.Errorf("AT+CMGS failed: %w", err)
			}
		} else if !promptReady {
			logger.Log.Warnf("[%s] CMGS prompt not parsed (%q), trying blind PDU submit", w.PortName, resp)
		}

		// Step 2: Send PDU hex followed by Ctrl+Z (0x1A)
		resp, err = w.ExecuteAT(pduCmd, 60*time.Second)
		if err != nil {
			_, _ = w.ExecuteATSilent("\x1A", 2*time.Second)
			return fmt.Errorf("failed to send PDU: %w", err)
		}

		// Check for +CMGS: <mr> response indicating success
		if !strings.Contains(resp, "+CMGS:") {
			return fmt.Errorf("SMS send failed, response: %s", resp)
		}

		logger.Log.Infof("[%s] PDU %d/%d sent successfully", w.PortName, i+1, len(tpdus))
	}

	logger.Log.Infof("[%s] SMS sent successfully to %s", w.PortName, phoneNumber)
	return nil
}

func (w *ModemWorker) Reboot() error {
	w.SetBusy(true)
	defer w.SetBusy(false)

	w.setCallState(callStateIdle, "reboot")
	w.setUACReady(false)

	_, err := w.ExecuteATSilent("AT+CFUN=1,1", 3*time.Second)
	if err == nil {
		return nil
	}

	errText := strings.ToLower(err.Error())
	if strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "closed") ||
		strings.Contains(errText, "port") {
		return nil
	}

	return err
}

// Helper to get TPDU alphabet type - used for debugging
func getTpduAlphabet(t *tpdu.TPDU) string {
	alpha, err := t.DCS.Alphabet()
	if err != nil {
		return "unknown"
	}
	switch alpha {
	case tpdu.Alpha7Bit:
		return "GSM7"
	case tpdu.Alpha8Bit:
		return "8bit"
	case tpdu.AlphaUCS2:
		return "UCS2"
	default:
		return "unknown"
	}
}
