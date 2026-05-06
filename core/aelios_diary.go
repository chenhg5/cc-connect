package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (m *ManagementServer) handleAeliosDiary(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.handleAeliosDiaryList(w, r)
	case http.MethodPost:
		m.handleAeliosDiaryCreate(w, r)
	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleAeliosDiaryList(w http.ResponseWriter, r *http.Request) {
	store, err := m.getAeliosStore("diary")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}

	all, err := ReadAllJSONL[AeliosDiaryEntry](store)
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, "read diary: "+err.Error())
		return
	}

	dateFilter := r.URL.Query().Get("date")
	if dateFilter != "" {
		if !isValidDate(dateFilter) {
			mgmtError(w, http.StatusBadRequest, "invalid date format, expected YYYY-MM-DD")
			return
		}
		var filtered []AeliosDiaryEntry
		for _, e := range all {
			if e.Date == dateFilter {
				filtered = append(filtered, e)
			}
		}
		all = filtered
	}

	if all == nil {
		all = []AeliosDiaryEntry{}
	}
	mgmtJSON(w, http.StatusOK, map[string]any{"entries": all})
}

func (m *ManagementServer) handleAeliosDiaryCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string `json:"type"`
		Content string `json:"content"`
		Date    string `json:"date"`
		Time    string `json:"time,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Content == "" {
		mgmtError(w, http.StatusBadRequest, "content is required")
		return
	}
	if !validDiaryTypes[body.Type] {
		mgmtError(w, http.StatusBadRequest, fmt.Sprintf("invalid type %q; allowed: manual, daily_summary, work, life", body.Type))
		return
	}
	if body.Date == "" {
		mgmtError(w, http.StatusBadRequest, "date is required")
		return
	}
	if !isValidDate(body.Date) {
		mgmtError(w, http.StatusBadRequest, "invalid date format, expected YYYY-MM-DD")
		return
	}

	entry := AeliosDiaryEntry{
		ID:        aeliosNewDiaryID(),
		Type:      body.Type,
		Content:   body.Content,
		Date:      body.Date,
		Time:      body.Time,
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	store, err := m.getAeliosStore("diary")
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := store.AppendJSON(entry); err != nil {
		mgmtError(w, http.StatusInternalServerError, "append diary: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, entry)
}

func (m *ManagementServer) handleAeliosDiaryByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/aelios/diary/")
	if id == "" {
		mgmtError(w, http.StatusBadRequest, "id required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		store, err := m.getAeliosStore("diary")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		all, err := ReadAllJSONL[AeliosDiaryEntry](store)
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "read diary: "+err.Error())
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
		store, err := m.getAeliosStore("diary")
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed, err := DeleteByIDJSONL(store, id, func(e AeliosDiaryEntry) string { return e.ID })
		if err != nil {
			mgmtError(w, http.StatusInternalServerError, "delete diary: "+err.Error())
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
