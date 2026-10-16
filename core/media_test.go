package core

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withMedia turns the media library on with fake watchvid / NAS for one test.
func withMedia(t *testing.T, run func(ctx context.Context, link string) (string, string, error), mounted bool) {
	t.Helper()
	oldEnabled, oldRun, oldMounted, oldRoot := mediaEnabled, runWatchvid, mediaNASMounted, mediaNASRoot
	mediaEnabled, runWatchvid, mediaNASRoot = true, run, "/nas"
	mediaNASMounted = func() bool { return mounted }
	videoMu.Lock()
	videoCalls = map[string]*watchvidCall{}
	videoMu.Unlock()
	t.Cleanup(func() {
		mediaEnabled, runWatchvid, mediaNASMounted, mediaNASRoot = oldEnabled, oldRun, oldMounted, oldRoot
		videoMu.Lock()
		videoCalls = map[string]*watchvidCall{}
		videoMu.Unlock()
	})
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

// Four bots get the same message at once: watchvid runs once, all four get the result; later
// messages with the same video reuse it.
func TestParseVideoOncePerLink(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		runs.Add(1)
		<-release
		return "log line\n/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md\n", "", nil
	}, true)

	link := videoLink{"https://b23.tv/I4TDkg4", "b23:I4TDkg4"}
	var wg sync.WaitGroup
	results := make([]videoResult, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = parseVideo(context.Background(), link) }(i)
	}
	time.Sleep(50 * time.Millisecond) // let all four reach parseVideo while the first run blocks
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Fatalf("watchvid ran %d times, want 1", n)
	}
	for i, r := range results {
		if r.Index != "/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md" || r.Err != "" {
			t.Errorf("bot %d got %+v", i, r)
		}
	}
	parseVideo(context.Background(), link)
	if n := runs.Load(); n != 1 {
		t.Fatalf("second message re-ran watchvid (%d runs)", n)
	}
	if _, ok := cachedVideoResult(link); !ok {
		t.Fatal("finished parse not cached")
	}
}

func TestParseVideoFailureIsRetried(t *testing.T) {
	var runs atomic.Int32
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		runs.Add(1)
		return "", "[watchvid] 短链没有跳转\n", errors.New("exit status 1")
	}, true)
	link := videoLink{"https://b23.tv/x", "b23:x"}
	r := parseVideo(context.Background(), link)
	if r.Err == "" || !strings.Contains(r.Err, "短链没有跳转") {
		t.Fatalf("error not reported: %+v", r)
	}
	parseVideo(context.Background(), link)
	if runs.Load() != 2 {
		t.Fatalf("failed parse was cached (%d runs)", runs.Load())
	}
	if _, ok := cachedVideoResult(link); ok {
		t.Fatal("failed parse offered to backfill")
	}
}

func TestPrepareMediaAppendsPath(t *testing.T) {
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		return "/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md\n", "", nil
	}, true)
	e := &Engine{ctx: context.Background(), name: "maomao"}
	msg := &Message{Content: "看看 https://b23.tv/I4TDkg4", SessionKey: "s"}
	e.prepareMedia(msg, "s")
	if !strings.Contains(msg.Content, "/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md") ||
		!strings.Contains(msg.Content, "mediaarchive --keep-video bili-BV1Yjpm6jE8j") {
		t.Fatalf("path not appended: %q", msg.Content)
	}
	if strings.Contains(msg.Content, nasMissingNote) {
		t.Fatalf("NAS note on a stored video: %q", msg.Content)
	}
	if got := withoutMediaNote(msg); got != "看看 https://b23.tv/I4TDkg4" {
		t.Fatalf("gate input = %q", got)
	}
	// a held gate burst re-enters handleMessage: no second note
	before := msg.Content
	e.prepareMedia(msg, "s")
	if msg.Content != before {
		t.Fatal("note appended twice")
	}
	// teammate messages are left alone
	bot := &Message{Content: "【龙娘】https://b23.tv/I4TDkg4", FromBot: true}
	e.prepareMedia(bot, "s")
	if bot.Content != "【龙娘】https://b23.tv/I4TDkg4" {
		t.Fatal("bot message changed")
	}
	if n := cachedVideoNotes("[01:02] zuiho: https://b23.tv/I4TDkg4"); !strings.Contains(n, "watchvid-index.md") {
		t.Fatalf("backfill note missing: %q", n)
	}
}

// NAS not mounted: the parse result still reaches the bot, with "NAS 没挂，这条没存"; attachments too.
func TestPrepareMediaNASNotMounted(t *testing.T) {
	tmp := os.TempDir() + "/watchvid/bili-BV1Yjpm6jE8j/watchvid-index.md"
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		return tmp + "\n", "[watchvid] NAS 没挂，这条没存\n", nil
	}, false)
	e := &Engine{ctx: context.Background(), name: "jingyu"}
	msg := &Message{Content: "https://b23.tv/I4TDkg4", Images: []ImageAttachment{{MimeType: "image/png"}}}
	e.prepareMedia(msg, "s")
	if !strings.Contains(msg.Content, tmp) {
		t.Fatalf("result path missing: %q", msg.Content)
	}
	if strings.Count(msg.Content, nasMissingNote) != 2 {
		t.Fatalf("want the NAS note for the video and the attachment: %q", msg.Content)
	}
	if strings.Contains(msg.Content, "已存进媒体库") {
		t.Fatalf("claims it was stored: %q", msg.Content)
	}
}

// A message right after a video link waits for the parse instead of overtaking it.
func TestPrepareMediaKeepsOrder(t *testing.T) {
	release := make(chan struct{})
	withMedia(t, func(ctx context.Context, link string) (string, string, error) {
		<-release
		return "/nas/wiki-media/bili-BV1Yjpm6jE8j/watchvid-index.md\n", "", nil
	}, true)
	e := &Engine{ctx: context.Background()}
	var order []string
	var mu sync.Mutex
	done := make(chan struct{}, 2)
	go func() {
		e.prepareMedia(&Message{Content: "https://b23.tv/I4TDkg4"}, "s")
		mu.Lock()
		order = append(order, "link")
		mu.Unlock()
		done <- struct{}{}
	}()
	time.Sleep(30 * time.Millisecond)
	go func() {
		e.prepareMedia(&Message{Content: "你们看看"}, "s")
		mu.Lock()
		order = append(order, "follow-up")
		mu.Unlock()
		done <- struct{}{}
	}()
	time.Sleep(30 * time.Millisecond)
	close(release)
	<-done
	<-done
	if len(order) != 2 || order[0] != "link" {
		t.Fatalf("order = %v", order)
	}
}

func TestMediaOffByDefault(t *testing.T) {
	msg := &Message{Content: "https://b23.tv/I4TDkg4"}
	(&Engine{ctx: context.Background()}).prepareMedia(msg, "s")
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
