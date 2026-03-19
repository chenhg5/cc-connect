package main

import (
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

	"github.com/chenhg5/cc-connect/config"
	qrterminal "github.com/mdp/qrterminal/v3"
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
	setAllowFromEmpty := fs.Bool("set-allow-from-empty", true, "fill allow_from with owner open_id when it is empty")
	debug := fs.Bool("debug", false, "print debug logs for onboarding requests")
	_ = fs.Parse(args)

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
			Debug:          *debug,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: onboarding failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Tip: you can bind an existing bot with `cc-connect feishu bind --app app_id:app_secret`")
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
	fmt.Println("提醒：扫码新建通常会自动预配权限与事件订阅；请在开放平台核验发布状态与可用范围。")
}

func printFeishuUsage() {
	fmt.Println(`Usage: cc-connect feishu <command> [options]

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
  --set-allow-from-empty      Fill allow_from with owner open_id if empty (default: true)
  --debug                     Print onboarding debug logs

Examples:
  # Recommended: one command for both flows
  cc-connect feishu setup --project my-project
  cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

  # Equivalent to "setup --app ..."
  cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx

  # Use only when you must force QR onboarding
  cc-connect feishu new --project my-project --platform-type lark`)
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
		return "", "", "", fmt.Errorf("new mode does not accept credentials; use `cc-connect feishu bind`")
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
	// Render a larger high-contrast QR for better scan success in terminals.
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
