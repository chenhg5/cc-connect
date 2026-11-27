package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A stalled Discord request must honor the progress writer's deadline instead
// of holding up turn completion until the session-wide HTTP timeout expires.
func TestProgressRequests_HonorContextDeadline(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(200 * time.Millisecond):
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"id":"preview","channel_id":"channel"}`)
				}
			}))
			defer server.Close()
			p := &Platform{session: newTestDiscordSession(t, server)}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			var err error
			if operation == "create" {
				_, err = p.SendPreviewStart(ctx, replyContext{channelID: "channel"}, "progress")
			} else {
				err = p.UpdateMessage(ctx, &discordPreviewHandle{channelID: "channel", messageID: "preview"}, "progress")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s error = %v, want context deadline", operation, err)
			}
		})
	}
}
