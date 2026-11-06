package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgentToolBindingRejectsUnknownAmbiguousAndDetachedThreads(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	api := &APIServer{engines: map[string]*Engine{"shared": env.e}}
	if p, key, err := api.bindAgentToolRequest("", "", "first"); err != nil || p != "shared" || key != "test:user" {
		t.Fatalf("%s %s %v", p, key, err)
	}
	if _, _, err := api.bindAgentToolRequest("", "", "unknown"); err == nil {
		t.Fatal("unbound thread guessed the only conversation")
	}
	if _, _, err := api.bindAgentToolRequest("wrong-project", "", "first"); err == nil {
		t.Fatal("explicit project ignored")
	}
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:other", Platform: "test", UserID: "other", Content: "/attach first", ReplyCtx: "other"})
	if _, _, err := api.bindAgentToolRequest("", "", "first"); err == nil {
		t.Fatal("ambiguous thread guessed a destination")
	}
	if p, key, err := api.bindAgentToolRequest("explicit", "test:explicit", "first"); err != nil || p != "explicit" || key != "test:explicit" {
		t.Fatal("explicit routing was changed")
	}
	env.send("/detach")
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:other", Platform: "test", UserID: "other", Content: "/detach", ReplyCtx: "other"})
	if _, _, err := api.bindAgentToolRequest("", "", "first"); err == nil {
		t.Fatal("detached observer retained a routing binding")
	}
}

func TestAgentToolBindingRoutesSendCronAndTimerWithoutPromptContext(t *testing.T) {
	env := newSharedTestEnv(t)
	env.send("/attach first")
	env.await("Attached to session first")
	cronStore, err := NewCronStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	timerStore, err := NewTimerStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := &APIServer{engines: map[string]*Engine{"shared": env.e}, cron: NewCronScheduler(cronStore), timer: NewTimerScheduler(timerStore)}
	api.cron.RegisterEngine("shared", env.e)
	api.timer.RegisterEngine("shared", env.e)
	t.Cleanup(api.timer.Stop)
	for _, test := range []struct {
		path, body string
		handler    http.HandlerFunc
	}{
		{"/send", `{"agent_session_id":"first","message":"BOUND TOOL SEND"}`, api.handleSend},
		{"/cron/add", `{"agent_session_id":"first","cron_expr":"0 0 * * *","prompt":"scheduled task"}`, api.handleCronAdd},
		{"/timer/add", `{"agent_session_id":"first","delay":"24h","prompt":"delayed task"}`, api.handleTimerAdd},
	} {
		t.Run(test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			test.handler(w, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if test.path == "/send" {
				env.await("BOUND TOOL SEND")
				return
			}
			var job struct {
				Project    string `json:"project"`
				SessionKey string `json:"session_key"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
			if job.Project != "shared" || job.SessionKey != "test:user" {
				t.Fatalf("wrong task destination: %+v", job)
			}
		})
	}
	for _, handler := range []http.HandlerFunc{api.handleCronList, api.handleTimerList} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/list?agent_session_id=first", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"project":"shared"`) {
			t.Fatalf("list failed: %d %s", w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/list?agent_session_id=unknown", nil))
		if w.Code != http.StatusBadRequest {
			t.Fatal("unbound list guessed a project")
		}
	}
}

func TestAgentToolBindingResolvesRelaySourceAndKeepsTargetExplicit(t *testing.T) {
	source := newSharedTestEnv(t)
	source.send("/attach first")
	source.await("Attached to session first")
	target, agent := newSharedCompatEnv(t)
	target.e.name = "target"
	rm := NewRelayManager("")
	rm.SetVisibility("none")
	rm.Bind("test", "user", map[string]string{"shared": "source", "target": "target"})
	rm.RegisterEngine("shared", source.e)
	rm.RegisterEngine("target", target.e)
	api := &APIServer{engines: map[string]*Engine{"shared": source.e, "target": target.e}, relay: rm}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		api.handleRelaySend(w, httptest.NewRequest(http.MethodPost, "/relay/send", strings.NewReader(`{"agent_session_id":"first","to":"target","message":"BOUND RELAY TASK"}`)))
	}()
	call := awaitSharedCompatCall(t, agent)
	if strings.Contains(call.prompt, "invocation context") || strings.Contains(call.prompt, "session_key") {
		t.Fatal("relay injected routing context")
	}
	call.finish("BOUND RELAY RESPONSE")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not complete")
	}
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "BOUND RELAY RESPONSE") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
