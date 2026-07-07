package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NotifyConfig wires the INDEX watcher: any RESULT row appended to the
// archive INDEX by any seat (dispatched or not) is pushed to the Secretary
// session and/or the local desktop. The INDEX row itself is the push event —
// no seat has to learn to send notifications (L-0311).
type NotifyConfig struct {
	Enabled         bool
	IndexPath       string
	PollInterval    time.Duration
	Platform        string // platform name for session injection; default "telegram"
	SessionKey      string // Secretary session receiving [LETTER_ARRIVED]
	TelegramEnabled bool
	ToastEnabled    bool
}

type indexResultRow struct {
	Letter  string
	Thread  string
	Summary string
}

type notifyLedger struct {
	Seeded   bool              `json:"seeded"`
	Notified map[string]string `json:"notified"`
}

type notifyStore struct {
	mu   sync.Mutex
	path string
}

func newNotifyStore(dataDir string) *notifyStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &notifyStore{path: filepath.Join(dataDir, "notify_ledger.json")}
}

func (s *notifyStore) load() (notifyLedger, error) {
	ledger := notifyLedger{Notified: map[string]string{}}
	if s == nil {
		return ledger, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ledger, nil
		}
		return ledger, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return ledger, nil
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, err
	}
	if ledger.Notified == nil {
		ledger.Notified = map[string]string{}
	}
	return ledger, nil
}

func (s *notifyStore) save(ledger notifyLedger) error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}

// parseIndexResultRows extracts RESULT rows from INDEX.md content.
// Row shape: | L-XXXX | RESULT | thread | parent | summary | date |
func parseIndexResultRows(data string) []indexResultRow {
	var out []indexResultRow
	for _, raw := range strings.Split(data, "\n") {
		fields := strings.Split(raw, "|")
		if len(fields) < 7 {
			continue
		}
		if strings.TrimSpace(fields[2]) != "RESULT" {
			continue
		}
		letter := strings.TrimSpace(fields[1])
		if !strings.HasPrefix(letter, "L-") {
			continue
		}
		out = append(out, indexResultRow{
			Letter:  letter,
			Thread:  strings.TrimSpace(fields[3]),
			Summary: strings.TrimSpace(fields[5]),
		})
	}
	return out
}

// scanNewResults returns rows not yet notified, skipping letters covered by
// the dispatch ledger (those get [RESULT_READY] from the dispatch watcher).
// Skipped-but-covered rows are still recorded so the ledger stays tidy.
func scanNewResults(rows []indexResultRow, ledger *notifyLedger, dispatchCovered map[string]bool) []indexResultRow {
	var fresh []indexResultRow
	now := time.Now().Format(time.RFC3339)
	for _, row := range rows {
		if _, seen := ledger.Notified[row.Letter]; seen {
			continue
		}
		ledger.Notified[row.Letter] = now
		if dispatchCovered[row.Letter] {
			continue
		}
		fresh = append(fresh, row)
	}
	return fresh
}

// letters returns the set of letter IDs present in the dispatch ledger,
// regardless of state.
func (s *dispatchStore) letters() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.loadLocked()
	if err != nil {
		return out
	}
	for _, exp := range ledger.Expectations {
		out[exp.Letter] = true
	}
	return out
}

func (e *Engine) SetNotifyConfig(cfg NotifyConfig) {
	e.configureNotify(cfg)
}

func (e *Engine) configureNotify(cfg NotifyConfig) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if strings.TrimSpace(cfg.Platform) == "" {
		cfg.Platform = "telegram"
	}
	e.notifyConfig = cfg
	if cfg.Enabled && strings.TrimSpace(cfg.IndexPath) != "" {
		if e.notifyStore == nil {
			e.notifyStore = newNotifyStore(e.dataDir)
		}
		e.startNotifyWatcher()
	}
}

func (e *Engine) startNotifyWatcher() {
	if e.notifyWatcherStarted {
		return
	}
	e.notifyWatcherStarted = true
	go e.runNotifyWatcher()
}

func (e *Engine) runNotifyWatcher() {
	ticker := time.NewTicker(e.notifyConfig.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.checkNewResults()
		}
	}
}

func (e *Engine) checkNewResults() {
	data, err := os.ReadFile(e.notifyConfig.IndexPath)
	if err != nil {
		slog.Warn("notify: failed to read INDEX", "path", e.notifyConfig.IndexPath, "error", err)
		return
	}
	ledger, err := e.notifyStore.load()
	if err != nil {
		slog.Warn("notify: failed to load ledger", "error", err)
		return
	}
	rows := parseIndexResultRows(string(data))

	// First run: seed every existing row without notifying, or the whole
	// archive history would fire at once.
	if !ledger.Seeded {
		now := time.Now().Format(time.RFC3339)
		for _, row := range rows {
			ledger.Notified[row.Letter] = now
		}
		ledger.Seeded = true
		if err := e.notifyStore.save(ledger); err != nil {
			slog.Warn("notify: failed to seed ledger", "error", err)
		}
		slog.Info("notify: ledger seeded", "rows", len(rows))
		return
	}

	fresh := scanNewResults(rows, &ledger, e.dispatchStore.letters())
	if len(fresh) == 0 {
		return
	}
	if err := e.notifyStore.save(ledger); err != nil {
		slog.Warn("notify: failed to save ledger", "error", err)
		return
	}
	for _, row := range fresh {
		e.notifyLetterArrived(row)
	}
}

func (e *Engine) notifyLetterArrived(row indexResultRow) {
	slog.Info("notify: letter arrived", "letter", row.Letter, "thread", row.Thread)
	if e.notifyConfig.TelegramEnabled && strings.TrimSpace(e.notifyConfig.SessionKey) != "" {
		content := fmt.Sprintf("[LETTER_ARRIVED]\nLetter: %s\nThread: %s\nSummary: %s", row.Letter, row.Thread, row.Summary)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := e.InjectSyntheticMessage(ctx, e.notifyConfig.Platform, e.notifyConfig.SessionKey, "cc-connect-notify", "cc-connect notify", content); err != nil {
			slog.Warn("notify: failed to inject LETTER_ARRIVED", "letter", row.Letter, "error", err)
		}
		cancel()
	}
	if e.notifyConfig.ToastEnabled {
		title := fmt.Sprintf("📬 %s RESULT 到了", row.Letter)
		body := fmt.Sprintf("%s — %s", row.Thread, row.Summary)
		if err := notifyToastFunc(title, body); err != nil {
			slog.Warn("notify: toast failed", "letter", row.Letter, "error", err)
		}
	}
}

// notifyToastFunc is a seam for tests.
var notifyToastFunc = showWindowsToast

// psToastEscape doubles single quotes for embedding in a single-quoted
// PowerShell string literal.
func psToastEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// showWindowsToast raises a native Windows toast via the WinRT notification
// API. Dependency-free: shells one PowerShell command under a timeout.
func showWindowsToast(title, body string) error {
	const appID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`
	script := fmt.Sprintf(`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null;`+
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);`+
		`$n = $t.GetElementsByTagName('text');`+
		`$n.Item(0).AppendChild($t.CreateTextNode('%s')) | Out-Null;`+
		`$n.Item(1).AppendChild($t.CreateTextNode('%s')) | Out-Null;`+
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('%s').Show([Windows.UI.Notifications.ToastNotification]::new($t))`,
		psToastEscape(title), psToastEscape(body), appID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}
