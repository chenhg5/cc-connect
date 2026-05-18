package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type codexRouterClient struct {
	URL        string
	Token      string
	Purpose    string
	TTLSeconds int
}

type codexRouterLease struct {
	LeaseID        string `json:"lease_id"`
	CodexHome      string `json:"codex_home"`
	AccountAlias   string `json:"account_alias"`
	AccountKeyHash string `json:"account_key_hash"`
}

type codexRouterLeaseResponse struct {
	OK             bool   `json:"ok"`
	Error          string `json:"error"`
	LeaseID        string `json:"lease_id"`
	CodexHome      string `json:"codex_home"`
	AccountAlias   string `json:"account_alias"`
	AccountKeyHash string `json:"account_key_hash"`
}

func (c codexRouterClient) enabled() bool {
	return strings.TrimSpace(c.URL) != ""
}

func (c codexRouterClient) lease(ctx context.Context, sessionID string) (*codexRouterLease, error) {
	url := strings.TrimRight(strings.TrimSpace(c.URL), "/") + "/v1/leases"
	purpose := strings.TrimSpace(c.Purpose)
	if purpose == "" {
		purpose = "chat"
	}
	ttl := c.TTLSeconds
	if ttl <= 0 {
		ttl = 2 * 60 * 60
	}
	payload := map[string]any{
		"client":      "cc-connect",
		"purpose":     purpose,
		"ttl_seconds": ttl,
	}
	if strings.TrimSpace(sessionID) != "" {
		payload["session_id_present"] = true
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex router lease request: %w", err)
	}
	defer resp.Body.Close()
	var decoded codexRouterLeaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("codex router lease decode: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !decoded.OK {
		if decoded.Error == "" {
			decoded.Error = resp.Status
		}
		return nil, fmt.Errorf("codex router lease failed: %s", decoded.Error)
	}
	if decoded.LeaseID == "" || decoded.CodexHome == "" {
		return nil, fmt.Errorf("codex router lease response missing lease_id or codex_home")
	}
	return &codexRouterLease{
		LeaseID:        decoded.LeaseID,
		CodexHome:      decoded.CodexHome,
		AccountAlias:   decoded.AccountAlias,
		AccountKeyHash: decoded.AccountKeyHash,
	}, nil
}

func (c codexRouterClient) release(ctx context.Context, leaseID, status string) error {
	if strings.TrimSpace(leaseID) == "" {
		return nil
	}
	url := strings.TrimRight(strings.TrimSpace(c.URL), "/") + "/v1/leases/" + leaseID + "/release"
	payload := map[string]any{"status": status}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("codex router release failed: %s", resp.Status)
	}
	return nil
}

type codexRouterSession struct {
	core.AgentSession
	router  codexRouterClient
	leaseID string
}

func (s *codexRouterSession) Close() error {
	err := s.AgentSession.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := "closed"
	if err != nil {
		status = "close_error"
	}
	if releaseErr := s.router.release(ctx, s.leaseID, status); releaseErr != nil {
		slog.Warn("codex router: release failed", "lease_id", s.leaseID, "error", releaseErr)
	}
	return err
}

func (s *codexRouterSession) SetMessageContext(messageID string) {
	if setter, ok := s.AgentSession.(core.MessageContextSetter); ok {
		setter.SetMessageContext(messageID)
	}
}

func (s *codexRouterSession) SetSessionKeyContext(sessionKey string) {
	if setter, ok := s.AgentSession.(core.SessionKeyContextSetter); ok {
		setter.SetSessionKeyContext(sessionKey)
	}
}

func (s *codexRouterSession) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	if reporter, ok := s.AgentSession.(core.UsageReporter); ok {
		return reporter.GetUsage(ctx)
	}
	return nil, fmt.Errorf("usage unavailable")
}

func (s *codexRouterSession) GetContextUsage() *core.ContextUsage {
	if reporter, ok := s.AgentSession.(core.ContextUsageReporter); ok {
		return reporter.GetContextUsage()
	}
	return nil
}
