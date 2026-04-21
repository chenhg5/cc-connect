package main

import (
	"context"
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

type initialModelRefreshStarter interface {
	StartInitialModelRefresh()
}

type providerWiringResult struct {
	explicitProviderRequested bool
	activeProviderApplied     bool
	canStartInitialRefresh    bool
}

func main() {
	checkUpdateAsync()

	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "config-example":
			fmt.Print(ccconnect.ConfigExampleTOML)
			return
		case "config":
			runConfig(os.Args[2:])
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
		case "sessions":
			runSessions(os.Args[2:])
			return
		case "agent-sid":
			runAgentSID(os.Args[2:])
			return
		case "daemon":
			runDaemon(os.Args[2:])
			return
		case "feishu":
			runFeishu(os.Args[2:])
			return
		case "weixin":
			runWeixin(os.Args[2:])
			return
		case "doctor":
			runDoctor(os.Args[2:])
			return
		case "web":
			runWeb(os.Args[2:])
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
	observeFlag := flag.Bool("observe", false, "observe native terminal Claude Code sessions and forward to Slack")
	observeChannel := flag.String("observe-channel", "", "Slack channel ID to forward terminal observations to (requires --observe)")
	forceFlag := flag.Bool("force", false, "kill any existing instance with the same config before starting")
	flag.Usage = printUsage
	flag.Parse()

	if *showVersion {
		fmt.Printf("cc-connect %s\ncommit:  %s\nbuilt:   %s\n", version, commit, buildTime)
		return
	}

	core.VersionInfo = fmt.Sprintf("cc-connect %s\ncommit: %s\nbuilt: %s", version, commit, buildTime)
	core.CurrentVersion = version
	core.CurrentCommit = commit
	core.CurrentBuildTime = buildTime

	configPath := resolveConfigPath(*configFlag)

	// Handle --force: kill any existing instance before we try to acquire the lock
	if *forceFlag {
		if KillExistingInstance(configPath) {
			slog.Info("killed existing instance via --force")
		}
	}

	// Acquire instance lock to prevent duplicate processes
	instanceLock, err := AcquireInstanceLock(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Use --force to kill the existing instance.\n")
		os.Exit(1)
	}
	slog.Info("acquired instance lock", "path", instanceLock.Path())

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

	// run_as_user preflight + isolation audit. MUST run before any engine
	// or agent is constructed. If any project fails, abort startup
	// entirely — never half-spawn. See core/runas_check.go and
	// core/runas_audit.go for the checks themselves.
	if err := runRunAsUserStartupChecks(context.Background(), cfg); err != nil {
		slog.Error("run_as_user: startup checks failed, refusing to start", "error", err)
		os.Exit(1)
	}

	engines := make([]*core.Engine, 0, len(cfg.Projects))
	effectiveWorkDirs := make([]string, 0, len(cfg.Projects))

	for _, proj := range cfg.Projects {
		// Inject project-level run_as_user / run_as_env into the agent's
		// opts map so agents that support isolation can pick them up
		// without needing their own top-level config plumbing.
		if proj.RunAsUser != "" {
			if proj.Agent.Options == nil {
				proj.Agent.Options = map[string]any{}
			}
			proj.Agent.Options["run_as_user"] = proj.RunAsUser
			if len(proj.RunAsEnv) > 0 {
				proj.Agent.Options["run_as_env"] = proj.RunAsEnv
			}
		}
		agent, err := core.CreateAgent(proj.Agent.Type, buildAgentOptions(cfg.DataDir, proj))
		if err != nil {
			slog.Error("failed to create agent", "project", proj.Name, "error", err)
			os.Exit(1)
		}

		providerWiring := wireAgentProviders(agent, proj.Agent)

		var platforms []core.Platform
		for _, pc := range proj.Platforms {
			opts := make(map[string]any, len(pc.Options)+2)
			for k, v := range pc.Options {
				opts[k] = v
			}
			opts["cc_data_dir"] = cfg.DataDir
			opts["cc_project"] = proj.Name
			p, err := core.CreatePlatform(pc.Type, opts)
			if err != nil {
				slog.Error("failed to create platform", "project", proj.Name, "type", pc.Type, "error", err)
				os.Exit(1)
			}
			platforms = append(platforms, p)
		}

		workDir, _ := proj.Agent.Options["work_dir"].(string)
		projectState := core.NewProjectStateStore(projectStatePath(cfg.DataDir, proj.Name))
		effectiveWorkDir := applyProjectStateOverride(proj.Name, agent, workDir, projectState)
		startInitialRefreshIfReady(agent, providerWiring)
		sessionFile := sessionStorePath(cfg.DataDir, proj.Name, effectiveWorkDir)

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
		showCtx := true
		if proj.ShowContextIndicator != nil {
			showCtx = *proj.ShowContextIndicator
		}
		engine.SetShowContextIndicator(showCtx)
		showFooter := true
		if proj.ReplyFooter != nil {
			showFooter = *proj.ReplyFooter
		}
		engine.SetReplyFooterEnabled(showFooter)
		engine.SetAttachmentSendEnabled(cfg.AttachmentSend != "off")
		engine.SetFilterExternalSessions(proj.FilterExternalSessions != nil && *proj.FilterExternalSessions)
		engine.SetBaseWorkDir(workDir)
		engine.SetProjectStateStore(projectState)

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

		// Wire terminal observation (--observe / [projects.observe])
		observeEnabled := *observeFlag
		obsChan := *observeChannel
		if proj.Observe != nil {
			if !observeEnabled && proj.Observe.Enabled {
				observeEnabled = true
			}
			if obsChan == "" && proj.Observe.Channel != "" {
				obsChan = proj.Observe.Channel
			}
		}
		if observeEnabled {
			if obsChan == "" {
				slog.Error("observe: channel is required (use --observe-channel or set channel in [projects.observe])")
				os.Exit(1)
			}
			hasSlack := false
			for _, p := range platforms {
				if p.Name() == "slack" {
					hasSlack = true
					break
				}
			}
			if !hasSlack {
				slog.Warn("observe requires a Slack platform; ignoring")
			} else {
				projectDir := resolveClaudeProjectDir(workDir)
				if projectDir == "" {
					slog.Warn("observe: could not find Claude Code project directory", "workDir", workDir)
				} else {
					sessionKey := fmt.Sprintf("slack:%s", obsChan)
					engine.SetObserveConfig(projectDir, sessionKey)
				}
			}
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

		// Wire per-user role-based policies
		if proj.Users != nil {
			engine.SetUserRoles(buildUserRoleManager(proj.Users))
		}

		// Wire display truncation settings (includes legacy quiet → display mapping)
		{
			tm, tool, tmlen, toollen := config.EffectiveDisplay(cfg, &proj)
			engine.SetDisplayConfig(core.DisplayCfg{
				ThinkingMessages: tm,
				ThinkingMaxLen:   tmlen,
				ToolMaxLen:       toollen,
				ToolMessages:     tool,
			})
		}

		// Wire hooks
		if len(cfg.Hooks) > 0 {
			coreHooks := make([]core.HookConfig, len(cfg.Hooks))
			for i, h := range cfg.Hooks {
				coreHooks[i] = core.HookConfig{
					Event:   h.Event,
					Type:    h.Type,
					Command: h.Command,
					URL:     h.URL,
					Timeout: h.Timeout,
					Async:   h.Async,
				}
			}
			engine.SetHooks(core.NewHookManager(proj.Name, coreHooks))
		}

		// Wire local reference normalization / rendering
		engine.SetReferenceConfig(core.ReferenceRenderCfg{
			NormalizeAgents: proj.References.NormalizeAgents,
			RenderPlatforms: proj.References.RenderPlatforms,
			DisplayPath:     proj.References.DisplayPath,
			MarkerStyle:     proj.References.MarkerStyle,
			EnclosureStyle:  proj.References.EnclosureStyle,
		})

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
		// Wire outgoing rate limiting
		{
			var maxPS float64
			if cfg.OutgoingRateLimit.MaxPerSecond != nil {
				maxPS = *cfg.OutgoingRateLimit.MaxPerSecond
			}
			var burst int
			if cfg.OutgoingRateLimit.Burst != nil {
				burst = *cfg.OutgoingRateLimit.Burst
			}
			defaults := core.OutgoingRateLimitCfg{MaxPerSecond: maxPS, Burst: burst}
			overrides := make(map[string]core.OutgoingRateLimitCfg)
			for name, pc := range cfg.OutgoingRateLimit.Platforms {
				var mps float64
				if pc.MaxPerSecond != nil {
					mps = *pc.MaxPerSecond
				}
				var b int
				if pc.Burst != nil {
					b = *pc.Burst
				}
				overrides[name] = core.OutgoingRateLimitCfg{MaxPerSecond: mps, Burst: b}
			}
			if maxPS > 0 || len(overrides) > 0 {
				engine.SetOutgoingRateLimitCfg(defaults, overrides)
			}
		}

		engine.SetDisplaySaveFunc(func(thinkingMessages *bool, thinkingMaxLen, toolMaxLen *int, toolMessages *bool) error {
			return config.SaveDisplayConfig(thinkingMessages, thinkingMaxLen, toolMaxLen, toolMessages)
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

		// Wire auto-compress settings
		if proj.AutoCompress.Enabled != nil && *proj.AutoCompress.Enabled {
			minGap := 30 * time.Minute
			if proj.AutoCompress.MinGapMins != nil {
				minGap = time.Duration(*proj.AutoCompress.MinGapMins) * time.Minute
			}
			maxTokens := derefInt(proj.AutoCompress.MaxTokens)
			if maxTokens <= 0 {
				maxTokens = 12000
			}
			engine.SetAutoCompressConfig(true, maxTokens, minGap)
		}
		if proj.ResetOnIdleMins != nil {
			engine.SetResetOnIdle(time.Duration(*proj.ResetOnIdleMins) * time.Minute)
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
			case "gemini":
				apiKey := cfg.Speech.Gemini.APIKey
				model := cfg.Speech.Gemini.Model
				if apiKey != "" {
					speechCfg.STT = core.NewGeminiSTT(apiKey, model)
				} else {
					slog.Warn("speech: gemini provider enabled but api_key is empty")
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
			case "espeak":
				voice := cfg.TTS.Voice
				if voice == "" {
					voice = "zh" // default to Chinese
				}
				ttsCfg.TTS = core.NewEspeakTTS("", voice)
				ttsCfg.Provider = "espeak"
			case "pico":
				voice := cfg.TTS.Voice
				if voice == "" {
					voice = "zh-CN" // default to Chinese (Simplified)
				}
				ttsCfg.TTS = core.NewPicoTTS("", voice)
				ttsCfg.Provider = "pico"
			case "edge":
				voice := cfg.TTS.Voice
				if voice == "" {
					voice = "zh-CN-XiaoxiaoNeural" // default Chinese neural voice
				}
				ttsCfg.TTS = core.NewEdgeTTS(voice)
				ttsCfg.Provider = "edge"
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
			cp := config.ProviderConfig{
				Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
				Model: p.Model, Models: convertCoreModels(p.Models), Thinking: p.Thinking, Env: p.Env,
			}
			if p.CodexWireAPI != "" || len(p.CodexHTTPHeaders) > 0 {
				cp.Codex = &config.CodexProviderConfig{
					WireAPI: p.CodexWireAPI, HTTPHeaders: p.CodexHTTPHeaders,
				}
			}
			return config.AddProviderToConfig(projName, cp)
		})
		engine.SetProviderRemoveSaveFunc(func(name string) error {
			return config.RemoveProviderFromConfig(projName, name)
		})
		engine.SetProviderModelSaveFunc(func(providerName, model string) error {
			return config.SaveProviderModel(projName, providerName, model)
		})
		engine.SetProviderRefsSaveFunc(func(refs []string) error {
			return config.SaveProviderRefs(projName, refs)
		})
		engine.SetListGlobalProvidersFunc(func(agentType string) ([]core.ProviderConfig, error) {
			globals, err := config.ListGlobalProviders()
			if err != nil {
				return nil, err
			}
			var result []core.ProviderConfig
			for _, g := range globals {
				if len(g.AgentTypes) > 0 && !containsString(g.AgentTypes, agentType) {
					continue
				}
				result = append(result, configProviderToCore(g.ResolveForAgent(agentType)))
			}
			return result, nil
		})
		engine.SetModelSaveFunc(func(model string) error {
			return config.SaveAgentModel(projName, model)
		})

		// Wire config reload
		capturedEngine := engine
		capturedProjName := projName
		engine.SetConfigReloadFunc(func() (*core.ConfigReloadResult, error) {
			return reloadConfig(configPath, capturedProjName, capturedEngine)
		})

		// Wire /web command callbacks
		engine.SetWebSetupFunc(func() (int, string, bool, error) {
			mgmtToken := core.GenerateToken(16)
			bridgeToken := core.GenerateToken(16)
			result, err := config.EnableWebAdmin(mgmtToken, bridgeToken)
			if err != nil {
				return 0, "", false, err
			}
			return result.ManagementPort, result.ManagementToken, !result.AlreadyEnabled, nil
		})
		engine.SetWebStatusFunc(func() string {
			if cfg.Management.Enabled == nil || !*cfg.Management.Enabled {
				return ""
			}
			port := cfg.Management.Port
			if port == 0 {
				port = 9820
			}
			return fmt.Sprintf("http://localhost:%d", port)
		})

		engines = append(engines, engine)
		effectiveWorkDirs = append(effectiveWorkDirs, effectiveWorkDir)
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
		if cfg.Cron.SessionMode != "" {
			cronSched.SetDefaultSessionMode(cfg.Cron.SessionMode)
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
			heartbeatSched.Register(proj.Name, hbCfg, engines[i], effectiveWorkDirs[i])
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
		bridgeSrv = core.NewBridgeServer(port, cfg.Bridge.Token, path, cfg.Bridge.CORSOrigins)
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
		mgmtSrv.SetSetupFeishuSave(func(req core.FeishuSetupSaveRequest) error {
			platType := req.PlatformType
			if platType == "" {
				platType = "feishu"
			}
			_, err := config.EnsureProjectWithFeishuPlatform(config.EnsureProjectWithFeishuOptions{
				ProjectName:  req.ProjectName,
				PlatformType: platType,
				WorkDir:      req.WorkDir,
				AgentType:    req.AgentType,
			})
			if err != nil {
				return fmt.Errorf("ensure project: %w", err)
			}
			_, err = config.SaveFeishuPlatformCredentials(config.FeishuCredentialUpdateOptions{
				ProjectName:       req.ProjectName,
				PlatformType:      platType,
				AppID:             req.AppID,
				AppSecret:         req.AppSecret,
				OwnerOpenID:       req.OwnerOpenID,
				SetAllowFromEmpty: true,
			})
			return err
		})
		mgmtSrv.SetSetupWeixinSave(func(req core.WeixinSetupSaveRequest) error {
			_, err := config.EnsureProjectWithWeixinPlatform(config.EnsureProjectWithWeixinOptions{
				ProjectName: req.ProjectName,
				WorkDir:     req.WorkDir,
				AgentType:   req.AgentType,
			})
			if err != nil {
				return fmt.Errorf("ensure project: %w", err)
			}
			_, err = config.SaveWeixinPlatformCredentials(config.WeixinCredentialUpdateOptions{
				ProjectName:       req.ProjectName,
				Token:             req.Token,
				BaseURL:           req.BaseURL,
				AccountID:         req.IlinkBotID,
				ScannedUserID:     req.IlinkUserID,
				SetAllowFromEmpty: true,
			})
			return err
		})
		mgmtSrv.SetAddPlatformToProject(func(projectName, platType string, opts map[string]any, workDir, agentType string) error {
			if opts == nil {
				opts = map[string]any{}
			}
			return config.AddPlatformToProject(projectName, config.PlatformConfig{Type: platType, Options: opts}, workDir, agentType)
		})
		mgmtSrv.SetRemoveProject(config.RemoveProject)
		mgmtSrv.SetSaveProjectSettings(func(name string, u core.ProjectSettingsUpdate) error {
			return config.SaveProjectSettings(name, config.ProjectSettingsUpdate{
				Language:             u.Language,
				AdminFrom:            u.AdminFrom,
				DisabledCommands:     u.DisabledCommands,
				WorkDir:              u.WorkDir,
				Mode:                 u.Mode,
				AgentType:            u.AgentType,
				ShowContextIndicator: u.ShowContextIndicator,
				ReplyFooter:          u.ReplyFooter,
				InjectSender:         u.InjectSender,
				PlatformAllowFrom:    u.PlatformAllowFrom,
			})
		})
		mgmtSrv.SetGetProjectConfig(config.GetProjectConfigDetails)
		mgmtSrv.SetSaveProviderRefs(config.SaveProviderRefs)
		mgmtSrv.SetConfigFilePath(configPath)
		mgmtSrv.SetGetGlobalSettings(config.GetGlobalSettings)
		mgmtSrv.SetSaveGlobalSettings(func(updates map[string]any) error {
			u := config.GlobalSettingsUpdate{}
			if v, ok := updates["language"].(string); ok {
				u.Language = &v
			}
			if v, ok := updates["attachment_send"].(string); ok {
				u.AttachmentSend = &v
			}
			if v, ok := updates["log_level"].(string); ok {
				u.LogLevel = &v
			}
			if v, ok := updates["idle_timeout_mins"].(float64); ok {
				iv := int(v)
				u.IdleTimeoutMins = &iv
			}
			if v, ok := updates["thinking_messages"].(bool); ok {
				u.ThinkingMessages = &v
			}
			if v, ok := updates["thinking_max_len"].(float64); ok {
				iv := int(v)
				u.ThinkingMaxLen = &iv
			}
			if v, ok := updates["tool_messages"].(bool); ok {
				u.ToolMessages = &v
			}
			if v, ok := updates["tool_max_len"].(float64); ok {
				iv := int(v)
				u.ToolMaxLen = &iv
			}
			if v, ok := updates["stream_preview_enabled"].(bool); ok {
				u.StreamPreviewOn = &v
			}
			if v, ok := updates["stream_preview_interval_ms"].(float64); ok {
				iv := int(v)
				u.StreamPreviewIntMs = &iv
			}
			if v, ok := updates["rate_limit_max_messages"].(float64); ok {
				iv := int(v)
				u.RateLimitMax = &iv
			}
			if v, ok := updates["rate_limit_window_secs"].(float64); ok {
				iv := int(v)
				u.RateLimitWindow = &iv
			}
			return config.SaveGlobalSettings(u)
		})
		mgmtSrv.SetListGlobalProviders(func() ([]core.GlobalProviderInfo, error) {
			providers, err := config.ListGlobalProviders()
			if err != nil {
				return nil, err
			}
			out := make([]core.GlobalProviderInfo, len(providers))
			for i, p := range providers {
				out[i] = configProviderToGlobal(p)
			}
			return out, nil
		})
		mgmtSrv.SetAddGlobalProvider(func(info core.GlobalProviderInfo) error {
			return config.AddGlobalProvider(globalProviderToConfig(info))
		})
		mgmtSrv.SetUpdateGlobalProvider(func(name string, info core.GlobalProviderInfo) error {
			return config.UpdateGlobalProvider(name, globalProviderToConfig(info))
		})
		mgmtSrv.SetRemoveGlobalProvider(func(name string) error {
			return config.RemoveGlobalProvider(name)
		})
		mgmtSrv.SetFetchPresets(core.FetchProviderPresets)
		mgmtSrv.SetFetchSkillPresets(core.FetchSkillPresets)
		if cfg.ProviderPresetsURL != "" {
			core.SetPresetsURL(cfg.ProviderPresetsURL)
		}
		mgmtSrv.SetListCCSwitchProviders(listCCSwitchProvidersForWeb)
		mgmtSrv.Start()
	}

	// Start internal API server for CLI send
	apiSrv, err := core.NewAPIServer(cfg.DataDir)
	if err != nil {
		slog.Warn("api server unavailable", "error", err)
	} else {
		relayMgr := core.NewRelayManager(cfg.DataDir)
		if cfg.Relay.TimeoutSecs != nil {
			secs := *cfg.Relay.TimeoutSecs
			if secs <= 0 {
				relayMgr.SetTimeout(0)
			} else {
				relayMgr.SetTimeout(time.Duration(secs) * time.Second)
			}
		}
		apiSrv.SetRelayManager(relayMgr)

		// Create shared DirHistory for all engines
		dirHistory := core.NewDirHistory(cfg.DataDir)

		for i, e := range engines {
			apiSrv.RegisterEngine(cfg.Projects[i].Name, e)
			e.SetRelayManager(relayMgr)
			e.SetDirHistory(dirHistory)

			// Ensure initial work_dir is in history
			if initWorkDir := effectiveWorkDirs[i]; initWorkDir != "" {
				if !dirHistory.Contains(cfg.Projects[i].Name, initWorkDir) {
					dirHistory.Add(cfg.Projects[i].Name, initWorkDir)
				}
			}
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
	instanceLock.Release()

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
// It checks for legacy session files (without the sessions/ subdirectory) in dataDir
// for backward compatibility; if found, uses that path. Otherwise uses dataDir/sessions/.
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

	// Check legacy path in dataDir (without sessions/ subdirectory) for backward compatibility.
	// Also check for the older .sessions.json naming convention.
	for _, legacy := range []string{
		filepath.Join(dataDir, filename),
		filepath.Join(dataDir, strings.TrimSuffix(filename, ".json")+".sessions.json"),
	} {
		if _, err := os.Stat(legacy); err == nil {
			slog.Info("session: using legacy file in dataDir", "path", legacy)
			return legacy
		}
	}

	return filepath.Join(dataDir, "sessions", filename)
}

func projectStatePath(dataDir, projectName string) string {
	replacer := strings.NewReplacer(
		"\\", "_",
		"/", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	name := strings.TrimSpace(projectName)
	name = replacer.Replace(name)
	if name == "" {
		name = "project"
	}
	return filepath.Join(dataDir, "projects", name+".state.json")
}

func applyProjectStateOverride(projectName string, agent core.Agent, configuredWorkDir string, store *core.ProjectStateStore) string {
	effectiveWorkDir := configuredWorkDir
	if store == nil {
		return effectiveWorkDir
	}

	switcher, ok := agent.(core.WorkDirSwitcher)
	if !ok {
		return effectiveWorkDir
	}

	override := store.WorkDirOverride()
	if override == "" {
		return effectiveWorkDir
	}
	if abs, err := filepath.Abs(override); err == nil {
		override = abs
	}

	info, err := os.Stat(override)
	if err != nil || !info.IsDir() {
		slog.Warn("project_state: ignoring invalid work_dir override", "project", projectName, "work_dir", override)
		return effectiveWorkDir
	}

	switcher.SetWorkDir(override)
	slog.Info("project_state: applied work_dir override", "project", projectName, "work_dir", override)
	return override
}

// resolveClaudeProjectDir returns the Claude Code project directory for a given
// work directory, or "" if it doesn't exist.
func resolveClaudeProjectDir(workDir string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Claude Code encodes paths by replacing os.PathSeparator with "-"
	// e.g. /home/leigh/workspace/cc-connect -> -home-leigh-workspace-cc-connect
	encoded := strings.ReplaceAll(workDir, string(os.PathSeparator), "-")
	dir := filepath.Join(homeDir, ".claude", "projects", encoded)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
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

	// 检查是否有新版本可用并显示提示
	updateHint := getUpdateHintIfAvailable()

	fmt.Fprintf(os.Stderr, `
                                              _
  ___ ___        ___ ___  _ __  _ __   ___  ___| |_
 / __/ __|_____ / __/ _ \| '_ \| '_ \ / _ \/ __| __|
| (_| (_|_____|  (_| (_) | | | | | | |  __/ (__| |_
 \___\__|      \___\___/|_| |_|_| |_|\___|\___|\__|  %s%s

  Bridge your messaging platforms to local AI coding agents.
  Supports: Claude Code, Codex, Cursor, Gemini CLI, Qoder CLI, OpenCode
  Platforms: Feishu, Telegram, Slack, DingTalk, Discord, LINE, WeChat Work, Weixin, QQ, QQ Bot

  GitHub:  https://github.com/chenhg5/cc-connect
  Docs:    https://github.com/chenhg5/cc-connect/blob/main/INSTALL.md

Usage:
  cc-connect [flags]
  cc-connect <command> [args]

Flags:
  --config <path>    Path to config file (default: ./config.toml or ~/.cc-connect/config.toml)
  --force            Kill any existing instance with the same config before starting
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

  sessions           Browse session history
    list             List all sessions (pipe-friendly)
    show <id>        Show session messages (-n N for last N)

  agent-sid          Print the agent session ID for the current session

  relay              Cross-project message relay
    send             Send a message to another project and get the response

  provider           Manage API providers for projects
    add              Add a provider (--project, --name, --api-key, ...)
    list             List providers (--project)
    remove           Remove a provider (--project, --name)
    import           Import providers from cc-switch

  feishu             Setup Feishu/Lark bot credentials
    setup            Smart setup (QR create or bind when --app is provided)
    new              Force QR onboarding to create a new bot
    bind             Bind existing app_id/app_secret

  weixin             Setup Weixin personal (ilink) via QR or token
    setup            QR login, or bind when --token is provided
    new              Force QR login
    bind             Bind existing ilink bot token

  config             Manage configuration
    example          Print a complete annotated config.toml example
    format           Format the config file (alias: fmt)
    path             Print the resolved config file path

  update             Check for updates and upgrade the binary (--pre for beta)
  check-update       Check if a newer version is available
  config-example     (deprecated: use 'config example' instead)

Examples:
  cc-connect                          Start with default config
  cc-connect --config /path/to.toml   Start with a specific config file
  cc-connect daemon install           Install as a system service
  cc-connect daemon logs -f           Follow daemon logs
  cc-connect send -m "hello"          Send a message to the active session
  cc-connect cron list                List all scheduled tasks
  cc-connect feishu setup             Setup Feishu/Lark bot credentials
  cc-connect weixin setup             Setup Weixin (ilink) with QR or --token
  cc-connect update                   Update to the latest version
  cc-connect config format            Format the config file
  cc-connect config example > c.toml  Save example config to a file

`, v, updateHint)
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

	// Reload display config (includes legacy quiet → display mapping)
	tm, tool, tmlen, toollen := config.EffectiveDisplay(cfg, proj)
	engine.SetDisplayConfig(core.DisplayCfg{
		ThinkingMessages: tm,
		ThinkingMaxLen:   tmlen,
		ToolMaxLen:       toollen,
		ToolMessages:     tool,
	})
	result.DisplayUpdated = true

	// Reload auto-compress settings
	if proj.AutoCompress.Enabled != nil && *proj.AutoCompress.Enabled {
		minGap := 30 * time.Minute
		if proj.AutoCompress.MinGapMins != nil {
			minGap = time.Duration(*proj.AutoCompress.MinGapMins) * time.Minute
		}
		maxTokens := derefInt(proj.AutoCompress.MaxTokens)
		if maxTokens <= 0 {
			maxTokens = 12000
		}
		engine.SetAutoCompressConfig(true, maxTokens, minGap)
	} else {
		engine.SetAutoCompressConfig(false, 0, 0)
	}
	if proj.ResetOnIdleMins != nil {
		engine.SetResetOnIdle(time.Duration(*proj.ResetOnIdleMins) * time.Minute)
	} else {
		engine.SetResetOnIdle(0)
	}

	showCtx := true
	if proj.ShowContextIndicator != nil {
		showCtx = *proj.ShowContextIndicator
	}
	engine.SetShowContextIndicator(showCtx)
	showFooter := true
	if proj.ReplyFooter != nil {
		showFooter = *proj.ReplyFooter
	}
	engine.SetReplyFooterEnabled(showFooter)

	// Reload sender injection
	engine.SetInjectSender(proj.InjectSender != nil && *proj.InjectSender)

	// Reload attachment send-back switch
	engine.SetAttachmentSendEnabled(cfg.AttachmentSend != "off")

	// Reload filter_external_sessions
	engine.SetFilterExternalSessions(proj.FilterExternalSessions != nil && *proj.FilterExternalSessions)

	// Reload providers
	if ps, ok := engine.GetAgent().(core.ProviderSwitcher); ok {
		providers := make([]core.ProviderConfig, len(proj.Agent.Providers))
		for i, p := range proj.Agent.Providers {
			providers[i] = configProviderToCore(p)
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

	// Reload per-user role-based policies
	if proj.Users != nil {
		engine.SetUserRoles(buildUserRoleManager(proj.Users))
	} else {
		engine.SetUserRoles(nil)
	}

	slog.Info("config reloaded", "project", projName)
	return result, nil
}

func buildUserRoleManager(uc *config.UsersConfig) *core.UserRoleManager {
	var roles []core.RoleInput
	for name, rc := range uc.Roles {
		var rlCfg *core.RateLimitCfg
		if rc.RateLimit != nil {
			maxMsg, windowSecs := 20, 60
			if rc.RateLimit.MaxMessages != nil {
				maxMsg = *rc.RateLimit.MaxMessages
			}
			if rc.RateLimit.WindowSecs != nil {
				windowSecs = *rc.RateLimit.WindowSecs
			}
			rlCfg = &core.RateLimitCfg{
				MaxMessages: maxMsg,
				Window:      time.Duration(windowSecs) * time.Second,
			}
		}
		roles = append(roles, core.RoleInput{
			Name:             name,
			UserIDs:          rc.UserIDs,
			DisabledCommands: rc.DisabledCommands,
			RateLimit:        rlCfg,
		})
	}
	defaultRole := "member"
	if uc.DefaultRole != "" {
		defaultRole = uc.DefaultRole
	}
	urm := core.NewUserRoleManager()
	urm.Configure(defaultRole, roles)
	return urm
}

func configProviderToCore(p config.ProviderConfig) core.ProviderConfig {
	c := core.ProviderConfig{
		Name: p.Name, APIKey: p.APIKey, BaseURL: p.BaseURL,
		Model: p.Model, Models: convertProviderModels(p.Models),
		Thinking: p.Thinking, Env: p.Env,
	}
	if p.Codex != nil {
		c.CodexWireAPI = p.Codex.WireAPI
		c.CodexHTTPHeaders = p.Codex.HTTPHeaders
	}
	return c
}

func convertProviderModels(ms []config.ProviderModelConfig) []core.ModelOption {
	if len(ms) == 0 {
		return nil
	}
	opts := make([]core.ModelOption, len(ms))
	for i, m := range ms {
		opts[i] = core.ModelOption{Name: m.Model, Alias: m.Alias}
	}
	return opts
}

func buildAgentOptions(dataDir string, proj config.ProjectConfig) map[string]any {
	opts := make(map[string]any, len(proj.Agent.Options)+2)
	for k, v := range proj.Agent.Options {
		opts[k] = v
	}
	opts["cc_data_dir"] = dataDir
	opts["cc_project"] = proj.Name
	return opts
}

func wireAgentProviders(agent core.Agent, agentCfg config.AgentConfig) providerWiringResult {
	result := providerWiringResult{canStartInitialRefresh: true}
	active, _ := agentCfg.Options["provider"].(string)
	result.explicitProviderRequested = active != ""

	ps, ok := agent.(core.ProviderSwitcher)
	if !ok || len(agentCfg.Providers) == 0 {
		return result
	}

	providers := make([]core.ProviderConfig, len(agentCfg.Providers))
	for i, p := range agentCfg.Providers {
		providers[i] = configProviderToCore(p)
	}
	ps.SetProviders(providers)
	if result.explicitProviderRequested {
		result.activeProviderApplied = ps.SetActiveProvider(active)
		result.canStartInitialRefresh = result.activeProviderApplied
	}
	return result
}

func startInitialRefreshIfReady(agent core.Agent, result providerWiringResult) {
	if !result.canStartInitialRefresh {
		return
	}
	if starter, ok := agent.(initialModelRefreshStarter); ok {
		starter.StartInitialModelRefresh()
	}
}

func configProviderToGlobal(p config.ProviderConfig) core.GlobalProviderInfo {
	info := core.GlobalProviderInfo{
		Name:        p.Name,
		APIKey:      p.APIKey,
		BaseURL:     p.BaseURL,
		Model:       p.Model,
		Thinking:    p.Thinking,
		Env:         p.Env,
		AgentTypes:  p.AgentTypes,
		Endpoints:   p.Endpoints,
		AgentModels: p.AgentModels,
	}
	for _, m := range p.Models {
		info.Models = append(info.Models, struct {
			Model string `json:"model"`
			Alias string `json:"alias,omitempty"`
		}{Model: m.Model, Alias: m.Alias})
	}
	if len(p.AgentModelLists) > 0 {
		info.AgentModelLists = make(map[string][]core.GlobalModelEntry, len(p.AgentModelLists))
		for at, ml := range p.AgentModelLists {
			entries := make([]core.GlobalModelEntry, len(ml))
			for i, m := range ml {
				entries[i] = core.GlobalModelEntry{Model: m.Model, Alias: m.Alias}
			}
			info.AgentModelLists[at] = entries
		}
	}
	if p.Codex != nil {
		info.Codex = &core.GlobalCodexConfig{
			WireAPI:     p.Codex.WireAPI,
			HTTPHeaders: p.Codex.HTTPHeaders,
		}
	}
	return info
}

func globalProviderToConfig(info core.GlobalProviderInfo) config.ProviderConfig {
	p := config.ProviderConfig{
		Name:        info.Name,
		APIKey:      info.APIKey,
		BaseURL:     info.BaseURL,
		Model:       info.Model,
		Thinking:    info.Thinking,
		Env:         info.Env,
		AgentTypes:  info.AgentTypes,
		Endpoints:   info.Endpoints,
		AgentModels: info.AgentModels,
	}
	for _, m := range info.Models {
		p.Models = append(p.Models, config.ProviderModelConfig{Model: m.Model, Alias: m.Alias})
	}
	if len(info.AgentModelLists) > 0 {
		p.AgentModelLists = make(map[string][]config.ProviderModelConfig, len(info.AgentModelLists))
		for at, ml := range info.AgentModelLists {
			entries := make([]config.ProviderModelConfig, len(ml))
			for i, m := range ml {
				entries[i] = config.ProviderModelConfig{Model: m.Model, Alias: m.Alias}
			}
			p.AgentModelLists[at] = entries
		}
	}
	if info.Codex != nil {
		p.Codex = &config.CodexProviderConfig{
			WireAPI:     info.Codex.WireAPI,
			HTTPHeaders: info.Codex.HTTPHeaders,
		}
	}
	return p
}

func convertCoreModels(ms []core.ModelOption) []config.ProviderModelConfig {
	if len(ms) == 0 {
		return nil
	}
	out := make([]config.ProviderModelConfig, len(ms))
	for i, m := range ms {
		out[i] = config.ProviderModelConfig{Model: m.Name, Alias: m.Alias}
	}
	return out
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

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
