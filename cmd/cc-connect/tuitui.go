//go:build !no_tuitui

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/platform/tuitui"
)

func runTuiTui(args []string) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printTuiTuiUsage()
		return
	}
	switch args[0] {
	case "messages", "recent", "search":
		runTuiTuiMessages(args[0], args[1:])
	case "download":
		runTuiTuiDownload(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown tuitui subcommand: %s\n", args[0])
		printTuiTuiUsage()
		os.Exit(1)
	}
}

type tuituiCLIOptions struct {
	configPath string
	configSet  bool
	project    string
	appID      string
	appSecret  string
	apiBase    string
	chatID     string
	chatType   string
	relative   string
	startTime  string
	endTime    string
	cursor     string
	limit      int
	orderAsc   *bool
	query      string
	url        string
	outDir     string
	maxBytes   int64
}

func runTuiTuiMessages(command string, args []string) {
	opts, err := parseTuiTuiArgs(args)
	if err != nil {
		fatalTuiTui(err)
	}
	if opts.chatID == "" {
		fatalTuiTui(errors.New("missing --chat"))
	}
	if command == "search" && strings.TrimSpace(opts.query) == "" {
		fatalTuiTui(errors.New("missing --q"))
	}
	p, err := loadTuiTuiPlatform(opts)
	if err != nil {
		fatalTuiTui(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := p.FetchHistory(ctx, opts.chatID, opts.chatType, tuitui.HistoryOptions{
		RelativeTime: opts.relative,
		StartTime:    opts.startTime,
		EndTime:      opts.endTime,
		Cursor:       opts.cursor,
		Limit:        opts.limit,
		OrderAsc:     opts.orderAsc,
	})
	if err != nil {
		fatalTuiTui(err)
	}
	if command == "search" {
		result = filterTuiTuiHistory(result, opts.query)
	}
	printJSON(result)
}

func runTuiTuiDownload(args []string) {
	opts, err := parseTuiTuiArgs(args)
	if err != nil {
		fatalTuiTui(err)
	}
	if opts.url == "" {
		fatalTuiTui(errors.New("missing --url"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, mimeType, name, err := downloadTuiTuiURL(ctx, opts.url, opts.maxBytes)
	if err != nil {
		fatalTuiTui(err)
	}
	outDir := opts.outDir
	if outDir == "" {
		outDir = "./tmp/tuitui"
	}
	if name == "" || name == "." || name == "/" {
		name = fmt.Sprintf("tuitui_%d", time.Now().Unix())
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalTuiTui(err)
	}
	outPath := filepath.Join(outDir, filepath.Base(name))
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		fatalTuiTui(err)
	}
	printJSON(map[string]any{
		"path":      outPath,
		"mime_type": mimeType,
		"filename":  name,
		"size":      len(data),
	})
}

func downloadTuiTuiURL(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limit := maxBytes
	if limit <= 0 {
		limit = 25 << 20
	}
	if resp.ContentLength > limit {
		return nil, "", "", fmt.Errorf("download exceeds --max-bytes: %d > %d", resp.ContentLength, limit)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", "", err
	}
	if int64(len(data)) > limit {
		return nil, "", "", fmt.Errorf("download exceeds --max-bytes: %d > %d", len(data), limit)
	}
	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	return data, mimeType, filenameFromTuiTuiResponse(rawURL, resp.Header.Get("Content-Disposition")), nil
}

func filenameFromTuiTuiResponse(rawURL, contentDisposition string) string {
	if contentDisposition != "" {
		_, params, err := mime.ParseMediaType(contentDisposition)
		if err == nil && params["filename"] != "" {
			return params["filename"]
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return filepath.Base(u.Path)
}

func parseTuiTuiArgs(args []string) (tuituiCLIOptions, error) {
	var opts tuituiCLIOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--config":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.configPath = v
			opts.configSet = true
		case "--project", "-p":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.project = v
		case "--app-id":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.appID = v
		case "--app-secret":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.appSecret = v
		case "--api-base":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.apiBase = v
		case "--chat", "--chat-id":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.chatID = v
		case "--chat-type":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.chatType = v
		case "--relative-time":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.relative = v
		case "--start-time":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.startTime = v
		case "--end-time":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.endTime = v
		case "--cursor":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.cursor = v
		case "--limit":
			v, err := value()
			if err != nil {
				return opts, err
			}
			if _, err := fmt.Sscanf(v, "%d", &opts.limit); err != nil {
				return opts, fmt.Errorf("invalid --limit %q", v)
			}
		case "--order-asc":
			v, err := value()
			if err != nil {
				return opts, err
			}
			b := strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
			opts.orderAsc = &b
		case "--q", "--query":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.query = v
		case "--url":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.url = v
		case "--out":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.outDir = v
		case "--max-bytes":
			v, err := value()
			if err != nil {
				return opts, err
			}
			if _, err := fmt.Sscanf(v, "%d", &opts.maxBytes); err != nil {
				return opts, fmt.Errorf("invalid --max-bytes %q", v)
			}
		case "--help", "-h":
			return opts, errTuiTuiUsage
		default:
			return opts, fmt.Errorf("unknown option: %s", arg)
		}
	}
	if opts.configPath == "" {
		opts.configPath = resolveConfigPath("")
	}
	return opts, nil
}

var errTuiTuiUsage = errors.New("show tuitui usage")

func loadTuiTuiPlatform(opts tuituiCLIOptions) (*tuitui.Platform, error) {
	platformOpts := map[string]any{}
	if opts.configPath != "" {
		cfg, err := config.Load(opts.configPath)
		if err != nil {
			if opts.configSet {
				return nil, err
			}
		} else {
			var found bool
			platformOpts, found = findTuiTuiOptions(cfg, opts)
			if opts.project != "" && !found {
				return nil, fmt.Errorf("tuitui platform not found in project %q", opts.project)
			}
		}
	}
	if opts.appID != "" {
		platformOpts["app_id"] = opts.appID
	}
	if opts.appSecret != "" {
		platformOpts["app_secret"] = opts.appSecret
	}
	if opts.apiBase != "" {
		platformOpts["api_base"] = opts.apiBase
	}
	if platformOpts["app_id"] == nil || platformOpts["app_id"] == "" {
		platformOpts["app_id"] = os.Getenv("TUITUI_APP_ID")
	}
	if platformOpts["app_secret"] == nil || platformOpts["app_secret"] == "" {
		platformOpts["app_secret"] = os.Getenv("TUITUI_APP_SECRET")
	}
	p, err := tuitui.New(platformOpts)
	if err != nil {
		return nil, err
	}
	return p.(*tuitui.Platform), nil
}

func findTuiTuiOptions(cfg *config.Config, opts tuituiCLIOptions) (map[string]any, bool) {
	for _, proj := range cfg.Projects {
		if opts.project != "" && proj.Name != opts.project {
			continue
		}
		for _, pc := range proj.Platforms {
			if pc.Type != "tuitui" {
				continue
			}
			out := make(map[string]any, len(pc.Options))
			for k, v := range pc.Options {
				out[k] = v
			}
			return out, true
		}
	}
	return map[string]any{}, false
}

func filterTuiTuiHistory(result *tuitui.HistoryResult, query string) *tuitui.HistoryResult {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return result
	}
	filtered := *result
	filtered.Messages = nil
	for _, msg := range result.Messages {
		data, _ := json.Marshal(msg)
		if strings.Contains(strings.ToLower(string(data)), query) {
			filtered.Messages = append(filtered.Messages, msg)
		}
	}
	filtered.Threads = nil
	for _, thread := range result.Threads {
		if strings.Contains(strings.ToLower(thread), query) {
			filtered.Threads = append(filtered.Threads, thread)
		}
	}
	return &filtered
}

func printJSON(value any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

func fatalTuiTui(err error) {
	if errors.Is(err, errTuiTuiUsage) {
		printTuiTuiUsage()
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

func printTuiTuiUsage() {
	fmt.Print(`Usage:
  cc-connect tuitui messages --chat <id> [--chat-type direct|group|channel] [--relative-time today] [--limit 100]
  cc-connect tuitui search --chat <id> --q <keyword> [--chat-type direct|group|channel] [--relative-time last_7_days]
  cc-connect tuitui download --url <url> [--out ./tmp/tuitui] [--max-bytes 26214400]

Credential options:
  --config <path>            Load credentials from cc-connect config (default config path)
  -p, --project <name>       Pick the TuiTui platform from a project
      --app-id <id>          Override TuiTui app id
      --app-secret <secret>  Override TuiTui app secret
      --api-base <url>       Override API base

History options:
  --chat <id>                User account, group ID, channel ID, or teams_<team>_<channel>_<thread>
  --chat-type <type>         direct, group, or channel (guessed when omitted)
  --relative-time <range>    today, yesterday, last_24_hours, last_7_days, etc.
  --start-time <time>        API start time for direct/group, or RFC3339/local time for channel
  --end-time <time>          API end time for direct/group, or RFC3339/local time for channel
  --cursor <cursor>          Pagination cursor
  --limit <n>                Max records
  --order-asc <true|false>   Sort order
`)
}
