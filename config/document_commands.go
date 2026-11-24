package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

func commandAliasBlock(raw, table string, index int) (documentTable, error) {
	_, tables, err := scanConfigDocument(raw)
	if err != nil {
		return documentTable{}, err
	}
	for _, item := range tables {
		if item.name != table {
			continue
		}
		if index == 0 {
			return item, nil
		}
		index--
	}
	return documentTable{}, fmt.Errorf("cannot locate %s block", table)
}

func removeCommandAliasBlock(raw, table string, index int) (string, error) {
	item, err := commandAliasBlock(raw, table, index)
	if err != nil {
		return "", err
	}
	return raw[:item.start] + raw[item.end:], nil
}

func patchAliasCommand(raw string, index int, command string) (string, error) {
	item, err := commandAliasBlock(raw, "aliases", index)
	if err != nil {
		return "", err
	}
	entries, _, err := scanConfigDocument(raw)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.table == "aliases" && entry.key == "command" && entry.lineStart >= item.start && entry.lineEnd <= item.end {
			value := *documentValue(command)
			if sameTOMLValue(raw[entry.valueAt:entry.valueEnd], value) {
				return raw, nil
			}
			return raw[:entry.valueAt] + value + raw[entry.valueEnd:], nil
		}
	}
	return "", fmt.Errorf("missing command in alias %d", index)
}

func appendCommandAliasBlock(raw, block string) string {
	if documentNewline(raw) == "\r\n" {
		block = strings.ReplaceAll(block, "\n", "\r\n")
	}
	return insertDocument(raw, len(raw), documentNewline(raw)+block)
}

func serializeCommandBlock(command CommandConfig) (string, error) {
	var out strings.Builder
	err := toml.NewEncoder(&out).Encode(struct {
		Commands []CommandConfig `toml:"commands"`
	}{Commands: []CommandConfig{command}})
	return out.String(), err
}

func serializeAliasBlock(alias AliasConfig) (string, error) {
	var out strings.Builder
	err := toml.NewEncoder(&out).Encode(struct {
		Aliases []AliasConfig `toml:"aliases"`
	}{Aliases: []AliasConfig{alias}})
	return out.String(), err
}
