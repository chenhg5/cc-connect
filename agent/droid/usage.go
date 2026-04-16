package droid

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
)

var (
	reUsageEmail  = regexp.MustCompile(`(?i)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}`)
	reUsage5h     = regexp.MustCompile(`(?i)(?:\b5\s*h\b|5\s*hour)[^\n%]{0,80}?(\d{1,3})\s*%`)
	reUsage7d     = regexp.MustCompile(`(?i)(?:\b7\s*d\b|7\s*day|week)[^\n%]{0,80}?(\d{1,3})\s*%`)
	reUsagePlan   = regexp.MustCompile(`(?im)^\s*(?:plan|tier|subscription)\s*[:：]\s*(.+?)\s*$`)
	reUsageTokens = regexp.MustCompile(`(?im)\b(input|output|total)\b[^\n0-9]{0,20}([0-9][0-9,._]*\s*[kKmMbB]?)`)
)

func (a *Agent) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	if report, err := a.getUsageFromSettings(); err == nil {
		return report, nil
	}

	var wg sync.WaitGroup
	var statusText, costText string
	var statusErr, costErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		statusText, statusErr = a.runUsageCommand(ctx, "/status")
	}()
	go func() {
		defer wg.Done()
		costText, costErr = a.runUsageCommand(ctx, "/cost")
	}()
	wg.Wait()

	if strings.TrimSpace(statusText) == "" && strings.TrimSpace(costText) == "" {
		if statusErr != nil && costErr != nil {
			return nil, fmt.Errorf("droid usage unavailable (/status: %v; /cost: %v)", statusErr, costErr)
		}
		if statusErr != nil {
			return nil, fmt.Errorf("droid usage /status: %w", statusErr)
		}
		if costErr != nil {
			return nil, fmt.Errorf("droid usage /cost: %w", costErr)
		}
		return nil, fmt.Errorf("droid usage unavailable: empty response")
	}

	return parseDroidUsage(statusText, costText), nil
}

func (a *Agent) getUsageFromSettings() (*core.UsageReport, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

	a.mu.RLock()
	workDir := a.workDir
	a.mu.RUnlock()

	baseDir := filepath.Join(homeDir, ".factory", "sessions")
	files, err := collectDroidSessionFiles(baseDir, workDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no droid session files")
	}

	sort.Slice(files, func(i, j int) bool {
		ii, ei := os.Stat(files[i])
		jj, ej := os.Stat(files[j])
		if ei != nil || ej != nil {
			return files[i] > files[j]
		}
		return ii.ModTime().After(jj.ModTime())
	})

	for _, sessionFile := range files {
		settingsPath := strings.TrimSuffix(sessionFile, ".jsonl") + ".settings.json"
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			continue
		}
		model, summary := parseTokenUsageSettings(data)
		if summary == "" {
			continue
		}
		report := &core.UsageReport{Provider: "droid", AccountID: "droid"}
		if model != "" {
			report.Plan = model + " | " + summary
		} else {
			report.Plan = summary
		}
		return report, nil
	}

	return nil, fmt.Errorf("no tokenUsage found in settings")
}

func (a *Agent) runUsageCommand(ctx context.Context, cmdPrompt string) (string, error) {
	a.mu.RLock()
	cmdName := a.cmd
	workDir := a.workDir
	model := core.GetProviderModel(a.providers, a.activeIdx, a.model)
	reasoningEffort := a.reasoningEffort
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	a.mu.RUnlock()

	args := []string{"exec", "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if reasoningEffort != "" {
		args = append(args, "--reasoning-effort", reasoningEffort)
	}
	if workDir != "" {
		args = append(args, "--cwd", workDir)
	}
	args = append(args, cmdPrompt)

	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = workDir
	if len(extraEnv) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), extraEnv)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := extractDroidResultText(stdout.Bytes())
	if strings.TrimSpace(out) == "" {
		out = strings.TrimSpace(stdout.String())
	}
	if err != nil {
		if strings.TrimSpace(out) != "" {
			return out, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("run droid exec: %s", msg)
	}

	if out == "" {
		return "", fmt.Errorf("empty response")
	}
	return out, nil
}

func extractDroidResultText(raw []byte) string {
	var parts []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		typ, _ := evt["type"].(string)
		switch typ {
		case "message":
			if role, _ := evt["role"].(string); role != "assistant" {
				continue
			}
			if txt, _ := evt["text"].(string); strings.TrimSpace(txt) != "" {
				parts = append(parts, txt)
			}
		case "result":
			if txt, _ := evt["content"].(string); strings.TrimSpace(txt) != "" {
				parts = append(parts, txt)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func parseDroidUsage(statusText, costText string) *core.UsageReport {
	combined := strings.TrimSpace(strings.Join([]string{statusText, costText}, "\n"))
	report := &core.UsageReport{Provider: "droid", AccountID: "droid"}

	if m := reUsageEmail.FindString(combined); m != "" {
		report.Email = strings.TrimSpace(m)
	}

	if m := reUsagePlan.FindStringSubmatch(combined); len(m) > 1 {
		report.Plan = strings.TrimSpace(m[1])
	}

	tokenSummary := parseTokenSummary(combined)
	if tokenSummary != "" {
		if report.Plan == "" {
			report.Plan = tokenSummary
		} else {
			report.Plan = report.Plan + " | " + tokenSummary
		}
	}

	if used := parsePercent(reUsage5h, combined); used >= 0 {
		report.Buckets = append(report.Buckets, core.UsageBucket{
			Name: "Rate limit",
			Windows: []core.UsageWindow{{
				Name:          "5h",
				UsedPercent:   used,
				WindowSeconds: 18000,
			}},
		})
	}
	if used := parsePercent(reUsage7d, combined); used >= 0 {
		if len(report.Buckets) == 0 {
			report.Buckets = append(report.Buckets, core.UsageBucket{Name: "Rate limit"})
		}
		report.Buckets[0].Windows = append(report.Buckets[0].Windows, core.UsageWindow{
			Name:          "7d",
			UsedPercent:   used,
			WindowSeconds: 604800,
		})
	}

	if report.Plan == "" && len(report.Buckets) == 0 {
		report.Plan = summarizeUsageText(costText, combined)
	}

	return report
}

func parsePercent(re *regexp.Regexp, text string) int {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return -1
	}
	v, err := strconv.Atoi(strings.TrimSpace(m[1]))
	if err != nil || v < 0 || v > 100 {
		return -1
	}
	return v
}

func parseTokenSummary(text string) string {
	matches := reUsageTokens.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(m[1]))
		v := strings.TrimSpace(m[2])
		if k == "" || v == "" {
			continue
		}
		item := k + " " + v
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		parts = append(parts, item)
	}
	return strings.Join(parts, ", ")
}

func parseTokenUsageSettings(data []byte) (model, summary string) {
	var cfg struct {
		Model      string `json:"model"`
		TokenUsage struct {
			InputTokens         int `json:"inputTokens"`
			OutputTokens        int `json:"outputTokens"`
			CacheCreationTokens int `json:"cacheCreationTokens"`
			CacheReadTokens     int `json:"cacheReadTokens"`
			ThinkingTokens      int `json:"thinkingTokens"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", ""
	}

	parts := make([]string, 0, 5)
	if cfg.TokenUsage.InputTokens > 0 {
		parts = append(parts, fmt.Sprintf("input %d", cfg.TokenUsage.InputTokens))
	}
	if cfg.TokenUsage.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("output %d", cfg.TokenUsage.OutputTokens))
	}
	if cfg.TokenUsage.ThinkingTokens > 0 {
		parts = append(parts, fmt.Sprintf("thinking %d", cfg.TokenUsage.ThinkingTokens))
	}
	if cfg.TokenUsage.CacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_read %d", cfg.TokenUsage.CacheReadTokens))
	}
	if cfg.TokenUsage.CacheCreationTokens > 0 {
		parts = append(parts, fmt.Sprintf("cache_create %d", cfg.TokenUsage.CacheCreationTokens))
	}

	return strings.TrimSpace(cfg.Model), strings.Join(parts, ", ")
}

func summarizeUsageText(preferred, fallback string) string {
	src := strings.TrimSpace(preferred)
	if src == "" {
		src = strings.TrimSpace(fallback)
	}
	if src == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(strings.Join(strings.Fields(ln), " "))
		if ln == "" {
			continue
		}
		out = append(out, ln)
		if len(out) >= 3 {
			break
		}
	}
	text := strings.Join(out, " | ")
	if len([]rune(text)) > 180 {
		text = string([]rune(text)[:180]) + "..."
	}
	return text
}
