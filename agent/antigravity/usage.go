package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Quota for the official Antigravity account login.
//
// agy has no usage subcommand and its stream-json output carries no quota
// data. The CLI itself polls the Cloud Code backend for it (agy log lines
// "Cache(retrieveUserQuotaSummary)" against
// https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary),
// which returns, per model group, a 5h and a weekly bucket with a
// remainingFraction. We call the same endpoint.
//
// Credentials: agy keeps its live token in the macOS keychain, which we do not
// touch (reading another app's keychain item opens a prompt). The token file
// ~/.gemini/antigravity-cli/antigravity-oauth-token holds a refresh token for
// the same account; we exchange it for an access token in memory and never
// write anything back. The OAuth client id comes from the token file's
// id_token audience and the client secret is read out of the agy binary, so no
// credential is stored in this repository.

const (
	antigravityQuotaURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	googleTokenURL      = "https://oauth2.googleapis.com/token"
	// The backend answers 403 without this user agent (checked 2026-10-15:
	// same token and body, Python default UA -> 403, "antigravity" -> 200).
	antigravityUserAgent = "antigravity"
	// Upper bound for one background quota fetch. Source: measured 2026-10-15
	// on this machine, token refresh ~0.4s + quota call 0.8-1.2s; the bound
	// only keeps a hung request from blocking later refreshes.
	antigravityQuotaFetchTimeout = 10 * time.Second
)

// Google installed-app client secrets are "GOCSPX-" + 28 chars.
// 实测: both GOCSPX secrets in the agy 1.3.5 binary have that shape.
const googleClientSecretBody = 28

var googleClientSecretRe = regexp.MustCompile(fmt.Sprintf(`GOCSPX-[A-Za-z0-9_-]{%d}`, googleClientSecretBody))

// googleClientSecretLen is the full match length, used as chunk carry-over
// when scanning the binary.
var googleClientSecretLen = len("GOCSPX-") + googleClientSecretBody

// quotaState caches the last quota report and the in-memory access token.
// The zero value is ready to use.
type quotaState struct {
	mu        sync.Mutex
	report    *core.UsageReport
	inflight  chan struct{}
	lastErr   error
	token     string
	tokenExp  time.Time
	secret    string
	binSecret map[string][]string // binary path -> client secret candidates

	// test hooks
	httpClient *http.Client
	tokenURL   string
	quotaURL   string
	homeDir    string
	binaryPath string
	now        func() time.Time
}

// GetUsage implements core.UsageReporter. The fetch runs in the background
// with its own timeout; if ctx expires first we return the last good report
// (if any) and the fetch still fills the cache for the next call. Call rate
// is bounded by the engine's reply-footer usage cache.
func (a *Agent) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	a.mu.RLock()
	usingProvider := a.activeIdx >= 0 && a.activeIdx < len(a.providers)
	model := a.model
	cmd := a.cmd
	extra := append([]string(nil), a.cliExtraArgs...)
	a.mu.RUnlock()
	if usingProvider {
		// API-key / relay provider: the account quota does not apply.
		return nil, errors.New("antigravity: quota only available for account login")
	}
	if m := modelFromArgs(extra); m != "" {
		model = m
	}

	q := &a.quota
	q.mu.Lock()
	done := q.inflight
	if done == nil {
		done = make(chan struct{})
		q.inflight = done
		go q.refresh(cmd, model, done)
	}
	q.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.report != nil {
		return cloneReport(q.report), nil
	}
	if q.lastErr != nil {
		return nil, q.lastErr
	}
	return nil, ctx.Err()
}

func (q *quotaState) refresh(cmd, model string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), antigravityQuotaFetchTimeout)
	defer cancel()
	report, err := q.fetch(ctx, cmd, model)
	q.mu.Lock()
	if err == nil {
		q.report = report
		q.lastErr = nil
	} else {
		q.lastErr = err
	}
	q.inflight = nil
	q.mu.Unlock()
	close(done)
}

func (q *quotaState) fetch(ctx context.Context, cmd, model string) (*core.UsageReport, error) {
	home := q.homeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = h
	}
	cliDir := filepath.Join(home, ".gemini", "antigravity-cli")
	projectRaw, err := os.ReadFile(filepath.Join(cliDir, "cache", "default_project_id.txt"))
	if err != nil {
		return nil, fmt.Errorf("antigravity: read project id: %w", err)
	}
	project := strings.TrimSpace(string(projectRaw))
	if project == "" {
		return nil, errors.New("antigravity: empty project id")
	}

	token, err := q.accessToken(ctx, filepath.Join(cliDir, "antigravity-oauth-token"), cmd)
	if err != nil {
		return nil, err
	}

	body, _ := json.Marshal(map[string]string{"project": project})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.quotaEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityUserAgent)
	resp, err := q.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity: quota request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusUnauthorized {
			q.setToken("", time.Time{})
		}
		return nil, fmt.Errorf("antigravity: quota status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var payload quotaSummary
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("antigravity: decode quota: %w", err)
	}
	report := mapQuotaSummary(payload, model, q.clock())
	if len(report.Buckets) == 0 {
		return nil, errors.New("antigravity: quota response has no buckets")
	}
	return report, nil
}

type quotaSummary struct {
	Groups []struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Buckets     []struct {
			BucketID          string   `json:"bucketId"`
			Window            string   `json:"window"`
			ResetTime         string   `json:"resetTime"`
			RemainingFraction *float64 `json:"remainingFraction"`
		} `json:"buckets"`
	} `json:"groups"`
}

// mapQuotaSummary turns the per-group 5h/weekly buckets into a UsageReport.
// The group that serves the current model goes first, because the footer
// shows the first bucket that has windows.
func mapQuotaSummary(payload quotaSummary, model string, now time.Time) *core.UsageReport {
	report := &core.UsageReport{Provider: "antigravity"}
	preferred := -1
	wantThirdParty := isThirdPartyModel(model)
	for i, g := range payload.Groups {
		text := strings.ToLower(g.DisplayName + " " + g.Description)
		thirdParty := strings.Contains(text, "claude") || strings.Contains(text, "gpt")
		if preferred < 0 && thirdParty == wantThirdParty {
			preferred = i
		}
	}
	order := make([]int, 0, len(payload.Groups))
	if preferred >= 0 {
		order = append(order, preferred)
	}
	for i := range payload.Groups {
		if i != preferred {
			order = append(order, i)
		}
	}
	for _, i := range order {
		g := payload.Groups[i]
		var windows []core.UsageWindow
		limitReached := false
		for _, b := range g.Buckets {
			if b.RemainingFraction == nil {
				continue
			}
			remaining := clampPercent(int(math.Round(*b.RemainingFraction * 100)))
			w := core.UsageWindow{
				Name:          b.Window,
				UsedPercent:   100 - remaining, // percent scale
				WindowSeconds: quotaWindowSeconds(b.Window),
			}
			if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
				w.ResetAtUnix = t.Unix()
				if d := int(t.Sub(now).Seconds()); d > 0 {
					w.ResetAfterSeconds = d
				}
			}
			if remaining == 0 {
				limitReached = true
			}
			windows = append(windows, w)
		}
		if len(windows) == 0 {
			continue
		}
		report.Buckets = append(report.Buckets, core.UsageBucket{
			Name:         g.DisplayName,
			Allowed:      !limitReached,
			LimitReached: limitReached,
			Windows:      windows,
		})
	}
	return report
}

func clampPercent(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 { // percent scale
		return 100 // percent scale
	}
	return p
}

// quotaWindowSeconds maps the backend's window names (observed values: "5h",
// "weekly") to the durations core uses to recognise 5h / 7d windows.
func quotaWindowSeconds(window string) int {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case "5h":
		return 5 * 3600 // window name "5h"
	case "weekly":
		return 7 * 24 * 3600 // window name "weekly"
	}
	return 0
}

func isThirdPartyModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "claude") || strings.Contains(m, "gpt")
}

func modelFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if v, ok := strings.CutPrefix(arg, "--model="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// accessToken returns a usable access token: the one in the token file if it
// has not expired, otherwise a fresh one from the refresh token (memory only).
func (q *quotaState) accessToken(ctx context.Context, tokenPath, cmd string) (string, error) {
	now := q.clock()
	q.mu.Lock()
	if q.token != "" && now.Add(time.Minute).Before(q.tokenExp) {
		t := q.token
		q.mu.Unlock()
		return t, nil
	}
	known := q.secret
	q.mu.Unlock()

	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("antigravity: read token file: %w", err)
	}
	var file struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expiry       string `json:"expiry"`
		} `json:"token"`
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return "", fmt.Errorf("antigravity: parse token file: %w", err)
	}
	if exp, err := time.Parse(time.RFC3339Nano, file.Token.Expiry); err == nil && file.Token.AccessToken != "" && now.Add(time.Minute).Before(exp) {
		q.setToken(file.Token.AccessToken, exp)
		return file.Token.AccessToken, nil
	}
	if file.Token.RefreshToken == "" {
		return "", errors.New("antigravity: token file has no refresh_token")
	}
	clientID := idTokenAudience(file.IDToken)
	if clientID == "" {
		return "", errors.New("antigravity: cannot determine OAuth client id from id_token")
	}

	var candidates []string
	if known != "" {
		candidates = append(candidates, known)
	}
	more, scanErr := q.secretCandidates(cmd)
	for _, s := range more {
		if s != known {
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		if scanErr != nil {
			return "", scanErr
		}
		return "", errors.New("antigravity: no OAuth client secret found in agy binary")
	}

	var lastErr error
	for _, secret := range candidates {
		tok, exp, err := q.refreshToken(ctx, clientID, secret, file.Token.RefreshToken)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		q.mu.Lock()
		q.secret = secret
		q.mu.Unlock()
		q.setToken(tok, exp)
		return tok, nil
	}
	return "", lastErr
}

func (q *quotaState) setToken(tok string, exp time.Time) {
	q.mu.Lock()
	q.token = tok
	q.tokenExp = exp
	q.mu.Unlock()
}

func (q *quotaState) refreshToken(ctx context.Context, clientID, secret, refresh string) (string, time.Time, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {secret},
		"refresh_token": {refresh},
		"grant_type":    {"refresh_token"},
	}
	endpoint := q.tokenURL
	if endpoint == "" {
		endpoint = googleTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := q.client().Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("antigravity: token refresh: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return "", time.Time{}, fmt.Errorf("antigravity: token refresh status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("antigravity: decode token: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, errors.New("antigravity: token refresh returned no access_token")
	}
	return out.AccessToken, q.clock().Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// secretCandidates scans the agy binary once (per path) for Google OAuth
// client secrets.
func (q *quotaState) secretCandidates(cmd string) ([]string, error) {
	path := q.binaryPath
	if path == "" {
		p, err := exec.LookPath(cmd)
		if err != nil {
			return nil, fmt.Errorf("antigravity: locate %q: %w", cmd, err)
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		path = p
	}
	q.mu.Lock()
	if s, ok := q.binSecret[path]; ok {
		q.mu.Unlock()
		return s, nil
	}
	q.mu.Unlock()

	found, err := scanFileForPattern(path, googleClientSecretRe, googleClientSecretLen)
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	if q.binSecret == nil {
		q.binSecret = map[string][]string{}
	}
	q.binSecret[path] = found
	q.mu.Unlock()
	return found, nil
}

// scanFileForPattern streams a large file (agy is ~180MB) and returns unique
// matches in first-seen order. maxMatch bytes are carried over between reads
// so a match split across two reads is still found.
func scanFileForPattern(path string, re *regexp.Regexp, maxMatch int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const chunk = 4 << 20 // read size only, not a policy value (any size works; 实测 not needed)
	r := bufio.NewReaderSize(f, chunk)
	tmp := make([]byte, chunk)
	buf := make([]byte, 0, chunk+maxMatch)
	seen := map[string]bool{}
	var out []string
	for {
		n, err := io.ReadFull(r, tmp)
		buf = append(buf, tmp[:n]...)
		for _, m := range re.FindAll(buf, -1) {
			s := string(m)
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return out, nil
			}
			return out, err
		}
		if len(buf) > maxMatch {
			buf = append(buf[:0], buf[len(buf)-maxMatch:]...)
		}
	}
}

func idTokenAudience(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Aud any `json:"aud"`
	}
	if json.Unmarshal(data, &claims) != nil {
		return ""
	}
	switch v := claims.Aud.(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func (q *quotaState) client() *http.Client {
	if q.httpClient != nil {
		return q.httpClient
	}
	return http.DefaultClient
}

func (q *quotaState) quotaEndpoint() string {
	if q.quotaURL != "" {
		return q.quotaURL
	}
	return antigravityQuotaURL
}

func (q *quotaState) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

func cloneReport(r *core.UsageReport) *core.UsageReport {
	if r == nil {
		return nil
	}
	out := *r
	out.Buckets = make([]core.UsageBucket, len(r.Buckets))
	for i, b := range r.Buckets {
		b.Windows = append([]core.UsageWindow(nil), b.Windows...)
		out.Buckets[i] = b
	}
	if r.Credits != nil {
		c := *r.Credits
		out.Credits = &c
	}
	return &out
}
