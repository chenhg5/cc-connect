package feishu

// enqueueMessageDispatch preserves SDK arrival order within a session without
// blocking the SDK event loop or other sessions. Reserve the predecessor here,
// not inside the goroutine: goroutine scheduling is not FIFO.
//
// The handler hands messages to the engine's processing/queueing path; it does
// not wait for the agent's turn to finish. Keep parsing and that handoff ordered
// together so the engine's create_time watermark cannot overtake a slow lookup.
func (p *Platform) enqueueMessageDispatch(sessionKey string, dispatch func()) {
	done := make(chan struct{})
	p.messageDispatchMu.Lock()
	if p.messageDispatchTails == nil {
		p.messageDispatchTails = make(map[string]chan struct{})
	}
	previous := p.messageDispatchTails[sessionKey]
	p.messageDispatchTails[sessionKey] = done
	p.messageDispatchMu.Unlock()

	go func() {
		defer func() {
			p.messageDispatchMu.Lock()
			if p.messageDispatchTails[sessionKey] == done {
				delete(p.messageDispatchTails, sessionKey)
			}
			close(done)
			p.messageDispatchMu.Unlock()
		}()
		if previous != nil {
			<-previous
		}
		dispatch()
	}()
}
