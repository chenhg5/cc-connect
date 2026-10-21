package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/platform/yuanbao"
)

func runYuanbao(args []string) {
	if len(args) == 0 {
		printYuanbaoUsage()
		return
	}

	switch args[0] {
	case "setup":
		runYuanbaoSetup(args[1:], yuanbaoSetupModeAuto)
	case "bind", "link":
		runYuanbaoSetup(args[1:], yuanbaoSetupModeBind)
	case "help", "--help", "-h":
		printYuanbaoUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown yuanbao subcommand: %s\n\n", args[0])
		printYuanbaoUsage()
		os.Exit(1)
	}
}

const (
	yuanbaoSetupModeAuto = "auto"
	yuanbaoSetupModeBind = "bind"
)

func runYuanbaoSetup(args []string, requestedMode string) {
	fs := flag.NewFlagSet("yuanbao "+requestedMode, flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (optional if only one project)")
	platformIndex := fs.Int("platform-index", 0, "1-based index among yuanbao platforms in the project (0 = first)")
	token := fs.String("token", "", "Yuanbao bot token (format: app_key:app_secret)")
	apiDomain := fs.String("api-domain", "", "yuanbao API base URL (default https://bot.yuanbao.tencent.com)")
	wsURL := fs.String("ws-url", "", "yuanbao WebSocket URL (default wss://bot-wss.yuanbao.tencent.com/wss/connection)")
	routeEnv := fs.String("route-env", "", "optional X-Route-Env header (e.g. prod, dev)")
	allowFrom := fs.String("allow-from", "", "restrict access to this user id (comma separated); empty = open")
	skipVerify := fs.Bool("skip-verify", false, "skip probing the sign-token API before saving")
	_ = fs.Parse(args)

	initConfigPath(*configFile)
	if _, err := os.Stat(config.ConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s (%v)\n", config.ConfigPath, err)
		os.Exit(1)
	}

	botToken, err := resolveYuanbaoBotToken(requestedMode, *token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	targetProject, err := resolveTargetProject(*project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	apiDomainValue := strings.TrimSpace(*apiDomain)
	wsURLValue := strings.TrimSpace(*wsURL)
	routeEnvValue := strings.TrimSpace(*routeEnv)
	allowFromValue := strings.TrimSpace(*allowFrom)

	if !*skipVerify {
		appKey, appSecret := splitYuanbaoTokenForVerify(botToken)
		fmt.Println("Verifying credentials against yuanbao sign-token API…")
		botID, err := yuanbao.VerifyCredentials(appKey, appSecret, apiDomainValue, routeEnvValue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: credential verification failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Tip: re-check --token (must be app_key:app_secret), or use --skip-verify to write anyway.")
			os.Exit(1)
		}
		fmt.Printf("✅ Verified. bot_id = %s\n", botID)
	}

	workDir, _ := os.Getwd()
	provision, err := config.EnsureProjectWithYuanbaoPlatform(config.EnsureProjectWithYuanbaoOptions{
		ProjectName: targetProject,
		WorkDir:     workDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: prepare project failed: %v\n", err)
		os.Exit(1)
	}
	if provision.Created {
		fmt.Printf("Created project %q automatically.\n", targetProject)
	} else if provision.AddedPlatform {
		fmt.Printf("Project %q had no Yuanbao platform; added one automatically.\n", targetProject)
	}

	saveResult, err := config.SaveYuanbaoPlatformCredentials(config.YuanbaoCredentialUpdateOptions{
		ProjectName:   targetProject,
		PlatformIndex: *platformIndex,
		BotToken:      botToken,
		APIDomain:     apiDomainValue,
		WSURL:         wsURLValue,
		RouteEnv:      routeEnvValue,
		AllowFrom:     allowFromValue,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: update config failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Yuanbao bot configured for project %q\n", saveResult.ProjectName)
	fmt.Printf("   Platform: yuanbao (projects[%d].platforms[%d])\n", saveResult.ProjectIndex, saveResult.PlatformAbsIndex)
	if saveResult.AllowFrom != "" {
		fmt.Printf("   allow_from: %s\n", saveResult.AllowFrom)
	}
	fmt.Println()
	fmt.Println("Next: run cc-connect (or restart the daemon) and the platform will")
	fmt.Println("auto-fetch sign-tokens via bot_token and connect over WebSocket.")
}

func resolveYuanbaoBotToken(mode, raw string) (string, error) {
	token := strings.TrimSpace(raw)
	idx := strings.Index(token, ":")
	if token == "" || idx <= 0 || idx >= len(token)-1 {
		if mode == yuanbaoSetupModeBind {
			return "", fmt.Errorf("bind mode requires --token (format: app_key:app_secret)")
		}
		return "", fmt.Errorf("--token is required (format: app_key:app_secret)")
	}
	return token, nil
}

// splitYuanbaoTokenForVerify is the same split as platform/yuanbao's
// splitBotToken, inlined here so the CLI doesn't need to import internal
// helpers of the platform package.
func splitYuanbaoTokenForVerify(raw string) (appKey, appSecret string) {
	raw = strings.TrimSpace(raw)
	idx := strings.Index(raw, ":")
	if idx <= 0 || idx >= len(raw)-1 {
		return "", ""
	}
	return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
}

func printYuanbaoUsage() {
	fmt.Println(`Usage: cc-connect yuanbao <command> [options]

Commands:
  setup   Save --token (app_key:app_secret) for a Yuanbao bot (verifies with sign-token API)
  bind    Same as setup, but fails if --token is missing

Options:
  --config <path>           Path to config file
  --project <name>          Target project (created if missing, like weixin setup)
  --platform-index <n>      1-based yuanbao platform index in project (default: first)
  --token <app_key:secret>  Yuanbao bot token (split on first ":")
  --api-domain <url>        Sign-token API base (default https://bot.yuanbao.tencent.com)
  --ws-url <url>            WebSocket URL (default wss://bot-wss.yuanbao.tencent.com/wss/connection)
  --route-env <env>         X-Route-Env header (e.g. prod, dev)
  --allow-from <id,id>      Restrict access to these user ids (comma separated)
  --skip-verify             Write credentials without probing sign-token API

Examples:
  cc-connect yuanbao setup --project my-bot --token DO7xNwDY...:CWLjJDW4...
  cc-connect yuanbao bind --project my-bot --token app_key:app_secret
  cc-connect yuanbao setup --project my-bot --token app_key:app_secret --allow-from open_id_1,open_id_2`)
}
