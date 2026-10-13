package config

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// AddBotIdentity appends a complete project in one atomic configuration write.
// It retains the parent's agent/workspace settings, but never inherits platform
// credentials, application-scoped administrator IDs or background jobs.
func AddBotIdentity(parent, name, platform, appID, appSecret, claimHash string) error {
	return addBotIdentity(parent, name, platform, appID, appSecret, claimHash, "")
}

// AddOwnedBotIdentity saves a registered bot and its scanner's identity in one
// write. The owner must come from this application's registration result.
func AddOwnedBotIdentity(parent, name, platform, appID, appSecret, owner string) error {
	if owner == "" || strings.ContainsAny(owner, ",* \t\r\n") {
		return fmt.Errorf("invalid registered owner")
	}
	return addBotIdentity(parent, name, platform, appID, appSecret, "", owner)
}

func addBotIdentity(parent, name, platform, appID, appSecret, claimHash, owner string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`).MatchString(name) {
		return fmt.Errorf("invalid bot name")
	}
	hash, err := hex.DecodeString(claimHash)
	if (owner == "" && (err != nil || len(hash) != 32)) || appID == "" || appSecret == "" || (platform != "feishu" && platform != "lark") {
		return fmt.Errorf("invalid identity configuration")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	var source *ProjectConfig
	for _, entry := range cfg.RuntimeProjects() {
		p := entry
		if p.Name == name {
			return fmt.Errorf("bot name already exists")
		}
		for _, pc := range p.Platforms {
			if (pc.Type == "feishu" || pc.Type == "lark") && pc.Options["app_id"] == appID {
				return fmt.Errorf("app identity already configured")
			}
		}
		if p.Name == parent {
			source = &p
		}
	}
	if source == nil {
		return fmt.Errorf("parent project not found")
	}
	proj := *source
	proj.SessionDataDir = ""
	proj.Name, proj.AdminFrom, proj.Users = name, owner, nil
	if proj.ID != "" {
		proj.ID = name
	}
	proj.BotOwnerClaimHash, proj.BotOwnerID = claimHash, owner
	proj.Heartbeat = HeartbeatConfig{}
	proj.Observe = nil
	proj.Platforms = []PlatformConfig{{Type: platform, Options: map[string]any{
		"app_id": appID, "app_secret": appSecret,
		"group_reply_all": false, "group_only": false,
		"share_session_in_channel": true, "thread_isolation": false,
		"single_card_reply": true, "separate_final_card": true,
	}}}
	var block bytes.Buffer
	if proj.ID != "" {
		if err := toml.NewEncoder(&block).Encode(struct {
			Bots []BotConfig `toml:"bots"`
		}{[]BotConfig{BotConfig(proj)}}); err != nil {
			return fmt.Errorf("encode bot: %w", err)
		}
		return writeRawConfig(strings.TrimRight(string(data), "\n") + "\n\n" + block.String())
	}
	if err := toml.NewEncoder(&block).Encode(struct {
		Projects []ProjectConfig `toml:"projects"`
	}{[]ProjectConfig{proj}}); err != nil {
		return fmt.Errorf("encode identity: %w", err)
	}
	return writeRawConfig(strings.TrimRight(string(data), "\n") + "\n\n" + block.String())
}

// ClaimBotIdentity stores the new application's own user ID, never an ID
// copied from the parent application. The hash check makes claims one-time.
func ClaimBotIdentity(name, expectedHash, userID string) error {
	configMu.Lock()
	defer configMu.Unlock()
	if expectedHash == "" || userID == "" {
		return fmt.Errorf("invalid owner claim")
	}
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	identities, table := cfg.Projects, "projects"
	for _, b := range cfg.Bots {
		if b.ID != name {
			continue
		}
		identities, table = nil, "bots"
		for _, b := range cfg.Bots {
			p := ProjectConfig(b)
			p.Name = b.ID
			identities = append(identities, p)
		}
		break
	}
	for i, p := range identities {
		if p.Name != name {
			continue
		}
		if p.BotOwnerClaimHash != expectedHash || p.BotOwnerID != "" {
			return fmt.Errorf("identity is already claimed or changed")
		}
		lines, trailing := splitConfigLines(string(data))
		// Rebuild spans after each insertion since missing fields shift offsets.
		for _, kv := range [][2]string{{"bot_owner_claim_hash", ""}, {"bot_owner_id", userID}, {"admin_from", userID}} {
			spanLines := lines
			if table == "bots" {
				spanLines = append([]string(nil), lines...)
				for j, line := range spanLines {
					if strings.HasPrefix(strings.TrimSpace(line), "[") {
						line = strings.ReplaceAll(line, "[projects", "[legacy_projects")
						spanLines[j] = strings.ReplaceAll(line, "[bots", "[projects")
					}
				}
			}
			spans := buildRawProjectSpans(spanLines)
			span := spans[i]
			end := span.end
			for n := span.start + 1; n <= span.end; n++ {
				if isAnyTableHeader(lines[n]) {
					end = n - 1
					break
				}
			}
			lines = upsertTomlStringKey(lines, span.start+1, end, kv[0], kv[1])
		}
		return writeRawConfig(joinConfigLines(lines, trailing))
	}
	return fmt.Errorf("identity not found")
}
