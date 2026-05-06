package core

import "net/http"

func (m *ManagementServer) handleAeliosStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	dataDir, err := AeliosDataDir()
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}

	mgmtJSON(w, http.StatusOK, map[string]any{
		"cc_connect":      "online",
		"storage":         "jsonl",
		"data_dir":        dataDir,
		"memory_adapter":  "not_configured",
	})
}
