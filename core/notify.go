package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NotifyConfig wires the result.md watcher: any threads/**/*.result.md
// created or modified by any seat (dispatched or not) is pushed to the
// Secretary session and/or the local desktop. Under the C' protocol, INDEX
// write authority belongs solely to the Secretary, who appends the RESULT
// row only after already having seen and validated the result — so watching
// INDEX.md can never notify the Secretary about its own inbox (it would be
// the Secretary waiting on itself). Watching result.md files directly
// removes that dependency (L-0429). Dispatched letters still get their
// [RESULT_READY] from the dispatch watcher's own result.md polling
// (L-0382); this watcher additionally covers non-dispatched manual letters
// and is the sole channel for them.
//
// IndexPath anchors the archive root: threads/ is resolved as its sibling
// directory. The field name is kept for config-compatibility with existing
// notify_index_path deployments even though INDEX.md's contents are no
// longer parsed.
type NotifyConfig struct {
	Enabled           bool
	IndexPath         string
	PollInterval      time.Duration
	Platform          string // platform name for session injection; default "telegram"
	SessionKey        string // Secretary session receiving [LETTER_ARRIVED]
	ReceiptSessionKey string // Secretary session that processes acknowledged receipts
	TelegramEnabled   bool
	ToastEnabled      bool
}

// threadsDir returns the threads/ directory that sits alongside INDEX.md at
// the archive root.
func (c NotifyConfig) threadsDir() string {
	return filepath.Join(filepath.Dir(c.IndexPath), "threads")
}

type indexResultRow struct {
	Letter  string
	Thread  string
	Summary string
	Path    string
	Status  string
}

// resultFileInfo describes one threads/**/*.result.md file discovered by
// scanResultFiles.
type resultFileInfo struct {
	Letter  string
	Thread  string
	Path    string
	ModTime time.Time
}

type notifyLedger struct {
	Seeded   bool                     `json:"seeded"`
	Notified map[string]string        `json:"notified"`
	Receipts map[string]receiptRecord `json:"receipts"`
}

type receiptRecord struct {
	Thread         string `json:"thread"`
	Summary        string `json:"summary"`
	ResultPath     string `json:"result_path"`
	Status         string `json:"status"`
	ArrivedAt      string `json:"arrived_at"`
	AcknowledgedAt string `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string `json:"acknowledged_by,omitempty"`
	ForwardedAt    string `json:"forwarded_at,omitempty"`
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
	ledger := notifyLedger{Notified: map[string]string{}, Receipts: map[string]receiptRecord{}}
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
	if ledger.Receipts == nil {
		ledger.Receipts = map[string]receiptRecord{}
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

func (s *notifyStore) recordArrival(row indexResultRow) error {
	if s == nil || strings.TrimSpace(row.Letter) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return err
	}
	record, exists := ledger.Receipts[row.Letter]
	if !exists {
		record = receiptRecord{
			Thread: row.Thread, Summary: compactReceiptSummary(row.Summary), ResultPath: row.Path,
			Status: row.Status, ArrivedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}
	if record.ResultPath == "" {
		record.ResultPath = row.Path
	}
	if record.Thread == "" {
		record.Thread = row.Thread
	}
	if record.Status == "" {
		record.Status = row.Status
	}
	if record.Summary == "" || len([]rune(record.Summary)) > 240 {
		record.Summary = compactReceiptSummary(row.Summary)
	}
	ledger.Receipts[row.Letter] = record
	return s.save(ledger)
}

func compactReceiptSummary(summary string) string {
	summary = strings.Join(strings.Fields(summary), " ")
	runes := []rune(summary)
	if len(runes) <= 240 {
		return summary
	}
	return string(runes[:237]) + "..."
}

func (s *notifyStore) acknowledge(letter, user string) (receiptRecord, bool, error) {
	if s == nil {
		return receiptRecord{}, false, fmt.Errorf("receipt store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return receiptRecord{}, false, err
	}
	record, exists := ledger.Receipts[letter]
	if !exists {
		return receiptRecord{}, false, fmt.Errorf("receipt %s not found", letter)
	}
	if record.AcknowledgedAt != "" {
		return record, false, nil
	}
	record.AcknowledgedAt = time.Now().UTC().Format(time.RFC3339)
	record.AcknowledgedBy = user
	ledger.Receipts[letter] = record
	return record, true, s.save(ledger)
}

func (s *notifyStore) markForwarded(letter string) (receiptRecord, bool, error) {
	if s == nil {
		return receiptRecord{}, false, fmt.Errorf("receipt store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return receiptRecord{}, false, err
	}
	record, exists := ledger.Receipts[letter]
	if !exists {
		return receiptRecord{}, false, fmt.Errorf("receipt %s not found", letter)
	}
	if record.ForwardedAt != "" {
		return record, false, nil
	}
	record.ForwardedAt = time.Now().UTC().Format(time.RFC3339)
	ledger.Receipts[letter] = record
	return record, true, s.save(ledger)
}

// restoreReceipt compensates a local ledger transition when its corresponding
// Telegram card operation fails. The caller supplies the record read before
// acknowledgement, so a retry remains a pending Inbox receipt.
func (s *notifyStore) restoreReceipt(letter string, previous receiptRecord) error {
	if s == nil {
		return fmt.Errorf("receipt store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return err
	}
	if _, exists := ledger.Receipts[letter]; !exists {
		return fmt.Errorf("receipt %s not found", letter)
	}
	ledger.Receipts[letter] = previous
	return s.save(ledger)
}

func (s *notifyStore) receipt(letter string) (receiptRecord, error) {
	if s == nil {
		return receiptRecord{}, fmt.Errorf("receipt store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return receiptRecord{}, err
	}
	record, exists := ledger.Receipts[letter]
	if !exists {
		return receiptRecord{}, fmt.Errorf("receipt %s not found", letter)
	}
	return record, nil
}

// scanResultFiles walks threadsDir for threads/<thread>/<letter>.result.md
// files. This is the authoritative signal that a letter has been answered —
// unlike INDEX.md's RESULT row, it exists the moment an Engineer writes the
// file, before any Secretary review (L-0429).
func scanResultFiles(threadsDir string) ([]resultFileInfo, error) {
	if _, err := os.Stat(threadsDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []resultFileInfo
	err := filepath.WalkDir(threadsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".result.md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, resultFileInfo{
			Letter:  strings.TrimSuffix(d.Name(), ".result.md"),
			Thread:  filepath.Base(filepath.Dir(path)),
			Path:    path,
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// scanNewResultFiles returns files that are new or modified since last
// notified, skipping letters covered by the dispatch ledger (those get
// [RESULT_READY] from the dispatch watcher's own result.md polling).
// Skipped-but-covered files are still recorded so the ledger stays tidy and
// never re-fires for them later.
func scanNewResultFiles(files []resultFileInfo, ledger *notifyLedger, dispatchCovered map[string]bool) []resultFileInfo {
	var fresh []resultFileInfo
	for _, f := range files {
		if last, seen := ledger.Notified[f.Letter]; seen {
			if lastT, err := time.Parse(time.RFC3339Nano, last); err == nil && !f.ModTime.After(lastT) {
				continue
			}
		}
		ledger.Notified[f.Letter] = f.ModTime.Format(time.RFC3339Nano)
		if dispatchCovered[f.Letter] {
			continue
		}
		fresh = append(fresh, f)
	}
	return fresh
}

// extractResultSummary pulls a one-line preview from a RESULT letter for the
// [LETTER_ARRIVED] notification body. DONE letters carry it under
// "## Conclusion"; STUCK/BLOCKED letters have no Conclusion section, so
// "## Blocker" is tried next.
func extractResultSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for _, heading := range []string{"## Conclusion", "## Blocker"} {
		if s := firstNonEmptyAfter(lines, heading); s != "" {
			return s
		}
	}
	return ""
}

// extractResultStatus reads Status from the RESULT header (before its closing
// --- separator) so body prose cannot override the receipt context.
func extractResultStatus(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		start = 1
	}
	end := -1
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return ""
	}
	for _, line := range lines[start:end] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}
	return ""
}

// formatReceiptEnvelope gives a secretary the original RESULT path without
// asking it to locate the letter itself.
func formatReceiptEnvelope(letter string, record receiptRecord) string {
	return fmt.Sprintf("[RECEIPT %s]\n原信文件：%s\n线程：%s\n状态：%s\n\n请直接读取上述 RESULT 原信，并按正常译信流程处理。",
		letter, record.ResultPath, record.Thread, record.Status)
}

func receiptOriginalPages(record receiptRecord, emptyText string) ([]string, error) {
	data, err := os.ReadFile(record.ResultPath)
	if err != nil {
		return nil, err
	}
	const pageSize = 3000
	runes := []rune(string(data))
	if len(runes) == 0 {
		return []string{emptyText}, nil
	}
	var pages []string
	for len(runes) > 0 {
		n := pageSize
		if len(runes) < n {
			n = len(runes)
		}
		pages = append(pages, string(runes[:n]))
		runes = runes[n:]
	}
	return pages, nil
}

// formatReceiptInboxCard renders the Boss-facing inbox card. A non-positive
// pageCount is the compact envelope; positive pageCount is an original page.
func formatReceiptInboxCard(i18n *I18n, letter string, record receiptRecord, body string, page, pageCount int) (string, [][]ButtonOption) {
	content := i18n.Tf(MsgReceiptCardCompact, letter, record.Thread, record.Status, record.Summary, record.ArrivedAt, record.ResultPath)
	if pageCount <= 0 {
		return content, [][]ButtonOption{{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/receipt page " + letter + " 0"},
			{Text: i18n.T(MsgReceiptReceive), Data: "cmd:/receipt receive " + letter},
			{Text: i18n.T(MsgReceiptHandoffPrimary), Data: "cmd:/receipt primary " + letter},
		}}
	}
	content += "\n\n" + i18n.Tf(MsgReceiptCardPage, page+1, pageCount, body)
	var buttons [][]ButtonOption
	var pageButtons []ButtonOption
	if page > 0 {
		pageButtons = append(pageButtons, ButtonOption{Text: i18n.T(MsgCardPrev), Data: fmt.Sprintf("cmd:/receipt page %s %d", letter, page-1)})
	}
	if page+1 < pageCount {
		pageButtons = append(pageButtons, ButtonOption{Text: i18n.T(MsgCardNext), Data: fmt.Sprintf("cmd:/receipt page %s %d", letter, page+1)})
	}
	if len(pageButtons) > 0 {
		buttons = append(buttons, pageButtons)
	}
	buttons = append(buttons, []ButtonOption{
		{Text: i18n.T(MsgReceiptCollapse), Data: "cmd:/receipt collapse " + letter},
		{Text: i18n.T(MsgReceiptReceive), Data: "cmd:/receipt receive " + letter},
		{Text: i18n.T(MsgReceiptHandoffPrimary), Data: "cmd:/receipt primary " + letter},
	})
	return content, buttons
}

func firstNonEmptyAfter(lines []string, heading string) string {
	for i, line := range lines {
		if strings.TrimSpace(line) != heading {
			continue
		}
		for _, next := range lines[i+1:] {
			if t := strings.TrimSpace(next); t != "" {
				return t
			}
		}
	}
	return ""
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
	threadsDir := e.notifyConfig.threadsDir()
	files, err := scanResultFiles(threadsDir)
	if err != nil {
		slog.Warn("notify: failed to scan result files", "path", threadsDir, "error", err)
		return
	}
	ledger, err := e.notifyStore.load()
	if err != nil {
		slog.Warn("notify: failed to load ledger", "error", err)
		return
	}

	// First run: seed every existing file without notifying, or the whole
	// archive history would fire at once.
	if !ledger.Seeded {
		for _, f := range files {
			ledger.Notified[f.Letter] = f.ModTime.Format(time.RFC3339Nano)
		}
		ledger.Seeded = true
		if err := e.notifyStore.save(ledger); err != nil {
			slog.Warn("notify: failed to seed ledger", "error", err)
		}
		slog.Info("notify: ledger seeded", "files", len(files))
		return
	}

	fresh := scanNewResultFiles(files, &ledger, e.dispatchStore.letters())
	if len(fresh) == 0 {
		return
	}
	if err := e.notifyStore.save(ledger); err != nil {
		slog.Warn("notify: failed to save ledger", "error", err)
		return
	}
	for _, f := range fresh {
		e.notifyLetterArrived(indexResultRow{
			Letter:  f.Letter,
			Thread:  f.Thread,
			Summary: extractResultSummary(f.Path),
			Path:    f.Path,
			Status:  extractResultStatus(f.Path),
		})
	}
}

func (e *Engine) notifyLetterArrived(row indexResultRow) {
	slog.Info("notify: letter arrived", "letter", row.Letter, "thread", row.Thread)
	if err := e.notifyStore.recordArrival(row); err != nil {
		slog.Warn("notify: failed to record receipt", "letter", row.Letter, "error", err)
	}
	if e.notifyConfig.TelegramEnabled && strings.TrimSpace(e.notifyConfig.SessionKey) != "" {
		content := fmt.Sprintf("📬 %s 到货", row.Letter)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, p := range e.platforms {
			if p.Name() != e.notifyConfig.Platform {
				continue
			}
			replyCtx := any(e.notifyConfig.SessionKey)
			if rc, ok := p.(ReplyContextReconstructor); ok {
				if reconstructed, err := rc.ReconstructReplyCtx(e.notifyConfig.SessionKey); err == nil {
					replyCtx = reconstructed
				}
			}
			if buttons, ok := p.(InlineButtonSender); ok && e.notifyStore != nil {
				receipt, err := e.notifyStore.receipt(row.Letter)
				if err == nil {
					content, cardButtons := formatReceiptInboxCard(e.i18n, row.Letter, receipt, "", 0, 0)
					err = buttons.SendWithButtons(ctx, replyCtx, content, cardButtons)
				}
				if err != nil {
					slog.Warn("notify: failed to send receipt button", "letter", row.Letter, "error", err)
					if err := p.Send(ctx, replyCtx, content); err != nil {
						slog.Warn("notify: failed to send fallback receipt notice", "letter", row.Letter, "error", err)
					}
				}
				break
			}
			if err := p.Send(ctx, replyCtx, content); err != nil {
				slog.Warn("notify: failed to send LETTER_ARRIVED", "letter", row.Letter, "error", err)
			}
			break
		}
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
