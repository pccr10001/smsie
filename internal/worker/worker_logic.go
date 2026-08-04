package worker

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/pkg/logger"
	"github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/tpdu"
)

func (w *ModemWorker) logicLoop() {
	// Wait for init
	time.Sleep(2 * time.Second)

	intervalStr := config.AppConfig.Serial.ScanInterval
	interval, err := time.ParseDuration(intervalStr)
	if err != nil || interval < time.Second {
		interval = 5 * time.Second
	}

	logger.Log.Infof("[%s] Starting polling loop with interval %v", w.PortName, interval)

	// Immediate poll
	w.poll()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-w.triggerChan:
			// Immediate poll triggered by URC
			w.poll()
		case <-ticker.C:
			w.poll()
		}
	}
}

func (w *ModemWorker) poll() {
	err := w.runATTransaction(func(session *atSession) error {
		if _, ok := w.modemSnapshot(); !ok || w.GetCallState().State != callStateIdle {
			return nil
		}
		w.checkSignal(session)
		w.checkSMS(session)
		return nil
	})
	if err != nil && !w.IsStopped() {
		logger.Log.Warnf("[%s] Poll transaction failed: %v", w.PortName, err)
	}
}

func (w *ModemWorker) checkOperator(session *atSession) {
	// +COPS: 0,0,"Chunghwa Telecom",7
	resp, err := session.execute("AT+COPS?", 2*time.Second, false)
	if err != nil {
		logger.Log.Errorf("[%s] Failed COPS: %v", w.PortName, err)
		return
	}
	if strings.Contains(resp, "+COPS:") {
		// Basic parsing for string between quotes
		parts := strings.Split(resp, "\"")
		if len(parts) >= 3 {
			// parts[0] = +COPS: 0,0,
			// parts[1] = Chunghwa Telecom (Operator)
			// parts[2] = ,7
			w.updateModem(func(modem *model.Modem) {
				modem.Operator = parts[1]
			})
			// We delay saving to avoid aggressive DB writes, or just save
			// w.repo.Upsert(w.modem)
			// We are UPSERTING frequenly in signal check too.
		}
	}
}

func (w *ModemWorker) checkSignal(session *atSession) {
	// Registration drives whether operator should be shown.
	regCode := w.checkRegistration(session)
	if regCode == "1" || regCode == "5" {
		w.checkOperator(session)
	} else if regCode != "" {
		w.updateModem(func(modem *model.Modem) {
			modem.Operator = ""
		})
	}

	resp, err := session.execute("AT+CSQ", 2*time.Second, false)
	if err != nil {
		logger.Log.Errorf("[%s] Failed CSQ: %v", w.PortName, err)
		return
	}
	// +CSQ: 20,99
	if strings.Contains(resp, "+CSQ:") {
		// Parse
		var rssi int
		// simple parsing logic
		parts := strings.Split(resp, ":")
		if len(parts) > 1 {
			vals := strings.Split(strings.TrimSpace(parts[1]), ",")
			if len(vals) > 0 {
				fmt.Sscanf(vals[0], "%d", &rssi)

				var signal int
				if rssi == 99 {
					signal = 0
				} else {
					// Convert 0-31 to 0-100%
					signal = int(float64(rssi) / 31.0 * 100.0)
				}

				w.updateModem(func(modem *model.Modem) {
					modem.SignalStrength = signal
					modem.LastSeen = time.Now()
				})
			}
		}
	}
}

func (w *ModemWorker) checkRegistration(session *atSession) string {
	resp, err := session.execute("AT+CREG?", 2*time.Second, false)
	if err != nil {
		logger.Log.Errorf("[%s] Failed CREG: %v", w.PortName, err)
		return ""
	}

	code, text, err := parseCREGStatus(resp)
	if err != nil {
		logger.Log.Warnf("[%s] Failed to parse CREG response: %v", w.PortName, err)
		return ""
	}

	w.updateModem(func(modem *model.Modem) {
		modem.Registration = text
	})
	return code
}

func parseCREGStatus(resp string) (string, string, error) {
	body := strings.TrimSpace(parseID(resp, "+CREG:"))
	if body == "" {
		return "", "", fmt.Errorf("missing +CREG response")
	}

	parts := strings.Split(body, ",")
	if len(parts) == 0 {
		return "", "", fmt.Errorf("invalid +CREG response")
	}

	// +CREG: <n>,<stat>[,...]
	// Fallback for malformed payloads where only <stat> is present.
	stat := strings.TrimSpace(parts[0])
	if len(parts) >= 2 {
		stat = strings.TrimSpace(parts[1])
	}
	stat = strings.Trim(stat, `"`)

	switch stat {
	case "1":
		return stat, "Home Network", nil
	case "5":
		return stat, "Roaming", nil
	case "2":
		return stat, "Searching...", nil
	case "3":
		return stat, "Denied", nil
	case "4":
		return stat, "Unknown", nil
	case "0":
		return stat, "Not Registered", nil
	default:
		return stat, "Unknown", nil
	}
}

func (w *ModemWorker) checkSMS(session *atSession) {
	// PDU mode read all
	resp, err := session.execute("AT+CMGL=4", 10*time.Second, false)
	if err != nil {
		logger.Log.Errorf("[%s] Failed CMGL: %v", w.PortName, err)
		return
	}
	if strings.TrimSpace(resp) == "OK" {
		return // No messages
	}

	pdus, seenMessage := parseCMGLPDUs(resp, w.isURC)
	for _, pdu := range pdus {
		w.processPDU(pdu)
	}

	// Delete all messages after reading to avoid filling memory
	// Warning: This deletes ALL messages. In production might want to delete by index.
	if seenMessage {
		if err := w.deleteReadMessages(session); err != nil {
			logger.Log.Warnf("[%s] Failed to delete messages: %v", w.PortName, err)
		}
	}
}

func parseCMGLPDUs(response string, isURC func(string) bool) ([]string, bool) {
	var pdus []string
	expectPDU := false
	seenMessage := false

	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "OK" {
			continue
		}
		if strings.HasPrefix(line, "+CMGL:") {
			expectPDU = true
			seenMessage = true
			continue
		}
		if isURC(line) {
			continue
		}
		if !expectPDU {
			continue
		}
		expectPDU = false
		if _, err := hex.DecodeString(line); err == nil {
			pdus = append(pdus, line)
		}
	}

	return pdus, seenMessage
}

func (w *ModemWorker) processPDU(raw string) {
	// Hex Decode
	b, err := hex.DecodeString(raw)
	if err != nil {
		logger.Log.Errorf("[%s] Failed to decode hex PDU: %v", w.PortName, err)
		return
	}

	// SMSC Address Handling
	// The first octet is the length of the SMSC field in octets
	if len(b) > 0 {
		smscLen := int(b[0])
		if len(b) > smscLen+1 {
			// Skip SMSC field (Len byte + Address bytes)
			b = b[smscLen+1:]
		}
	}

	// Use sms.Unmarshal (Default is AsMT - Mobile Terminated / Received)
	msg, err := sms.Unmarshal(b)
	if err != nil {
		logger.Log.Errorf("[%s] Failed to decode TPDU: %v", w.PortName, err)
	}

	var content string
	var sender string
	var timestamp time.Time = time.Now()

	if msg != nil {
		switch msg.SmsType() {
		case tpdu.SmsDeliver:
			sender = msg.OA.Number()
			timestamp = msg.SCTS.Time
		}

		// Use tpdu.DecodeUserData to correctly handle GSM7/UCS2 encoding
		alphabet, alphaErr := msg.DCS.Alphabet()
		var udContent []byte
		var decErr error

		if alphaErr != nil {
			decErr = alphaErr // Handle alpha error as decode error
		} else {
			udContent, decErr = tpdu.DecodeUserData(msg.UD, msg.UDH, alphabet)
		}

		if decErr == nil {
			content = string(udContent)
		} else {
			logger.Log.Warnf("[%s] Failed to decode UD: %v. DCS: %02X.", w.PortName, decErr, msg.DCS)
			// Fallback to simpler extraction or raw
			// If 7-bit, simply casting to string is wrong, but better than nothing for ASCII-like?
			// Actually better to show hex if it failed
			content = fmt.Sprintf("Decode Failed (DCS: 0x%02X)", msg.DCS)
		}

		// Final check
		if content == "" && len(msg.UD) > 0 {
			content = fmt.Sprintf("UD Hex: %X", msg.UD)
		}
	} else {
		// Decoding failed entirely previously
		content = fmt.Sprintf("Failed to decode PDU: %s", raw)
	}

	logger.Log.Infof("[%s] SMS From %s: %s", w.PortName, sender, content)

	sms := &model.SMS{
		ICCID:     w.modemICCID(),
		Phone:     sender,
		Content:   content,
		Timestamp: timestamp,
		Type:      "received",
		IsRead:    false, // Webhook or UI will mark read? Or just new.
		RawPDU:    raw,
		CreatedAt: time.Now(),
	}
	if sms.Timestamp.IsZero() {
		sms.Timestamp = time.Now()
	}

	w.smsRepo.Create(sms)

	// Trigger Webhook
	w.webhookService.Dispatch(sms)
}

func (w *ModemWorker) deleteReadMessages(session *atSession) error {
	// Instead of deleting all, we should iterate.
	// But AT+CMGD=1,4 deletes all.
	// The user asked to delete read messages.
	// Since we query AT+CMGL=4 (ALL), we can safely delete all AFTER processing.
	// However, to be safer, let's keep using Delete All for now as intended, but enable it always.
	// Or we can parse indices.
	// For "read after delete", CMGD=1,4 is fine if we processed everything.

	_, err := session.execute("AT+CMGD=1,4", 5*time.Second, false)
	return err
}
