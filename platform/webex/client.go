package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const webexBaseURL = "https://webexapis.com/v1"

// maxRetryAfter caps how long we honor a 429 Retry-After header before retrying.
const maxRetryAfter = 60 * time.Second

// errUnauthorized signals a 401 from the Webex API so callers can stop retrying.
var errUnauthorized = errors.New("webex: unauthorized (401) — check bot token")

// webexClient abstracts the Webex REST API so tests can stub it.
type webexClient interface {
	GetMe(ctx context.Context) (*person, error)
	CreateDevice(ctx context.Context) (*device, error)
	DeleteDevice(ctx context.Context, deviceURL string) error
	GetMessage(ctx context.Context, id string) (*message, error)
	DownloadFile(ctx context.Context, url string) (*downloadedFile, error)
	PostMessage(ctx context.Context, roomID, parentID, markdown string) error
	PostFile(ctx context.Context, roomID string, f *downloadedFile) error
}

// httpClient is the real webexClient backed by net/http.
type httpClient struct {
	token   string
	hc      *http.Client
	baseURL string // Webex REST base; overridable in tests.
}

func newHTTPClient(token string) *httpClient {
	return &httpClient{token: token, hc: &http.Client{Timeout: 60 * time.Second}, baseURL: webexBaseURL}
}

// base returns the configured REST base URL, falling back to the default.
func (c *httpClient) base() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return webexBaseURL
}

func (c *httpClient) do(ctx context.Context, method, url string, body []byte, contentType string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.hc.Do(req)
}

// doWithRetry wraps do to surface 401 as errUnauthorized and to retry once on
// 429 after honoring the Retry-After header (capped). The returned response (on
// success) has an unread body the caller must close.
func (c *httpClient) doWithRetry(ctx context.Context, method, url string, body []byte, contentType, what string) (*http.Response, error) {
	resp, err := c.do(ctx, method, url, body, contentType)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %w", what, errUnauthorized)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := retryAfter(resp.Header.Get("Retry-After"))
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		resp, err = c.do(ctx, method, url, body, contentType)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s: %w", what, errUnauthorized)
		}
	}
	return resp, nil
}

// retryAfter parses a Retry-After header value (in seconds), capping the wait.
func retryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

func (c *httpClient) GetMe(ctx context.Context) (*person, error) {
	resp, err := c.doWithRetry(ctx, http.MethodGet, c.base()+"/people/me", nil, "", "webex: getMe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: getMe status %d", resp.StatusCode)
	}
	var p person
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (c *httpClient) CreateDevice(ctx context.Context) (*device, error) {
	// The WDM device endpoint requires name + model + localizedModel; omitting
	// model returns HTTP 400 "Missing Model". Verified against the live API.
	payload := []byte(`{"deviceName":"cc-connect","name":"cc-connect","model":"cc-connect","localizedModel":"cc-connect","systemName":"cc-connect","systemVersion":"1.0","deviceType":"DESKTOP"}`)
	resp, err := c.doWithRetry(ctx, http.MethodPost, "https://wdm-a.wbx2.com/wdm/api/v1/devices", payload, "application/json", "webex: createDevice")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("webex: createDevice status %d", resp.StatusCode)
	}
	var d device
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (c *httpClient) DeleteDevice(ctx context.Context, deviceURL string) error {
	if deviceURL == "" {
		return nil
	}
	resp, err := c.doWithRetry(ctx, http.MethodDelete, deviceURL, nil, "", "webex: deleteDevice")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("webex: deleteDevice status %d", resp.StatusCode)
	}
	return nil
}

func (c *httpClient) GetMessage(ctx context.Context, id string) (*message, error) {
	resp, err := c.doWithRetry(ctx, http.MethodGet, c.base()+"/messages/"+id, nil, "", "webex: getMessage")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: getMessage status %d", resp.StatusCode)
	}
	var m message
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *httpClient) DownloadFile(ctx context.Context, url string) (*downloadedFile, error) {
	resp, err := c.doWithRetry(ctx, http.MethodGet, url, nil, "", "webex: downloadFile")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webex: downloadFile status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	f := &downloadedFile{Data: data, MimeType: resp.Header.Get("Content-Type")}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			f.FileName = params["filename"]
		}
	}
	return f, nil
}

func (c *httpClient) PostMessage(ctx context.Context, roomID, parentID, markdown string) error {
	body := map[string]string{"roomId": roomID, "markdown": markdown}
	if parentID != "" {
		body["parentId"] = parentID
	}
	buf, _ := json.Marshal(body)
	resp, err := c.doWithRetry(ctx, http.MethodPost, c.base()+"/messages", buf, "application/json", "webex: postMessage")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webex: postMessage status %d", resp.StatusCode)
	}
	return nil
}

func (c *httpClient) PostFile(ctx context.Context, roomID string, f *downloadedFile) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("roomId", roomID)
	name := f.FileName
	if name == "" {
		name = "attachment"
	}
	part, err := w.CreateFormFile("files", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(f.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	resp, err := c.doWithRetry(ctx, http.MethodPost, c.base()+"/messages", buf.Bytes(), w.FormDataContentType(), "webex: postFile")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webex: postFile status %d", resp.StatusCode)
	}
	return nil
}
