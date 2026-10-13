package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Block the actual outbound update, then click the card while it is in flight.
// The callback must acknowledge within the platform deadline and finish later.
func TestExecutionClickDuringUpdateIsProcessedInsteadOfRejected(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(fmt.Sprint(approval), func(t *testing.T) {
			p, f := newSplitCardFixture(t, true)
			c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_private", sessionKey: "feishu:oc_private:owner"}}
			cleanupExecutionStream(t, c)
			if err := c.UpdateExecution(context.Background(), []core.ExecutionBlock{{Tool: "Bash", Command: "build", Output: "LIVE_OUTPUT"}}); err != nil {
				t.Fatal(err)
			}
			part := c.cards[0]
			if approval {
				prompt := core.NewCard().Title("Approve", "orange").Build()
				prompt.Elements = []core.CardElement{core.CardActions{Buttons: []core.CardButton{{Text: "Allow", Value: "perm:allow", Extra: map[string]string{"query_card": "true", "permission_token": "current-request"}}}}}
				if err := c.SetPrompt(context.Background(), prompt); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			f.beforeUpdate = func() { once.Do(func() { close(entered); <-release }) }
			updated := make(chan error, 1)
			go func() { updated <- c.SetNotice(context.Background(), "stream update") }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("update did not start")
			}
			received := make(chan *core.Message, 1)
			p.userNameCache.Store("owner", "Owner")
			p.chatNameCache.Store("oc_private", "Project")
			p.handler = func(_ core.Platform, m *core.Message) { received <- m }
			action := fmt.Sprintf("exec:%s:open:b0:0", part.id)
			click := panelClick("om_1", action)
			if approval {
				click.Event.Action.Value = map[string]any{"action": "perm:allow", "query_card": "true", "execution_card": part.id, "permission_token": "current-request"}
			}
			started := time.Now()
			resp, err := p.onCardAction(click)
			if time.Since(started) > 2*time.Second {
				t.Error("callback waited for the streaming HTTP request")
			}
			releaseOnce.Do(func() { close(release) })
			if err != nil {
				t.Fatal(err)
			}
			if resp != nil && resp.Toast != nil && resp.Toast.Type == "warning" {
				t.Fatalf("valid click rejected: %s", resp.Toast.Content)
			}
			if err := <-updated; err != nil {
				t.Fatal(err)
			}
			if approval {
				select {
				case m := <-received:
					if m.PermissionToken != "current-request" || m.Content != "allow" {
						t.Fatal("wrong approval dispatched")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("approval lost during update")
				}
			} else {
				deadline := time.Now().Add(3 * time.Second)
				for !strings.Contains(f.shown("om_1"), "LIVE_OUTPUT") && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if !strings.Contains(f.shown("om_1"), "LIVE_OUTPUT") {
					t.Fatal("details never opened after streaming update")
				}
			}
		})
	}
}

func TestExecutionCommandRowsUseCompactSpacing(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_private"}}
	cleanupExecutionStream(t, c)
	if err := c.UpdateExecution(context.Background(), []core.ExecutionBlock{{Text: "Inspect", Done: true}, {Tool: "Bash", Command: "first"}, {Tool: "Bash", Command: "second"}}); err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal([]byte(f.shown("om_1")), &card); err != nil {
		t.Fatal(err)
	}
	body := card["body"].(map[string]any)
	if body["vertical_spacing"] != "4px" {
		t.Fatal("command rows inherit large default card spacing")
	}
	commands := 0
	for _, e := range body["elements"].([]any) {
		row := e.(map[string]any)
		content, _ := row["content"].(string)
		if strings.Contains(content, "`first`") || strings.Contains(content, "`second`") {
			commands++
			if row["tag"] != "markdown" || row["margin"] != "0px" || row["behaviors"] != nil {
				t.Fatal("commands retain padded or interactive rows")
			}
		}
	}
	if commands != 2 {
		t.Fatal("command previews were dropped")
	}
}

func TestExecutionCallbackFilteredPlatformsDoNotConsumeSharedEvent(t *testing.T) {
	for _, filter := range []string{"chat", "user"} {
		t.Run(filter, func(t *testing.T) {
			p, _ := newSplitCardFixture(t, true)
			if filter == "chat" {
				p.allowChat = "oc_other"
			} else {
				p.allowFrom = "other-user"
			}
			resp, err := p.onCardAction(panelClick("om_1", "exec:unowned:open:b0:0"))
			if err != nil || resp != nil {
				t.Fatal("filtered platform consumed a callback intended for another platform")
			}
		})
	}
}
