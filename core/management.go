package core

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProjectSettingsUpdate is passed to SetSaveProjectSettings to persist management API PATCH fields.
// The implementation (typically in cmd/cc-connect) maps this to config.ProjectSettingsUpdate.
type ProjectSettingsUpdate struct {
	Quiet                *bool
	Language             *string
	AdminFrom            *string
	DisabledCommands     []string
	WorkDir              *string
	Mode                 *string
	ShowContextIndicator *bool
	PlatformAllowFrom    map[string]string
}

// ManagementServer provides an HTTP REST API for external management tools
// (web dashboards, TUI clients, GUI desktop apps, Mac tray apps, etc.).
type ManagementServer struct {
	port        int
	token       string
	corsOrigins []string
	server      *http.Server
	startedAt   time.Time

	mu      sync.RWMutex
	engines map[string]*Engine // project name → engine

	cronScheduler      *CronScheduler
	heartbeatScheduler *HeartbeatScheduler
	bridgeServer       *BridgeServer

	setupFeishuSave      func(req FeishuSetupSaveRequest) error
	setupWeixinSave      func(req WeixinSetupSaveRequest) error
	addPlatformToProject func(projectName, platType string, opts map[string]any, workDir, agentType string) error
	removeProject        func(projectName string) error
	saveProjectSettings  func(projectName string, update ProjectSettingsUpdate) error
	getProjectConfig     func(projectName string) map[string]any
	configFilePath       string
}

// NewManagementServer creates a new management API server.
func NewManagementServer(port int, token string, corsOrigins []string) *ManagementServer {
	return &ManagementServer{
		port:        port,
		token:       token,
		corsOrigins: corsOrigins,
		engines:     make(map[string]*Engine),
		startedAt:   time.Now(),
	}
}

func (m *ManagementServer) RegisterEngine(name string, e *Engine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.engines[name] = e
}

func (m *ManagementServer) SetCronScheduler(cs *CronScheduler)           { m.cronScheduler = cs }
func (m *ManagementServer) SetHeartbeatScheduler(hs *HeartbeatScheduler) { m.heartbeatScheduler = hs }
func (m *ManagementServer) SetBridgeServer(bs *BridgeServer)             { m.bridgeServer = bs }
func (m *ManagementServer) SetSetupFeishuSave(fn func(FeishuSetupSaveRequest) error) {
	m.setupFeishuSave = fn
}
func (m *ManagementServer) SetSetupWeixinSave(fn func(WeixinSetupSaveRequest) error) {
	m.setupWeixinSave = fn
}

func (m *ManagementServer) SetAddPlatformToProject(fn func(string, string, map[string]any, string, string) error) {
	m.addPlatformToProject = fn
}

func (m *ManagementServer) SetRemoveProject(fn func(string) error) {
	m.removeProject = fn
}

func (m *ManagementServer) SetConfigFilePath(path string) {
	m.configFilePath = path
}

func (m *ManagementServer) SetSaveProjectSettings(fn func(string, ProjectSettingsUpdate) error) {
	m.saveProjectSettings = fn
}

func (m *ManagementServer) SetGetProjectConfig(fn func(string) map[string]any) {
	m.getProjectConfig = fn
}

func (m *ManagementServer) Start() {
	mux := http.NewServeMux()
	prefix := "/api/v1"

	// System
	mux.HandleFunc(prefix+"/status", m.wrap(m.handleStatus))
	mux.HandleFunc(prefix+"/restart", m.wrap(m.handleRestart))
	mux.HandleFunc(prefix+"/reload", m.wrap(m.handleReload))
	mux.HandleFunc(prefix+"/config", m.wrap(m.handleConfig))

	// Projects
	mux.HandleFunc(prefix+"/projects", m.wrap(m.handleProjects))
	mux.HandleFunc(prefix+"/projects/", m.wrap(m.handleProjectRoutes))

	// Cron (global)
	mux.HandleFunc(prefix+"/cron", m.wrap(m.handleCron))
	mux.HandleFunc(prefix+"/cron/", m.wrap(m.handleCronByID))

	// Setup (QR onboarding for feishu/weixin)
	mux.HandleFunc(prefix+"/setup/feishu/begin", m.wrap(m.handleSetupFeishuBegin))
	mux.HandleFunc(prefix+"/setup/feishu/poll", m.wrap(m.handleSetupFeishuPoll))
	mux.HandleFunc(prefix+"/setup/feishu/save", m.wrap(m.handleSetupFeishuSave))
	mux.HandleFunc(prefix+"/setup/weixin/begin", m.wrap(m.handleSetupWeixinBegin))
	mux.HandleFunc(prefix+"/setup/weixin/poll", m.wrap(m.handleSetupWeixinPoll))
	mux.HandleFunc(prefix+"/setup/weixin/save", m.wrap(m.handleSetupWeixinSave))

	// Bridge
	mux.HandleFunc(prefix+"/bridge/adapters", m.wrap(m.handleBridgeAdapters))

	m.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", m.port),
		Handler: mux,
	}
	go func() {
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("management api server error", "error", err)
		}
	}()
	slog.Info("management api started", "port", m.port)
}

func (m *ManagementServer) Stop() {
	if m.server != nil {
		m.server.Close()
	}
}

// ── Auth & Middleware ──────────────────────────────────────────

func (m *ManagementServer) wrap(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.setCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !m.authenticate(r) {
			mgmtError(w, http.StatusUnauthorized, "unauthorized: missing or invalid token")
			return
		}
		handler(w, r)
	}
}

func (m *ManagementServer) authenticate(r *http.Request) bool {
	if m.token == "" {
		return true
	}
	// Bearer token
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(m.token)) == 1
	}
	// Query param
	if t := r.URL.Query().Get("token"); t != "" {
		return subtle.ConstantTimeCompare([]byte(t), []byte(m.token)) == 1
	}
	return false
}

func (m *ManagementServer) setCORS(w http.ResponseWriter, r *http.Request) {
	if len(m.corsOrigins) == 0 {
		return
	}
	origin := r.Header.Get("Origin")
	for _, o := range m.corsOrigins {
		if o == "*" || o == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			break
		}
	}
}

// ── Response helpers ──────────────────────────────────────────

func mgmtJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data}); err != nil {
		slog.Error("management api: write JSON failed", "error", err)
	}
}

func splitSessionKey(key string) []string {
	return strings.SplitN(key, ":", 3)
}

func mgmtError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg}); err != nil {
		slog.Error("management api: write error JSON failed", "error", err)
	}
}

func mgmtOK(w http.ResponseWriter, msg string) {
	mgmtJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// ── System endpoints ──────────────────────────────────────────

func (m *ManagementServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	platformSet := make(map[string]bool)
	for _, e := range m.engines {
		for _, p := range e.platforms {
			platformSet[p.Name()] = true
		}
	}
	platforms := make([]string, 0, len(platformSet))
	for p := range platformSet {
		platforms = append(platforms, p)
	}

	var adapters []map[string]any
	if m.bridgeServer != nil {
		adapters = m.listBridgeAdapters()
	}

	resp := map[string]any{
		"version":             CurrentVersion,
		"uptime_seconds":      int(time.Since(m.startedAt).Seconds()),
		"connected_platforms": platforms,
		"projects_count":      len(m.engines),
		"bridge_adapters":     adapters,
	}
	if m.bridgeServer != nil {
		resp["bridge"] = map[string]any{
			"enabled": true,
			"port":    m.bridgeServer.port,
			"path":    m.bridgeServer.path,
			"token":   m.bridgeServer.token,
		}
	}
	mgmtJSON(w, http.StatusOK, resp)
}

func (m *ManagementServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		SessionKey string `json:"session_key"`
		Platform   string `json:"platform"`
	}
	// Body is optional; ignore decode errors from empty body
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	select {
	case RestartCh <- RestartRequest{SessionKey: body.SessionKey, Platform: body.Platform}:
		mgmtOK(w, "restart initiated")
	default:
		mgmtError(w, http.StatusConflict, "restart already in progress")
	}
}

func (m *ManagementServer) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var updated []string
	for name, e := range m.engines {
		if e.configReloadFunc != nil {
			if _, err := e.configReloadFunc(); err != nil {
				mgmtError(w, http.StatusInternalServerError, fmt.Sprintf("reload %s: %v", name, err))
				return
			}
			updated = append(updated, name)
		}
	}

	mgmtJSON(w, http.StatusOK, map[string]any{
		"message":          "config reloaded",
		"projects_updated": updated,
	})
}

func (m *ManagementServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	if m.configFilePath == "" {
		mgmtError(w, http.StatusNotFound, "config file path not set")
		return
	}
	data, err := os.ReadFile(m.configFilePath)
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ── Project endpoints ─────────────────────────────────────────

func (m *ManagementServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	projects := make([]map[string]any, 0, len(m.engines))
	for name, e := range m.engines {
		platNames := make([]string, len(e.platforms))
		for i, p := range e.platforms {
			platNames[i] = p.Name()
		}

		sessCount := 0
		e.interactiveMu.Lock()
		sessCount = len(e.interactiveStates)
		e.interactiveMu.Unlock()

		hbEnabled := false
		if m.heartbeatScheduler != nil {
			if st := m.heartbeatScheduler.Status(name); st != nil {
				hbEnabled = st.Enabled
			}
		}

		projects = append(projects, map[string]any{
			"name":              name,
			"agent_type":        e.agent.Name(),
			"platforms":         platNames,
			"sessions_count":    sessCount,
			"heartbeat_enabled": hbEnabled,
		})
	}
	mgmtJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// handleProjectRoutes dispatches /api/v1/projects/{name}/...
func (m *ManagementServer) handleProjectRoutes(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/projects/{name}[/sub[/subsub]]
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) == 0 || parts[0] == "" {
		mgmtError(w, http.StatusBadRequest, "project name required")
		return
	}

	projName := parts[0]
	m.mu.RLock()
	engine, ok := m.engines[projName]
	m.mu.RUnlock()
	if !ok {
		mgmtError(w, http.StatusNotFound, fmt.Sprintf("project not found: %s", projName))
		return
	}

	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	rest := ""
	if len(parts) > 2 {
		rest = parts[2]
	}

	switch sub {
	case "":
		m.handleProjectDetail(w, r, projName, engine)
	case "sessions":
		m.handleProjectSessions(w, r, projName, engine, rest)
	case "send":
		m.handleProjectSend(w, r, engine)
	case "providers":
		m.handleProjectProviders(w, r, engine, rest)
	case "models":
		m.handleProjectModels(w, r, engine)
	case "model":
		m.handleProjectModel(w, r, engine)
	case "heartbeat":
		m.handleProjectHeartbeat(w, r, projName, rest)
	case "users":
		m.handleProjectUsers(w, r, engine)
	case "add-platform":
		m.handleProjectAddPlatform(w, r, projName)
	default:
		mgmtError(w, http.StatusNotFound, "not found")
	}
}

func (m *ManagementServer) handleProjectDetail(w http.ResponseWriter, r *http.Request, name string, e *Engine) {
	if r.Method == http.MethodGet {
		platInfos := make([]map[string]any, len(e.platforms))
		for i, p := range e.platforms {
			platInfos[i] = map[string]any{
				"type":      p.Name(),
				"connected": true,
			}
		}

		e.interactiveMu.Lock()
		sessCount := len(e.interactiveStates)
		keys := make([]string, 0, sessCount)
		for k := range e.interactiveStates {
			keys = append(keys, k)
		}
		e.interactiveMu.Unlock()

		data := map[string]any{
			"name":                name,
			"agent_type":          e.agent.Name(),
			"platforms":           platInfos,
			"sessions_count":      sessCount,
			"active_session_keys": keys,
		}

		if m.heartbeatScheduler != nil {
			if st := m.heartbeatScheduler.Status(name); st != nil {
				data["heartbeat"] = map[string]any{
					"enabled":       st.Enabled,
					"paused":        st.Paused,
					"interval_mins": st.IntervalMins,
					"session_key":   st.SessionKey,
				}
			}
		}

		e.quietMu.RLock()
		quiet := e.quiet
		e.quietMu.RUnlock()

		e.userRolesMu.RLock()
		adminFrom := e.adminFrom
		e.userRolesMu.RUnlock()

		data["settings"] = map[string]any{
			"quiet":             quiet,
			"language":          string(e.i18n.CurrentLang()),
			"admin_from":        adminFrom,
			"disabled_commands": e.GetDisabledCommands(),
		}

		var workDir string
		if wd, ok := e.agent.(interface{ GetWorkDir() string }); ok {
			workDir = wd.GetWorkDir()
		}
		var agentMode string
		if am, ok := e.agent.(interface{ GetMode() string }); ok {
			agentMode = am.GetMode()
		}
		data["work_dir"] = workDir
		data["agent_mode"] = agentMode

		if m.getProjectConfig != nil {
			if extra := m.getProjectConfig(name); extra != nil {
				for k, v := range extra {
					data[k] = v
				}
			}
		}

		mgmtJSON(w, http.StatusOK, data)
		return
	}

	if r.Method == http.MethodPatch {
		var body struct {
			Quiet                 *bool             `json:"quiet"`
			Language              *string           `json:"language"`
			AdminFrom             *string           `json:"admin_from"`
			DisabledCommands      []string          `json:"disabled_commands"`
			WorkDir               *string           `json:"work_dir"`
			Mode                  *string           `json:"mode"`
			ShowContextIndicator  *bool             `json:"show_context_indicator"`
			PlatformAllowFrom     map[string]string `json:"platform_allow_from"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		if body.Quiet != nil {
			e.SetDefaultQuiet(*body.Quiet)
		}
		if body.Language != nil {
			switch *body.Language {
			case "en":
				e.i18n.SetLang(LangEnglish)
			case "zh":
				e.i18n.SetLang(LangChinese)
			case "zh-TW":
				e.i18n.SetLang(LangTraditionalChinese)
			case "ja":
				e.i18n.SetLang(LangJapanese)
			case "es":
				e.i18n.SetLang(LangSpanish)
			}
		}
		if body.AdminFrom != nil {
			e.SetAdminFrom(*body.AdminFrom)
		}
		if body.DisabledCommands != nil {
			e.SetDisabledCommands(body.DisabledCommands)
		}
		if body.WorkDir != nil {
			if switcher, ok := e.agent.(WorkDirSwitcher); ok {
				switcher.SetWorkDir(*body.WorkDir)
			}
		}
		if body.Mode != nil {
			if switcher, ok := e.agent.(ModeSwitcher); ok {
				switcher.SetMode(*body.Mode)
			}
		}
		if body.ShowContextIndicator != nil {
			e.SetShowContextIndicator(*body.ShowContextIndicator)
		}

		if m.saveProjectSettings != nil {
			patch := ProjectSettingsUpdate{
				Quiet:                body.Quiet,
				Language:             body.Language,
				AdminFrom:            body.AdminFrom,
				DisabledCommands:     body.DisabledCommands,
				WorkDir:              body.WorkDir,
				Mode:                 body.Mode,
				ShowContextIndicator: body.ShowContextIndicator,
				PlatformAllowFrom:    body.PlatformAllowFrom,
			}
			if err := m.saveProjectSettings(name, patch); err != nil {
				slog.Warn("management: failed to persist project settings", "project", name, "error", err)
			}
		}

		mgmtOK(w, "settings updated")
		return
	}

	if r.Method == http.MethodDelete {
		if m.removeProject == nil {
			mgmtError(w, http.StatusNotImplemented, "project removal not configured")
			return
		}
		if err := m.removeProject(name); err != nil {
			mgmtError(w, http.StatusInternalServerError, err.Error())
			return
		}
		mgmtJSON(w, http.StatusOK, map[string]any{
			"message":          fmt.Sprintf("project %q removed from config", name),
			"restart_required": true,
		})
		return
	}

	mgmtError(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE only")
}

// ── Users endpoints ──────────────────────────────────────────

func (m *ManagementServer) handleProjectUsers(w http.ResponseWriter, r *http.Request, e *Engine) {
	switch r.Method {
	case http.MethodGet:
		e.userRolesMu.RLock()
		urm := e.userRoles
		e.userRolesMu.RUnlock()
		mgmtJSON(w, http.StatusOK, urm.Snapshot())

	case http.MethodPatch:
		var body struct {
			DefaultRole string                     `json:"default_role"`
			Roles       map[string]json.RawMessage `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}

		var roles []RoleInput
		for name, raw := range body.Roles {
			var rc struct {
				UserIDs          []string `json:"user_ids"`
				DisabledCommands []string `json:"disabled_commands"`
				RateLimit        *struct {
					MaxMessages int `json:"max_messages"`
					WindowSecs  int `json:"window_secs"`
				} `json:"rate_limit"`
			}
			if err := json.Unmarshal(raw, &rc); err != nil {
				mgmtError(w, http.StatusBadRequest, fmt.Sprintf("invalid role %q: %s", name, err))
				return
			}
			ri := RoleInput{
				Name:             name,
				UserIDs:          rc.UserIDs,
				DisabledCommands: rc.DisabledCommands,
			}
			if rc.RateLimit != nil {
				ri.RateLimit = &RateLimitCfg{
					MaxMessages: rc.RateLimit.MaxMessages,
					Window:      time.Duration(rc.RateLimit.WindowSecs) * time.Second,
				}
			}
			roles = append(roles, ri)
		}

		defaultRole := body.DefaultRole
		if defaultRole == "" {
			defaultRole = "member"
		}

		if err := ValidateRoleInputs(defaultRole, roles); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid users config: "+err.Error())
			return
		}

		urm := NewUserRoleManager()
		urm.Configure(defaultRole, roles)
		e.SetUserRoles(urm)

		mgmtOK(w, "users config updated")

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or PATCH only")
	}
}

// ── Session endpoints ─────────────────────────────────────────

func (m *ManagementServer) handleProjectSessions(w http.ResponseWriter, r *http.Request, projName string, e *Engine, rest string) {
	// sub-routes like /sessions/switch
	if rest == "switch" {
		m.handleProjectSessionSwitch(w, r, e)
		return
	}
	if rest != "" {
		m.handleProjectSessionDetail(w, r, e, rest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		activeKeys := make(map[string]string) // sessionKey → platform
		e.interactiveMu.Lock()
		for key, state := range e.interactiveStates {
			pName := ""
			if state.platform != nil {
				pName = state.platform.Name()
			}
			activeKeys[key] = pName
		}
		e.interactiveMu.Unlock()

		idToKey, activeIDs := e.sessions.SessionKeyMap()
		stored := e.sessions.AllSessions()
		sessions := make([]map[string]any, 0, len(stored))
		for _, s := range stored {
			s.mu.Lock()
			histCount := len(s.History)
			var lastMsg map[string]any
			if histCount > 0 {
				last := s.History[histCount-1]
				preview := last.Content
				if len(preview) > 200 {
					preview = preview[:200]
				}
				lastMsg = map[string]any{
					"role":      last.Role,
					"content":   preview,
					"timestamp": last.Timestamp,
				}
			}
			info := map[string]any{
				"id":            s.ID,
				"name":          s.Name,
				"session_key":   idToKey[s.ID],
				"agent_type":    s.AgentType,
				"active":        activeIDs[s.ID],
				"history_count": histCount,
				"created_at":    s.CreatedAt,
				"updated_at":    s.UpdatedAt,
				"last_message":  lastMsg,
			}
			s.mu.Unlock()

			sessionKey := idToKey[s.ID]
			_, live := activeKeys[sessionKey]
			info["live"] = live
			if p, ok := activeKeys[sessionKey]; ok {
				info["platform"] = p
			} else if len(sessionKey) > 0 {
				parts := splitSessionKey(sessionKey)
				if len(parts) > 0 {
					info["platform"] = parts[0]
				}
			}

			if meta := e.sessions.GetUserMeta(sessionKey); meta != nil {
				info["user_name"] = meta.UserName
				info["chat_name"] = meta.ChatName
			}

			sessions = append(sessions, info)
		}

		mgmtJSON(w, http.StatusOK, map[string]any{
			"sessions":    sessions,
			"active_keys": activeKeys,
		})

	case http.MethodPost:
		var body struct {
			SessionKey string `json:"session_key"`
			Name       string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.SessionKey == "" {
			mgmtError(w, http.StatusBadRequest, "session_key is required")
			return
		}

		s := e.sessions.GetOrCreateActive(body.SessionKey)
		if body.Name != "" {
			s.Name = body.Name
		}
		e.sessions.Save()

		mgmtJSON(w, http.StatusOK, map[string]any{
			"session_key": body.SessionKey,
			"name":        s.Name,
		})

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleProjectSessionDetail(w http.ResponseWriter, r *http.Request, e *Engine, sessionID string) {
	switch r.Method {
	case http.MethodGet:
		s := e.sessions.FindByID(sessionID)
		if s == nil {
			mgmtError(w, http.StatusNotFound, "session not found")
			return
		}
		histLimit := 50
		if v := r.URL.Query().Get("history_limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				histLimit = n
			}
		}
		hist := s.GetHistory(histLimit)

		histJSON := make([]map[string]any, len(hist))
		for i, h := range hist {
			histJSON[i] = map[string]any{
				"role":      h.Role,
				"content":   h.Content,
				"timestamp": h.Timestamp,
			}
		}

		idToKey, activeIDs := e.sessions.SessionKeyMap()
		sessionKey := idToKey[s.ID]

		e.interactiveMu.Lock()
		_, live := e.interactiveStates[sessionKey]
		e.interactiveMu.Unlock()

		s.mu.Lock()
		data := map[string]any{
			"id":               s.ID,
			"name":             s.Name,
			"session_key":      sessionKey,
			"agent_session_id": s.AgentSessionID,
			"agent_type":       s.AgentType,
			"active":           activeIDs[s.ID],
			"live":             live,
			"history_count":    len(s.History),
			"created_at":       s.CreatedAt,
			"updated_at":       s.UpdatedAt,
			"history":          histJSON,
		}
		s.mu.Unlock()

		if len(sessionKey) > 0 {
			parts := splitSessionKey(sessionKey)
			if len(parts) > 0 {
				data["platform"] = parts[0]
			}
		}

		mgmtJSON(w, http.StatusOK, data)

	case http.MethodDelete:
		if e.sessions.DeleteByID(sessionID) {
			mgmtOK(w, "session deleted")
		} else {
			mgmtError(w, http.StatusNotFound, "session not found")
		}

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or DELETE only")
	}
}

func (m *ManagementServer) handleProjectSessionSwitch(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		SessionKey string `json:"session_key"`
		SessionID  string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.SessionKey == "" || body.SessionID == "" {
		mgmtError(w, http.StatusBadRequest, "session_key and session_id are required")
		return
	}
	s, err := e.sessions.SwitchSession(body.SessionKey, body.SessionID)
	if err != nil {
		mgmtError(w, http.StatusNotFound, err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, map[string]any{
		"message":           "active session switched",
		"active_session_id": s.ID,
	})
}

func (m *ManagementServer) handleProjectSend(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		SessionKey string `json:"session_key"`
		Message    string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Message == "" {
		mgmtError(w, http.StatusBadRequest, "message is required")
		return
	}
	if err := e.SendToSession(body.SessionKey, body.Message); err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mgmtOK(w, "message sent")
}

// ── Provider endpoints ────────────────────────────────────────

func (m *ManagementServer) handleProjectProviders(w http.ResponseWriter, r *http.Request, e *Engine, rest string) {
	ps, ok := e.agent.(ProviderSwitcher)
	if !ok {
		mgmtError(w, http.StatusBadRequest, "agent does not support provider switching")
		return
	}

	// /providers/{name}/activate
	if rest != "" {
		parts := strings.SplitN(rest, "/", 2)
		provName := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}
		if action == "activate" && r.Method == http.MethodPost {
			if !ps.SetActiveProvider(provName) {
				mgmtError(w, http.StatusNotFound, fmt.Sprintf("provider not found: %s", provName))
				return
			}
			if e.providerSaveFunc != nil {
				_ = e.providerSaveFunc(provName)
			}
			mgmtJSON(w, http.StatusOK, map[string]any{
				"active_provider": provName,
				"message":         "provider activated",
			})
			return
		}
		if r.Method == http.MethodDelete {
			current := ps.GetActiveProvider()
			if current != nil && current.Name == provName {
				mgmtError(w, http.StatusBadRequest, "cannot remove active provider; switch to another first")
				return
			}
			providers := ps.ListProviders()
			var remaining []ProviderConfig
			found := false
			for _, p := range providers {
				if p.Name == provName {
					found = true
					continue
				}
				remaining = append(remaining, p)
			}
			if !found {
				mgmtError(w, http.StatusNotFound, fmt.Sprintf("provider not found: %s", provName))
				return
			}
			ps.SetProviders(remaining)
			if e.providerRemoveSaveFunc != nil {
				_ = e.providerRemoveSaveFunc(provName)
			}
			mgmtOK(w, "provider removed")
			return
		}
		mgmtError(w, http.StatusNotFound, "not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		providers := ps.ListProviders()
		current := ps.GetActiveProvider()
		provList := make([]map[string]any, len(providers))
		activeName := ""
		if current != nil {
			activeName = current.Name
		}
		for i, p := range providers {
			provList[i] = map[string]any{
				"name":     p.Name,
				"active":   p.Name == activeName,
				"model":    p.Model,
				"base_url": p.BaseURL,
			}
		}
		mgmtJSON(w, http.StatusOK, map[string]any{
			"providers":       provList,
			"active_provider": activeName,
		})

	case http.MethodPost:
		var body struct {
			Name     string            `json:"name"`
			APIKey   string            `json:"api_key"`
			BaseURL  string            `json:"base_url"`
			Model    string            `json:"model"`
			Thinking string            `json:"thinking"`
			Env      map[string]string `json:"env"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.Name == "" {
			mgmtError(w, http.StatusBadRequest, "name is required")
			return
		}
		prov := ProviderConfig{
			Name:     body.Name,
			APIKey:   body.APIKey,
			BaseURL:  body.BaseURL,
			Model:    body.Model,
			Thinking: body.Thinking,
			Env:      body.Env,
		}
		providers := ps.ListProviders()
		providers = append(providers, prov)
		ps.SetProviders(providers)
		if e.providerAddSaveFunc != nil {
			_ = e.providerAddSaveFunc(prov)
		}
		mgmtJSON(w, http.StatusOK, map[string]any{
			"name":    body.Name,
			"message": "provider added",
		})

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleProjectModels(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	ms, ok := e.agent.(ModelSwitcher)
	if !ok {
		mgmtError(w, http.StatusBadRequest, "agent does not support model switching")
		return
	}
	models := ms.AvailableModels(r.Context())
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	mgmtJSON(w, http.StatusOK, map[string]any{
		"models":  names,
		"current": ms.GetModel(),
	})
}

func (m *ManagementServer) handleProjectModel(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if _, ok := e.agent.(ModelSwitcher); !ok {
		mgmtError(w, http.StatusBadRequest, "agent does not support model switching")
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Model == "" {
		mgmtError(w, http.StatusBadRequest, "model is required")
		return
	}
	model, err := e.switchModel(body.Model)
	if err != nil {
		mgmtError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, map[string]any{
		"model":   model,
		"message": "model updated",
	})
}

// ── Heartbeat endpoints ───────────────────────────────────────

func (m *ManagementServer) handleProjectHeartbeat(w http.ResponseWriter, r *http.Request, projName, rest string) {
	if m.heartbeatScheduler == nil {
		mgmtError(w, http.StatusServiceUnavailable, "heartbeat scheduler not available")
		return
	}

	switch rest {
	case "", "status":
		if r.Method != http.MethodGet {
			mgmtError(w, http.StatusMethodNotAllowed, "GET only")
			return
		}
		st := m.heartbeatScheduler.Status(projName)
		if st == nil {
			mgmtJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		data := map[string]any{
			"enabled":        st.Enabled,
			"paused":         st.Paused,
			"interval_mins":  st.IntervalMins,
			"only_when_idle": st.OnlyWhenIdle,
			"session_key":    st.SessionKey,
			"silent":         st.Silent,
			"run_count":      st.RunCount,
			"error_count":    st.ErrorCount,
			"skipped_busy":   st.SkippedBusy,
			"last_error":     st.LastError,
		}
		if !st.LastRun.IsZero() {
			data["last_run"] = st.LastRun.Format(time.RFC3339)
		}
		mgmtJSON(w, http.StatusOK, data)

	case "pause":
		if r.Method != http.MethodPost {
			mgmtError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if m.heartbeatScheduler.Pause(projName) {
			mgmtOK(w, "heartbeat paused")
		} else {
			mgmtError(w, http.StatusNotFound, "heartbeat not found for project")
		}

	case "resume":
		if r.Method != http.MethodPost {
			mgmtError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if m.heartbeatScheduler.Resume(projName) {
			mgmtOK(w, "heartbeat resumed")
		} else {
			mgmtError(w, http.StatusNotFound, "heartbeat not found for project")
		}

	case "run":
		if r.Method != http.MethodPost {
			mgmtError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if m.heartbeatScheduler.TriggerNow(projName) {
			mgmtOK(w, "heartbeat triggered")
		} else {
			mgmtError(w, http.StatusNotFound, "heartbeat not found for project")
		}

	case "interval":
		if r.Method != http.MethodPost {
			mgmtError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Minutes int `json:"minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if body.Minutes < 1 {
			mgmtError(w, http.StatusBadRequest, "minutes must be >= 1")
			return
		}
		if m.heartbeatScheduler.SetInterval(projName, body.Minutes) {
			mgmtJSON(w, http.StatusOK, map[string]any{
				"interval_mins": body.Minutes,
				"message":       "interval updated",
			})
		} else {
			mgmtError(w, http.StatusNotFound, "heartbeat not found for project")
		}

	default:
		mgmtError(w, http.StatusNotFound, "not found")
	}
}

// ── Cron endpoints ────────────────────────────────────────────

func (m *ManagementServer) handleCron(w http.ResponseWriter, r *http.Request) {
	if m.cronScheduler == nil {
		mgmtError(w, http.StatusServiceUnavailable, "cron scheduler not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		project := r.URL.Query().Get("project")
		var jobs []*CronJob
		if project != "" {
			jobs = m.cronScheduler.Store().ListByProject(project)
		} else {
			jobs = m.cronScheduler.Store().List()
		}
		mgmtJSON(w, http.StatusOK, map[string]any{"jobs": jobs})

	case http.MethodPost:
		var req CronAddRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.CronExpr == "" {
			mgmtError(w, http.StatusBadRequest, "cron_expr is required")
			return
		}
		if req.Prompt == "" && req.Exec == "" {
			mgmtError(w, http.StatusBadRequest, "either prompt or exec is required")
			return
		}
		if req.Prompt != "" && req.Exec != "" {
			mgmtError(w, http.StatusBadRequest, "prompt and exec are mutually exclusive")
			return
		}

		project := req.Project
		if project == "" {
			m.mu.RLock()
			if len(m.engines) == 1 {
				for name := range m.engines {
					project = name
				}
			}
			m.mu.RUnlock()
		}
		if project == "" {
			mgmtError(w, http.StatusBadRequest, "project is required (multiple projects configured)")
			return
		}

		job := &CronJob{
			ID:          GenerateCronID(),
			Project:     project,
			SessionKey:  req.SessionKey,
			CronExpr:    req.CronExpr,
			Prompt:      req.Prompt,
			Exec:        req.Exec,
			WorkDir:     req.WorkDir,
			Description: req.Description,
			Enabled:     true,
			Silent:      req.Silent,
			SessionMode: NormalizeCronSessionMode(req.SessionMode),
			TimeoutMins: req.TimeoutMins,
			CreatedAt:   time.Now(),
		}
		if err := m.cronScheduler.AddJob(job); err != nil {
			mgmtError(w, http.StatusBadRequest, err.Error())
			return
		}
		mgmtJSON(w, http.StatusOK, job)

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (m *ManagementServer) handleCronByID(w http.ResponseWriter, r *http.Request) {
	if m.cronScheduler == nil {
		mgmtError(w, http.StatusServiceUnavailable, "cron scheduler not available")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/cron/")
	if id == "" {
		mgmtError(w, http.StatusBadRequest, "cron job id required")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if m.cronScheduler.RemoveJob(id) {
			mgmtOK(w, "cron job deleted")
		} else {
			mgmtError(w, http.StatusNotFound, fmt.Sprintf("cron job not found: %s", id))
		}

	case http.MethodPatch:
		var updates map[string]any
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		for field, value := range updates {
			if err := m.cronScheduler.UpdateJob(id, field, value); err != nil {
				mgmtError(w, http.StatusBadRequest, fmt.Sprintf("update %s: %s", field, err.Error()))
				return
			}
		}
		job := m.cronScheduler.Store().Get(id)
		if job == nil {
			mgmtError(w, http.StatusNotFound, "cron job not found after update")
			return
		}
		mgmtJSON(w, http.StatusOK, job)

	default:
		mgmtError(w, http.StatusMethodNotAllowed, "DELETE or PATCH only")
	}
}

// ── Bridge endpoints ──────────────────────────────────────────

func (m *ManagementServer) handleBridgeAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	adapters := m.listBridgeAdapters()
	mgmtJSON(w, http.StatusOK, map[string]any{"adapters": adapters})
}

func (m *ManagementServer) listBridgeAdapters() []map[string]any {
	if m.bridgeServer == nil {
		return nil
	}
	m.bridgeServer.mu.RLock()
	defer m.bridgeServer.mu.RUnlock()

	adapters := make([]map[string]any, 0, len(m.bridgeServer.adapters))
	for name, a := range m.bridgeServer.adapters {
		caps := make([]string, 0, len(a.capabilities))
		for c := range a.capabilities {
			caps = append(caps, c)
		}

		project := ""
		m.bridgeServer.enginesMu.RLock()
		for pName, ref := range m.bridgeServer.engines {
			if ref.platform != nil && ref.platform.Name() == name {
				project = pName
				break
			}
		}
		m.bridgeServer.enginesMu.RUnlock()

		adapters = append(adapters, map[string]any{
			"platform":     name,
			"project":      project,
			"capabilities": caps,
		})
	}
	return adapters
}
