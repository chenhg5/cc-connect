package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/config"
)

const (
	weixinSetupModeAuto = "auto"
	weixinSetupModeNew  = "new"
	weixinSetupModeBind = "bind"

	defaultWeixinAPIURL  = "https://ilinkai.weixin.qq.com"
	defaultWeixinBotType = "3"
	weixinQRPollTimeout  = 35 * time.Second
	weixinMaxQRRefresh   = 3
)

type weixinBotQRResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type weixinQRStatusResponse struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	IlinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	IlinkUserID string `json:"ilink_user_id"`
}

func runWeixin(args []string) {
	if len(args) == 0 {
		printWeixinUsage()
		return
	}

	switch args[0] {
	case "setup":
		runWeixinSetup(args[1:], weixinSetupModeAuto)
	case "new", "create":
		runWeixinSetup(args[1:], weixinSetupModeNew)
	case "bind", "link":
		runWeixinSetup(args[1:], weixinSetupModeBind)
	case "help", "--help", "-h":
		printWeixinUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown weixin subcommand: %s\n\n", args[0])
		printWeixinUsage()
		os.Exit(1)
	}
}

func runWeixinSetup(args []string, requestedMode string) {
	fs := flag.NewFlagSet("weixin "+requestedMode, flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (optional if only one project)")
	platformIndex := fs.Int("platform-index", 0, "1-based index among weixin platforms in the project (0 = first)")
	token := fs.String("token", "", "existing ilink bot Bearer token (bind mode)")
	apiURL := fs.String("api-url", defaultWeixinAPIURL, "ilink API base URL (no trailing path)")
	cdnURL := fs.String("cdn-url", "", "optional CDN base URL to write as cdn_base_url")
	timeout := fs.Int("timeout", 480, "QR login timeout in seconds")
	qrImage := fs.String("qr-image", "", "save QR as PNG (URL encoded; optional)")
	routeTag := fs.String("route-tag", "", "optional SKRouteTag header")
	botType := fs.String("bot-type", defaultWeixinBotType, "bot_type query param for get_bot_qrcode")
	setAllowFromEmpty := fs.Bool("set-allow-from-empty", false, "merge scanned Weixin user id into allow_from (preserves *)")
	skipVerify := fs.Bool("skip-verify", false, "bind mode: do not call getUpdates to verify token")
	debug := fs.Bool("debug", false, "print debug logs for HTTP steps")
	_ = fs.Parse(args)

	initConfigPath(*configFile)
	if _, err := os.Stat(config.ConfigPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: config file not found at %s (%v)\n", config.ConfigPath, err)
		os.Exit(1)
	}

	effectiveMode, err := resolveWeixinSetupMode(requestedMode, strings.TrimSpace(*token))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	targetProject, err := resolveTargetProject(*project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	base := strings.TrimRight(strings.TrimSpace(*apiURL), "/")
	if base == "" {
		base = strings.TrimRight(defaultWeixinAPIURL, "/")
	}

	var (
		outToken    string
		outBaseURL  string
		accountID   string
		scannedUser string
	)

	switch effectiveMode {
	case weixinSetupModeBind:
		outToken = strings.TrimSpace(*token)
		if outToken == "" {
			fmt.Fprintf(os.Stderr, "Error: bind mode requires --token\n")
			os.Exit(1)
		}
		if !*skipVerify {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := verifyWeixinToken(ctx, base, outToken, strings.TrimSpace(*routeTag), *debug)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: token verification failed: %v\n", err)
				fmt.Fprintln(os.Stderr, "Tip: pass --skip-verify to save the token without checking, or fix --api-url.")
				os.Exit(1)
			}
			fmt.Println("Token verified against ilink getUpdates.")
		}
		outBaseURL = base

	case weixinSetupModeNew:
		res, err := runWeixinQRLoginFlow(weixinQRLoginOptions{
			APIBaseURL: base,
			RouteTag:   strings.TrimSpace(*routeTag),
			BotType:    strings.TrimSpace(*botType),
			Timeout:    time.Duration(*timeout) * time.Second,
			QRImage:    strings.TrimSpace(*qrImage),
			Debug:      *debug,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: QR login failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "Tip: bind an existing token with: cc-connect weixin bind --token <token>")
			os.Exit(1)
		}
		outToken = res.BotToken
		if strings.TrimSpace(res.BaseURL) != "" {
			outBaseURL = strings.TrimRight(strings.TrimSpace(res.BaseURL), "/")
		} else {
			outBaseURL = base
		}
		accountID = strings.TrimSpace(res.IlinkBotID)
		scannedUser = strings.TrimSpace(res.IlinkUserID)

	default:
		fmt.Fprintf(os.Stderr, "Error: unsupported mode %q\n", effectiveMode)
		os.Exit(1)
	}

	workDir, _ := os.Getwd()
	provision, err := config.EnsureProjectWithWeixinPlatform(config.EnsureProjectWithWeixinOptions{
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
		fmt.Printf("Project %q had no Weixin platform; added one automatically.\n", targetProject)
	}

	saveOpts := config.WeixinCredentialUpdateOptions{
		ProjectName:       targetProject,
		PlatformIndex:     *platformIndex,
		Token:             outToken,
		BaseURL:           outBaseURL,
		AccountID:         accountID,
		ScannedUserID:     scannedUser,
		SetAllowFromEmpty: *setAllowFromEmpty,
	}
	if s := strings.TrimSpace(*cdnURL); s != "" {
		saveOpts.CDNBaseURL = strings.TrimRight(s, "/")
	}

	saveResult, err := config.SaveWeixinPlatformCredentials(saveOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: update config failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Weixin (ilink) configured for project %q\n", saveResult.ProjectName)
	fmt.Printf("   Platform: weixin (projects[%d].platforms[%d])\n", saveResult.ProjectIndex, saveResult.PlatformAbsIndex)
	if saveResult.AllowFrom != "" {
		fmt.Printf("   allow_from: %s\n", saveResult.AllowFrom)
	}
	fmt.Println()
	fmt.Println("Next: run cc-connect (or restart the daemon) and send a message from WeChat to finish linking context_token.")
}

func resolveWeixinSetupMode(requested, token string) (string, error) {
	switch requested {
	case weixinSetupModeAuto:
		if token != "" {
			return weixinSetupModeBind, nil
		}
		return weixinSetupModeNew, nil
	case weixinSetupModeBind:
		if token == "" {
			return "", errors.New("bind mode requires --token")
		}
		return weixinSetupModeBind, nil
	case weixinSetupModeNew:
		if token != "" {
			return "", errors.New("new/QR mode does not accept --token; use `cc-connect weixin bind --token ...`")
		}
		return weixinSetupModeNew, nil
	default:
		return "", fmt.Errorf("internal: unknown mode %q", requested)
	}
}

type weixinQRLoginOptions struct {
	APIBaseURL string
	RouteTag   string
	BotType    string
	Timeout    time.Duration
	QRImage    string
	Debug      bool
}

type weixinQRLoginResult struct {
	BotToken    string
	IlinkBotID  string
	BaseURL     string
	IlinkUserID string
}

func runWeixinQRLoginFlow(opts weixinQRLoginOptions) (*weixinQRLoginResult, error) {
	if opts.Timeout < time.Second {
		opts.Timeout = 480 * time.Second
	}
	botType := opts.BotType
	if botType == "" {
		botType = defaultWeixinBotType
	}

	ctx := context.Background()
	qrPayload, err := weixinFetchBotQRCode(ctx, opts.APIBaseURL, botType, opts.RouteTag, opts.Debug)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(qrPayload.QRCodeImgContent) == "" {
		return nil, fmt.Errorf("empty qrcode_img_content from server")
	}

	qrURL := strings.TrimSpace(qrPayload.QRCodeImgContent)
	fmt.Println("请使用微信扫描下方二维码（或打开 URL）以连接 ilink 机器人：")
	fmt.Printf("URL: %s\n\n", qrURL)
	tryPrintTerminalQRCode(qrURL)
	if opts.QRImage != "" {
		if err := saveQRCodeImage(qrURL, opts.QRImage); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save QR image: %v\n", err)
		} else {
			fmt.Printf("QR code saved to: %s\n\n", opts.QRImage)
		}
	}

	deadline := time.Now().Add(opts.Timeout)
	qrKey := qrPayload.QRCode
	refreshCount := 1
	scannedPrinted := false

	for time.Now().Before(deadline) {
		status, err := weixinPollQRStatus(ctx, opts.APIBaseURL, qrKey, opts.RouteTag, opts.Debug)
		if err != nil {
			return nil, err
		}

		switch status.Status {
		case "wait", "":
			time.Sleep(time.Second)
			continue
		case "scaned":
			if !scannedPrinted {
				fmt.Println("\n已扫码，请在手机上确认登录…")
				scannedPrinted = true
			}
			time.Sleep(time.Second)
			continue
		case "expired":
			refreshCount++
			if refreshCount > weixinMaxQRRefresh {
				return nil, fmt.Errorf("二维码多次过期，请重试 setup")
			}
			fmt.Printf("\n二维码已过期，正在刷新 (%d/%d)…\n", refreshCount, weixinMaxQRRefresh)
			newQR, err := weixinFetchBotQRCode(ctx, opts.APIBaseURL, botType, opts.RouteTag, opts.Debug)
			if err != nil {
				return nil, fmt.Errorf("refresh QR: %w", err)
			}
			qrKey = newQR.QRCode
			scannedPrinted = false
			newURL := strings.TrimSpace(newQR.QRCodeImgContent)
			if newURL != "" {
				fmt.Println("请扫描新二维码：")
				fmt.Printf("URL: %s\n\n", newURL)
				tryPrintTerminalQRCode(newURL)
			}
			time.Sleep(time.Second)
			continue
		case "confirmed":
			if strings.TrimSpace(status.IlinkBotID) == "" {
				return nil, fmt.Errorf("login confirmed but ilink_bot_id missing")
			}
			if strings.TrimSpace(status.BotToken) == "" {
				return nil, fmt.Errorf("login confirmed but bot_token missing")
			}
			fmt.Println("\n✅ 已与微信建立连接。")
			return &weixinQRLoginResult{
				BotToken:    strings.TrimSpace(status.BotToken),
				IlinkBotID:  strings.TrimSpace(status.IlinkBotID),
				BaseURL:     strings.TrimSpace(status.BaseURL),
				IlinkUserID: strings.TrimSpace(status.IlinkUserID),
			}, nil
		default:
			time.Sleep(time.Second)
		}
	}

	return nil, fmt.Errorf("等待扫码超时，请重试")
}

func weixinHTTPGet(ctx context.Context, fullURL, routeTag string, debug bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	if routeTag != "" {
		req.Header.Set("SKRouteTag", routeTag)
	}
	client := &http.Client{Timeout: weixinQRPollTimeout + 5*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if debug {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		fmt.Fprintf(os.Stderr, "[debug] GET %s -> %d %s\n", fullURL, resp.StatusCode, strings.TrimSpace(snippet))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, weixinTruncateBody(body, 256))
	}
	return body, nil
}

func weixinTruncateBody(b []byte, max int) string {
	s := string(b)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func weixinFetchBotQRCode(ctx context.Context, apiBase, botType, routeTag string, debug bool) (*weixinBotQRResponse, error) {
	base := strings.TrimRight(apiBase, "/") + "/"
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u = u.JoinPath("ilink", "bot", "get_bot_qrcode")
	q := u.Query()
	q.Set("bot_type", botType)
	u.RawQuery = q.Encode()
	raw, err := weixinHTTPGet(ctx, u.String(), routeTag, debug)
	if err != nil {
		return nil, fmt.Errorf("get_bot_qrcode: %w", err)
	}
	var out weixinBotQRResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("get_bot_qrcode json: %w", err)
	}
	return &out, nil
}

func weixinPollQRStatus(ctx context.Context, apiBase, qrKey, routeTag string, debug bool) (*weixinQRStatusResponse, error) {
	base := strings.TrimRight(apiBase, "/") + "/"
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	u = u.JoinPath("ilink", "bot", "get_qrcode_status")
	q := u.Query()
	q.Set("qrcode", qrKey)
	u.RawQuery = q.Encode()

	pollCtx, cancel := context.WithTimeout(ctx, weixinQRPollTimeout+2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-ClientVersion", "1")
	if routeTag != "" {
		req.Header.Set("SKRouteTag", routeTag)
	}
	client := &http.Client{Timeout: weixinQRPollTimeout + 5*time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return &weixinQRStatusResponse{Status: "wait"}, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return &weixinQRStatusResponse{Status: "wait"}, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if debug {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		fmt.Fprintf(os.Stderr, "[debug] poll status -> %d %s\n", resp.StatusCode, strings.TrimSpace(snippet))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get_qrcode_status http %d: %s", resp.StatusCode, weixinTruncateBody(body, 256))
	}
	var out weixinQRStatusResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("get_qrcode_status json: %w", err)
	}
	return &out, nil
}

func verifyWeixinToken(ctx context.Context, apiBase, token, routeTag string, debug bool) error {
	base := strings.TrimRight(apiBase, "/") + "/"
	u := strings.TrimRight(base, "/") + "/ilink/bot/getupdates"
	body := []byte(`{"get_updates_buf":"","base_info":{"channel_version":"cc-connect-weixin-setup/1.0"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	req.Header.Set("X-WECHAT-UIN", randomWeixinUIN())
	if routeTag != "" {
		req.Header.Set("SKRouteTag", routeTag)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if debug {
		snippet := string(raw)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		fmt.Fprintf(os.Stderr, "[debug] verify getUpdates -> %d %s\n", resp.StatusCode, strings.TrimSpace(snippet))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, weixinTruncateBody(raw, 256))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("invalid json response: %w", err)
	}
	return nil
}

func randomWeixinUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0000"))
	}
	u := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", u)))
}

func printWeixinUsage() {
	fmt.Println(`Usage: cc-connect weixin <command> [options]

Commands:
  setup   QR login when no --token; with --token => bind existing ilink bot token
  new     Force QR login (rejects --token)
  bind    Force token bind (requires --token)

Options:
  --config <path>           Path to config file
  --project <name>          Target project (created if missing, like feishu setup)
  --platform-index <n>      1-based weixin platform index in project (default: first)
  --token <bearer>          Existing ilink bot token (bind / setup with token)
  --api-url <url>           ilink base URL (default https://ilinkai.weixin.qq.com)
  --cdn-url <url>           Optional: also set cdn_base_url in config
  --timeout <seconds>       QR wait timeout (default 480)
  --qr-image <path>         Save QR PNG for the login URL
  --route-tag <tag>         Optional SKRouteTag header if your operator requires it
  --bot-type <type>         get_bot_qrcode bot_type (default 3)
  --set-allow-from-empty    Merge scanned user into allow_from (default false)
  --skip-verify             Bind only: skip getUpdates verification
  --debug                   Verbose HTTP logs

Examples:
  cc-connect weixin setup --project my-bot
  cc-connect weixin bind --project my-bot --token eyJ...
  cc-connect weixin setup --project my-bot --token eyJ...`)
}
