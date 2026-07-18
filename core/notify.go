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
// removes that dependency (L-0429) and is the sole Inbox delivery channel
// for both dispatched and manually written RESULT letters.
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
	Letter               string
	Thread               string
	Summary              string
	Path                 string
	SourceAgentSessionID string
	SourceSessionPath    string
	Status               string
	Generation           string
	OpenPoints           []string
	Update               receiptUpdate
}

type receiptSection struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
}

type receiptUpdate struct {
	Sections []receiptSection `json:"sections,omitempty"`
}

func resultSections(body string) []receiptSection {
	var sections []receiptSection
	var heading string
	var lines []string
	flush := func() {
		if heading == "" {
			return
		}
		sections = append(sections, receiptSection{Heading: heading, Body: strings.TrimSpace(strings.Join(lines, "\n"))})
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			lines = nil
			continue
		}
		if heading != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return sections
}

func extractOpenPoints(body string) []string {
	var points []string
	for _, section := range resultSections(body) {
		if section.Heading != "Open Points" && section.Heading != "Open Questions" {
			continue
		}
		for _, line := range strings.Split(section.Body, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "*"))
			if line != "" {
				points = append(points, line)
			}
		}
	}
	return points
}

func diffResultSections(previous, current string) receiptUpdate {
	if strings.TrimSpace(previous) == "" {
		return receiptUpdate{}
	}
	previousBodies := make(map[string]string)
	for _, section := range resultSections(previous) {
		previousBodies[section.Heading] = section.Body
	}
	var changed []receiptSection
	for _, section := range resultSections(current) {
		if previousBody, ok := previousBodies[section.Heading]; !ok || previousBody != section.Body {
			changed = append(changed, section)
		}
	}
	return receiptUpdate{Sections: changed}
}

// resultFileInfo describes one threads/**/*.result.md file discovered by
// scanResultFiles.
type resultFileInfo struct {
	Letter  string
	Thread  string
	Path    string
	ModTime time.Time
}

func resolveLetterResult(threadsDir, letter string) (resultFileInfo, []byte, error) {
	if !validLetterID(letter) {
		return resultFileInfo{}, nil, fmt.Errorf("invalid letter ID %q", letter)
	}
	files, err := scanResultFiles(threadsDir)
	if err != nil {
		return resultFileInfo{}, nil, err
	}
	var matches []resultFileInfo
	for _, file := range files {
		if file.Letter == letter {
			matches = append(matches, file)
		}
	}
	if len(matches) != 1 {
		return resultFileInfo{}, nil, fmt.Errorf("RESULT for %s: expected one match, found %d", letter, len(matches))
	}
	body, err := os.ReadFile(matches[0].Path)
	if err != nil {
		return resultFileInfo{}, nil, fmt.Errorf("read RESULT for %s: %w", letter, err)
	}
	return matches[0], body, nil
}

func validLetterID(letter string) bool {
	if len(letter) < 3 || letter[0] != 'L' || letter[1] != '-' {
		return false
	}
	for _, ch := range letter[2:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func formatLetterSourceEnvelope(letter, path, sourceSessionPath string, source []byte, query string) string {
	var b strings.Builder
	b.WriteString("[LETTER SOURCE]\n")
	fmt.Fprintf(&b, "L-ID: %s\nResult path: %s\n", letter, path)
	if sourceSessionPath != "" {
		fmt.Fprintf(&b, "Source session: %s\n", sourceSessionPath)
	}
	b.WriteString("Instruction: Treat the following as the exact source for this L-ID. Do not search for another copy.\n---\n")
	b.Write(source)
	b.WriteString("\n---")
	if query = strings.TrimSpace(query); query != "" {
		b.WriteString("\n[Boss query]\n")
		b.WriteString(query)
	}
	return b.String()
}

type notifyLedger struct {
	Seeded   bool                     `json:"seeded"`
	Notified map[string]string        `json:"notified"`
	Receipts map[string]receiptRecord `json:"receipts"`
}

type receiptRecord struct {
	Thread               string          `json:"thread"`
	Summary              string          `json:"summary"`
	ResultPath           string          `json:"result_path"`
	SourceAgentSessionID string          `json:"source_agent_session_id,omitempty"`
	SourceSessionPath    string          `json:"source_session_path,omitempty"`
	Generation           string          `json:"generation"`
	Card                 *MessageLocator `json:"card,omitempty"`
	Status               string          `json:"status"`
	ArrivedAt            string          `json:"arrived_at"`
	AcknowledgedAt       string          `json:"acknowledged_at,omitempty"`
	AcknowledgedBy       string          `json:"acknowledged_by,omitempty"`
	ForwardedAt          string          `json:"forwarded_at,omitempty"`
	OpenPoints           []string        `json:"open_points,omitempty"`
	Update               receiptUpdate   `json:"update,omitempty"`
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

func (s *notifyStore) diffBasePath(letter string) string {
	return filepath.Join(filepath.Dir(s.path), "notify_diff_cache", letter+".md")
}

func (s *notifyStore) updateDiffBase(letter string, current []byte) (receiptUpdate, error) {
	if s == nil || strings.TrimSpace(letter) == "" {
		return receiptUpdate{}, nil
	}
	path := s.diffBasePath(letter)
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return receiptUpdate{}, err
	}
	update := diffResultSections(string(previous), string(current))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return receiptUpdate{}, err
	}
	if err := AtomicWriteFile(path, current, 0o644); err != nil {
		return receiptUpdate{}, err
	}
	return update, nil
}

// pruneDiffBases removes rolling diff bases whose RESULT file no longer exists.
// Acknowledging a receipt deliberately does not remove its base: a later RESULT
// update must still be able to show what changed when it re-enters the Inbox.
func (s *notifyStore) pruneDiffBases(files []resultFileInfo) error {
	if s == nil {
		return nil
	}
	active := make(map[string]struct{}, len(files))
	for _, file := range files {
		active[file.Letter] = struct{}{}
	}
	dir := filepath.Dir(s.diffBasePath("placeholder"))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		letter := strings.TrimSuffix(entry.Name(), ".md")
		if _, exists := active[letter]; exists {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
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

type receiptArrival struct {
	Receipt  receiptRecord
	Previous receiptRecord
	Replaced bool
}

func (s *notifyStore) recordArrivalTransition(row indexResultRow) (receiptArrival, error) {
	if s == nil || strings.TrimSpace(row.Letter) == "" {
		return receiptArrival{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return receiptArrival{}, err
	}
	record, exists := ledger.Receipts[row.Letter]
	previous := record
	generation := row.Generation
	if generation == "" {
		generation = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if !exists {
		record = receiptRecord{
			Thread: row.Thread, Summary: row.Summary, ResultPath: row.Path,
			SourceSessionPath:    row.SourceSessionPath,
			SourceAgentSessionID: row.SourceAgentSessionID,
			Status:               row.Status, Generation: generation, ArrivedAt: generation,
			OpenPoints: row.OpenPoints, Update: row.Update,
		}
	} else if record.Generation == "" || generation > record.Generation {
		record.Thread = row.Thread
		record.Summary = row.Summary
		record.ResultPath = row.Path
		record.SourceSessionPath = row.SourceSessionPath
		record.SourceAgentSessionID = row.SourceAgentSessionID
		record.Status = row.Status
		record.Generation = generation
		record.ArrivedAt = generation
		record.OpenPoints = row.OpenPoints
		record.Update = row.Update
		if record.AcknowledgedAt != "" {
			record.AcknowledgedAt = ""
			record.AcknowledgedBy = ""
			record.ForwardedAt = ""
			record.Card = nil
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
	if record.Summary == "" {
		record.Summary = row.Summary
	}
	ledger.Receipts[row.Letter] = record
	if err := s.save(ledger); err != nil {
		return receiptArrival{}, err
	}
	return receiptArrival{Receipt: record, Previous: previous, Replaced: exists && generation != previous.Generation && generation > previous.Generation}, nil
}

func (s *notifyStore) recordArrival(row indexResultRow) error {
	_, err := s.recordArrivalTransition(row)
	return err
}

func (s *notifyStore) storeReceiptCard(letter string, card MessageLocator) error {
	if s == nil {
		return fmt.Errorf("receipt store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, err := s.load()
	if err != nil {
		return err
	}
	record, ok := ledger.Receipts[letter]
	if !ok {
		return fmt.Errorf("receipt %s not found", letter)
	}
	record.Card = &card
	ledger.Receipts[letter] = record
	return s.save(ledger)
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

// scanNewResultFiles returns files that are new or modified since their last
// Inbox delivery. Dispatch provenance enriches a receipt; it never changes
// which RESULT delivery path is used.
func scanNewResultFiles(files []resultFileInfo, ledger *notifyLedger) []resultFileInfo {
	var fresh []resultFileInfo
	for _, f := range files {
		if last, seen := ledger.Notified[f.Letter]; seen {
			if lastT, err := time.Parse(time.RFC3339Nano, last); err == nil && !f.ModTime.After(lastT) {
				continue
			}
		}
		ledger.Notified[f.Letter] = f.ModTime.Format(time.RFC3339Nano)
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
	return extractResultSummaryFromBody(string(data))
}

func extractResultSummaryFromBody(body string) string {
	lines := strings.Split(body, "\n")
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
	return extractResultStatusFromBody(string(data))
}

func extractResultStatusFromBody(body string) string {
	lines := strings.Split(body, "\n")
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

// declaredSourceSessionPath accepts only the exact RESULT front-matter key.
// External harnesses may provide it; ordinary body text is never searched.
func declaredSourceSessionPath(body string) string {
	return strings.TrimSpace(parseArchiveFrontMatter(body)["Source-Session-Path"])
}

// formatReceiptEnvelope gives a secretary the original RESULT path without
// asking it to locate the letter itself.
func formatReceiptEnvelope(letter string, record receiptRecord) string {
	path := ""
	if record.SourceSessionPath != "" {
		path = "\n来源会话：" + record.SourceSessionPath
	} else if record.SourceAgentSessionID != "" {
		path = "\n来源会话：unavailable"
	}
	return fmt.Sprintf("[RECEIPT %s]\n原信文件：%s\n线程：%s\n状态：%s%s\n\n请直接读取上述 RESULT 原信，并按正常译信流程处理。",
		letter, record.ResultPath, record.Thread, record.Status, path)
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

func formatReceiptUpdateBody(update receiptUpdate) string {
	if len(update.Sections) == 0 {
		return ""
	}
	var blocks []string
	for _, section := range update.Sections {
		blocks = append(blocks, section.Heading+"\n"+section.Body)
	}
	return strings.Join(blocks, "\n\n")
}

func receiptUpdatePages(update receiptUpdate) []string {
	runes := []rune(formatReceiptUpdateBody(update))
	if len(runes) == 0 {
		return nil
	}
	const pageSize = 3000
	var pages []string
	for len(runes) > 0 {
		n := pageSize
		if len(runes) < n {
			n = len(runes)
		}
		pages, runes = append(pages, string(runes[:n])), runes[n:]
	}
	return pages
}

const receiptCompactUpdateLimit = 1800
const receiptCompactTextLimit = 3500

// formatReceiptInboxCard renders the Boss-facing inbox card. A non-positive
// pageCount is the compact envelope; positive pageCount is an original page.
func formatReceiptInboxCard(i18n *I18n, letter string, record receiptRecord, body string, page, pageCount int) (string, [][]ButtonOption) {
	content := i18n.Tf(MsgReceiptCardCompact, letter, record.Thread, record.Status, record.Summary, record.ArrivedAt, record.ResultPath)
	if record.SourceSessionPath != "" {
		content += "\nSource session: " + record.SourceSessionPath
	} else if record.SourceAgentSessionID != "" {
		content += "\nSource session: unavailable"
	}
	if len(record.Update.Sections) > 0 {
		content = strings.Replace(content, "📬 "+letter, "📬 "+letter+" · "+i18n.T(MsgReceiptUpdated), 1)
	}
	if len(record.OpenPoints) > 0 {
		content += "\n\n" + i18n.T(MsgReceiptOpenPoints) + "\n• " + strings.Join(record.OpenPoints, "\n• ")
	}
	update := formatReceiptUpdateBody(record.Update)
	inlineUpdate := update != "" && len([]rune(update)) < receiptCompactUpdateLimit && len([]rune(content))+len([]rune(i18n.T(MsgReceiptChanges)))+len([]rune(update))+2 <= receiptCompactTextLimit
	if inlineUpdate {
		content += "\n\n" + i18n.T(MsgReceiptChanges) + "\n" + update
	}
	generation := record.Generation
	if generation == "" {
		generation = record.ArrivedAt
	}
	if pageCount <= 0 {
		buttons := []ButtonOption{
			{Text: i18n.T(MsgReceiptViewOriginal), Data: "cmd:/receipt page " + letter + " " + generation + " 0"},
			{Text: i18n.T(MsgReceiptReceive), Data: "cmd:/receipt receive " + letter + " " + generation},
			{Text: i18n.T(MsgReceiptHandoffPrimary), Data: "cmd:/receipt primary " + letter + " " + generation},
		}
		if update != "" && !inlineUpdate {
			buttons = append([]ButtonOption{{Text: i18n.T(MsgReceiptViewUpdate), Data: "cmd:/receipt update " + letter + " " + generation + " 0"}}, buttons...)
		}
		closeRow := []ButtonOption{{Text: i18n.T(MsgReceiptClose), Data: "cmd:/receipt close " + letter + " " + generation}}
		return content, [][]ButtonOption{buttons, closeRow}
	}
	content += "\n\n" + i18n.Tf(MsgReceiptCardPage, page+1, pageCount, body)
	var buttons [][]ButtonOption
	var pageButtons []ButtonOption
	if page > 0 {
		pageButtons = append(pageButtons, ButtonOption{Text: i18n.T(MsgCardPrev), Data: fmt.Sprintf("cmd:/receipt page %s %s %d", letter, generation, page-1)})
	}
	if page+1 < pageCount {
		pageButtons = append(pageButtons, ButtonOption{Text: i18n.T(MsgCardNext), Data: fmt.Sprintf("cmd:/receipt page %s %s %d", letter, generation, page+1)})
	}
	if len(pageButtons) > 0 {
		buttons = append(buttons, pageButtons)
	}
	buttons = append(buttons, []ButtonOption{
		{Text: i18n.T(MsgReceiptCollapse), Data: "cmd:/receipt collapse " + letter + " " + generation},
		{Text: i18n.T(MsgReceiptReceive), Data: "cmd:/receipt receive " + letter + " " + generation},
		{Text: i18n.T(MsgReceiptHandoffPrimary), Data: "cmd:/receipt primary " + letter + " " + generation},
	})
	buttons = append(buttons, []ButtonOption{{Text: i18n.T(MsgReceiptClose), Data: "cmd:/receipt close " + letter + " " + generation}})
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
	if err := e.notifyStore.pruneDiffBases(files); err != nil {
		slog.Warn("notify: failed to prune stale diff bases", "error", err)
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
			if body, readErr := os.ReadFile(f.Path); readErr != nil {
				slog.Warn("notify: failed to seed diff base", "letter", f.Letter, "error", readErr)
			} else if _, baseErr := e.notifyStore.updateDiffBase(f.Letter, body); baseErr != nil {
				slog.Warn("notify: failed to seed diff base", "letter", f.Letter, "error", baseErr)
			}
		}
		ledger.Seeded = true
		if err := e.notifyStore.save(ledger); err != nil {
			slog.Warn("notify: failed to seed ledger", "error", err)
		}
		slog.Info("notify: ledger seeded", "files", len(files))
		return
	}

	fresh := scanNewResultFiles(files, &ledger)
	if len(fresh) == 0 {
		return
	}
	if err := e.notifyStore.save(ledger); err != nil {
		slog.Warn("notify: failed to save ledger", "error", err)
		return
	}
	for _, f := range fresh {
		body, err := os.ReadFile(f.Path)
		if err != nil {
			slog.Warn("notify: failed to read result", "letter", f.Letter, "error", err)
			continue
		}
		update, err := e.notifyStore.updateDiffBase(f.Letter, body)
		if err != nil {
			slog.Warn("notify: diff base unavailable", "letter", f.Letter, "error", err)
			update = receiptUpdate{}
		}
		sourceAgentSessionID, sourceSessionPath := e.ensureDispatchStore().resultProvenance(f.Letter)
		if sourceSessionPath == "" {
			sourceSessionPath = declaredSourceSessionPath(string(body))
		}
		e.notifyLetterArrived(indexResultRow{
			Letter:               f.Letter,
			Thread:               f.Thread,
			Summary:              extractResultSummaryFromBody(string(body)),
			Path:                 f.Path,
			SourceAgentSessionID: sourceAgentSessionID,
			SourceSessionPath:    sourceSessionPath,
			Status:               extractResultStatusFromBody(string(body)),
			Generation:           f.ModTime.UTC().Format(time.RFC3339Nano),
			OpenPoints:           extractOpenPoints(string(body)),
			Update:               update,
		})
	}
}

func (e *Engine) notifyLetterArrived(row indexResultRow) {
	slog.Info("notify: letter arrived", "letter", row.Letter, "thread", row.Thread)
	arrival, err := e.notifyStore.recordArrivalTransition(row)
	if err != nil {
		slog.Warn("notify: failed to record receipt", "letter", row.Letter, "error", err)
		return
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
			if cards, ok := p.(ReceiptCardManager); ok && e.notifyStore != nil {
				content, cardButtons := formatReceiptInboxCard(e.i18n, row.Letter, arrival.Receipt, "", 0, 0)
				if arrival.Replaced && arrival.Previous.AcknowledgedAt == "" && arrival.Previous.Card != nil {
					if err := cards.UpdateReceiptCard(ctx, *arrival.Previous.Card, content, cardButtons); err != nil {
						slog.Warn("notify: failed to replace receipt card", "letter", row.Letter, "error", err)
						if card, sendErr := cards.SendReceiptCard(ctx, replyCtx, content, cardButtons); sendErr != nil {
							slog.Warn("notify: failed to send replacement receipt card", "letter", row.Letter, "error", sendErr)
						} else if storeErr := e.notifyStore.storeReceiptCard(row.Letter, card); storeErr != nil {
							slog.Warn("notify: failed to persist replacement receipt card", "letter", row.Letter, "error", storeErr)
						}
					}
					break
				}
				if arrival.Receipt.Card != nil && !arrival.Replaced {
					break
				}
				card, err := cards.SendReceiptCard(ctx, replyCtx, content, cardButtons)
				if err != nil {
					slog.Warn("notify: failed to send receipt card", "letter", row.Letter, "error", err)
					break
				}
				if err := e.notifyStore.storeReceiptCard(row.Letter, card); err != nil {
					slog.Warn("notify: failed to persist receipt card", "letter", row.Letter, "error", err)
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
