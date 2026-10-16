package core

// maomao fork: media library.
//
// User (2026-10-16/17): 「我希望wiki可以链接到多模态信息……你们解析完之后看到了可以保留图片或者音乐或者整个链接……
// 或者说作为一个记忆」「不应该特异化，不是应该统一吗」「你直接搞磁盘不就行了」.
//
//   - A user message with a video link (the sites watchvid handles: Bilibili, YouTube) runs
//     ~/im-bridge/bin/watchvid once per link for the whole process — the four bots get the
//     same Discord message, the first one runs it, the others wait for that run. watchvid writes
//     straight into the NAS media library (/Volumes/personal_folder/wiki-media/<id>/) and
//     mediaarchive adds the wiki catalogue line. The message then carries the result path, so
//     every bot only reads the files; no bot sandbox changes.
//   - Attachments the agents save under <work_dir>/.cc-connect/attachments are handed to
//     `mediaarchive attach`, which copies them straight to the NAS (no queue, no LaunchAgent).
//   - NAS not mounted: the video is still parsed (watchvid falls back to a local temp dir that
//     is never moved), archiving fails, and the message says "NAS 没挂，这条没存".
//
// Off unless EnableMediaLibrary is called (cmd/cc-connect does it when the scripts exist).

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	mediaEnabled     bool
	watchvidPath     string
	mediaArchivePath string
	mediaNASRoot     = "/Volumes/personal_folder"
	// mediaNASMounted reports whether the NAS share is mounted (var for tests).
	mediaNASMounted = func() bool { return isMountPoint(mediaNASRoot) }
	// runWatchvid runs watchvid on url and returns its stdout and stderr (var for tests).
	runWatchvid = func(ctx context.Context, link string) (string, string, error) {
		cmd := exec.CommandContext(ctx, watchvidPath, link)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
	// archiveAttachmentsFn hands saved attachment paths to mediaarchive (var for tests).
	archiveAttachmentsFn = func(paths []string) {
		cmd := exec.Command(mediaArchivePath, append([]string{"attach"}, paths...)...)
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			slog.Warn("mediaarchive: attachments not archived", "paths", paths, "error", err, "stderr", lastLine(errb.String()))
			return
		}
		slog.Info("mediaarchive: attachments archived", "paths", paths)
	}
)

// EnableMediaLibrary turns the media library on when both scripts exist under home.
func EnableMediaLibrary(home string) {
	wv := filepath.Join(home, "im-bridge", "bin", "watchvid")
	ma := filepath.Join(home, "im-bridge", "bin", "mediaarchive")
	for _, p := range []string{wv, ma} {
		if _, err := os.Stat(p); err != nil {
			slog.Info("media library off: script missing", "path", p)
			return
		}
	}
	watchvidPath, mediaArchivePath, mediaEnabled = wv, ma, true
	slog.Info("media library on", "watchvid", wv, "mediaarchive", ma, "nas", mediaNASRoot)
}

// ArchiveAttachments archives attachment files an agent session has just saved to disk.
// Runs in the background; a no-op when the media library is off.
func ArchiveAttachments(paths []string) {
	if !mediaEnabled || len(paths) == 0 {
		return
	}
	go archiveAttachmentsFn(append([]string(nil), paths...))
}

// ── video links ──────────────────────────────────────────────

type videoLink struct {
	URL string // passed to watchvid
	Key string // same video → same key (dedup)
}

// Characters that end a link in chat text: whitespace, brackets Discord or people put
// around links, and CJK punctuation.
const linkTail = `[^\s<>()（）【】\[\]"'，。！？、；]*`

var (
	reBiliVideo = regexp.MustCompile(`https?://(?:www\.|m\.)?bilibili\.com/video/(BV[0-9A-Za-z]{10})` + linkTail)
	reB23       = regexp.MustCompile(`https?://(?:www\.)?b23\.tv/[0-9A-Za-z]+`)
	reYouTube   = regexp.MustCompile(`https?://(?:(?:www\.|m\.)?youtube\.com/(?:watch\?` + linkTail + `|shorts/[\w-]{11}|live/[\w-]{11})|youtu\.be/[\w-]{11})` + linkTail)
	reYouTubeID = regexp.MustCompile(`(?:[?&]v=|shorts/|live/|youtu\.be/)([\w-]{11})`)
)

// findVideoLinks returns the video links in text that watchvid handles, in order, one per video.
func findVideoLinks(text string) []videoLink {
	type hit struct {
		pos  int
		link videoLink
	}
	var hits []hit
	for _, m := range reBiliVideo.FindAllStringSubmatchIndex(text, -1) {
		u := text[m[0]:m[1]]
		key := "bili:" + text[m[2]:m[3]]
		if pu, err := url.Parse(u); err == nil {
			if p := pu.Query().Get("p"); p != "" && p != "1" {
				key += ":p" + p
			}
		}
		hits = append(hits, hit{m[0], videoLink{u, key}})
	}
	for _, m := range reB23.FindAllStringIndex(text, -1) {
		u := text[m[0]:m[1]]
		hits = append(hits, hit{m[0], videoLink{u, "b23:" + u[strings.LastIndex(u, "/")+1:]}})
	}
	for _, m := range reYouTube.FindAllStringIndex(text, -1) {
		u := text[m[0]:m[1]]
		id := reYouTubeID.FindStringSubmatch(u)
		if id == nil {
			continue
		}
		hits = append(hits, hit{m[0], videoLink{u, "yt:" + id[1]}})
	}
	// order by position, drop repeats of the same video
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].pos < hits[j-1].pos; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	seen := map[string]bool{}
	var out []videoLink
	for _, h := range hits {
		if !seen[h.link.Key] {
			seen[h.link.Key] = true
			out = append(out, h.link)
		}
	}
	return out
}

type videoResult struct {
	Index string // watchvid-index.md path
	Err   string
}

func (r videoResult) onNAS() bool { return strings.HasPrefix(r.Index, mediaNASRoot+"/") }

type watchvidCall struct {
	done chan struct{}
	res  videoResult
}

var (
	videoMu    sync.Mutex
	videoCalls = map[string]*watchvidCall{} // key → running or finished (successful) watchvid run
)

// parseVideo runs watchvid for link once per process: concurrent callers share the running call,
// later callers get the finished result. Failed runs are forgotten, so a later message retries.
func parseVideo(ctx context.Context, link videoLink) videoResult {
	videoMu.Lock()
	if c, ok := videoCalls[link.Key]; ok {
		videoMu.Unlock()
		select {
		case <-c.done:
			slog.Info("watchvid: cached", "key", link.Key, "index", c.res.Index)
		default:
			slog.Info("watchvid: shared, waiting for the running parse", "key", link.Key)
			<-c.done
		}
		return c.res
	}
	c := &watchvidCall{done: make(chan struct{})}
	videoCalls[link.Key] = c
	videoMu.Unlock()

	slog.Info("watchvid: start", "key", link.Key, "url", link.URL)
	stdout, stderr, err := runWatchvid(ctx, link.URL)
	idx := lastLine(stdout)
	switch {
	case err != nil:
		c.res = videoResult{Err: fmt.Sprintf("%v: %s", err, lastLine(stderr))}
	case idx == "":
		c.res = videoResult{Err: "watchvid printed no path: " + lastLine(stderr)}
	default:
		c.res = videoResult{Index: idx}
	}
	if c.res.Err != "" {
		slog.Warn("watchvid: failed", "key", link.Key, "url", link.URL, "error", c.res.Err)
		videoMu.Lock()
		delete(videoCalls, link.Key)
		videoMu.Unlock()
	} else {
		slog.Info("watchvid: done", "key", link.Key, "index", idx, "on_nas", c.res.onNAS())
	}
	close(c.done)
	return c.res
}

// cachedVideoResult returns a finished successful parse of link, without waiting or running.
func cachedVideoResult(link videoLink) (videoResult, bool) {
	videoMu.Lock()
	c, ok := videoCalls[link.Key]
	videoMu.Unlock()
	if !ok {
		return videoResult{}, false
	}
	select {
	case <-c.done:
		return c.res, c.res.Err == ""
	default:
		return videoResult{}, false
	}
}

const nasMissingNote = "NAS 没挂，这条没存"

func videoNote(link videoLink, r videoResult) string {
	if r.Err != "" {
		return fmt.Sprintf("（cc-connect 用 watchvid 解析视频 %s 失败：%s）", link.URL, r.Err)
	}
	dir := filepath.Dir(r.Index)
	if !r.onNAS() {
		return fmt.Sprintf("（cc-connect 已用 watchvid 解析视频 %s：先读 %s，关键帧、音频、转写都在同一目录，只读即可，不用再跑 watchvid。%s：这份结果在本机临时目录，没进媒体库。）",
			link.URL, r.Index, nasMissingNote)
	}
	return fmt.Sprintf("（cc-connect 已用 watchvid 解析视频 %s：先读 %s，关键帧 frames/、音频 audio.m4a、转写、弹幕、热评都在同一目录，只读即可，不用再跑 watchvid。"+
		"已存进媒体库 %s，wiki 目录 ~/wiki/refs/media/ 有一行；原视频默认不存，用户明确要存时跑 `mediaarchive --keep-video %s`。）",
		link.URL, r.Index, dir, filepath.Base(dir))
}

// cachedVideoNotes returns notes for the already-parsed video links in text (no waiting, no
// parsing) — for channel backfill, where a message seen only as context shows up again.
func cachedVideoNotes(text string) string {
	if !mediaEnabled {
		return ""
	}
	var notes []string
	for _, l := range findVideoLinks(text) {
		if r, ok := cachedVideoResult(l); ok {
			notes = append(notes, videoNote(l, r))
		}
	}
	return strings.Join(notes, "")
}

// mediaLock serialises media preparation per session, so a message sent right after a video
// link (「你们看看」) does not overtake it while watchvid runs.
func (e *Engine) mediaLock(key string) *sync.Mutex {
	mu, _ := e.mediaLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// prepareMedia parses the video links in a user message (once per link across all bots) and
// appends the result paths; it also notes when attachments can't be archived. Blocks while
// watchvid runs.
func (e *Engine) prepareMedia(msg *Message, key string) {
	if !mediaEnabled || msg.FromBot || msg.mediaDone {
		return
	}
	msg.mediaDone = true
	mu := e.mediaLock(key)
	mu.Lock()
	defer mu.Unlock()

	var notes []string
	for _, l := range findVideoLinks(msg.Content) {
		r := parseVideo(e.ctx, l)
		notes = append(notes, videoNote(l, r))
		slog.Info("media: video note added", "project", e.name, "session", msg.SessionKey, "key", l.Key, "index", r.Index, "error", r.Err)
	}
	if (len(msg.Images) > 0 || len(msg.Files) > 0) && !mediaNASMounted() {
		notes = append(notes, "（"+nasMissingNote+"：这条消息的附件没存进媒体库。）")
		slog.Warn("media: NAS not mounted, attachments not archived", "project", e.name, "session", msg.SessionKey)
	}
	if len(notes) == 0 {
		return
	}
	msg.mediaNote = "\n\n" + strings.Join(notes, "\n")
	msg.Content += msg.mediaNote
}

// withoutMediaNote is msg.Content minus the appended media note (what the user wrote), for the gate.
func withoutMediaNote(msg *Message) string {
	if msg.mediaNote != "" {
		return strings.TrimSuffix(msg.Content, msg.mediaNote)
	}
	return msg.Content
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
