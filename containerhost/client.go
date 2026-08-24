package containerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/releaseinstall"
)

type Client struct {
	http *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	if !strings.HasPrefix(strings.TrimSpace(socketPath), "/") {
		return nil, errors.New("container host client: absolute Unix socket path is required")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 2 * time.Minute}}, nil
}

func (c *Client) LatestTag(ctx context.Context) (string, error) {
	var result struct {
		Tag string `json:"tag"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/latest", nil, &result); err != nil {
		return "", err
	}
	return result.Tag, nil
}

func (c *Client) Prepare(ctx context.Context, tag string) (releaseinstall.Release, Preparation, error) {
	var preparation Preparation
	if err := c.call(ctx, http.MethodPost, "/v1/prepare", PrepareRequest{Tag: tag}, &preparation); err != nil {
		return releaseinstall.Release{}, Preparation{}, err
	}
	manifest, err := releaseinstall.DecodeLockedManifest(preparation.Manifest, tag)
	if err != nil {
		return releaseinstall.Release{}, Preparation{}, err
	}
	return releaseinstall.Release{Manifest: manifest, ManifestRaw: append([]byte(nil), preparation.Manifest...)}, preparation, nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.call(ctx, http.MethodGet, "/v1/status", nil, &status)
	return status, err
}

func (c *Client) Activate(ctx context.Context, request ActivateRequest) error {
	return c.call(ctx, http.MethodPost, "/v1/activate", request, nil)
}

func (c *Client) Commit(ctx context.Context, runID string) error {
	return c.call(ctx, http.MethodPost, "/v1/commit", RunRequest{RunID: runID}, nil)
}

func (c *Client) Cancel(ctx context.Context, runID string) error {
	return c.call(ctx, http.MethodPost, "/v1/cancel", RunRequest{RunID: runID}, nil)
}

func (c *Client) Confirm(ctx context.Context, runID string) error {
	return c.call(ctx, http.MethodPost, "/v1/confirm", RunRequest{RunID: runID}, nil)
}

func (c *Client) call(ctx context.Context, method, path string, payload, result any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://cc-connect-deploy-host"+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(contractHeader, ContractHash)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("container host client: %s: %w", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 10<<20))
	if err := decoder.Decode(&envelope); err != nil {
		return fmt.Errorf("container host client: decode %s: %w", path, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		if envelope.Error == "" {
			envelope.Error = response.Status
		}
		return errors.New(envelope.Error)
	}
	if result != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("container host client: decode result %s: %w", path, err)
		}
	}
	return nil
}
