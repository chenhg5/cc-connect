package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// maomao fork: the images of channel posts a bot only listened to must reach its next turn as
// images (2026-10-17: 鲸鱼娘 was @-ed after two photos and said she could not see them; user:
// "他每次都要看到之前的消息的呀，就是可能这长中短，近期的一个消息").

// attachmentServer serves /<name> with the bytes registered for it; anything else is a 404.
func attachmentServer(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// replyWhenMentioned is a gate that only listens unless the bot is @-ed.
func replyWhenMentioned(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		act := "context"
		if req.Mentioned {
			act = "reply"
		}
		_ = json.NewEncoder(w).Encode(gateResponse{Action: act})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// imgCaptureSession records every Send (prompt and images); the first Send ends the turn.
type imgCaptureSession struct {
	*resultAgentSession
	mu      sync.Mutex
	images  [][]ImageAttachment
	prompts []string
}

func newImgCaptureSession() *imgCaptureSession {
	return &imgCaptureSession{resultAgentSession: newResultAgentSession("ok")}
}

func (s *imgCaptureSession) Send(prompt, id string, images []ImageAttachment, files []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = append(s.images, images)
	s.prompts = append(s.prompts, prompt)
	return s.resultAgentSession.Send(prompt, id, images, files)
}

func (s *imgCaptureSession) sent() ([][]ImageAttachment, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]ImageAttachment(nil), s.images...), append([]string(nil), s.prompts...)
}

// workDirAgent starts imgCaptureSession and saves attachments under dir.
type workDirAgent struct {
	resultAgent
	dir string
}

func (a *workDirAgent) GetWorkDir() string { return a.dir }

func imageData(imgs []ImageAttachment) []string {
	var out []string
	for _, im := range imgs {
		out = append(out, string(im.Data))
	}
	return out
}

func newImgEngine(t *testing.T, p Platform) (*Engine, *imgCaptureSession, string) {
	t.Helper()
	as := newImgCaptureSession()
	dir := t.TempDir()
	e := NewEngine("test", &workDirAgent{resultAgent: resultAgent{session: as}, dir: dir}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return e, as, dir
}

// (a) A photo nobody @-ed this bot about, then a message that @-s her: the turn carries the photo.
// Also: a PDF goes to work_dir/.cc-connect/attachments with its path in the text, and a failed
// download is written into the text instead of stopping the turn.
func TestBackfillImages_CalledTurnGetsListenedImage(t *testing.T) {
	srv := attachmentServer(t, map[string][]byte{"cat.png": []byte("CAT"), "a.pdf": []byte("%PDF")})
	now := time.Now()
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Time: now, Attachments: []ChannelHistoryAttachment{
			{URL: srv.URL + "/cat.png", FileName: "cat.png", ContentType: "image/png"},
			{URL: srv.URL + "/gone.png", FileName: "gone.png", ContentType: "image/png"},
		}},
		{ID: "102", Author: "zuiho", Content: "资料", Time: now, Attachments: []ChannelHistoryAttachment{
			{URL: srv.URL + "/a.pdf", FileName: "a.pdf", ContentType: "application/pdf"},
		}},
	}}
	e, _, dir := newImgEngine(t, p)
	e.SetGate(replyWhenMentioned(t).URL, 2000, "reply")
	s := e.sessions.GetOrCreateActive("discord:c1")
	s.SetLastSeenMsgID("100")

	own := ImageAttachment{Data: []byte("OWN")}
	msg := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "103", UserName: "zuiho",
		Content: "鲸鱼你看看这只猫", Mentioned: true, Images: []ImageAttachment{own}}
	e.gate.released[msg] = true
	if e.applyGate(p, msg, s, "k") {
		t.Fatal("mentioned message should start a turn")
	}
	if got := imageData(msg.Images); strings.Join(got, ",") != "CAT,OWN" {
		t.Fatalf("turn images = %v, want the listened photo first, then the message's own", got)
	}
	for _, want := range []string{"附图 1 张（已随本条附上）", "gone.png 下载失败", "a.pdf 正在后台下载到 "} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("content lacks %q:\n%s", want, msg.Content)
		}
	}
	saved := dir + "/.cc-connect/attachments/102/a.pdf"
	if !strings.Contains(msg.Content, dir+"/.cc-connect/attachments/102/") {
		t.Fatalf("pdf directory not named:\n%s", msg.Content)
	}
	if b := waitFile(t, saved); string(b) != "%PDF" {
		t.Fatalf("pdf at %s = %q", saved, b)
	}
}

// cronHistoryPlatform can also start a scheduled turn in a channel.
type cronHistoryPlatform struct{ historyPlatform }

func (p *cronHistoryPlatform) ReconstructReplyCtx(string) (any, error) { return "rctx", nil }

// (b) A scheduled turn sees the photo posted since its last turn as an image.
func TestBackfillImages_CronTurnGetsImage(t *testing.T) {
	srv := attachmentServer(t, map[string][]byte{"cat.png": []byte("CAT")})
	now := time.Now()
	p := &cronHistoryPlatform{historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "龙娘", IsBot: true, Content: "截图", Time: now, Attachments: []ChannelHistoryAttachment{
			{URL: srv.URL + "/cat.png", FileName: "cat.png", Width: 10, Height: 10}, // no content type
		}},
	}}}
	e, as, _ := newImgEngine(t, p)
	e.sessions.GetOrCreateActive("discord:c1").SetLastSeenMsgID("100")

	if err := e.ExecuteCronJob(&CronJob{ID: "j", SessionKey: "discord:c1", Prompt: "回家"}); err != nil {
		t.Fatalf("ExecuteCronJob: %v", err)
	}
	imgs, prompts := as.sent()
	if len(imgs) != 1 || strings.Join(imageData(imgs[0]), ",") != "CAT" {
		t.Fatalf("cron turn images = %v", imgs)
	}
	if !strings.Contains(prompts[0], "龙娘（bot）: 截图（附图 1 张（已随本条附上））") {
		t.Fatalf("cron prompt:\n%s", prompts[0])
	}
}

// (c) @-ed while a turn runs: the listened photo goes into the running turn with the message.
func TestBackfillImages_InjectedMidTurn(t *testing.T) {
	srv := attachmentServer(t, map[string][]byte{"cat.png": []byte("CAT")})
	now := time.Now()
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "看过了", Time: now},
		{ID: "101", Author: "zuiho", Time: now, Attachments: []ChannelHistoryAttachment{
			{URL: srv.URL + "/cat.png", FileName: "cat.png", ContentType: "image/png"},
		}},
	}}
	e, _, _ := newImgEngine(t, p)
	e.SetGate(replyWhenMentioned(t).URL, 2000, "reply")
	e.gate.inject = true
	running := newImgCaptureSession()
	e.interactiveMu.Lock()
	e.interactiveStates["k"] = &interactiveState{agentSession: running}
	e.interactiveMu.Unlock()
	s := e.sessions.GetOrCreateActive("discord:c1")
	s.SetLastSeenMsgID("100")
	s.TryLock() // busy

	msg := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "102", UserName: "zuiho",
		Content: "看图", Mentioned: true, Images: []ImageAttachment{{Data: []byte("OWN")}}}
	if !e.applyGate(p, msg, s, "k") {
		t.Fatal("busy mentioned message should be injected")
	}
	imgs, prompts := running.sent()
	if len(imgs) != 1 || strings.Join(imageData(imgs[0]), ",") != "CAT,OWN" {
		t.Fatalf("injected images = %v", imgs)
	}
	if !strings.Contains(prompts[0], "附图 1 张（已随本条附上）") {
		t.Fatalf("injected text:\n%s", prompts[0])
	}
}

// (d) First turn of a fresh context (here: the old one is gone): the recent photos the old
// context had seen come back, newest first, under the image_fresh limit, without repeating
// what this turn's backfill already brings, and only once.
func TestBackfillImages_FreshContextGetsRecentImages(t *testing.T) {
	big := func(c byte) []byte { return bytes.Repeat([]byte{c}, 1_200_000) } // 1.6 MB as base64
	srv := attachmentServer(t, map[string][]byte{"old": []byte("OLD"), "b": big('B'), "c": big('C'), "d": []byte("D"), "e": []byte("E")})
	now := time.Now()
	img := func(name string) []ChannelHistoryAttachment {
		return []ChannelHistoryAttachment{{URL: srv.URL + "/" + name, FileName: name + ".png", ContentType: "image/png"}}
	}
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Time: now.Add(-4 * time.Hour), Attachments: img("old")}, // outside backfillFirstWindow
		{ID: "101", Author: "zuiho", Time: now.Add(-time.Hour), Attachments: img("b")},
		{ID: "102", Author: "zuiho", Time: now.Add(-50 * time.Minute), Attachments: img("c")},
		{ID: "103", Author: "鲸鱼娘", IsBot: true, IsSelf: true, FromTurn: true, Content: "看到了", Time: now.Add(-40 * time.Minute), Attachments: img("d")},
		{ID: "104", Author: "zuiho", Content: "嗯", Time: now.Add(-30 * time.Minute)},
		{ID: "105", Author: "zuiho", Time: now.Add(-time.Minute), Attachments: img("e")}, // not seen yet
	}}
	e, as, _ := newImgEngine(t, p)
	e.SetGate(replyWhenMentioned(t).URL, 2000, "reply")
	e.SetImageFresh(true, int(DefaultImageFreshMB*1e6))
	s := e.sessions.GetOrCreateActive("discord:c1")
	s.SetLastSeenMsgID("104") // the old context saw up to 104; no agent session to resume

	msg := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "106", UserName: "zuiho",
		Content: "刚才那几张图你还记得吗", Mentioned: true, Images: []ImageAttachment{{Data: []byte("OWN")}}}
	e.gate.released[msg] = true
	if e.applyGate(p, msg, s, "k") {
		t.Fatal("mentioned message should start a turn")
	}
	if !s.TryLock() {
		t.Fatal("lock")
	}
	e.processInteractiveMessageWith(p, msg, s, e.agent, e.sessions, "k", "", "")

	imgs, prompts := as.sent()
	if len(imgs) != 1 {
		t.Fatalf("sends = %d", len(imgs))
	}
	got := imageData(imgs[0])
	short := make([]string, len(got))
	total := 0
	for i, g := range got {
		short[i] = g[:1]
		total += base64Len(len(g))
	}
	// C and D fit (newest first); B would pass 3 MB; OLD is outside the window; E came with backfill.
	if strings.Join(short, ",") != "C,D,E,O" {
		t.Fatalf("fresh turn images = %v, want C,D (re-attached), E (backfill), OWN", short)
	}
	if total >= e.imageContextBudget() {
		t.Fatalf("images total %d base64 bytes, must stay under %d", total, e.imageContextBudget())
	}
	if !strings.Contains(prompts[0], "旧会话里看过的频道图片补回 2 张") || !strings.Contains(prompts[0], "更早的 1 张图片未附上（超出图片预算）") {
		t.Fatalf("fresh turn text:\n%s", prompts[0])
	}
	if s.TakeFreshContext() {
		t.Fatal("fresh-context images must be attached once")
	}
}

// waitFile waits for a file saved in the background.
func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Review 1: the first turn of a session (no last-seen mark) reads 3 hours of posts; their
// images stay under the image_fresh limit, newest kept, the rest named as left out.
func TestBackfillImages_FirstWindowCapped(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 1_200_000) // 1.6 MB as base64
	srv := attachmentServer(t, map[string][]byte{"a": append([]byte("A"), big...), "b": append([]byte("B"), big...), "c": append([]byte("C"), big...)})
	now := time.Now()
	img := func(name string, size int) []ChannelHistoryAttachment {
		return []ChannelHistoryAttachment{{URL: srv.URL + "/" + name, FileName: name, ContentType: "image/png", Size: size}}
	}
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "101", Author: "zuiho", Time: now, Attachments: img("a", 1_200_001)},
		{ID: "102", Author: "zuiho", Time: now, Attachments: img("b", 0)}, // size unknown: checked after download
		{ID: "103", Author: "zuiho", Time: now, Attachments: img("c", 1_200_001)},
	}}
	e, _, _ := newImgEngine(t, p)
	s := e.sessions.GetOrCreateActive("discord:c1") // no last-seen mark
	msg := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "104", Content: "看看"}
	back, images, ok := e.channelBackfill(p, msg, s)
	if !ok || len(images) != 1 || images[0].Data[0] != 'C' {
		t.Fatalf("ok=%v images=%d, want only the newest", ok, len(images))
	}
	if strings.Count(back, "1 张图片未附上（超出图片预算）") != 2 || !strings.Contains(back, "附图 1 张（已随本条附上）") {
		t.Fatalf("backfill text:\n%s", back)
	}
}

// Review 2: a second renewal does not re-attach what the first one already brought back, so
// the fresh context is not refilled to the limit every time.
func TestBackfillImages_FreshContextDoesNotRecarry(t *testing.T) {
	srv := attachmentServer(t, map[string][]byte{"b": []byte("B"), "c": []byte("C")})
	now := time.Now()
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "101", Author: "zuiho", Time: now, Attachments: []ChannelHistoryAttachment{{URL: srv.URL + "/b", FileName: "b", ContentType: "image/png"}}},
		{ID: "103", Author: "zuiho", Time: now, Attachments: []ChannelHistoryAttachment{{URL: srv.URL + "/c", FileName: "c", ContentType: "image/png"}}},
	}}
	e, _, _ := newImgEngine(t, p)
	s := e.sessions.GetOrCreateActive("discord:c1")
	first := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "102", BackfillRan: true, BackfillFrom: "101"}
	s.MarkFreshContext()
	if _, imgs := e.freshContextImages(p, s, first, nil); strings.Join(imageData(imgs), ",") != "B" {
		t.Fatalf("first renewal = %v", imageData(imgs))
	}
	s.SettleHandoff(true)
	second := &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: "104", BackfillRan: true, BackfillFrom: "103"}
	s.MarkFreshContext()
	if _, imgs := e.freshContextImages(p, s, second, nil); strings.Join(imageData(imgs), ",") != "C" {
		t.Fatalf("second renewal = %v, want only the image after the first renewal", imageData(imgs))
	}
}

// Review 3: images download together, and a big file never holds up the turn.
func TestBackfillImages_DownloadsDoNotSerialize(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/video.mp4" {
			<-release
		} else {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	defer close(release)
	now := time.Now()
	var atts []ChannelHistoryAttachment
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		atts = append(atts, ChannelHistoryAttachment{URL: srv.URL + "/" + n, FileName: n, ContentType: "image/png"})
	}
	atts = append(atts, ChannelHistoryAttachment{URL: srv.URL + "/video.mp4", FileName: "video.mp4", ContentType: "video/mp4"})
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Time: now},
		{ID: "101", Author: "zuiho", Time: now, Attachments: atts},
	}}
	e, _, _ := newImgEngine(t, p)
	s := e.sessions.GetOrCreateActive("discord:c1")
	s.SetLastSeenMsgID("100")
	start := time.Now()
	back, images, _ := e.channelBackfill(p, &Message{ChannelID: "c1", MessageID: "102"}, s)
	if took := time.Since(start); took > time.Second || len(images) != 5 {
		t.Fatalf("took %v for %d images (5 x 300 ms one by one would be 1.5 s; the video must not be waited for)", took, len(images))
	}
	if !strings.Contains(back, "video.mp4 正在后台下载到") {
		t.Fatalf("text:\n%s", back)
	}
}

// Review 4: two messages handled at the same time while busy deliver the earlier photo once.
func TestBackfillImages_ConcurrentMessagesDeliverOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("CAT"))
	}))
	defer srv.Close()
	now := time.Now()
	p := &historyPlatform{stubPlatformEngine: stubPlatformEngine{n: "discord"}, history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Time: now},
		{ID: "101", Author: "zuiho", Time: now, Attachments: []ChannelHistoryAttachment{{URL: srv.URL + "/cat", FileName: "cat", ContentType: "image/png"}}},
		{ID: "102", Author: "zuiho", Content: "看", Time: now},
		{ID: "103", Author: "zuiho", Content: "看看", Time: now},
	}}
	e, _, _ := newImgEngine(t, p)
	e.SetGate(replyWhenMentioned(t).URL, 2000, "reply")
	e.gate.inject = true
	running := newImgCaptureSession()
	e.interactiveMu.Lock()
	e.interactiveStates["k"] = &interactiveState{agentSession: running}
	e.interactiveMu.Unlock()
	s := e.sessions.GetOrCreateActive("discord:c1")
	s.SetLastSeenMsgID("100")
	s.TryLock() // busy

	var wg sync.WaitGroup
	for _, id := range []string{"102", "103"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			e.applyGate(p, &Message{SessionKey: "discord:c1", ChannelID: "c1", MessageID: id, UserName: "zuiho", Content: "看", Mentioned: true}, s, "k")
		}(id)
	}
	wg.Wait()
	imgs, _ := running.sent()
	n := 0
	for _, batch := range imgs {
		n += len(batch)
	}
	if len(imgs) != 2 || n != 1 {
		t.Fatalf("sends=%d images=%d, want the photo delivered once", len(imgs), n)
	}
}
