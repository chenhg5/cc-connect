package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

const webexBaseURL = "https://webexapis.com/v1"

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
	token string
	hc    *http.Client
}

func newHTTPClient(token string) *httpClient {
	return &httpClient{token: token, hc: &http.Client{Timeout: 60 * time.Second}}
}

func (c *httpClient) do(ctx context.Context, method, url string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.hc.Do(req)
}

func (c *httpClient) GetMe(ctx context.Context) (*person, error) {
	resp, err := c.do(ctx, http.MethodGet, webexBaseURL+"/people/me", nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	payload := strings.NewReader(`{"deviceName":"cc-connect","deviceType":"DESKTOP","name":"cc-connect","systemName":"cc-connect","systemVersion":"1.0"}`)
	resp, err := c.do(ctx, http.MethodPost, "https://wdm-a.wbx2.com/wdm/api/v1/devices", payload, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	resp, err := c.do(ctx, http.MethodDelete, deviceURL, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *httpClient) GetMessage(ctx context.Context, id string) (*message, error) {
	resp, err := c.do(ctx, http.MethodGet, webexBaseURL+"/messages/"+id, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	resp, err := c.do(ctx, http.MethodGet, url, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	resp, err := c.do(ctx, http.MethodPost, webexBaseURL+"/messages", bytes.NewReader(buf), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
	w.Close()
	resp, err := c.do(ctx, http.MethodPost, webexBaseURL+"/messages", &buf, w.FormDataContentType())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("webex: postFile status %d", resp.StatusCode)
	}
	return nil
}
