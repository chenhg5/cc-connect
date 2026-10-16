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
	videoByMsg = map[string]*watchvidRun{}
	videoMsgOrder = nil
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
	msgs := make([]*Message, len(bots))
	var wg sync.WaitGroup
	for i, e := range bots {
		msgs[i] = &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "1000", ChannelID: "c"}
		wg.Add(1)
		go func(e *Engine, m *Message) { defer wg.Done(); e.prepareMedia(m) }(e, msgs[i])
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
	if r, b := videoStateSize(); r != 0 || b != 1 {
		t.Fatalf("running=%d byMsg=%d, want 0 and 1", r, b)
	}
}

// Per-message results are kept FIFO, at most videoKeep finished ones; a running one is never evicted.
func TestVideoByMsgFIFO(t *testing.T) {
	release := make(chan struct{})
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		if strings.Contains(link, "slow") {
			<-release
		}
		return nasIndex + "\n", "", nil
	}, nil)
	e := newMediaEngine("a")
	slowDone := make(chan struct{})
	go func() {
		e.prepareMedia(&Message{Content: "https://b23.tv/slow", MessageID: "1", ChannelID: "c"})
		close(slowDone)
	}()
	time.Sleep(30 * time.Millisecond)
	for i := 0; i < videoKeep+10; i++ {
		e.prepareMedia(&Message{Content: "https://b23.tv/v" + strconv.Itoa(i), MessageID: strconv.Itoa(100 + i), ChannelID: "c"})
	}
	videoMu.Lock()
	_, slowKept := videoByMsg["|c|1|b23:slow"]
	n := len(videoByMsg)
	_, oldestKept := videoByMsg["|c|100|b23:v0"]
	videoMu.Unlock()
	if !slowKept {
		t.Fatal("running result evicted")
	}
	if n != videoKeep || oldestKept {
		t.Fatalf("kept %d (want %d), oldest finished kept=%v", n, videoKeep, oldestKept)
	}
	close(release)
	<-slowDone
	e.prepareMedia(&Message{Content: "https://b23.tv/next", MessageID: "999", ChannelID: "c"})
	videoMu.Lock()
	_, slowKept = videoByMsg["|c|1|b23:slow"]
	n = len(videoByMsg)
	videoMu.Unlock()
	if slowKept || n != videoKeep {
		t.Fatalf("after it finished: slow kept=%v, size=%d", slowKept, n)
	}
}

// The share key includes platform and channel: the same message ID elsewhere is a new message.
func TestVideoShareKeyHasChannel(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, func(context.Context, string) (string, string, error) {
		runs.Add(1)
		return "", "dead link\n", errors.New("exit status 1")
	}, nil)
	e := newMediaEngine("a")
	e.prepareMedia(&Message{Content: "https://b23.tv/x", MessageID: "7", ChannelID: "c1", Platform: "discord"})
	e.prepareMedia(&Message{Content: "https://b23.tv/x", MessageID: "7", ChannelID: "c2", Platform: "discord"})
	e.prepareMedia(&Message{Content: "https://b23.tv/x", MessageID: "7", ChannelID: "c1", Platform: "telegram"})
	if runs.Load() != 3 {
		t.Fatalf("runs = %d, want 3", runs.Load())
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
	for _, e := range bots { // one after another: the first has failed before the others start
		m := &Message{Content: "https://b23.tv/x", MessageID: "2000", ChannelID: "c"}
		e.prepareMedia(m)
		if !strings.Contains(m.Content, "短链没有跳转") {
			t.Fatalf("%s: failure not reported: %q", e.name, m.Content)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("one message ran watchvid %d times, want 1", n)
	}
	bots[0].prepareMedia(&Message{Content: "https://b23.tv/x", MessageID: "2001", ChannelID: "c"})
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
	e.prepareMedia(m1)
	if strings.Contains(m1.Content, nasMissingNote) || !strings.Contains(m1.Content, "已存进媒体库") {
		t.Fatalf("mounted: %q", m1.Content)
	}
	mounted.Store(false)
	m2 := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "3001", ChannelID: "c",
		Images: []ImageAttachment{{MimeType: "image/png"}}}
	e.prepareMedia(m2)
	if !strings.Contains(m2.Content, tmp) || strings.Count(m2.Content, nasMissingNote) != 2 || strings.Contains(m2.Content, "已存进媒体库") {
		t.Fatalf("unmounted (video + attachment notes): %q", m2.Content)
	}
	mounted.Store(true)
	m3 := &Message{Content: "https://b23.tv/I4TDkg4", MessageID: "3002", ChannelID: "c"}
	e.prepareMedia(m3)
	if strings.Contains(m3.Content, nasMissingNote) {
		t.Fatalf("remounted, still says not stored: %q", m3.Content)
	}
	if runs.Load() != 3 {
		t.Fatalf("runs = %d, want one per message", runs.Load())
	}
}

// watchvid hangs: the message goes on with a failure note after the timeout; later links in the
// same message are not started; a plain message in the session is not delayed meanwhile.
func TestVideoTimeout(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		runs.Add(1)
		<-ctx.Done()
		return "", "", errors.New("killed: " + ctx.Err().Error())
	}, nil)
	watchvidTimeout = 100 * time.Millisecond
	e := newMediaEngine("maomao")
	start := time.Now()
	m := &Message{Content: "https://b23.tv/first https://b23.tv/second", MessageID: "4000", ChannelID: "c"}
	done := make(chan struct{})
	go func() { e.prepareMedia(m); close(done) }()
	time.Sleep(20 * time.Millisecond)
	plain := &Message{Content: "你们看看", MessageID: "4001", ChannelID: "c"}
	t0 := time.Now()
	e.prepareMedia(plain)
	if d := time.Since(t0); d > 20*time.Millisecond {
		t.Fatalf("plain message waited %v", d)
	}
	<-done
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	if !strings.Contains(m.Content, "失败") || !strings.Contains(m.Content, "没有解析：https://b23.tv/second") {
		t.Fatalf("notes: %q", m.Content)
	}
	if plain.Content != "你们看看" {
		t.Fatalf("plain message changed: %q", plain.Content)
	}
	waitVideoRuns(t)
	if runs.Load() != 1 {
		t.Fatalf("watchvid started %d times, want 1 (second link must not start after the timeout)", runs.Load())
	}
}

// Nothing new is started once the caller's context is done.
func TestParseVideoNoStartAfterCancel(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, okRun(&runs), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newMediaEngine("a").parseVideo(ctx, &Message{MessageID: "1"}, videoLink{"https://b23.tv/x", "b23:x"})
	if r.Err == "" || runs.Load() != 0 {
		t.Fatalf("res=%+v runs=%d", r, runs.Load())
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

func TestPrepareMediaSkips(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, okRun(&runs), nil)
	e := newMediaEngine("maomao")
	msg := &Message{Content: "看看 https://b23.tv/I4TDkg4", MessageID: "5000"}
	e.prepareMedia(msg)
	before := msg.Content
	e.prepareMedia(msg) // a held gate burst re-enters handleMessage
	if msg.Content != before {
		t.Fatal("note appended twice")
	}
	bot := &Message{Content: "【龙娘】https://b23.tv/I4TDkg4", FromBot: true}
	e.prepareMedia(bot)
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
	e.prepareMedia(a)
	b := &Message{SessionKey: "discord:c", Content: "你们看看"}
	e.prepareMedia(b)
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

// Attachment archiving never blocks the caller and holds at most attachQueueLen waiting jobs:
// one worker, the rest dropped with a log line.
func TestArchiveAttachmentsQueueBounded(t *testing.T) {
	oldEnabled, oldRun := mediaEnabled, runMediaArchive
	t.Cleanup(func() {
		attachInFlight.Wait() // no job may outlive the test's settings
		mediaEnabled, runMediaArchive = oldEnabled, oldRun
	})
	mediaEnabled = true
	started, release := make(chan struct{}, 1), make(chan struct{})
	var runs, cur, peak atomic.Int32
	runMediaArchive = func(ctx context.Context, paths []string) (string, string, error) {
		runs.Add(1)
		n := cur.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		defer cur.Add(-1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "", "", nil
	}
	if !ArchiveAttachments([]string{"/x/first"}) {
		t.Fatal("first job not queued")
	}
	<-started // the worker holds the first job
	t0 := time.Now()
	for i := 0; i < attachQueueLen; i++ {
		if !ArchiveAttachments([]string{"/x/" + strconv.Itoa(i)}) {
			t.Fatalf("job %d dropped with room in the queue", i)
		}
	}
	if ArchiveAttachments([]string{"/x/overflow"}) {
		t.Fatal("job queued beyond attachQueueLen")
	}
	if d := time.Since(t0); d > 50*time.Millisecond {
		t.Fatalf("enqueue blocked for %v", d)
	}
	close(release)
	attachInFlight.Wait()
	if runs.Load() != int32(attachQueueLen)+1 || peak.Load() != 1 {
		t.Fatalf("runs=%d peak=%d", runs.Load(), peak.Load())
	}
}

// Each job is bounded by attachTimeout.
func TestArchiveAttachmentsTimeout(t *testing.T) {
	oldEnabled, oldRun, oldTimeout := mediaEnabled, runMediaArchive, attachTimeout
	t.Cleanup(func() {
		attachInFlight.Wait()
		mediaEnabled, runMediaArchive, attachTimeout = oldEnabled, oldRun, oldTimeout
	})
	mediaEnabled, attachTimeout = true, 100*time.Millisecond
	var timedOut atomic.Int32
	runMediaArchive = func(ctx context.Context, paths []string) (string, string, error) {
		<-ctx.Done()
		timedOut.Add(1)
		return "", "hung", ctx.Err()
	}
	start := time.Now()
	for i := 0; i < 3; i++ {
		ArchiveAttachments([]string{"/x/" + strconv.Itoa(i)})
	}
	attachInFlight.Wait()
	if d := time.Since(start); d > 2*time.Second || timedOut.Load() != 3 {
		t.Fatalf("took %v, timed out %d", d, timedOut.Load())
	}
}

// The attachment hold window is the same whether or not a NAS note was appended.
func TestHoldWindowIgnoresMediaNote(t *testing.T) {
	var mounted atomic.Bool
	withMedia(t, func(context.Context, string) (string, string, error) { return "", "", nil }, &mounted)
	for _, m := range []bool{true, false} {
		mounted.Store(m)
		msg := &Message{Images: []ImageAttachment{{MimeType: "image/png"}}}
		newMediaEngine("a").prepareMedia(msg)
		if got := holdWindow(msg); got != attachmentHold {
			t.Fatalf("mounted=%v: window %v, want %v (content %q)", m, got, attachmentHold, msg.Content)
		}
	}
	if holdWindow(&Message{Content: "hi", Images: []ImageAttachment{{}}}) != burstWindow {
		t.Fatal("captioned attachment should use the burst window")
	}
}

func TestMediaOffByDefault(t *testing.T) {
	msg := &Message{Content: "https://b23.tv/I4TDkg4"}
	newMediaEngine("x").prepareMedia(msg)
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
