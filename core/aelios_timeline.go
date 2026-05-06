package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (m *ManagementServer) handleAeliosTimeline(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.handleAeliosTimelineList(w, r)
	case http.MethodPost:
		m.handleAeliosTimelineCreate(w, r)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleAeliosTimelineList(w http.ResponseWriter, r *http.Request) {
	store, err := m.getAeliosStore("timeline")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}

	all, err := ReadAllJSONL[AeliosTimelineEntry](store)
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, "read timeline: "+err.Error())
		return
	}

	dateFilter := r.URL.Query().Get("date")
	if dateFilter != "" {
		if !isValidDate(dateFilter) {
			mgmtError(w, http.StatusBadRequest, "invalid date format, expected YYYY-MM-DD")
			return
		}
		var filtered []AeliosTimelineEntry
		for _, e := range all {
			if e.Date == dateFilter || strings.HasPrefix(e.CreatedAt, dateFilter) {
				filtered = append(filtered, e)
			}
		}
		all = filtered
	}

	if all == nil {
		all = []AeliosTimelineEntry{}
	}
	mgmtJSON(w, http.StatusOK, map[string]any{"entries": all})
}

func (m *ManagementServer) handleAeliosTimelineCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Date    string `json:"date,omitempty"`
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
	if !validTimelineTypes[body.Type] {
		mgmtError(w, http.StatusBadRequest, fmt.Sprintf("invalid type %q; allowed: chat_summary, agent_task, favorite, diary, memory_update, system_event, file_result", body.Type))
		return
	}

	entry := AeliosTimelineEntry{
		ID:        aeliosNewTimelineID(),
		Type:      body.Type,
		Content:   body.Content,
		Date:      body.Date,
		Source:    body.Source,
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	store, err := m.getAeliosStore("timeline")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := store.AppendJSON(entry); err != nil {
		mgmtError(w, http.StatusInternalServerError, "append timeline: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, entry)
}

func (m *ManagementServer) handleAeliosTimelineByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/aelios/timeline/")
	if id == "" {
		mgmtError(w, http.StatusBadRequest, "id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		store, err := m.getAeliosStore("timeline")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		all, err := ReadAllJSONL[AeliosTimelineEntry](store)
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "read timeline: "+err.Error())
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
		store, err := m.getAeliosStore("timeline")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed, err := DeleteByIDJSONL(store, id, func(e AeliosTimelineEntry) string { return e.ID })
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "delete timeline: "+err.Error())
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
