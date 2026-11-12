package grokbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/require"
)

const testKey = "crsr_test_key_do_not_log_9f3a"

func TestMain(m *testing.M) {
	// Keep the package quiet unless a test installs its own handler.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func baseOpts(t *testing.T) map[string]any {
	t.Helper()
	t.Setenv(defaultKeyEnv, testKey)
	return map[string]any{
		"webhook_url": "https://example.test/automations/webhook/routine",
		"callback":    false,
		"outbox":      true,
		"outbox_dir":  t.TempDir(),
		"work_dir":    t.TempDir(),
		"timeout":     "1s",
	}
}

func mustAgent(t *testing.T, opts map[string]any) *Agent {
	t.Helper()
	a, err := New(opts)
	require.NoError(t, err)
	ag := a.(*Agent)
	t.Cleanup(func() { _ = ag.Stop() })
	return ag
}

func TestNew_Validation(t *testing.T) {
	_, err := New(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "webhook_url")

	t.Setenv(defaultKeyEnv, "")
	_, err = New(map[string]any{
		"webhook_url": "https://example.test/hook",
		"callback":    false,
		"outbox":      true,
		"outbox_dir":  t.TempDir(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), defaultKeyEnv)
	require.NotContains(t, err.Error(), "crsr_")

	t.Setenv(defaultKeyEnv, testKey)
	_, err = New(map[string]any{
		"webhook_url": "https://example.test/hook",
		"webhook_key": "super-secret-value",
		"callback":    false,
		"outbox":      true,
		"outbox_dir":  t.TempDir(),
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "super-secret-value")
	require.Contains(t, err.Error(), defaultKeyEnv)

	_, err = New(map[string]any{
		"webhook_url": "not a url",
		"callback":    false,
		"outbox":      true,
		"outbox_dir":  t.TempDir(),
	})
	require.Error(t, err)

	_, err = New(map[string]any{
		"webhook_url": "https://example.test/hook",
		"callback":    false,
		"outbox":      false,
	})
	require.Error(t, err)

	_, err = New(map[string]any{
		"webhook_url": "https://example.test/hook",
		"callback":    false,
		"outbox":      true,
		"outbox_dir":  t.TempDir(),
		"timeout":     "nope",
	})
	require.Error(t, err)
}

func TestNew_TimeoutMinsAndDoctor(t *testing.T) {
	opts := baseOpts(t)
	delete(opts, "timeout")
	opts["timeout_mins"] = int64(3)
	logs := captureLogs(t)
	ag := mustAgent(t, opts)
	require.Equal(t, 3*time.Minute, ag.timeout)
	require.Equal(t, "grokbot", ag.Name())
	require.Empty(t, ag.CLIBinaryName())
	require.Equal(t, "Grok Bot", ag.CLIDisplayName())

	checks := ag.DoctorChecks(context.Background())
	require.NotEmpty(t, checks)
	blob := logs.String()
	for _, c := range checks {
		blob += c.Detail
		require.NotContains(t, c.Detail, testKey)
	}
	require.NotContains(t, blob, testKey)

	sessions, err := ag.ListSessions(context.Background())
	require.NoError(t, err)
	require.Nil(t, sessions)
}

func TestSend_HTTPReplyWithImages(t *testing.T) {
	logs := captureLogs(t)
	png := tinyPNG(t)
	work := t.TempDir()

	type shot struct {
		mu   sync.Mutex
		body webhookBody
		auth string
	}
	got := &shot{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		got.mu.Lock()
		got.auth = r.Header.Get("Authorization")
		got.body = body
		got.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"success": true, "runUuid": "run-123",
		}))
		go postReply(t, body.Reply, `{"text":"hello from grok","images":[{"name":"out.png","mime":"image/png","data_base64":"`+base64.StdEncoding.EncodeToString(png)+`"}]}`)
	}))
	t.Cleanup(hook.Close)

	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	opts["embed_images"] = true
	opts["work_dir"] = work
	opts["timeout"] = "5s"
	ag := mustAgent(t, opts)

	sess, err := ag.StartSession(context.Background(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	require.NotEmpty(t, sess.CurrentSessionID())

	err = sess.Send("look", "msg-1", []core.ImageAttachment{{
		MimeType: "image/png",
		Data:     png,
		FileName: "in.png",
	}}, nil)
	require.NoError(t, err)

	events := readEvents(t, sess.Events(), 2)
	require.Equal(t, core.EventText, events[0].Type)
	require.Equal(t, "hello from grok", events[0].Content)
	require.Equal(t, core.EventResult, events[1].Type)
	require.True(t, events[1].Done)
	require.Equal(t, "hello from grok", events[1].Content)
	require.Len(t, events[1].Images, 1)
	require.Equal(t, png, events[1].Images[0].Data)
	require.Equal(t, "out.png", events[1].Images[0].FileName)
	require.Equal(t, sess.CurrentSessionID(), events[1].SessionID)

	got.mu.Lock()
	defer got.mu.Unlock()
	require.Equal(t, "Bearer "+testKey, got.auth)
	require.Equal(t, "cc-connect", got.body.Source)
	require.Equal(t, sess.CurrentSessionID(), got.body.SessionID)
	require.Contains(t, got.body.Text, "look")
	require.Contains(t, got.body.Text, "attached image")
	require.Len(t, got.body.Images, 1)
	require.FileExists(t, got.body.Images[0].Path)
	require.True(t, strings.HasPrefix(got.body.Images[0].Path, work))
	decoded, err := base64.StdEncoding.DecodeString(got.body.Images[0].DataBase64)
	require.NoError(t, err)
	require.Equal(t, png, decoded)
	require.NotEmpty(t, got.body.Reply.HTTP)
	require.NotEmpty(t, got.body.Reply.Token)
	require.NotEqual(t, testKey, got.body.Reply.Token)
	require.Contains(t, got.body.Reply.File, ".json")

	logged := logs.String()
	require.NotContains(t, logged, testKey)
	require.NotContains(t, logged, got.body.Reply.Token)

	// A second POST for the same id is acknowledged and does not start another turn.
	req, err := http.NewRequest(http.MethodPost, got.body.Reply.HTTP, strings.NewReader(`{"text":"again"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+got.body.Reply.Token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var dup map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&dup))
	require.Equal(t, true, dup["duplicate"])
}

func TestSend_EmbedImagesOffKeepsPathOnly(t *testing.T) {
	png := tinyPNG(t)
	var body webhookBody
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"text":"ok"}`))
	}))
	t.Cleanup(hook.Close)

	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["callback"] = false
	opts["embed_images"] = false
	opts["timeout"] = "3s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "kept-session")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	require.Equal(t, "kept-session", sess.CurrentSessionID())

	require.NoError(t, sess.Send("pic", "m", []core.ImageAttachment{{
		MimeType: "image/png", Data: png, FileName: "shot.png",
	}}, nil))
	require.Empty(t, body.Images[0].DataBase64)
	require.NotEmpty(t, body.Images[0].Path)
	require.Empty(t, body.Reply.HTTP)
	require.NotEmpty(t, body.Reply.File)
}

func TestSend_FileOutboxAndPathImage(t *testing.T) {
	work := t.TempDir()
	outPNG := filepath.Join(work, "chart.png")
	require.NoError(t, os.WriteFile(outPNG, tinyPNG(t), 0o600))

	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		payload, err := json.Marshal(map[string]any{
			"text":   "chart ready",
			"images": []string{outPNG},
		})
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		go writeOutbox(t, body.Reply.File, payload)
	}))
	t.Cleanup(hook.Close)

	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["work_dir"] = work
	opts["timeout"] = "3s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s1")
	require.NoError(t, err)
	require.NoError(t, sess.Send("draw", "", nil, nil))

	events := readEvents(t, sess.Events(), 2)
	result := events[len(events)-1]
	require.Equal(t, core.EventResult, result.Type)
	require.Equal(t, "chart ready", result.Content)
	require.Len(t, result.Images, 1)
	require.Equal(t, tinyPNG(t), result.Images[0].Data)
	require.Equal(t, "chart.png", result.Images[0].FileName)
}

func TestSend_UnauthorizedRedactsKey(t *testing.T) {
	logs := captureLogs(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad `+testKey+`"}`)
	}))
	t.Cleanup(hook.Close)

	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "10s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)

	start := time.Now()
	err = sess.Send("hi", "", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
	require.Contains(t, err.Error(), defaultKeyEnv)
	require.NotContains(t, err.Error(), testKey)
	require.Less(t, time.Since(start), 3*time.Second)
	require.NotContains(t, logs.String(), testKey)
}

func TestSend_RoutineDisabled(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"routine disabled"}`)
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	err = sess.Send("hi", "", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "400")
	require.Contains(t, err.Error(), "routine disabled")
	require.NotContains(t, err.Error(), testKey)
}

func TestSend_SuccessFalse(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":false,"error":"not started"}`)
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	err = sess.Send("hi", "", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not started")
}

func TestSend_Timeout(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"runUuid":"run-slow"}`)
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "200ms"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("hi", "", nil, nil))
	events := readEvents(t, sess.Events(), 1)
	require.Equal(t, core.EventError, events[0].Type)
	require.Error(t, events[0].Error)
	require.Contains(t, events[0].Error.Error(), "timed out")
	require.True(t, sess.Alive())
}

func TestSend_ReplyError(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"error":"model overloaded"}`))
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "3s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("hi", "", nil, nil))
	events := readEvents(t, sess.Events(), 1)
	require.Equal(t, core.EventError, events[0].Type)
	require.Error(t, events[0].Error)
	require.Contains(t, events[0].Error.Error(), "overloaded")
	require.Error(t, sess.RespondPermission("x", core.PermissionResult{}))
}

func TestSend_CloseCancelsWait(t *testing.T) {
	received := make(chan struct{})
	var once sync.Once
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		once.Do(func() { close(received) })
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "30s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s2")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- sess.Send("wait", "", nil, nil) }()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook was not called")
	}
	require.NoError(t, sess.Close())
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("Send did not return after Close")
	}
}

func TestSend_RejectsOverlappingTurn(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "3s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s3")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	first := make(chan error, 1)
	go func() { first <- sess.Send("one", "", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first turn did not reach the webhook")
	}
	err = sess.Send("two", "", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already in progress")
	close(release)
	require.NoError(t, sess.Close())
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first Send did not return")
	}
}

func TestCallbackAuth(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	opts["callback_auth"] = true
	ag := mustAgent(t, opts)
	id := newID()
	ch := ag.register(id)
	t.Cleanup(func() { ag.unregister(id) })

	url := ag.callbackBase + "/outbox/" + id
	resp, err := http.Post(url, "application/json", strings.NewReader(`{"text":"nope"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"text":"ok"}`))
	require.NoError(t, err)
	req.Header.Set("X-Grokbot-Token", ag.replyToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	select {
	case msg := <-ch:
		require.NoError(t, msg.err)
		require.Equal(t, "ok", msg.text)
	case <-time.After(time.Second):
		t.Fatal("reply was not delivered")
	}

	// Bad ids and traversal are rejected.
	resp, err = http.Post(ag.callbackBase+"/outbox/../x", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
}

func TestCallbackAuthDisabled(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	opts["callback_auth"] = false
	ag := mustAgent(t, opts)
	id := newID()
	ch := ag.register(id)
	resp, err := http.Post(ag.callbackBase+"/outbox/"+id, "application/json", strings.NewReader(`{"content":"plain"}`))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	msg := <-ch
	require.Equal(t, "plain", msg.text)
}

func TestSend_BearerKeyIsNotDoubled(t *testing.T) {
	var auth string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"text":"ok"}`))
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	t.Setenv(defaultKeyEnv, "Bearer "+testKey)
	opts["webhook_url"] = hook.URL
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("hi", "", nil, nil))
	require.Equal(t, "Bearer "+testKey, auth)
	require.NotContains(t, auth, "Bearer Bearer")
}

func TestSend_WorkDirSwitchAndFiles(t *testing.T) {
	var body webhookBody
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"text":"got file"}`))
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	ag := mustAgent(t, opts)
	dir := t.TempDir()
	ag.SetWorkDir(dir)
	require.Equal(t, dir, ag.GetWorkDir())
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("file", "mid", nil, []core.FileAttachment{{
		MimeType: "text/plain",
		Data:     []byte("hello file"),
		FileName: "note.txt",
	}}))
	require.Len(t, body.Files, 1)
	require.Contains(t, body.Files[0].Path, dir)
	require.Contains(t, body.Text, "note.txt")
	data, err := os.ReadFile(body.Files[0].Path)
	require.NoError(t, err)
	require.Equal(t, "hello file", string(data))
}

func TestStop_ClosesCallback(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	ag := mustAgent(t, opts)
	url := ag.callbackBase + "/health"
	resp, err := http.Get(url)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, ag.Stop())
	require.NoError(t, ag.Stop())
	_, err = http.Get(url)
	require.Error(t, err)
}

func TestParseReplyBody_MissingImage(t *testing.T) {
	msg, err := parseReplyBody([]byte(`{"text":"","images":["/no/such/image.png"]}`), nil)
	require.NoError(t, err)
	require.Error(t, msg.err)
}

func postReply(t *testing.T, rt replyTarget, payload string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rt.HTTP, strings.NewReader(payload))
	if err != nil {
		t.Errorf("reply request: %v", err)
		return
	}
	if rt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Errorf("reply post: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("reply status %d: %s", resp.StatusCode, b)
	}
}

func writeOutbox(t *testing.T, path string, payload []byte) {
	t.Helper()
	if path == "" {
		t.Error("empty outbox path")
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		t.Errorf("write outbox: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Errorf("rename outbox: %v", err)
	}
}

func readEvents(t *testing.T, ch <-chan core.Event, n int) []core.Event {
	t.Helper()
	var out []core.Event
	deadline := time.After(4 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			require.True(t, ok)
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out with %d/%d events", len(out), n)
		}
	}
	return out
}

func TestReplyPath_RejectsOutsideAndSymlink(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	const secretBody = "SECRET-KEY-MATERIAL"
	require.NoError(t, os.WriteFile(secret, []byte(secretBody), 0o600))
	require.NoError(t, os.Symlink(secret, filepath.Join(work, "link")))

	for _, path := range []string{secret, filepath.Join(work, "link")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body webhookBody
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				payload, err := json.Marshal(map[string]any{
					"text":   "look",
					"images": []string{path},
				})
				require.NoError(t, err)
				w.WriteHeader(http.StatusNoContent)
				go writeOutbox(t, body.Reply.File, payload)
			}))
			t.Cleanup(hook.Close)
			opts := baseOpts(t)
			opts["webhook_url"] = hook.URL
			opts["work_dir"] = work
			opts["timeout"] = "3s"
			ag := mustAgent(t, opts)
			sess, err := ag.StartSession(context.Background(), "s")
			require.NoError(t, err)
			require.NoError(t, sess.Send("hi", "", nil, nil))
			ev := readEvents(t, sess.Events(), 1)[0]
			require.Equal(t, core.EventError, ev.Type)
			require.Error(t, ev.Error)
			require.Contains(t, ev.Error.Error(), "outside")
			require.NotContains(t, ev.Error.Error(), secretBody)
		})
	}
}

func TestReplyPath_AllowlistAndOutboxAndSniff(t *testing.T) {
	work := t.TempDir()
	allowed := t.TempDir()
	pngPath := filepath.Join(allowed, "shot.unknown")
	require.NoError(t, os.WriteFile(pngPath, tinyPNG(t), 0o600))

	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		payload, err := json.Marshal(map[string]any{"text": "sniffed", "images": []string{pngPath}})
		require.NoError(t, err)
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, payload)
	}))
	t.Cleanup(hook.Close)

	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["work_dir"] = work
	opts["reply_path_allow"] = []string{allowed}
	opts["timeout"] = "3s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("hi", "", nil, nil))
	ev := readEvents(t, sess.Events(), 2)
	result := ev[len(ev)-1]
	require.Equal(t, core.EventResult, result.Type)
	require.Len(t, result.Images, 1)
	require.Equal(t, "image/png", result.Images[0].MimeType)
	require.Equal(t, tinyPNG(t), result.Images[0].Data)

	// A text file with an unknown extension must not be labeled image/png.
	note := filepath.Join(work, "note.dat")
	require.NoError(t, os.WriteFile(note, []byte("hello from a text file\n"), 0o600))
	roots := ag.allowedRoots()
	f, err := fileFromPath(note, roots)
	require.NoError(t, err)
	require.NotEqual(t, "image/png", f.MimeType)
	require.Contains(t, f.MimeType, "text/plain")
}

func TestOutboxInvalidJSONFailsTurn(t *testing.T) {
	logs := captureLogs(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"text":`))
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["timeout"] = "30s"
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, sess.Send("hi", "", nil, nil))
	elapsed := time.Since(start)
	ev := readEvents(t, sess.Events(), 1)[0]
	require.Equal(t, core.EventError, ev.Type)
	require.Error(t, ev.Error)
	require.Contains(t, ev.Error.Error(), "not valid JSON")
	require.Less(t, elapsed, 8*time.Second)
	require.Greater(t, elapsed, 400*time.Millisecond)
	require.Contains(t, logs.String(), "not valid JSON")
}

func TestHandleOutbox_LateAndInvalidReply(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	opts["callback_auth"] = false
	ag := mustAgent(t, opts)

	late, err := http.Post(ag.callbackBase+"/outbox/"+newID(), "application/json", strings.NewReader(`{"text":"late"}`))
	require.NoError(t, err)
	defer func() { _ = late.Body.Close() }()
	require.Equal(t, http.StatusConflict, late.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(late.Body).Decode(&body))
	require.Contains(t, fmt.Sprint(body["error"]), "finished")

	bad, err := http.Post(ag.callbackBase+"/outbox/"+newID(), "application/json", strings.NewReader(`{"text":`))
	require.NoError(t, err)
	_ = bad.Body.Close()
	require.Equal(t, http.StatusBadRequest, bad.StatusCode)
}

func TestWaitRegistered_TakesReplyAlreadyQueued(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = false
	ag := mustAgent(t, opts)
	id := newID()
	ch := ag.register(id)
	require.True(t, ag.deliver(id, replyMsg{text: "in time"}))
	msg, err := ag.waitRegistered(context.Background(), id, ch, time.Nanosecond)
	require.NoError(t, err)
	require.Equal(t, "in time", msg.text)
}

func TestCallbackAuthFalse_RefusesPublicBind(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_auth"] = false
	opts["callback_addr"] = "0.0.0.0:0"
	_, err := New(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "callback_auth=false")
	require.Contains(t, err.Error(), "callback_insecure_ok")
}

func TestCallbackAuthFalse_OptInAndSend(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_auth"] = false
	opts["callback_insecure_ok"] = true
	opts["callback_addr"] = "0.0.0.0:0"
	opts["callback_advertise_host"] = "127.0.0.1"
	ag := mustAgent(t, opts)
	require.True(t, strings.HasPrefix(ag.callbackBase, "http://127.0.0.1:"), ag.callbackBase)
	resp, err := http.Get(ag.callbackBase + "/health")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var token string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		token = body.Reply.Token
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"runUuid":"run-open"}`)
		go postReply(t, body.Reply, `{"text":"open callback"}`)
	}))
	t.Cleanup(hook.Close)
	// The public listener above proved opt-in bind. The send uses loopback,
	// which is allowed without the opt-in flag.
	opts = baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["callback"] = true
	opts["callback_auth"] = false
	opts["callback_addr"] = "127.0.0.1:0"
	opts["timeout"] = "5s"
	ag = mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("hi", "", nil, nil))
	require.Empty(t, token)
	ev := readEvents(t, sess.Events(), 2)
	require.Equal(t, "open callback", ev[len(ev)-1].Content)
}

func TestWebhookHTTPRemoteRefusedUnlessOptIn(t *testing.T) {
	opts := baseOpts(t)
	opts["webhook_url"] = "http://example.test/hook"
	_, err := New(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not https")
	require.NotContains(t, err.Error(), testKey)

	opts["webhook_insecure_ok"] = true
	ag := mustAgent(t, opts)
	require.Equal(t, "http://example.test/hook", ag.webhookURL)

	loop := baseOpts(t)
	loop["webhook_url"] = "http://127.0.0.1:9/hook"
	ag = mustAgent(t, loop)
	require.Equal(t, "http://127.0.0.1:9/hook", ag.webhookURL)
}

func TestLoopbackCallbackAdvertisesItself(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "127.0.0.1:0"
	ag := mustAgent(t, opts)
	require.True(t, strings.HasPrefix(ag.callbackBase, "http://127.0.0.1:"), ag.callbackBase)
	require.NotContains(t, ag.callbackBase, "0.0.0.0")
}

func TestWildcardCallbackRequiresAdvertiseHost(t *testing.T) {
	opts := baseOpts(t)
	opts["callback"] = true
	opts["callback_addr"] = "0.0.0.0:0"
	_, err := New(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "callback_advertise_host")
	require.NotContains(t, err.Error(), "127.0.0.1")

	opts["callback_advertise_host"] = "10.1.2.3"
	ag := mustAgent(t, opts)
	require.True(t, strings.HasPrefix(ag.callbackBase, "http://10.1.2.3:"), ag.callbackBase)
	require.NotContains(t, ag.callbackBase, "127.0.0.1")
}

func TestConsumeOutbox_RejectsUnsafeFiles(t *testing.T) {
	opts := baseOpts(t)
	ag := mustAgent(t, opts)
	dir := ag.outboxDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))

	id := newID()
	big, err := os.Create(filepath.Join(dir, id+".json"))
	require.NoError(t, err)
	require.NoError(t, big.Truncate(maxReplyBytes+1))
	require.NoError(t, big.Close())
	_, kind, err := ag.consumeOutbox(id)
	require.Equal(t, outboxFatal, kind)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")

	real := filepath.Join(dir, "real.json")
	require.NoError(t, os.WriteFile(real, []byte(`{"text":"hidden"}`), 0o600))
	linkID := newID()
	require.NoError(t, os.Symlink(real, filepath.Join(dir, linkID+".json")))
	msg, kind, err := ag.consumeOutbox(linkID)
	require.Equal(t, outboxFatal, kind)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlink")
	require.Empty(t, msg.text)
	require.NotContains(t, err.Error(), "hidden")

	require.Error(t, replyFileProblem("d", modeInfo{mode: os.ModeDevice | 0o644, size: 1}))
	require.Error(t, replyFileProblem("p", modeInfo{mode: os.ModeNamedPipe | 0o644, size: 0}))

	if runtime.GOOS == "windows" {
		return
	}
	fifoID := newID()
	fifo := filepath.Join(dir, fifoID+".json")
	require.NoError(t, mkfifo(fifo))
	done := make(chan struct{})
	go func() {
		_, _, _ = ag.consumeOutbox(fifoID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reading a FIFO outbox reply blocked")
	}
}

type modeInfo struct {
	mode os.FileMode
	size int64
}

func (m modeInfo) Name() string       { return "reply.json" }
func (m modeInfo) Size() int64        { return m.size }
func (m modeInfo) Mode() os.FileMode  { return m.mode }
func (m modeInfo) ModTime() time.Time { return time.Time{} }
func (m modeInfo) IsDir() bool        { return m.mode.IsDir() }
func (m modeInfo) Sys() any           { return nil }

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	require.NoError(t, err)
	return raw
}

func TestSend_OversizeEmbedSendsPathOnly(t *testing.T) {
	var body webhookBody
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusNoContent)
		go writeOutbox(t, body.Reply.File, []byte(`{"text":"ok"}`))
	}))
	t.Cleanup(hook.Close)
	opts := baseOpts(t)
	opts["webhook_url"] = hook.URL
	opts["embed_images"] = true
	opts["max_embed_bytes"] = 4
	ag := mustAgent(t, opts)
	sess, err := ag.StartSession(context.Background(), "s")
	require.NoError(t, err)
	require.NoError(t, sess.Send("", "m", []core.ImageAttachment{{
		MimeType: "image/png", Data: tinyPNG(t), FileName: "big.png",
	}}, nil))
	require.Empty(t, body.Images[0].DataBase64)
	require.NotEmpty(t, body.Images[0].Path)
	require.Contains(t, body.Text, "Please look at the attached image")
}
