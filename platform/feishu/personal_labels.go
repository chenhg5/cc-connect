package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const labelScopes = "im:feed_group_v1:read im:feed_group_v1:write offline_access"
const labelTokenPath = "/open-apis/authen/v2/oauth/token"

// One credential store per application and user. It is independent of agent
// CLI credentials and cannot delete or modify their keychain entries.
type personalLabelClient struct {
	app, secret, base, accounts, dir string
	http                             *http.Client
}
type labelToken struct {
	User           string    `json:"user"`
	Access         string    `json:"access_token"`
	Refresh        string    `json:"refresh_token"`
	Expires        time.Time `json:"expires"`
	RefreshExpires time.Time `json:"refresh_expires"`
}

var labelCredentialLocks sync.Map

func labelLock(path string) *sync.Mutex {
	value, _ := labelCredentialLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}
func labelErr(code string) error { return &core.PersonalLabelError{Code: code} }
func (c *personalLabelClient) path(user string) string {
	sum := sha256.Sum256([]byte(c.base + "\x00" + c.app + "\x00" + user))
	return filepath.Join(c.dir, fmt.Sprintf("%x.json", sum))
}
func writeLabelJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".label-")
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
func (c *personalLabelClient) read(user string) (labelToken, error) {
	var t labelToken
	if c.dir == "" || user == "" {
		return t, labelErr("authorization_required")
	}
	b, err := os.ReadFile(c.path(user))
	if err != nil {
		return t, labelErr("authorization_required")
	}
	if json.Unmarshal(b, &t) != nil || t.User != user {
		return t, labelErr("authorization_required")
	}
	return t, nil
}
func (p *Platform) PersonalLabelsAuthorized(user string) bool {
	if p.labels == nil {
		return false
	}
	t, err := p.labels.read(user)
	return err == nil && ((t.Access != "" && time.Now().Before(t.Expires)) || (t.Refresh != "" && time.Now().Before(t.RefreshExpires)))
}

// Never return raw response bodies, URL query strings or OAuth descriptions:
// they may contain tokens. Only protocol error codes are exposed.
func (c *personalLabelClient) request(ctx context.Context, method, endpoint, token string, body any, form bool, out any) error {
	var reader io.Reader
	contentType := "application/json"
	if form {
		reader = strings.NewReader(body.(url.Values).Encode())
		contentType = "application/x-www-form-urlencoded"
	} else if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return labelErr("request_failed")
	}
	req.Header.Set("Content-Type", contentType)
	if token == "basic" {
		req.SetBasicAuth(c.app, c.secret)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return labelErr("network_error")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return labelErr("invalid_response")
	}
	var envelope struct {
		Code  int             `json:"code"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return labelErr("invalid_response")
	}
	if envelope.Error != "" {
		switch envelope.Error {
		case "authorization_pending", "slow_down", "access_denied", "expired_token", "invalid_grant", "invalid_client", "invalid_scope":
			return labelErr(envelope.Error)
		default:
			return labelErr("oauth_failed")
		}
	}
	if envelope.Code != 0 {
		return labelErr(fmt.Sprintf("api_%d", envelope.Code))
	}
	if resp.StatusCode >= 300 {
		return labelErr(fmt.Sprintf("http_%d", resp.StatusCode))
	}
	if out == nil {
		return nil
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		raw = envelope.Data
	}
	if json.Unmarshal(raw, out) != nil {
		return labelErr("invalid_response")
	}
	return nil
}

type labelTokenResponse struct {
	Access         string `json:"access_token"`
	Refresh        string `json:"refresh_token"`
	Expires        int    `json:"expires_in"`
	RefreshExpires int    `json:"refresh_token_expires_in"`
	Scope          string `json:"scope"`
}

func (c *personalLabelClient) verifyUser(ctx context.Context, token, user string) error {
	var data struct {
		OpenID string `json:"open_id"`
	}
	if err := c.request(ctx, "GET", c.base+"/open-apis/authen/v1/user_info", token, nil, false, &data); err != nil {
		return err
	}
	if data.OpenID != user {
		return labelErr("wrong_user")
	}
	return nil
}
func (c *personalLabelClient) saveToken(user string, r labelTokenResponse, old labelToken) error {
	if r.Access == "" || r.Expires <= 0 {
		return labelErr("invalid_response")
	}
	t := labelToken{User: user, Access: r.Access, Refresh: r.Refresh, Expires: time.Now().Add(time.Duration(r.Expires) * time.Second)}
	if r.Refresh == "" {
		t.Refresh = old.Refresh
		t.RefreshExpires = old.RefreshExpires
	} else if r.RefreshExpires > 0 {
		t.RefreshExpires = time.Now().Add(time.Duration(r.RefreshExpires) * time.Second)
	}
	if err := writeLabelJSON(c.path(user), t); err != nil {
		return labelErr("save_failed")
	}
	return nil
}
func (p *Platform) AuthorizePersonalLabels(ctx context.Context, user string, notify func(string) error) error {
	c := p.labels
	if c == nil || c.dir == "" || user == "" {
		return labelErr("unavailable")
	}
	lock := labelLock(c.path(user))
	if !lock.TryLock() {
		return labelErr("busy")
	}
	defer lock.Unlock()
	release, err := core.LockPrivateFile(c.path(user))
	if err != nil {
		return labelErr("busy")
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var device struct {
		Code     string `json:"device_code"`
		URI      string `json:"verification_uri_complete"`
		Expires  int    `json:"expires_in"`
		Interval int    `json:"interval"`
	}
	if err := c.request(ctx, "POST", c.accounts+"/oauth/v1/device_authorization", "basic", url.Values{"client_id": {c.app}, "scope": {labelScopes}}, true, &device); err != nil {
		return err
	}
	link, err := url.Parse(device.URI)
	if err != nil || link.Scheme != "https" || device.Code == "" || device.Expires <= 0 {
		return labelErr("invalid_response")
	}
	if err := notify(device.URI); err != nil {
		return err
	}
	deadline := time.Now().Add(time.Duration(device.Expires) * time.Second)
	interval := time.Duration(max(1, device.Interval)) * time.Second
	for time.Now().Before(deadline) {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return labelErr("expired_token")
		case <-timer.C:
		}
		var r labelTokenResponse
		err := c.request(ctx, "POST", c.base+labelTokenPath, "", url.Values{"client_id": {c.app}, "client_secret": {c.secret}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {device.Code}}, true, &r)
		if err != nil {
			if e, ok := err.(*core.PersonalLabelError); ok {
				switch e.Code {
				case "authorization_pending":
					continue
				case "slow_down":
					interval = min(interval+5*time.Second, time.Minute)
					continue
				}
			}
			return err
		}
		if err := c.verifyUser(ctx, r.Access, user); err != nil {
			return err
		}
		if err := c.saveToken(user, r, labelToken{}); err != nil {
			return err
		}
		return nil
	}
	return labelErr("expired_token")
}
func (c *personalLabelClient) token(ctx context.Context, user string) (string, error) {
	lock := labelLock(c.path(user))
	if !lock.TryLock() {
		return "", labelErr("busy")
	}
	defer lock.Unlock()
	release, err := core.LockPrivateFile(c.path(user))
	if err != nil {
		return "", labelErr("busy")
	}
	defer release()
	old, err := c.read(user)
	if err != nil {
		return "", err
	}
	if old.Access != "" && time.Now().Add(time.Minute).Before(old.Expires) {
		return old.Access, nil
	}
	if old.Refresh == "" || !time.Now().Before(old.RefreshExpires) {
		return "", labelErr("authorization_required")
	}
	var r labelTokenResponse
	err = c.request(ctx, "POST", c.base+labelTokenPath, "", url.Values{"client_id": {c.app}, "client_secret": {c.secret}, "grant_type": {"refresh_token"}, "refresh_token": {old.Refresh}}, true, &r)
	if err != nil {
		return "", err
	} // Preserve credentials on transient failures.
	if err = c.saveToken(user, r, old); err != nil {
		return "", err
	}
	return r.Access, nil
}
func (p *Platform) labelAPI(ctx context.Context, user, method, path string, body, out any) error {
	if p.labels == nil {
		return labelErr("unavailable")
	}
	token, err := p.labels.token(ctx, user)
	if err != nil {
		return err
	}
	return p.labels.request(ctx, method, p.labels.base+path, token, body, false, out)
}
func (p *Platform) ListPersonalLabels(ctx context.Context, user string) ([]core.PersonalLabel, error) {
	var labels []core.PersonalLabel
	page := ""
	seen := map[string]bool{}
	for n := 0; n < 100; n++ {
		var data struct {
			Groups []core.PersonalLabel `json:"groups"`
			More   bool                 `json:"has_more"`
			Page   string               `json:"page_token"`
		}
		err := p.labelAPI(ctx, user, "GET", "/open-apis/im/v1/groups?page_size=50&page_token="+url.QueryEscape(page), nil, &data)
		if err != nil {
			return nil, err
		}
		for _, g := range data.Groups {
			if g.Type == "normal" && g.ID != "" {
				labels = append(labels, g)
			}
		}
		if !data.More {
			return labels, nil
		}
		if data.Page == "" || seen[data.Page] {
			return nil, labelErr("invalid_response")
		}
		seen[data.Page] = true
		page = data.Page
	}
	return nil, labelErr("too_many_labels")
}
func (p *Platform) CreatePersonalLabel(ctx context.Context, user, name string) (core.PersonalLabel, error) {
	var data struct {
		ID string `json:"group_id"`
	}
	err := p.labelAPI(ctx, user, "POST", "/open-apis/im/v1/groups", map[string]any{"feed_group_creator": map[string]string{"type": "normal", "name": name}}, &data)
	if err == nil && data.ID == "" {
		err = labelErr("invalid_response")
	}
	return core.PersonalLabel{ID: data.ID, Name: name, Type: "normal"}, err
}
func (p *Platform) AddPersonalLabel(ctx context.Context, user, chat, id string) error {
	if chat == "" || id == "" {
		return labelErr("invalid_target")
	}
	var data struct {
		Failed []json.RawMessage `json:"failed_items"`
	}
	err := p.labelAPI(ctx, user, "POST", "/open-apis/im/v1/groups/"+url.PathEscape(id)+"/batch_add_item", map[string]any{"items": []map[string]string{{"feed_id": chat, "feed_type": "chat"}}}, &data)
	if err == nil && len(data.Failed) > 0 {
		return labelErr("add_failed")
	}
	return err
}
func (p *Platform) OpenLabelConversation(ctx context.Context, user, uuid, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(user).MsgType("text").Content(string(content)).Uuid(uuid).Build()).Build()
	resp, err := p.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", labelErr("network_error")
	}
	if !resp.Success() {
		return "", labelErr(fmt.Sprintf("api_%d", resp.Code))
	}
	if resp.Data == nil || resp.Data.ChatId == nil || *resp.Data.ChatId == "" {
		return "", labelErr("invalid_response")
	}
	return *resp.Data.ChatId, nil
}
