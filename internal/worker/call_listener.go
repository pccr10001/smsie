package worker

type CallStateListener func(w *ModemWorker, state CallState)

func (m *Manager) AddCallStateListener(listener CallStateListener) func() {
	if m == nil || listener == nil {
		return func() {}
	}

	m.mu.Lock()
	id := m.nextCallStateListenerID
	m.nextCallStateListenerID++
	m.callStateListeners[id] = listener
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		delete(m.callStateListeners, id)
		m.mu.Unlock()
	}
}

func (m *Manager) notifyCallStateChanged(w *ModemWorker, state CallState) {
	if m == nil {
		return
	}

	m.mu.RLock()
	listeners := make([]CallStateListener, 0, len(m.callStateListeners))
	for _, listener := range m.callStateListeners {
		listeners = append(listeners, listener)
	}
	m.mu.RUnlock()

	for _, listener := range listeners {
		listener(w, state)
	}
}
