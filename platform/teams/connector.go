package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// connectorTimeout bounds outbound Bot Connector calls so a slow or
// attacker-controlled serviceURL cannot hang a request goroutine indefinitely.
const connectorTimeout = 30 * time.Second

// errActivityTooLarge is returned when the Bot Connector rejects an activity as
// too large (HTTP 413). It lets callers (SendImage) degrade to a user notice on
// the real service limit rather than guessing a byte cap up front.
var errActivityTooLarge = errors.New("teams: activity too large (413)")

// outboundActivity is the JSON body POSTed/PUT to the Bot Connector. The
// from/recipient/conversation envelope mirrors what the Bot Framework SDKs send
// on every outbound activity (apply_conversation_reference).
type outboundActivity struct {
	Type         string               `json:"type"` // "message"
	Text         string               `json:"text,omitempty"`
	ID           string               `json:"id,omitempty"`
	From         *channelAccount      `json:"from,omitempty"`
	Recipient    *channelAccount      `json:"recipient,omitempty"`
	Conversation *conversationAccount `json:"conversation,omitempty"`
	Attachments  []attachment         `json:"attachments,omitempty"`
	Entities     []map[string]any     `json:"entities,omitempty"`
}

// attachment carries either a card payload or an inline media payload. For
// Adaptive Cards ContentType is "application/vnd.microsoft.card.adaptive" and
// Content is the card object. For an inline image ContentType is the image mime
// (e.g. "image/png"), ContentURL is a "data:<mime>;base64,<...>" URI, and Name
// is the filename — the Bot Framework inline-attachment shape.
type attachment struct {
	ContentType string `json:"contentType"`
	Content     any    `json:"content,omitempty"`
	ContentURL  string `json:"contentUrl,omitempty"`
	Name        string `json:"name,omitempty"`
}

// newMessageActivity builds a message activity with the conversation-reference
// envelope (bot as sender, user as recipient, conversation id) populated from
// the reply context.
func newMessageActivity(rc replyContext, text string) outboundActivity {
	a := newActivity(rc, "message")
	a.Text = text
	return a
}

// newActivity builds an activity of the given type with the conversation-reference
// envelope (bot as sender, user as recipient, conversation id) from the reply
// context.
func newActivity(rc replyContext, typ string) outboundActivity {
	a := outboundActivity{Type: typ}
	if rc.botAccount.ID != "" {
		bot := rc.botAccount
		a.From = &bot
	}
	if rc.userAccount.ID != "" {
		user := rc.userAccount
		a.Recipient = &user
	}
	if rc.conversationID != "" {
		a.Conversation = &conversationAccount{ID: rc.conversationID}
	}
	return a
}

// connector posts activities to the Bot Connector REST API at the per-activity
// serviceUrl, authenticated with a client-credentials bearer token.
type connector struct {
	tokens tokenSource
	http   *http.Client
}

func newConnector(tokens tokenSource) *connector {
	return &connector{tokens: tokens, http: &http.Client{Timeout: connectorTimeout}}
}

// resourceResponse is the Bot Connector reply to a send; ID identifies the
// created activity (used to address in-place edits for streaming preview).
type resourceResponse struct {
	ID string `json:"id"`
}

// send POSTs a new activity to {serviceURL}/v3/conversations/{conversationID}/activities
// and returns the created activity id.
func (c *connector) send(ctx context.Context, rc replyContext, a outboundActivity) (string, error) {
	if rc.serviceURL == "" || rc.conversationID == "" {
		return "", fmt.Errorf("teams: reply context missing serviceURL/conversationID")
	}
	url := fmt.Sprintf("%s/v3/conversations/%s/activities",
		strings.TrimRight(rc.serviceURL, "/"), rc.conversationID)
	return c.sendTo(ctx, url, a)
}

// replyTo POSTs a threaded reply to .../activities/{activityID}, the Bot
// Framework reply-to-activity endpoint (threading is keyed by the URL, not a
// body field).
func (c *connector) replyTo(ctx context.Context, rc replyContext, activityID string, a outboundActivity) error {
	if rc.serviceURL == "" || rc.conversationID == "" || activityID == "" {
		return fmt.Errorf("teams: replyTo missing serviceURL/conversationID/activityID")
	}
	url := fmt.Sprintf("%s/v3/conversations/%s/activities/%s",
		strings.TrimRight(rc.serviceURL, "/"), rc.conversationID, activityID)
	_, err := c.sendTo(ctx, url, a)
	return err
}

// update PUTs an edited activity to .../activities/{activityID}, editing the
// message in place (used for streaming preview).
func (c *connector) update(ctx context.Context, rc replyContext, activityID string, a outboundActivity) error {
	if rc.serviceURL == "" || rc.conversationID == "" || activityID == "" {
		return fmt.Errorf("teams: update missing serviceURL/conversationID/activityID")
	}
	a.ID = activityID
	url := fmt.Sprintf("%s/v3/conversations/%s/activities/%s",
		strings.TrimRight(rc.serviceURL, "/"), rc.conversationID, activityID)
	_, err := c.do(ctx, http.MethodPut, url, a)
	return err
}

func (c *connector) sendTo(ctx context.Context, url string, a outboundActivity) (string, error) {
	body, err := c.do(ctx, http.MethodPost, url, a)
	if err != nil {
		return "", err
	}
	var rr resourceResponse
	_ = json.Unmarshal(body, &rr) // id is best-effort
	return rr.ID, nil
}

// fetchOutcome classifies an attachment download so the caller can tell a usable
// payload apart from an oversize or failed one (the latter two drive a user notice).
type fetchOutcome int

const (
	fetchOK       fetchOutcome = iota // download succeeded within the size cap
	fetchOversize                     // payload exceeded maxBytes; skipped
	fetchFailed                       // request/transport/status error; skipped
)

// fetch GETs rawURL and returns its bytes bounded by maxBytes. A bearer token is
// attached only when withToken is true (file downloadUrls are pre-authenticated
// and must NOT carry the token; images on the Bot Connector host require it). A
// payload larger than maxBytes is reported as fetchOversize rather than truncated,
// and any transport/status error as fetchFailed — neither is fatal to the turn.
func (c *connector) fetch(ctx context.Context, rawURL string, withToken bool, maxBytes int64) ([]byte, fetchOutcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fetchFailed
	}
	if withToken {
		token, err := c.tokens.token(ctx)
		if err != nil {
			return nil, fetchFailed
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fetchFailed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fetchFailed
	}
	// Read one byte past the cap so a payload exactly at the limit still succeeds
	// while anything larger is detected as oversize.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fetchFailed
	}
	if int64(len(data)) > maxBytes {
		return nil, fetchOversize
	}
	return data, fetchOK
}

func (c *connector) do(ctx context.Context, method, url string, a outboundActivity) ([]byte, error) {
	payload, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	token, err := c.tokens.token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("teams: connector %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return nil, fmt.Errorf("teams: connector returned 413: %w", errActivityTooLarge)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teams: connector returned %d", resp.StatusCode)
	}
	return body, nil
}
