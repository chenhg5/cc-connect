package youzone

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type client struct {
	cfg        config
	httpClient *http.Client
}

func newClient(cfg config, httpClient *http.Client) *client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.httpTimeout}
	}
	if httpClient.Timeout == 0 {
		httpClient.Timeout = defaultHTTPTimeout
	}
	return &client{cfg: cfg, httpClient: httpClient}
}

func (c *client) listRobots(ctx context.Context, machineCode string) ([]robotRecord, error) {
	target := c.buildURL("robot/web/list")
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(machineCode) != "" {
		q := u.Query()
		q.Set("machineCode", strings.TrimSpace(machineCode))
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, false)
	resp, body, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("youzone: list robots: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("youzone: list robots: HTTP %d: %s", resp.StatusCode, c.redactBody(body))
	}
	robots := normalizeRobotList(body)
	return robots, nil
}

func (c *client) createRobot(ctx context.Context, machineCode, robotExplain string) (robotRecord, error) {
	machineCode = strings.TrimSpace(machineCode)
	if machineCode == "" {
		return robotRecord{}, fmt.Errorf("youzone: create robot: machineCode is required")
	}
	payload := map[string]string{"machineCode": machineCode}
	if strings.TrimSpace(robotExplain) != "" {
		payload["robotExplain"] = strings.TrimSpace(robotExplain)
	}
	body, err := c.postJSON(ctx, "robot/web/create", payload)
	if err != nil {
		return robotRecord{}, fmt.Errorf("youzone: create robot: %w", err)
	}
	robot := normalizeRobot(body)
	if robot.ID == "" {
		return robotRecord{}, fmt.Errorf("youzone: create robot: missing robot id in response: %s", c.redactBody(body))
	}
	return robot, nil
}

func (c *client) getWSS(ctx context.Context, robotID string) (string, error) {
	payload := map[string]string{"id": robotID, "robotId": robotID}
	body, err := c.postJSON(ctx, "claw-robot/client/getWss", payload)
	if err != nil {
		return "", fmt.Errorf("youzone: get wss: %w", err)
	}
	wss := normalizeWSS(body)
	if wss == "" {
		return "", fmt.Errorf("youzone: get wss: missing wss in response: %s", c.redactBody(body))
	}
	return wss, nil
}

func (c *client) sendMessage(ctx context.Context, robotID string, msg outboundMessage) (sendResult, error) {
	// No conversation/recipient field: a YOUZONE "claw robot" is bound to one
	// conversation, so robotId alone identifies the target — same as YonClaw's
	// claw-robot/client/sendMessage. See outbound.go for the payload rationale.
	payload := map[string]any{
		"id":          robotID,
		"robotId":     robotID,
		"content":     msg.Content,
		"contentType": msg.ContentType,
	}
	if strings.TrimSpace(msg.Extend) != "" {
		payload["extend"] = msg.Extend
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return sendResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL("claw-robot/client/sendMessage"), bytes.NewReader(data))
	if err != nil {
		return sendResult{}, err
	}
	c.setHeaders(req, true)
	resp, body, err := c.do(req)
	if err != nil {
		return sendResult{}, fmt.Errorf("youzone: send message: %w", err)
	}
	result := parseSendResult(resp.StatusCode, body)
	if !result.Success {
		return result, fmt.Errorf("youzone: send message failed: HTTP %d business=%v body=%s", result.Status, result.BusinessCode, c.redactBody(body))
	}
	return result, nil
}

func (c *client) postJSON(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL(path), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, true)
	resp, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, c.redactBody(body))
	}
	if code, ok := businessCode(body); ok && code != 200 {
		return nil, fmt.Errorf("business code %d: %s", code, c.redactBody(body))
	}
	return body, nil
}

func (c *client) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

func (c *client) buildURL(path string) string {
	return strings.TrimRight(c.cfg.baseURL, "/") + c.cfg.apiPrefix + "/" + strings.TrimLeft(path, "/")
}

func (c *client) setHeaders(req *http.Request, jsonBody bool) {
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Cookie", fmt.Sprintf("yht_access_token=%s; tenantid=%s", c.cfg.accessToken, c.cfg.tenantID))
	req.Header.Set("Origin", strings.TrimRight(c.cfg.baseURL, "/"))
	req.Header.Set("Referer", strings.TrimRight(c.cfg.baseURL, "/")+"/")
	req.Header.Set("User-Agent", "cc-connect-youzone/0.1")
	if c.cfg.enableTokenHeaderFallback {
		req.Header.Set("Authorization", "Bearer "+c.cfg.accessToken)
		req.Header.Set("X-Access-Token", c.cfg.accessToken)
	}
}

func (c *client) redactBody(body []byte) string {
	text := string(body)
	text = core.RedactToken(text, c.cfg.accessToken)
	if len(text) > 1024 {
		text = text[:1024] + "..."
	}
	return text
}

func normalizeRobotList(body []byte) []robotRecord {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	candidates := []any{valueAt(v, "data", "dataList"), valueAt(v, "dataList"), valueAt(v, "data")}
	for _, candidate := range candidates {
		if arr, ok := candidate.([]any); ok {
			robots := make([]robotRecord, 0, len(arr))
			for _, item := range arr {
				if robot := readRobot(item); robot.ID != "" {
					robots = append(robots, robot)
				}
			}
			return robots
		}
	}
	return nil
}

func normalizeRobot(body []byte) robotRecord {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return robotRecord{}
	}
	for _, candidate := range []any{valueAt(v, "data"), valueAt(v, "result"), valueAt(v, "robot"), v} {
		if robot := readRobot(candidate); robot.ID != "" {
			return robot
		}
	}
	return robotRecord{}
}

func readRobot(v any) robotRecord {
	m, ok := v.(map[string]any)
	if !ok {
		return robotRecord{}
	}
	return robotRecord{
		ID:          readString(m["id"]),
		Name:        readString(m["name"]),
		MachineCode: readString(m["machineCode"]),
		RobotUserID: readString(m["robotUserId"]),
	}
}

func normalizeWSS(body []byte) string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return pickString(v, "wss", "url", "wsUrl", "wssUrl")
}

func parseSendResult(status int, body []byte) sendResult {
	result := sendResult{Status: status, ResponseText: string(body)}
	var parsed struct {
		Code *int `json:"code"`
		Data struct {
			PacketID string `json:"packetId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &parsed)
	result.BusinessCode = parsed.Code
	result.PacketID = strings.TrimSpace(parsed.Data.PacketID)
	result.Success = status >= 200 && status < 300 && (parsed.Code == nil || *parsed.Code == 200)
	return result
}

func businessCode(body []byte) (int, bool) {
	var parsed struct {
		Code *int `json:"code"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.Code == nil {
		return 0, false
	}
	return *parsed.Code, true
}

func valueAt(v any, path ...string) any {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func pickString(v any, keys ...string) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range keys {
		if s := readString(m[key]); s != "" {
			return s
		}
	}
	if data := pickString(m["data"], keys...); data != "" {
		return data
	}
	return ""
}

func readString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
