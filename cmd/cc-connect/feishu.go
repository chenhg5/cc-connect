package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/timmyagentic/cc-connect-next/config"
	qrterminal "github.com/mdp/qrterminal/v3"
	"rsc.io/qr"
)

const (
	feishuSetupModeAuto = "auto"
	feishuSetupModeNew  = "new"
	feishuSetupModeBind = "bind"

	accountsFeishuBaseURL = "https://accounts.feishu.cn"
	accountsLarkBaseURL   = "https://accounts.larksuite.com"
	openFeishuBaseURL     = "https://open.feishu.cn"
	openLarkBaseURL       = "https://open.larksuite.com"
)

type registrationInitResponse struct {
	SupportedAuthMethods []string `json:"supported_auth_methods"`
	Error                string   `json:"error"`
	ErrorDescription     string   `json:"error_description"`
}

type registrationBeginResponse struct {
	DeviceCode              string `json:"device_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpireIn                int    `json:"expire_in"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

type registrationPollUserInfo struct {
	OpenID      string `json:"open_id"`
	TenantBrand string `json:"tenant_brand"`
}

type registrationPollResponse struct {
	ClientID         string                   `json:"client_id"`
	ClientSecret     string                   `json:"client_secret"`
	UserInfo         registrationPollUserInfo `json:"user_info"`
	Error            string                   `json:"error"`
	ErrorDescription string                   `json:"error_description"`
}

type tenantTokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
}

type registrationClient struct {
	baseURL string
	http    *http.Client
	debug   bool
}

type registrationFlowOptions struct {
	TimeoutSeconds int
	QRImagePath    string
	Debug          bool
}

type registrationFlowResult struct {
	AppID       string
	AppSecret   string
	OwnerOpenID string
	Platform    string // feishu or lark
}

func runFeishu(args []string) {
	if len(args) == 0 {
		printFeishuUsage()
		return
	}

	switch args[0] {
	case "setup":
		runFeishuSetup(args[1:], feishuSetupModeAuto)
	case "new", "create":
		runFeishuSetup(args[1:], feishuSetupModeNew)
	case "bind", "link":
		runFeishuSetup(args[1:], feishuSetupModeBind)
	case "help", "--help", "-h":
		printFeishuUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown feishu subcommand: %s\n\n", args[0])
		printFeishuUsage()
		os.Exit(1)
	}
}

func runFeishuSetup(args []string, requestedMode string) {
	fs := flag.NewFlagSet("feishu "+requestedMode, flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (optional if only one project)")
	platformIndex := fs.Int("platform-index", 0, "1-based index among feishu/lark platforms in the project (0 = first)")
	platformType := fs.String("platform-type", "", "force platform type: feishu or lark")
	app := fs.String("app", "", "existing bot credentials in app_id:app_secret format")
	appID := fs.String("app-id", "", "existing bot app_id")
	appSecret := fs.String("app-secret", "", "existing bot app_secret")
	timeout := fs.Int("timeout", 600, "QR onboarding timeout in seconds")
	qrImage := fs.String("qr-image", "", "save QR code as PNG image to this path (e.g. qr.png)")
	setAllowFromEmpty := fs.Bool("set-allow-from-empty", false, "merge owner open_id into allow_from when onboarding returns it (preserves *)")
	recommended := fs.Bool("recommended", false, "apply the recommended Feishu configuration without asking")
	noRecommended := fs.Bool("no-recommended", false, "skip the recommended Feishu configuration without asking")
	debug := fs.Bool("debug", false, "print debug logs for onboarding requests")
	_ = fs.Parse(args)

	profileDecided, profileApply, err := resolveRecommendedProfileFlags(*recommended, *noRecommended)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	initConfigPath(*configFile)

	effectiveMode, resolvedAppID, resolvedAppSecret, err := resolveFeishuSetupInputs(
		requestedMode,
		*app,
		*appID,
		*appSecret,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	targetProject, err := resolveTargetProject(*project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	normalizedType, err := normalizeFeishuPlatformType(*platformType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	finalPlatformType := normalizedType
	var ownerOpenID string

	switch effectiveMode {
	case feishuSetupModeBind:
		detectedType, err := validateAppCredentials(resolvedAppID, resolvedAppSecret, normalizedType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: app_id/app_secret validation failed: %v\n", err)
			os.Exit(1)
		}
		if finalPlatformType == "" {
			finalPlatformType = detectedType
		}
		fmt.Printf("Credentials verified for app_id %s.\n", resolvedAppID)

	case feishuSetupModeNew:
		result, err := runRegistrationFlow(registrationFlowOptions{
			TimeoutSeconds: *timeout,
			QRImagePath:    *qrImage,
			Debug:          *debug,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: onboarding failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Tip: you can bind an existing bot with `cc-connect-next feishu bind --app app_id:app_secret`")
			os.Exit(1)
		}
		resolvedAppID = result.AppID
		resolvedAppSecret = result.AppSecret
		ownerOpenID = result.OwnerOpenID
		if finalPlatformType == "" {
			finalPlatformType = result.Platform
		}

	default:
		fmt.Fprintf(os.Stderr, "Error: unsupported mode %q\n", effectiveMode)
		os.Exit(1)
	}

	provisionType := finalPlatformType
	if provisionType == "" {
		provisionType = "feishu"
	}
	workDir, _ := os.Getwd()
	provisionResult, err := config.EnsureProjectWithFeishuPlatform(config.EnsureProjectWithFeishuOptions{
		ProjectName:  targetProject,
		PlatformType: provisionType,
		WorkDir:      workDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: prepare project failed: %v\n", err)
		os.Exit(1)
	}
	if provisionResult.Created {
		fmt.Printf("Created project %q automatically.\n", targetProject)
	} else if provisionResult.AddedPlatform {
		fmt.Printf("Project %q had no Feishu/Lark platform, added one automatically.\n", targetProject)
	}

	saveResult, err := config.SaveFeishuPlatformCredentials(config.FeishuCredentialUpdateOptions{
		ProjectName:       targetProject,
		PlatformIndex:     *platformIndex,
		PlatformType:      finalPlatformType,
		AppID:             resolvedAppID,
		AppSecret:         resolvedAppSecret,
		OwnerOpenID:       ownerOpenID,
		SetAllowFromEmpty: *setAllowFromEmpty,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: update config failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Feishu/Lark bot configured for project %q\n", saveResult.ProjectName)
	fmt.Printf("   Platform: %s (projects[%d].platforms[%d])\n", saveResult.PlatformType, saveResult.ProjectIndex, saveResult.PlatformAbsIndex)
	fmt.Printf("   App ID:   %s\n", resolvedAppID)
	if saveResult.AllowFrom != "" {
		fmt.Printf("   allow_from: %s\n", saveResult.AllowFrom)
	}
	fmt.Println()

	if ownerOpenID != "" {
		printAllowFromGuidance(resolvedAppID, resolvedAppSecret, ownerOpenID, saveResult)
	}

	applyRecommendedFeishuProfile(
		os.Stdout,
		os.Stdin,
		saveResult.ProjectName,
		*platformIndex,
		profileDecided,
		profileApply,
		stdinIsInteractive(),
	)

	printBotMenuGuidance(saveResult.PlatformType)

	fmt.Println("提醒：扫码新建通常会自动预配权限与事件订阅；请在开放平台核验发布状态与可用范围。")
}

// resolveRecommendedProfileFlags reports whether the command line already
// decided about the recommended profile, so the prompt is only shown when the
// user has not answered in advance.
func resolveRecommendedProfileFlags(yes, no bool) (decided, apply bool, err error) {
	if yes && no {
		return false, false, fmt.Errorf("--recommended and --no-recommended cannot be combined")
	}
	if yes {
		return true, true, nil
	}
	if no {
		return true, false, nil
	}
	return false, false, nil
}

// promptYesNo asks a yes/no question. Anything that is not a yes is a no,
// except an empty answer, which takes the default. A closed or unreadable
// stdin also takes the default rather than blocking a scripted install.
func promptYesNo(in io.Reader, out io.Writer, question string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	_, _ = fmt.Fprintf(out, "%s %s ", question, suffix)

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		_, _ = fmt.Fprintln(out)
		return defaultYes
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes
	case "y", "yes", "是", "好":
		return true
	default:
		return false
	}
}

// stdinIsInteractive reports whether a prompt would reach a human. A piped or
// redirected stdin must not turn an unattended install into a hang.
func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func printRecommendedFeishuProfile(out io.Writer, projectName string, settings []config.RecommendedFeishuSetting) {
	_, _ = fmt.Fprintf(out, "📐 推荐飞书配置（project %q，本项目日常运行所用的形态）：\n", projectName)
	_, _ = fmt.Fprintln(out, "   只发最终答案的引用卡片、可点击的文件引用、群里免 @ 应答。")
	_, _ = fmt.Fprintln(out)

	lastHeader := ""
	for _, setting := range settings {
		if header := setting.TableHeader(); header != lastHeader {
			if lastHeader != "" {
				_, _ = fmt.Fprintln(out)
			}
			_, _ = fmt.Fprintf(out, "   %s\n", header)
			lastHeader = header
		}
		_, _ = fmt.Fprintf(out, "   %-22s = %-16s # %s\n", setting.Key, setting.Value, setting.Why)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "   凭据、allow_from、allow_chat、work_dir 等与本机身份相关的设置不会被改动。")
	_, _ = fmt.Fprintln(out)
}

// applyRecommendedFeishuProfile runs the post-credential step that offers the
// recommended profile. Everything it writes is a deliberate answer: the flags
// when they were given, the prompt when a human is there, and otherwise
// nothing at all.
func applyRecommendedFeishuProfile(out io.Writer, in io.Reader, projectName string, platformIndex int, decided, apply, interactive bool) {
	if decided && !apply {
		return
	}
	settings := config.RecommendedFeishuProfileFor(projectName)
	if !decided {
		if !interactive {
			_, _ = fmt.Fprintln(out, "ℹ️  未套用推荐飞书配置（stdin 非交互）。加 --recommended 套用，或 --no-recommended 静默跳过。")
			_, _ = fmt.Fprintln(out)
			return
		}
		printRecommendedFeishuProfile(out, projectName, settings)
		if !promptYesNo(in, out, "是否套用以上推荐配置？", true) {
			_, _ = fmt.Fprintln(out, "已跳过。稍后可运行 `cc-connect-next feishu setup --recommended` 套用。")
			_, _ = fmt.Fprintln(out)
			return
		}
	}

	result, err := config.ApplyRecommendedFeishuProfile(config.RecommendedFeishuProfileOptions{
		ProjectName:   projectName,
		PlatformIndex: platformIndex,
	})
	if err != nil {
		_, _ = fmt.Fprintf(out, "⚠️  推荐配置未写入：%v\n", err)
		_, _ = fmt.Fprintln(out, "   凭据已保存，可手工按上面的清单设置。")
		_, _ = fmt.Fprintln(out)
		return
	}
	_, _ = fmt.Fprintf(out, "✅ 已套用推荐飞书配置（%d 项）到 project %q。\n", result.Applied, result.ProjectName)
	_, _ = fmt.Fprintln(out, "   下一步请设置 allow_from / allow_chat 限定可用范围，group_reply_all 才是安全的。")
	_, _ = fmt.Fprintln(out)
}

func printAllowFromGuidance(appID, appSecret, ownerOpenID string, result *config.FeishuCredentialUpdateResult) {
	botOpenID := fetchBotOpenIDForSetup(appID, appSecret, result.PlatformType)

	if botOpenID != "" && ownerOpenID == botOpenID {
		fmt.Println("⚠️  注册返回的 open_id 是机器人自身的 ID，不是你的用户 ID。")
		fmt.Println("   飞书 open_id 是应用级别的标识符，注册流程返回的 ID 无法直接用于 allow_from。")
		fmt.Println()
	}

	if result.AllowFrom == "" {
		fmt.Println("💡 allow_from 未设置（所有用户均可使用）。如需限制访问，请：")
		fmt.Println("   1. 向机器人发送 /whoami 获取你的 User ID")
		fmt.Println("   2. 在 config.toml 中设置: allow_from = \"<你的User ID>\"")
		fmt.Println("   同样，admin_from 也可用 /whoami 获取的 ID 来设置。")
		fmt.Println()
	}
}

func fetchBotOpenIDForSetup(appID, appSecret, platformType string) string {
	base := openFeishuBaseURL
	if platformType == "lark" {
		base = openLarkBaseURL
	}
	body, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	req, err := http.NewRequest(http.MethodPost, base+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tokenResp tenantTokenResponse
	if err := json.Unmarshal(data, &tokenResp); err != nil || tokenResp.TenantAccessToken == "" {
		return ""
	}

	botReq, err := http.NewRequest(http.MethodGet, base+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return ""
	}
	botReq.Header.Set("Authorization", "Bearer "+tokenResp.TenantAccessToken)

	botResp, err := client.Do(botReq)
	if err != nil {
		return ""
	}
	defer botResp.Body.Close()
	botData, _ := io.ReadAll(io.LimitReader(botResp.Body, 1<<20))

	var result struct {
		Code int `json:"code"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(botData, &result); err != nil || result.Code != 0 {
		return ""
	}
	return result.Bot.OpenID
}

func printBotMenuGuidance(platformType string) {
	base := "https://open.feishu.cn"
	if platformType == "lark" {
		base = "https://open.larksuite.com"
	}

	fmt.Println("📋 机器人菜单配置（可选）：")
	fmt.Println("   飞书机器人支持自定义悬浮菜单，可将常用命令固定在输入框上方。")
	fmt.Println("   菜单需在开发者后台手动配置（暂不支持 API 设置），步骤：")
	fmt.Printf("   1. 打开开发者后台: %s/app\n", base)
	fmt.Println("   2. 选择你的应用 → 应用能力 → 机器人")
	fmt.Println("   3. 开启「机器人自定义菜单」，选择「悬浮菜单」样式")
	fmt.Println("   4. 添加菜单项，响应动作选择「发送文字消息」")
	fmt.Println("   5. 创建版本并发布（生效约需 5 分钟）")
	fmt.Println()
	fmt.Println("   推荐菜单配置：")
	fmt.Println("   ┌─────────────────────────────────────────────┐")
	fmt.Println("   │ 主菜单: cc-connect-next                          │")
	fmt.Println("   │   ├── /help     帮助                        │")
	fmt.Println("   │   ├── /status   状态                        │")
	fmt.Println("   │   ├── /new      新会话                      │")
	fmt.Println("   │   ├── /list     会话列表                    │")
	fmt.Println("   │   └── /stop     停止                        │")
	fmt.Println("   │ 主菜单: 设置                                 │")
	fmt.Println("   │   ├── /model    切换模型                    │")
	fmt.Println("   │   ├── /mode     切换模式                    │")
	fmt.Println("   │   ├── /quiet    静默模式                    │")
	fmt.Println("   │   ├── /lang     语言                        │")
	fmt.Println("   │   └── /config   配置                        │")
	fmt.Println("   │ 主菜单: 工具                                 │")
	fmt.Println("   │   ├── /compress 压缩上下文                  │")
	fmt.Println("   │   ├── /memory   记忆                        │")
	fmt.Println("   │   ├── /cron     定时任务                    │")
	fmt.Println("   │   ├── /whoami   查看我的ID                  │")
	fmt.Println("   │   └── /doctor   诊断                        │")
	fmt.Println("   └─────────────────────────────────────────────┘")
	fmt.Println()
}

func printFeishuUsage() {
	fmt.Println(`Usage: cc-connect-next feishu <command> [options]

Commands:
  setup   Unified entry: no credentials => NEW flow; with --app/--app-id => BIND flow
  new     Force NEW flow (QR onboarding). Rejects --app/--app-id.
  bind    Force BIND flow (requires app_id/app_secret).

Options:
  --config <path>             Path to config file
  --project <name>            Target project (auto-created if missing)
  --platform-index <n>        1-based Feishu/Lark platform index in the project (default: first)
  --platform-type <type>      Force platform type: feishu or lark
  --app <id:secret>           Existing credentials (recommended for bind/setup)
  --app-id <id>               Existing app_id
  --app-secret <secret>       Existing app_secret
  --timeout <seconds>         QR onboarding timeout (default: 600)
  --qr-image <path>           Save QR code as PNG image file (e.g. --qr-image qr.png)
  --set-allow-from-empty      Merge owner open_id into allow_from when available (default: false)
  --recommended               Apply the recommended Feishu configuration without asking
  --no-recommended            Skip the recommended Feishu configuration without asking
  --debug                     Print onboarding debug logs

After the credentials are saved, setup shows the recommended Feishu
configuration and asks whether to apply it. Credentials, allow_from, allow_chat,
and work_dir are never changed by it. With neither --recommended nor
--no-recommended and a non-interactive stdin, nothing is applied.

Examples:
  # Recommended: one command for both flows
  cc-connect-next feishu setup --project my-project
  cc-connect-next feishu setup --project my-project --app cli_xxx:sec_xxx

  # Equivalent to "setup --app ..."
  cc-connect-next feishu bind --project my-project --app cli_xxx:sec_xxx

  # Unattended install with the recommended configuration
  cc-connect-next feishu setup --project my-project --app cli_xxx:sec_xxx --recommended

  # Use only when you must force QR onboarding
  cc-connect-next feishu new --project my-project --platform-type lark`)
}

func resolveFeishuSetupInputs(mode, app, appID, appSecret string) (effectiveMode, resolvedAppID, resolvedAppSecret string, err error) {
	app = strings.TrimSpace(app)
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)

	if app != "" && (appID != "" || appSecret != "") {
		return "", "", "", fmt.Errorf("use either --app or --app-id/--app-secret, not both")
	}

	if app != "" {
		appID, appSecret, err = parseAppPair(app)
		if err != nil {
			return "", "", "", err
		}
	}

	if appID != "" || appSecret != "" {
		if appID == "" || appSecret == "" {
			return "", "", "", fmt.Errorf("both --app-id and --app-secret are required")
		}
	}

	effectiveMode = mode
	if mode == feishuSetupModeAuto {
		if appID != "" && appSecret != "" {
			effectiveMode = feishuSetupModeBind
		} else {
			effectiveMode = feishuSetupModeNew
		}
	}
	if mode == feishuSetupModeBind && (appID == "" || appSecret == "") {
		return "", "", "", fmt.Errorf("bind mode requires credentials: use --app id:secret or --app-id/--app-secret")
	}
	if mode == feishuSetupModeNew && (appID != "" || appSecret != "") {
		return "", "", "", fmt.Errorf("new mode does not accept credentials; use `cc-connect-next feishu bind`")
	}

	return effectiveMode, appID, appSecret, nil
}

func parseAppPair(raw string) (appID, appSecret string, err error) {
	idx := strings.Index(raw, ":")
	if idx <= 0 || idx >= len(raw)-1 {
		return "", "", fmt.Errorf("--app format must be app_id:app_secret")
	}
	appID = strings.TrimSpace(raw[:idx])
	appSecret = strings.TrimSpace(raw[idx+1:])
	if appID == "" || appSecret == "" {
		return "", "", fmt.Errorf("--app format must be app_id:app_secret")
	}
	return appID, appSecret, nil
}

func resolveTargetProject(project string) (string, error) {
	project = strings.TrimSpace(project)
	if project != "" {
		return project, nil
	}
	projects, err := config.ListProjects()
	if err != nil {
		return "", err
	}
	switch len(projects) {
	case 0:
		return "", fmt.Errorf("no project found in config")
	case 1:
		return projects[0], nil
	default:
		sort.Strings(projects)
		return "", fmt.Errorf("multiple projects found, please specify --project (%s)", strings.Join(projects, ", "))
	}
}

func normalizeFeishuPlatformType(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "", nil
	}
	if raw != "feishu" && raw != "lark" {
		return "", fmt.Errorf("invalid --platform-type %q, want feishu or lark", raw)
	}
	return raw, nil
}

func validateAppCredentials(appID, appSecret, platformType string) (string, error) {
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	if appID == "" || appSecret == "" {
		return "", errors.New("app_id/app_secret are required")
	}

	candidates := []string{"feishu", "lark"}
	if platformType == "feishu" || platformType == "lark" {
		candidates = []string{platformType}
	}

	var lastErr error
	for _, candidate := range candidates {
		base := openFeishuBaseURL
		if candidate == "lark" {
			base = openLarkBaseURL
		}
		ok, err := validateAppCredentialsAgainstBase(base, appID, appSecret)
		if err != nil {
			lastErr = err
			continue
		}
		if ok {
			return candidate, nil
		}
		lastErr = fmt.Errorf("remote returned non-zero code")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown validation error")
	}
	return "", lastErr
}

func validateAppCredentialsAgainstBase(baseURL, appID, appSecret string) (bool, error) {
	body, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}

	var parsed tenantTokenResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	if parsed.Code == 0 && parsed.TenantAccessToken != "" {
		return true, nil
	}
	if parsed.Msg != "" {
		return false, fmt.Errorf("code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return false, nil
}

func runRegistrationFlow(opts registrationFlowOptions) (*registrationFlowResult, error) {
	if opts.TimeoutSeconds <= 0 {
		opts.TimeoutSeconds = 600
	}
	client := &registrationClient{
		baseURL: accountsFeishuBaseURL,
		http:    &http.Client{Timeout: 15 * time.Second},
		debug:   opts.Debug,
	}

	var initRes registrationInitResponse
	if err := client.registrationCall("init", nil, &initRes); err != nil {
		return nil, fmt.Errorf("init failed: %w", err)
	}
	if initRes.Error != "" {
		return nil, fmt.Errorf("%s: %s", initRes.Error, initRes.ErrorDescription)
	}
	if len(initRes.SupportedAuthMethods) > 0 && !containsString(initRes.SupportedAuthMethods, "client_secret") {
		return nil, fmt.Errorf("current environment does not support client_secret auth")
	}

	var beginRes registrationBeginResponse
	beginParams := map[string]string{
		"archetype":         "PersonalAgent",
		"auth_method":       "client_secret",
		"request_user_info": "open_id",
	}
	if err := client.registrationCall("begin", beginParams, &beginRes); err != nil {
		return nil, fmt.Errorf("begin failed: %w", err)
	}
	if beginRes.Error != "" {
		return nil, fmt.Errorf("%s: %s", beginRes.Error, beginRes.ErrorDescription)
	}
	if beginRes.DeviceCode == "" || beginRes.VerificationURIComplete == "" {
		return nil, fmt.Errorf("incomplete onboarding response")
	}

	fmt.Println("请使用飞书/Lark 手机 App 扫码完成机器人创建与授权：")
	fmt.Printf("URL: %s\n\n", beginRes.VerificationURIComplete)
	tryPrintTerminalQRCode(beginRes.VerificationURIComplete)
	if opts.QRImagePath != "" {
		if err := saveQRCodeImage(beginRes.VerificationURIComplete, opts.QRImagePath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save QR image: %v\n", err)
		} else {
			fmt.Printf("QR code saved to: %s\n\n", opts.QRImagePath)
		}
	}

	interval := beginRes.Interval
	if interval <= 0 {
		interval = 5
	}
	expireIn := beginRes.ExpireIn
	if expireIn <= 0 {
		expireIn = opts.TimeoutSeconds
	}

	timeoutAt := time.Now().Add(time.Duration(expireIn) * time.Second)
	if limitByFlag := time.Now().Add(time.Duration(opts.TimeoutSeconds) * time.Second); limitByFlag.Before(timeoutAt) {
		timeoutAt = limitByFlag
	}

	platformType := "feishu"
	for time.Now().Before(timeoutAt) {
		var pollRes registrationPollResponse
		if err := client.registrationCall("poll", map[string]string{"device_code": beginRes.DeviceCode}, &pollRes); err != nil {
			return nil, fmt.Errorf("poll failed: %w", err)
		}

		tenantBrand := strings.ToLower(strings.TrimSpace(pollRes.UserInfo.TenantBrand))
		if tenantBrand == "lark" {
			platformType = "lark"
			if client.baseURL != accountsLarkBaseURL {
				client.baseURL = accountsLarkBaseURL
				if opts.Debug {
					fmt.Fprintln(os.Stderr, "[debug] tenant brand detected as lark, switched onboarding domain")
				}
				continue
			}
		}

		if pollRes.ClientID != "" && pollRes.ClientSecret != "" {
			return &registrationFlowResult{
				AppID:       pollRes.ClientID,
				AppSecret:   pollRes.ClientSecret,
				OwnerOpenID: pollRes.UserInfo.OpenID,
				Platform:    platformType,
			}, nil
		}

		switch pollRes.Error {
		case "", "authorization_pending":
		case "slow_down":
			interval += 5
		case "access_denied":
			return nil, fmt.Errorf("authorization denied by user")
		case "expired_token":
			return nil, fmt.Errorf("onboarding session expired")
		default:
			if pollRes.Error != "" {
				return nil, fmt.Errorf("%s: %s", pollRes.Error, pollRes.ErrorDescription)
			}
		}

		time.Sleep(time.Duration(interval) * time.Second)
	}

	return nil, fmt.Errorf("timed out waiting for QR onboarding result")
}

func (c *registrationClient) registrationCall(action string, params map[string]string, out any) error {
	form := url.Values{}
	form.Set("action", action)
	for k, v := range params {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if c.debug {
		fmt.Fprintf(os.Stderr, "[debug] registration action=%s status=%d body=%s\n", action, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), expected) {
			return true
		}
	}
	return false
}

func tryPrintTerminalQRCode(content string) {
	if content == "" {
		return
	}
	qrterminal.GenerateWithConfig(content, qrterminal.Config{
		Level:      qrterminal.M,
		Writer:     os.Stdout,
		HalfBlocks: false,
		BlackChar:  "██",
		WhiteChar:  "  ",
		QuietZone:  4,
	})
	if _, err := fmt.Fprintln(os.Stdout); err != nil {
		return
	}
}

func saveQRCodeImage(content, path string) error {
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	code.Scale = 8
	return os.WriteFile(path, code.PNG(), 0644)
}
