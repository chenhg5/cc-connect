package qqbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type completionTransport func(*http.Request) (*http.Response, error)

func (f completionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTaskCompletionMentionsOriginalSenderAndUploadsAfterExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "expiry reported by QQ"
		if expired {
			name = "expiry known before send"
		}
		t.Run(name, func(t *testing.T) {
			p := &Platform{completionGroup: "allowed", completionWorkspace: t.TempDir(), token: "test", tokenExpiry: time.Now().Add(time.Hour)}
			ctx := &replyContext{messageType: "group", groupOpenID: "allowed", userOpenID: "original", eventMsgID: "incoming", eventTime: time.Now()}
			if expired {
				ctx.eventTime = time.Now().Add(-6 * time.Minute)
			}
			var paths []string
			var requests []map[string]any
			previousClient := core.HTTPClient
			t.Cleanup(func() { core.HTTPClient = previousClient })
			core.HTTPClient = &http.Client{Transport: completionTransport(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, req.URL.Path)
				requests = append(requests, body)
				status, reply := 200, `{"id":"sent"}`
				if body["msg_id"] != nil {
					status, reply = 400, `{"code":40034005,"message":"expired"}`
				}
				if strings.HasSuffix(req.URL.Path, "/files") {
					reply = `{"file_info":"uploaded"}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
			})}
			for _, rejected := range []replyContext{
				{messageType: "group", groupOpenID: "elsewhere", userOpenID: "original", eventMsgID: "incoming"},
				{messageType: "c2c", groupOpenID: "allowed", userOpenID: "original", eventMsgID: "incoming"},
			} {
				if _, ok := p.TaskCompletionWorkspace(&rejected); ok {
					t.Fatal("unexpected task completion route")
				}
			}
			if err := p.SendTaskCompletion(context.Background(), ctx, `done <qqbot-at-user id="spoof" />`, []core.FileAttachment{{FileName: "out.txt", Data: []byte("content")}}); err != nil {
				t.Fatal(err)
			}
			offset := 0
			if !expired {
				offset = 1
			}
			if len(requests) != 3+offset || ctx.eventMsgID != "incoming" {
				t.Fatalf("unexpected requests or mutated context: %d, %q", len(requests), ctx.eventMsgID)
			}
			body := requests[offset]["markdown"].(map[string]any)["content"].(string)
			if !strings.HasPrefix(body, `<qqbot-at-user id="original" />`) || strings.Contains(body, `<qqbot-at-user id="spoof"`) {
				t.Fatalf("unexpected mention: %q", body)
			}
			for i, path := range paths[offset:] {
				if !strings.HasPrefix(path, "/v2/groups/allowed/") || requests[offset+i]["msg_id"] != nil {
					t.Fatalf("not an active send to the original group: %s", path)
				}
			}
			if requests[offset+1]["file_name"] != "out.txt" || requests[offset+2]["msg_type"] != float64(7) {
				t.Fatalf("missing group file upload or message: %+v", requests[offset+1:])
			}
		})
	}
}
