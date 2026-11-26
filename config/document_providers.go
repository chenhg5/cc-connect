package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

func loadDocumentConfigLocked() ([]byte, *Config, error) {
	if ConfigPath == "" {
		return nil, nil, fmt.Errorf("config path not set")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	return data, &cfg, nil
}

func providerEdit(table string, project, provider int, key string, value any) documentEdit {
	return documentEdit{table: table, project: project, platform: -1, provider: provider, key: key, value: documentValue(value)}
}

func providerDelete(table string, project, provider int, key string) documentEdit {
	return documentEdit{table: table, project: project, platform: -1, provider: provider, key: key}
}

func serializeProviderBlock(provider ProviderConfig, base string) (string, error) {
	var out strings.Builder
	value := struct {
		Providers []ProviderConfig `toml:"providers"`
	}{Providers: []ProviderConfig{provider}}
	if err := toml.NewEncoder(&out).Encode(value); err != nil {
		return "", err
	}
	block := out.String()
	if base != "providers" {
		block = strings.ReplaceAll(block, "[providers", "["+base)
	}
	return block, nil
}

func providerBlockBase(project int) string {
	if project >= 0 {
		return "projects.agent.providers"
	}
	return "providers"
}

func insertProviderBlock(raw string, project int, block string) (string, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	base := providerBlockBase(project)
	last := -1
	for _, table := range tables {
		if table.name == base && table.project == project {
			last = table.provider
		}
	}
	insertAt := len(raw)
	if last >= 0 {
		_, insertAt, err = providerDocumentSpan(tables, len(raw), base, project, last)
		if err != nil {
			return "", err
		}
	} else if project >= 0 {
		insertAt = documentOwnerEnd(tables, len(raw), project, -1)
		if insertAt < 0 {
			return "", fmt.Errorf("cannot locate project %d for provider insertion", project)
		}
		for _, table := range tables {
			if table.name == "projects.platforms" && table.project == project {
				insertAt = table.start
				break
			}
		}
	} else {
		for _, table := range tables {
			if table.name == "projects" {
				insertAt = table.start
				break
			}
		}
	}
	return insertDocument(raw, insertAt, documentNewline(raw)+block), nil
}

func removeProviderBlock(raw string, project, provider int) (string, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	start, end, err := providerDocumentSpan(tables, len(raw), providerBlockBase(project), project, provider)
	if err != nil {
		return "", err
	}
	return raw[:start] + raw[end:], nil
}

func providerModelSpans(raw, path string, project, provider int) ([]documentTable, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return nil, err
	}
	var spans []documentTable
	for _, table := range tables {
		if table.name == path && table.project == project && table.provider == provider {
			spans = append(spans, table)
		}
	}
	return spans, nil
}

func appendModelBlocks(base string, items []ProviderModelConfig) string {
	var out strings.Builder
	for _, item := range items {
		out.WriteString("[[")
		out.WriteString(base)
		out.WriteString("]]\nmodel = ")
		out.WriteString(*documentValue(item.Model))
		out.WriteByte('\n')
		if item.Alias != "" {
			out.WriteString("alias = ")
			out.WriteString(*documentValue(item.Alias))
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func patchProviderModels(raw, path, header string, project, provider int, old, next []ProviderModelConfig) (string, error) {
	spans, err := providerModelSpans(raw, path, project, provider)
	if err != nil {
		return "", err
	}
	if len(spans) != len(old) {
		return "", fmt.Errorf("model count mismatch in %s", path)
	}
	for i := len(old) - 1; i >= 0; i-- {
		span := spans[i]
		if i >= len(next) {
			raw = raw[:span.start] + raw[span.end:]
			continue
		}
		if old[i] == next[i] {
			continue
		}
		item := raw[span.start:span.end]
		for _, field := range []struct{ key, before, after string }{
			{"model", old[i].Model, next[i].Model},
			{"alias", old[i].Alias, next[i].Alias},
		} {
			item, err = patchProviderScalar(item, path, -1, -1, field.key, field.before, field.after, field.key == "model")
			if err != nil {
				return "", err
			}
		}
		raw = raw[:span.start] + item + raw[span.end:]
	}
	if len(next) <= len(old) {
		return raw, nil
	}
	spans, err = providerModelSpans(raw, path, project, provider)
	if err != nil {
		return "", err
	}
	var at int
	if len(spans) > 0 {
		at = spans[len(spans)-1].end
	} else {
		_, tables, err := scanConfigDocument(raw)
		if err != nil {
			return "", err
		}
		_, at, err = providerDocumentSpan(tables, len(raw), providerBlockBase(project), project, provider)
		if err != nil {
			return "", err
		}
	}
	return insertDocument(raw, at, documentNewline(raw)+appendModelBlocks(header, next[len(old):])), nil
}

func replaceProviderSubtree(raw, base string, project, provider int, replacement string) (string, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	start, end, seenEnd := -1, len(raw), false
	for _, table := range tables[1:] {
		matches := table.project == project && table.provider == provider && (table.name == base || strings.HasPrefix(table.name, base+"."))
		if matches {
			if seenEnd {
				return "", fmt.Errorf("noncontiguous provider section %s", base)
			}
			if start < 0 {
				start = table.start
			}
		} else if start >= 0 && !seenEnd {
			end, seenEnd = table.start, true
		}
	}
	if start < 0 {
		if replacement == "" {
			return raw, nil
		}
		_, at, err := providerDocumentSpan(tables, len(raw), providerBlockBase(project), project, provider)
		if err != nil {
			return "", err
		}
		return insertDocument(raw, at, documentNewline(raw)+replacement), nil
	}
	return raw[:start] + replacement + raw[end:], nil
}

func patchProviderScalar(raw, table string, project, provider int, key, old, new string, required bool) (string, error) {
	if old == new {
		return raw, nil
	}
	if new == "" && !required {
		return patchDocument(raw, providerDelete(table, project, provider, key))
	}
	return patchDocument(raw, providerEdit(table, project, provider, key, new))
}

func patchProviderMap(raw, table string, project, provider int, old, next map[string]string) (string, error) {
	if reflect.DeepEqual(old, next) {
		return raw, nil
	}
	if len(next) == 0 {
		return replaceProviderSubtree(raw, table, project, provider, "")
	}
	keys := make([]string, 0, len(old)+len(next))
	seen := make(map[string]bool)
	for key := range old {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range next {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := next[key]
		var err error
		if !ok {
			raw, err = patchDocument(raw, providerDelete(table, project, provider, key))
		} else if before, existed := old[key]; !existed || before != value {
			raw, err = patchDocument(raw, providerEdit(table, project, provider, key, value))
		}
		if err != nil {
			return "", err
		}
	}
	return raw, nil
}

func patchProviderUpdate(raw string, project, index int, old, next ProviderConfig) (string, error) {
	base := providerBlockBase(project)
	var err error
	for _, field := range []struct {
		key, old, next string
		required       bool
	}{
		{"api_key", old.APIKey, next.APIKey, true},
		{"base_url", old.BaseURL, next.BaseURL, false},
		{"model", old.Model, next.Model, false},
		{"thinking", old.Thinking, next.Thinking, false},
	} {
		raw, err = patchProviderScalar(raw, base, project, index, field.key, field.old, field.next, field.required)
		if err != nil {
			return "", err
		}
	}
	if !reflect.DeepEqual(old.AgentTypes, next.AgentTypes) {
		if len(next.AgentTypes) == 0 {
			raw, err = patchDocument(raw, providerDelete(base, project, index, "agent_types"))
		} else {
			raw, err = patchDocument(raw, providerEdit(base, project, index, "agent_types", next.AgentTypes))
		}
		if err != nil {
			return "", err
		}
	}
	for _, item := range []struct {
		name      string
		old, next map[string]string
	}{
		{"env", old.Env, next.Env},
		{"endpoints", old.Endpoints, next.Endpoints},
		{"agent_models", old.AgentModels, next.AgentModels},
	} {
		raw, err = patchProviderMap(raw, base+"."+item.name, project, index, item.old, item.next)
		if err != nil {
			return "", err
		}
	}
	if !reflect.DeepEqual(old.Codex, next.Codex) {
		if next.Codex == nil {
			raw, err = replaceProviderSubtree(raw, base+".codex", project, index, "")
		} else {
			var oldCodex CodexProviderConfig
			if old.Codex != nil {
				oldCodex = *old.Codex
			}
			raw, err = patchProviderScalar(raw, base+".codex", project, index, "env_key", oldCodex.EnvKey, next.Codex.EnvKey, false)
			if err == nil {
				raw, err = patchProviderScalar(raw, base+".codex", project, index, "wire_api", oldCodex.WireAPI, next.Codex.WireAPI, false)
			}
			if err == nil {
				raw, err = patchProviderMap(raw, base+".codex.http_headers", project, index, oldCodex.HTTPHeaders, next.Codex.HTTPHeaders)
			}
		}
		if err != nil {
			return "", err
		}
	}
	if !reflect.DeepEqual(old.Models, next.Models) || !reflect.DeepEqual(old.AgentModelLists, next.AgentModelLists) {
		if !reflect.DeepEqual(old.Models, next.Models) {
			raw, err = patchProviderModels(raw, base+".models", base+".models", project, index, old.Models, next.Models)
			if err != nil {
				return "", err
			}
		}
		if !reflect.DeepEqual(old.AgentModelLists, next.AgentModelLists) {
			if len(next.AgentModelLists) == 0 {
				raw, err = replaceProviderSubtree(raw, base+".agent_model_lists", project, index, "")
				if err != nil {
					return "", err
				}
			} else {
				keys := make([]string, 0, len(old.AgentModelLists)+len(next.AgentModelLists))
				seen := make(map[string]bool)
				for key := range old.AgentModelLists {
					seen[key] = true
					keys = append(keys, key)
				}
				for key := range next.AgentModelLists {
					if !seen[key] {
						keys = append(keys, key)
					}
				}
				sort.Strings(keys)
				for _, key := range keys {
					path := base + ".agent_model_lists." + key
					raw, err = patchProviderModels(raw, path, base+".agent_model_lists."+documentKey(key), project, index, old.AgentModelLists[key], next.AgentModelLists[key])
					if err != nil {
						return "", err
					}
				}
			}
		}
	}
	return raw, nil
}
