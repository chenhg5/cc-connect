package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type DoctorStatus int

const (
	DoctorPass DoctorStatus = iota
	DoctorWarn
	DoctorFail
)

func (s DoctorStatus) Icon() string {
	switch s {
	case DoctorPass:
		return "✅"
	case DoctorWarn:
		return "⚠️"
	default:
		return "❌"
	}
}

type DoctorCheckResult struct {
	Name    string
	Status  DoctorStatus
	Detail  string
	Latency time.Duration
}

// DoctorChecker is an optional interface for agents to provide specific health checks.
type DoctorChecker interface {
	DoctorChecks(ctx context.Context) []DoctorCheckResult
}

// AgentDoctorInfo is an optional interface agents can implement to provide
// CLI binary name and display label for doctor checks, avoiding hardcoded
// agent-specific knowledge in core.
type AgentDoctorInfo interface {
	CLIBinaryName() string  // e.g. "claude", "codex"
	CLIDisplayName() string // e.g. "Claude", "Codex" (for display in doctor output)
}

// RunDoctorChecks performs all diagnostic checks.
func RunDoctorChecks(ctx context.Context, agent Agent, platforms []Platform) []DoctorCheckResult {
	var results []DoctorCheckResult

	results = append(results, checkAgentBinary(ctx, agent)...)
	results = append(results, checkAgentAuth(ctx, agent)...)
	results = append(results, checkPlatforms(platforms)...)
	results = append(results, checkSystem(ctx)...)
	results = append(results, checkDependencies()...)
	results = append(results, checkNetwork(ctx)...)

	if dc, ok := agent.(DoctorChecker); ok {
		results = append(results, dc.DoctorChecks(ctx)...)
	}

	return results
}

func agentCLIInfo(agent Agent) (bin, label string) {
	if info, ok := agent.(AgentDoctorInfo); ok {
		bin = info.CLIBinaryName()
		label = info.CLIDisplayName()
	}
	if bin == "" {
		bin = agent.Name()
	}
	if label == "" {
		label = bin
	}
	return bin, label
}

func checkAgentBinary(ctx context.Context, agent Agent) []DoctorCheckResult {
	bin, _ := agentCLIInfo(agent)

	path, err := exec.LookPath(bin)
	if err != nil {
		return []DoctorCheckResult{{
			Name:   fmt.Sprintf("Agent CLI (%s)", bin),
			Status: DoctorFail,
			Detail: "not found in PATH",
		}}
	}

	detail := path
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(tctx, bin, "--version").Output(); err == nil {
		ver := strings.TrimSpace(string(out))
		if len(ver) > 80 {
			ver = ver[:80]
		}
		detail = ver
	}
	return []DoctorCheckResult{{
		Name:   fmt.Sprintf("Agent CLI (%s)", bin),
		Status: DoctorPass,
		Detail: detail,
	}}
}

func checkAgentAuth(ctx context.Context, agent Agent) []DoctorCheckResult {
	bin, label := agentCLIInfo(agent)
	return checkCLIAuth(ctx, bin, []string{"--version"}, label)
}

func checkCLIAuth(ctx context.Context, bin string, args []string, label string) []DoctorCheckResult {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(tctx, bin, args...)
	out, err := cmd.CombinedOutput()
	latency := time.Since(start)

	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		if len(detail) > 120 {
			detail = detail[:120] + "..."
		}
		return []DoctorCheckResult{{
			Name:    fmt.Sprintf("%s Auth", label),
			Status:  DoctorWarn,
			Detail:  detail,
			Latency: latency,
		}}
	}
	return []DoctorCheckResult{{
		Name:    fmt.Sprintf("%s Auth", label),
		Status:  DoctorPass,
		Detail:  "OK",
		Latency: latency,
	}}
}

func checkPlatforms(platforms []Platform) []DoctorCheckResult {
	var results []DoctorCheckResult
	for _, p := range platforms {
		results = append(results, DoctorCheckResult{
			Name:   fmt.Sprintf("Platform (%s)", p.Name()),
			Status: DoctorPass,
			Detail: "connected",
		})
	}
	if len(results) == 0 {
		results = append(results, DoctorCheckResult{
			Name:   "Platforms",
			Status: DoctorWarn,
			Detail: "no platforms configured",
		})
	}
	return results
}

func checkSystem(ctx context.Context) []DoctorCheckResult {
	var results []DoctorCheckResult

	// Memory
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	allocMB := memStats.Alloc / 1024 / 1024
	sysMB := memStats.Sys / 1024 / 1024
	results = append(results, DoctorCheckResult{
		Name:   "Memory (Go runtime)",
		Status: DoctorPass,
		Detail: fmt.Sprintf("alloc %d MB / sys %d MB", allocMB, sysMB),
	})

	// System memory (Linux)
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/meminfo"); err == nil {
			var totalKB, availKB uint64
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					_, _ = fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
				} else if strings.HasPrefix(line, "MemAvailable:") {
					_, _ = fmt.Sscanf(line, "MemAvailable: %d kB", &availKB)
				}
			}
			if totalKB > 0 {
				totalMB := totalKB / 1024
				availMB := availKB / 1024
				usedPct := 100 - (availKB*100)/totalKB
				status := DoctorPass
				if usedPct > 90 {
					status = DoctorFail
				} else if usedPct > 75 {
					status = DoctorWarn
				}
				results = append(results, DoctorCheckResult{
					Name:   "System Memory",
					Status: status,
					Detail: fmt.Sprintf("%d MB available / %d MB total (%d%% used)", availMB, totalMB, usedPct),
				})
			}
		}
	}

	// CPU
	results = append(results, DoctorCheckResult{
		Name:   "CPU",
		Status: DoctorPass,
		Detail: fmt.Sprintf("%d cores, %s/%s", runtime.NumCPU(), runtime.GOOS, runtime.GOARCH),
	})

	// Load average (Linux/macOS)
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/loadavg"); err == nil {
			parts := strings.Fields(string(data))
			if len(parts) >= 3 {
				status := DoctorPass
				detail := fmt.Sprintf("load avg: %s %s %s", parts[0], parts[1], parts[2])
				// Rough check: if 1-min load > 2x CPU count, warn
				var load1 float64
				_, _ = fmt.Sscanf(parts[0], "%f", &load1)
				if load1 > float64(runtime.NumCPU()*2) {
					status = DoctorWarn
				}
				results = append(results, DoctorCheckResult{
					Name:   "CPU Load",
					Status: status,
					Detail: detail,
				})
			}
		}
	}

	// Disk space
	if wd, err := os.Getwd(); err == nil {
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(tctx, "df", "-h", wd).Output(); err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) >= 2 {
				fields := strings.Fields(lines[len(lines)-1])
				if len(fields) >= 5 {
					status := DoctorPass
					usePct := strings.TrimSuffix(fields[4], "%")
					var pct int
					_, _ = fmt.Sscanf(usePct, "%d", &pct)
					if pct > 95 {
						status = DoctorFail
					} else if pct > 85 {
						status = DoctorWarn
					}
					results = append(results, DoctorCheckResult{
						Name:   "Disk Space",
						Status: status,
						Detail: fmt.Sprintf("%s available / %s total (%s used)", fields[3], fields[1], fields[4]),
					})
				}
			}
		}
	}

	return results
}

func checkDependencies() []DoctorCheckResult {
	deps := []struct {
		bin      string
		label    string
		required bool
	}{
		{"git", "Git", true},
		{"sqlite3", "SQLite3", false},
		{"ffmpeg", "FFmpeg (voice)", false},
	}

	var results []DoctorCheckResult
	for _, d := range deps {
		path, err := exec.LookPath(d.bin)
		if err != nil {
			status := DoctorWarn
			if d.required {
				status = DoctorFail
			}
			results = append(results, DoctorCheckResult{
				Name:   d.label,
				Status: status,
				Detail: "not found",
			})
		} else {
			results = append(results, DoctorCheckResult{
				Name:   d.label,
				Status: DoctorPass,
				Detail: path,
			})
		}
	}
	return results
}

func checkNetwork(ctx context.Context) []DoctorCheckResult {
	endpoints := []struct {
		label string
		host  string
		url   string
	}{
		{"API (Anthropic)", "api.anthropic.com:443", "https://api.anthropic.com"},
		{"API (OpenAI)", "api.openai.com:443", "https://api.openai.com"},
	}

	var results []DoctorCheckResult
	for _, ep := range endpoints {
		tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		conn, err := (&net.Dialer{}).DialContext(tctx, "tcp", ep.host)
		latency := time.Since(start)
		cancel()

		if err != nil {
			results = append(results, DoctorCheckResult{
				Name:    ep.label,
				Status:  DoctorWarn,
				Detail:  fmt.Sprintf("connect failed: %v", err),
				Latency: latency,
			})
			continue
		}
		conn.Close()

		status := DoctorPass
		if latency > 3*time.Second {
			status = DoctorWarn
		}
		results = append(results, DoctorCheckResult{
			Name:    ep.label,
			Status:  status,
			Detail:  "TCP connect OK",
			Latency: latency,
		})
	}

	// HTTP check to verify proxy/firewall
	tctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	start := time.Now()
	client := &http.Client{Timeout: 8 * time.Second}
	req, _ := http.NewRequestWithContext(tctx, "HEAD", "https://api.anthropic.com", nil)
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		results = append(results, DoctorCheckResult{
			Name:    "HTTPS (Anthropic)",
			Status:  DoctorWarn,
			Detail:  "HTTPS request failed: " + err.Error(),
			Latency: latency,
		})
	} else {
		resp.Body.Close()
		status := DoctorPass
		if latency > 5*time.Second {
			status = DoctorWarn
		}
		results = append(results, DoctorCheckResult{
			Name:    "HTTPS (Anthropic)",
			Status:  status,
			Detail:  fmt.Sprintf("HTTP %d", resp.StatusCode),
			Latency: latency,
		})
	}

	// Check config file
	if cfgPath := os.Getenv("CC_CONFIG_PATH"); cfgPath != "" {
		if _, err := os.Stat(cfgPath); err != nil {
			results = append(results, DoctorCheckResult{
				Name:   "Config File",
				Status: DoctorFail,
				Detail: cfgPath + ": " + err.Error(),
			})
		}
	}

	// Check data directory
	if home, err := os.UserHomeDir(); err == nil {
		dataDir := filepath.Join(home, ".cc-connect")
		if info, err := os.Stat(dataDir); err != nil {
			results = append(results, DoctorCheckResult{
				Name:   "Data Directory",
				Status: DoctorWarn,
				Detail: dataDir + " does not exist",
			})
		} else if !info.IsDir() {
			results = append(results, DoctorCheckResult{
				Name:   "Data Directory",
				Status: DoctorFail,
				Detail: dataDir + " is not a directory",
			})
		} else {
			results = append(results, DoctorCheckResult{
				Name:   "Data Directory",
				Status: DoctorPass,
				Detail: dataDir,
			})
		}
	}

	return results
}

// checkNameZh provides Chinese translations for common check names.
var checkNameZh = map[string]string{
	"Memory (Go runtime)": "内存 (Go runtime)",
	"System Memory":       "系统内存",
	"CPU":                 "CPU",
	"CPU Load":            "CPU 负载",
	"Disk Space":          "磁盘空间",
	"Git":                 "Git",
	"SQLite3":             "SQLite3",
	"FFmpeg (voice)":      "FFmpeg (语音)",
	"HTTPS (Anthropic)":   "HTTPS (Anthropic)",
	"Data Directory":      "数据目录",
	"Config File":         "配置文件",
	"Platforms":           "平台",
}

// checkNameJa provides Japanese translations for common check names.
var checkNameJa = map[string]string{
	"Memory (Go runtime)": "メモリ (Go runtime)",
	"System Memory":       "システムメモリ",
	"CPU Load":            "CPU 負荷",
	"Disk Space":          "ディスク容量",
	"FFmpeg (voice)":      "FFmpeg (音声)",
	"Data Directory":      "データディレクトリ",
	"Config File":         "設定ファイル",
	"Platforms":           "プラットフォーム",
}

func localizeCheckName(name string, lang Language) string {
	switch lang {
	case LangChinese, LangTraditionalChinese:
		// Translate known names; parametric names (e.g. "Agent CLI (claude)") need prefix matching
		if zh, ok := checkNameZh[name]; ok {
			return zh
		}
		if strings.HasPrefix(name, "Agent CLI") {
			return strings.Replace(name, "Agent CLI", "Agent 命令行", 1)
		}
		if strings.HasPrefix(name, "Platform (") {
			return strings.Replace(name, "Platform", "平台", 1)
		}
		if strings.Contains(name, "Auth") {
			return strings.Replace(name, "Auth", "认证", 1)
		}
	case LangJapanese:
		if ja, ok := checkNameJa[name]; ok {
			return ja
		}
		if strings.HasPrefix(name, "Agent CLI") {
			return strings.Replace(name, "Agent CLI", "Agent CLI", 1)
		}
		if strings.HasPrefix(name, "Platform (") {
			return strings.Replace(name, "Platform", "プラットフォーム", 1)
		}
		if strings.Contains(name, "Auth") {
			return strings.Replace(name, "Auth", "認証", 1)
		}
	}
	return name
}

// FormatDoctorResults formats check results using the i18n system.
func FormatDoctorResults(results []DoctorCheckResult, i18n *I18n) string {
	lang := i18n.currentLang()

	var sb strings.Builder
	sb.WriteString(i18n.T(MsgDoctorTitle))

	passCount, warnCount, failCount := 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case DoctorPass:
			passCount++
		case DoctorWarn:
			warnCount++
		case DoctorFail:
			failCount++
		}

		displayName := localizeCheckName(r.Name, lang)
		latStr := ""
		if r.Latency > 0 {
			latStr = fmt.Sprintf(" (%s)", r.Latency.Round(time.Millisecond))
		}
		sb.WriteString(fmt.Sprintf("%s %s%s\n   %s\n\n", r.Status.Icon(), displayName, latStr, r.Detail))
	}

	sb.WriteString(i18n.Tf(MsgDoctorSummary, passCount, warnCount, failCount))
	return sb.String()
}
