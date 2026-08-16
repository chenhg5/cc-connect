package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	ccconfig "github.com/timmyagentic/cc-connect-next/config"
	"github.com/timmyagentic/cc-connect-next/core"
)

const migrationManifestFilename = "migration-manifest.json"

const automaticMigrationSourceVersion = "auto-layout-v1"

var supportedMigrationSourceVersions = map[string]struct{}{
	"v1.4.1":        {},
	"v1.5.0-beta.1": {},
	"v1.5.0-beta.2": {},
	"v1.5.0-beta.3": {},
}

type migrationOptions struct {
	Source             string
	Target             string
	Home               string
	RuntimeWorkDir     string
	SourceVersion      string
	Force              bool
	DryRun             bool
	IncludeProjectData bool
}

type migrationManifest struct {
	Version            int                             `json:"version"`
	SourceVersion      string                          `json:"source_version"`
	CreatedAt          string                          `json:"created_at"`
	SourceRoot         string                          `json:"source_root"`
	SourceWorkDir      string                          `json:"source_work_dir"`
	SourceDataDir      string                          `json:"source_data_dir"`
	TargetRoot         string                          `json:"target_root"`
	Files              []migrationFileRecord           `json:"files"`
	ProjectDirectories []migrationProjectRecord        `json:"project_directories,omitempty"`
	SkippedProjects    []migrationSkippedProjectRecord `json:"skipped_project_discovery,omitempty"`
	Backups            []migrationBackupRecord         `json:"backups,omitempty"`
	SkippedRuntime     int                             `json:"skipped_runtime_entries"`
	SkippedSymlinks    int                             `json:"skipped_symlinks"`
}

type migrationFileRecord struct {
	Scope  string `json:"scope"`
	Source string `json:"source"`
	Target string `json:"target"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type migrationProjectRecord struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Files  int    `json:"files"`
}

type migrationSkippedProjectRecord struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type migrationBackupRecord struct {
	Target string `json:"target"`
	Backup string `json:"backup"`
}

type legacyMigrationConfig struct {
	DataDir  string                   `toml:"data_dir"`
	Projects []legacyMigrationProject `toml:"projects"`
}

type legacyMigrationProject struct {
	Name    string `toml:"name"`
	Mode    string `toml:"mode"`
	BaseDir string `toml:"base_dir"`
	Agent   struct {
		Type    string         `toml:"type"`
		Options map[string]any `toml:"options"`
	} `toml:"agent"`
	Platforms []legacyMigrationPlatform `toml:"platforms"`
}

type legacyMigrationPlatform struct {
	Type string `toml:"type"`
}

type plannedMigrationFile struct {
	Scope        string
	Source       string
	Rel          string
	Size         int64
	SHA256       string
	SourceSize   int64
	SourceSHA256 string
	ModTime      time.Time
	Content      []byte
}

type migrationAccessMetadata struct {
	Mode     fs.FileMode
	IsDir    bool
	UID      int
	GID      int
	HasOwner bool
}

type migrationDestination struct {
	Scope            string
	Target           string
	Files            map[string]plannedMigrationFile
	PreserveAccess   bool
	Access           map[string]migrationAccessMetadata
	TargetSnapshot   *migrationTargetSnapshot
	Stage            string
	Backup           string
	Existed          bool
	ExistingNonEmpty bool
}

type migrationTargetSnapshot struct {
	Exists  bool
	Entries map[string]migrationTargetSnapshotEntry
}

type migrationTargetSnapshotEntry struct {
	Kind     string
	Mode     fs.FileMode
	Size     int64
	SHA256   string
	Link     string
	UID      int
	GID      int
	HasOwner bool
}

type migrationHooks struct {
	BeforePromotion   func()
	AfterTargetRename func(target, backup string)
	AfterPromotion    func(target string)
}

type preparedMigration struct {
	SourceVersion string
	SourceRoot    string
	SourceWorkDir string
	SourceDataDir string
	Main          *migrationDestination
	Projects      []*migrationDestination
	Report        migrationReport
	CreatedAt     time.Time
}

// normalizeMigrationSourceVersion records exact provenance when the caller
// knows it. Auto mode intentionally validates the on-disk layout and every
// configured plugin instead of executing a binary named by daemon metadata.
// This keeps the default one-command migration safe for installed, stopped,
// and already-uninstalled official instances alike.
func normalizeMigrationSourceVersion(raw string) (string, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" || value == "auto" {
		return automaticMigrationSourceVersion, nil
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if _, ok := supportedMigrationSourceVersions[value]; !ok {
		versions := make([]string, 0, len(supportedMigrationSourceVersions))
		for version := range supportedMigrationSourceVersions {
			versions = append(versions, version)
		}
		sort.Strings(versions)
		return "", fmt.Errorf("unsupported official CC Connect source release %q; supported releases: %s, or use auto for strict layout validation", raw, strings.Join(versions, ", "))
	}
	return value, nil
}

// migrationGatedAgentOptions names official CC Connect agent options whose
// behavior this build does not implement yet. A config that enables one must
// fail preflight instead of migrating into silently different behavior.
// An explicit boolean false is behavior-neutral and passes.
var migrationGatedAgentOptions = map[string][]string{
	// Official v1.5.0-beta.x pi gained a persistent RPC transport
	// (--mode rpc). This build still runs pi one-shot in json mode.
	"pi": {"rpc"},
}

// migrationGatedPlatformOptions is the platform-side counterpart of
// migrationGatedAgentOptions. Currently empty: audit new official releases
// before extending the supported-source matrix and record gaps here.
var migrationGatedPlatformOptions = map[string][]string{}

func validateMigrationGatedOptions(projectName, pluginKind, pluginType string, options map[string]any, gated map[string][]string) error {
	for _, gatedKey := range gated[pluginType] {
		value, exists := options[gatedKey]
		if !exists {
			continue
		}
		if flag, ok := value.(bool); ok && !flag {
			continue
		}
		return fmt.Errorf("source project %q uses %s %q option %q, which this cc-connect-next build does not implement; migration would preserve bytes but not behavior, so no target was written (remove the option or keep the official installation for it)", projectName, pluginKind, pluginType, gatedKey)
	}
	return nil
}

func validateMigrationProjectPlugins(cfg *ccconfig.Config) error {
	agents := stringSet(core.ListRegisteredAgents())
	platforms := stringSet(core.ListRegisteredPlatforms())
	for _, project := range cfg.Projects {
		projectName := strings.TrimSpace(project.Name)
		if projectName == "" {
			projectName = "<unnamed>"
		}
		// Runtime registry lookups are exact and case-sensitive. Preflight must
		// use identical semantics so it cannot approve a config that startup
		// later rejects (for example, "Codex" or " codex ").
		agentType := project.Agent.Type
		if agentType != "" {
			if _, ok := agents[agentType]; !ok {
				return fmt.Errorf("source project %q uses agent %q, which this cc-connect-next build does not provide; migration would create a configuration that cannot start", projectName, project.Agent.Type)
			}
			if err := validateMigrationGatedOptions(projectName, "agent", agentType, project.Agent.Options, migrationGatedAgentOptions); err != nil {
				return err
			}
			if err := core.ValidateAgentOptions(agentType, buildAgentOptions(cfg.DataDir, project)); err != nil {
				return fmt.Errorf("source project %q uses agent %q with options this cc-connect-next build cannot start: %w; migration did not write the target", projectName, project.Agent.Type, err)
			}
		}
		for _, platform := range project.Platforms {
			platformType := platform.Type
			if platformType == "" {
				continue
			}
			if _, ok := platforms[platformType]; !ok {
				return fmt.Errorf("source project %q uses platform %q, which this cc-connect-next build does not provide; migration would create a configuration that cannot start", projectName, platform.Type)
			}
			if err := validateMigrationGatedOptions(projectName, "platform", platformType, platform.Options, migrationGatedPlatformOptions); err != nil {
				return err
			}
			opts := make(map[string]any, len(platform.Options)+2)
			for key, value := range platform.Options {
				opts[key] = value
			}
			opts["cc_data_dir"] = cfg.DataDir
			opts["cc_project"] = project.Name
			if err := core.ValidatePlatformOptions(platformType, opts); err != nil {
				return fmt.Errorf("source project %q uses platform %q with options this cc-connect-next build cannot start: %w; migration did not write the target", projectName, platform.Type, err)
			}
		}
	}
	return nil
}

// migrationEnvConsumingAgents lists the agent types whose factories read the
// env option table (directly or via core.ParseConfigEnv). devin and tmux do
// not spawn a CLI with a caller-controlled environment and ignore env, so an
// env table configured for them must not migrate as if it kept its behavior.
var migrationEnvConsumingAgents = map[string]struct{}{
	"acp":         {},
	"antigravity": {},
	"claudecode":  {},
	"codex":       {},
	"copilot":     {},
	"cursor":      {},
	"gemini":      {},
	"iflow":       {},
	"kimi":        {},
	"opencode":    {},
	"pi":          {},
	"qoder":       {},
}

// migrationAllowedPlatformOptionTables returns the nested platform-option
// tables whose leaves may be ignored by the undecoded check. A table name is
// allowed only when every project that defines it uses a platform type that
// actually consumes it; otherwise the migrated bytes would not preserve
// behavior and the leaves must surface as unsupported settings.
func migrationAllowedPlatformOptionTables(cfg *ccconfig.Config) map[string]struct{} {
	feishuFamily := map[string]struct{}{"feishu": {}, "lark": {}}
	consumers := map[string]map[string]struct{}{
		// mention_map and peer_bots power Feishu bot-to-bot mention
		// resolution and relay; no other platform reads them. Their types
		// and values are checked by the Feishu validator afterwards.
		"mention_map": feishuFamily,
		"peer_bots":   feishuFamily,
	}
	allowed := make(map[string]struct{})
	for table, types := range consumers {
		present := false
		safe := true
		for _, project := range cfg.Projects {
			for _, platform := range project.Platforms {
				if _, exists := platform.Options[table]; !exists {
					continue
				}
				present = true
				if _, ok := types[platform.Type]; !ok {
					safe = false
				}
			}
		}
		if present && safe {
			allowed[table] = struct{}{}
		}
	}
	return allowed
}

// migrationAllowedAgentOptionTables is the agent-side counterpart: env is the
// only nested agent-option table any supported official release defines.
func migrationAllowedAgentOptionTables(cfg *ccconfig.Config) map[string]struct{} {
	present := false
	safe := true
	for _, project := range cfg.Projects {
		if _, exists := project.Agent.Options["env"]; !exists {
			continue
		}
		present = true
		if _, ok := migrationEnvConsumingAgents[project.Agent.Type]; !ok {
			safe = false
		}
	}
	if present && safe {
		return map[string]struct{}{"env": {}}
	}
	return map[string]struct{}{}
}

func decodeAndValidateMigrationConfig(configBytes []byte) (*ccconfig.Config, error) {
	var cfg ccconfig.Config
	metadata, err := toml.Decode(string(configBytes), &cfg)
	if err != nil {
		return nil, fmt.Errorf("source config is incompatible with this cc-connect-next build: %w", err)
	}
	allowedPlatformTables := migrationAllowedPlatformOptionTables(&cfg)
	allowedAgentTables := migrationAllowedAgentOptionTables(&cfg)
	undecoded := metadata.Undecoded()
	if len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			// BurntSushi/toml decodes map-valued entries inside the dynamic
			// agent/platform options maps, but still reports each nested leaf
			// as undecoded metadata. Explicitly supported dynamic tables
			// (env, mention_map, peer_bots) whose values are validated by the
			// consuming plugin must not be mistaken for unknown settings here.
			parts := []string(key)
			if len(parts) >= 5 && parts[0] == "projects" && parts[2] == "options" {
				if parts[1] == "platforms" {
					if _, ok := allowedPlatformTables[parts[3]]; ok {
						continue
					}
				}
				if parts[1] == "agent" {
					if _, ok := allowedAgentTables[parts[3]]; ok {
						continue
					}
				}
			}
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			return nil, fmt.Errorf("source config uses unsupported settings (%s); migration would preserve bytes but not behavior, so no target was written", strings.Join(keys, ", "))
		}
	}
	// Load resolves environment placeholders and provider references before
	// semantic validation. Migration preserves the original TOML bytes, but its
	// preflight must evaluate them with the same process environment and runtime
	// semantics as the successor that will later read those bytes.
	cfg.ResolveEnvironment()
	cfg.ResolveProviderRefs()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("source config is semantically incompatible with this cc-connect-next build: %w; no target was written", err)
	}
	return &cfg, nil
}

// collectMigrationSourceReferences returns the config key paths whose string
// values still name the source directory.
//
// Migration is byte-faithful and rewrites only the top-level data_dir, so every
// other absolute path — a wrapper `cmd`, a plugin directory, a state_dir, a log
// file — keeps pointing into the official installation after migration. Those
// values cannot be rewritten safely (they may belong to third-party tooling
// with its own layout), but leaving them unmentioned makes a migrated setup
// look self-contained when it is not, and the source impossible to retire
// safely. Only key paths are reported: values can carry credentials.
func collectMigrationSourceReferences(parsed map[string]any, roots []string) []string {
	prefixes := make([]string, 0, len(roots))
	seenRoot := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		cleaned := strings.TrimSpace(root)
		if cleaned == "" {
			continue
		}
		cleaned = strings.ToLower(filepath.ToSlash(filepath.Clean(cleaned)))
		if cleaned == "" || cleaned == "/" || cleaned == "." {
			continue
		}
		if _, ok := seenRoot[cleaned]; ok {
			continue
		}
		seenRoot[cleaned] = struct{}{}
		prefixes = append(prefixes, cleaned)
	}
	if len(prefixes) == 0 {
		return nil
	}

	references := make([]string, 0)
	mentions := func(value string) bool {
		haystack := strings.ToLower(filepath.ToSlash(value))
		for _, prefix := range prefixes {
			if strings.Contains(haystack, prefix) {
				return true
			}
		}
		return false
	}
	var walk func(path string, node any)
	walk = func(path string, node any) {
		switch typed := node.(type) {
		case string:
			// data_dir is the one path migration rewrites for the caller.
			if path == "data_dir" {
				return
			}
			if mentions(typed) {
				references = append(references, path)
			}
		case []any:
			for i, child := range typed {
				walk(fmt.Sprintf("%s[%d]", path, i), child)
			}
		case map[string]any:
			for key, child := range typed {
				child := child
				next := key
				if path != "" {
					next = path + "." + key
				}
				walk(next, child)
			}
		case []map[string]any:
			for i, child := range typed {
				walk(fmt.Sprintf("%s[%d]", path, i), child)
			}
		}
	}
	walk("", parsed)
	sort.Strings(references)
	return references
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func migrateLegacyDataWithOptions(opts migrationOptions) (migrationReport, error) {
	return migrateLegacyDataWithHooks(opts, migrationHooks{})
}

func migrateLegacyDataWithHooks(opts migrationOptions, hooks migrationHooks) (migrationReport, error) {
	plan, err := prepareLegacyMigration(opts)
	if err != nil {
		return migrationReport{DryRun: opts.DryRun}, err
	}
	if opts.DryRun {
		return plan.Report, nil
	}

	destinations := append(append([]*migrationDestination{}, plan.Projects...), plan.Main)
	for _, destination := range destinations {
		if err := prepareMigrationStage(destination); err != nil {
			cleanupMigrationStages(destinations)
			return plan.Report, fmt.Errorf("prepare target %s: %w", destination.Target, err)
		}
	}
	if err := verifyMigrationSourcesUnchanged(destinations); err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, err
	}

	manifest := buildMigrationManifest(plan)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, fmt.Errorf("encode migration manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writeStagedContent(plan.Main.Stage, migrationManifestFilename, manifestBytes, sha256Bytes(manifestBytes)); err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, fmt.Errorf("write migration manifest: %w", err)
	}

	// The official process intentionally remains online during migration. Build
	// a fresh, read-only plan immediately before activation so additions,
	// deletions, newly discovered projects, and access-mode changes cannot be
	// silently omitted just because they were absent from the first inventory.
	freshPlan, err := prepareLegacyMigration(opts)
	if err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, fmt.Errorf("re-scan migration source: %w", err)
	}
	if err := verifyMigrationPlanUnchanged(plan, freshPlan); err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, err
	}
	if hooks.BeforePromotion != nil {
		hooks.BeforePromotion()
	}
	if err := verifyMigrationTargetsUnchanged(destinations); err != nil {
		cleanupMigrationStages(destinations)
		return plan.Report, err
	}

	promoted := make([]*migrationDestination, 0, len(destinations))
	for _, destination := range destinations {
		if err := promoteMigrationDestination(destination, hooks); err != nil {
			recoveryPaths, rollbackErr := rollbackPromotedDestinations(promoted)
			cleanupMigrationStages(destinations)
			migrationErr := fmt.Errorf("activate target %s: %w", destination.Target, err)
			if len(recoveryPaths) > 0 {
				migrationErr = errors.Join(migrationErr, fmt.Errorf("rollback preserved promoted targets for recovery: %s", strings.Join(recoveryPaths, ", ")))
			}
			if rollbackErr != nil {
				migrationErr = errors.Join(migrationErr, fmt.Errorf("rollback was incomplete: %w", rollbackErr))
			}
			return plan.Report, migrationErr
		}
		promoted = append(promoted, destination)
		if hooks.AfterPromotion != nil {
			hooks.AfterPromotion(destination.Target)
		}
	}
	cleanupMigrationStages(destinations)
	return plan.Report, nil
}

func prepareLegacyMigration(opts migrationOptions) (*preparedMigration, error) {
	if strings.TrimSpace(opts.Home) == "" {
		return nil, fmt.Errorf("home directory is required")
	}
	source, err := canonicalExistingDirectory(opts.Source)
	if err != nil {
		return nil, fmt.Errorf("read source directory: %w", err)
	}
	target, err := canonicalDestinationPath(opts.Target)
	if err != nil {
		return nil, fmt.Errorf("resolve target: %w", err)
	}
	overlaps, err := migrationPathsOverlap(source, target)
	if err != nil {
		return nil, fmt.Errorf("compare source and target paths: %w", err)
	}
	if overlaps {
		return nil, fmt.Errorf("source and target must be separate directories")
	}

	configPath := filepath.Join(source, "config.toml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read source config: %w", err)
	}
	var legacyCfg legacyMigrationConfig
	if _, err := toml.Decode(string(configBytes), &legacyCfg); err != nil {
		return nil, fmt.Errorf("source config is invalid TOML: %w", err)
	}
	// Keep raw path fields for the migration-specific fail-closed resolver,
	// which rejects unset environment placeholders instead of turning them into
	// empty paths. Use validatedCfg for runtime identities such as project names
	// so inventory matches the configuration that startup will actually use.
	validatedCfg, err := decodeAndValidateMigrationConfig(configBytes)
	if err != nil {
		return nil, err
	}
	sourceVersion, err := normalizeMigrationSourceVersion(opts.SourceVersion)
	if err != nil {
		return nil, err
	}
	if err := validateMigrationProjectPlugins(validatedCfg); err != nil {
		return nil, err
	}
	runtimeWorkDir, err := resolveLegacyRuntimeWorkDir(opts.RuntimeWorkDir, source, opts.Home)
	if err != nil {
		return nil, err
	}

	dataDirCandidate := filepath.Join(opts.Home, ".cc-connect")
	customDataDir := false
	if strings.TrimSpace(legacyCfg.DataDir) != "" {
		customDataDir = true
		dataDirCandidate, err = resolveLegacyConfigPath(legacyCfg.DataDir, runtimeWorkDir, opts.Home)
		if err != nil {
			return nil, fmt.Errorf("resolve source data_dir: %w", err)
		}
	}
	dataDirExists := true
	dataDir, err := canonicalExistingDirectory(dataDirCandidate)
	if errors.Is(err, os.ErrNotExist) {
		dataDirExists = false
		dataDir, err = canonicalDestinationPath(dataDirCandidate)
	}
	if err != nil {
		return nil, fmt.Errorf("read source data_dir: %w", err)
	}
	dataDirIsSource, err := migrationExistingPathsSame(dataDir, source)
	if err != nil {
		return nil, fmt.Errorf("compare source config and data directories: %w", err)
	}
	overlaps, err = migrationPathsOverlap(dataDir, target)
	if err != nil {
		return nil, fmt.Errorf("compare source data_dir and target paths: %w", err)
	}
	if overlaps {
		return nil, fmt.Errorf("source data_dir and target must be separate directories")
	}
	dataDirContainsSource, err := migrationPathContains(dataDir, source)
	if err != nil {
		return nil, fmt.Errorf("compare source data_dir and config paths: %w", err)
	}
	if customDataDir && !dataDirIsSource && dataDirContainsSource {
		return nil, fmt.Errorf("source data_dir must be dedicated: %s contains the source config directory %s; refusing to inventory unrelated files", dataDir, source)
	}

	mainDestination := &migrationDestination{
		Scope:          "data",
		Target:         target,
		Files:          make(map[string]plannedMigrationFile),
		PreserveAccess: true,
		Access:         make(map[string]migrationAccessMetadata),
	}
	report := migrationReport{
		DryRun:        opts.DryRun,
		SourceRoot:    source,
		SourceDataDir: dataDir,
		SourceWorkDir: runtimeWorkDir,
		ManifestPath:  filepath.Join(target, migrationManifestFilename),
		SourceVersion: sourceVersion,
	}

	// --source is the directory containing config.toml, not necessarily a
	// product-owned data directory. Only traverse it when it is also the
	// effective data_dir; otherwise a project-local config could cause an
	// entire repository, .env files, or service home to be copied. The config
	// file itself is added explicitly below.
	if dataDirIsSource && dataDirExists {
		var collectErr error
		if customDataDir {
			collectErr = collectLegacyDataDir(dataDir, validatedCfg, mainDestination, &report)
		} else {
			collectErr = collectMigrationTree(dataDir, "data-dir", true, mainDestination, &report)
		}
		if collectErr != nil {
			return nil, fmt.Errorf("inventory source data_dir: %w", collectErr)
		}
	} else if dataDirExists {
		var collectErr error
		if customDataDir {
			collectErr = collectLegacyDataDir(dataDir, validatedCfg, mainDestination, &report)
		} else {
			// An omitted data_dir means the official product-owned default
			// ~/.cc-connect even when config.toml was loaded from elsewhere.
			var excludedRoots []string
			if dataDirContainsSource && !dataDirIsSource {
				excludedRoots = append(excludedRoots, source)
			}
			collectErr = collectMigrationTreeExcluding(dataDir, "data-dir", true, excludedRoots, mainDestination, &report)
		}
		if collectErr != nil {
			return nil, fmt.Errorf("inventory source data_dir: %w", collectErr)
		}
	}

	migratedConfig, err := rewriteMigratedDataDir(configBytes, target)
	if err != nil {
		return nil, fmt.Errorf("rewrite config data_dir: %w", err)
	}
	var parsed map[string]any
	if _, err := toml.Decode(string(migratedConfig), &parsed); err != nil {
		return nil, fmt.Errorf("rewritten config is invalid TOML: %w", err)
	}
	report.SourceReferences = collectMigrationSourceReferences(parsed, []string{source, opts.Source, expandMigrationPath(opts.Source, opts.Home)})
	addPlannedMigrationFile(mainDestination, plannedMigrationFile{
		Scope:        "config",
		Source:       configPath,
		Rel:          "config.toml",
		Size:         int64(len(migratedConfig)),
		SHA256:       sha256Bytes(migratedConfig),
		SourceSize:   int64(len(configBytes)),
		SourceSHA256: sha256Bytes(configBytes),
		Content:      migratedConfig,
	}, false)

	projectDestinations := []*migrationDestination{}
	var skippedProjects []migrationSkippedProjectRecord
	if opts.IncludeProjectData {
		var projectRoots []string
		projectRoots, skippedProjects, err = discoverLegacyProjectRoots(legacyCfg, runtimeWorkDir, dataDir, opts.Home)
		if err != nil {
			return nil, err
		}
		for _, projectRoot := range projectRoots {
			projectSourceCandidate := filepath.Join(projectRoot, ".cc-connect")
			projectTargetCandidate := filepath.Join(projectRoot, ".cc-connect-next")
			projectSource := projectSourceCandidate
			projectTarget := projectTargetCandidate
			projectSource, err = canonicalExistingDirectory(projectSource)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				reason := "project data unavailable"
				if errors.Is(err, os.ErrPermission) {
					reason = "permission denied"
				}
				skippedProjects = appendMigrationSkippedProject(skippedProjects, projectSourceCandidate, reason)
				continue
			}
			projectTarget, err = canonicalDestinationPath(projectTarget)
			if err != nil {
				skippedProjects = appendMigrationSkippedProject(skippedProjects, projectTargetCandidate, "project target unavailable")
				continue
			}
			// A configured work_dir may be the home/runtime root, making its
			// apparent project-local .cc-connect the global config or effective
			// data directory. De-duplicate by source identity regardless of the
			// custom migration target; otherwise runtime files can enter the
			// project inventory and an unrequested sibling target can be created.
			projectIsSource, compareErr := migrationExistingPathsSame(projectSource, source)
			if compareErr != nil {
				return nil, fmt.Errorf("compare project and config source paths: %w", compareErr)
			}
			projectIsDataDir, compareErr := migrationExistingPathsSame(projectSource, dataDir)
			if compareErr != nil {
				return nil, fmt.Errorf("compare project and data source paths: %w", compareErr)
			}
			if projectIsSource || projectIsDataDir {
				continue
			}
			unsafeOverlap := false
			for _, pair := range [][2]string{
				{projectSource, projectTarget},
				{projectSource, target},
				{source, projectTarget},
				{dataDir, projectTarget},
			} {
				pairOverlaps, compareErr := migrationPathsOverlap(pair[0], pair[1])
				if compareErr != nil {
					return nil, fmt.Errorf("compare project-local migration paths: %w", compareErr)
				}
				if pairOverlaps {
					unsafeOverlap = true
					break
				}
			}
			if unsafeOverlap {
				return nil, fmt.Errorf("unsafe overlapping project-local migration path: %s -> %s", projectSource, projectTarget)
			}
			destination := &migrationDestination{
				Scope:          "project-data",
				Target:         projectTarget,
				Files:          make(map[string]plannedMigrationFile),
				PreserveAccess: true,
				Access:         make(map[string]migrationAccessMetadata),
			}
			var projectReport migrationReport
			if err := collectMigrationTree(projectSource, "project-data", false, destination, &projectReport); err != nil {
				reason := "project data inventory failed"
				if errors.Is(err, os.ErrPermission) {
					reason = "permission denied"
				}
				skippedProjects = appendMigrationSkippedProject(skippedProjects, projectSource, reason)
				continue
			}
			if len(destination.Files) == 0 {
				continue
			}
			report.SkippedRuntime += projectReport.SkippedRuntime
			report.SkippedSymlinks += projectReport.SkippedSymlinks
			projectDestinations = append(projectDestinations, destination)
		}
	}
	sort.Slice(skippedProjects, func(i, j int) bool { return skippedProjects[i].Source < skippedProjects[j].Source })
	report.SkippedProjects = append([]migrationSkippedProjectRecord(nil), skippedProjects...)

	destinations := append(append([]*migrationDestination{}, projectDestinations...), mainDestination)
	for i := 0; i < len(destinations); i++ {
		for j := i + 1; j < len(destinations); j++ {
			destinationsOverlap, compareErr := migrationPathsOverlap(destinations[i].Target, destinations[j].Target)
			if compareErr != nil {
				return nil, fmt.Errorf("compare migration destinations: %w", compareErr)
			}
			if destinationsOverlap {
				return nil, fmt.Errorf("migration destinations overlap: %s and %s", destinations[i].Target, destinations[j].Target)
			}
		}
	}
	for _, destination := range destinations {
		if info, err := os.Lstat(destination.Target); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("target must not be a symbolic link: %s", destination.Target)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect target %s: %w", destination.Target, err)
		}
		nonEmpty, err := directoryHasEntries(destination.Target)
		if err != nil {
			return nil, err
		}
		destination.ExistingNonEmpty = nonEmpty
		if _, err := os.Lstat(destination.Target); err == nil {
			destination.Existed = true
		}
		if nonEmpty && !opts.Force {
			return nil, fmt.Errorf("target is not empty: %s (use --force to merge deliberately)", destination.Target)
		}
	}

	createdAt := time.Now().UTC()
	for index, destination := range destinations {
		if destination.Existed {
			destination.Backup = availableMigrationBackupPath(destination.Target, createdAt, index)
		}
		if destination.Existed {
			report.Backups = append(report.Backups, migrationBackupRecord{
				Target: destination.Target,
				Backup: destination.Backup,
			})
		}
	}
	if mainDestination.Existed {
		report.BackupDir = mainDestination.Backup
	}
	report.ProjectDirectories = len(projectDestinations)
	report.CopiedFiles = len(mainDestination.Files)
	for _, project := range projectDestinations {
		report.CopiedFiles += len(project.Files)
	}

	return &preparedMigration{
		SourceVersion: sourceVersion,
		SourceRoot:    source,
		SourceWorkDir: runtimeWorkDir,
		SourceDataDir: dataDir,
		Main:          mainDestination,
		Projects:      projectDestinations,
		Report:        report,
		CreatedAt:     createdAt,
	}, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", resolved)
	}
	return resolved, nil
}

func canonicalDestinationPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(abs)
	resolvedParent, err := resolvePathThroughExistingAncestor(parent)
	if err != nil {
		return "", err
	}
	// Deliberately leave the final component unresolved. Existing target
	// symlinks are rejected later with Lstat, while every existing symlink in
	// the parent chain has already been canonicalized for overlap checks.
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

type migrationPathAncestor struct {
	info   fs.FileInfo
	suffix []string
}

// migrationPathsOverlap compares topology through filesystem identities, not
// only path spelling. This rejects case-only aliases on case-insensitive
// volumes and symlinked ancestors. For missing siblings below the same existing
// directory it compares components case-insensitively as a conservative safety
// rule; choosing a distinctly named target is preferable to risking source
// mutation on a volume whose case semantics cannot be inferred read-only.
func migrationPathsOverlap(a, b string) (bool, error) {
	aContainsB, err := migrationPathContains(a, b)
	if err != nil || aContainsB {
		return aContainsB, err
	}
	return migrationPathContains(b, a)
}

func migrationPathContains(parent, child string) (bool, error) {
	rel, err := filepath.Rel(parent, child)
	if err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
		return true, nil
	}
	parentAncestors, err := migrationPathAncestors(parent)
	if err != nil {
		return false, err
	}
	childAncestors, err := migrationPathAncestors(child)
	if err != nil {
		return false, err
	}
	for _, parentAncestor := range parentAncestors {
		for _, childAncestor := range childAncestors {
			if !os.SameFile(parentAncestor.info, childAncestor.info) {
				continue
			}
			if migrationPathComponentsPrefix(parentAncestor.suffix, childAncestor.suffix) {
				return true, nil
			}
		}
	}
	return false, nil
}

func migrationPathAncestors(path string) ([]migrationPathAncestor, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	current := abs
	suffix := make([]string, 0)
	ancestors := make([]migrationPathAncestor, 0)
	for {
		info, statErr := os.Stat(current)
		if statErr == nil {
			ancestors = append(ancestors, migrationPathAncestor{
				info:   info,
				suffix: append([]string(nil), suffix...),
			})
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect path ancestor %s: %w", current, statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
	}
	if len(ancestors) == 0 {
		return nil, fmt.Errorf("no existing ancestor for path %s", path)
	}
	return ancestors, nil
}

func migrationPathComponentsPrefix(parent, child []string) bool {
	if len(parent) > len(child) {
		return false
	}
	for i := range parent {
		if !strings.EqualFold(parent[i], child[i]) {
			return false
		}
	}
	return true
}

func migrationExistingPathsSame(a, b string) (bool, error) {
	aInfo, err := os.Stat(a)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	bInfo, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(aInfo, bInfo), nil
}

func resolvePathThroughExistingAncestor(path string) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func collectMigrationTree(root, scope string, skipRuntime bool, destination *migrationDestination, report *migrationReport) error {
	return collectMigrationTreeExcluding(root, scope, skipRuntime, nil, destination, report)
}

func pathStrictlyWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func collectMigrationTreeExcluding(root, scope string, skipRuntime bool, excludedRoots []string, destination *migrationDestination, report *migrationReport) error {
	return collectMigrationTreeExcludingWithPolicy(root, scope, skipRuntime, excludedRoots, nil, destination, report)
}

type migrationPathPolicy func(rel string, entry fs.DirEntry) error

func collectMigrationTreeExcludingWithPolicy(root, scope string, skipRuntime bool, excludedRoots []string, policy migrationPathPolicy, destination *migrationDestination, report *migrationReport) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		for _, excluded := range excludedRoots {
			if filepath.Clean(path) == filepath.Clean(excluded) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			if destination.PreserveAccess {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				destination.Access[rel] = migrationAccessMetadataForInfo(info)
			}
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if skipRuntime {
			_, namedRuntime := legacyRuntimeEntries[parts[0]]
			if namedRuntime || strings.HasSuffix(parts[0], ".lock") {
				if len(parts) == 1 {
					report.SkippedRuntime++
				}
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if rel == "config.toml" || rel == migrationManifestFilename {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			report.SkippedSymlinks++
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if policy != nil {
				if err := policy(rel, entry); err != nil {
					if errors.Is(err, errSkipMigrationEntry) {
						return filepath.SkipDir
					}
					return err
				}
			}
			if destination.PreserveAccess {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				destination.Access[filepath.Clean(rel)] = migrationAccessMetadataForInfo(info)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			report.SkippedRuntime++
			return nil
		}
		if policy != nil {
			if err := policy(rel, entry); err != nil {
				if errors.Is(err, errSkipMigrationEntry) {
					return nil
				}
				return err
			}
		}
		if destination.PreserveAccess {
			destination.Access[filepath.Clean(rel)] = migrationAccessMetadataForInfo(info)
		}
		hash, size, err := sha256File(path)
		if err != nil {
			return err
		}
		addPlannedMigrationFile(destination, plannedMigrationFile{
			Scope:        scope,
			Source:       path,
			Rel:          rel,
			Size:         size,
			SHA256:       hash,
			SourceSize:   size,
			SourceSHA256: hash,
			ModTime:      info.ModTime(),
		}, true)
		return nil
	})
}

var legacyPersistentRootFiles = map[string]struct{}{
	"dir_history.json":        {},
	"heartbeat_state.json":    {},
	"relay_bindings.json":     {},
	"workspace_bindings.json": {},
}

// errSkipMigrationEntry lets a path policy drop one entry without failing the
// whole inventory. Directories are not descended into.
var errSkipMigrationEntry = errors.New("skip migration entry")

// collectLegacyDataDir inventories a custom data_dir, skipping anything this
// build does not recognize as CC Connect state.
//
// A configured data_dir is not necessarily a directory the product owns alone:
// real installations accumulate onboarding images, hand-made backups, and
// third-party runtimes beside the state. Failing the migration for one of those
// blocked users whose setup was fine, while the protection that actually
// matters — never inventorying an unrelated file — is what skipping does. A
// data_dir that yields no recognizable state at all is still refused, because
// then the setting itself is wrong and a near-empty "success" would hide it.
func collectLegacyDataDir(root string, cfg *ccconfig.Config, destination *migrationDestination, report *migrationReport) error {
	skipped := make([]string, 0)
	policy := func(rel string, entry fs.DirEntry) error {
		if isKnownLegacyDataDirPath(rel, entry.IsDir(), cfg) {
			return nil
		}
		skipped = append(skipped, filepath.ToSlash(filepath.Clean(rel)))
		return errSkipMigrationEntry
	}
	if err := collectMigrationTreeExcludingWithPolicy(root, "data-dir", true, nil, policy, destination, report); err != nil {
		return err
	}
	if len(destination.Files) == 0 && len(skipped) > 0 {
		sort.Strings(skipped)
		sample := skipped
		if len(sample) > 5 {
			sample = sample[:5]
		}
		return fmt.Errorf("source data_dir must be dedicated: %s holds no recognizable CC Connect state, only unrelated entries (%s); refusing to inventory it", root, strings.Join(sample, ", "))
	}
	sort.Strings(skipped)
	report.SkippedDataEntries = append(report.SkippedDataEntries, skipped...)
	return nil
}

func isKnownLegacyDataDirPath(rel string, isDir bool, cfg *ccconfig.Config) bool {
	rel = filepath.Clean(rel)
	for _, project := range cfg.Projects {
		if isLegacyProjectSessionPath(rel, isDir, project.Name) {
			return true
		}
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
		return false
	}

	switch parts[0] {
	case "sessions":
		if len(parts) == 1 {
			return isDir
		}
		if isDir {
			return true
		}
		return strings.HasSuffix(parts[len(parts)-1], ".json")
	case "projects":
		if len(parts) == 1 {
			return isDir
		}
		if len(parts) != 2 || isDir {
			return false
		}
		return strings.HasSuffix(parts[1], ".state.json") || strings.HasSuffix(parts[1], ".opencode-models.json")
	case "crons", "timers":
		if len(parts) == 1 {
			return isDir
		}
		return len(parts) == 2 && !isDir && parts[1] == "jobs.json"
	case "config":
		if len(parts) == 1 {
			return isDir
		}
		return len(parts) == 2 && !isDir && parts[1] == "minimax.json"
	case "agent-prompts":
		if len(parts) == 1 {
			return isDir
		}
		if len(parts) != 2 || isDir {
			return false
		}
		return parts[1] == "cc-connect-system.md" ||
			strings.HasPrefix(parts[1], "cc-connect-system-") && strings.HasSuffix(parts[1], ".md")
	case "weixin":
		if len(parts) <= 3 {
			return isDir
		}
		if len(parts) != 4 || isDir {
			return false
		}
		return parts[3] == "context_tokens.json" || parts[3] == "get_updates.buf"
	}

	if len(parts) != 1 || isDir {
		return false
	}
	name := parts[0]
	if _, ok := legacyPersistentRootFiles[name]; ok {
		return true
	}
	if isLegacyMatrixStateFile(name) || name == "sessions.json" || strings.HasSuffix(name, ".sessions.json") {
		return true
	}
	return false
}

func isLegacyMatrixStateFile(name string) bool {
	if strings.HasPrefix(name, "matrix-cross-signing-") && strings.HasSuffix(name, ".json") {
		return true
	}
	if !strings.HasPrefix(name, "matrix-crypto-") {
		return false
	}
	return strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-shm") || strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-journal")
}

func isLegacyProjectSessionPath(rel string, isDir bool, projectName string) bool {
	if strings.TrimSpace(projectName) == "" {
		return false
	}

	directCandidates := []string{projectName + ".json", projectName + ".sessions.json"}
	for _, rawCandidate := range directCandidates {
		candidate, ok := safeLegacySessionRelativePath(rawCandidate)
		if !ok {
			continue
		}
		if isDir && isLegacySessionCandidateParent(rel, candidate) {
			return true
		}
		if !isDir && rel == candidate {
			return true
		}
	}

	for _, suffix := range []string{".json", ".sessions.json"} {
		template, ok := safeLegacySessionRelativePath(projectName + "_00000000" + suffix)
		if !ok {
			continue
		}
		if isDir {
			if isLegacySessionCandidateParent(rel, template) {
				return true
			}
			continue
		}
		marker := "00000000" + suffix
		if !strings.HasSuffix(template, marker) || !strings.HasSuffix(rel, suffix) {
			continue
		}
		prefix := strings.TrimSuffix(template, marker)
		if !strings.HasPrefix(rel, prefix) {
			continue
		}
		hash := strings.TrimSuffix(strings.TrimPrefix(rel, prefix), suffix)
		if len(hash) == 8 && isLowerHex(hash) {
			return true
		}
	}
	return false
}

func safeLegacySessionRelativePath(raw string) (string, bool) {
	cleaned := filepath.Clean(raw)
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleaned, true
}

func isLegacySessionCandidateParent(rel, candidate string) bool {
	dir := filepath.Dir(candidate)
	return dir != "." && (rel == dir || pathStrictlyWithin(rel, dir))
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func addPlannedMigrationFile(destination *migrationDestination, file plannedMigrationFile, archiveCollision bool) {
	file.Rel = filepath.Clean(file.Rel)
	existing, found := destination.Files[file.Rel]
	if !found {
		destination.Files[file.Rel] = file
		return
	}
	if existing.SHA256 == file.SHA256 {
		// The effective data_dir is authoritative when the same bytes also
		// happen to exist in the config root.
		destination.Files[file.Rel] = file
		return
	}
	if archiveCollision {
		archiveRel := filepath.Join("migration-archive", sanitizeMigrationScope(existing.Scope), existing.Rel)
		archiveRel = availableMigrationRel(destination.Files, archiveRel)
		existing.Rel = archiveRel
		destination.Files[archiveRel] = existing
	}
	destination.Files[file.Rel] = file
}

func sanitizeMigrationScope(scope string) string {
	scope = strings.TrimSpace(strings.ToLower(scope))
	scope = strings.ReplaceAll(scope, string(filepath.Separator), "-")
	if scope == "" {
		return "source"
	}
	return scope
}

func availableMigrationRel(files map[string]plannedMigrationFile, candidate string) string {
	if _, exists := files[candidate]; !exists {
		return candidate
	}
	ext := filepath.Ext(candidate)
	base := strings.TrimSuffix(candidate, ext)
	for i := 2; ; i++ {
		value := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, exists := files[value]; !exists {
			return value
		}
	}
}

func discoverLegacyProjectRoots(cfg legacyMigrationConfig, runtimeWorkDir, dataDir, home string) ([]string, []migrationSkippedProjectRecord, error) {
	roots := make(map[string]struct{})
	skipped := make(map[string]migrationSkippedProjectRecord)
	recordSkip := func(path, reason string) {
		path = filepath.Clean(path)
		skipped[path] = migrationSkippedProjectRecord{Source: path, Reason: reason}
	}
	addRoot := func(raw string) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		resolved, err := resolveLegacyConfigPath(raw, runtimeWorkDir, home)
		if err != nil {
			recordSkip(raw, "project path resolution failed")
			return
		}
		info, err := os.Stat(resolved)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if errors.Is(err, os.ErrPermission) {
			recordSkip(resolved, "permission denied")
			return
		}
		if err != nil {
			recordSkip(resolved, "project path unavailable")
			return
		}
		if !info.IsDir() {
			return
		}
		canonical, err := filepath.EvalSymlinks(resolved)
		if errors.Is(err, os.ErrPermission) {
			recordSkip(resolved, "permission denied")
			return
		}
		if err != nil {
			recordSkip(resolved, "project path resolution failed")
			return
		}
		roots[canonical] = struct{}{}
	}

	for _, project := range cfg.Projects {
		if raw, ok := project.Agent.Options["work_dir"].(string); ok {
			addRoot(raw)
		}
		if strings.TrimSpace(project.BaseDir) != "" {
			baseDir, err := resolveLegacyWorkspaceBaseDir(project.BaseDir, runtimeWorkDir, home)
			if err != nil {
				recordSkip(project.BaseDir, "workspace base path resolution failed")
				continue
			}
			discoverProjectRootsUnderBase(baseDir, roots, recordSkip)
		}
	}

	if err := discoverProjectRootsFromState(dataDir, addRoot, recordSkip); err != nil {
		return nil, nil, err
	}
	if err := discoverProjectRootsFromBindings(dataDir, addRoot, recordSkip); err != nil {
		return nil, nil, err
	}

	result := make([]string, 0, len(roots))
	for root := range roots {
		projectData := filepath.Join(root, ".cc-connect")
		if info, err := os.Stat(projectData); err == nil && info.IsDir() {
			result = append(result, root)
		} else if errors.Is(err, os.ErrPermission) {
			recordSkip(projectData, "permission denied")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			recordSkip(projectData, "project data unavailable")
		}
	}
	sort.Strings(result)
	skippedResult := make([]migrationSkippedProjectRecord, 0, len(skipped))
	for _, record := range skipped {
		skippedResult = append(skippedResult, record)
	}
	sort.Slice(skippedResult, func(i, j int) bool { return skippedResult[i].Source < skippedResult[j].Source })
	return result, skippedResult, nil
}

func appendMigrationSkippedProject(records []migrationSkippedProjectRecord, source, reason string) []migrationSkippedProjectRecord {
	source = filepath.Clean(source)
	for _, record := range records {
		if record.Source == source {
			return records
		}
	}
	return append(records, migrationSkippedProjectRecord{Source: source, Reason: reason})
}

func discoverProjectRootsUnderBase(baseDir string, roots map[string]struct{}, recordSkip func(string, string)) {
	info, err := os.Stat(baseDir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if errors.Is(err, os.ErrPermission) {
		recordSkip(baseDir, "permission denied")
		return
	}
	if err != nil {
		recordSkip(baseDir, "workspace base unavailable")
		return
	}
	if !info.IsDir() {
		return
	}
	_ = filepath.WalkDir(baseDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			reason := "workspace scan failed"
			if errors.Is(walkErr, os.ErrPermission) {
				reason = "permission denied"
			}
			recordSkip(path, reason)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.IsDir() && entry.Name() == ".cc-connect" {
			root, err := filepath.EvalSymlinks(filepath.Dir(path))
			if errors.Is(err, os.ErrPermission) {
				recordSkip(filepath.Dir(path), "permission denied")
				return filepath.SkipDir
			}
			if err != nil {
				recordSkip(filepath.Dir(path), "workspace path resolution failed")
				return filepath.SkipDir
			}
			roots[root] = struct{}{}
			return filepath.SkipDir
		}
		return nil
	})
}

func discoverProjectRootsFromState(dataRoot string, addRoot func(string), recordSkip func(string, string)) error {
	paths, err := filepath.Glob(filepath.Join(dataRoot, "projects", "*.state.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			recordSkip(path, "project state unreadable")
			continue
		}
		var state struct {
			WorkDirOverride       string            `json:"work_dir_override"`
			WorkspaceDirOverrides map[string]string `json:"workspace_dir_overrides"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			recordSkip(path, "malformed project state")
			continue
		}
		addRoot(state.WorkDirOverride)
		for workspace, override := range state.WorkspaceDirOverrides {
			addRoot(workspace)
			addRoot(override)
		}
	}
	return nil
}

func discoverProjectRootsFromBindings(dataRoot string, addRoot func(string), recordSkip func(string, string)) error {
	path := filepath.Join(dataRoot, "workspace_bindings.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		recordSkip(path, "workspace bindings unreadable")
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		recordSkip(path, "malformed workspace bindings")
		return nil
	}
	var walk func(any) error
	walk = func(node any) error {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "workspace" {
					if workspace, ok := child.(string); ok {
						addRoot(workspace)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func resolveLegacyRuntimeWorkDir(override, sourceRoot, home string) (string, error) {
	if strings.TrimSpace(override) != "" {
		resolved, err := resolveLegacyConfigPath(override, sourceRoot, home)
		if err != nil {
			return "", fmt.Errorf("resolve official runtime work_dir: %w", err)
		}
		canonical, err := canonicalExistingDirectory(resolved)
		if err != nil {
			return "", fmt.Errorf("read official runtime work_dir: %w", err)
		}
		return canonical, nil
	}

	// Official v1.4.1 writes daemon metadata under its default product data
	// directory even when config.toml is loaded from another working directory.
	// Do not trust a same-named file beside an arbitrary --source config.
	metadataRoot := filepath.Join(home, ".cc-connect")
	metadataPath := filepath.Join(metadataRoot, "daemon.json")
	data, err := os.ReadFile(metadataPath)
	if err == nil {
		var metadata struct {
			WorkDir string `json:"work_dir"`
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			return "", fmt.Errorf("parse official daemon metadata: %w; pass --runtime-work-dir with the verified original working directory", err)
		}
		if strings.TrimSpace(metadata.WorkDir) != "" {
			resolved, resolveErr := resolveLegacyConfigPath(metadata.WorkDir, metadataRoot, home)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve official daemon work_dir: %w", resolveErr)
			}
			canonical, canonicalErr := canonicalExistingDirectory(resolved)
			if canonicalErr != nil {
				return "", fmt.Errorf("read official daemon work_dir: %w; pass --runtime-work-dir with the verified original working directory", canonicalErr)
			}
			return canonical, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read official daemon metadata: %w", err)
	}
	return sourceRoot, nil
}

var legacyEnvPlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func resolveLegacyConfigPath(raw, baseDir, _ string) (string, error) {
	// Match config.resolveEnvPlaceholders syntax exactly: only ${NAME} expands,
	// while bare $NAME and ~ remain literal config path components. Migration is
	// intentionally stricter than startup for an unset placeholder: the daemon
	// may have captured an environment value that this interactive process did
	// not inherit, so substituting an empty string could inventory the wrong tree.
	var missingEnv string
	expanded := legacyEnvPlaceholderPattern.ReplaceAllStringFunc(raw, func(match string) string {
		parts := legacyEnvPlaceholderPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		value, ok := os.LookupEnv(parts[1])
		if !ok && missingEnv == "" {
			missingEnv = parts[1]
		}
		return value
	})
	if missingEnv != "" {
		return "", fmt.Errorf("environment variable %s referenced by legacy path is not set", missingEnv)
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(baseDir, expanded)
	}
	return filepath.Abs(filepath.Clean(expanded))
}

func resolveLegacyWorkspaceBaseDir(raw, runtimeWorkDir, home string) (string, error) {
	// Match the official multi-workspace startup path: base_dir alone treats a
	// leading "~/" as the user's home. Generic config paths deliberately keep
	// tilde literal, so this must remain a base_dir-specific compatibility rule.
	if strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(home, raw[2:])
	}
	return resolveLegacyConfigPath(raw, runtimeWorkDir, home)
}

func prepareMigrationStage(destination *migrationDestination) error {
	parent := filepath.Dir(destination.Target)
	if err := ensureMigrationDestinationParent(destination); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(destination.Target)+".migrate-*")
	if err != nil {
		return err
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	destination.Stage = stage
	targetSnapshot, err := snapshotMigrationTarget(destination.Target)
	if err != nil {
		return fmt.Errorf("snapshot existing target: %w", err)
	}
	if targetSnapshot.Exists != destination.Existed {
		return fmt.Errorf("target changed during migration: %s; refusing stale promotion", destination.Target)
	}
	if targetSnapshot.Exists && (len(targetSnapshot.Entries) > 1) != destination.ExistingNonEmpty {
		return fmt.Errorf("target changed during migration: %s; refusing stale promotion", destination.Target)
	}
	destination.TargetSnapshot = targetSnapshot
	access := make(map[string]migrationAccessMetadata)
	if destination.PreserveAccess && destination.Existed {
		existingAccess, err := collectMigrationAccess(destination.Target)
		if err != nil {
			return err
		}
		for rel, metadata := range existingAccess {
			access[rel] = metadata
		}
	}
	if destination.PreserveAccess {
		for rel, metadata := range destination.Access {
			access[rel] = metadata
		}
		// Generated files such as the rewritten config intentionally use the
		// migration's secure mode instead of inheriting a stale target mode
		// during --force. Source-backed files have explicit access metadata.
		for rel, file := range destination.Files {
			if file.Content == nil {
				continue
			}
			cleanRel := filepath.Clean(rel)
			if _, sourceBacked := destination.Access[cleanRel]; !sourceBacked {
				delete(access, cleanRel)
			}
		}
	}

	if destination.Existed {
		if err := copyExistingMigrationTarget(destination.Target, stage); err != nil {
			return err
		}
		if err := verifyMigrationTargetUnchanged(destination); err != nil {
			return err
		}
	}
	if destination.PreserveAccess {
		if err := ensurePreservedMigrationDirectories(stage, access); err != nil {
			return err
		}
	}

	rels := make([]string, 0, len(destination.Files))
	for rel := range destination.Files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if err := writePlannedMigrationFile(stage, destination.Files[rel]); err != nil {
			return err
		}
	}
	if destination.PreserveAccess {
		if err := applyMigrationAccessTree(stage, access); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationDestinationParent creates only missing target ancestors. New
// ancestors inherit the source data root's traversal mode and ownership so a
// run_as_user process can reach the migrated tree. Existing ancestors are
// intentionally left untouched because they may be shared with unrelated data.
func ensureMigrationDestinationParent(destination *migrationDestination) error {
	parent := filepath.Dir(destination.Target)
	missing := make([]string, 0)
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("migration target parent is not a directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect migration target parent %s: %w", current, err)
		}
		missing = append(missing, current)
		next := filepath.Dir(current)
		if next == current {
			return fmt.Errorf("cannot find existing ancestor for migration target %s", destination.Target)
		}
	}

	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil {
			return fmt.Errorf("create migration target parent %s: %w", missing[i], err)
		}
	}
	metadata, preserve := destination.Access["."]
	if !destination.PreserveAccess || !preserve || !metadata.IsDir {
		return nil
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := applyMigrationAccessPath(missing[i], metadata); err != nil {
			return fmt.Errorf("preserve migration target parent access for %s: %w", missing[i], err)
		}
	}
	return nil
}

func verifyMigrationSourcesUnchanged(destinations []*migrationDestination) error {
	verified := make(map[string]struct{})
	for _, destination := range destinations {
		for _, file := range destination.Files {
			if file.Source == "" {
				continue
			}
			key := file.Source + "\x00" + file.SourceSHA256
			if _, ok := verified[key]; ok {
				continue
			}
			hash, size, err := sha256File(file.Source)
			if err != nil {
				return fmt.Errorf("verify source %s: %w", file.Source, err)
			}
			if hash != file.SourceSHA256 || size != file.SourceSize {
				return fmt.Errorf("source changed during migration: %s; no target was activated", file.Source)
			}
			verified[key] = struct{}{}
		}
	}
	return nil
}

func verifyMigrationPlanUnchanged(original, fresh *preparedMigration) error {
	if original.SourceRoot != fresh.SourceRoot || original.SourceDataDir != fresh.SourceDataDir || original.SourceWorkDir != fresh.SourceWorkDir {
		return fmt.Errorf("source topology changed during migration; no target was activated")
	}
	if original.Report.SkippedSymlinks != fresh.Report.SkippedSymlinks || !sameSkippedProjects(original.Report.SkippedProjects, fresh.Report.SkippedProjects) {
		return fmt.Errorf("source discovery changed during migration; no target was activated")
	}
	originalDestinations := migrationDestinationsByTarget(original)
	freshDestinations := migrationDestinationsByTarget(fresh)
	if len(originalDestinations) != len(freshDestinations) {
		return fmt.Errorf("persistent source entries changed during migration; no target was activated")
	}
	for target, before := range originalDestinations {
		after, ok := freshDestinations[target]
		if !ok || before.Scope != after.Scope || before.PreserveAccess != after.PreserveAccess {
			return fmt.Errorf("persistent source destination changed during migration: %s; no target was activated", target)
		}
		if len(before.Files) != len(after.Files) {
			return fmt.Errorf("persistent source entries changed during migration for %s; no target was activated", target)
		}
		for rel, beforeFile := range before.Files {
			afterFile, ok := after.Files[rel]
			if !ok || beforeFile.Source != afterFile.Source || beforeFile.SourceSize != afterFile.SourceSize || beforeFile.SourceSHA256 != afterFile.SourceSHA256 || beforeFile.Size != afterFile.Size || beforeFile.SHA256 != afterFile.SHA256 {
				return fmt.Errorf("persistent source changed during migration: %s; no target was activated", beforeFile.Source)
			}
		}
		if len(before.Access) != len(after.Access) {
			return fmt.Errorf("migration access metadata changed during migration for %s; no target was activated", target)
		}
		for rel, beforeAccess := range before.Access {
			if afterAccess, ok := after.Access[rel]; !ok || beforeAccess != afterAccess {
				return fmt.Errorf("migration access metadata changed during migration: %s; no target was activated", filepath.Join(target, rel))
			}
		}
	}
	return nil
}

func migrationDestinationsByTarget(plan *preparedMigration) map[string]*migrationDestination {
	result := make(map[string]*migrationDestination, len(plan.Projects)+1)
	for _, destination := range append(append([]*migrationDestination{}, plan.Projects...), plan.Main) {
		result[destination.Target] = destination
	}
	return result
}

func sameSkippedProjects(a, b []migrationSkippedProjectRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func snapshotMigrationTarget(root string) (*migrationTargetSnapshot, error) {
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return &migrationTargetSnapshot{Entries: make(map[string]migrationTargetSnapshotEntry)}, nil
	}
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("target must not be a symbolic link: %s", root)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("target must be a directory: %s", root)
	}

	snapshot := &migrationTargetSnapshot{
		Exists:  true,
		Entries: make(map[string]migrationTargetSnapshotEntry),
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.Clean(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot.Entries[rel] = migrationTargetSnapshotEntry{Kind: "symlink", Link: link}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := "directory"
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("existing target contains unsupported runtime entry %s; stop cc-connect-next before --force", path)
			}
			kind = "file"
		}
		uid, gid, hasOwner := migrationOwnership(info)
		record := migrationTargetSnapshotEntry{
			Kind:     kind,
			Mode:     info.Mode().Perm(),
			Size:     info.Size(),
			UID:      uid,
			GID:      gid,
			HasOwner: hasOwner,
		}
		if kind == "file" {
			record.SHA256, record.Size, err = sha256File(path)
			if err != nil {
				return err
			}
		}
		snapshot.Entries[rel] = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func migrationTargetSnapshotsEqual(before, after *migrationTargetSnapshot) bool {
	if before == nil || after == nil || before.Exists != after.Exists || len(before.Entries) != len(after.Entries) {
		return false
	}
	for rel, beforeEntry := range before.Entries {
		if afterEntry, ok := after.Entries[rel]; !ok || afterEntry != beforeEntry {
			return false
		}
	}
	return true
}

func verifyMigrationTargetUnchanged(destination *migrationDestination) error {
	return verifyMigrationTargetSnapshotAt(destination, destination.Target)
}

func verifyMigrationTargetSnapshotAt(destination *migrationDestination, path string) error {
	if destination.TargetSnapshot == nil {
		return fmt.Errorf("target snapshot is missing for %s", destination.Target)
	}
	current, err := snapshotMigrationTarget(path)
	if err != nil {
		return fmt.Errorf("re-scan target %s: %w", path, err)
	}
	if !migrationTargetSnapshotsEqual(destination.TargetSnapshot, current) {
		return fmt.Errorf("target changed during migration: %s; refusing stale promotion", destination.Target)
	}
	return nil
}

func verifyMigrationTargetsUnchanged(destinations []*migrationDestination) error {
	for _, destination := range destinations {
		if err := verifyMigrationTargetUnchanged(destination); err != nil {
			return err
		}
	}
	return nil
}

func collectMigrationAccess(root string) (map[string]migrationAccessMetadata, error) {
	result := make(map[string]migrationAccessMetadata)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result[filepath.Clean(rel)] = migrationAccessMetadataForInfo(info)
		return nil
	})
	return result, err
}

func migrationAccessMetadataForInfo(info fs.FileInfo) migrationAccessMetadata {
	uid, gid, hasOwner := migrationOwnership(info)
	return migrationAccessMetadata{
		Mode:     info.Mode().Perm(),
		IsDir:    info.IsDir(),
		UID:      uid,
		GID:      gid,
		HasOwner: hasOwner,
	}
}

func ensurePreservedMigrationDirectories(root string, access map[string]migrationAccessMetadata) error {
	rels := make([]string, 0, len(access))
	for rel, metadata := range access {
		if metadata.IsDir && rel != "." {
			rels = append(rels, rel)
		}
	}
	sort.Slice(rels, func(i, j int) bool {
		depthI := strings.Count(filepath.ToSlash(rels[i]), "/")
		depthJ := strings.Count(filepath.ToSlash(rels[j]), "/")
		if depthI == depthJ {
			return rels[i] < rels[j]
		}
		return depthI < depthJ
	})
	for _, rel := range rels {
		if err := ensureStagedDirectory(root, rel); err != nil {
			return err
		}
	}
	return nil
}

func applyMigrationAccessTree(root string, access map[string]migrationAccessMetadata) error {
	rels := make([]string, 0, len(access))
	for rel := range access {
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool {
		if rels[i] == "." {
			return false
		}
		if rels[j] == "." {
			return true
		}
		depthI := strings.Count(filepath.ToSlash(rels[i]), "/")
		depthJ := strings.Count(filepath.ToSlash(rels[j]), "/")
		if depthI == depthJ {
			return rels[i] < rels[j]
		}
		return depthI > depthJ
	})
	for _, rel := range rels {
		path := root
		if rel != "." {
			if err := validateMigrationRel(rel); err != nil {
				return err
			}
			path = filepath.Join(root, rel)
		}
		metadata := access[rel]
		if err := applyMigrationAccessPath(path, metadata); err != nil {
			return err
		}
	}
	return nil
}

func applyMigrationAccessPath(path string, metadata migrationAccessMetadata) error {
	if metadata.HasOwner {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		uid, gid, ok := migrationOwnership(info)
		if !ok || uid != metadata.UID || gid != metadata.GID {
			if err := os.Chown(path, metadata.UID, metadata.GID); err != nil {
				return fmt.Errorf("preserve ownership for %s: %w", path, err)
			}
		}
	}
	if err := os.Chmod(path, metadata.Mode.Perm()); err != nil {
		return fmt.Errorf("preserve access mode for %s: %w", path, err)
	}
	return nil
}

func copyExistingMigrationTarget(source, stage string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(stage, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := ensureStagedDirectory(stage, filepath.Dir(rel)); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("existing target contains unsupported runtime entry %s; stop cc-connect-next before --force", path)
		}
		return copyMigrationFile(path, target)
	})
}

func writePlannedMigrationFile(stage string, file plannedMigrationFile) error {
	if err := validateMigrationRel(file.Rel); err != nil {
		return err
	}
	if file.Content != nil {
		if err := writeStagedContent(stage, file.Rel, file.Content, file.SHA256); err != nil {
			return err
		}
		return nil
	}
	if err := ensureStagedDirectory(stage, filepath.Dir(file.Rel)); err != nil {
		return err
	}
	target := filepath.Join(stage, file.Rel)
	if err := removeStagedLeaf(target); err != nil {
		return err
	}
	if err := copyMigrationFile(file.Source, target); err != nil {
		return fmt.Errorf("copy %s: %w", file.Source, err)
	}
	if !file.ModTime.IsZero() {
		if err := os.Chtimes(target, file.ModTime, file.ModTime); err != nil {
			return err
		}
	}
	hash, size, err := sha256File(target)
	if err != nil {
		return err
	}
	if hash != file.SHA256 || size != file.Size {
		return fmt.Errorf("verification failed for %s: source changed during migration", file.Source)
	}
	return nil
}

func writeStagedContent(stage, rel string, content []byte, expectedHash string) error {
	if err := validateMigrationRel(rel); err != nil {
		return err
	}
	if err := ensureStagedDirectory(stage, filepath.Dir(rel)); err != nil {
		return err
	}
	target := filepath.Join(stage, rel)
	if err := removeStagedLeaf(target); err != nil {
		return err
	}
	if err := writeMigrationFile(target, content); err != nil {
		return err
	}
	hash, _, err := sha256File(target)
	if err != nil {
		return err
	}
	if hash != expectedHash {
		return fmt.Errorf("verification failed for generated file %s", rel)
	}
	return nil
}

func validateMigrationRel(rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe migration path %q", rel)
	}
	return nil
}

func ensureStagedDirectory(stage, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	if err := validateMigrationRel(rel); err != nil {
		return err
	}
	current := stage
	for _, component := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target path contains a non-directory component: %s", current)
		}
	}
	return nil
}

func removeStagedLeaf(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func availableMigrationBackupPath(target string, createdAt time.Time, index int) string {
	stamp := createdAt.Format("20060102T150405Z")
	base := fmt.Sprintf("%s.pre-migration-%s", target, stamp)
	if index > 0 {
		base = fmt.Sprintf("%s-%d", base, index+1)
	}
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func promoteMigrationDestination(destination *migrationDestination, hooks migrationHooks) error {
	// Revalidate immediately before the rename as well as before the promotion
	// loop. This narrows the final write window for later project destinations;
	// any detected target activity aborts instead of activating a stale merge.
	if err := verifyMigrationTargetUnchanged(destination); err != nil {
		return err
	}
	if destination.Existed {
		if err := os.Rename(destination.Target, destination.Backup); err != nil {
			return err
		}
		if hooks.AfterTargetRename != nil {
			hooks.AfterTargetRename(destination.Target, destination.Backup)
		}
		// The rename closes the final path-based TOCTOU window: compare the
		// frozen old tree at its backup path before activating the stage. If an
		// already-open writer changed it at the boundary, put that newer tree
		// back and abort instead of hiding it behind a stale migration.
		if err := verifyMigrationTargetSnapshotAt(destination, destination.Backup); err != nil {
			if restoreErr := os.Rename(destination.Backup, destination.Target); restoreErr != nil {
				return fmt.Errorf("%w; restore changed target from %s: %v", err, destination.Backup, restoreErr)
			}
			return err
		}
	}
	if err := os.Rename(destination.Stage, destination.Target); err != nil {
		if destination.Existed {
			if restoreErr := os.Rename(destination.Backup, destination.Target); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("restore pre-migration target %s from retained backup %s: %w", destination.Target, destination.Backup, restoreErr))
			}
		}
		return err
	}
	destination.Stage = ""
	return nil
}

func rollbackPromotedDestinations(destinations []*migrationDestination) ([]string, error) {
	recoveryPaths := make([]string, 0, len(destinations))
	rollbackErrors := make([]error, 0)
	for i := len(destinations) - 1; i >= 0; i-- {
		destination := destinations[i]
		recoveryRoot, err := os.MkdirTemp(filepath.Dir(destination.Target), filepath.Base(destination.Target)+".failed-migration-*")
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("create recovery directory for %s: %w", destination.Target, err))
			continue
		}
		if err := os.Chmod(recoveryRoot, 0o700); err != nil {
			_ = os.Remove(recoveryRoot)
			rollbackErrors = append(rollbackErrors, fmt.Errorf("protect recovery directory for %s: %w", destination.Target, err))
			continue
		}
		recoveryPath := filepath.Join(recoveryRoot, "preserved")
		if err := os.Rename(destination.Target, recoveryPath); err != nil {
			_ = os.Remove(recoveryRoot)
			rollbackErrors = append(rollbackErrors, fmt.Errorf("preserve promoted target %s: %w", destination.Target, err))
			continue
		}
		recoveryPaths = append(recoveryPaths, recoveryPath)
		if destination.Existed && destination.Backup != "" {
			if err := os.Rename(destination.Backup, destination.Target); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore pre-migration target %s from %s: %w", destination.Target, destination.Backup, err))
			}
		}
	}
	return recoveryPaths, errors.Join(rollbackErrors...)
}

func cleanupMigrationStages(destinations []*migrationDestination) {
	for _, destination := range destinations {
		if destination.Stage != "" {
			_ = os.RemoveAll(destination.Stage)
			destination.Stage = ""
		}
	}
}

func buildMigrationManifest(plan *preparedMigration) migrationManifest {
	manifest := migrationManifest{
		Version:         2,
		SourceVersion:   plan.SourceVersion,
		CreatedAt:       plan.CreatedAt.Format(time.RFC3339Nano),
		SourceRoot:      plan.SourceRoot,
		SourceWorkDir:   plan.SourceWorkDir,
		SourceDataDir:   plan.SourceDataDir,
		TargetRoot:      plan.Main.Target,
		SkippedRuntime:  plan.Report.SkippedRuntime,
		SkippedSymlinks: plan.Report.SkippedSymlinks,
		SkippedProjects: append([]migrationSkippedProjectRecord(nil), plan.Report.SkippedProjects...),
	}
	destinations := append(append([]*migrationDestination{}, plan.Projects...), plan.Main)
	for _, destination := range destinations {
		rels := make([]string, 0, len(destination.Files))
		for rel := range destination.Files {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			file := destination.Files[rel]
			manifest.Files = append(manifest.Files, migrationFileRecord{
				Scope:  file.Scope,
				Source: file.Source,
				Target: filepath.Join(destination.Target, file.Rel),
				Size:   file.Size,
				SHA256: file.SHA256,
			})
		}
		if destination.Scope == "project-data" {
			manifest.ProjectDirectories = append(manifest.ProjectDirectories, migrationProjectRecord{
				Source: filepath.Join(filepath.Dir(destination.Target), ".cc-connect"),
				Target: destination.Target,
				Files:  len(destination.Files),
			})
		}
		if destination.Existed && destination.Backup != "" {
			manifest.Backups = append(manifest.Backups, migrationBackupRecord{
				Target: destination.Target,
				Backup: destination.Backup,
			})
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool {
		if manifest.Files[i].Target == manifest.Files[j].Target {
			return manifest.Files[i].Source < manifest.Files[j].Source
		}
		return manifest.Files[i].Target < manifest.Files[j].Target
	})
	return manifest
}

func sha256File(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func sha256Bytes(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}
