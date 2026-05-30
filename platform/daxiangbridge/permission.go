package daxiangbridge

import "sync"

type permissionResult struct {
	permissionID string
	requestID    string
	sessionID    string
	decision     string
}

type pendingPermissions struct {
	mu      sync.Mutex
	pending map[string]chan permissionResult
	meta    map[string]permissionResult
}

func newPendingPermissions() *pendingPermissions {
	return &pendingPermissions{
		pending: make(map[string]chan permissionResult),
		meta:    make(map[string]permissionResult),
	}
}

func (p *pendingPermissions) register(permissionID, requestID, sessionID string) <-chan permissionResult {
	ch := make(chan permissionResult, 1)
	p.mu.Lock()
	p.pending[permissionID] = ch
	p.meta[permissionID] = permissionResult{
		permissionID: permissionID,
		requestID:    requestID,
		sessionID:    sessionID,
	}
	p.mu.Unlock()
	return ch
}

func (p *pendingPermissions) resolve(permissionID, decision string) {
	p.mu.Lock()
	ch, ok := p.pending[permissionID]
	meta := p.meta[permissionID]
	if ok {
		delete(p.pending, permissionID)
		delete(p.meta, permissionID)
	}
	p.mu.Unlock()
	if ok {
		ch <- permissionResult{
			permissionID: permissionID,
			requestID:    meta.requestID,
			sessionID:    meta.sessionID,
			decision:     decision,
		}
	}
}
