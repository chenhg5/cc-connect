// Package googlechat connects cc-connect to Google Chat.
//
// Google Chat has no native socket/long-poll inbound for self-hosted apps
// without a public endpoint. This adapter therefore drives the `gws`
// (google-workspace-cli) binary as a subprocess:
//
//   - receive: `gws events +subscribe` pulls Workspace Events from a Pub/Sub
//     topic and emits one JSON event per line on stdout (no public IP needed).
//   - send:    `gws chat spaces messages create` posts a (optionally threaded)
//     reply back into the space.
//
// Wrapping a binary deviates from cc-connect's "platforms should be native Go"
// norm; the core.Platform interface is unchanged so the engine can later be
// swapped to a native client without touching core/ or agent/.
package googlechat

import (
	"context"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

const createdEventType = "google.workspace.chat.message.v1.created"

func init() {
	core.RegisterPlatform("googlechat", New)
}

// replyContext carries the platform-specific data needed to reply: the space
// to post into and, when known, the thread to reply within.
type replyContext struct {
	space  string // e.g. "spaces/AAAA"
	thread string // e.g. "spaces/AAAA/threads/XXXX" (empty = top-level)
}

type Platform struct {
	gwsPath         string
	project         string
	target          string
	subscription    string
	allowFrom       string
	trigger         string
	sessionScope    string // "space" (default) | "thread" | "user"
	credentialsFile string

	handler core.MessageHandler
	cancel  context.CancelFunc
}

// New builds a Google Chat platform from config options.
func New(opts map[string]any) (core.Platform, error) {
	gwsPath, _ := opts["gws_path"].(string)
	if strings.TrimSpace(gwsPath) == "" {
		gwsPath = "gws"
	}
	project, _ := opts["project"].(string)
	target, _ := opts["target"].(string)
	if strings.TrimSpace(target) == "" {
		target = "//chat.googleapis.com/users/me"
	}
	subscription, _ := opts["subscription"].(string)
	allowFrom, _ := opts["allow_from"].(string)
	trigger, _ := opts["trigger"].(string)
	credentialsFile, _ := opts["credentials_file"].(string)

	if strings.TrimSpace(project) == "" && strings.TrimSpace(subscription) == "" {
		return nil, fmt.Errorf("googlechat: project is required (or set subscription to reuse an existing one)")
	}

	core.CheckAllowFrom("googlechat", allowFrom)

	return &Platform{
		gwsPath:         gwsPath,
		project:         project,
		target:          target,
		subscription:    subscription,
		allowFrom:       allowFrom,
		trigger:         trigger,
		sessionScope:    normalizeSessionScope(opts["session_scope"]),
		credentialsFile: credentialsFile,
	}, nil
}

// normalizeSessionScope resolves session_scope to "space" | "thread" | "user",
// defaulting to "space".
func normalizeSessionScope(raw any) string {
	s, _ := raw.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "thread":
		return "thread"
	case "user":
		return "user"
	case "space", "":
		return "space"
	default:
		return "space"
	}
}

func (p *Platform) Name() string { return "googlechat" }

func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	return nil
}

func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	return nil
}

func (p *Platform) Send(ctx context.Context, rctx any, content string) error {
	return nil
}

func (p *Platform) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}
