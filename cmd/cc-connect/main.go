package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	ccconnect "github.com/chenhg5/cc-connect"
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/daemon"

	// Agent and platform imports are in separate plugin_*.go files
	// controlled by build tags. See Makefile for selective compilation.
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "config-example":
			fmt.Print(ccconnect.ConfigExampleTOML)
			return
		case "update":
			runUpdate()
			return
		case "check-update":
			checkUpdate()
			return
		case "provider":
			runProviderCommand(os.Args[2:])
			return
		case "send":
			runSend(os.Args[2:])
			return
		case "cron":
			runCron(os.Args[2:])
			return
		case "relay":
			runRelay(os.Args[2:])
			return
		case "daemon":
			runDaemon(os.Args[2:])
			return
		}
	}

	// When started as a daemon (CC_LOG_FILE set), redirect logs to a rotating file.
	var logWriter io.Writer
	var logCloser io.Closer
	if logFile := os.Getenv("CC_LOG_FILE"); logFile != "" {
		maxSize := int64(daemon.DefaultLogMaxSize)
		if v := os.Getenv("CC_LOG_MAX_SIZE"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				maxSize = n
			}
		}
		w, err := daemon.NewRotatingWriter(logFile, maxSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", logFile, err)
			os.Exit(1)
		}
		logWriter = w
		logCloser = w
		slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	configFlag := flag.String("config", "", "path to config file (default: ./config.toml or ~/.cc-connect/config.toml)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = printUsage
	flag.Parse()

	if *showVersion {
		fmt.Printf("cc-connect %s\ncommit:  %s\nbuilt:   %s\n", version, commit, buildTime)
		return
	}

	core.VersionInfo = fmt.Sprintf("cc-connect %s\ncommit: %s\nbuilt: %s", version, commit, buildTime)
	core.CurrentVersion = version

	configPath := resolveConfigPath(*configFlag)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := bootstrapConfig(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created default config at %s\n", configPath)
		fmt.Println("Please edit this file to add your agent and platform credentials, then run cc-connect again.")
		os.Exit(0)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config (%s): %v\n", configPath, err)
		os.Exit(1)
	}

	config.ConfigPath = configPath
	slog.Info("config loaded", "path", configPath)

	setupLogger(cfg.Log.Level, logWriter)

	engines := make([]*core.Engine, 0, len(cfg.Projects))

	for _, proj := range cfg.Projects {
		agent, err := core.CreateAgent(proj.Agent.Type, proj.Agent.Options)
		if err != nil {
			slog.Error("failed to create agent", "project", proj.Name, "error", err)
			os.Exit(1)
		}

		// Wire providers if the agent supports it
		if ps, ok := agent.(core.ProviderSwitcher); ok && len(proj.Agent.Providers) > 0 {
			providers := make([]core.ProviderConfig, len(proj.Agent.Providers))
			for i, p := range proj.Agent.Providers {
				providers[i] = core.ProviderConfig{
					Name:     p.Name,
					APIKey:   p.APIKey,
					BaseURL:  p.BaseURL,
					Model:    p.Model,
					Thinking: p.Thinking,
					Env:      p.Env,
				}
			}
			ps.SetProviders(providers)
			if active, _ := proj.Agent.Options["provider"].(string); active != "" {
				ps.SetActiveProvider(active)
			}
		}

		var platforms []core.Platform
		for _, pc := range proj.Platforms {
			p, err := core.CreatePlatform(pc.Type, pc.Options)
			if err != nil {
				slog.Error("failed to create platform", "project", proj.Name, "type", pc.Type, "error", err)
				os.Exit(1)
			}
			platforms = append(platforms, p)
		}

		workDir, _ := proj.Agent.Options["work_dir"].(string)
		sessionFile := sessionStorePath(cfg.DataDir, proj.Name, workDir)

		// Parse language setting
		var lang core.Language
		switch cfg.Language {
		case "zh", "chinese":
			lang = core.LangChinese
		case "zh-TW", "zh_TW", "zhtw":
			lang = core.LangTraditionalChinese
		case "ja", "japanese":
			lang = core.LangJapanese
		case "es", "spanish":
			lang = core.LangSpanish
		case "en", "english":
			lang = core.LangEnglish
		default:
			lang = core.LangAuto // auto-detect
		}

		engine := core.NewEngine(proj.Name, agent, platforms, sessionFile, lang)

		// Wire multi-workspace mode
		if proj.Mode == "multi-workspace" {
			baseDir := proj.BaseDir
			if strings.HasPrefix(baseDir, "~/") {
				home, _ := os.UserHomeDir()
				baseDir = filepath.Join(home, baseDir[2:])
			}
			if err := os.MkdirAll(baseDir, 0o755); err != nil {
				slog.Error("failed to create base_dir", "path", baseDir, "err", err)
				continue
			}
			bindingStore := filepath.Join(cfg.DataDir, "workspace_bindings.json")
			engine.SetMultiWorkspace(baseDir, bindingStore)
			slog.Info("multi-workspace mode enabled", "project", proj.Name, "base_dir", baseDir)
		}

		// Wire global custom commands
		for _, c := range cfg.Commands {
			engine.AddCommand(c.Name, c.Description, c.Prompt, c.Exec, c.WorkDir, "config")
		}

		// Wire command persistence callbacks
		engine.SetCommandSaveAddFunc(func(name, description, prompt, exec, workDir string) error {
			return config.AddCommand(config.CommandConfig{Name: name, Description: description, Prompt: prompt, Exec: exec, WorkDir: workDir})
		})
		engine.SetCommandSaveDelFunc(func(name string) error {
			return config.RemoveCommand(name)
		})

		// Wire global aliases
		for _, a := range cfg.Aliases {
			engine.AddAlias(a.Name, a.Command)
		}
		engine.SetAliasSaveAddFunc(func(name, command string) error {
			return config.AddAlias(config.AliasConfig{Name: name, Command: command})
		})
		engine.SetAliasSaveDelFunc(func(name string) error {
			return config.RemoveAlias(name)
		})

		// Wire banned words
		if len(cfg.BannedWords) > 0 {
			engine.SetBannedWords(cfg.BannedWords)
		}

		// Wire disabled commands (project-level)
		if len(proj.DisabledCommands) > 0 {
			engine.SetDisabledCommands(proj.DisabledCommands)
		}

		// Wire admin allowlist for privileged commands
		engine.SetAdminFrom(proj.AdminFrom)

		// Wire display truncation settings
		{
			dcfg := core.DisplayCfg{
				ThinkingMaxLen: 300,
				ToolMaxLen:     500,
			}
			if cfg.Display.ThinkingMaxLen != nil {
				dcfg.ThinkingMaxLen = *cfg.Display.ThinkingMaxLen
			}
			if cfg.Display.ToolMaxLen != nil {
				dcfg.ToolMaxLen = *cfg.Display.ToolMaxLen
			}
			engine.SetDisplayConfig(dcfg)
		}

		// Wire streaming preview
		{
			spcfg := core.DefaultStreamPreviewCfg()
			if cfg.StreamPreview.Enabled != nil {
				spcfg.Enabled = *cfg.StreamPreview.Enabled
			}
			if cfg.StreamPreview.IntervalMs != nil {
				spcfg.IntervalMs = *cfg.StreamPreview.IntervalMs
			}
			if cfg.StreamPreview.MinDeltaChars != nil {
				spcfg.MinDeltaChars = *cfg.StreamPreview.MinDeltaChars
			}
			if cfg.StreamPreview.MaxChars != nil {
				spcfg.MaxChars = *cfg.StreamPreview.MaxChars
			}
			if cfg.StreamPreview.DisabledPlatforms != nil {
				spcfg.DisabledPlatforms = cfg.StreamPreview.DisabledPlatforms
			}
			engine.SetStreamPreviewCfg(spcfg)
		}

		// Wire rate limiting
		{
			maxMsg := 20
			windowSecs := 60
			if cfg.RateLimit.MaxMessages != nil {
				maxMsg = *cfg.RateLimit.MaxMessages
			}
			if cfg.RateLimit.WindowSecs != nil {
				windowSecs = *cfg.RateLimit.WindowSecs
			}
			if maxMsg > 0 {
				engine.SetRateLimitCfg(core.RateLimitCfg{
					MaxMessages: maxMsg,
					Window:      time.Duration(windowSecs) * time.Second,
				})
			}
		}
		engine.SetDisplaySaveFunc(func(thinkingMaxLen, toolMaxLen *int) error {
			return config.SaveDisplayConfig(thinkingMaxLen, toolMaxLen)
		})

		// Wire idle timeout
		if cfg.IdleTimeoutMins != nil {
			mins := *cfg.IdleTimeoutMins
			if mins <= 0 {
				engine.SetEventIdleTimeout(0)
			} else {
				engine.SetEventIdleTimeout(time.Duration(mins) * time.Minute)
			}
		}

		// Wire default quiet mode: project-level overrides global
		if proj.Quiet != nil {
			engine.SetDefaultQuiet(*proj.Quiet)
		} else if cfg.Quiet != nil {
			engine.SetDefaultQuiet(*cfg.Quiet)
		}

		// Wire sender injection
		if proj.InjectSender != nil {
			engine.SetInjectSender(*proj.InjectSender)
		}

		// Wire speech-to-text if enabled
		if cfg.Speech.Enabled {
			speechCfg := core.SpeechCfg{
				Enabled:  true,
				Language: cfg.Speech.Language,
			}
			switch cfg.Speech.Provider {
			case "groq":
				apiKey := cfg.Speech.Groq.APIKey
				model := cfg.Speech.Groq.Model
				if model == "" {
					model = "whisper-large-v3-turbo"
				}
				if apiKey != "" {
					speechCfg.STT = core.NewOpenAIWhisper(apiKey, "https://api.groq.com/openai/v1", model)
				} else {
					slog.Warn("speech: groq provider enabled but api_key is empty")
				}
			case "qwen":
				apiKey := cfg.Speech.Qwen.APIKey
				baseURL := cfg.Speech.Qwen.BaseURL
				model := cfg.Speech.Qwen.Model
				if apiKey != "" {
					speechCfg.STT = core.NewQwenASR(apiKey, baseURL, model)
				} else {
					slog.Warn("speech: qwen provider enabled but api_key is empty")
				}
			default: // "openai" or unspecified
				apiKey := cfg.Speech.OpenAI.APIKey
				baseURL := cfg.Speech.OpenAI.BaseURL
				model := cfg.Speech.OpenAI.Model
				if apiKey != "" {
					speechCfg.STT = core.NewOpenAIWhisper(apiKey, baseURL, model)
				} else {
					slog.Warn("speech: openai provider enabled but api_key is empty")
				}
			}
			if speechCfg.STT != nil {
				engine.SetSpeechConfig(speechCfg)
				slog.Info("speech: enabled", "provider", cfg.Speech.Provider)
			}
		}

		// Wire text-to-speech if enabled
		if cfg.TTS.Enabled {
			ttsCfg := &core.TTSCfg{
				Enabled:    true,
				Voice:      cfg.TTS.Voice,
				MaxTextLen: cfg.TTS.MaxTextLen,
			}
			initMode := cfg.TTS.TTSMode
			switch initMode {
			case "always", "voice_only":
			case "":
				initMode = "voice_only"
			default:
				slog.Warn("tts: invalid tts_mode in config, falling back to voice_only", "tts_mode", initMode)
				initMode = "voice_only"
			}
			ttsCfg.SetTTSMode(initMode)
			switch cfg.TTS.Provider {
			case "qwen":
				apiKey := cfg.TTS.Qwen.APIKey
				baseURL := cfg.TTS.Qwen.BaseURL
				model := cfg.TTS.Qwen.Model
				if apiKey != "" {
					ttsCfg.TTS = core.NewQwenTTS(apiKey, baseURL, model, nil)
					ttsCfg.Provider = "qwen"
				} else {
					slog.Warn("tts: qwen provider enabled but api_key is empty")
				}
			case "minimax":
				apiKey := cfg.TTS.MiniMax.APIKey
				baseURL := cfg.TTS.MiniMax.BaseURL
				model := cfg.TTS.MiniMax.Model
				if apiKey != "" {
					ttsCfg.TTS = core.NewMiniMaxTTS(apiKey, baseURL, model, nil)
					ttsCfg.Provider = "minimax"
				} else {
					slog.Warn("tts: minimax provider enabled but api_key is empty")
				}
			default: // "openai" or unspecified
				apiKey := cfg.TTS.OpenAI.APIKey
				baseURL := cfg.TTS.OpenAI.BaseURL
				model := cfg.TTS.OpenAI.Model
				if apiKey != "" {
					ttsCfg.TTS = core.NewOpenAITTS(apiKey, baseURL, model, nil)
					ttsCfg.Provider = "openai"
				} else {
					slog.Warn("tts: openai provider enabled but api_key is empty")
				}
			}
			if ttsCfg.TTS != nil {
				engine.SetTTSConfig(ttsCfg)
				engine.SetTTSSaveFunc(func(mode string) error {
					return config.SaveTTSMode(mode)
				})
				slog.Info("tts: enabled", "provider", ttsCfg.Provider, "voice", ttsCfg.Voice, "mode", initMode)
			}
		}

		// Set up save callback for auto-detected language
		if lang == core.LangAuto {
			engine.SetLanguageSaveFunc(func(l core.Language) error {
				return config.SaveLanguage(string(l))
			})
		}

		// Set up save callbacks for provider management
		projName := proj.Name
		engine.SetProviderSaveFunc(func(providerName string) error {
			return config.SaveActiveProvider(projName, providerName)
		})
		engine.SetProviderAddSaveFunc(func(p core.ProviderConfig) error {
			return config.AddProviderToConfig(projName, config.ProviderConfig{
				Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
				Model: p.Model, Thinking: p.Thinking, Env: p.Env,
			})
		})
		engine.SetProviderRemoveSaveFunc(func(name string) error {
			return config.RemoveProviderFromConfig(projName, name)
		})

		// Wire config reload
		capturedEngine := engine
		capturedProjName := projName
		engine.SetConfigReloadFunc(func() (*core.ConfigReloadResult, error) {
			return reloadConfig(configPath, capturedProjName, capturedEngine)
		})

		engines = append(engines, engine)
	}

	// Start cron scheduler
	cronStore, err := core.NewCronStore(cfg.DataDir)
	if err != nil {
		slog.Warn("cron store unavailable", "error", err)
	}
	var cronSched *core.CronScheduler
	if cronStore != nil {
		cronSched = core.NewCronScheduler(cronStore)
		if cfg.Cron.Silent != nil && *cfg.Cron.Silent {
			cronSched.SetDefaultSilent(true)
		}
		for i, e := range engines {
			cronSched.RegisterEngine(cfg.Projects[i].Name, e)
			e.SetCronScheduler(cronSched)
		}
	}

	// Start heartbeat scheduler
	heartbeatSched := core.NewHeartbeatScheduler(cfg.DataDir)
	for i, proj := range cfg.Projects {
		hbCfg := buildHeartbeatConfig(proj.Heartbeat)
		if hbCfg.Enabled {
			workDir, _ := proj.Agent.Options["work_dir"].(string)
			heartbeatSched.Register(proj.Name, hbCfg, engines[i], workDir)
		}
		engines[i].SetHeartbeatScheduler(heartbeatSched)
	}

	var startErrors []error
	for _, e := range engines {
		if err := e.Start(); err != nil {
			slog.Warn("engine start partially failed (some platforms may be unavailable)", "error", err)
			startErrors = append(startErrors, err)
		}
	}
	// Only exit if ALL engines failed to start
	if len(startErrors) > 0 && len(startErrors) == len(engines) {
		slog.Error("all engines failed to start, exiting")
		os.Exit(1)
	}

	if cronSched != nil {
		if err := cronSched.Start(); err != nil {
			slog.Error("cron scheduler start failed", "error", err)
		}
	}

	heartbeatSched.Start()

	// Start bridge server if enabled
	var bridgeSrv *core.BridgeServer
	if cfg.Bridge.Enabled != nil && *cfg.Bridge.Enabled {
		port := cfg.Bridge.Port
		if port <= 0 {
			port = 9810
		}
		path := cfg.Bridge.Path
		if path == "" {
			path = "/bridge/ws"
		}
		bridgeSrv = core.NewBridgeServer(port, cfg.Bridge.Token, path)
		for i, e := range engines {
			bp := bridgeSrv.NewPlatform(cfg.Projects[i].Name)
			bridgeSrv.RegisterEngine(cfg.Projects[i].Name, e, bp)
			e.AddPlatform(bp)
		}
		bridgeSrv.Start()
	}

	// Start webhook server if enabled
	var webhookSrv *core.WebhookServer
	if cfg.Webhook.Enabled != nil && *cfg.Webhook.Enabled {
		port := cfg.Webhook.Port
		if port <= 0 {
			port = 9111
		}
		path := cfg.Webhook.Path
		if path == "" {
			path = "/hook"
		}
		webhookSrv = core.NewWebhookServer(port, cfg.Webhook.Token, path)
		for i, e := range engines {
			webhookSrv.RegisterEngine(cfg.Projects[i].Name, e)
		}
		webhookSrv.Start()
	}

	// Start management API server if enabled
	var mgmtSrv *core.ManagementServer
	if cfg.Management.Enabled != nil && *cfg.Management.Enabled {
		port := cfg.Management.Port
		if port <= 0 {
			port = 9820
		}
		mgmtSrv = core.NewManagementServer(port, cfg.Management.Token, cfg.Management.CORSOrigins)
		for i, e := range engines {
			mgmtSrv.RegisterEngine(cfg.Projects[i].Name, e)
		}
		if cronSched != nil {
			mgmtSrv.SetCronScheduler(cronSched)
		}
		mgmtSrv.SetHeartbeatScheduler(heartbeatSched)
		if bridgeSrv != nil {
			mgmtSrv.SetBridgeServer(bridgeSrv)
		}
		mgmtSrv.Start()
	}

	// Start internal API server for CLI send
	apiSrv, err := core.NewAPIServer(cfg.DataDir)
	if err != nil {
		slog.Warn("api server unavailable", "error", err)
	} else {
		relayMgr := core.NewRelayManager(cfg.DataDir)
		apiSrv.SetRelayManager(relayMgr)
		for i, e := range engines {
			apiSrv.RegisterEngine(cfg.Projects[i].Name, e)
			e.SetRelayManager(relayMgr)
		}
		if cronSched != nil {
			apiSrv.SetCronScheduler(cronSched)
		}
		apiSrv.Start()
	}

	slog.Info("cc-connect is running", "projects", len(engines))

	// After startup, check if we were restarted and send success notification
	if notify := core.ConsumeRestartNotify(cfg.DataDir); notify != nil {
		slog.Info("post-restart: sending success notification", "platform", notify.Platform, "session", notify.SessionKey)
		for _, e := range engines {
			e.SendRestartNotification(notify.Platform, notify.SessionKey)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var restartReq *core.RestartRequest
	select {
	case <-sigCh:
	case req := <-core.RestartCh:
		restartReq = &req
		slog.Info("restart requested via /restart command", "session", req.SessionKey, "platform", req.Platform)
	}

	slog.Info("shutting down...")
	if mgmtSrv != nil {
		mgmtSrv.Stop()
	}
	if bridgeSrv != nil {
		bridgeSrv.Stop()
	}
	if webhookSrv != nil {
		webhookSrv.Stop()
	}
	heartbeatSched.Stop()
	if cronSched != nil {
		cronSched.Stop()
	}
	if apiSrv != nil {
		apiSrv.Stop()
	}
	for _, e := range engines {
		if err := e.Stop(); err != nil {
			slog.Error("shutdown error", "error", err)
		}
	}
	if logCloser != nil {
		logCloser.Close()
	}

	if restartReq != nil {
		if err := core.SaveRestartNotify(cfg.DataDir, *restartReq); err != nil {
			slog.Error("restart: save notify failed", "error", err)
		}
		execPath, err := os.Executable()
		if err != nil {
			slog.Error("restart: cannot determine executable path", "error", err)
			os.Exit(1)
		}
		// After self-update, os.Executable() may return the .old path on Linux.
		// Strip the .old suffix to restart from the updated binary.
		if strings.HasSuffix(execPath, ".old") {
			newPath := strings.TrimSuffix(execPath, ".old")
			if _, err := os.Stat(newPath); err == nil {
				execPath = newPath
			}
		}
		slog.Info("restarting...", "path", execPath, "args", os.Args)
		if err := restartProcess(execPath); err != nil {
			slog.Error("restart: failed", "error", err)
			os.Exit(1)
		}
	}

	slog.Info("bye")
}

// sessionStorePath builds a unique filename from project name + work_dir.
// It checks the local .cc-connect/ directory first for backward compatibility;
// if the file exists there, it is used. Otherwise falls back to dataDir/sessions/.
func sessionStorePath(dataDir, name, workDir string) string {
	var filename string
	if workDir == "" {
		filename = name + ".json"
	} else {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			abs = workDir
		}
		h := sha256.Sum256([]byte(abs))
		short := hex.EncodeToString(h[:4])
		filename = fmt.Sprintf("%s_%s.json", name, short)
	}

	// Check legacy local path: .cc-connect/<name>.json or .cc-connect/<name>.sessions.json
	for _, legacy := range []string{
		filepath.Join(".cc-connect", filename),
		filepath.Join(".cc-connect", strings.TrimSuffix(filename, ".json")+".sessions.json"),
	} {
		if _, err := os.Stat(legacy); err == nil {
			slog.Info("session: using local file", "path", legacy)
			return legacy
		}
	}

	return filepath.Join(dataDir, "sessions", filename)
}

// resolveConfigPath determines which config file to use.
// Priority: explicit flag → ./config.toml → ~/.cc-connect/config.toml
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return "config.toml"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect", "config.toml")
	}
	return "config.toml"
}

func bootstrapConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	const tmpl = `# cc-connect configuration
# Docs: https://github.com/chenhg5/cc-connect

[log]
level = "info"

[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"   # "claudecode", "codex", "cursor", "gemini", "qoder", "opencode", or "iflow"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"
# model = "claude-sonnet-4-20250514"

# --- Choose at least one platform below ---

# Feishu / Lark (WebSocket, no public IP needed)
[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "your-feishu-app-id"
app_secret = "your-feishu-app-secret"

# For more platforms (DingTalk, Telegram, Slack, Discord, LINE, WeChat Work)
# see: https://github.com/chenhg5/cc-connect/blob/main/config.example.toml
`
	return os.WriteFile(path, []byte(tmpl), 0o644)
}

func printUsage() {
	v := version
	if v == "" || v == "dev" {
		v = "dev"
	}
	fmt.Fprintf(os.Stderr, `
                                              _
  ___ ___        ___ ___  _ __  _ __   ___  ___| |_
 / __/ __|_____ / __/ _ \| '_ \| '_ \ / _ \/ __| __|
| (_| (_|_____|  (_| (_) | | | | | | |  __/ (__| |_
 \___\__|      \___\___/|_| |_|_| |_|\___|\___|\__|  %s

  Bridge your messaging platforms to local AI coding agents.
  Supports: Claude Code, Codex, Cursor, Gemini CLI, Qoder CLI, OpenCode
  Platforms: Feishu, Telegram, Slack, DingTalk, Discord, LINE, WeChat Work, QQ, QQ Bot

  GitHub:  https://github.com/chenhg5/cc-connect
  Docs:    https://github.com/chenhg5/cc-connect/blob/main/INSTALL.md

Usage:
  cc-connect [flags]
  cc-connect <command> [args]

Flags:
  --config <path>    Path to config file (default: ./config.toml or ~/.cc-connect/config.toml)
  --version          Print version and exit
  --help             Show this help message

Commands:
  daemon             Manage cc-connect as a background service (systemd/launchd)
    install          Install and start the daemon service
    uninstall        Remove the daemon service
    start            Start the daemon
    stop             Stop the daemon
    restart          Restart the daemon
    status           Show daemon status
    logs             View daemon logs (-f to follow, -n N for last N lines)

  send               Send a message to an active session via internal API
                     (-m <text> | --stdin, -p <project>, -s <session>)

  cron               Manage scheduled tasks
    add              Create a scheduled task (-c <expr> --prompt <text>)
    list             List scheduled tasks
    del              Delete a scheduled task by ID

  relay              Cross-project message relay
    send             Send a message to another project and get the response

  provider           Manage API providers for projects
    add              Add a provider (--project, --name, --api-key, ...)
    list             List providers (--project)
    remove           Remove a provider (--project, --name)
    import           Import providers from cc-switch

  update             Check for updates and upgrade the binary (--pre for beta)
  check-update       Check if a newer version is available
  config-example     Print a complete annotated config.toml example

Examples:
  cc-connect                          Start with default config
  cc-connect --config /path/to.toml   Start with a specific config file
  cc-connect daemon install           Install as a system service
  cc-connect daemon logs -f           Follow daemon logs
  cc-connect send -m "hello"          Send a message to the active session
  cc-connect cron list                List all scheduled tasks
  cc-connect update                   Update to the latest version
  cc-connect config-example           Print full config.toml example
  cc-connect config-example > c.toml  Save example config to a file

`, v)
}

func setupLogger(level string, w io.Writer) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	if w == nil {
		w = os.Stdout
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: logLevel,
	})))
}

// reloadConfig re-reads config.toml and applies hot-reloadable settings
// (display, providers, commands) to the given engine.
func reloadConfig(configPath, projName string, engine *core.Engine) (*core.ConfigReloadResult, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("reload config: %w", err)
	}

	result := &core.ConfigReloadResult{}

	// Find the matching project
	var proj *config.ProjectConfig
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == projName {
			proj = &cfg.Projects[i]
			break
		}
	}
	if proj == nil {
		return nil, fmt.Errorf("project %q not found in config", projName)
	}

	// Reload display config
	dcfg := core.DisplayCfg{ThinkingMaxLen: 300, ToolMaxLen: 500}
	if cfg.Display.ThinkingMaxLen != nil {
		dcfg.ThinkingMaxLen = *cfg.Display.ThinkingMaxLen
	}
	if cfg.Display.ToolMaxLen != nil {
		dcfg.ToolMaxLen = *cfg.Display.ToolMaxLen
	}
	engine.SetDisplayConfig(dcfg)
	result.DisplayUpdated = true

	// Reload default quiet mode
	if proj.Quiet != nil {
		engine.SetDefaultQuiet(*proj.Quiet)
	} else if cfg.Quiet != nil {
		engine.SetDefaultQuiet(*cfg.Quiet)
	} else {
		engine.SetDefaultQuiet(false)
	}

	// Reload sender injection
	engine.SetInjectSender(proj.InjectSender != nil && *proj.InjectSender)

	// Reload providers
	if ps, ok := engine.GetAgent().(core.ProviderSwitcher); ok {
		providers := make([]core.ProviderConfig, len(proj.Agent.Providers))
		for i, p := range proj.Agent.Providers {
			providers[i] = core.ProviderConfig{
				Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
				Model: p.Model, Thinking: p.Thinking, Env: p.Env,
			}
		}
		ps.SetProviders(providers)
		result.ProvidersUpdated = len(providers)

		if active, _ := proj.Agent.Options["provider"].(string); active != "" {
			ps.SetActiveProvider(active)
		}
	}

	// Reload custom commands
	engine.ClearCommands("config")
	for _, c := range cfg.Commands {
		engine.AddCommand(c.Name, c.Description, c.Prompt, c.Exec, c.WorkDir, "config")
	}
	result.CommandsUpdated = len(cfg.Commands)

	// Reload aliases
	engine.ClearAliases()
	for _, a := range cfg.Aliases {
		engine.AddAlias(a.Name, a.Command)
	}

	// Reload banned words
	engine.SetBannedWords(cfg.BannedWords)

	// Reload disabled commands
	engine.SetDisabledCommands(proj.DisabledCommands)

	// Reload admin allowlist
	engine.SetAdminFrom(proj.AdminFrom)

	slog.Info("config reloaded", "project", projName)
	return result, nil
}

func buildHeartbeatConfig(hc config.HeartbeatConfig) core.HeartbeatConfig {
	cfg := core.HeartbeatConfig{
		IntervalMins: 30,
		OnlyWhenIdle: true,
		Silent:       true,
		TimeoutMins:  30,
		SessionKey:   hc.SessionKey,
		Prompt:       hc.Prompt,
	}
	if hc.Enabled != nil {
		cfg.Enabled = *hc.Enabled
	}
	if hc.IntervalMins != nil {
		cfg.IntervalMins = *hc.IntervalMins
	}
	if hc.OnlyWhenIdle != nil {
		cfg.OnlyWhenIdle = *hc.OnlyWhenIdle
	}
	if hc.Silent != nil {
		cfg.Silent = *hc.Silent
	}
	if hc.TimeoutMins != nil {
		cfg.TimeoutMins = *hc.TimeoutMins
	}
	return cfg
}
