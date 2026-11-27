package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type startupGuidanceError struct {
	message string
	err     error
}

func (err *startupGuidanceError) Error() string       { return err.err.Error() }
func (err *startupGuidanceError) Unwrap() error       { return err.err }
func (err *startupGuidanceError) UserMessage() string { return err.message }

func TestReceiveMessage_StartFailureShowsOnlySafeGuidance(t *testing.T) {
	diagnostic := errors.New("private startup diagnostic")
	guidance := &startupGuidanceError{message: "Upgrade the configured CLI.", err: diagnostic}
	for _, test := range []struct {
		name       string
		initialErr error
		finalErr   error
		want       string
	}{
		{name: "safe_guidance", initialErr: guidance, want: guidance.message},
		{name: "wrapped_guidance", initialErr: fmt.Errorf("start: %w", guidance), want: guidance.message},
		{name: "arbitrary_error_stays_generic", initialErr: diagnostic},
		{name: "empty_guidance_stays_generic", initialErr: &startupGuidanceError{err: diagnostic}},
		{name: "fresh_retry_guidance", initialErr: diagnostic, finalErr: guidance, want: guidance.message},
		{name: "fresh_retry_drops_old_guidance", initialErr: guidance, finalErr: diagnostic},
	} {
		t.Run(test.name, func(t *testing.T) {
			var starts atomic.Int32
			agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) {
				if starts.Add(1) > 1 {
					return nil, test.finalErr
				}
				return nil, test.initialErr
			}}
			platform := &stubPlatformEngine{n: "test"}
			engine := NewEngine("startup-guidance", agent, []Platform{platform}, "", LangEnglish)
			t.Cleanup(func() { _ = engine.Stop() })
			key := "test:startup-user"
			if test.finalErr != nil {
				engine.GetSessions().GetOrCreateActive(key).SetAgentSessionID("resume-id", agent.Name())
			}
			engine.ReceiveMessage(platform, &Message{
				SessionKey: key, Platform: platform.Name(), UserID: "startup-user",
				MessageID: "startup", Content: "start the task", ReplyCtx: "startup",
			})
			sent := waitForPlatformSend(platform, 1, 3*time.Second)
			want := test.want
			if want == "" {
				want = engine.i18n.T(MsgFailedToStartAgentSession)
			}
			if len(sent) != 1 || sent[0] != want {
				t.Fatalf("startup reply = %q, want %q", sent, want)
			}
			if strings.Contains(sent[0], diagnostic.Error()) {
				t.Fatalf("startup reply exposed diagnostics: %q", sent[0])
			}
			wantStarts := int32(1)
			if test.finalErr != nil {
				wantStarts = 2
			}
			if got := starts.Load(); got != wantStarts {
				t.Fatalf("StartSession calls = %d, want %d", got, wantStarts)
			}
		})
	}
}
