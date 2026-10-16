package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// withMedia turns the media library on with fake watchvid / NAS for one test.
func withMedia(t *testing.T, run func(ctx context.Context, link string) (string, string, error), mounted *atomic.Bool) {
	t.Helper()
	oldEnabled, oldRun, oldMounted, oldRoot, oldTimeout := mediaEnabled, runWatchvid, mediaNASMounted, mediaNASRoot, watchvidTimeout
	mediaEnabled, runWatchvid, mediaNASRoot = true, run, "/nas"
	mediaNASMounted = func() bool { return mounted == nil || mounted.Load() }
	resetVideoState()
	t.Cleanup(func() {
		waitVideoRuns(t)
		mediaEnabled, runWatchvid, mediaNASMounted, mediaNASRoot, watchvidTimeout = oldEnabled, oldRun, oldMounted, oldRoot, oldTimeout
		resetVideoState()
	})
}

func resetVideoState() {
	videoMu.Lock()
	videoRunning = map[string]*watchvidRun{}
	videoByMsg = map[string]*msgVideo{}
	mediaEngines = map[*Engine]bool{}
	videoMu.Unlock()
}

// waitVideoRuns waits until no watchvid run is in flight.
func waitVideoRuns(t *testing.T) {
	for i := 0; i < 200; i++ {
		if r, _ := videoStateSize(); r == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("watchvid run still in flight")
}

func videoStateSize() (running, byMsg int) {
	videoMu.Lock()
	defer videoMu.Unlock()
	return len(videoRunning), len(videoByMsg)
}

// prepareMedia is beginMedia with the session released right away (the gate step is not under test).
func (e *Engine) prepareMedia(msg *Message, key string) { e.beginMedia(msg, key)() }

func newMediaEngine(name string) *Engine { return &Engine{ctx: context.Background(), name: name} }

const nasIndex = "/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md"

func okRun(runs *atomic.Int32) func(context.Context, string) (string, string, error) {
	return func(context.Context, string) (string, string, error) {
		runs.Add(1)
		return "log line\n" + nasIndex + "\n", "", nil
	}
}

func TestFindVideoLinks(t *testing.T) {
	text := "看这个 https://www.bilibili.com/video/BV1Yjpm6jE8j?spm_id_from=333.1007，还有【选择你的ai模型-哔哩哔哩】 https://b23.tv/I4TDkg4 " +
		"<https://youtu.be/jNQXAC9IVRw> 再贴一次 https://m.bilibili.com/video/BV1Yjpm6jE8j/ " +
		"分P https://www.bilibili.com/video/BV1Fpp26WEus?p=2 [yt](https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s) " +
		"https://www.youtube.com/shorts/abcdefghijk 不是视频 https://github.com/x/y https://www.bilibili.com/read/cv123"
	got := findVideoLinks(text)
	want := []videoLink{
		{"https://www.bilibili.com/video/BV1Yjpm6jE8j?spm_id_from=333.1007", "bili:BV1Yjpm6jE8j"},
		{"https://b23.tv/I4TDkg4", "b23:I4TDkg4"},
		{"https://youtu.be/jNQXAC9IVRw", "yt:jNQXAC9IVRw"},
		{"https://www.bilibili.com/video/BV1Fpp26WEus?p=2", "bili:BV1Fpp26WEus:p2"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s", "yt:dQw4w9WgXcQ"},
		{"https://www.youtube.com/shorts/abcdefghijk", "yt:abcdefghijk"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d links %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if l := findVideoLinks("没有链接 https://example.com/video/BV1Yjpm6jE8j"); len(l) != 0 {
		t.Errorf("non-video links matched: %+v", l)
	}
}

// Four bots get the same message at once: watchvid runs once, all four get the path, and the
// shared state is empty once every bot is through.
func TestVideoOncePerMessageAndCleanup(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		<-release
		return okRun(&runs)(ctx, link)
	}, nil)
	bots := []*Engine{newMediaEngine("maomao"), newMediaEngine("longniang"), newMediaEngine("jingyu"), newMediaEngine("caihongmao")}
	for _, e := range bots {
		registerMediaEngine(e)
	}
	msgs := make([]*Message, len(bots))
	var wg sync.WaitGroup
	for i, e := range bots {
		msgs[i] = &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "1000", ChannelID: "c"}
		wg.Add(1)
		go func(e *Engine, m *Message) { defer wg.Done(); e.prepareMedia(m, "discord:c") }(e, msgs[i])
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("watchvid ran %d times, want 1", n)
	}
	for i, m := range msgs {
		if !strings.Contains(m.Content, nasIndex) {
			t.Errorf("bot %d got no path: %q", i, m.Content)
		}
	}
	if r, b := videoStateSize(); r != 0 || b != 0 {
		t.Fatalf("state not released: running=%d byMsg=%d", r, b)
	}
	for _, e := range bots {
		if len(e.mediaLockMap) != 0 {
			t.Fatalf("%s: session locks not released: %d", e.name, len(e.mediaLockMap))
		}
	}
}

// A bot that never gets a message must not pin its result: being past it in the channel counts.
func TestVideoByMsgReleasedWhenBotsMoveOn(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, okRun(&runs), nil)
	a, b := newMediaEngine("a"), newMediaEngine("b")
	registerMediaEngine(a)
	registerMediaEngine(b)
	a.prepareMedia(&Message{Content: "https://b23.tv/I4TDkg4", MessageID: "1000", ChannelID: "c"}, "k")
	if _, n := videoStateSize(); n != 1 {
		t.Fatalf("result for b dropped too early (%d)", n)
	}
	b.prepareMedia(&Message{Content: "later", MessageID: "1001", ChannelID: "c"}, "k")
	if _, n := videoStateSize(); n != 0 {
		t.Fatalf("result kept after every bot moved on (%d)", n)
	}
}

// A fast failure is shared by every bot handling that message; the next message retries.
func TestVideoFailureSharedPerMessage(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, func(context.Context, string) (string, string, error) {
		runs.Add(1)
		return "", "[watchvid] 短链没有跳转\n", errors.New("exit status 1")
	}, nil)
	bots := []*Engine{newMediaEngine("a"), newMediaEngine("b"), newMediaEngine("c"), newMediaEngine("d")}
	for _, e := range bots {
		registerMediaEngine(e)
	}
	for _, e := range bots { // one after another: the first has failed before the others start
		m := &Message{Content: "https://b23.tv/x", MessageID: "2000", ChannelID: "c"}
		e.prepareMedia(m, "k")
		if !strings.Contains(m.Content, "短链没有跳转") {
			t.Fatalf("%s: failure not reported: %q", e.name, m.Content)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("one message ran watchvid %d times, want 1", n)
	}
	bots[0].prepareMedia(&Message{Content: "https://b23.tv/x", MessageID: "2001", ChannelID: "c"}, "k")
	if n := runs.Load(); n != 2 {
		t.Fatalf("next message did not retry (%d runs)", n)
	}
}

// Each message gets the NAS state of its own run: stored → no NAS note; unmounted → note.
func TestVideoNoteFollowsMountState(t *testing.T) {
	var mounted atomic.Bool
	mounted.Store(true)
	var runs atomic.Int32
	tmp := filepath.Join(os.TempDir(), "watchvid", "bili-BV1Yjpm6jE8j", "watchvid-index.md")
	withMedia(t, func(context.Context, string) (string, string, error) {
		runs.Add(1)
		if mounted.Load() {
			return nasIndex + "\n", "", nil
		}
		return tmp + "\n", "[watchvid] NAS 没挂，这条没存\n", nil
	}, &mounted)
	e := newMediaEngine("maomao")
	m1 := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "3000", ChannelID: "c"}
	e.prepareMedia(m1, "k")
	if strings.Contains(m1.Content, nasMissingNote) || !strings.Contains(m1.Content, "已存进媒体库") {
		t.Fatalf("mounted: %q", m1.Content)
	}
	mounted.Store(false)
	m2 := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "3001", ChannelID: "c",
		Images: []ImageAttachment{{MimeType: "image/png"}}}
	e.prepareMedia(m2, "k")
	if !strings.Contains(m2.Content, tmp) || strings.Count(m2.Content, nasMissingNote) != 2 || strings.Contains(m2.Content, "已存进媒体库") {
		t.Fatalf("unmounted (video + attachment notes): %q", m2.Content)
	}
	mounted.Store(true)
	m3 := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "3002", ChannelID: "c"}
	e.prepareMedia(m3, "k")
	if strings.Contains(m3.Content, nasMissingNote) {
		t.Fatalf("remounted, still says not stored: %q", m3.Content)
	}
	if runs.Load() != 3 {
		t.Fatalf("runs = %d, want one per message", runs.Load())
	}
}

// watchvid hangs: the message goes on with a failure note after the timeout, the next message in
// the session is not stuck behind it, and the shared state is released.
func TestVideoTimeout(t *testing.T) {
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		<-ctx.Done()
		return "", "", errors.New("killed: " + ctx.Err().Error())
	}, nil)
	watchvidTimeout = 100 * time.Millisecond
	e := newMediaEngine("maomao")
	start := time.Now()
	m := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "4000", ChannelID: "c"}
	done := make(chan struct{})
	go func() { e.prepareMedia(m, "k"); close(done) }()
	time.Sleep(20 * time.Millisecond)
	follow := &Message{Content: "你们看看", MessageID: "4001", ChannelID: "c"}
	e.prepareMedia(follow, "k")
	<-done
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if !strings.Contains(m.Content, "失败") {
		t.Fatalf("no failure note: %q", m.Content)
	}
	if follow.Content != "你们看看" {
		t.Fatalf("follow-up changed: %q", follow.Content)
	}
	time.Sleep(50 * time.Millisecond)
	if r, _ := videoStateSize(); r != 0 {
		t.Fatal("running entry kept after timeout")
	}
	if len(e.mediaLockMap) != 0 {
		t.Fatal("session lock kept")
	}
}

// The real runner kills the whole process group on timeout (watchvid → uv → ffmpeg …).
func TestRunProcessGroupKillsChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := withLimit(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := runProcessGroup(ctx, "/bin/sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("returned after %v", d)
	}
	b, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 {
		t.Fatal("child pid not written")
	}
	time.Sleep(100 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived", pid)
	}
}

// A message right after a video link waits for the parse instead of overtaking it.
func TestPrepareMediaKeepsOrder(t *testing.T) {
	release := make(chan struct{})
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		<-release
		return nasIndex + "\n", "", nil
	}, nil)
	e := newMediaEngine("maomao")
	var order []string
	var mu sync.Mutex
	done := make(chan struct{}, 2)
	add := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock(); done <- struct{}{} }
	// add stands for the gate step, which runs before release (see handleMessage)
	go func() { r := e.beginMedia(&Message{Content: "https://b23.tv/I4TDkg4"}, "s"); add("link"); r() }()
	time.Sleep(30 * time.Millisecond)
	go func() { r := e.beginMedia(&Message{Content: "你们看看"}, "s"); add("follow-up"); r() }()
	time.Sleep(30 * time.Millisecond)
	close(release)
	<-done
	<-done
	if len(order) != 2 || order[0] != "link" {
		t.Fatalf("order = %v", order)
	}
	if len(e.mediaLockMap) != 0 {
		t.Fatal("session lock kept")
	}
}

func TestPrepareMediaSkips(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, okRun(&runs), nil)
	e := newMediaEngine("maomao")
	msg := &Message{Content: "看看 https://b23.tv/I4TDkg4", MessageID: "5000"}
	e.prepareMedia(msg, "s")
	before := msg.Content
	e.prepareMedia(msg, "s") // a held gate burst re-enters handleMessage
	if msg.Content != before {
		t.Fatal("note appended twice")
	}
	bot := &Message{Content: "【龙娘】https://b23.tv/I4TDkg4", FromBot: true}
	e.prepareMedia(bot, "s")
	if bot.Content != "【龙娘】https://b23.tv/I4TDkg4" {
		t.Fatal("bot message changed")
	}
	if userTextOf(msg) != "看看 https://b23.tv/I4TDkg4" {
		t.Fatalf("user text = %q", userTextOf(msg))
	}
	if !strings.Contains(e.mediaNoteFor("5000"), nasIndex) {
		t.Fatal("note not remembered for backfill")
	}
}

func TestMediaNotesCapped(t *testing.T) {
	e := newMediaEngine("x")
	for i := 0; i < mediaNoteKeep+10; i++ {
		e.rememberMediaNote(strconv.Itoa(i), "n")
	}
	if len(e.mediaNotes) != mediaNoteKeep || len(e.mediaNoteIDs) != mediaNoteKeep || e.mediaNoteFor("0") != "" {
		t.Fatalf("notes = %d ids = %d", len(e.mediaNotes), len(e.mediaNoteIDs))
	}
}

// The gate sees only what the user wrote: in Content, after a burst merge, and in Buffered.
func TestGateSeesUserTextOnly(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, okRun(&runs), nil)
	var reqs []gateRequest
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gateRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		json.NewEncoder(w).Encode(gateResponse{Action: "context"})
	}))
	defer srv.Close()
	e := newTestEngine()
	e.SetGate(srv.URL, 2000, "context")
	s := &Session{}

	a := &Message{SessionKey: "discord:c", Content: "看这个 https://b23.tv/I4TDkg4"}
	e.prepareMedia(a, "k")
	b := &Message{SessionKey: "discord:c", Content: "你们看看"}
	e.prepareMedia(b, "k")
	// burst: a is held, b folds into it
	e.gate.held["k"] = &heldMsg{msg: a, timer: time.NewTimer(time.Hour)}
	if !e.applyGate(nil, b, s, "k") {
		t.Fatal("b should fold into the held burst")
	}
	delete(e.gate.held, "k")
	e.gate.released[a] = true
	if !e.applyGate(nil, a, s, "k") { // gate says context → buffered
		t.Fatal("context not consumed")
	}
	c := &Message{SessionKey: "discord:c", Content: "嗯"}
	e.gate.released[c] = true
	e.applyGate(nil, c, s, "k")

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("gate calls = %d", len(reqs))
	}
	if reqs[0].Content != "看这个 https://b23.tv/I4TDkg4\n你们看看" {
		t.Fatalf("gate content = %q", reqs[0].Content)
	}
	if len(reqs[1].Buffered) != 1 || reqs[1].Buffered[0] != "看这个 https://b23.tv/I4TDkg4\n你们看看" {
		t.Fatalf("gate buffered = %q", reqs[1].Buffered)
	}
	if got := e.gate.buf["k"]; len(got) != 2 || !strings.Contains(got[0], nasIndex) {
		t.Fatalf("agent-side buffer lost the note: %q", got)
	}
}

// Attachment archiving: at most cap(attachSlots) runs at once; every job ends within
// attachTimeout of being queued — run to the timeout, or given up while waiting for a slot
// (logged); failures are only logged.
func TestArchiveAttachmentsBounded(t *testing.T) {
	oldEnabled, oldRun, oldTimeout := mediaEnabled, runMediaArchive, attachTimeout
	t.Cleanup(func() {
		attachInFlight.Wait() // no archive goroutine may outlive the test's settings
		mediaEnabled, runMediaArchive, attachTimeout = oldEnabled, oldRun, oldTimeout
	})
	mediaEnabled = true
	attachTimeout = 300 * time.Millisecond
	var cur, peak, calls, timedOut atomic.Int32
	runMediaArchive = func(ctx context.Context, paths []string) (string, string, error) {
		calls.Add(1)
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer cur.Add(-1)
		<-ctx.Done() // hangs until the timeout
		timedOut.Add(1)
		return "", "hung", ctx.Err()
	}
	const jobs = 12
	start := time.Now()
	for i := 0; i < jobs; i++ {
		ArchiveAttachments([]string{"/x/" + strconv.Itoa(i)})
	}
	attachInFlight.Wait()
	if d := time.Since(start); d > attachTimeout+500*time.Millisecond {
		t.Fatalf("jobs took %v, limit %v", d, attachTimeout)
	}
	if p := peak.Load(); p > int32(cap(attachSlots)) || p == 0 {
		t.Fatalf("peak concurrency = %d, cap %d", p, cap(attachSlots))
	}
	if c := calls.Load(); c < int32(cap(attachSlots)) || c > jobs {
		t.Fatalf("runs = %d", c)
	}
	if calls.Load() != timedOut.Load() || cur.Load() != 0 {
		t.Fatal("a run did not end at the timeout")
	}
}

func TestMediaOffByDefault(t *testing.T) {
	msg := &Message{Content: "https://b23.tv/I4TDkg4"}
	newMediaEngine("x").prepareMedia(msg, "s")
	if msg.Content != "https://b23.tv/I4TDkg4" || mediaEnabled {
		t.Fatal("media library must be off unless EnableMediaLibrary is called")
	}
}

func TestIsMountPoint(t *testing.T) {
	if isMountPoint(t.TempDir()) {
		t.Error("a plain directory is not a mount point")
	}
	if !isMountPoint("/") {
		t.Error("/ is a mount point")
	}
	if isMountPoint("/definitely/not/here") {
		t.Error("missing path is not a mount point")
	}
}
