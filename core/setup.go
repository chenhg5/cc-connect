package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	feishuAccountsBaseURL = "https://accounts.feishu.cn"
	larkAccountsBaseURL   = "https://accounts.larksuite.com"
)

// ── Request types for setup save callbacks ──────────────────

type FeishuSetupSaveRequest struct {
	ProjectName  string `json:"project"`
	AppID        string `json:"app_id"`
	AppSecret    string `json:"app_secret"`
	PlatformType string `json:"platform_type"`
	OwnerOpenID  string `json:"owner_open_id"`
	WorkDir      string `json:"work_dir"`
	AgentType    string `json:"agent_type"`
}

// ── Feishu / Lark QR Setup ──────────────────────────────────

func (m *ManagementServer) handleSetupFeishuBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}

	initResp, err := feishuRegistrationCall(client, feishuAccountsBaseURL, "init", nil)
	if err != nil {
		mgmtError(w, http.StatusBadGateway, "feishu init: "+err.Error())
		return
	}
	if errMsg, _ := initResp["error"].(string); errMsg != "" {
		desc, _ := initResp["error_description"].(string)
		mgmtError(w, http.StatusBadGateway, fmt.Sprintf("feishu init: %s: %s", errMsg, desc))
		return
	}

	beginResp, err := feishuRegistrationCall(client, feishuAccountsBaseURL, "begin", map[string]string{
		"archetype":         "PersonalAgent",
		"auth_method":       "client_secret",
		"request_user_info": "open_id",
	})
	if err != nil {
		mgmtError(w, http.StatusBadGateway, "feishu begin: "+err.Error())
		return
	}
	if errMsg, _ := beginResp["error"].(string); errMsg != "" {
		desc, _ := beginResp["error_description"].(string)
		mgmtError(w, http.StatusBadGateway, fmt.Sprintf("feishu begin: %s: %s", errMsg, desc))
		return
	}

	deviceCode, _ := beginResp["device_code"].(string)
	qrURL, _ := beginResp["verification_uri_complete"].(string)
	interval, _ := beginResp["interval"].(float64)
	expiresIn, _ := beginResp["expire_in"].(float64)
	if deviceCode == "" || qrURL == "" {
		mgmtError(w, http.StatusBadGateway, "feishu begin: incomplete response")
		return
	}

	mgmtJSON(w, http.StatusOK, map[string]any{
		"device_code": deviceCode,
		"qr_url":      qrURL,
		"interval":    int(interval),
		"expires_in":  int(expiresIn),
	})
}

func (m *ManagementServer) handleSetupFeishuPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	var req struct {
		DeviceCode string `json:"device_code"`
		BaseURL    string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DeviceCode == "" {
		mgmtError(w, http.StatusBadRequest, "device_code required")
		return
	}

	baseURL := feishuAccountsBaseURL
	if req.BaseURL != "" {
		baseURL = req.BaseURL
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// Retry up to 2 times to handle feishu→lark base URL auto-switch
	for attempt := 0; attempt < 2; attempt++ {
		pollResp, err := feishuRegistrationCall(client, baseURL, "poll", map[string]string{
			"device_code": req.DeviceCode,
		})
		if err != nil {
			mgmtError(w, http.StatusBadGateway, "feishu poll: "+err.Error())
			return
		}

		if userInfo, ok := pollResp["user_info"].(map[string]any); ok {
			if brand, _ := userInfo["tenant_brand"].(string); strings.EqualFold(brand, "lark") {
				if baseURL != larkAccountsBaseURL {
					baseURL = larkAccountsBaseURL
					continue
				}
			}
		}

		result := map[string]any{
			"status":   "pending",
			"base_url": baseURL,
		}

		clientID, _ := pollResp["client_id"].(string)
		clientSecret, _ := pollResp["client_secret"].(string)
		if clientID != "" && clientSecret != "" {
			platform := "feishu"
			if userInfo, ok := pollResp["user_info"].(map[string]any); ok {
				if brand, _ := userInfo["tenant_brand"].(string); strings.EqualFold(brand, "lark") {
					platform = "lark"
				}
				if oid, _ := userInfo["open_id"].(string); oid != "" {
					result["owner_open_id"] = oid
				}
			}
			result["status"] = "completed"
			result["app_id"] = clientID
			result["app_secret"] = clientSecret
			result["platform"] = platform
			mgmtJSON(w, http.StatusOK, result)
			return
		}

		if errCode, _ := pollResp["error"].(string); errCode != "" {
			switch errCode {
			case "authorization_pending":
				// still pending
			case "slow_down":
				result["slow_down"] = true
			case "access_denied":
				result["status"] = "denied"
			case "expired_token":
				result["status"] = "expired"
			default:
				desc, _ := pollResp["error_description"].(string)
				result["status"] = "error"
				result["error"] = fmt.Sprintf("%s: %s", errCode, desc)
			}
		}

		mgmtJSON(w, http.StatusOK, result)
		return
	}

	mgmtJSON(w, http.StatusOK, map[string]any{"status": "pending", "base_url": baseURL})
}

func (m *ManagementServer) handleSetupFeishuSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req FeishuSetupSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.ProjectName == "" || req.AppID == "" || req.AppSecret == "" {
		mgmtError(w, http.StatusBadRequest, "project, app_id, app_secret required")
		return
	}
	if m.setupFeishuSave == nil {
		mgmtError(w, http.StatusServiceUnavailable, "feishu setup save not configured")
		return
	}
	if err := m.setupFeishuSave(req); err != nil {
		mgmtError(w, http.StatusInternalServerError, "save: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusOK, map[string]any{
		"message":          fmt.Sprintf("feishu platform configured for project %q", req.ProjectName),
		"restart_required": true,
	})
}

func feishuRegistrationCall(client *http.Client, baseURL, action string, params map[string]string) (map[string]any, error) {
	form := url.Values{}
	form.Set("action", action)
	for k, v := range params {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return result, nil
}

// ── Generic platform add (manual config) ─────────────────────

type AddPlatformRequest struct {
	Type      string         `json:"type"`
	Options   map[string]any `json:"options"`
	WorkDir   string         `json:"work_dir"`
	AgentType string         `json:"agent_type"`
}

func (m *ManagementServer) handleProjectAddPlatform(w http.ResponseWriter, r *http.Request, projectName string) {
	if r.Method != http.MethodPost {
		mgmtError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req AddPlatformRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		mgmtError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Type == "" {
		mgmtError(w, http.StatusBadRequest, "type is required")
		return
	}
	if m.addPlatformToProject == nil {
		mgmtError(w, http.StatusServiceUnavailable, "config persistence not available")
		return
	}
	if err := m.addPlatformToProject(projectName, req.Type, req.Options, req.WorkDir, req.AgentType); err != nil {
		mgmtError(w, http.StatusInternalServerError, "save config: "+err.Error())
		return
	}
	mgmtJSON(w, http.StatusCreated, map[string]any{
		"message":          fmt.Sprintf("platform %q added to project %q", req.Type, projectName),
		"restart_required": true,
	})
}
