package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	ccconfig "github.com/timmyagentic/cc-connect-next/config"
)

func TestNormalizeMigrationSourceVersion(t *testing.T) {
	for _, version := range []string{
		"v1.4.1",
		"1.4.1",
		"v1.5.0-beta.1",
		"v1.5.0-beta.2",
		"v1.5.0-beta.3",
	} {
		got, err := normalizeMigrationSourceVersion(version)
		if err != nil {
			t.Fatalf("normalizeMigrationSourceVersion(%q): %v", version, err)
		}
		if !strings.HasPrefix(got, "v") {
			t.Fatalf("normalizeMigrationSourceVersion(%q) = %q, want canonical v prefix", version, got)
		}
	}

	if got, err := normalizeMigrationSourceVersion("auto"); err != nil || got != automaticMigrationSourceVersion {
		t.Fatalf("auto source version = %q, %v", got, err)
	}
	if _, err := normalizeMigrationSourceVersion("v1.5.0-beta.4"); err == nil {
		t.Fatal("unknown future source release was accepted")
	}
}

func TestPrepareLegacyMigration_KnownSourceReleaseMatrix(t *testing.T) {
	for _, version := range []string{
		"v1.4.1",
		"v1.5.0-beta.1",
		"v1.5.0-beta.2",
		"v1.5.0-beta.3",
	} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			configText := `[[projects]]
name = "known-release"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
`
			if version == "v1.5.0-beta.3" {
				configText = "[display]\nhide_agent_footer = true\n" + configText
			}
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), configText)

			plan, err := prepareLegacyMigration(migrationOptions{
				Source:        source,
				Target:        target,
				Home:          root,
				SourceVersion: version,
				DryRun:        true,
			})
			if err != nil {
				t.Fatalf("prepare %s migration: %v", version, err)
			}
			if plan.SourceVersion != version || plan.Report.SourceVersion != version {
				t.Fatalf("source version provenance = plan %q report %q, want %q", plan.SourceVersion, plan.Report.SourceVersion, version)
			}
		})
	}
}

func TestPrepareLegacyMigration_RejectsUnavailableConfiguredPlugin(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "future-project"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "future-platform"
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("unsupported configured plugin error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after compatibility failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_RejectsInvalidPluginOptionsBeforeWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "missing-feishu-credentials"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "only-an-app-id"
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "app_id and app_secret are required") {
		t.Fatalf("invalid plugin options error = %v, want Feishu credential refusal", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after plugin-option failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_AcceptsFeishuMentionMap(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "feishu-mention-map"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_test"
app_secret = "secret"
resolve_mentions = true
mention_map = { Reviewer-Bot = "ou_reviewer_bot" }
`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("mention_map migration preflight failed: %v", err)
	}
	if plan == nil {
		t.Fatal("mention_map migration produced no plan")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote target state: %v", statErr)
	}
}

func TestPrepareLegacyMigration_RejectsInactiveFeishuMentionMapBeforeWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "inactive-feishu-mention-map"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_test"
app_secret = "secret"
mention_map = { Reviewer-Bot = "ou_reviewer_bot" }
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "mention_map requires resolve_mentions = true") {
		t.Fatalf("inactive mention_map error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after mention_map compatibility failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_DoesNotAllowMentionMapOnOtherPlatforms(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "telegram-with-feishu-option"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "telegram"
[projects.platforms.options]
token = "test-token"
mention_map = { Reviewer-Bot = "ou_reviewer_bot" }
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported settings") {
		t.Fatalf("foreign mention_map error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after foreign mention_map failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_RejectsPluginNamesThatRuntimeRegistryWouldReject(t *testing.T) {
	tests := []struct {
		name       string
		configText string
	}{
		{
			name: "agent casing",
			configText: `[[projects]]
name = "agent-casing"
[projects.agent]
type = "Codex"
[[projects.platforms]]
type = "feishu"
`,
		},
		{
			name: "platform casing",
			configText: `[[projects]]
name = "platform-casing"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "Feishu"
`,
		},
		{
			name: "agent whitespace",
			configText: `[[projects]]
name = "agent-whitespace"
[projects.agent]
type = " codex "
[[projects.platforms]]
type = "feishu"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), tt.configText)

			_, err := prepareLegacyMigration(migrationOptions{
				Source:        source,
				Target:        target,
				Home:          root,
				SourceVersion: "v1.5.0-beta.3",
				DryRun:        true,
			})
			if err == nil || !strings.Contains(err.Error(), "does not provide") {
				t.Fatalf("registry-semantics error = %v, want unavailable-plugin refusal", err)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("target was written after registry-semantics failure: %v", statErr)
			}
		})
	}
}

func TestPrepareLegacyMigration_ResolvesEnvironmentBeforeSemanticAndRegistryValidation(t *testing.T) {
	t.Setenv("CC_NEXT_MIGRATION_DISPLAY_MODE", "compact")
	t.Setenv("CC_NEXT_MIGRATION_AGENT_TYPE", "codex")
	t.Setenv("CC_NEXT_MIGRATION_PLATFORM_TYPE", "feishu")

	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `[display]
mode = "${CC_NEXT_MIGRATION_DISPLAY_MODE}"
[[projects]]
name = "environment-backed"
[projects.agent]
type = "${CC_NEXT_MIGRATION_AGENT_TYPE}"
[[projects.platforms]]
type = "${CC_NEXT_MIGRATION_PLATFORM_TYPE}"
[projects.platforms.options]
app_id = "environment-app-id"
app_secret = "environment-app-secret"
`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("environment-backed config was rejected before migration: %v", err)
	}
	if plan == nil {
		t.Fatal("environment-backed config produced no migration plan")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote target state: %v", statErr)
	}
}

func TestPrepareLegacyMigration_UsesResolvedProjectNamesForCustomDataInventory(t *testing.T) {
	t.Setenv("CC_NEXT_MIGRATION_PROJECT_NAME", "environment-backed")

	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	customData := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(customData)+`"
[[projects]]
name = "${CC_NEXT_MIGRATION_PROJECT_NAME}"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "resolved-project-app-id"
app_secret = "resolved-project-app-secret"
`)
	writeMigrationFixture(t, filepath.Join(customData, "environment-backed.json"), `{"sessions":{}}`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("resolved project session inventory was rejected: %v", err)
	}
	if _, ok := plan.Main.Files["environment-backed.json"]; !ok {
		t.Fatalf("resolved project session file is missing from the migration plan: %v", plan.Main.Files)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote target state: %v", statErr)
	}
}

func TestPrepareLegacyMigration_RejectsSemanticallyInvalidConfigBeforeWrites(t *testing.T) {
	tests := []struct {
		name       string
		configText string
		wantError  string
	}{
		{
			name: "invalid display mode",
			configText: `[display]
mode = "verbose"
[[projects]]
name = "invalid-display"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
`,
			wantError: "display.mode",
		},
		{
			name: "missing agent type",
			configText: `[[projects]]
name = "missing-agent"
[[projects.platforms]]
type = "feishu"
`,
			wantError: "agent.type is required",
		},
		{
			name: "missing platform",
			configText: `[[projects]]
name = "missing-platform"
[projects.agent]
type = "codex"
`,
			wantError: "needs at least one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), tt.configText)

			_, err := prepareLegacyMigration(migrationOptions{
				Source:        source,
				Target:        target,
				Home:          root,
				SourceVersion: "v1.5.0-beta.3",
				DryRun:        true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("semantic validation error = %v, want %q", err, tt.wantError)
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatalf("target was written after semantic validation failure: %v", statErr)
			}
		})
	}
}

func TestPrepareLegacyMigration_RejectsUnsupportedConfigBehavior(t *testing.T) {
	for _, setting := range []string{
		"[[projects]]\nname = \"idle\"\nagent_session_idle_timeout_mins = 10\n[projects.agent]\ntype = \"codex\"\n",
	} {
		root := t.TempDir()
		source := filepath.Join(root, ".cc-connect")
		target := filepath.Join(root, ".cc-connect-next")
		writeMigrationFixture(t, filepath.Join(source, "config.toml"), setting)

		_, err := prepareLegacyMigration(migrationOptions{
			Source:        source,
			Target:        target,
			Home:          root,
			SourceVersion: "v1.5.0-beta.3",
			DryRun:        true,
		})
		if err == nil || !strings.Contains(err.Error(), "preserve bytes but not behavior") {
			t.Fatalf("unsupported source setting error = %v", err)
		}
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("target was written after schema failure: %v", statErr)
		}
	}
}

func TestPrepareLegacyMigration_RejectsUnimplementedPiRPCOptionBeforeWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "pi-project"
[projects.agent]
type = "pi"
[projects.agent.options]
rpc = true
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_fixture"
app_secret = "fixture-secret"
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), `option "rpc"`) || !strings.Contains(err.Error(), "preserve bytes but not behavior") {
		t.Fatalf("unimplemented pi rpc option error = %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after gated-option failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_AcceptsAgentEnvOptionTable(t *testing.T) {
	// The env table decodes into the dynamic agent options map. Its nested
	// leaves must not be mistaken for unsupported top-level settings.
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "env-project"
[projects.agent]
type = "codex"
[projects.agent.options.env]
CC_FIXTURE_KEY = "fixture-value"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_fixture"
app_secret = "fixture-secret"
`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "auto",
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("agent env option table must migrate, got error: %v", err)
	}
	if plan == nil || len(plan.Main.Files) == 0 {
		t.Fatal("expected a migration plan covering the config file")
	}
}

func TestPrepareLegacyMigration_AcceptsImplementedWeixinBurstOptions(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "wx-project"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "weixin"
[projects.platforms.options]
token = "fixture-token"
burst_limit = 2
burst_window_secs = 60
`)

	if _, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "v1.5.0-beta.3",
		DryRun:        true,
	}); err != nil {
		t.Fatalf("weixin burst options are implemented and must migrate, got error: %v", err)
	}
}

func TestPrepareLegacyMigration_RejectsEnvTableForAgentThatIgnoresIt(t *testing.T) {
	// devin never spawns a CLI with a caller-controlled environment, so an
	// env table on it would migrate as dead configuration.
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "devin-project"
[projects.agent]
type = "devin"
[projects.agent.options.env]
CC_FIXTURE_KEY = "fixture-value"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_fixture"
app_secret = "fixture-secret"
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "auto",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "projects.agent.options.env") {
		t.Fatalf("env table for a non-consuming agent must be rejected, got: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written after schema failure: %v", statErr)
	}
}

func TestPrepareLegacyMigration_AcceptsFeishuPeerBotsTable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "relay-project"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_fixture"
app_secret = "fixture-secret"
[projects.platforms.options.peer_bots]
helper = "ou_fixture"
`)

	if _, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "auto",
		DryRun:        true,
	}); err != nil {
		t.Fatalf("feishu peer_bots table must migrate, got error: %v", err)
	}
}

func TestPrepareLegacyMigration_DoesNotAllowPeerBotsOnOtherPlatforms(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "relay-project"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "telegram"
[projects.platforms.options]
token = "fixture-token"
[projects.platforms.options.peer_bots]
helper = "ou_fixture"
`)

	_, err := prepareLegacyMigration(migrationOptions{
		Source:        source,
		Target:        target,
		Home:          root,
		SourceVersion: "auto",
		DryRun:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "peer_bots") {
		t.Fatalf("peer_bots on a non-Feishu platform must be rejected, got: %v", err)
	}
}

func TestRunMigrateCommandReturnsFailureForMissingSource(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := runMigrateCommand([]string{
		"--source", filepath.Join(root, "missing"),
		"--target", filepath.Join(root, ".cc-connect-next"),
	}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("runMigrateCommand() code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "migrate: read source directory") {
		t.Fatalf("stderr = %q, want source error", stderr.String())
	}
}

func TestRunMigrateCommandHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := runMigrateCommand([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runMigrateCommand(--help) code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "Usage: cc-connect-next migrate") {
		t.Fatalf("help output = %q, want usage", stderr.String())
	}
}

func TestMigrateLegacyDataCopiesPersistentStateAndIsolatesRuntime(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(source)+`"
language = "zh"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
api_key = "keep-this-secret"
`)
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"session":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "projects", "demo.state.json"), `{"state":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "config", "minimax.json"), `{"token":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "run", "api.sock"), "volatile")
	writeMigrationFixture(t, filepath.Join(source, "logs", "cc-connect.log"), "volatile")
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"pid":1}`)

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if report.CopiedFiles != 4 {
		t.Fatalf("copied files = %d, want 4", report.CopiedFiles)
	}

	configBytes, err := os.ReadFile(filepath.Join(target, "config.toml"))
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	configText := string(configBytes)
	canonicalTarget, err := canonicalDestinationPath(target)
	if err != nil {
		t.Fatalf("canonical target: %v", err)
	}
	if !strings.Contains(configText, `data_dir = "`+filepath.ToSlash(canonicalTarget)+`"`) {
		t.Fatalf("migrated config does not use isolated target data_dir: %q", configText)
	}
	if !strings.Contains(configText, "keep-this-secret") {
		t.Fatalf("migrated config lost existing values: %q", configText)
	}
	if info, err := os.Stat(filepath.Join(target, "config.toml")); err != nil {
		t.Fatalf("stat migrated config: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("migrated config mode = %#o, want 0600", got)
	}

	for _, rel := range []string{"sessions/demo.json", "projects/demo.state.json", "config/minimax.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("persistent file %s was not copied: %v", rel, err)
		}
	}
	for _, rel := range []string{"run", "logs", "daemon.json"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("runtime path %s should not be migrated, err=%v", rel, err)
		}
	}
}

func TestMigrateLegacyDataRewritesQuotedTopLevelDataDir(t *testing.T) {
	for _, key := range []string{`"data_dir"`, `'data_dir'`} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), key+` = "`+filepath.ToSlash(source)+`" # preserve comment`+"\n")
			writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"session":"kept"}`)

			if _, err := migrateLegacyData(source, target, false, false); err != nil {
				t.Fatalf("migrateLegacyData() error = %v", err)
			}
			configBytes, err := os.ReadFile(filepath.Join(target, "config.toml"))
			if err != nil {
				t.Fatalf("read migrated config: %v", err)
			}
			canonicalTarget, err := canonicalExistingDirectory(target)
			if err != nil {
				t.Fatalf("canonical target: %v", err)
			}
			configText := string(configBytes)
			want := key + ` = "` + filepath.ToSlash(canonicalTarget) + `" # preserve comment`
			if !strings.Contains(configText, want) {
				t.Fatalf("quoted data_dir was not rewritten in place: got %q, want %q", configText, want)
			}
			if got := strings.Count(configText, key); got != 1 {
				t.Fatalf("migrated config contains %d quoted data_dir keys, want 1: %q", got, configText)
			}
			if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != `{"session":"kept"}` {
				t.Fatalf("quoted data_dir migration lost persistent state: content=%q err=%v", got, err)
			}
		})
	}
}

func TestMigrateLegacyDataRewritesAllTOMLStringForms(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{name: "multiline basic one line", config: `data_dir = """$SOURCE""" # preserve comment` + "\n"},
		{name: "multiline literal one line", config: `data_dir = '''$SOURCE''' # preserve comment` + "\n"},
		{name: "multiline basic across lines", config: "data_dir = \"\"\"\n$SOURCE\"\"\" # preserve comment\n"},
		{name: "multiline literal across lines", config: "data_dir = '''\n$SOURCE''' # preserve comment\n"},
		{name: "escaped quoted key", config: `"data\u005fdir" = """$SOURCE"""` + "\n"},
		{
			name: "ignore assignment text inside preceding multiline value",
			config: "provider_presets_url = \"\"\"\n" +
				`data_dir = "/must-not-match"` + "\n\"\"\"\n" +
				`data_dir = """$SOURCE"""` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			configText := strings.ReplaceAll(tt.config, "$SOURCE", filepath.ToSlash(source))
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), configText)
			writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"session":"kept"}`)

			if _, err := migrateLegacyData(source, target, false, false); err != nil {
				t.Fatalf("migrateLegacyData() error = %v", err)
			}
			configBytes, err := os.ReadFile(filepath.Join(target, "config.toml"))
			if err != nil {
				t.Fatalf("read migrated config: %v", err)
			}
			var migrated legacyMigrationConfig
			if _, err := toml.Decode(string(configBytes), &migrated); err != nil {
				t.Fatalf("decode migrated config: %v\n%s", err, configBytes)
			}
			canonicalTarget, err := canonicalExistingDirectory(target)
			if err != nil {
				t.Fatalf("canonical target: %v", err)
			}
			if got, want := migrated.DataDir, filepath.ToSlash(canonicalTarget); got != want {
				t.Fatalf("migrated data_dir = %q, want %q\n%s", got, want, configBytes)
			}
			if strings.Contains(tt.config, "must-not-match") && !strings.Contains(string(configBytes), `data_dir = "/must-not-match"`) {
				t.Fatalf("rewriter modified multiline string content: %s", configBytes)
			}
			if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != `{"session":"kept"}` {
				t.Fatalf("multiline data_dir migration lost persistent state: content=%q err=%v", got, err)
			}
		})
	}
}

func TestMigrateLegacyDataRefusesExistingTargetWithoutForce(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(target, "keep.txt"), "do not overwrite")

	if _, err := migrateLegacyData(source, target, false, false); err == nil {
		t.Fatal("migrateLegacyData() error = nil, want existing-target refusal")
	}
	got, err := os.ReadFile(filepath.Join(target, "keep.txt"))
	if err != nil || string(got) != "do not overwrite" {
		t.Fatalf("existing target was modified: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{}`)

	report, err := migrateLegacyData(source, target, false, true)
	if err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	if !report.DryRun || report.CopiedFiles != 2 {
		t.Fatalf("dry-run report = %+v, want two planned files", report)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run created target, err=%v", err)
	}
}

func TestMigrateLegacyDataSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	outside := filepath.Join(root, "outside-secret")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, outside, "must-not-copy")
	if err := os.Symlink(outside, filepath.Join(source, "linked-secret")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if report.SkippedSymlinks != 1 {
		t.Fatalf("skipped symlinks = %d, want 1", report.SkippedSymlinks)
	}
	if _, err := os.Stat(filepath.Join(target, "linked-secret")); !os.IsNotExist(err) {
		t.Fatalf("symlink target should not be copied, err=%v", err)
	}
}

func TestMigrateLegacyDataRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	outside := filepath.Join(root, "existing-next-data")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(outside, "keep.txt"), "must-remain-untouched")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatalf("symlink target: %v", err)
	}

	if _, err := migrateLegacyData(source, target, true, false); err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("migrateLegacyData() error = %v, want symlink-target refusal", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(got) != "must-remain-untouched" {
		t.Fatalf("symlink destination was modified: content=%q err=%v", got, err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target symlink was replaced: info=%v err=%v", info, err)
	}
}

func TestPrepareLegacyMigrationRefusesMissingTargetBelowSymlinkIntoSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	link := filepath.Join(root, "source-link")
	target := filepath.Join(link, "missing-parent", ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	if err := os.Symlink(source, link); err != nil {
		t.Fatalf("symlink source ancestor: %v", err)
	}

	_, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   root,
	})
	if err == nil || !strings.Contains(err.Error(), "separate directories") {
		t.Fatalf("prepareLegacyMigration() error = %v, want source/target overlap refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(source, "missing-parent")); !os.IsNotExist(statErr) {
		t.Fatalf("preflight created a target parent inside the source, err=%v", statErr)
	}
}

func TestPrepareLegacyMigrationRefusesCaseOnlyTargetAlias(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".CC-CONNECT")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	originalConfig, err := os.ReadFile(filepath.Join(source, "config.toml"))
	if err != nil {
		t.Fatalf("read original source config: %v", err)
	}

	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil || !os.SameFile(sourceInfo, targetInfo) {
		t.Skip("test filesystem is case-sensitive")
	}

	_, err = prepareLegacyMigration(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		Force:              true,
		DryRun:             true,
		IncludeProjectData: true,
	})
	if err == nil || !strings.Contains(err.Error(), "separate directories") {
		t.Fatalf("prepareLegacyMigration() error = %v, want case-only alias refusal", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(source, "config.toml")); readErr != nil || !bytes.Equal(got, originalConfig) {
		t.Fatalf("source config changed during alias preflight: content=%q err=%v", got, readErr)
	}
}

func TestMigrationPathsOverlapTreatsCaseOnlyMissingSiblingsAsUnsafe(t *testing.T) {
	root := t.TempDir()
	overlaps, err := migrationPathsOverlap(filepath.Join(root, "future-state"), filepath.Join(root, "FUTURE-STATE"))
	if err != nil {
		t.Fatalf("migrationPathsOverlap() error = %v", err)
	}
	if !overlaps {
		t.Fatal("case-only missing siblings below the same directory were treated as separate")
	}
}

func TestMigrationPathsOverlapAllowsDistinctExistingSiblings(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "alpha")
	b := filepath.Join(root, "beta")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	if err := os.Mkdir(b, 0o700); err != nil {
		t.Fatalf("mkdir beta: %v", err)
	}
	overlaps, err := migrationPathsOverlap(a, b)
	if err != nil {
		t.Fatalf("migrationPathsOverlap() error = %v", err)
	}
	if overlaps {
		t.Fatal("distinct existing siblings were treated as overlapping")
	}
}

func TestMigrateLegacyDataIncludesCustomDataDirAndProjectLocalData(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	customData := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	workDir := filepath.Join(root, "project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(customData)+`"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(workDir)+`"
`)
	writeMigrationFixture(t, filepath.Join(source, "backups", "config.toml.bak"), "backup")
	writeMigrationFixture(t, filepath.Join(customData, "sessions", "demo.json"), `{"session":"custom"}`)
	writeMigrationFixture(t, filepath.Join(customData, "crons", "jobs.json"), `[{"id":"cron"}]`)
	writeMigrationFixture(t, filepath.Join(customData, "workspace_bindings.json"), `{}`)
	writeMigrationFixture(t, filepath.Join(workDir, ".cc-connect", "images", "input.png"), "image-bytes")
	writeMigrationFixture(t, filepath.Join(workDir, ".cc-connect", "attachments", "prompt.txt"), "attachment")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	canonicalData, err := canonicalExistingDirectory(customData)
	if err != nil {
		t.Fatalf("canonical data dir: %v", err)
	}
	if got, want := report.SourceDataDir, canonicalData; got != want {
		t.Fatalf("source data dir = %q, want %q", got, want)
	}
	if report.ProjectDirectories != 1 {
		t.Fatalf("project directories = %d, want 1", report.ProjectDirectories)
	}

	for _, rel := range []string{
		"sessions/demo.json",
		"crons/jobs.json",
		"workspace_bindings.json",
	} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("persistent file %s was not copied: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "backups", "config.toml.bak")); !os.IsNotExist(err) {
		t.Fatalf("separate config-root backup was migrated: %v", err)
	}
	for _, rel := range []string{"images/input.png", "attachments/prompt.txt"} {
		migrated := filepath.Join(workDir, ".cc-connect-next", filepath.FromSlash(rel))
		if _, err := os.Stat(migrated); err != nil {
			t.Fatalf("project-local file %s was not copied: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, ".cc-connect", "images", "input.png")); err != nil {
		t.Fatalf("official project-local data was modified: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(target, migrationManifestFilename))
	if err != nil {
		t.Fatalf("read migration manifest: %v", err)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse migration manifest: %v", err)
	}
	if manifest.Version != 2 || manifest.SourceVersion != automaticMigrationSourceVersion {
		t.Fatalf("manifest compatibility metadata = version %d source %q, want 2 and %q", manifest.Version, manifest.SourceVersion, automaticMigrationSourceVersion)
	}
	if manifest.SourceDataDir != canonicalData || len(manifest.Files) != 6 {
		t.Fatalf("manifest does not inventory the complete migration: %+v", manifest)
	}
	for _, file := range manifest.Files {
		if file.SHA256 == "" || file.Size < 0 {
			t.Fatalf("manifest file is missing verification metadata: %+v", file)
		}
	}
}

func TestPrepareLegacyMigrationDoesNotTreatGlobalStateAsProjectDataWithCustomTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, "custom-next-target")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(source)+`"

[[projects]]
name = "home"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(root)+`"
`)
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"session":"kept"}`)
	writeMigrationFixture(t, filepath.Join(source, "run", "api.sock"), "runtime-must-stay-excluded")

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		DryRun:             true,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("prepareLegacyMigration() error = %v", err)
	}
	if len(plan.Projects) != 0 || plan.Report.ProjectDirectories != 0 {
		t.Fatalf("global source was duplicated as project-local data: projects=%d report=%+v", len(plan.Projects), plan.Report)
	}
	if _, ok := plan.Main.Files[filepath.ToSlash(filepath.Join("run", "api.sock"))]; ok {
		t.Fatal("runtime socket entered the main migration inventory")
	}
	if got, want := plan.Report.CopiedFiles, 2; got != want {
		t.Fatalf("copied files = %d, want %d unique persistent files", got, want)
	}
}

func TestPrepareLegacyMigrationRefusesProjectTargetInsideOfficialSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	dataDir := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	project := filepath.Join(source, "projects", "demo")
	projectTarget := filepath.Join(project, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(dataDir)+`"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(project)+`"
`)
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"session":"kept"}`)
	writeMigrationFixture(t, filepath.Join(project, ".cc-connect", "attachments", "prompt.txt"), "project-data")

	_, err := prepareLegacyMigration(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		DryRun:             true,
		IncludeProjectData: true,
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe overlapping project-local migration path") {
		t.Fatalf("prepareLegacyMigration() error = %v, want official-source overlap refusal", err)
	}
	if _, statErr := os.Stat(projectTarget); !os.IsNotExist(statErr) {
		t.Fatalf("overlap preflight created project target inside official source, err=%v", statErr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("overlap preflight created main target, err=%v", statErr)
	}
}

func TestResolveLegacyConfigPathMatchesOfficialBracedEnvSyntax(t *testing.T) {
	base := t.TempDir()
	expandedRoot := filepath.Join(base, "expanded")
	t.Setenv("CC_CONNECT_MIGRATION_TEST_ROOT", expandedRoot)

	braced, err := resolveLegacyConfigPath("${CC_CONNECT_MIGRATION_TEST_ROOT}/state", base, base)
	if err != nil {
		t.Fatalf("resolve braced placeholder: %v", err)
	}
	if got, want := braced, filepath.Join(expandedRoot, "state"); got != want {
		t.Fatalf("braced placeholder = %q, want %q", got, want)
	}

	bare, err := resolveLegacyConfigPath("$CC_CONNECT_MIGRATION_TEST_ROOT/state", base, base)
	if err != nil {
		t.Fatalf("resolve bare variable: %v", err)
	}
	if got, want := bare, filepath.Join(base, "$CC_CONNECT_MIGRATION_TEST_ROOT", "state"); got != want {
		t.Fatalf("bare variable = %q, want literal official-config path %q", got, want)
	}
}

func TestResolveLegacyConfigPathRejectsUnsetBracedEnv(t *testing.T) {
	base := t.TempDir()
	const envName = "CC_CONNECT_MIGRATION_TEST_UNSET_PATH"
	oldValue, hadValue := os.LookupEnv(envName)
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("unset test environment variable: %v", err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(envName, oldValue)
		} else {
			_ = os.Unsetenv(envName)
		}
	})

	_, err := resolveLegacyConfigPath("${"+envName+"}/state", base, base)
	if err == nil || !strings.Contains(err.Error(), envName) || !strings.Contains(err.Error(), "is not set") {
		t.Fatalf("resolve unset placeholder error = %v, want fail-closed variable error", err)
	}
}

func TestPrepareLegacyMigrationRejectsUnsetDataDirEnvBeforeTargetWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	const envName = "CC_CONNECT_MIGRATION_TEST_UNSET_DATA_DIR"
	oldValue, hadValue := os.LookupEnv(envName)
	if err := os.Unsetenv(envName); err != nil {
		t.Fatalf("unset test environment variable: %v", err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(envName, oldValue)
		} else {
			_ = os.Unsetenv(envName)
		}
	})
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "${`+envName+`}/state"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{}`)

	_, err := prepareLegacyMigration(migrationOptions{Source: source, Target: target, Home: root, DryRun: true})
	if err == nil || !strings.Contains(err.Error(), envName) || !strings.Contains(err.Error(), "is not set") {
		t.Fatalf("prepare migration error = %v, want unset data_dir variable refusal", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("unset data_dir variable created a target before refusal: %v", statErr)
	}
}

func TestPrepareLegacyMigrationSkipsInaccessibleOptionalProjectRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-mode test is Unix-specific")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	lockedParent := filepath.Join(root, "locked")
	privateProject := filepath.Join(lockedParent, "private-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "private"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(privateProject)+`"
`)
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{}`)
	writeMigrationFixture(t, filepath.Join(privateProject, ".cc-connect", "images", "private.png"), "private")
	if err := os.Chmod(lockedParent, 0); err != nil {
		t.Fatalf("lock optional project parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedParent, 0o700) })
	if _, err := os.Stat(privateProject); !errors.Is(err, os.ErrPermission) {
		t.Skipf("filesystem does not enforce the permission fixture: %v", err)
	}

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		DryRun:             true,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("inaccessible optional project blocked global migration: %v", err)
	}
	if len(plan.Projects) != 0 || len(plan.Report.SkippedProjects) != 1 {
		t.Fatalf("optional project skip was not reported: projects=%d report=%+v", len(plan.Projects), plan.Report)
	}
	manifest := buildMigrationManifest(plan)
	if len(manifest.SkippedProjects) != 1 || manifest.SkippedProjects[0].Source != filepath.Clean(privateProject) {
		t.Fatalf("optional project skip is missing from the manifest: %+v", manifest.SkippedProjects)
	}
	if got, want := plan.Report.CopiedFiles, 2; got != want {
		t.Fatalf("global persistent inventory = %d, want %d", got, want)
	}
}

func TestMigrateLegacyDataSkipsMalformedOptionalProjectDiscoveryMetadata(t *testing.T) {
	tests := []struct {
		name   string
		rel    string
		reason string
	}{
		{name: "project state", rel: filepath.Join("projects", "broken.state.json"), reason: "malformed project state"},
		{name: "workspace bindings", rel: "workspace_bindings.json", reason: "malformed workspace bindings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
			writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{}`)
			writeMigrationFixture(t, filepath.Join(source, tt.rel), `{`)

			report, err := migrateLegacyDataWithOptions(migrationOptions{
				Source:             source,
				Target:             target,
				Home:               root,
				IncludeProjectData: true,
			})
			if err != nil {
				t.Fatalf("malformed optional metadata blocked global migration: %v", err)
			}
			if got, want := report.CopiedFiles, 3; got != want {
				t.Fatalf("global persistent files = %d, want %d", got, want)
			}
			canonicalSource, err := canonicalExistingDirectory(source)
			if err != nil {
				t.Fatalf("canonical source: %v", err)
			}
			wantSkippedSource := filepath.Join(canonicalSource, tt.rel)
			if len(report.SkippedProjects) != 1 || report.SkippedProjects[0].Source != wantSkippedSource || report.SkippedProjects[0].Reason != tt.reason {
				t.Fatalf("optional discovery skip = %+v, want %s (%s)", report.SkippedProjects, wantSkippedSource, tt.reason)
			}
			if got, err := os.ReadFile(filepath.Join(target, tt.rel)); err != nil || string(got) != `{` {
				t.Fatalf("malformed metadata was not copied verbatim: content=%q err=%v", got, err)
			}

			manifestBytes, err := os.ReadFile(filepath.Join(target, migrationManifestFilename))
			if err != nil {
				t.Fatalf("read migration manifest: %v", err)
			}
			if !bytes.Contains(manifestBytes, []byte(`"skipped_project_discovery"`)) {
				t.Fatalf("manifest JSON does not expose skipped discovery records: %s", manifestBytes)
			}
			var manifest migrationManifest
			if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
				t.Fatalf("parse migration manifest: %v", err)
			}
			if len(manifest.SkippedProjects) != 1 || manifest.SkippedProjects[0] != report.SkippedProjects[0] {
				t.Fatalf("manifest did not record optional discovery skip: %+v", manifest.SkippedProjects)
			}
		})
	}
}

func TestPrepareLegacyMigrationExcludesNestedDataDirFromConfigInventory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "state"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"work_dir":"`+filepath.ToSlash(source)+`"}`)
	writeMigrationFixture(t, filepath.Join(source, "state", "sessions", "demo.json"), `{"session":"nested"}`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   root,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("prepare nested data_dir migration: %v", err)
	}
	if _, duplicated := plan.Main.Files[filepath.Join("state", "sessions", "demo.json")]; duplicated {
		t.Fatalf("nested data_dir was duplicated under its config-root path: %+v", plan.Main.Files)
	}
	if _, copied := plan.Main.Files[filepath.Join("sessions", "demo.json")]; !copied {
		t.Fatalf("effective nested data_dir was not mapped to the target root: %+v", plan.Main.Files)
	}
	if got, want := plan.Report.CopiedFiles, 2; got != want {
		t.Fatalf("copied files = %d, want %d unique files", got, want)
	}
}

func TestPrepareLegacyMigrationRejectsAncestorDataDir(t *testing.T) {
	dataDir := t.TempDir()
	source := filepath.Join(dataDir, ".cc-connect")
	target := filepath.Join(t.TempDir(), ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(dataDir)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "projects", "demo.state.json"), `{"project":"config-root"}`)
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"session":"data-dir"}`)
	writeMigrationFixture(t, filepath.Join(dataDir, ".ssh", "id_private"), "must-not-be-inventoried")

	_, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   dataDir,
		DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), "source data_dir must be dedicated") {
		t.Fatalf("prepare ancestor data_dir error = %v, want dedicated-directory refusal", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe ancestor data_dir created target before refusal: %v", statErr)
	}
}

func TestPrepareLegacyMigrationNeverInventoriesUnrelatedFilesInABroadCustomDataDir(t *testing.T) {
	// A custom data_dir that also holds a service home's private files must
	// migrate the CC Connect state and nothing else. The unrelated tree is
	// skipped and named in the report rather than copied.
	root := t.TempDir()
	source := filepath.Join(root, "etc", "cc-connect")
	dataDir := filepath.Join(root, "home", "service")
	target := filepath.Join(root, "var", "lib", "cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(dataDir)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"sessions":{}}`)
	writeMigrationFixture(t, filepath.Join(dataDir, ".ssh", "id_private"), "must-not-be-inventoried")

	plan, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   filepath.Join(root, "root-home"),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("prepareLegacyMigration() error = %v", err)
	}
	for rel := range plan.Main.Files {
		if strings.Contains(filepath.ToSlash(rel), ".ssh") {
			t.Fatalf("unrelated private file was inventoried: %q", rel)
		}
	}
	if _, ok := plan.Main.Files[filepath.Join("sessions", "demo.json")]; !ok {
		t.Fatalf("CC Connect state was not inventoried: %+v", plan.Main.Files)
	}
	if got := strings.Join(plan.Report.SkippedDataEntries, ","); !strings.Contains(got, ".ssh") {
		t.Fatalf("skipped entries = %q, want the unrelated tree reported", got)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("dry run created a target: %v", statErr)
	}
}

func TestMigrateLegacyDataUsesOfficialDefaultDataDirWithCustomConfigRoot(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "service")
	source := filepath.Join(root, "etc", "cc-connect")
	dataDir := filepath.Join(home, ".cc-connect")
	target := filepath.Join(root, "var", "lib", "cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"sessions":{}}`)
	writeMigrationFixture(t, filepath.Join(dataDir, "backups", "legacy.bin"), "dedicated-default-data")

	report, err := migrateLegacyDataWithOptions(migrationOptions{
		Source: source,
		Target: target,
		Home:   home,
	})
	if err != nil {
		t.Fatalf("migrate custom config root with default data_dir: %v", err)
	}
	canonicalData, err := canonicalExistingDirectory(dataDir)
	if err != nil {
		t.Fatalf("canonical default data_dir: %v", err)
	}
	if report.SourceDataDir != canonicalData {
		t.Fatalf("SourceDataDir = %q, want official default %q", report.SourceDataDir, canonicalData)
	}
	for _, rel := range []string{"sessions/demo.json", "backups/legacy.bin"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("default data_dir file %s was not migrated: %v", rel, err)
		}
	}
}

func TestMigrateLegacyDataRestrictsSeparateConfigRootToConfigFile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "service")
	source := filepath.Join(root, "workspace", "project")
	dataDir := filepath.Join(home, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, ".env"), "PRIVATE_TOKEN=must-not-copy")
	writeMigrationFixture(t, filepath.Join(source, ".git", "config"), "repository metadata")
	writeMigrationFixture(t, filepath.Join(source, "backups", "config.toml.bak"), "user backup")
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"sessions":{}}`)

	if _, err := migrateLegacyDataWithOptions(migrationOptions{Source: source, Target: target, Home: home}); err != nil {
		t.Fatalf("migrate separate working-directory config: %v", err)
	}
	for _, rel := range []string{".env", ".git/config", "backups/config.toml.bak"} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("unrelated config-root path %s was migrated: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "sessions", "demo.json")); err != nil {
		t.Fatalf("effective default data_dir was not migrated: %v", err)
	}
}

func TestMigrateLegacyDataUsesDefaultDataDirContainingCustomConfigRoot(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "service")
	dataDir := filepath.Join(home, ".cc-connect")
	source := filepath.Join(dataDir, "configs", "bot")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "backups", "config.toml.bak"), "config-backup")
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"sessions":{}}`)

	if _, err := migrateLegacyDataWithOptions(migrationOptions{Source: source, Target: target, Home: home}); err != nil {
		t.Fatalf("migrate nested custom config root with default data_dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "sessions", "demo.json")); err != nil {
		t.Fatalf("nested-default migration lost effective session state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "backups", "config.toml.bak")); !os.IsNotExist(err) {
		t.Fatalf("separate config-root backup was migrated at the target root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "configs", "bot", "backups", "config.toml.bak")); !os.IsNotExist(err) {
		t.Fatalf("nested config root was copied through default data_dir inventory: %v", err)
	}
}

func TestKnownLegacyDataDirPathAllowsOnlyOwnedPersistentPaths(t *testing.T) {
	cfg := &ccconfig.Config{Projects: []ccconfig.ProjectConfig{{Name: "demo"}, {Name: " bot "}, {Name: "team/bot"}}}
	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{rel: "sessions", isDir: true, want: true},
		{rel: "sessions/demo.json", want: true},
		{rel: "projects/demo.state.json", want: true},
		{rel: "projects/demo-0123456789abcdef.opencode-models.json", want: true},
		{rel: "crons/jobs.json", want: true},
		{rel: "timers/jobs.json", want: true},
		{rel: "config/minimax.json", want: true},
		{rel: "agent-prompts/cc-connect-system.md", want: true},
		{rel: "weixin/demo/account/context_tokens.json", want: true},
		{rel: "workspace_bindings.json", want: true},
		{rel: "matrix-crypto-DEVICE.db-wal", want: true},
		{rel: "matrix-cross-signing-DEVICE.json", want: true},
		{rel: "demo.json", want: true},
		{rel: "demo_0123abcd.sessions.json", want: true},
		{rel: " bot _0123abcd.json", want: true},
		{rel: "team", isDir: true, want: true},
		{rel: "team/bot_0123abcd.json", want: true},
		{rel: "team/private.json", want: false},
		{rel: ".ssh", isDir: true, want: false},
		{rel: "projects/repository", isDir: true, want: false},
		{rel: "sessions/private.pem", want: false},
		{rel: "config/browser-profile.json", want: false},
		{rel: "credentials.json", want: false},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.rel, "/", "_"), func(t *testing.T) {
			if got := isKnownLegacyDataDirPath(filepath.FromSlash(tt.rel), tt.isDir, cfg); got != tt.want {
				t.Fatalf("isKnownLegacyDataDirPath(%q, isDir=%v) = %v, want %v", tt.rel, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestMigrateLegacyDataResolvesRelativePathsFromOfficialRuntimeWorkDir(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	runtimeWorkDir := filepath.Join(root, "official-runtime")
	projectDir := filepath.Join(runtimeWorkDir, "project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "state"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "project"
`)
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"work_dir":"`+filepath.ToSlash(runtimeWorkDir)+`"}`)
	writeMigrationFixture(t, filepath.Join(runtimeWorkDir, "state", "sessions", "demo.json"), "relative-state")
	writeMigrationFixture(t, filepath.Join(projectDir, ".cc-connect", "images", "relative.png"), "relative-project-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	canonicalRuntime, err := canonicalExistingDirectory(runtimeWorkDir)
	if err != nil {
		t.Fatalf("canonical runtime work dir: %v", err)
	}
	if report.SourceWorkDir != canonicalRuntime {
		t.Fatalf("source runtime work_dir = %q, want %q", report.SourceWorkDir, canonicalRuntime)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "relative-state" {
		t.Fatalf("relative data_dir state was not migrated: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(projectDir, ".cc-connect-next", "images", "relative.png")); err != nil || string(got) != "relative-project-data" {
		t.Fatalf("relative project work_dir data was not migrated: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime daemon metadata should not be copied, err=%v", err)
	}
}

func TestMigrateLegacyDataFindsDefaultDaemonMetadataForSeparateConfig(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "service")
	source := filepath.Join(root, "workspace", "project-config")
	target := filepath.Join(root, ".cc-connect-next")
	runtimeWorkDir := filepath.Join(root, "official-runtime")
	projectDir := filepath.Join(runtimeWorkDir, "project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "state"

[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "project"
`)
	// A same-named project file is not official daemon metadata and must not
	// override the metadata stored in the product's default data directory.
	writeMigrationFixture(t, filepath.Join(source, "daemon.json"), `{"work_dir":"`+filepath.ToSlash(source)+`"}`)
	writeMigrationFixture(t, filepath.Join(home, ".cc-connect", "daemon.json"), `{"work_dir":"`+filepath.ToSlash(runtimeWorkDir)+`"}`)
	writeMigrationFixture(t, filepath.Join(runtimeWorkDir, "state", "sessions", "demo.json"), "relative-state")
	writeMigrationFixture(t, filepath.Join(projectDir, ".cc-connect", "images", "relative.png"), "relative-project-data")

	report, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               home,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("migrate separate config with default daemon metadata: %v", err)
	}
	canonicalRuntime, err := canonicalExistingDirectory(runtimeWorkDir)
	if err != nil {
		t.Fatalf("canonical runtime work dir: %v", err)
	}
	if report.SourceWorkDir != canonicalRuntime {
		t.Fatalf("source runtime work_dir = %q, want default metadata value %q", report.SourceWorkDir, canonicalRuntime)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "relative-state" {
		t.Fatalf("relative data_dir state was not migrated: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(projectDir, ".cc-connect-next", "images", "relative.png")); err != nil || string(got) != "relative-project-data" {
		t.Fatalf("relative project data was not migrated: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime daemon metadata should not be copied, err=%v", err)
	}
}

func TestMigrateLegacyDataRejectsUnavailableDefaultDaemonWorkDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "service")
	source := filepath.Join(root, "workspace", "project-config")
	target := filepath.Join(root, ".cc-connect-next")
	missingRuntimeWorkDir := filepath.Join(root, "missing-official-runtime")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "state"`)
	writeMigrationFixture(t, filepath.Join(home, ".cc-connect", "daemon.json"), `{"work_dir":"`+filepath.ToSlash(missingRuntimeWorkDir)+`"}`)

	_, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               home,
		IncludeProjectData: true,
	})
	if err == nil || !strings.Contains(err.Error(), "read official daemon work_dir") || !strings.Contains(err.Error(), "--runtime-work-dir") {
		t.Fatalf("migrate with unavailable recorded daemon work_dir error = %v, want fail-closed override guidance", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target was written despite unavailable daemon work_dir: %v", statErr)
	}

	override := filepath.Join(root, "verified-runtime")
	writeMigrationFixture(t, filepath.Join(override, "state", "sessions", "demo.json"), "verified-state")
	report, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               home,
		RuntimeWorkDir:     override,
		DryRun:             true,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("migrate with explicit runtime work_dir override: %v", err)
	}
	canonicalOverride, err := canonicalExistingDirectory(override)
	if err != nil {
		t.Fatalf("canonical runtime override: %v", err)
	}
	if report.SourceWorkDir != canonicalOverride {
		t.Fatalf("source runtime work_dir = %q, want explicit override %q", report.SourceWorkDir, canonicalOverride)
	}
}

func TestMigrateLegacyDataDiscoversProjectDataFromStateAndBindings(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	stateProject := filepath.Join(root, "state-project")
	bindingProject := filepath.Join(root, "binding-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "projects", "demo.state.json"), `{
  "work_dir_override": "`+filepath.ToSlash(stateProject)+`"
}`)
	writeMigrationFixture(t, filepath.Join(source, "workspace_bindings.json"), `{
  "project:demo": {
    "feishu:chat": {
      "workspace": "`+filepath.ToSlash(bindingProject)+`"
    }
  }
}`)
	writeMigrationFixture(t, filepath.Join(stateProject, ".cc-connect", "attachments", "state.txt"), "state-data")
	writeMigrationFixture(t, filepath.Join(bindingProject, ".cc-connect", "images", "binding.png"), "binding-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, want := report.ProjectDirectories, 2; got != want {
		t.Fatalf("project directories = %d, want %d", got, want)
	}
	for _, path := range []string{
		filepath.Join(stateProject, ".cc-connect-next", "attachments", "state.txt"),
		filepath.Join(bindingProject, ".cc-connect-next", "images", "binding.png"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("discovered project-local data was not migrated at %s: %v", path, err)
		}
	}
}

func TestMigrateLegacyDataDiscoversProjectDataUnderMultiWorkspaceBase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	baseDir := filepath.Join(root, "workspaces")
	workspace := filepath.Join(baseDir, "team-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "multi"
mode = "multi-workspace"
base_dir = "`+filepath.ToSlash(baseDir)+`"
`)
	writeMigrationFixture(t, filepath.Join(workspace, ".cc-connect", "attachments", "context.txt"), "workspace-data")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, want := report.ProjectDirectories, 1; got != want {
		t.Fatalf("project directories = %d, want %d", got, want)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, ".cc-connect-next", "attachments", "context.txt")); err != nil || string(got) != "workspace-data" {
		t.Fatalf("multi-workspace project data was not migrated: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataPreservesProjectDataAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX project access modes are not available on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	project := filepath.Join(root, "project")
	projectSource := filepath.Join(project, ".cc-connect")
	projectSubdir := filepath.Join(projectSource, "attachments")
	projectEmptyDir := filepath.Join(projectSource, "images")
	projectFile := filepath.Join(projectSubdir, "context.txt")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "isolated"
run_as_user = "agent-user"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(project)+`"
`)
	writeMigrationFixture(t, projectFile, "project-data")
	if err := os.MkdirAll(projectEmptyDir, 0o710); err != nil {
		t.Fatalf("mkdir empty project directory: %v", err)
	}
	if err := os.Chmod(projectSource, 0o750); err != nil {
		t.Fatalf("chmod project source: %v", err)
	}
	if err := os.Chmod(projectSubdir, 0o750); err != nil {
		t.Fatalf("chmod project subdirectory: %v", err)
	}
	if err := os.Chmod(projectEmptyDir, 0o710); err != nil {
		t.Fatalf("chmod empty project directory: %v", err)
	}
	if err := os.Chmod(projectFile, 0o640); err != nil {
		t.Fatalf("chmod project file: %v", err)
	}

	if _, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		IncludeProjectData: true,
	}); err != nil {
		t.Fatalf("migrateLegacyDataWithOptions() error = %v", err)
	}

	projectTarget := filepath.Join(project, ".cc-connect-next")
	for _, check := range []struct {
		source string
		target string
		mode   os.FileMode
	}{
		{source: projectSource, target: projectTarget, mode: 0o750},
		{source: projectSubdir, target: filepath.Join(projectTarget, "attachments"), mode: 0o750},
		{source: projectEmptyDir, target: filepath.Join(projectTarget, "images"), mode: 0o710},
		{source: projectFile, target: filepath.Join(projectTarget, "attachments", "context.txt"), mode: 0o640},
	} {
		sourceInfo, err := os.Stat(check.source)
		if err != nil {
			t.Fatalf("stat source access fixture: %v", err)
		}
		targetInfo, err := os.Stat(check.target)
		if err != nil {
			t.Fatalf("stat migrated project data: %v", err)
		}
		if got := targetInfo.Mode().Perm(); got != check.mode {
			t.Fatalf("migrated mode for %s = %#o, want %#o", check.target, got, check.mode)
		}
		sourceUID, sourceGID, sourceHasOwner := migrationOwnership(sourceInfo)
		targetUID, targetGID, targetHasOwner := migrationOwnership(targetInfo)
		if sourceHasOwner != targetHasOwner || (sourceHasOwner && (sourceUID != targetUID || sourceGID != targetGID)) {
			t.Fatalf("migrated ownership for %s = %d:%d, want %d:%d", check.target, targetUID, targetGID, sourceUID, sourceGID)
		}
	}
}

func TestMigrateLegacyDataPreservesGlobalRunAsTraversalAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX global access modes are not available on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	promptDir := filepath.Join(source, "agent-prompts")
	promptFile := filepath.Join(promptDir, "cc-connect-system.md")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "isolated"
run_as_user = "agent-user"
[projects.agent]
type = "claudecode"
`)
	writeMigrationFixture(t, promptFile, "shared prompt")
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatalf("chmod global source: %v", err)
	}
	if err := os.Chmod(promptDir, 0o755); err != nil {
		t.Fatalf("chmod prompt directory: %v", err)
	}
	if err := os.Chmod(promptFile, 0o644); err != nil {
		t.Fatalf("chmod prompt file: %v", err)
	}

	// A forced merge must not restore a looser pre-existing config mode when
	// config.toml is regenerated with the migrated data_dir.
	writeMigrationFixture(t, filepath.Join(target, "config.toml"), "language = \"en\"\n")
	if err := os.Chmod(filepath.Join(target, "config.toml"), 0o644); err != nil {
		t.Fatalf("chmod existing target config: %v", err)
	}

	if _, err := migrateLegacyData(source, target, true, false); err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	for _, check := range []struct {
		source string
		target string
		mode   os.FileMode
	}{
		{source: source, target: target, mode: 0o755},
		{source: promptDir, target: filepath.Join(target, "agent-prompts"), mode: 0o755},
		{source: promptFile, target: filepath.Join(target, "agent-prompts", "cc-connect-system.md"), mode: 0o644},
		{target: filepath.Join(target, "config.toml"), mode: 0o600},
	} {
		info, err := os.Stat(check.target)
		if err != nil {
			t.Fatalf("stat migrated global path %s: %v", check.target, err)
		}
		if got := info.Mode().Perm(); got != check.mode {
			t.Fatalf("migrated mode for %s = %#o, want %#o", check.target, got, check.mode)
		}
		if check.source != "" {
			sourceInfo, err := os.Stat(check.source)
			if err != nil {
				t.Fatalf("stat global access source %s: %v", check.source, err)
			}
			sourceUID, sourceGID, sourceHasOwner := migrationOwnership(sourceInfo)
			targetUID, targetGID, targetHasOwner := migrationOwnership(info)
			if sourceHasOwner != targetHasOwner || (sourceHasOwner && (sourceUID != targetUID || sourceGID != targetGID)) {
				t.Fatalf("migrated ownership for %s = %d:%d, want %d:%d", check.target, targetUID, targetGID, sourceUID, sourceGID)
			}
		}
	}
}

func TestMigrateLegacyDataPreservesRunAsTraversalOnCreatedTargetParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX global access modes are not available on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	targetParent := filepath.Join(root, "new", "nested")
	target := filepath.Join(targetParent, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "isolated"
run_as_user = "agent-user"
[projects.agent]
type = "claudecode"
`)
	writeMigrationFixture(t, filepath.Join(source, "agent-prompts", "cc-connect-system.md"), "shared prompt")
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatalf("chmod global source: %v", err)
	}
	if err := os.Chmod(filepath.Join(source, "agent-prompts"), 0o755); err != nil {
		t.Fatalf("chmod prompt directory: %v", err)
	}
	if err := os.Chmod(filepath.Join(source, "agent-prompts", "cc-connect-system.md"), 0o644); err != nil {
		t.Fatalf("chmod prompt file: %v", err)
	}

	if _, err := migrateLegacyData(source, target, false, false); err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat global source: %v", err)
	}
	sourceUID, sourceGID, sourceHasOwner := migrationOwnership(sourceInfo)
	for _, path := range []string{filepath.Join(root, "new"), targetParent, target} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat created migration path %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Fatalf("created migration path mode for %s = %#o, want 0755", path, got)
		}
		targetUID, targetGID, targetHasOwner := migrationOwnership(info)
		if sourceHasOwner != targetHasOwner || (sourceHasOwner && (sourceUID != targetUID || sourceGID != targetGID)) {
			t.Fatalf("created migration path ownership for %s = %d:%d, want %d:%d", path, targetUID, targetGID, sourceUID, sourceGID)
		}
	}
}

func TestVerifyMigrationPlanUnchangedDetectsAddedPersistentFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "existing.json"), "existing")
	opts := migrationOptions{Source: source, Target: target, Home: root, IncludeProjectData: true}

	original, err := prepareLegacyMigration(opts)
	if err != nil {
		t.Fatalf("prepare original migration: %v", err)
	}
	destinations := append(append([]*migrationDestination{}, original.Projects...), original.Main)
	for _, destination := range destinations {
		if err := prepareMigrationStage(destination); err != nil {
			t.Fatalf("prepare migration stage: %v", err)
		}
	}
	t.Cleanup(func() { cleanupMigrationStages(destinations) })
	writeMigrationFixture(t, filepath.Join(source, "sessions", "created-while-running.json"), "new")

	fresh, err := prepareLegacyMigration(opts)
	if err != nil {
		t.Fatalf("prepare fresh migration inventory: %v", err)
	}
	if err := verifyMigrationPlanUnchanged(original, fresh); err == nil || !strings.Contains(err.Error(), "changed during migration") {
		t.Fatalf("verifyMigrationPlanUnchanged() error = %v, want added-file refusal", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("source drift activated a target, err=%v", err)
	}
}

func TestMigrateLegacyDataExpandsHomeInMultiWorkspaceBase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	workspace := filepath.Join(root, "workspaces", "team-project")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "multi"
mode = "multi-workspace"
base_dir = "~/workspaces"
`)
	writeMigrationFixture(t, filepath.Join(workspace, ".cc-connect", "attachments", "context.txt"), "workspace-data")

	report, err := migrateLegacyDataWithOptions(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("migrateLegacyDataWithOptions() error = %v", err)
	}
	if got, want := report.ProjectDirectories, 1; got != want {
		t.Fatalf("project directories = %d, want %d", got, want)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, ".cc-connect-next", "attachments", "context.txt")); err != nil || string(got) != "workspace-data" {
		t.Fatalf("home-relative multi-workspace project data was not migrated: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataIgnoresStaleStateBesideSeparateConfig(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	customData := filepath.Join(root, "official-state")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(customData)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "stale-config-root-session")
	writeMigrationFixture(t, filepath.Join(customData, "sessions", "demo.json"), "effective-data-dir-session")

	if _, err := migrateLegacyData(source, target, false, false); err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "effective-data-dir-session" {
		t.Fatalf("effective data_dir did not remain authoritative: content=%q err=%v", got, err)
	}
	archive := filepath.Join(target, "migration-archive", "config-root", "sessions", "demo.json")
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("stale state beside separate config was migrated: %v", err)
	}
}

func TestMigrateLegacyDataForceCreatesRecoverableBackup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "new-session")
	writeMigrationFixture(t, filepath.Join(target, "keep.txt"), "existing-target")
	writeMigrationFixture(t, filepath.Join(target, "sessions", "demo.json"), "old-session")

	report, err := migrateLegacyData(source, target, true, false)
	if err != nil {
		t.Fatalf("migrateLegacyData(force) error = %v", err)
	}
	if report.BackupDir == "" {
		t.Fatal("force migration did not report a backup directory")
	}
	canonicalTarget, err := canonicalDestinationPath(target)
	if err != nil {
		t.Fatalf("canonical target: %v", err)
	}
	if len(report.Backups) != 1 || report.Backups[0].Target != canonicalTarget || report.Backups[0].Backup != report.BackupDir {
		t.Fatalf("force migration backup report = %+v, want target and backup path", report.Backups)
	}
	if got, err := os.ReadFile(filepath.Join(target, "keep.txt")); err != nil || string(got) != "existing-target" {
		t.Fatalf("non-conflicting target file was not preserved: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "new-session" {
		t.Fatalf("legacy source did not replace matching target state: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(report.BackupDir, "sessions", "demo.json")); err != nil || string(got) != "old-session" {
		t.Fatalf("backup does not preserve the pre-migration target: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataPreservesExistingEmptyTargetBackup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("create empty target: %v", err)
	}

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("migrateLegacyData() error = %v", err)
	}
	if report.BackupDir == "" || len(report.Backups) != 1 || report.Backups[0].Backup != report.BackupDir {
		t.Fatalf("empty existing target backup report = %+v, backup_dir=%q", report.Backups, report.BackupDir)
	}
	info, err := os.Stat(report.BackupDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("empty pre-migration target backup was not retained: info=%v err=%v", info, err)
	}
	entries, err := os.ReadDir(report.BackupDir)
	if err != nil {
		t.Fatalf("read empty target backup: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty target backup unexpectedly contains entries: %v", entries)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(target, migrationManifestFilename))
	if err != nil {
		t.Fatalf("read migration manifest: %v", err)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse migration manifest: %v", err)
	}
	if len(manifest.Backups) != 1 || manifest.Backups[0].Backup != report.BackupDir {
		t.Fatalf("manifest did not record empty target backup: %+v", manifest.Backups)
	}
}

func TestMigrateLegacyDataRejectsTargetChangesBeforePromotion(t *testing.T) {
	tests := []struct {
		name          string
		force         bool
		prepareTarget func(*testing.T, string)
		mutateTarget  func(*testing.T, string)
		assertTarget  func(*testing.T, string)
	}{
		{
			name:  "force target receives a newer session write",
			force: true,
			prepareTarget: func(t *testing.T, target string) {
				writeMigrationFixture(t, filepath.Join(target, "sessions", "demo.json"), "old-target-session")
			},
			mutateTarget: func(t *testing.T, target string) {
				writeMigrationFixture(t, filepath.Join(target, "sessions", "demo.json"), "concurrent-target-session")
				writeMigrationFixture(t, filepath.Join(target, "sessions", "created-concurrently.json"), "concurrent")
			},
			assertTarget: func(t *testing.T, target string) {
				if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "concurrent-target-session" {
					t.Fatalf("concurrent target state was not preserved: content=%q err=%v", got, err)
				}
				if got, err := os.ReadFile(filepath.Join(target, "sessions", "created-concurrently.json")); err != nil || string(got) != "concurrent" {
					t.Fatalf("concurrently created target state was not preserved: content=%q err=%v", got, err)
				}
			},
		},
		{
			name:  "previously absent target appears",
			force: false,
			prepareTarget: func(*testing.T, string) {
			},
			mutateTarget: func(t *testing.T, target string) {
				writeMigrationFixture(t, filepath.Join(target, "claimed-by-another-process.txt"), "do-not-overwrite")
			},
			assertTarget: func(t *testing.T, target string) {
				if got, err := os.ReadFile(filepath.Join(target, "claimed-by-another-process.txt")); err != nil || string(got) != "do-not-overwrite" {
					t.Fatalf("new target was overwritten: content=%q err=%v", got, err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			target := filepath.Join(root, ".cc-connect-next")
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
			writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "legacy-session")
			tt.prepareTarget(t, target)

			_, err := migrateLegacyDataWithHooks(migrationOptions{
				Source:             source,
				Target:             target,
				Home:               root,
				Force:              tt.force,
				IncludeProjectData: true,
			}, migrationHooks{BeforePromotion: func() { tt.mutateTarget(t, target) }})
			if err == nil || !strings.Contains(err.Error(), "target changed during migration") {
				t.Fatalf("migrateLegacyDataWithHooks() error = %v, want target-change refusal", err)
			}
			tt.assertTarget(t, target)
			if _, err := os.Stat(filepath.Join(target, "config.toml")); !os.IsNotExist(err) {
				t.Fatalf("staged migration was activated despite target drift, err=%v", err)
			}
			stages, err := filepath.Glob(filepath.Join(root, ".cc-connect-next.migrate-*"))
			if err != nil {
				t.Fatalf("glob migration stages: %v", err)
			}
			if len(stages) != 0 {
				t.Fatalf("target drift left staging directories behind: %v", stages)
			}
			backups, err := filepath.Glob(target + ".pre-migration-*")
			if err != nil {
				t.Fatalf("glob migration backups: %v", err)
			}
			if len(backups) != 0 {
				t.Fatalf("target drift moved the live target into a backup: %v", backups)
			}
		})
	}
}

func TestPrepareMigrationStageRejectsEmptyTargetThatBecameNonEmpty(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("create empty target: %v", err)
	}

	plan, err := prepareLegacyMigration(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		IncludeProjectData: true,
	})
	if err != nil {
		t.Fatalf("prepare migration: %v", err)
	}
	writeMigrationFixture(t, filepath.Join(target, "claimed-concurrently.txt"), "do-not-merge-without-force")

	if err := prepareMigrationStage(plan.Main); err == nil || !strings.Contains(err.Error(), "target changed during migration") {
		t.Fatalf("prepareMigrationStage() error = %v, want target-change refusal", err)
	}
	cleanupMigrationStages([]*migrationDestination{plan.Main})
	if got, err := os.ReadFile(filepath.Join(target, "claimed-concurrently.txt")); err != nil || string(got) != "do-not-merge-without-force" {
		t.Fatalf("concurrently claimed target was modified: content=%q err=%v", got, err)
	}
}

func TestMigrateLegacyDataRestoresTargetChangedAtRenameBoundary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), "legacy-session")
	writeMigrationFixture(t, filepath.Join(target, "sessions", "demo.json"), "old-target-session")

	_, err := migrateLegacyDataWithHooks(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		Force:              true,
		IncludeProjectData: true,
	}, migrationHooks{AfterTargetRename: func(_ string, backup string) {
		// Simulate an already-open target writer completing at the rename
		// boundary. On Unix that file descriptor now points into the backup.
		writeMigrationFixture(t, filepath.Join(backup, "sessions", "demo.json"), "rename-boundary-session")
		writeMigrationFixture(t, filepath.Join(backup, "sessions", "created-at-boundary.json"), "boundary")
	}})
	if err == nil || !strings.Contains(err.Error(), "target changed during migration") {
		t.Fatalf("migrateLegacyDataWithHooks() error = %v, want rename-boundary refusal", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "demo.json")); err != nil || string(got) != "rename-boundary-session" {
		t.Fatalf("rename-boundary target state was not restored: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "sessions", "created-at-boundary.json")); err != nil || string(got) != "boundary" {
		t.Fatalf("rename-boundary target addition was not restored: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(target, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("staged migration was activated despite rename-boundary drift, err=%v", err)
	}
	stages, err := filepath.Glob(filepath.Join(root, ".cc-connect-next.migrate-*"))
	if err != nil {
		t.Fatalf("glob migration stages: %v", err)
	}
	if len(stages) != 0 {
		t.Fatalf("rename-boundary drift left staging directories behind: %v", stages)
	}
	backups, err := filepath.Glob(target + ".pre-migration-*")
	if err != nil {
		t.Fatalf("glob migration backups: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("rename-boundary drift did not restore the target in place: %v", backups)
	}
}

func TestMigrateLegacyDataRollbackPreservesWritesToEarlierPromotedTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	projectA := filepath.Join(root, "a-project")
	projectB := filepath.Join(root, "b-project")
	projectATarget := filepath.Join(projectA, ".cc-connect-next")
	projectBTarget := filepath.Join(projectB, ".cc-connect-next")
	canonicalProjectATarget, err := canonicalDestinationPath(projectATarget)
	if err != nil {
		t.Fatalf("canonicalize project A target: %v", err)
	}
	canonicalProjectBTarget, err := canonicalDestinationPath(projectBTarget)
	if err != nil {
		t.Fatalf("canonicalize project B target: %v", err)
	}
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "a"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(projectA)+`"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "project-a-app-id"
app_secret = "project-a-app-secret"

[[projects]]
name = "b"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(projectB)+`"
[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "project-b-app-id"
app_secret = "project-b-app-secret"
`)
	writeMigrationFixture(t, filepath.Join(projectA, ".cc-connect", "attachments", "legacy-a.txt"), "legacy-a")
	writeMigrationFixture(t, filepath.Join(projectB, ".cc-connect", "attachments", "legacy-b.txt"), "legacy-b")
	writeMigrationFixture(t, filepath.Join(projectATarget, "original-a.txt"), "pre-migration-a")
	writeMigrationFixture(t, filepath.Join(projectBTarget, "original-b.txt"), "pre-migration-b")

	_, err = migrateLegacyDataWithHooks(migrationOptions{
		Source:             source,
		Target:             target,
		Home:               root,
		Force:              true,
		IncludeProjectData: true,
	}, migrationHooks{AfterPromotion: func(promotedTarget string) {
		if promotedTarget != canonicalProjectATarget {
			return
		}
		writeMigrationFixture(t, filepath.Join(canonicalProjectATarget, "sessions", "written-after-promotion.json"), "fresh-a")
		// Make the next destination fail its final target revalidation so the
		// already-promoted A destination must be rolled back.
		writeMigrationFixture(t, filepath.Join(canonicalProjectBTarget, "sessions", "written-before-promotion.json"), "fresh-b")
	}})
	if err == nil || !strings.Contains(err.Error(), "rollback preserved promoted targets for recovery") {
		t.Fatalf("migrateLegacyDataWithHooks() error = %v, want preserved-recovery report", err)
	}

	if got, err := os.ReadFile(filepath.Join(projectATarget, "original-a.txt")); err != nil || string(got) != "pre-migration-a" {
		t.Fatalf("project A pre-migration target was not restored: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(projectATarget, "sessions", "written-after-promotion.json")); !os.IsNotExist(err) {
		t.Fatalf("fresh project A write remained mixed into restored target, err=%v", err)
	}
	recoveries, err := filepath.Glob(filepath.Join(projectA, ".cc-connect-next.failed-migration-*", "preserved"))
	if err != nil {
		t.Fatalf("glob project A recovery: %v", err)
	}
	if len(recoveries) != 1 {
		t.Fatalf("project A recovery paths = %v, want one preserved tree", recoveries)
	}
	if got, err := os.ReadFile(filepath.Join(recoveries[0], "sessions", "written-after-promotion.json")); err != nil || string(got) != "fresh-a" {
		t.Fatalf("fresh project A write was not preserved: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(recoveries[0], "attachments", "legacy-a.txt")); err != nil || string(got) != "legacy-a" {
		t.Fatalf("promoted project A migration was not preserved: content=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(projectBTarget, "sessions", "written-before-promotion.json")); err != nil || string(got) != "fresh-b" {
		t.Fatalf("failing project B target write was not preserved: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("global target was activated after project rollback, err=%v", err)
	}
}

func TestPrepareLegacyMigrationRejectsOverlappingDestinations(t *testing.T) {
	tests := []struct {
		name       string
		targetPath func(string) string
	}{
		{name: "same target", targetPath: func(project string) string { return filepath.Join(project, ".cc-connect-next") }},
		{name: "nested target", targetPath: func(project string) string { return filepath.Join(project, ".cc-connect-next", "main") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, ".cc-connect")
			project := filepath.Join(root, "project")
			target := tt.targetPath(project)
			writeMigrationFixture(t, filepath.Join(source, "config.toml"), `[[projects]]
name = "demo"
[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "`+filepath.ToSlash(project)+`"
`)
			writeMigrationFixture(t, filepath.Join(project, ".cc-connect", "images", "input.png"), "image")

			_, err := prepareLegacyMigration(migrationOptions{
				Source:             source,
				Target:             target,
				Home:               root,
				Force:              true,
				DryRun:             true,
				IncludeProjectData: true,
			})
			if err == nil || !strings.Contains(err.Error(), "migration destinations overlap") {
				t.Fatalf("prepareLegacyMigration() error = %v, want destination-overlap refusal", err)
			}
			if _, statErr := os.Stat(filepath.Join(project, ".cc-connect-next")); !os.IsNotExist(statErr) {
				t.Fatalf("overlap preflight created a destination, err=%v", statErr)
			}
		})
	}
}

func TestMigrateLegacyDataTreatsMissingConfiguredDataDirAsEmpty(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	missingData := filepath.Join(root, "missing-state")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(missingData)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(source, "backups", "config.toml.bak"), "backup")

	report, err := migrateLegacyData(source, target, false, false)
	if err != nil {
		t.Fatalf("missing lazy data_dir blocked config migration: %v", err)
	}
	if got, want := report.CopiedFiles, 1; got != want {
		t.Fatalf("copied files = %d, want only the rewritten config", got)
	}
	canonicalMissingData, err := canonicalDestinationPath(missingData)
	if err != nil {
		t.Fatalf("canonical missing data_dir: %v", err)
	}
	if report.SourceDataDir != canonicalMissingData {
		t.Fatalf("reported data_dir = %q, want %q", report.SourceDataDir, canonicalMissingData)
	}
	if _, err := os.Stat(missingData); !os.IsNotExist(err) {
		t.Fatalf("migration created the absent legacy data_dir, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "backups", "config.toml.bak")); !os.IsNotExist(err) {
		t.Fatalf("unrelated config-root backup was migrated: %v", err)
	}
	configBytes, err := os.ReadFile(filepath.Join(target, "config.toml"))
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	canonicalTarget, err := canonicalExistingDirectory(target)
	if err != nil {
		t.Fatalf("canonical target: %v", err)
	}
	if !strings.Contains(string(configBytes), `data_dir = "`+filepath.ToSlash(canonicalTarget)+`"`) {
		t.Fatalf("migrated config did not rewrite missing data_dir to isolated target: %s", configBytes)
	}
}

func writeMigrationFixture(t *testing.T, path, content string) {
	t.Helper()
	// Most migration tests exercise inventory, isolation, and rollback rather
	// than startup validation. Keep their config fixtures runnable so those
	// tests reach the boundary they intend to cover. Semantic-rejection tests
	// use writeRawMigrationFixture explicitly.
	if filepath.Base(path) == "config.toml" {
		projectCount := strings.Count(content, "[[projects]]")
		switch projectCount {
		case 0:
			content = strings.TrimRight(content, "\n") + `

[[projects]]
name = "migration-fixture"
[projects.agent]
type = "codex"
[[projects.platforms]]
type = "feishu"
`
		case 1:
			if !strings.Contains(content, "[projects.agent]") {
				content = strings.TrimRight(content, "\n") + `
[projects.agent]
type = "codex"
`
			}
			if !strings.Contains(content, "[[projects.platforms]]") {
				content = strings.TrimRight(content, "\n") + `
[[projects.platforms]]
type = "feishu"
`
			}
		}
		if strings.Count(content, `type = "feishu"`) == 1 && !strings.Contains(content, "[projects.platforms.options]") {
			content = strings.TrimRight(content, "\n") + `
[projects.platforms.options]
app_id = "migration-fixture-app-id"
app_secret = "migration-fixture-app-secret"
`
		}
	}
	writeRawMigrationFixture(t, path, content)
}

func writeRawMigrationFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestCollectLegacyDataDirSkipsUnrecognizedEntriesInsteadOfFailing(t *testing.T) {
	// Real official installations accumulate their own extras beside the
	// product state: onboarding QR images, hand-made backups, third-party
	// runtimes. Refusing the whole migration for one of them blocked users who
	// had nothing wrong with their setup, while skipping keeps the actual
	// protection — an unrecognized entry is still never inventoried.
	root := t.TempDir()
	source := filepath.Join(root, "etc", "cc-connect")
	dataDir := filepath.Join(root, "state", "cc-connect")
	target := filepath.Join(root, "var", "cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(dataDir)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(dataDir, "sessions", "demo.json"), `{"sessions":{}}`)
	writeMigrationFixture(t, filepath.Join(dataDir, "feishu-setup-qr.png"), "png")
	writeMigrationFixture(t, filepath.Join(dataDir, "backups", "config.toml.bak"), "old")

	plan, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   filepath.Join(root, "root-home"),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("prepareLegacyMigration() error = %v, want the extras to be skipped", err)
	}
	if _, ok := plan.Main.Files[filepath.Join("sessions", "demo.json")]; !ok {
		t.Fatalf("recognized state was not inventoried: %+v", plan.Main.Files)
	}
	for rel := range plan.Main.Files {
		if strings.HasPrefix(rel, "backups") || strings.Contains(rel, "feishu-setup-qr.png") {
			t.Fatalf("unrecognized entry %q was inventoried", rel)
		}
	}
	skipped := strings.Join(plan.Report.SkippedDataEntries, ",")
	if !strings.Contains(skipped, "backups") || !strings.Contains(skipped, "feishu-setup-qr.png") {
		t.Fatalf("skipped entries = %q, want both extras reported", skipped)
	}
}

func TestCollectLegacyDataDirRejectsDataDirWithNoRecognizableState(t *testing.T) {
	// Skipping must not turn a data_dir that points somewhere else entirely
	// into a silent, near-empty "success".
	root := t.TempDir()
	source := filepath.Join(root, "etc", "cc-connect")
	dataDir := filepath.Join(root, "home", "service")
	target := filepath.Join(root, "var", "cc-connect-next")
	writeMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(dataDir)+`"`+"\n")
	writeMigrationFixture(t, filepath.Join(dataDir, ".ssh", "id_private"), "must-not-be-inventoried")
	writeMigrationFixture(t, filepath.Join(dataDir, "Documents", "notes.txt"), "unrelated")

	_, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   filepath.Join(root, "root-home"),
		DryRun: true,
	})
	if err == nil || !strings.Contains(err.Error(), "source data_dir must be dedicated") {
		t.Fatalf("prepare error = %v, want dedicated-directory refusal", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("refusal created a target: %v", statErr)
	}
}

func TestPrepareLegacyMigrationReportsConfigValuesPointingAtTheSource(t *testing.T) {
	// Migration preserves bytes and rewrites only data_dir, so any other
	// absolute path keeps binding the migrated install to the old directory.
	// Those values have to be named, otherwise the source cannot be retired.
	root := t.TempDir()
	source := filepath.Join(root, ".cc-connect")
	target := filepath.Join(root, ".cc-connect-next")
	proxy := filepath.ToSlash(filepath.Join(source, "plus", "proxy.mjs"))
	writeRawMigrationFixture(t, filepath.Join(source, "config.toml"), `data_dir = "`+filepath.ToSlash(source)+`"

[[projects]]
name = "demo"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "`+filepath.ToSlash(root)+`"
cmd = "node `+proxy+`"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_demo"
app_secret = "demo-secret"
`)
	writeMigrationFixture(t, filepath.Join(source, "sessions", "demo.json"), `{"sessions":{}}`)

	plan, err := prepareLegacyMigration(migrationOptions{
		Source: source,
		Target: target,
		Home:   root,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("prepareLegacyMigration() error = %v", err)
	}
	refs := plan.Report.SourceReferences
	if len(refs) != 1 || refs[0] != "projects[0].agent.options.cmd" {
		t.Fatalf("source references = %v, want the agent cmd key path only", refs)
	}
	for _, ref := range refs {
		if strings.Contains(ref, "proxy.mjs") {
			t.Fatalf("reference %q leaks the value; report key paths only", ref)
		}
	}
}

func TestRunMigrateCommandNamesBothRootsWhenDataDirIsElsewhere(t *testing.T) {
	// --source is the directory holding config.toml. When that config omits
	// data_dir the official default is the effective state directory, so the
	// summary must not attribute those files to --source.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	staging := filepath.Join(home, "staging")
	official := filepath.Join(home, ".cc-connect")
	target := filepath.Join(home, ".cc-connect-next")
	writeMigrationFixture(t, filepath.Join(staging, "config.toml"), "language = \"zh\"\n")
	writeMigrationFixture(t, filepath.Join(official, "sessions", "demo.json"), `{"sessions":{}}`)

	var stdout, stderr bytes.Buffer
	code := runMigrateCommand([]string{
		"--dry-run",
		"--source", staging,
		"--target", target,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runMigrateCommand() code = %d, stderr = %q", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "config from") || !strings.Contains(out, "state from") {
		t.Fatalf("summary = %q, want both roots named", out)
	}
	if strings.Contains(out, "persistent files from "+staging) {
		t.Fatalf("summary = %q, still attributes the state to --source", out)
	}
}
