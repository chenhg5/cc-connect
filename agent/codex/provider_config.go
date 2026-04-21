package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ensureCodexProviderConfig writes or updates a [model_providers.<name>] section
// in $CODEX_HOME/config.toml so that Codex CLI can use the provider's wire_api
// and http_headers settings.
func ensureCodexProviderConfig(codexHome, name, baseURL, wireAPI string, headers map[string]string) error {
	if name == "" {
		return nil
	}
	home, err := resolveCodexHomeForConfig(codexHome)
	if err != nil {
		return fmt.Errorf("codex: resolve codex home: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("codex: mkdir codex home: %w", err)
	}

	cfgPath := filepath.Join(home, "config.toml")
	raw, _ := os.ReadFile(cfgPath)
	content := string(raw)

	section := buildProviderSection(name, baseURL, wireAPI, headers)
	updated := upsertProviderSection(content, name, section)

	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("codex: write config.toml: %w", err)
	}
	slog.Debug("codex: wrote provider config", "provider", name, "path", cfgPath)
	return nil
}

// ensureCodexAuth writes $CODEX_HOME/auth.json with the provider's API key,
// matching cc-switch's approach: {"OPENAI_API_KEY": "...", "auth_mode": "api_key"}.
// This is the standard way to authenticate Codex CLI with third-party providers.
func ensureCodexAuth(codexHome, apiKey string) error {
	if apiKey == "" {
		return nil
	}
	home, err := resolveCodexHomeForConfig(codexHome)
	if err != nil {
		return fmt.Errorf("codex: resolve codex home: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("codex: mkdir codex home: %w", err)
	}

	authPath := filepath.Join(home, "auth.json")
	payload := map[string]any{
		"OPENAI_API_KEY": apiKey,
		"auth_mode":      "apikey",
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: marshal auth.json: %w", err)
	}
	if err := os.WriteFile(authPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("codex: write auth.json: %w", err)
	}
	slog.Debug("codex: wrote auth.json", "path", authPath)
	return nil
}

func resolveCodexHomeForConfig(explicit string) (string, error) {
	if h := strings.TrimSpace(explicit); h != "" {
		return h, nil
	}
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".codex"), nil
}

func buildProviderSection(name, baseURL, wireAPI string, headers map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[model_providers.%s]\n", name)
	fmt.Fprintf(&sb, "name = %q\n", name)
	if baseURL != "" {
		fmt.Fprintf(&sb, "base_url = %q\n", baseURL)
	}
	fmt.Fprintf(&sb, "env_key = %q\n", "OPENAI_API_KEY")
	if wireAPI != "" {
		fmt.Fprintf(&sb, "wire_api = %q\n", wireAPI)
	}
	if len(headers) > 0 {
		fmt.Fprintf(&sb, "\n[model_providers.%s.http_headers]\n", name)
		for k, v := range headers {
			fmt.Fprintf(&sb, "%q = %q\n", k, v)
		}
	}
	return sb.String()
}

// upsertProviderSection replaces an existing [model_providers.<name>] section
// or appends a new one at the end of the config content.
func upsertProviderSection(content, name, newSection string) string {
	sectionHeader := fmt.Sprintf("[model_providers.%s]", name)
	subSectionPrefix := fmt.Sprintf("[model_providers.%s.", name)

	if !strings.Contains(content, sectionHeader) {
		trimmed := strings.TrimRight(content, "\n\t ")
		if trimmed == "" {
			return newSection
		}
		return trimmed + "\n\n" + newSection
	}

	idx := strings.Index(content, sectionHeader)

	after := content[idx+len(sectionHeader):]
	end := len(content)
	lines := strings.Split(after, "\n")
	offset := idx + len(sectionHeader)
	for _, line := range lines {
		offset += len(line) + 1
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '[' && !strings.HasPrefix(trimmed, subSectionPrefix) && trimmed != sectionHeader {
			end = offset - len(line) - 1
			break
		}
	}

	return strings.TrimRight(content[:idx], "\n") + "\n\n" + newSection + "\n" + content[end:]
}
