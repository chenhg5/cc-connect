package yuanbao

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIDomain = "https://bot.yuanbao.tencent.com"
	defaultWSURL     = "wss://bot-wss.yuanbao.tencent.com/wss/connection"
	tokenPath        = "/api/v5/robotLogic/sign-token"
	maxRetries       = 3
	retryDelay       = time.Second
	cacheRefreshSecs = 60
	httpTimeout      = 10 * time.Second
)

type tokenData struct {
	token    string
	botID    string
	duration int
	product  string
	source   string
}

type cachedToken struct {
	tokenData
	expireAt time.Time
}

type tokenManager struct {
	mu       sync.Mutex
	cache    *cachedToken
	inflight chan struct{}
}

func newTokenManager() *tokenManager {
	return &tokenManager{
		inflight: make(chan struct{}, 1),
	}
}

func (tm *tokenManager) getToken(appKey, appSecret, apiDomain, routeEnv string) (*tokenData, error) {
	tm.mu.Lock()
	if tm.cache != nil && time.Now().Before(tm.cache.expireAt) {
		data := &tm.cache.tokenData
		tm.mu.Unlock()
		return data, nil
	}
	tm.mu.Unlock()

	select {
	case tm.inflight <- struct{}{}:
	default:
		tm.inflight <- struct{}{}
	}
	defer func() { <-tm.inflight }()

	tm.mu.Lock()
	if tm.cache != nil && time.Now().Before(tm.cache.expireAt) {
		data := &tm.cache.tokenData
		tm.mu.Unlock()
		return data, nil
	}
	tm.mu.Unlock()

	data, err := fetchToken(appKey, appSecret, apiDomain, routeEnv)
	if err != nil {
		return nil, err
	}

	dur := time.Duration(data.duration) * time.Second
	if dur < 300*time.Second {
		dur = 3600 * time.Second
	}
	tm.mu.Lock()
	tm.cache = &cachedToken{
		tokenData: *data,
		expireAt:  time.Now().Add(dur - cacheRefreshSecs*time.Second),
	}
	tm.mu.Unlock()
	return data, nil
}

func (tm *tokenManager) forceRefresh() {
	tm.mu.Lock()
	tm.cache = nil
	tm.mu.Unlock()
}

func computeSignature(nonce, timestamp, appKey, appSecret string) string {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(nonce + timestamp + appKey + appSecret))
	return hex.EncodeToString(mac.Sum(nil))
}

func buildTimestamp() string {
	now := time.Now().UTC().Add(8 * time.Hour)
	s := now.Format("2006-01-02T15:04:05+08:00")
	return s
}

func fetchToken(appKey, appSecret, apiDomain, routeEnv string) (*tokenData, error) {
	if apiDomain == "" {
		apiDomain = defaultAPIDomain
	}
	urlStr := strings.TrimRight(apiDomain, "/") + tokenPath
	client := &http.Client{Timeout: httpTimeout}
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelay)
		}
		nonceBytes := make([]byte, 16)
		if _, err := rand.Read(nonceBytes); err != nil {
			lastErr = fmt.Errorf("yuanbao: generate nonce: %w", err)
			continue
		}
		nonce := hex.EncodeToString(nonceBytes)
		timestamp := buildTimestamp()
		signature := computeSignature(nonce, timestamp, appKey, appSecret)

		payload := map[string]string{
			"app_key": appKey, "nonce": nonce,
			"signature": signature, "timestamp": timestamp,
		}
		body, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", urlStr, strings.NewReader(string(body)))
		if err != nil {
			lastErr = fmt.Errorf("yuanbao: create request: %w", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AppVersion", "cc-connect-yuanbao/1.0.0")
		req.Header.Set("X-Instance-Id", fmt.Sprintf("%d", instanceID))
		req.Header.Set("X-Bot-Version", "cc-connect-yuanbao/1.0.0")
		if routeEnv != "" {
			req.Header.Set("X-Route-Env", routeEnv)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("yuanbao: request failed: %w", err)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("yuanbao: sign token API returned %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		var result struct {
			Code int `json:"code"`
			Data *struct {
				Token    string `json:"token"`
				BotID    string `json:"bot_id"`
				Duration int    `json:"duration"`
				Product  string `json:"product"`
				Source   string `json:"source"`
			} `json:"data"`
		}
		if err := json.Unmarshal(respBody, &result); err != nil {
			lastErr = fmt.Errorf("yuanbao: parse response: %w", err)
			continue
		}
		if result.Code == 10099 && attempt < maxRetries {
			continue
		}
		if result.Code != 0 {
			return nil, fmt.Errorf("yuanbao: sign token error code=%d", result.Code)
		}
		if result.Data == nil {
			return nil, fmt.Errorf("yuanbao: sign token response missing data")
		}
		return &tokenData{
			token: result.Data.Token, botID: result.Data.BotID,
			duration: result.Data.Duration, product: result.Data.Product,
			source: result.Data.Source,
		}, nil
	}
	return nil, lastErr
}

// VerifyCredentials probes the sign-token API with app_key/app_secret and
// returns the bot_id on success. Used by `cc-connect yuanbao setup` so users
// see a clear error before the platform starts retrying on a background loop.
func VerifyCredentials(appKey, appSecret, apiDomain, routeEnv string) (botID string, err error) {
	if strings.TrimSpace(appKey) == "" || strings.TrimSpace(appSecret) == "" {
		return "", fmt.Errorf("yuanbao: bot_token is required (format: app_key:app_secret)")
	}
	data, err := fetchToken(appKey, appSecret, apiDomain, routeEnv)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(data.botID), nil
}
