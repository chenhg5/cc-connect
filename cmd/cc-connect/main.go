package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"

	_ "github.com/chenhg5/cc-connect/agent/claudecode"
	_ "github.com/chenhg5/cc-connect/agent/codex"
	_ "github.com/chenhg5/cc-connect/agent/cursor"
	_ "github.com/chenhg5/cc-connect/agent/gemini"

	_ "github.com/chenhg5/cc-connect/platform/dingtalk"
	_ "github.com/chenhg5/cc-connect/platform/discord"
	_ "github.com/chenhg5/cc-connect/platform/feishu"
	_ "github.com/chenhg5/cc-connect/platform/line"
	_ "github.com/chenhg5/cc-connect/platform/slack"
	_ "github.com/chenhg5/cc-connect/platform/telegram"
	_ "github.com/chenhg5/cc-connect/platform/wecom"
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
		}
	}

	configFlag := flag.String("config", "", "path to config file (default: ./config.toml or ~/.cc-connect/config.toml)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cc-connect %s\ncommit:  %s\nbuilt:   %s\n", version, commit, buildTime)
		return
	}

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

	setupLogger(cfg.Log.Level)

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
					Name:    p.Name,
					APIKey:  p.APIKey,
					BaseURL: p.BaseURL,
					Model:   p.Model,
					Env:     p.Env,
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
		case "en", "english":
			lang = core.LangEnglish
		default:
			lang = core.LangAuto // auto-detect
		}

		engine := core.NewEngine(proj.Name, agent, platforms, sessionFile, lang)

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
				Model: p.Model, Env: p.Env,
			})
		})
		engine.SetProviderRemoveSaveFunc(func(name string) error {
			return config.RemoveProviderFromConfig(projName, name)
		})

		engines = append(engines, engine)
	}

	for _, e := range engines {
		if err := e.Start(); err != nil {
			slog.Error("failed to start engine", "error", err)
			os.Exit(1)
		}
	}

	// Start internal API server for CLI send
	apiSrv, err := core.NewAPIServer(cfg.DataDir)
	if err != nil {
		slog.Warn("api server unavailable", "error", err)
	} else {
		for i, e := range engines {
			apiSrv.RegisterEngine(cfg.Projects[i].Name, e)
		}
		apiSrv.Start()
	}

	slog.Info("cc-connect is running", "projects", len(engines))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutting down...")
	if apiSrv != nil {
		apiSrv.Stop()
	}
	for _, e := range engines {
		if err := e.Stop(); err != nil {
			slog.Error("shutdown error", "error", err)
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
type = "claudecode"   # "claudecode", "codex", "cursor", or "gemini"

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

func setupLogger(level string) {
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
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))
}
