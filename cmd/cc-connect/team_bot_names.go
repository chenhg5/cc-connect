package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/config"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// Each request uses the target bot's own application, never the manager's
// credentials. The official API returns the published Feishu display name:
// https://open.feishu.cn/document/client-docs/bot-v3/obtain-bot-info
func readFeishuBotName(ctx context.Context, identity registrationFlowResult) (string, error) {
	base := openFeishuBaseURL
	if identity.Platform == "lark" {
		base = openLarkBaseURL
	}
	return readFeishuBotNameWithClient(ctx, identity, base, &http.Client{Timeout: 8 * time.Second})
}

func readFeishuBotNameWithClient(ctx context.Context, identity registrationFlowResult, base string, httpClient *http.Client) (string, error) {
	if identity.AppID == "" || identity.AppSecret == "" {
		return "", fmt.Errorf("missing bot credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := lark.NewClient(identity.AppID, identity.AppSecret, lark.WithOpenBaseUrl(base), lark.WithHttpClient(httpClient))
	response, err := client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", fmt.Errorf("read bot name failed")
	}
	var result struct {
		Code int `json:"code"`
		Bot  struct {
			Name string `json:"app_name"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(response.RawBody, &result); err != nil || response.StatusCode != http.StatusOK || result.Code != 0 || strings.TrimSpace(result.Bot.Name) == "" {
		return "", fmt.Errorf("read bot name failed")
	}
	return result.Bot.Name, nil
}

// Refresh only in-memory display metadata. Identity keys and user configuration
// are unaffected by opening a card; a saved profile retains the verified name.
func (s *teamProjectService) refreshTeamNames(ctx context.Context, cfg *config.Config, team *config.TeamConfig, only string) bool {
	if s.readBotName == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var failed atomic.Bool
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i := range team.Members {
		member := &team.Members[i]
		if only != "" && member.Bot != only {
			continue
		}
		var identity registrationFlowResult
		for _, project := range cfg.Projects {
			if project.Name != member.Bot {
				continue
			}
			for _, p := range project.Platforms {
				if p.Type != "feishu" && p.Type != "lark" {
					continue
				}
				identity.Platform = p.Type
				identity.AppID, _ = p.Options["app_id"].(string)
				identity.AppSecret, _ = p.Options["app_secret"].(string)
				break
			}
		}
		if identity.AppID == "" || identity.AppSecret == "" {
			failed.Store(true)
			continue
		}
		wg.Add(1)
		go func(member *config.TeamMember, identity registrationFlowResult) {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				failed.Store(true)
				return
			}
			defer func() { <-limit }()
			if name, err := s.readBotName(ctx, identity); err == nil {
				member.DisplayName = name
			} else {
				failed.Store(true)
				slog.Warn("bot display name refresh failed", "bot", member.Bot)
			}
		}(member, identity)
	}
	wg.Wait()
	return !failed.Load()
}
