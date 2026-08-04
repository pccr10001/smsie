package worker

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pccr10001/smsie/pkg/logger"
)

// atSession is owned by the worker loop. Commands issued through one
// transaction run consecutively, so modem prompt mode cannot be interrupted.
type atSession struct {
	worker *ModemWorker
}

type atTransaction struct {
	run  func(*atSession) error
	done chan error
}

func (w *ModemWorker) runATTransaction(run func(*atSession) error) error {
	tx := atTransaction{
		run:  run,
		done: make(chan error, 1),
	}

	select {
	case w.transactionChan <- tx:
	case <-w.stop:
		return errors.New("modem worker stopped")
	}

	select {
	case err := <-tx.done:
		return err
	case <-w.stop:
		return errors.New("modem worker stopped")
	}
}

func (w *ModemWorker) ExecuteAT(cmd string, timeout time.Duration) (string, error) {
	return w.executeAT(cmd, timeout, false)
}

func (w *ModemWorker) ExecuteATSilent(cmd string, timeout time.Duration) (string, error) {
	return w.executeAT(cmd, timeout, true)
}

func (w *ModemWorker) executeAT(cmd string, timeout time.Duration, silent bool) (string, error) {
	if requiresInteractiveTransaction(cmd) {
		return "", errors.New("interactive AT input must run in a single modem transaction")
	}

	var response string
	err := w.runATTransaction(func(session *atSession) error {
		var err error
		response, err = session.execute(cmd, timeout, silent)
		return err
	})
	return response, err
}

func requiresInteractiveTransaction(cmd string) bool {
	trimmed := strings.ToUpper(strings.TrimSpace(cmd))
	return strings.HasPrefix(trimmed, "AT+CMGS=") ||
		strings.HasPrefix(trimmed, "AT+CMGW=") ||
		strings.HasSuffix(cmd, "\x1A") ||
		strings.HasSuffix(cmd, "\x1B")
}

func (s *atSession) execute(cmd string, timeout time.Duration, silent bool) (string, error) {
	w := s.worker
	if !silent {
		logger.Log.Debugf("[%s] TX: %s", w.PortName, cmd)
	}

	payload := cmd + "\r"
	if strings.HasSuffix(cmd, "\x1A") || strings.HasSuffix(cmd, "\x1B") {
		payload = cmd
	}
	if _, err := w.port.Write([]byte(payload)); err != nil {
		w.requestReprobe()
		w.Stop()
		return "", err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	response := make([]string, 0, 4)

	for {
		select {
		case <-w.stop:
			return "", errors.New("modem worker stopped")
		case <-timer.C:
			return "", errors.New("timeout")
		case msg := <-w.rxChan:
			if msg.Err != nil {
				logger.Log.Errorf("[%s] Port read error during command: %v. Stopping.", w.PortName, msg.Err)
				w.requestReprobe()
				w.Stop()
				return "", msg.Err
			}

			line := msg.Data
			if !silent {
				logger.Log.Debugf("[%s] RX: %s", w.PortName, line)
			}

			if strings.HasPrefix(line, ">") && strings.HasPrefix(cmd, "AT+CMGS=") {
				return line, nil
			}

			response = append(response, line)
			if w.isURC(line) {
				w.handleURC(line)
			}
			if line == "OK" {
				return strings.Join(response, "\n"), nil
			}
			if strings.Contains(line, "ERROR") {
				return "", fmt.Errorf("modem error: %s", strings.Join(response, "\n"))
			}
		}
	}
}
