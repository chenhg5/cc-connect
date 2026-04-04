package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL    = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

	defaultLongPollTimeout = 35 * time.Second
	defaultAPITimeout      = 15 * time.Second

	// maxIlinkHTTPResponseBody caps JSON response size (getUpdates may batch many msgs).
	maxIlinkHTTPResponseBody = 64 << 20

	channelVersion = "cc-connect-weixin/1.0"
)

type apiClient struct {
	baseURL    string
	token      string
	routeTag   string
	httpClient *http.Client
}

func newAPIClient(baseURL, token, routeTag string, httpClient *http.Client) *apiClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/") + "/"
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultAPITimeout}
	}
	return &apiClient{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		routeTag:   strings.TrimSpace(routeTag),
		httpClient: httpClient,
	}
}

func (c *apiClient) longPollClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultLongPollTimeout
	}
	tr := http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := t.Clone()
		tr = cloned
	}
	return &http.Client{
		Timeout:   timeout + 5*time.Second,
		Transport: tr,
	}
}

func randomWechatUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0000"))
	}
	u := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", u)))
}

func (c *apiClient) post(ctx context.Context, endpoint string, body []byte, timeout time.Duration, label string) ([]byte, error) {
	url := c.baseURL + strings.TrimPrefix(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: new request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.routeTag != "" {
		req.Header.Set("SKRouteTag", c.routeTag)
	}

	client := c.httpClient
	if timeout > 0 {
		// Dedicated client so long-poll does not inherit short Timeout from default client.
		client = c.longPollClient(timeout)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: %w", label, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxIlinkHTTPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("weixin: %s: read body: %w", label, err)
	}
	if len(raw) > maxIlinkHTTPResponseBody {
		return nil, fmt.Errorf("weixin: %s: response body exceeds %d bytes", label, maxIlinkHTTPResponseBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin: %s: http %d: %s", label, resp.StatusCode, truncateForLog(raw, 512))
	}
	return raw, nil
}

func truncateForLog(b []byte, max int) string {
	s := string(b)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func (c *apiClient) getUpdates(ctx context.Context, buf string, timeoutMs int) (*getUpdatesResp, error) {
	timeout := defaultLongPollTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	req := getUpdatesReq{
		GetUpdatesBuf: buf,
		BaseInfo:      baseInfo{ChannelVersion: channelVersion},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "ilink/bot/getupdates", payload, timeout, "getUpdates")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &getUpdatesResp{Ret: 0, Msgs: nil, GetUpdatesBuf: buf}, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return &getUpdatesResp{Ret: 0, Msgs: nil, GetUpdatesBuf: buf}, nil
		}
		return nil, err
	}
	var out getUpdatesResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("weixin: getUpdates json: %w", err)
	}
	return &out, nil
}

func (c *apiClient) sendMessage(ctx context.Context, msg *sendMessageReq) error {
	if msg == nil {
		return fmt.Errorf("weixin: sendMessage: nil request")
	}
	msg.BaseInfo = baseInfo{ChannelVersion: channelVersion}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	raw, err := c.post(ctx, "ilink/bot/sendmessage", payload, 0, "sendMessage")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var resp sendMessageResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("weixin: sendMessage: response json: %w: %s", err, truncateForLog(raw, 256))
	}
	if resp.Ret != 0 {
		slog.Warn("weixin: sendMessage declined by API",
			"ret", resp.Ret, "errcode", resp.Errcode, "errmsg", resp.Errmsg)
		return fmt.Errorf("weixin: sendMessage: ret=%d errcode=%d errmsg=%s",
			resp.Ret, resp.Errcode, resp.Errmsg)
	}
	return nil
}

func (c *apiClient) getUploadURL(ctx context.Context, req getUploadURLRequest) (*getUploadURLResponse, error) {
	req.BaseInfo = baseInfo{ChannelVersion: channelVersion}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "ilink/bot/getuploadurl", payload, 0, "getUploadUrl")
	if err != nil {
		return nil, err
	}
	var out getUploadURLResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("weixin: getUploadUrl json: %w", err)
	}
	// 兼容微信 iLink API 变更：新版返回 upload_full_url 而非 upload_param
	// upload_full_url 是完整的 CDN 上传地址，可独立作为成功路径
	if strings.TrimSpace(out.UploadParam) == "" && strings.TrimSpace(out.UploadFullURL) == "" {
		return nil, fmt.Errorf("weixin: getUploadUrl: empty upload_param and upload_full_url in %s", truncateForLog(raw, 512))
	}
	return &out, nil
}

func (c *apiClient) sendText(ctx context.Context, to, text, contextToken, clientID string) error {
	if strings.TrimSpace(contextToken) == "" {
		return fmt.Errorf("weixin: context_token is required for send")
	}
	items := []messageItem{}
	if strings.TrimSpace(text) != "" {
		items = append(items, messageItem{
			Type:     messageItemText,
			TextItem: &textItem{Text: text},
		})
	}
	if len(items) == 0 {
		return fmt.Errorf("weixin: sendText: empty item_list")
	}
	msg := sendMessageReq{
		Msg: weixinOutboundMsg{
			FromUserID:   "",
			ToUserID:     to,
			ClientID:     clientID,
			MessageType:  messageTypeBot,
			MessageState: messageStateFinish,
			ItemList:     items,
			ContextToken: contextToken,
		},
	}
	return c.sendMessage(ctx, &msg)
}
