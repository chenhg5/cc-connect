package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (m *ManagementServer) handleAeliosSaved(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.handleAeliosSavedList(w, r)
	case http.MethodPost:
		m.handleAeliosSavedCreate(w, r)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleAeliosSavedList(w http.ResponseWriter, r *http.Request) {
	store, err := m.getAeliosStore("saved")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}

	all, err := ReadAllJSONL[AeliosSavedEntry](store)
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, "read saved: "+err.Error())
		return
	}

	if all == nil {
		all = []AeliosSavedEntry{}
	}
	mgmtJSON(w, http.StatusOK, map[string]any{"entries": all})
}

func (m *ManagementServer) handleAeliosSavedCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Source  string `json:"source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Content == "" {
		mgmtError(w, http.StatusBadRequest, "content is required")
		return
	}
	if !validSavedTypes[body.Type] {
		mgmtError(w, http.StatusBadRequest, fmt.Sprintf("invalid type %q; allowed: text, link", body.Type))
		return
	}

	entry := AeliosSavedEntry{
		ID:        aeliosNewSavedID(),
		Type:      body.Type,
		Content:   body.Content,
		Source:    body.Source,
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	store, err := m.getAeliosStore("saved")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := store.AppendJSON(entry); err != nil {
		mgmtError(w, http.StatusInternalServerError, "append saved: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, entry)
}

func (m *ManagementServer) handleAeliosSavedByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/aelios/saved/")
	if id == "" {
		mgmtError(w, http.StatusBadRequest, "id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		store, err := m.getAeliosStore("saved")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		all, err := ReadAllJSONL[AeliosSavedEntry](store)
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "read saved: "+err.Error())
			return
		}
		for _, e := range all {
			if e.ID == id {
				mgmtJSON(w, http.StatusOK, e)
				return
			}
		}
		mgmtError(w, http.StatusNotFound, "not found")

	case http.MethodDelete:
		store, err := m.getAeliosStore("saved")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed, err := DeleteByIDJSONL(store, id, func(e AeliosSavedEntry) string { return e.ID })
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "delete saved: "+err.Error())
			return
		}
		if !removed {
			mgmtError(w, http.StatusNotFound, "not found")
			return
		}
		mgmtOK(w, "deleted")

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or DELETE only")
	}
}
