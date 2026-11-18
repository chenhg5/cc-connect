package core

import "net/http"

// SetShutdownHandler connects local shutdown requests to the process owner.
// The API never exits the process itself, and an unset handler rejects requests.
func (s *APIServer) SetShutdownHandler(handler func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownHandler = handler
}

func (s *APIServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		apiJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	s.mu.RLock()
	handler := s.shutdownHandler
	s.mu.RUnlock()
	if handler == nil {
		apiJSON(w, http.StatusNotImplemented, map[string]string{"error": "process owner does not support shutdown"})
		return
	}
	apiJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	s.shutdownOnce.Do(func() { go handler() })
}
