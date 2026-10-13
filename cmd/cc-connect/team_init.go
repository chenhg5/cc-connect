package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// A durable receipt allows local save/reply failures to be retried without
// registering another external app. Version 1 receipts retain legacy pairing;
// version 2 binds only the scanner returned by the registration service.
type teamInitSpec struct {
	ConfigPath string
	DataDir    string
	WorkDir    string
	Agent      string
	BotID      string
	Name       string
	Language   string
}

type teamInitReceipt struct {
	Version    int
	Spec       teamInitSpec
	Identity   registrationFlowResult
	ClaimToken string
}

func teamInitCommand(args []string) error {
	return teamInitCommandWithDeps(args, runRegistrationFlow, startTeamService, os.Stdout, ensureRolePermissions)
}

// The production launcher enters normal service mode; tests replace only the
// external registration service and process handoff.
func teamInitCommandWithDeps(args []string, register func(registrationFlowOptions) (*registrationFlowResult, error), start func(string) error, output io.Writer, permissionChecks ...func(context.Context, registrationFlowResult, string, string) rolePermissionReport) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var spec teamInitSpec
	fs := flag.NewFlagSet("team init", flag.ContinueOnError)
	fs.StringVar(&spec.ConfigPath, "config", filepath.Join(home, ".cc-connect-team", "config.toml"), "new configuration path")
	fs.StringVar(&spec.DataDir, "data-dir", "", "data directory (default: <config directory>/data)")
	fs.StringVar(&spec.WorkDir, "work-dir", ".", "existing manager working directory")
	fs.StringVar(&spec.Agent, "agent", "codex", "codex, claudecode, or trae")
	fs.StringVar(&spec.BotID, "id", "manager-main", "stable manager Bot ID")
	fs.StringVar(&spec.Name, "name", "", "manager display name (default: Bot ID)")
	fs.StringVar(&spec.Language, "language", "zh", "zh, en, zh-TW, ja, or es")
	timeout := fs.Int("timeout", 600, "QR authorization timeout in seconds")
	qrImage := fs.String("qr-image", "", "optional QR PNG destination")
	noStart := fs.Bool("no-start", false, "save configuration without starting the service")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *timeout <= 0 {
		return fmt.Errorf("unexpected arguments or invalid --timeout; use team init --help")
	}
	if err := spec.normalize(); err != nil {
		return err
	}
	i18n := core.NewI18n(core.NormalizeLanguageString(spec.Language))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	registerWithQR := func(opts registrationFlowOptions) (*registrationFlowResult, error) {
		opts.OnVerification = func(link string) error {
			if _, err := fmt.Fprintln(output, i18n.Tf(core.MsgTeamInitScan, link)); err != nil {
				return err
			}
			tryPrintTerminalQRCode(link)
			if *qrImage != "" {
				return saveQRCodeImage(link, *qrImage)
			}
			return nil
		}
		return register(opts)
	}
	receipt, err := initializeTeam(ctx, spec, *timeout, registerWithQR)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(permissionChecks) > 0 {
		report := permissionChecks[0](ctx, receipt.Identity, "manager", receipt.Spec.DataDir)
		permissionNotice(receipt.Identity, "manager", report, func(k core.MsgKey, a ...any) { fmt.Fprintln(output, i18n.Tf(k, a...)) })
	}
	// Restore signal handling before handing the terminal to the service. The
	// initializer's instance lock has already been released by initializeTeam.
	cancel()
	return startInitializedTeam(output, receipt, *noStart, start)
}

func startInitializedTeam(output io.Writer, receipt *teamInitReceipt, noStart bool, start func(string) error) error {
	i18n := core.NewI18n(core.NormalizeLanguageString(receipt.Spec.Language))
	if err := printTeamInitReady(output, receipt); err != nil {
		return err
	}
	if noStart {
		_, err := fmt.Fprintln(output, i18n.T(core.MsgTeamInitNoStart))
		return err
	}
	if _, err := fmt.Fprintln(output, i18n.T(core.MsgTeamInitStarting)); err != nil {
		return err
	}
	return start(receipt.Spec.ConfigPath)
}

func (s *teamInitSpec) normalize() error {
	if !core.ValidBotName(s.BotID) {
		return fmt.Errorf("invalid manager Bot ID")
	}
	s.Agent = strings.ToLower(strings.TrimSpace(s.Agent))
	if s.Agent != "codex" && s.Agent != "claudecode" && s.Agent != "trae" {
		return fmt.Errorf("--agent must be codex, claudecode, or trae")
	}
	lang := core.NormalizeLanguageString(s.Language)
	if lang == core.LangAuto {
		return fmt.Errorf("--language must be zh, en, zh-TW, ja, or es")
	}
	s.Language = string(lang)
	if s.Name == "" {
		s.Name = s.BotID
	}
	var err error
	for _, p := range []*string{&s.ConfigPath, &s.WorkDir} {
		if *p, err = absoluteTeamPath(*p); err != nil {
			return err
		}
	}
	if s.DataDir == "" {
		s.DataDir = filepath.Join(filepath.Dir(s.ConfigPath), "data")
	}
	if s.DataDir, err = absoluteTeamPath(s.DataDir); err != nil {
		return err
	}
	info, err := os.Stat(s.WorkDir)
	if err != nil {
		return fmt.Errorf("manager working directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("manager working directory must be a directory")
	}
	return nil
}

func absoluteTeamPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Abs(path)
}

func (s teamInitSpec) manager() config.BotConfig {
	agent := config.AgentConfig{Type: s.Agent, Options: map[string]any{"work_dir": s.WorkDir}}
	if s.Agent == "trae" {
		agent.Type = "acp"
		agent.Options["cmd"] = "traecli"
		agent.Options["args"] = []string{"acp", "serve"}
	}
	config.UseTeamApprovalDefaults(&agent)
	return config.BotConfig{ID: s.BotID, Name: s.Name, Role: "manager", Agent: agent}
}

func initializeTeam(ctx context.Context, spec teamInitSpec, timeout int, register func(registrationFlowOptions) (*registrationFlowResult, error)) (*teamInitReceipt, error) {
	if err := spec.normalize(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(spec.ConfigPath), 0700); err != nil {
		return nil, err
	}
	lock, err := AcquireInstanceLock(spec.ConfigPath)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	receiptPath := spec.ConfigPath + ".setup.json"
	receipt, err := readTeamInitReceipt(receiptPath)
	if err == nil {
		if receipt.Spec != spec {
			return nil, fmt.Errorf("pending setup options differ; retry with the original team init options")
		}
		if err := finishTeamInit(receipt); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := os.Lstat(spec.ConfigPath); err == nil {
		return nil, fmt.Errorf("config already exists; use a new --config path for team init")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := preflightTeamRegistration(spec); err != nil {
		return nil, err
	}
	identity, err := register(registrationFlowOptions{Context: ctx, TimeoutSeconds: timeout})
	if err != nil {
		return nil, fmt.Errorf("team registration: %w", err)
	}
	if identity == nil || identity.AppID == "" || identity.AppSecret == "" || (identity.Platform != "feishu" && identity.Platform != "lark") {
		return nil, fmt.Errorf("incomplete team registration result")
	}
	receipt = &teamInitReceipt{Version: 3, Spec: spec, Identity: *identity}
	data, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if err := writeNewPrivateFile(receiptPath, data); err != nil {
		return nil, fmt.Errorf("app registration completed but local receipt could not be saved: %w", err)
	}
	if err := finishTeamInit(receipt); err != nil {
		return nil, fmt.Errorf("registration saved; retry the same team init command: %w", err)
	}
	return receipt, nil
}

func preflightTeamRegistration(spec teamInitSpec) error {
	// Check compiled adapters and CLI availability before registering an app.
	bot := spec.manager()
	if !containsString(core.ListRegisteredPlatforms(), "feishu") {
		return fmt.Errorf("this build does not include Feishu")
	}
	agent, err := core.CreateAgent(bot.Agent.Type, bot.Agent.Options)
	if err != nil {
		return err
	}
	if err := agent.Stop(); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.DataDir, 0700); err != nil {
		return err
	}
	// Probe storage before the external operation.
	probe, err := os.CreateTemp(filepath.Dir(spec.ConfigPath), ".team-probe-*")
	if err != nil {
		return err
	}
	probe.Close()
	os.Remove(probe.Name())
	return nil
}

func readTeamInitReceipt(path string) (*teamInitReceipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r teamInitReceipt
	if json.Unmarshal(data, &r) != nil || (r.Version != 1 && r.Version != 2 && r.Version != 3) || r.Identity.AppID == "" || r.Identity.AppSecret == "" || !core.ValidBotName(r.Spec.BotID) || (r.Identity.Platform != "feishu" && r.Identity.Platform != "lark") {
		return nil, fmt.Errorf("invalid or unsupported setup receipt")
	}
	if r.Version == 1 {
		if token, err := hex.DecodeString(r.ClaimToken); err != nil || len(token) != 32 {
			return nil, fmt.Errorf("invalid setup claim token")
		}
	} else if r.ClaimToken != "" {
		return nil, fmt.Errorf("unexpected setup claim token")
	}
	return &r, nil
}

func (r *teamInitReceipt) config() *config.Config {
	bot := r.Spec.manager()
	if r.Version == 1 {
		hash := sha256.Sum256([]byte(r.ClaimToken))
		bot.BotOwnerClaimHash = hex.EncodeToString(hash[:])
	} else {
		bot.BotOwnerID = r.Identity.OwnerOpenID
		bot.AdminFrom = r.Identity.OwnerOpenID
	}
	bot.Platforms = []config.PlatformConfig{{Type: r.Identity.Platform, Options: map[string]any{
		"app_id": r.Identity.AppID, "app_secret": r.Identity.AppSecret,
		"group_only": false, "group_reply_all": false, "single_card_reply": true, "separate_final_card": true,
	}}}
	if r.Version >= 3 {
		p := config.ProjectConfig(bot)
		p.Name = bot.ID
		p.ID, p.Role = "", ""
		return &config.Config{DataDir: r.Spec.DataDir, Language: r.Spec.Language, Projects: []config.ProjectConfig{p}, Teams: []config.TeamConfig{{ID: "default", Manager: p.Name, AutoCoordinate: true, Members: []config.TeamMember{{Bot: p.Name, Role: "manager"}}}}}
	}
	return &config.Config{DataDir: r.Spec.DataDir, Language: r.Spec.Language, Bots: []config.BotConfig{bot}}
}

func finishTeamInit(r *teamInitReceipt) error {
	if r.Version >= 2 && !validRegistrationOwner(r.Identity.OwnerOpenID) {
		return fmt.Errorf("%s", core.NewI18n(core.NormalizeLanguageString(r.Spec.Language)).T(core.MsgBotRegistrationOwnerMissing))
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(r.config()); err != nil {
		return err
	}
	if err := writeNewPrivateFile(r.Spec.ConfigPath, out.Bytes()); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		data, err := os.ReadFile(r.Spec.ConfigPath)
		if err != nil || !bytes.Equal(data, out.Bytes()) {
			return fmt.Errorf("config already exists and differs from pending setup; no changes made")
		}
	}
	return nil
}

func printTeamInitReady(w io.Writer, r *teamInitReceipt) error {
	domain := "applink.feishu.cn"
	if r.Identity.Platform == "lark" {
		domain = "applink.larksuite.com"
	}
	i18n := core.NewI18n(core.NormalizeLanguageString(r.Spec.Language))
	if r.Version >= 2 {
		_, err := fmt.Fprintln(w, i18n.Tf(core.MsgTeamInitAutoReady, r.Spec.ConfigPath,
			"https://"+domain+"/client/bot/open?appId="+r.Identity.AppID))
		return err
	}
	_, err := fmt.Fprintln(w, i18n.Tf(core.MsgTeamInitReady, r.Spec.ConfigPath,
		"https://"+domain+"/client/bot/open?appId="+r.Identity.AppID, r.ClaimToken))
	return err
}

func cleanupTeamInitReceipt(configPath, botID string) error {
	path := configPath + ".setup.json"
	r, err := readTeamInitReceipt(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Spec.BotID != botID {
		return nil
	}
	return os.Remove(path)
}
