package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type cardActionPayload struct {
	Action    string         `json:"action"`
	UserID    string         `json:"user_id"`
	ChatID    string         `json:"chat_id"`
	MessageID string         `json:"message_id"`
	Value     map[string]any `json:"value,omitempty"`
	Form      any            `json:"form_value,omitempty"`
	Option    string         `json:"option,omitempty"`
}

func parseCardActionWebhook(raw any) (string, error) {
	if raw == nil || raw == "" {
		return "", nil
	}
	endpoint, ok := raw.(string)
	u, err := url.Parse(endpoint)
	if !ok || err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return "", errors.New("card_action_webhook requires a loopback HTTP URL")
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return "", errors.New("card_action_webhook requires a numeric loopback address")
	}
	return endpoint, nil
}

var cardWebhookClient = &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func forwardCardAction(endpoint string, action cardActionPayload) (*callback.CardActionTriggerResponse, error) {
	body, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}
	res, err := cardWebhookClient.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("card action webhook request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("card action webhook rejected request")
	}
	const maxSize = 256 << 10
	data, err := io.ReadAll(io.LimitReader(res.Body, maxSize+1))
	if err != nil || len(data) > maxSize {
		return nil, errors.New("invalid card action webhook response size")
	}
	var response callback.CardActionTriggerResponse
	if err = json.Unmarshal(data, &response); err != nil || response.Card == nil && response.Toast == nil {
		return nil, errors.New("card action webhook must return a toast or a card")
	}
	return &response, nil
}
