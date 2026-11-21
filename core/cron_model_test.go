package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestUpdateJobField_Model guards the `cron edit <id> model <value>` path:
// updateJobField must accept a string value for "model" and reject non-strings,
// mirroring the other string fields.
func TestUpdateJobField_Model(t *testing.T) {
	job := &CronJob{ID: "j1"}

	if err := updateJobField(job, "model", "haiku"); err != nil {
		t.Fatalf("updateJobField(model, haiku): %v", err)
	}
	if job.Model != "haiku" {
		t.Errorf("job.Model = %q, want haiku", job.Model)
	}

	if err := updateJobField(job, "model", ""); err != nil {
		t.Fatalf("updateJobField(model, empty): %v", err)
	}
	if job.Model != "" {
		t.Errorf("job.Model = %q, want empty (override cleared)", job.Model)
	}

	if err := updateJobField(job, "model", 42); err == nil {
		t.Error("updateJobField(model, 42) succeeded, want type error")
	}
}

// TestCronJobModel_JSONRoundTrip ensures the model field persists through the
// store's JSON encoding and is omitted when empty (no store-format churn for
// existing jobs).
func TestCronJobModel_JSONRoundTrip(t *testing.T) {
	job := &CronJob{ID: "j1", Model: "sonnet"}
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CronJob
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Model != "sonnet" {
		t.Errorf("round-trip Model = %q, want sonnet", back.Model)
	}

	empty, err := json.Marshal(&CronJob{ID: "j2"})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != "" && jsonHasKey(empty, "model") {
		t.Errorf("empty Model serialized into JSON: %s", empty)
	}
}

func jsonHasKey(b []byte, key string) bool {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// modelCUJAgent is a cujAgent that implements ModelOverrideStarter. Each
// spawned session replies "reply from <model>" ("default" = configured
// model), so platform-visible output shows which model served each turn.
type modelCUJAgent struct {
	cujAgent
	modelMu sync.Mutex
	models  []string // one entry per spawn; "" = configured model
}

func (a *modelCUJAgent) StartSession(ctx context.Context, sid string) (AgentSession, error) {
	return a.start(ctx, sid, "")
}

func (a *modelCUJAgent) StartSessionWithModel(ctx context.Context, sid, model string) (AgentSession, error) {
	return a.start(ctx, sid, model)
}

func (a *modelCUJAgent) start(ctx context.Context, sid, model string) (AgentSession, error) {
	s, err := a.cujAgent.StartSession(ctx, sid)
	if err != nil {
		return nil, err
	}
	label := model
	if label == "" {
		label = "default"
	}
	cs := s.(*cujAgentSession)
	cs.mu.Lock()
	cs.reply = "reply from " + label
	cs.mu.Unlock()
	a.modelMu.Lock()
	a.models = append(a.models, model)
	a.modelMu.Unlock()
	return s, nil
}

func (a *modelCUJAgent) spawnedModels() []string {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	return append([]string(nil), a.models...)
}

func (a *cujAgent) sessionsSnapshot() []*cujAgentSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*cujAgentSession(nil), a.sessions...)
}

const modelJobKey = "test:erin"

func newModelJobEngine(t *testing.T, agent Agent, schedDefault string) (*Engine, *cujReplyCtxPlatform) {
	t.Helper()
	p := &cujReplyCtxPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(e.cancel)
	if schedDefault != "none" {
		cstore, err := NewCronStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cs := NewCronScheduler(cstore)
		cs.SetDefaultSessionMode(schedDefault)
		cs.RegisterEngine("test", e)
		e.SetCronScheduler(cs)
		tstore, err := NewTimerStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		ts := NewTimerScheduler(tstore)
		ts.SetDefaultSessionMode(schedDefault)
		ts.RegisterEngine("test", e)
		e.SetTimerScheduler(ts)
	}
	return e, p
}

// userTurn drives an ordinary chat turn through ReceiveMessage and waits
// until the platform shows a reply containing want.
func userTurn(t *testing.T, e *Engine, p *cujReplyCtxPlatform, content, want string) {
	t.Helper()
	before := len(p.getSent())
	e.ReceiveMessage(p, &Message{
		SessionKey: modelJobKey, Platform: "test", MessageID: "m-" + content,
		UserID: "erin", UserName: "erin", Content: content, ReplyCtx: "ctx-erin",
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range p.getSent()[before:] {
			if strings.Contains(s, want) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("user turn %q: no reply containing %q; sent=%q", content, want, p.getSent()[before:])
}

func runModelJob(e *Engine, kind, model, sessionMode string) error {
	if kind == "timer" {
		return e.ExecuteTimerJob(&TimerJob{ID: "t1", Project: "test", SessionKey: modelJobKey,
			Prompt: "sweep", Description: "sweep", Model: model, SessionMode: sessionMode})
	}
	return e.ExecuteCronJob(&CronJob{ID: "c1", Project: "test", SessionKey: modelJobKey,
		Prompt: "sweep", Description: "sweep", Model: model, SessionMode: sessionMode})
}

func historyContains(s *Session, needle string) bool {
	for _, h := range s.GetHistory(0) {
		if strings.Contains(h.Content, needle) {
			return true
		}
	}
	return false
}

// TestModelJob_RunsInIsolatedSessionWithRequestedModel is the regression for
// the shared-session defect: a cron/timer job with a model override used to
// either run on the user's live session (override silently dropped) or spawn
// the shared interactive key with the job's model (user's next turns then ran
// on it). A nonempty model must force a separate per-run session regardless
// of saved/default session mode, and ordinary turns must stay on the
// configured model.
func TestModelJob_RunsInIsolatedSessionWithRequestedModel(t *testing.T) {
	for _, kind := range []string{"cron", "timer"} {
		for _, live := range []bool{false, true} {
			for _, sched := range []string{"none", "reuse"} {
				for _, mode := range []string{"", "reuse", "new_per_run"} {
					if kind == "timer" && mode == "reuse" {
						continue // timer add rejects literal reuse; covered by omitted mode
					}
					name := fmt.Sprintf("%s/live=%v/sched=%s/mode=%q", kind, live, sched, mode)
					t.Run(name, func(t *testing.T) {
						agent := &modelCUJAgent{}
						e, p := newModelJobEngine(t, agent, sched)
						if live {
							userTurn(t, e, p, "hello", "reply from default")
						}
						if err := runModelJob(e, kind, "haiku", mode); err != nil {
							t.Fatalf("job error: %v", err)
						}
						if !containsSent(p.getSent(), "reply from haiku") {
							t.Fatalf("job reply not from haiku; sent=%q", p.getSent())
						}
						userTurn(t, e, p, "after", "reply from default")
						if last := p.getSent()[len(p.getSent())-1]; strings.Contains(last, "haiku") {
							t.Fatalf("ordinary turn served by job model: %q", last)
						}

						models := agent.spawnedModels()
						if got := countModel(models, "haiku"); got != 1 {
							t.Fatalf("haiku spawns = %d, want 1 (models=%q)", got, models)
						}
						sessions := agent.sessionsSnapshot()
						var jobSess *cujAgentSession
						for i, m := range models {
							if m == "haiku" {
								jobSess = sessions[i]
							}
						}
						for i, m := range models {
							if m != "haiku" && sessions[i] == jobSess {
								t.Fatal("job and chat share an agent session")
							}
						}
						if got := jobSess.getSentPrompts(); len(got) != 1 || !strings.Contains(got[0], "sweep") {
							t.Fatalf("job session prompts = %q, want only the job prompt", got)
						}
						if historyContains(e.sessions.GetOrCreateActive(modelJobKey), "sweep") {
							t.Fatal("job prompt leaked into the interactive session history")
						}
					})
				}
			}
		}
	}
}

func containsSent(sent []string, needle string) bool {
	for _, s := range sent {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func countModel(models []string, want string) int {
	n := 0
	for _, m := range models {
		if m == want {
			n++
		}
	}
	return n
}

// TestModelJob_NoModelReuseUnchanged guards the default path: without a model
// a reuse-mode job still runs on the user's live session (no extra spawn).
func TestModelJob_NoModelReuseUnchanged(t *testing.T) {
	for _, kind := range []string{"cron", "timer"} {
		t.Run(kind, func(t *testing.T) {
			agent := &modelCUJAgent{}
			e, p := newModelJobEngine(t, agent, "reuse")
			userTurn(t, e, p, "hello", "reply from default")
			if err := runModelJob(e, kind, "", ""); err != nil {
				t.Fatalf("job error: %v", err)
			}
			sessions := agent.sessionsSnapshot()
			if len(sessions) != 1 {
				t.Fatalf("spawns = %d, want 1 (job must reuse the live session)", len(sessions))
			}
			if got := sessions[0].getSentPrompts(); len(got) != 2 || !strings.Contains(got[1], "sweep") {
				t.Fatalf("live session prompts = %q, want hello then sweep", got)
			}
		})
	}
}

// TestModelJob_UnsupportedAgentRejected: an agent without ModelOverrideStarter
// must not silently run the job on its configured model.
func TestModelJob_UnsupportedAgentRejected(t *testing.T) {
	for _, kind := range []string{"cron", "timer"} {
		for _, mute := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mute=%v", kind, mute), func(t *testing.T) {
				agent := &cujAgent{}
				e, p := newModelJobEngine(t, agent, "reuse")
				userTurn(t, e, p, "hello", "ok")
				before := len(p.getSent())
				var err error
				if kind == "timer" {
					err = e.ExecuteTimerJob(&TimerJob{ID: "t1", SessionKey: modelJobKey, Prompt: "sweep", Model: "haiku", Mute: mute})
				} else {
					err = e.ExecuteCronJob(&CronJob{ID: "c1", SessionKey: modelJobKey, Prompt: "sweep", Model: "haiku", Mute: mute})
				}
				if err == nil || !strings.Contains(err.Error(), "haiku") {
					t.Fatalf("err = %v, want model-override-unsupported error", err)
				}
				sessions := agent.sessionsSnapshot()
				if len(sessions) != 1 {
					t.Fatalf("spawns = %d, want 1 (no fallback spawn)", len(sessions))
				}
				if got := sessions[0].getSentPrompts(); len(got) != 1 {
					t.Fatalf("live session prompts = %q, job must not run on it", got)
				}
				sent := p.getSent()[before:]
				if mute && len(sent) != 0 {
					t.Fatalf("muted job sent %q", sent)
				}
				if !mute && !containsSent(sent, "haiku") {
					t.Fatalf("no visible unsupported-model notice; sent=%q", sent)
				}
			})
		}
	}
}

// TestModelJob_RuntimeRejectsInvalidPersistedJobs covers jobs that bypass
// AddJob validation (persisted/direct): whitespace-only model and shell+model
// must fail without running anything.
func TestModelJob_RuntimeRejectsInvalidPersistedJobs(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	cases := []struct {
		name  string
		model string
		exec  string
	}{
		{"whitespace model", "   ", ""},
		{"shell with model", "haiku", "touch " + marker},
	}
	for _, kind := range []string{"cron", "timer"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				agent := &modelCUJAgent{}
				e, p := newModelJobEngine(t, agent, "reuse")
				prompt := "sweep"
				if tc.exec != "" {
					prompt = ""
				}
				var err error
				if kind == "timer" {
					err = e.ExecuteTimerJob(&TimerJob{ID: "t1", SessionKey: modelJobKey, Prompt: prompt, Exec: tc.exec, Model: tc.model})
				} else {
					err = e.ExecuteCronJob(&CronJob{ID: "c1", SessionKey: modelJobKey, Prompt: prompt, Exec: tc.exec, Model: tc.model})
				}
				if err == nil {
					t.Fatal("want error")
				}
				if n := len(agent.sessionsSnapshot()); n != 0 {
					t.Fatalf("spawns = %d, want 0", n)
				}
				if _, statErr := os.Stat(marker); statErr == nil {
					t.Fatal("shell command ran despite model override")
				}
				if containsSent(p.getSent(), "⏰ sweep") {
					t.Fatalf("start notice sent for rejected job: %q", p.getSent())
				}
			})
		}
	}
}

// TestModelJob_NotificationShowsModel: the start notice names the requested
// model and isolated session, and silent/mute still suppress it.
func TestModelJob_NotificationShowsModel(t *testing.T) {
	agent := &modelCUJAgent{}
	e, p := newModelJobEngine(t, agent, "reuse")
	if err := runModelJob(e, "cron", "haiku", ""); err != nil {
		t.Fatal(err)
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.HasPrefix(sent[0], "⏰ sweep") || !strings.Contains(sent[0], "haiku") {
		t.Fatalf("start notice = %q, want description + model", sent)
	}
	if !strings.Contains(sent[0], "isolated") {
		t.Fatalf("start notice = %q, want isolated-session note", sent[0])
	}

	e2, p2 := newModelJobEngine(t, &modelCUJAgent{}, "reuse")
	e2.cronScheduler.SetDefaultSilent(true)
	if err := runModelJob(e2, "cron", "haiku", ""); err != nil {
		t.Fatal(err)
	}
	if containsSent(p2.getSent(), "⏰") {
		t.Fatalf("silent job sent start notice: %q", p2.getSent())
	}
}

// TestModelJob_PersistedJobsIsolatedViaScheduler loads model jobs from disk
// (bypassing AddJob) and runs them through the real scheduler paths.
func TestModelJob_PersistedJobsIsolatedViaScheduler(t *testing.T) {
	t.Run("cron", func(t *testing.T) {
		agent := &modelCUJAgent{}
		e, p := newModelJobEngine(t, agent, "none")
		userTurn(t, e, p, "hello", "reply from default")
		dir := t.TempDir()
		writeJobsFile(t, filepath.Join(dir, "crons", "jobs.json"),
			`[{"id":"p1","project":"test","session_key":"test:erin","cron_expr":"0 9 * * *","prompt":"sweep","model":" haiku ","session_mode":"reuse","enabled":true}]`)
		store, err := NewCronStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		cs := NewCronScheduler(store)
		cs.RegisterEngine("test", e)
		e.SetCronScheduler(cs)
		cs.runJob(store.Get("p1"), true)
		if le := store.Get("p1").LastError; le != "" {
			t.Fatalf("LastError = %q", le)
		}
		if got := agent.spawnedModels(); len(got) != 2 || got[1] != "haiku" {
			t.Fatalf("spawned models = %q, want [\"\" haiku]", got)
		}
		if store.Get("p1").SessionMode != "reuse" {
			t.Fatal("saved session_mode must be retained")
		}
		userTurn(t, e, p, "after", "reply from default")
	})
	t.Run("timer", func(t *testing.T) {
		agent := &modelCUJAgent{}
		e, p := newModelJobEngine(t, agent, "none")
		userTurn(t, e, p, "hello", "reply from default")
		dir := t.TempDir()
		writeJobsFile(t, filepath.Join(dir, "timers", "jobs.json"),
			`[{"id":"p1","project":"test","session_key":"test:erin","scheduled_at":"2030-01-01T00:00:00Z","prompt":"sweep","model":"haiku"},
			  {"id":"p2","project":"test","session_key":"test:erin","scheduled_at":"2030-01-01T00:00:00Z","prompt":"sweep","model":"  "}]`)
		store, err := NewTimerStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		ts := NewTimerScheduler(store)
		ts.RegisterEngine("test", e)
		e.SetTimerScheduler(ts)
		ts.executeJob("p1")
		if le := store.Get("p1").LastError; le != "" {
			t.Fatalf("p1 LastError = %q", le)
		}
		ts.executeJob("p2")
		if le := store.Get("p2").LastError; le == "" {
			t.Fatal("p2 (whitespace model) must record an error")
		}
		if got := agent.spawnedModels(); len(got) != 2 || got[1] != "haiku" {
			t.Fatalf("spawned models = %q, want [\"\" haiku]", got)
		}
		userTurn(t, e, p, "after", "reply from default")
	})
}

func writeJobsFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestModelJob_IngressValidation: trim legitimate IDs, empty = default/clear,
// whitespace-only and shell+model rejected; custom IDs stay valid.
func TestModelJob_IngressValidation(t *testing.T) {
	cases := []struct {
		name, model, exec string
		want              string
		wantErr           bool
	}{
		{name: "empty means default", model: "", want: ""},
		{name: "trims", model: "  haiku ", want: "haiku"},
		{name: "custom provider id", model: "us.anthropic.claude-x[1m]", want: "us.anthropic.claude-x[1m]"},
		{name: "whitespace rejected", model: " \t ", wantErr: true},
		{name: "shell with model rejected", model: "haiku", exec: "echo hi", wantErr: true},
	}
	for _, tc := range cases {
		t.Run("cron/"+tc.name, func(t *testing.T) {
			store, _ := NewCronStore(t.TempDir())
			cs := NewCronScheduler(store)
			prompt := "p"
			if tc.exec != "" {
				prompt = ""
			}
			job := &CronJob{ID: "c", SessionKey: "test:k", CronExpr: "0 9 * * *", Prompt: prompt, Exec: tc.exec, Model: tc.model}
			err := cs.AddJob(job)
			if (err != nil) != tc.wantErr {
				t.Fatalf("AddJob err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && job.Model != tc.want {
				t.Fatalf("Model = %q, want %q", job.Model, tc.want)
			}
		})
		t.Run("timer/"+tc.name, func(t *testing.T) {
			store, _ := NewTimerStore(t.TempDir())
			ts := NewTimerScheduler(store)
			prompt := "p"
			if tc.exec != "" {
				prompt = ""
			}
			job := &TimerJob{ID: "t", SessionKey: "test:k", ScheduledAt: time.Now().Add(time.Hour), Prompt: prompt, Exec: tc.exec, Model: tc.model}
			err := ts.AddJob(job)
			defer ts.Stop()
			if (err != nil) != tc.wantErr {
				t.Fatalf("AddJob err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && job.Model != tc.want {
				t.Fatalf("Model = %q, want %q", job.Model, tc.want)
			}
		})
	}
}

// TestModelJob_EditValidation covers cron edit / PATCH of the model field.
func TestModelJob_EditValidation(t *testing.T) {
	store, _ := NewCronStore(t.TempDir())
	cs := NewCronScheduler(store)
	if err := cs.AddJob(&CronJob{ID: "c", SessionKey: "test:k", CronExpr: "0 9 * * *", Prompt: "p", SessionMode: "reuse"}); err != nil {
		t.Fatal(err)
	}
	if err := cs.AddJob(&CronJob{ID: "s", SessionKey: "test:k", CronExpr: "0 9 * * *", Exec: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		id, field string
		value     any
		wantErr   bool
		want      string
	}{
		{"c", "model", " sonnet ", false, "sonnet"},
		{"c", "model", "   ", true, "sonnet"},
		{"c", "Model", "  ", true, "sonnet"},
		{"c", "model", 42, true, "sonnet"},
		{"c", "Model", "haiku", false, "haiku"},
		{"c", "model", "", false, ""},
		{"s", "model", "haiku", true, ""},
	}
	for i, st := range steps {
		err := cs.UpdateJob(st.id, st.field, st.value)
		if (err != nil) != st.wantErr {
			t.Fatalf("step %d UpdateJob(%s,%v) err = %v, wantErr %v", i, st.field, st.value, err, st.wantErr)
		}
		if got := store.Get(st.id).Model; got != st.want {
			t.Fatalf("step %d Model = %q, want %q", i, got, st.want)
		}
	}
	if store.Get("c").SessionMode != "reuse" {
		t.Fatal("model edits must not rewrite saved session_mode")
	}
}

// TestModelJob_APIMapping: socket API (cron + timer) and management API cron
// add must carry the model field into the stored job.
func TestModelJob_APIMapping(t *testing.T) {
	cstore, _ := NewCronStore(t.TempDir())
	cs := NewCronScheduler(cstore)
	tstore, _ := NewTimerStore(t.TempDir())
	tsch := NewTimerScheduler(tstore)
	defer tsch.Stop()
	api := &APIServer{engines: map[string]*Engine{}, cron: cs, timer: tsch}

	post := func(h http.HandlerFunc, body map[string]any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
		return rec
	}
	rec := post(api.handleCronAdd, map[string]any{"project": "test", "session_key": "test:k", "cron_expr": "0 9 * * *", "prompt": "p", "model": " haiku "})
	var cj CronJob
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &cj) != nil || cj.Model != "haiku" {
		t.Fatalf("socket cron add: %d %s", rec.Code, rec.Body.String())
	}
	rec = post(api.handleTimerAdd, map[string]any{"project": "test", "session_key": "test:k", "delay": "1h", "prompt": "p", "model": "haiku"})
	var tj TimerJob
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &tj) != nil || tj.Model != "haiku" {
		t.Fatalf("socket timer add: %d %s", rec.Code, rec.Body.String())
	}
	if rec = post(api.handleTimerAdd, map[string]any{"project": "test", "session_key": "test:k", "delay": "1h", "prompt": "p", "model": "  "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("whitespace timer model: %d, want 400", rec.Code)
	}

	mgmt, ts, e := testManagementServer(t, "tok")
	mstore, _ := NewCronStore(t.TempDir())
	mcs := NewCronScheduler(mstore)
	mcs.RegisterEngine("test-project", e)
	mgmt.SetCronScheduler(mcs)
	r := mgmtPost(t, ts.URL+"/api/v1/cron", "tok", map[string]any{
		"project": "test-project", "session_key": "test:chan:user", "cron_expr": "0 9 * * *", "prompt": "p", "model": "haiku",
	})
	var mj CronJob
	if !r.OK || json.Unmarshal(r.Data, &mj) != nil || mj.Model != "haiku" || mstore.Get(mj.ID).Model != "haiku" {
		t.Fatalf("management cron add lost model: %+v %s", mj, r.Error)
	}
	r = mgmtPatch(t, ts.URL+"/api/v1/cron/"+mj.ID, "tok", map[string]any{"model": ""})
	if !r.OK || mstore.Get(mj.ID).Model != "" {
		t.Fatalf("management PATCH clear: %s model=%q", r.Error, mstore.Get(mj.ID).Model)
	}
}
