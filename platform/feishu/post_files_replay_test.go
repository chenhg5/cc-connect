package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

// Opt-in read-only replay: no messages are sent and no agent is invoked.
// Credentials stay in the test process environment, never in fixtures/logs.
func TestPostFilesLiveReplay(t *testing.T) {
	manifest := os.Getenv("POST_FILES_REPLAY_MANIFEST")
	if manifest == "" {
		t.Skip("live replay not requested")
	}
	var cases []struct{ MessageID, BodyPath, SHA256 string }
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	id, secret := os.Getenv("POST_FILES_APP_ID"), os.Getenv("POST_FILES_APP_SECRET")
	if id == "" || secret == "" {
		t.Fatal("replay credentials missing")
	}
	for _, tc := range cases {
		t.Run(tc.MessageID, func(t *testing.T) {
			body, err := os.ReadFile(tc.BodyPath)
			if err != nil {
				t.Fatal(err)
			}
			var got *core.Message
			p := &Platform{platformName: "feishu", client: lark.NewClient(id, secret), handler: func(_ core.Platform, m *core.Message) { got = m }}
			p.dispatchMessage(context.Background(), "post", string(body), nil, tc.MessageID, "replay", "", "", replyContext{}, "", 0)
			if got == nil || len(got.Files) != 1 {
				t.Fatal("real attachment did not reach core handler")
			}
			paths := core.SaveFilesToDisk(t.TempDir(), tc.MessageID, got.Files)
			if len(paths) != 1 {
				t.Fatal("attachment not saved")
			}
			saved, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(saved)
			if hex.EncodeToString(digest[:]) != tc.SHA256 {
				t.Fatal("downloaded/saved attachment differs from original")
			}
			prompt := core.AppendFileRefs(got.Content, paths)
			if !strings.Contains(prompt, paths[0]) {
				t.Fatal("agent prompt omitted attachment path")
			}
			t.Logf("verified real API download, core dispatch, local save and agent file reference; bytes=%d", len(saved))
		})
	}
}
