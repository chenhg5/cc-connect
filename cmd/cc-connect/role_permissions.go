package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// Application grants and personal OAuth are separate. Only the manager needs
// the label user scopes; coding roles operate on the local workspace.
type roleScope struct {
	Name   string `json:"scope_name"`
	Type   string `json:"scope_type"`
	Status int    `json:"grant_status"`
}
type rolePermissionReport struct {
	Checked   bool
	Submitted bool
	Missing   []roleScope
	Error     string
}

type rolePermissionClient struct {
	base    string
	http    *http.Client
	dataDir string
}

func rolePermissionScopes(role string) ([]roleScope, error) {
	if role != "" {
		if _, err := core.AgentRoleByID(role); err != nil {
			return nil, err
		}
	}
	names := []string{"im:message.group_at_msg:readonly", "im:message.p2p_msg:readonly", "im:message:send_as_bot", "im:message:readonly", "im:message:update", "im:message.reactions:write_only", "im:resource", "im:chat:read", "im:chat.members:bot_access", "cardkit:card:write", "application:bot.basic_info:read", "im:message.group_at_msg.include_bot:readonly"}
	// Every role confirms its own membership through members/is_in_chat. The
	// bot_access grant alone does not authorize this read endpoint.
	names = append(names, "im:chat.members:read")
	if role == "manager" {
		names = append(names, "im:message.group_msg", "im:chat:create", "im:chat:update", "im:chat.members:write_only", "im:message.group_msg.include_bot:read")
	}
	var scopes []roleScope
	for _, n := range names {
		scopes = append(scopes, roleScope{Name: n, Type: "tenant"})
	}
	if role == "manager" {
		for _, n := range strings.Fields("im:feed_group_v1:read im:feed_group_v1:write offline_access") {
			scopes = append(scopes, roleScope{Name: n, Type: "user"})
		}
	}
	return scopes, nil
}

func permissionBase(platform string) string {
	if platform == "lark" {
		return openLarkBaseURL
	}
	return openFeishuBaseURL
}

func permissionLinks(id registrationFlowResult, missing []roleScope) string {
	var lines []string
	for _, kind := range []string{"tenant", "user"} {
		var names []string
		for _, s := range missing {
			if s.Type == kind {
				names = append(names, s.Name)
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		// The console's official auth link preserves the requested identity type.
		q := url.Values{"q": {strings.Join(names, ",")}, "token_type": {kind}, "op_from": {"openapi"}}
		lines = append(lines, permissionBase(id.Platform)+"/app/"+url.PathEscape(id.AppID)+"/auth?"+q.Encode())
	}
	return strings.Join(lines, "\n")
}

// Do not surface server messages, URLs or HTTP errors containing credentials.
func (c *rolePermissionClient) request(ctx context.Context, method, path, token string, body any, out any) error {
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode_request")
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, input)
	if err != nil {
		return fmt.Errorf("invalid_request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request_failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read_failed")
	}
	var envelope struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(b, &envelope) != nil || envelope.Code == nil {
		return fmt.Errorf("invalid_response")
	}
	if resp.StatusCode != 200 || *envelope.Code != 0 {
		return fmt.Errorf("api_code_%d", *envelope.Code)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("invalid_response")
	}
	return nil
}

// Apply has NO scope-selection body: it requests all configured ungranted
// scopes. Call it only when every ungranted scope belongs to this role. Scopes
// not yet configured require the preselected console links instead.
func (c *rolePermissionClient) ensure(ctx context.Context, id registrationFlowResult, role string) rolePermissionReport {
	wanted, err := rolePermissionScopes(role)
	report := rolePermissionReport{Missing: wanted}
	if err != nil {
		report.Error = "invalid_role"
		return report
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var auth struct {
		Token string `json:"tenant_access_token"`
	}
	if err := c.request(ctx, "POST", "/open-apis/auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": id.AppID, "app_secret": id.AppSecret}, &auth); err != nil || auth.Token == "" {
		report.Error = "token_failed"
		return report
	}
	var res struct {
		Data *struct {
			Scopes []roleScope `json:"scopes"`
		} `json:"data"`
	}
	if err := c.request(ctx, "GET", "/open-apis/application/v6/scopes", auth.Token, nil, &res); err != nil || res.Data == nil {
		report.Error = "scope_check_failed"
		return report
	}
	report.Checked = true
	report.Missing = nil
	key := func(s roleScope) string { return s.Type + ":" + s.Name }
	desired, granted := map[string]bool{}, map[string]bool{}
	for _, s := range wanted {
		desired[key(s)] = true
	}
	canApply, configuredMissing := true, false
	for _, s := range res.Data.Scopes {
		if s.Status == 1 {
			granted[key(s)] = true
			continue
		}
		if !desired[key(s)] {
			canApply = false
		} else {
			configuredMissing = true
		}
	}
	for _, s := range wanted {
		if !granted[key(s)] {
			report.Missing = append(report.Missing, s)
		}
	}
	if len(report.Missing) == 0 || !canApply || !configuredMissing {
		return report
	}
	// A durable cooldown prevents retries/restarts from notifying administrators
	// repeatedly. Reserve before POST because a timeout may still mean submitted.
	sum := sha256.Sum256([]byte(c.base + "\x00" + id.AppID))
	if c.dataDir == "" {
		report.Error = "state_unavailable"
		return report
	}
	path := filepath.Join(c.dataDir, "role-permissions", fmt.Sprintf("%x.json", sum))
	release, err := core.LockPrivateFile(path)
	if err != nil {
		report.Error = "request_busy"
		return report
	}
	defer release()
	var stamp struct {
		At          time.Time
		Submitted   bool
		Fingerprint string
	}
	var pending []string
	for _, scope := range res.Data.Scopes {
		if scope.Status != 1 {
			pending = append(pending, key(scope))
		}
	}
	sort.Strings(pending)
	fingerprint := strings.Join(pending, "\n")
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &stamp) != nil {
			report.Error = "state_invalid"
			return report
		}
		if time.Since(stamp.At) < 15*time.Minute {
			report.Submitted = stamp.Submitted && stamp.Fingerprint == fingerprint
			return report
		}
	} else if !os.IsNotExist(err) {
		report.Error = "state_unavailable"
		return report
	}
	stamp.At = time.Now()
	stamp.Fingerprint = fingerprint
	stamp.Submitted = false
	if err := writePermissionStamp(path, stamp); err != nil {
		report.Error = "state_unavailable"
		return report
	}
	var applied struct {
		Code int `json:"code"`
	}
	if err := c.request(ctx, "POST", "/open-apis/application/v6/scopes/apply", auth.Token, nil, &applied); err != nil {
		report.Error = "apply_failed"
		return report
	}
	report.Submitted = true
	stamp.Submitted = true
	if err := writePermissionStamp(path, stamp); err != nil {
		report.Error = "state_unavailable"
	}
	return report
}

func writePermissionStamp(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".permission-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func ensureRolePermissions(ctx context.Context, id registrationFlowResult, role, dataDir string) rolePermissionReport {
	client := &rolePermissionClient{base: permissionBase(id.Platform), dataDir: dataDir, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return client.ensure(ctx, id, role)
}

func permissionNotice(id registrationFlowResult, role string, report rolePermissionReport, notify func(core.MsgKey, ...any)) {
	if report.Error != "" {
		slog.Warn("role permission setup incomplete", "role", role, "reason", report.Error)
	}
	if report.Checked && len(report.Missing) == 0 {
		notify(core.MsgRolePermissionsReady, role)
		if role == "manager" {
			notify(core.MsgRolePermissionsUser)
		}
		return
	}
	key := core.MsgRolePermissionsPending
	if !report.Checked {
		key = core.MsgRolePermissionsUnknown
	} else if report.Error != "" {
		key = core.MsgRolePermissionsApplyFailed
	} else if report.Submitted {
		key = core.MsgRolePermissionsSubmitted
	}
	notify(key, role, permissionLinks(id, report.Missing))
	if role == "manager" {
		notify(core.MsgRolePermissionsUser)
	}
}

// Available in the manager's private chat for existing bots and pending grants.
func (s *teamProjectService) permissions(ctx context.Context, p core.Platform, msg *core.Message, cfg *config.Config, team config.TeamConfig, args []string) (string, error) {
	i := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
	private, ok := p.(core.PrivateUserMessageDetector)
	if !ok || !private.IsPrivateUserMessage(msg) {
		return "", fmt.Errorf("%s", i.T(core.MsgRolePermissionsUsage))
	}
	if len(args) == 0 {
		card := core.NewCard().Title(i.T("team permissions"), "blue")
		for _, m := range team.Members {
			card.ListItem(memberDisplayName(m)+" · "+m.Role, i.T("team permissions"), "cmd:/team permissions "+m.Bot)
		}
		card.Divider().Buttons(core.DefaultBtn(i.T(core.MsgCardBack), "nav:/help team"))
		panel := card.Build()
		panel.Reusable = true
		if sender, ok := p.(core.CardSender); ok {
			return "", sender.ReplyCard(ctx, msg.ReplyCtx, panel)
		}
		return panel.RenderText() + "\n" + i.T(core.MsgRolePermissionsUsage), nil
	}
	if len(args) != 1 {
		return "", fmt.Errorf("%s", i.T(core.MsgRolePermissionsUsage))
	}
	target := args[0]
	role := ""
	for _, m := range team.Members {
		if m.Bot == target {
			role = m.Role
		}
	}
	if role == "" {
		return "", fmt.Errorf("%s", i.T(core.MsgTeamMemberNotFound))
	}
	_, pc, err := teamBotPlatform(cfg, target, p.Name())
	if err != nil {
		return "", err
	}
	id := registrationFlowResult{Platform: pc.Type}
	id.AppID, _ = pc.Options["app_id"].(string)
	id.AppSecret, _ = pc.Options["app_secret"].(string)
	report := s.checkRolePermissions(ctx, id, role)
	var lines []string
	permissionNotice(id, role, report, func(k core.MsgKey, a ...any) { lines = append(lines, i.Tf(k, a...)) })
	return strings.Join(lines, "\n\n"), nil
}

func (s *teamProjectService) checkRolePermissions(ctx context.Context, id registrationFlowResult, role string) rolePermissionReport {
	if s.permissionCheck != nil {
		return s.permissionCheck(ctx, id, role)
	}
	return ensureRolePermissions(ctx, id, role, s.dataDir)
}
