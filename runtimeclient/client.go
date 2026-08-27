package runtimeclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/runtimeprotocol"
	"github.com/gorilla/websocket"
)

type ClientConfig struct {
	ServerURL             string
	DeviceID              string
	PrivateKey            ed25519.PrivateKey
	Handler               *Handler
	Checkpoint            EventCheckpointStore
	AllowInsecureLoopback bool
}

const (
	maxRuntimeAttachmentSize = 50 << 20
	runtimePingInterval      = 30 * time.Second
	runtimePongWait          = 90 * time.Second
	runtimePingWriteTimeout  = 10 * time.Second
)

type EventCheckpointStore interface {
	RecordUnconfirmed(generation, sequence uint64, method runtimeprotocol.Method, resource runtimeprotocol.Resource, payload []byte) error
	Confirm(generation, sequence uint64) error
}

type Client struct {
	config ClientConfig
	http   *http.Client

	mu         sync.Mutex
	connection *websocket.Conn
	generation uint64
	sequence   uint64
	closed     bool
	pingEvery  time.Duration
	pongWait   time.Duration
}

func NewClient(config ClientConfig) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(config.ServerURL))
	if err != nil || base.Host == "" {
		return nil, errors.New("runtime client: valid server URL is required")
	}
	if base.Scheme != "https" && (!config.AllowInsecureLoopback || base.Scheme != "http" || !isLoopbackHost(base.Hostname())) {
		return nil, errors.New("runtime client: server URL must use HTTPS")
	}
	if strings.TrimSpace(config.DeviceID) == "" || len(config.PrivateKey) != ed25519.PrivateKeySize || config.Handler == nil || config.Checkpoint == nil {
		return nil, errors.New("runtime client: device identity, handler and checkpoint store are required")
	}
	config.ServerURL = strings.TrimSuffix(base.String(), "/")
	client := &Client{
		config:    config,
		http:      &http.Client{Timeout: 20 * time.Second},
		pingEvery: runtimePingInterval,
		pongWait:  runtimePongWait,
	}
	config.Handler.SetEventEmitter(client.emit)
	config.Handler.SetAttachmentFetcher(client.fetchAttachment)
	return client, nil
}

func Pair(ctx context.Context, serverURL, code, name string, publicKey ed25519.PublicKey, allowInsecureLoopback bool) (string, error) {
	base, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || base.Host == "" {
		return "", errors.New("runtime client: valid server URL is required")
	}
	if base.Scheme != "https" && (!allowInsecureLoopback || base.Scheme != "http" || !isLoopbackHost(base.Hostname())) {
		return "", errors.New("runtime client: pairing URL must use HTTPS")
	}
	body, _ := json.Marshal(map[string]string{
		"code": code, "name": name, "public_key": base64.RawURLEncoding.EncodeToString(publicKey),
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base.String(), "/")+"/runtime/v1/pair", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("runtime client: pair: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			DeviceID     string `json:"device_id"`
			ContractHash string `json:"contract_hash"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return "", fmt.Errorf("runtime client: decode pairing response: %w", err)
	}
	if !envelope.OK || response.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("runtime client: pairing rejected: %s", envelope.Error)
	}
	if envelope.Data.ContractHash != runtimeprotocol.ContractHash {
		return "", errors.New("runtime client: update_required")
	}
	return envelope.Data.DeviceID, nil
}

func (c *Client) Run(ctx context.Context) error {
	delay := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.runConnection(ctx)
		if ctx.Err() != nil || c.isClosed() {
			return ctx.Err()
		}
		if errors.Is(err, runtimeprotocol.ErrContractMismatch) || strings.Contains(err.Error(), "update_required") {
			return err
		}
		slog.Warn("Runtime 连接已断开，等待重连", "error", err, "retry_after", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func (c *Client) runConnection(ctx context.Context) error {
	challenge, err := c.challenge(ctx)
	if err != nil {
		return err
	}
	message := []byte(runtimeprotocol.ContractHash + "\n" + c.config.DeviceID + "\n" + challenge)
	signature := ed25519.Sign(c.config.PrivateKey, message)
	base, _ := url.Parse(c.config.ServerURL)
	base.Scheme = map[string]string{"https": "wss", "http": "ws"}[base.Scheme]
	base.Path = "/runtime/v1/connect"
	query := base.Query()
	query.Set("device_id", c.config.DeviceID)
	base.RawQuery = query.Encode()
	header := http.Header{
		"X-CC-Contract-Hash": []string{runtimeprotocol.ContractHash},
		"X-CC-Challenge":     []string{challenge},
		"X-CC-Signature":     []string{base64.RawURLEncoding.EncodeToString(signature)},
	}
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, base.String(), header)
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUpgradeRequired {
			return errors.New("runtime client: update_required")
		}
		return fmt.Errorf("runtime client: connect: %w", err)
	}
	generation, err := strconv.ParseUint(response.Header.Get("X-CC-Connection-Generation"), 10, 64)
	if err != nil || generation == 0 {
		_ = connection.Close()
		return errors.New("runtime client: control did not assign a connection generation")
	}
	if err := c.configureConnectionLiveness(connection); err != nil {
		_ = connection.Close()
		return err
	}
	c.mu.Lock()
	c.connection, c.generation, c.sequence = connection, generation, 0
	c.mu.Unlock()
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		c.watchCatalog(connectionCtx, 15*time.Second)
	}()
	go func() {
		defer workers.Done()
		c.keepConnectionAlive(connectionCtx, connection, generation)
	}()
	go func() {
		defer workers.Done()
		<-connectionCtx.Done()
		_ = connection.Close()
	}()
	defer func() {
		cancelConnection()
		c.mu.Lock()
		if c.connection == connection {
			c.connection = nil
		}
		c.mu.Unlock()
		_ = connection.Close()
		workers.Wait()
		c.config.Handler.ReleaseConnection()
	}()
	var guard runtimeprotocol.SequenceGuard
	for {
		_, raw, err := connection.ReadMessage()
		if err != nil {
			return fmt.Errorf("runtime client: read: %w", err)
		}
		envelope, err := runtimeprotocol.Decode(raw)
		if err != nil {
			return err
		}
		if envelope.DeviceID != c.config.DeviceID || envelope.ConnectionGeneration != generation {
			return errors.New("runtime client: envelope identity does not match connection")
		}
		if err := guard.Accept(envelope); err != nil {
			return err
		}
		if envelope.Method == runtimeprotocol.MethodAcknowledge && envelope.RequestID == "" {
			acknowledgement, err := runtimeprotocol.DecodePayload[runtimeprotocol.Acknowledgement](envelope)
			if err != nil || acknowledgement.ConfirmedSequence == 0 {
				return errors.New("runtime client: invalid acknowledgement")
			}
			if err := c.config.Checkpoint.Confirm(generation, acknowledgement.ConfirmedSequence); err != nil {
				return fmt.Errorf("runtime client: persist acknowledgement: %w", err)
			}
			continue
		}
		if envelope.RequestID == "" {
			return errors.New("runtime client: control sent an unsolicited request")
		}
		workers.Add(1)
		go func(request runtimeprotocol.Envelope) {
			defer workers.Done()
			c.respond(connectionCtx, connection, generation, request)
		}(envelope)
	}
}

func (c *Client) configureConnectionLiveness(connection *websocket.Conn) error {
	if err := connection.SetReadDeadline(time.Now().Add(c.pongWait)); err != nil {
		return fmt.Errorf("runtime client: set pong deadline: %w", err)
	}
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(c.pongWait))
	})
	return nil
}

func (c *Client) keepConnectionAlive(ctx context.Context, connection *websocket.Conn, generation uint64) {
	ticker := time.NewTicker(c.pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.pingForConnection(connection, generation); err != nil {
				_ = connection.Close()
				return
			}
		}
	}
}

func (c *Client) pingForConnection(connection *websocket.Conn, generation uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connection == nil || c.connection != connection || c.generation != generation {
		return errors.New("runtime client: stale connection generation")
	}
	return connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(runtimePingWriteTimeout))
}

func (c *Client) watchCatalog(ctx context.Context, interval time.Duration) {
	previous, _ := c.config.Handler.CatalogSnapshot(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := c.config.Handler.CatalogSnapshot(ctx)
			if err != nil {
				continue
			}
			if previous == nil {
				previous = current
				continue
			}
			if bytes.Equal(previous, current) {
				continue
			}
			previous = current
			if err := c.emit(runtimeprotocol.MethodCatalogChanged, runtimeprotocol.Resource{}, current); err != nil {
				return
			}
		}
	}
}

func (c *Client) challenge(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"device_id": c.config.DeviceID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.ServerURL+"/runtime/v1/connect", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("runtime client: request challenge: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Challenge    string `json:"challenge"`
			ContractHash string `json:"contract_hash"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return "", err
	}
	if !envelope.OK {
		return "", fmt.Errorf("runtime client: challenge rejected: %s", envelope.Error)
	}
	if envelope.Data.ContractHash != runtimeprotocol.ContractHash {
		return "", errors.New("runtime client: update_required")
	}
	return envelope.Data.Challenge, nil
}

func (c *Client) fetchAttachment(ctx context.Context, ref string) (runtimeprotocol.AttachmentContent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return runtimeprotocol.AttachmentContent{}, errors.New("runtime client: attachment reference is required")
	}
	nonceRaw := make([]byte, 18)
	if _, err := rand.Read(nonceRaw); err != nil {
		return runtimeprotocol.AttachmentContent{}, fmt.Errorf("runtime client: generate attachment nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature := ed25519.Sign(c.config.PrivateKey, runtimeprotocol.SignedRequestMessage(
		runtimeprotocol.SignedPurposeAttachmentDownload, c.config.DeviceID, ref, timestamp, nonce,
	))
	base, _ := url.Parse(c.config.ServerURL)
	base.Path = "/runtime/v1/attachments/" + url.PathEscape(ref)
	base.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return runtimeprotocol.AttachmentContent{}, err
	}
	request.Header.Set("X-CC-Contract-Hash", runtimeprotocol.ContractHash)
	request.Header.Set("X-CC-Device-ID", c.config.DeviceID)
	request.Header.Set("X-CC-Timestamp", timestamp)
	request.Header.Set("X-CC-Nonce", nonce)
	request.Header.Set("X-CC-Signature", base64.RawURLEncoding.EncodeToString(signature))
	response, err := c.http.Do(request)
	if err != nil {
		return runtimeprotocol.AttachmentContent{}, fmt.Errorf("runtime client: download attachment: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	var envelope struct {
		OK    bool                              `json:"ok"`
		Data  runtimeprotocol.AttachmentContent `json:"data"`
		Error string                            `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 72<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return runtimeprotocol.AttachmentContent{}, fmt.Errorf("runtime client: decode attachment response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return runtimeprotocol.AttachmentContent{}, errors.New("runtime client: attachment response contains trailing JSON")
	}
	if !envelope.OK || response.StatusCode != http.StatusOK {
		return runtimeprotocol.AttachmentContent{}, fmt.Errorf("runtime client: attachment rejected: %s", envelope.Error)
	}
	if len(envelope.Data.Data) == 0 || len(envelope.Data.Data) > maxRuntimeAttachmentSize {
		return runtimeprotocol.AttachmentContent{}, errors.New("runtime client: attachment payload size is invalid")
	}
	return envelope.Data, nil
}

func (c *Client) respond(ctx context.Context, connection *websocket.Conn, generation uint64, request runtimeprotocol.Envelope) {
	payload, err := c.config.Handler.Handle(ctx, request.Method, request.Resource, request.Payload)
	response := runtimeprotocol.Envelope{RequestID: request.RequestID, Method: request.Method, Resource: request.Resource, Payload: payload}
	if err != nil {
		response.Error = &runtimeprotocol.RPCError{Code: "operation_failed", Message: err.Error()}
		response.Payload = nil
	}
	_ = c.writeForConnection(response, connection, generation)
}

func (c *Client) emit(method runtimeprotocol.Method, resource runtimeprotocol.Resource, payload json.RawMessage) error {
	return c.write(runtimeprotocol.Envelope{Method: method, Resource: resource, Payload: payload})
}

func (c *Client) write(envelope runtimeprotocol.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connection == nil {
		return errors.New("runtime client: device_offline")
	}
	return c.writeLocked(envelope)
}

func (c *Client) writeForConnection(envelope runtimeprotocol.Envelope, connection *websocket.Conn, generation uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connection == nil || c.connection != connection || c.generation != generation {
		return errors.New("runtime client: stale connection generation")
	}
	return c.writeLocked(envelope)
}

func (c *Client) writeLocked(envelope runtimeprotocol.Envelope) error {
	c.sequence++
	envelope.ContractHash = runtimeprotocol.ContractHash
	envelope.DeviceID = c.config.DeviceID
	envelope.ConnectionGeneration = c.generation
	envelope.Sequence = c.sequence
	if envelope.RequestID == "" {
		if err := c.config.Checkpoint.RecordUnconfirmed(c.generation, c.sequence, envelope.Method, envelope.Resource, envelope.Payload); err != nil {
			return fmt.Errorf("runtime client: persist unconfirmed event: %w", err)
		}
	}
	return c.connection.WriteJSON(envelope)
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	connection := c.connection
	c.connection = nil
	c.mu.Unlock()
	c.config.Handler.Close()
	if connection != nil {
		return connection.Close()
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
