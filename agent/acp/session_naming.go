package acp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.NativeSessionNamer = (*acpSession)(nil)

func (s *acpSession) SupportsSessionNaming() bool { return s.nativeNamer != nil }

func (s *acpSession) SetSessionName(ctx context.Context, name string) error {
	if s.nativeNamer == nil {
		return fmt.Errorf("acp: native session naming is unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	return s.nativeNamer(ctx, s.CurrentSessionID(), name)
}

// ACP has no standard rename request. Trae exposes the same persisted session
// through its native App Server API. Use a metadata-only connection: never
// start/resume a thread, send a prompt, or touch the active ACP transport.
// Capture the executable, arguments and environment belonging to this session.
func traeSessionNamer(command string, args, env []string, dir string) func(context.Context, string, string) error {
	if !strings.EqualFold(strings.TrimSuffix(filepath.Base(command), ".exe"), "traecli") {
		return nil
	}
	acpIndex := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "acp" && args[i+1] == "serve" {
			acpIndex = i
			break
		}
	}
	if acpIndex < 0 {
		return nil
	}
	probe := &Agent{cmd: command, args: append(append([]string{}, args[:acpIndex]...), "app-server"), extraEnv: append([]string(nil), env...)}
	return func(ctx context.Context, id, name string) error {
		if id == "" {
			return fmt.Errorf("trae: cannot name a session before its ID is known")
		}
		tr, _, teardown, err := probe.probeSpawn(ctx, dir)
		if err != nil {
			return fmt.Errorf("trae: session naming connection: %w", err)
		}
		defer teardown()
		if _, err := tr.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "cc-connect-session-names", "version": "1.0"}}); err != nil {
			return fmt.Errorf("trae: session naming initialize: %w", err)
		}
		if err := tr.sendNotification("initialized", map[string]any{}); err != nil {
			return fmt.Errorf("trae: session naming initialized: %w", err)
		}
		if _, err := tr.call(ctx, "thread/name/set", map[string]string{"threadId": id, "name": name}); err != nil {
			return fmt.Errorf("trae: set session name: %w", err)
		}
		return nil
	}
}
