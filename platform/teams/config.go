package teams

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// Default webhook bind values follow the Bot Framework convention so the Bot
// Framework Emulator and Azure Bot "messaging endpoint" defaults line up.
const (
	defaultWebhookPort          = "3978"
	defaultWebhookPath          = "/api/messages"
	defaultCardUpdateIntervalMS = 1500     // card edit throttle; Teams rate-limits edits ~1/s
	defaultMaxAttachmentBytes   = 20 << 20 // 20 MiB cap per inbound attachment download
)

// config holds the resolved Teams platform settings parsed from the config.toml
// `[[projects.platforms]]` options table.
type config struct {
	// appID is the Bot/Azure AD application (client) ID. The inbound JWT `aud`
	// claim must equal it; the outbound client-credentials grant uses it.
	appID string
	// appPassword is the application client secret used for the outbound token.
	appPassword string
	// tenantID is the AAD tenant that owns the Azure Bot resource. It is required:
	// the connector is single-tenant only. Azure deprecated multi-tenant bot
	// creation after 2025-07-31, so every new bot is single-tenant; requiring the
	// tenant also scopes who can reach the bot to that one organization.
	tenantID string

	webhookPort string
	webhookPath string

	allowFrom    string
	sessionScope string // "thread" (default) | "channel" | "user"

	// serviceURLAllowlist restricts the outbound Bot Connector serviceURL to these
	// hosts (defense-in-depth against a forged serviceURL exfiltrating the bot
	// token). Empty (default) = allow any JWT-validated serviceURL, matching the
	// Bot Framework / M365 Agents SDK, which trust the authenticated inbound host.
	serviceURLAllowlist []string

	// cardLoadingText is the label on the placeholder card shown while the agent
	// thinks. Empty (default) renders a label-less card — no built-in default.
	cardLoadingText string

	cardUpdateIntervalMS int // card edit throttle (ms); smaller = finer chunks (floor ~1s)

	// maxAttachmentBytes caps each inbound 1:1 attachment download. A payload
	// larger than this is skipped (with a user notice) rather than truncated or
	// buffered unbounded. Defaults to defaultMaxAttachmentBytes.
	maxAttachmentBytes int64

	// dataDir and project are injected by cc-connect (cc_data_dir / cc_project)
	// and locate the on-disk engagement store. Empty => engagement stays
	// in-memory only (e.g. tests / standalone construction).
	dataDir string
	project string
}

// parseConfig extracts and validates the Teams config from the platform opts map.
func parseConfig(opts map[string]any) (config, error) {
	c := config{
		appID:               strings.TrimSpace(stringOpt(opts, "app_id")),
		appPassword:         stringOpt(opts, "app_password"),
		tenantID:            strings.TrimSpace(stringOpt(opts, "tenant_id")),
		webhookPort:         strings.TrimSpace(stringOpt(opts, "webhook_port")),
		webhookPath:         strings.TrimSpace(stringOpt(opts, "webhook_path")),
		allowFrom:           stringOpt(opts, "allow_from"),
		sessionScope:        normalizeSessionScope(opts["session_scope"]),
		serviceURLAllowlist: splitCSV(stringOpt(opts, "service_url_allowlist")),
		cardLoadingText:     strings.TrimSpace(stringOpt(opts, "card_loading_text")),
		dataDir:             stringOpt(opts, "cc_data_dir"),
		project:             stringOpt(opts, "cc_project"),
	}
	c.cardUpdateIntervalMS = intOpt(opts, "card_update_interval_ms", defaultCardUpdateIntervalMS)
	if c.cardUpdateIntervalMS <= 0 {
		c.cardUpdateIntervalMS = defaultCardUpdateIntervalMS
	}
	c.maxAttachmentBytes = int64(intOpt(opts, "max_attachment_bytes", defaultMaxAttachmentBytes))
	if c.maxAttachmentBytes <= 0 {
		c.maxAttachmentBytes = defaultMaxAttachmentBytes
	}

	if c.appID == "" {
		return config{}, fmt.Errorf("teams: app_id is required")
	}
	if c.appPassword == "" {
		return config{}, fmt.Errorf("teams: app_password is required")
	}
	if c.tenantID == "" {
		return config{}, fmt.Errorf("teams: tenant_id is required (the connector is single-tenant; multi-tenant bots are deprecated by Azure)")
	}

	if c.webhookPort == "" {
		c.webhookPort = defaultWebhookPort
	}
	if c.webhookPath == "" {
		c.webhookPath = defaultWebhookPath
	}
	if !strings.HasPrefix(c.webhookPath, "/") {
		c.webhookPath = "/" + c.webhookPath
	}
	return c, nil
}

func stringOpt(opts map[string]any, key string) string {
	s, _ := opts[key].(string)
	return s
}

// splitCSV splits a comma-separated option into trimmed, non-empty entries,
// returning nil when the option is empty.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// serviceURLAllowed reports whether the activity serviceURL's host is permitted.
// An empty allowlist permits any host (the serviceURL is already bound to a
// JWT-validated request); otherwise the parsed host must match an entry
// case-insensitively. An unparseable URL with a non-empty allowlist is rejected.
func serviceURLAllowed(rawURL string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	for _, h := range allowlist {
		if strings.EqualFold(u.Host, h) {
			return true
		}
	}
	return false
}

// intOpt reads an integer option, returning def when absent or the wrong type.
// TOML/JSON decoders surface numbers as int64 or float64, so accept both.
func intOpt(opts map[string]any, key string, def int) int {
	switch n := opts[key].(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return def
	}
}

// normalizeSessionScope resolves session_scope to one of "thread" | "channel" |
// "user", defaulting to "thread" (Teams is thread-centric) and warning on unknown
// values.
func normalizeSessionScope(raw any) string {
	s, _ := raw.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "thread":
		return "thread"
	case "channel":
		return "channel"
	case "user":
		return "user"
	default:
		slog.Warn("teams: unknown session_scope, falling back to thread", "session_scope", s)
		return "thread"
	}
}
