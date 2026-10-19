package cursor

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Compile-time checks for the history/memory parity interfaces.
var (
	_ core.HistoryProvider      = (*Agent)(nil)
	_ core.SessionTitleProvider = (*Agent)(nil)
	_ core.SessionIDValidator   = (*Agent)(nil)
	_ core.MemoryFileProvider   = (*Agent)(nil)
)

// cursorUserHomeDir is os.UserHomeDir in production. Tests replace it
// because macOS UserHomeDir ignores HOME.
var cursorUserHomeDir = os.UserHomeDir

const (
	cursorSQLiteTimeout  = 15 * time.Second
	cursorBlobBatchSize  = 40
	cursorMaxDecodedBlob = 8 << 20
	cursorMetaKey        = "0"
)

// cursorMaxHexResult caps bytes read from one sqlite3 invocation.
// Tests lower it to prove the reader stops before buffering the rest.
var cursorMaxHexResult = 16 << 20

// cursorSQLiteBin overrides the sqlite3 binary. Empty uses PATH.
// Tests point it at a script that writes past the cap and then blocks.
var cursorSQLiteBin string

// ProjectMemoryFile is <work_dir>/AGENTS.md. The Cursor Agent CLI loads
// AGENTS.md from the workspace and its ancestors as an always-applied rule.
func (a *Agent) ProjectMemoryFile() string {
	dir := "."
	if a != nil {
		if wd := strings.TrimSpace(a.GetWorkDir()); wd != "" {
			dir = wd
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Join(dir, "AGENTS.md")
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		abs = resolved
	}
	return filepath.Join(abs, "AGENTS.md")
}

// GlobalMemoryFile is empty. Cursor CLI 2026.10.14 loads project AGENTS.md
// (and ancestor copies) plus .cursor/rules, but user rules are account data,
// not a local instruction file. ~/.cursor/skills is a skill directory.
func (a *Agent) GlobalMemoryFile() string { return "" }

// ValidateSessionID reports whether sessionID is a safe name for a chat
// that exists under this workspace's Cursor store.
func (a *Agent) ValidateSessionID(_ context.Context, sessionID string) bool {
	dir, err := a.safeSessionDir(sessionID)
	if err != nil {
		return false
	}
	db := filepath.Join(dir, "store.db")
	info, err := os.Lstat(db)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	return true
}

// GetSessionTitle returns the store name, or the first real user query when
// the CLI left the session named "New Agent".
func (a *Agent) GetSessionTitle(sessionID string) string {
	dir, err := a.safeSessionDir(sessionID)
	if err != nil {
		return ""
	}
	meta, err := readCursorMeta(context.Background(), filepath.Join(dir, "store.db"))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(meta.Name)
	if name != "" && !strings.EqualFold(name, "New Agent") {
		return cursorSessionSummary(name, "", sessionID)
	}
	entries, err := a.readHistory(context.Background(), dir, 0)
	if err != nil {
		return cursorSessionSummary(name, "", sessionID)
	}
	first := ""
	for _, e := range entries {
		if e.Role == "user" && strings.TrimSpace(e.Content) != "" {
			first = e.Content
			break
		}
	}
	return cursorSessionSummary(name, first, sessionID)
}

// GetSessionHistory returns user and assistant turns in store order.
// limit <= 0 returns the full transcript; a positive limit keeps the last N
// entries. Injected CLI context (system prompt, user_info, reminders) is omitted.
func (a *Agent) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	dir, err := a.safeSessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	return a.readHistory(ctx, dir, limit)
}

func (a *Agent) safeSessionDir(sessionID string) (string, error) {
	home, err := cursorUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cursor: cannot determine home dir: %w", err)
	}
	workDir := "."
	if a != nil {
		if wd := strings.TrimSpace(a.GetWorkDir()); wd != "" {
			workDir = wd
		}
	}
	return findCursorSessionDir(home, workDir, sessionID)
}

func rejectUnsafeSessionID(sessionID string) error {
	if sessionID == "" || strings.TrimSpace(sessionID) != sessionID {
		return fmt.Errorf("cursor: invalid session id")
	}
	if sessionID == "." || sessionID == ".." || filepath.IsAbs(sessionID) {
		return fmt.Errorf("cursor: invalid session id")
	}
	if strings.Contains(sessionID, "..") || strings.ContainsAny(sessionID, `/\`) {
		return fmt.Errorf("cursor: invalid session id")
	}
	if filepath.Clean(sessionID) != sessionID || filepath.Base(sessionID) != sessionID {
		return fmt.Errorf("cursor: invalid session id")
	}
	return nil
}

// cursorWorkspaceKeys returns path strings whose MD5 the CLI may use as the
// chat directory name. The current CLI canonicalizes symlinks (/tmp ->
// /private/tmp). The absolute path without symlink resolution is kept as a
// legacy fallback.
func cursorWorkspaceKeys(workDir string) []string {
	abs, err := filepath.Abs(workDir)
	if err != nil || abs == "" {
		abs = workDir
	}
	keys := []string{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, existing := range keys {
			if existing == p {
				return
			}
		}
		keys = append(keys, p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		add(resolved)
	}
	add(abs)
	add(workDir)
	return keys
}

func cursorCandidateChatDirs(homeDir, workDir string) []string {
	var dirs []string
	seen := map[string]struct{}{}
	for _, key := range cursorWorkspaceKeys(workDir) {
		sum := md5.Sum([]byte(key))
		hash := hex.EncodeToString(sum[:])
		for _, base := range cursorChatsBaseDirs(homeDir) {
			dir := filepath.Join(base, hash)
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			info, err := os.Lstat(dir)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue
			}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func sessionDirWithinWorkspace(workspaceDir, sessionDir string) error {
	info, err := os.Lstat(sessionDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor: session directory is a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("cursor: session path is not a directory")
	}
	if err := pathInside(workspaceDir, sessionDir); err != nil {
		return fmt.Errorf("cursor: session path escapes workspace: %w", err)
	}
	db := filepath.Join(sessionDir, "store.db")
	dbInfo, err := os.Lstat(db)
	if err != nil {
		return err
	}
	if dbInfo.Mode()&os.ModeSymlink != 0 || !dbInfo.Mode().IsRegular() {
		return fmt.Errorf("cursor: store.db is not a regular file")
	}
	if err := pathInside(sessionDir, db); err != nil {
		return fmt.Errorf("cursor: store.db escapes session dir: %w", err)
	}
	return nil
}

func pathInside(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := relInside(rootAbs, targetAbs); err != nil {
		return err
	}
	rootRes, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return err
	}
	targetRes, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return err
	}
	return relInside(rootRes, targetRes)
}

func relInside(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s is outside %s", target, root)
	}
	return nil
}

type cursorMeta struct {
	Name       string
	RootBlobID string
}

func readCursorMeta(ctx context.Context, dbPath string) (cursorMeta, error) {
	out, err := runSQLite(ctx, dbPath, "SELECT value FROM meta WHERE key='"+cursorMetaKey+"' LIMIT 1;")
	if err != nil {
		return cursorMeta{}, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return cursorMeta{}, fmt.Errorf("cursor: session meta is empty")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		decoded = []byte(raw)
	}
	var m struct {
		Name       string `json:"name"`
		RootBlobID string `json:"latestRootBlobId"`
	}
	if err := json.Unmarshal(decoded, &m); err != nil {
		return cursorMeta{}, fmt.Errorf("cursor: decode session meta: %w", err)
	}
	if !blobIDHex(m.RootBlobID) && m.RootBlobID != "" {
		return cursorMeta{}, fmt.Errorf("cursor: root blob id is not hex")
	}
	return cursorMeta{Name: m.Name, RootBlobID: m.RootBlobID}, nil
}

func (a *Agent) readHistory(ctx context.Context, sessionDir string, limit int) ([]core.HistoryEntry, error) {
	dbPath := filepath.Join(sessionDir, "store.db")
	meta, err := readCursorMeta(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	if meta.RootBlobID == "" {
		return nil, fmt.Errorf("cursor: session has no root blob")
	}
	rootHex, err := runSQLite(ctx, dbPath, fmt.Sprintf(
		"SELECT hex(data) FROM blobs WHERE id='%s' LIMIT 1;", meta.RootBlobID,
	))
	if err != nil {
		return nil, err
	}
	rootHexStr := strings.TrimSpace(string(rootHex))
	if rootHexStr == "" {
		return nil, fmt.Errorf("cursor: root blob %s is missing", meta.RootBlobID)
	}
	rootBytes, err := hex.DecodeString(rootHexStr)
	if err != nil {
		return nil, fmt.Errorf("cursor: decode root blob: %w", err)
	}
	childIDs, err := cursorRootChildIDs(rootBytes)
	if err != nil {
		return nil, err
	}
	blobs, err := readBlobHexBatched(ctx, dbPath, childIDs)
	if err != nil {
		return nil, err
	}
	var entries []core.HistoryEntry
	for _, id := range childIDs {
		rawHex, ok := blobs[id]
		if !ok || rawHex == "" {
			continue
		}
		raw, err := hex.DecodeString(rawHex)
		if err != nil || len(raw) == 0 || raw[0] != '{' {
			continue
		}
		entry, ok := historyEntryFromBlob(raw)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func historyEntryFromBlob(raw []byte) (core.HistoryEntry, bool) {
	var msg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return core.HistoryEntry{}, false
	}
	switch msg.Role {
	case "user":
		text := historyUserText(msg.Content)
		if strings.TrimSpace(text) == "" {
			return core.HistoryEntry{}, false
		}
		return core.HistoryEntry{Role: "user", Content: text}, true
	case "assistant":
		text := assistantHistoryText(msg.Content)
		if strings.TrimSpace(text) == "" {
			return core.HistoryEntry{}, false
		}
		return core.HistoryEntry{Role: "assistant", Content: text}, true
	default:
		return core.HistoryEntry{}, false
	}
}

// historyUserText keeps <user_query> bodies, including internal newlines.
// Injected CLI wrappers with no query are dropped. Other user text is kept
// even when it starts with '<', which extractCursorUserSummary would discard.
func historyUserText(content any) string {
	text := cursorMessageContentText(content)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if m := cursorUserQueryRE.FindStringSubmatch(text); len(m) == 2 {
		if query := strings.TrimSpace(m[1]); query != "" {
			return query
		}
	}
	if injectedUserContext(text) {
		return ""
	}
	return text
}

func injectedUserContext(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{
		"<user_info>",
		"<open_and_recently_viewed_files>",
		"<attached_files>",
		"<agent_transcripts>",
		"<system_reminder>",
		"<rules>",
		"<timestamp>",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func assistantHistoryText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := m["type"].(string)
			if kind != "" && kind != "text" {
				continue
			}
			text, _ := m["text"].(string)
			if strings.TrimSpace(text) == "" {
				continue
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func cursorRootChildIDs(root []byte) ([]string, error) {
	var ids []string
	i := 0
	for i < len(root) {
		tag, n, err := readProtoVarint(root, i)
		if err != nil {
			return nil, fmt.Errorf("cursor: malformed root blob: %w", err)
		}
		i = n
		field := tag >> 3
		wt := tag & 7
		switch wt {
		case 0:
			_, n, err = readProtoVarint(root, i)
			if err != nil {
				return nil, fmt.Errorf("cursor: malformed root blob: %w", err)
			}
			i = n
		case 1:
			if len(root)-i < 8 {
				return nil, fmt.Errorf("cursor: malformed root blob: truncated fixed64")
			}
			i += 8
		case 5:
			if len(root)-i < 4 {
				return nil, fmt.Errorf("cursor: malformed root blob: truncated fixed32")
			}
			i += 4
		case 2:
			ln, n, err := readProtoVarint(root, i)
			if err != nil {
				return nil, fmt.Errorf("cursor: malformed root blob: %w", err)
			}
			i = n
			if i > len(root) || ln > uint64(len(root)-i) {
				return nil, fmt.Errorf("cursor: malformed root blob: bad length")
			}
			payload := root[i : i+int(ln)]
			i += int(ln)
			if field == 1 && ln == 32 {
				ids = append(ids, hex.EncodeToString(payload))
			}
		default:
			return nil, fmt.Errorf("cursor: malformed root blob: wire type %d", wt)
		}
	}
	return ids, nil
}

func readProtoVarint(buf []byte, i int) (uint64, int, error) {
	var n uint64
	shift := 0
	for i < len(buf) {
		if shift >= 64 {
			return 0, i, fmt.Errorf("bad varint")
		}
		b := buf[i]
		i++
		n |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return n, i, nil
		}
		shift += 7
	}
	return 0, i, fmt.Errorf("bad varint")
}

func blobIDHex(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func readBlobHexBatched(ctx context.Context, dbPath string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		query := "SELECT id || '|' || hex(data) FROM blobs WHERE id IN (" + strings.Join(batch, ",") + ");"
		raw, err := runSQLite(ctx, dbPath, query)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			id, hexData, ok := strings.Cut(line, "|")
			if !ok || !blobIDHex(id) {
				continue
			}
			if len(hexData) > cursorMaxDecodedBlob*2 {
				return fmt.Errorf("cursor: blob %s exceeds %d bytes", id, cursorMaxDecodedBlob)
			}
			out[id] = hexData
		}
		batch = batch[:0]
		return nil
	}
	for _, id := range ids {
		if !blobIDHex(id) {
			continue
		}
		batch = append(batch, "'"+id+"'")
		if len(batch) >= cursorBlobBatchSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func sqliteURI(dbPath, mode string) (string, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("cursor: sqlite path: %w", err)
	}
	return sqliteFileURI(filepath.ToSlash(abs), mode)
}

// sqliteFileURI builds a SQLite file URI from a slash-separated absolute path.
// A Windows drive path must be "/C:/..." so the letter is not parsed as the
// URI authority (file://C:/... is wrong; file:///C:/... is right). UNC paths
// stay file://server/share/...
func sqliteFileURI(absSlash, mode string) (string, error) {
	if mode != "ro" && mode != "rw" {
		return "", fmt.Errorf("cursor: sqlite mode %q", mode)
	}
	path := absSlash
	if strings.HasPrefix(path, "//") {
		rest := strings.TrimPrefix(path, "//")
		host, sharePath, ok := strings.Cut(rest, "/")
		if !ok || host == "" || sharePath == "" {
			return "", fmt.Errorf("cursor: sqlite unc path")
		}
		u := url.URL{Scheme: "file", Host: host, Path: "/" + sharePath, RawQuery: "mode=" + mode}
		return u.String(), nil
	}
	if len(path) >= 2 && path[1] == ':' && isDriveLetter(path[0]) && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode}
	return u.String(), nil
}

func isDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func runSQLite(ctx context.Context, dbPath, query string) ([]byte, error) {
	bin := cursorSQLiteBin
	if bin == "" {
		var err error
		bin, err = exec.LookPath("sqlite3")
		if err != nil {
			return nil, fmt.Errorf("cursor: sqlite3 not found in PATH: %w", err)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, cursorSQLiteTimeout)
	defer cancel()
	out, err := runSQLiteOnce(ctx, bin, dbPath, query, "ro")
	if err == nil || !retryWALOpen(err, dbPath) {
		return out, err
	}
	// A WAL database with no -wal/-shm cannot be opened mode=ro (error 14).
	// mode=rw creates those sidecars but query_only blocks writes. Never
	// use immutable=1: that ignores a live WAL.
	return runSQLiteOnce(ctx, bin, dbPath, query, "rw")
}

func retryWALOpen(err error, dbPath string) bool {
	if err == nil || !strings.Contains(err.Error(), "unable to open database") {
		return false
	}
	info, statErr := os.Lstat(dbPath)
	return statErr == nil && info.Mode().IsRegular()
}

func runSQLiteOnce(ctx context.Context, bin, dbPath, query, mode string) ([]byte, error) {
	uri, err := sqliteURI(dbPath, mode)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "-batch", "-cmd", "PRAGMA query_only=ON;", uri, query)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cursor: sqlite stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cursor: sqlite query failed: %w", err)
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(cursorMaxHexResult)+1))
	if len(out) > cursorMaxHexResult || readErr != nil {
		_ = stdout.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		if len(out) > cursorMaxHexResult {
			return nil, fmt.Errorf("cursor: sqlite result exceeds %d bytes", cursorMaxHexResult)
		}
		return nil, fmt.Errorf("cursor: sqlite query failed: %w", readErr)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("cursor: sqlite query timed out: %w", ctx.Err())
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("cursor: sqlite query failed: %s", msg)
		}
		return nil, fmt.Errorf("cursor: sqlite query failed: %w", waitErr)
	}
	return out, nil
}
