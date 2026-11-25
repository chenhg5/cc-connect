package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// documentEdit addresses a key by its TOML table and, for array tables, by
// the indexes already resolved by the config decoder. A nil value deletes it.
type documentEdit struct {
	table    string
	project  int
	platform int
	provider int
	key      string
	value    *string
}

func documentValue(v any) *string {
	var out strings.Builder
	if err := toml.NewEncoder(&out).Encode(map[string]any{"value": v}); err != nil {
		panic(fmt.Sprintf("encode settings value: %v", err)) // only built-in scalar and []string values reach here
	}
	s := strings.TrimSpace(strings.TrimPrefix(out.String(), "value = "))
	return &s
}

func settingEdit(table string, project, platform int, key string, value any) documentEdit {
	return documentEdit{table: table, project: project, platform: platform, provider: -1, key: key, value: documentValue(value)}
}

func deleteSetting(table string, project, platform int, key string) documentEdit {
	return documentEdit{table: table, project: project, platform: platform, provider: -1, key: key}
}

// saveSettingsDocument is called under configMu. Failed source or semantic
// validation uses the old encoder, but first retains the exact input as .bak.
func saveSettingsDocument(original []byte, expected *Config, edits []documentEdit) error {
	if len(edits) == 0 {
		return nil
	}
	return saveDocumentPatch(original, expected, func(raw string) (string, error) {
		var err error
		for _, edit := range edits {
			raw, err = patchDocument(raw, edit)
			if err != nil {
				return "", err
			}
		}
		return raw, nil
	})
}

func saveDocumentPatch(original []byte, expected *Config, patch func(string) (string, error)) error {
	patched, patchErr := patch(string(original))
	if patchErr == nil {
		var actual Config
		if err := toml.Unmarshal([]byte(patched), &actual); err != nil {
			patchErr = safePatchedDocumentError(err)
		} else if !reflect.DeepEqual(&actual, expected) {
			patchErr = fmt.Errorf("patched settings differ from requested configuration")
		}
	}
	if patchErr != nil {
		if err := validateLegacySettingsSave(expected); err != nil {
			return fmt.Errorf("document patch failed (%v); legacy save is unsafe: %w", patchErr, err)
		}
		current, err := os.ReadFile(ConfigPath)
		if err != nil {
			return fmt.Errorf("recheck config before fallback: %w", err)
		}
		if !bytes.Equal(current, original) {
			return fmt.Errorf("config changed outside this process during settings save")
		}
		if err := writeConfigBackup(original); err != nil {
			return fmt.Errorf("document patch failed (%v); backup failed: %w", patchErr, err)
		}
		slog.Warn("config: document patch validation failed; using legacy full-file save", "error", patchErr, "backup", ConfigPath+".bak")
		return saveConfig(expected)
	}
	if patched == string(original) {
		return nil
	}
	return writeConfigAtomically(original, []byte(patched))
}

// BurntSushi's parse error may include a source key or value. Keep its line
// number for the operator without copying any TOML content into the log.
func safePatchedDocumentError(err error) error {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) && parseErr.Position.Line > 0 {
		return fmt.Errorf("parse patched document: line %d: invalid TOML", parseErr.Position.Line)
	}
	return fmt.Errorf("parse patched document: invalid TOML")
}

func validateLegacySettingsSave(expected *Config) error {
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(expected); err != nil {
		return err
	}
	var decoded Config
	return toml.Unmarshal([]byte(formatTOML(encoded.String())), &decoded)
}

func writeConfigBackup(original []byte) error {
	return writeAtomicFile(ConfigPath+".bak", original, 0600)
}

func writeConfigAtomically(original, patched []byte) error {
	current, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("recheck config before save: %w", err)
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("config changed outside this process during settings save")
	}
	info, err := os.Stat(ConfigPath)
	if err != nil {
		return fmt.Errorf("stat config: %w", err)
	}
	return writeAtomicFile(ConfigPath, patched, info.Mode().Perm())
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-patch-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type documentEntry struct {
	table              string
	project, platform  int
	provider           int
	key                string
	lineStart, valueAt int
	valueEnd, lineEnd  int
}

type documentTable struct {
	name              string
	project, platform int
	provider          int
	start, end        int
}

func patchDocument(raw string, edit documentEdit) (string, error) {
	entries, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	var found *documentEntry
	for i := range entries {
		e := &entries[i]
		if e.table == edit.table && e.project == edit.project && e.platform == edit.platform && e.provider == edit.provider && e.key == edit.key {
			if found != nil {
				return "", fmt.Errorf("ambiguous key %s in [%s]", edit.key, edit.table)
			}
			found = e
		}
	}
	if found != nil {
		if edit.value == nil {
			return raw[:found.lineStart] + raw[found.lineEnd:], nil
		}
		if sameTOMLValue(raw[found.valueAt:found.valueEnd], *edit.value) {
			return raw, nil
		}
		return raw[:found.valueAt] + *edit.value + raw[found.valueEnd:], nil
	}
	if edit.value == nil {
		return raw, nil
	}
	var target *documentTable
	for i := range tables {
		t := &tables[i]
		if t.name == edit.table && t.project == edit.project && t.platform == edit.platform && t.provider == edit.provider {
			if target != nil {
				return "", fmt.Errorf("ambiguous table [%s]", edit.table)
			}
			target = t
		}
	}
	line := documentKey(edit.key) + " = " + *edit.value + documentNewline(raw)
	if target != nil {
		return insertDocument(raw, target.end, line), nil
	}
	if edit.table == "" {
		return insertDocument(raw, 0, line), nil
	}
	if edit.provider >= 0 {
		_, ownerEnd, err := providerDocumentSpan(tables, len(raw), edit.table, edit.project, edit.provider)
		if err != nil {
			return "", err
		}
		return insertDocument(raw, ownerEnd, documentNewline(raw)+"["+edit.table+"]"+documentNewline(raw)+line), nil
	}
	// A missing nested table belongs to the selected project/platform. Insert
	// it at the end of its owning array-table block, before the next sibling.
	if edit.project >= 0 {
		owner := documentOwnerEnd(tables, len(raw), edit.project, edit.platform)
		if owner < 0 {
			return "", fmt.Errorf("cannot locate owner for [%s]", edit.table)
		}
		return insertDocument(raw, owner, documentNewline(raw)+"["+edit.table+"]"+documentNewline(raw)+line), nil
	}
	return insertDocument(raw, len(raw), documentNewline(raw)+"["+edit.table+"]"+documentNewline(raw)+line), nil
}

func documentKey(key string) string {
	for _, c := range key {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !valid {
			return strconv.Quote(key)
		}
	}
	return key
}

func providerDocumentSpan(tables []documentTable, total int, table string, project, provider int) (int, int, error) {
	base := "providers"
	if strings.HasPrefix(table, "projects.agent.providers") {
		base = "projects.agent.providers"
	}
	start := -1
	for _, t := range tables {
		if t.name == base && t.project == project && t.provider == provider {
			start = t.start
			break
		}
	}
	if start < 0 {
		return 0, 0, fmt.Errorf("cannot locate provider %d in %s", provider, base)
	}
	end := total
	for _, t := range tables {
		if t.start <= start {
			continue
		}
		belongs := t.name == base || strings.HasPrefix(t.name, base+".")
		if t.project != project || t.provider != provider || !belongs {
			end = t.start
			break
		}
	}
	return start, end, nil
}

func documentOwnerEnd(tables []documentTable, total, project, platform int) int {
	ownerStart := -1
	for _, table := range tables {
		if table.name == "projects" && table.project == project {
			ownerStart = table.start
		}
		if platform >= 0 && table.name == "projects.platforms" && table.project == project && table.platform == platform {
			ownerStart = table.start
		}
	}
	if ownerStart < 0 {
		return -1
	}
	for _, table := range tables {
		if table.start <= ownerStart {
			continue
		}
		if table.name == "projects" || (platform >= 0 && table.name == "projects.platforms" && table.project == project) {
			return table.start
		}
	}
	return total
}

func insertDocument(raw string, at int, addition string) string {
	if at > 0 && raw[at-1] != '\n' {
		addition = documentNewline(raw) + addition
	}
	return raw[:at] + addition + raw[at:]
}

func documentNewline(raw string) string {
	if strings.Contains(raw, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func sameTOMLValue(a, b string) bool {
	var va, vb map[string]any
	if _, err := toml.Decode("value = "+a, &va); err != nil {
		return false
	}
	if _, err := toml.Decode("value = "+b, &vb); err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// scanConfigDocument recognizes table and assignment spans without
// normalizing the source. Its lexical scan handles comments, quoted keys,
// multiline strings and collection values; unsupported syntax fails closed
// into the backup + legacy save path.
func scanConfigDocument(raw string) ([]documentEntry, []documentTable, error) {
	entries := make([]documentEntry, 0)
	tables := []documentTable{{name: "", project: -1, platform: -1, provider: -1, start: 0, end: len(raw)}}
	table, project, platform, provider := "", -1, -1, -1
	projectCount := -1
	globalProviderCount, inlineProviderCount := -1, -1
	for at := 0; at < len(raw); {
		lineEnd := strings.IndexByte(raw[at:], '\n')
		if lineEnd < 0 {
			lineEnd = len(raw)
		} else {
			lineEnd += at + 1
		}
		line := strings.TrimSpace(raw[at:lineEnd])
		if line == "" || strings.HasPrefix(line, "#") {
			at = lineEnd
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, array, err := parseDocumentHeader(line)
			if err != nil {
				return nil, nil, fmt.Errorf("line %d: %w", 1+strings.Count(raw[:at], "\n"), err)
			}
			tables[len(tables)-1].end = at
			if name == "projects" && array {
				projectCount++
				project = projectCount
				platform, provider, inlineProviderCount = -1, -1, -1
			} else if name == "projects.platforms" && array {
				project = projectCount
				platform++
				provider = -1
			} else if strings.HasPrefix(name, "projects.platforms.") {
				project = projectCount
				provider = -1
			} else if name == "projects.agent.providers" && array {
				project = projectCount
				platform = -1
				inlineProviderCount++
				provider = inlineProviderCount
			} else if strings.HasPrefix(name, "projects.agent.providers.") {
				project = projectCount
				platform = -1
				provider = inlineProviderCount
			} else if strings.HasPrefix(name, "projects.") {
				project = projectCount
				platform, provider = -1, -1
			} else if name == "providers" && array {
				project, platform = -1, -1
				globalProviderCount++
				provider = globalProviderCount
			} else if strings.HasPrefix(name, "providers.") {
				project, platform = -1, -1
				provider = globalProviderCount
			} else if name != "projects" {
				project, platform, provider = -1, -1, -1
			}
			table = name
			tables = append(tables, documentTable{name: table, project: project, platform: platform, provider: provider, start: at, end: len(raw)})
			at = lineEnd
			continue
		}
		key, eq, err := parseDocumentAssignment(raw[at:lineEnd])
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", 1+strings.Count(raw[:at], "\n"), err)
		}
		valueAt := at + eq + 1
		for valueAt < len(raw) && (raw[valueAt] == ' ' || raw[valueAt] == '\t') {
			valueAt++
		}
		valueEnd, assignmentEnd, err := scanDocumentValue(raw, valueAt)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", 1+strings.Count(raw[:at], "\n"), err)
		}
		entries = append(entries, documentEntry{table: table, project: project, platform: platform, provider: provider, key: key, lineStart: at, valueAt: valueAt, valueEnd: valueEnd, lineEnd: assignmentEnd})
		at = assignmentEnd
	}
	return entries, tables, nil
}

func parseDocumentHeader(line string) (string, bool, error) {
	array := strings.HasPrefix(line, "[[")
	open, close := 1, 1
	if array {
		open, close = 2, 2
	}
	end := strings.Index(line[open:], strings.Repeat("]", close))
	if end < 0 {
		return "", false, fmt.Errorf("unsupported TOML header")
	}
	end += open
	if tail := strings.TrimSpace(line[end+close:]); tail != "" && !strings.HasPrefix(tail, "#") {
		return "", false, fmt.Errorf("unsupported TOML header suffix")
	}
	parts, err := parseDocumentName(line[open:end])
	if err != nil {
		return "", false, err
	}
	return strings.Join(parts, "."), array, nil
}

func parseDocumentAssignment(line string) (string, int, error) {
	quote, escape := byte(0), false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escape {
			escape = false
			continue
		}
		if quote != 0 {
			if quote == '"' && c == '\\' {
				escape = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '=' {
			parts, err := parseDocumentName(line[:i])
			if err != nil {
				return "", 0, fmt.Errorf("unsupported TOML assignment key")
			}
			return strings.Join(parts, "."), i, nil
		}
	}
	return "", 0, fmt.Errorf("unsupported TOML line")
}

func parseDocumentName(raw string) ([]string, error) {
	var parts []string
	for s := strings.TrimSpace(raw); s != ""; {
		var part string
		if s[0] == '"' || s[0] == '\'' {
			quote := s[0]
			i, escaped := 1, false
			for ; i < len(s); i++ {
				if quote == '"' && s[i] == '\\' && !escaped {
					escaped = true
					continue
				}
				if s[i] == quote && !escaped {
					break
				}
				escaped = false
			}
			if i >= len(s) {
				return nil, fmt.Errorf("unterminated TOML name")
			}
			if quote == '"' {
				v, err := strconv.Unquote(s[:i+1])
				if err != nil {
					return nil, err
				}
				part = v
			} else {
				part = s[1:i]
			}
			s = strings.TrimSpace(s[i+1:])
		} else {
			i := strings.IndexByte(s, '.')
			if i < 0 {
				i = len(s)
			}
			part = strings.TrimSpace(s[:i])
			for _, c := range part {
				valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
				if !valid {
					return nil, fmt.Errorf("unsupported TOML name")
				}
			}
			s = strings.TrimSpace(s[i:])
		}
		if part == "" {
			return nil, fmt.Errorf("empty TOML name")
		}
		parts = append(parts, part)
		if s == "" {
			break
		}
		if s[0] != '.' {
			return nil, fmt.Errorf("unsupported TOML name")
		}
		s = strings.TrimSpace(s[1:])
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty TOML name")
	}
	return parts, nil
}

func scanDocumentValue(raw string, at int) (int, int, error) {
	quote, triple, escape := byte(0), false, false
	square, curly := 0, 0
	for i := at; i < len(raw); i++ {
		c := raw[i]
		if quote != 0 {
			if escape {
				escape = false
				continue
			}
			if quote == '"' && c == '\\' {
				escape = true
				continue
			}
			if triple && i+2 < len(raw) && raw[i:i+3] == strings.Repeat(string(quote), 3) {
				quote, triple = 0, false
				i += 2
			} else if !triple && c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			if i+2 < len(raw) && raw[i:i+3] == strings.Repeat(string(c), 3) {
				triple = true
				i += 2
			}
		case '[':
			square++
		case ']':
			square--
		case '{':
			curly++
		case '}':
			curly--
		case '#':
			if square == 0 && curly == 0 {
				end := i
				for end > at && (raw[end-1] == ' ' || raw[end-1] == '\t') {
					end--
				}
				lineEnd := strings.IndexByte(raw[i:], '\n')
				if lineEnd < 0 {
					return end, len(raw), nil
				}
				return end, i + lineEnd + 1, nil
			}
			lineEnd := strings.IndexByte(raw[i:], '\n')
			if lineEnd < 0 {
				return 0, 0, fmt.Errorf("unfinished collection comment")
			}
			i += lineEnd
		case '\n':
			if square == 0 && curly == 0 {
				end := i
				for end > at && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\r') {
					end--
				}
				return end, i + 1, nil
			}
		}
		if square < 0 || curly < 0 {
			return 0, 0, fmt.Errorf("unbalanced TOML collection")
		}
	}
	if quote != 0 || square != 0 || curly != 0 {
		return 0, 0, fmt.Errorf("unterminated TOML value")
	}
	end := len(raw)
	for end > at && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\r') {
		end--
	}
	return end, len(raw), nil
}
