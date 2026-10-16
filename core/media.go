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
	"time"
)

var (
	// watchvidTimeout bounds one watchvid run; on timeout the whole process group is killed and the
	// message goes on with a failure note. Source: 2026-10-17 measurements — a 19 s YouTube clip took
	// 127 s (the slowest seen, mostly yt-dlp), a 19 s Bilibili clip 13.5 s; long videos add ASR time
	// per 30 s segment, so ~5× the slowest measurement: 600 s.
	watchvidTimeout = 10 * time.Minute
	// attachTimeout bounds one `mediaarchive attach`. Source: its commit_wiki runs git pull and push
	// with 180 s each over two routes (proxy, direct) = 720 s worst case, plus the NAS copy: 15 min.
	attachTimeout = 15 * time.Minute
	// attachSlots caps concurrent `mediaarchive attach` runs. Source: the four bots each save the same
	// attachment at once (4 runs per message); mediaarchive serialises on a file lock anyway.
	attachSlots = make(chan struct{}, 4)
	// mediaNoteKeep caps the remembered per-message notes for channel backfill; same cap as the gate's
	// silent buffer (maxBuffered), which is what backfill replaces.
	mediaNoteKeep = maxBuffered
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
		return runProcessGroup(ctx, watchvidPath, link)
	}
	// runMediaArchive runs `mediaarchive attach <paths>` (var for tests).
	runMediaArchive = func(ctx context.Context, paths []string) (string, string, error) {
		return runProcessGroup(ctx, mediaArchivePath, append([]string{"attach"}, paths...)...)
	}
)

// runProcessGroup runs name in its own process group; when ctx ends the whole group is killed
// (watchvid starts uv, yt-dlp, ffmpeg …) and Run returns even if a grandchild kept the pipes.
func runProcessGroup(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	killGroupOnCancel(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("超时（%s）已终止: %w", ctxLimit(ctx), ctx.Err())
	}
	return out.String(), errb.String(), err
}

func ctxLimit(ctx context.Context) string {
	if v, ok := ctx.Value(limitKey{}).(time.Duration); ok {
		return v.String()
	}
	return "context done"
}

type limitKey struct{}

// withLimit is context.WithTimeout that remembers the limit for error messages.
func withLimit(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(parent, limitKey{}, d), d)
}

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
// Runs in the background with at most cap(attachSlots) runs at once, each bounded by
// attachTimeout; failures are only logged. A no-op when the media library is off.
func ArchiveAttachments(paths []string) {
	if !mediaEnabled || len(paths) == 0 {
		return
	}
	// Read the settings here, not in the goroutine: it outlives this call (and, in tests, the
	// settings), so it must not touch the package variables afterwards.
	run, limit := runMediaArchive, attachTimeout
	paths = append([]string(nil), paths...)
	attachInFlight.Add(1)
	go func() {
		defer attachInFlight.Done()
		archiveAttachments(paths, run, limit)
	}()
}

// attachInFlight counts running archiveAttachments goroutines (tests wait on it).
var attachInFlight sync.WaitGroup

// archiveAttachments waits for a slot and runs mediaarchive; slot wait and run share one limit,
// so a job ends (archived, failed or given up) within limit of being queued.
func archiveAttachments(paths []string, run func(context.Context, []string) (string, string, error), limit time.Duration) {
	ctx, cancel := withLimit(context.Background(), limit)
	defer cancel()
	select {
	case attachSlots <- struct{}{}:
		defer func() { <-attachSlots }()
	case <-ctx.Done():
		slog.Error("mediaarchive: no free slot within the timeout, attachments not archived", "paths", paths)
		return
	}
	_, stderr, err := run(ctx, paths)
	if err != nil {
		slog.Error("mediaarchive: attachments not archived", "paths", paths, "error", err, "stderr", lastLine(stderr))
		return
	}
	slog.Info("mediaarchive: attachments archived", "paths", paths)
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

// watchvidRun is one watchvid process; everyone waiting for the same video shares it.
type watchvidRun struct {
	done chan struct{}
	res  videoResult // set before done is closed
}

func (r *watchvidRun) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// msgVideo is the result for one video link in one platform message, shared by every bot that
// gets that message — failures too, so a dead link is tried once per message, not once per bot.
// A later message with the same link starts a new run (watchvid itself returns at once when the
// NAS entry exists), so the note always reflects the NAS state at that time.
type msgVideo struct {
	channel, msgID string
	run            *watchvidRun
	consumed       map[*Engine]bool // bots that took the result or are past this message
}

var (
	videoMu      sync.Mutex
	videoRunning = map[string]*watchvidRun{} // link key → running watchvid; removed when it ends
	videoByMsg   = map[string]*msgVideo{}    // msgID|link key → result; removed once every bot is past it
	mediaEngines = map[*Engine]bool{}        // bots that get user messages (registered in Start, one per project)
)

// parseVideo returns the watchvid result for link in msg. Bots handling the same message share one
// result; concurrent messages with the same link share one running watchvid. Waiting stops at ctx.
func (e *Engine) parseVideo(ctx context.Context, msg *Message, link videoLink) videoResult {
	videoMu.Lock()
	var run *watchvidRun
	mk := ""
	if msg.MessageID != "" {
		mk = msg.MessageID + "|" + link.Key
		if mv := videoByMsg[mk]; mv != nil {
			run = mv.run
			mv.consumed[e] = true
			slog.Info("watchvid: shared (same message)", "project", e.name, "key", link.Key, "msg_id", msg.MessageID, "finished", run.finished())
			gcVideoByMsgLocked()
		}
	}
	if run == nil {
		if r := videoRunning[link.Key]; r != nil {
			run = r
			slog.Info("watchvid: shared (already running)", "project", e.name, "key", link.Key)
		} else {
			run = &watchvidRun{done: make(chan struct{})}
			videoRunning[link.Key] = run
			slog.Info("watchvid: start", "project", e.name, "key", link.Key, "url", link.URL, "msg_id", msg.MessageID)
			go execWatchvid(e.ctx, run, link, runWatchvid, watchvidTimeout) // engine shutdown also kills it
		}
		if mk != "" {
			videoByMsg[mk] = &msgVideo{channel: mediaChannel(msg), msgID: msg.MessageID, run: run, consumed: map[*Engine]bool{e: true}}
		}
	}
	videoMu.Unlock()

	select {
	case <-run.done:
		return run.res
	case <-ctx.Done():
		return videoResult{Err: fmt.Sprintf("等解析结果超时（%s），watchvid 还没跑完", ctxLimit(ctx))}
	}
}

// execWatchvid gets the runner and limit from its caller (see ArchiveAttachments for why).
func execWatchvid(parent context.Context, run *watchvidRun, link videoLink,
	runner func(context.Context, string) (string, string, error), limit time.Duration) {
	ctx, cancel := withLimit(parent, limit)
	defer cancel()
	stdout, stderr, err := runner(ctx, link.URL)
	idx := lastLine(stdout)
	var res videoResult
	switch {
	case err != nil:
		res = videoResult{Err: fmt.Sprintf("%v: %s", err, lastLine(stderr))}
	case idx == "":
		res = videoResult{Err: "watchvid printed no path: " + lastLine(stderr)}
	default:
		res = videoResult{Index: idx}
	}
	if res.Err != "" {
		slog.Warn("watchvid: failed", "key", link.Key, "url", link.URL, "error", res.Err)
	} else {
		slog.Info("watchvid: done", "key", link.Key, "index", idx, "on_nas", res.onNAS())
	}
	videoMu.Lock()
	run.res = res
	close(run.done)
	if videoRunning[link.Key] == run {
		delete(videoRunning, link.Key)
	}
	gcVideoByMsgLocked()
	videoMu.Unlock()
}

func mediaChannel(msg *Message) string {
	if msg.ChannelID != "" {
		return msg.ChannelID
	}
	return msg.SessionKey
}

// registerMediaEngine counts e among the bots a per-message result is kept for (Engine.Start).
func registerMediaEngine(e *Engine) {
	videoMu.Lock()
	mediaEngines[e] = true
	videoMu.Unlock()
}

// videoPassed records that bot e is handling msg: per-message results of earlier messages in the
// same channel are no longer needed by e. Finished results every bot is past are dropped.
func (e *Engine) videoPassed(msg *Message) {
	videoMu.Lock()
	defer videoMu.Unlock()
	mediaEngines[e] = true
	ch := mediaChannel(msg)
	for _, mv := range videoByMsg {
		if mv.channel == ch && msg.MessageID != "" && snowflakeAfter(msg.MessageID, mv.msgID) {
			mv.consumed[e] = true
		}
	}
	gcVideoByMsgLocked()
}

func gcVideoByMsgLocked() {
	for k, mv := range videoByMsg {
		if !mv.run.finished() {
			continue
		}
		all := true
		for eng := range mediaEngines {
			if !mv.consumed[eng] {
				all = false
				break
			}
		}
		if all {
			delete(videoByMsg, k)
		}
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

// ── per-session ordering ─────────────────────────────────────

type mediaLockEntry struct {
	ch   chan struct{} // holds one token while a message of this session is being prepared
	refs int
}

// acquireMediaLock serialises media preparation per session, so a message sent right after a
// video link (「你们看看」) does not overtake it while watchvid runs. The entry is removed when
// nobody holds or waits for it. ok=false: ctx ended while waiting.
func (e *Engine) acquireMediaLock(ctx context.Context, key string) (release func(), ok bool) {
	e.mediaLockMu.Lock()
	if e.mediaLockMap == nil {
		e.mediaLockMap = map[string]*mediaLockEntry{}
	}
	ent := e.mediaLockMap[key]
	if ent == nil {
		ent = &mediaLockEntry{ch: make(chan struct{}, 1)}
		e.mediaLockMap[key] = ent
	}
	ent.refs++
	e.mediaLockMu.Unlock()

	drop := func() {
		e.mediaLockMu.Lock()
		ent.refs--
		if ent.refs == 0 {
			delete(e.mediaLockMap, key)
		}
		e.mediaLockMu.Unlock()
	}
	select {
	case ent.ch <- struct{}{}:
		return func() { <-ent.ch; drop() }, true
	case <-ctx.Done():
		drop()
		return nil, false
	}
}

// ── message ──────────────────────────────────────────────────

// beginMedia parses the video links in a user message (one watchvid result per message and
// link, shared by all bots) and appends the result paths; it also notes when attachments can't be
// archived. Blocks while watchvid runs, at most watchvidTimeout.
//
// Ordering: a message waits here while an earlier message of the same session is prepared. A
// message with video links keeps the session's turn until the caller calls the returned release
// after the gate has taken it — otherwise a follow-up (「你们看看」) released at the same moment
// could reach the gate first and the burst would read in the wrong order. Messages without links
// release at once, so they add no wait for anyone.
func (e *Engine) beginMedia(msg *Message, key string) (release func()) {
	release = func() {}
	if !mediaEnabled || msg.FromBot || msg.mediaDone {
		return
	}
	msg.mediaDone = true
	e.videoPassed(msg)
	links := findVideoLinks(msg.Content)

	ctx, cancel := withLimit(e.ctx, watchvidTimeout)
	defer cancel()
	if rel, ok := e.acquireMediaLock(ctx, key); ok {
		if len(links) > 0 {
			release = rel
		} else {
			defer rel()
		}
	} else {
		slog.Warn("media: gave up waiting for the previous video message in this session", "project", e.name, "session", msg.SessionKey)
	}

	var notes []string
	for _, l := range links {
		r := e.parseVideo(ctx, msg, l)
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
	note := strings.Join(notes, "")
	msg.userText, msg.userTextSet = msg.Content, true
	msg.Content += "\n\n" + note
	e.rememberMediaNote(msg.MessageID, note)
	return
}

// userTextOf is what the user wrote, without media notes — the gate's input.
func userTextOf(msg *Message) string {
	if msg.userTextSet {
		return msg.userText
	}
	return msg.Content
}

// rememberMediaNote keeps the note for channel backfill: a message the gate only buffered shows up
// again from the channel history (raw text), and needs its note there.
func (e *Engine) rememberMediaNote(msgID, note string) {
	if msgID == "" {
		return
	}
	e.mediaNoteMu.Lock()
	defer e.mediaNoteMu.Unlock()
	if e.mediaNotes == nil {
		e.mediaNotes = map[string]string{}
	}
	if _, ok := e.mediaNotes[msgID]; !ok {
		e.mediaNoteIDs = append(e.mediaNoteIDs, msgID)
	}
	e.mediaNotes[msgID] = note
	for len(e.mediaNoteIDs) > mediaNoteKeep {
		delete(e.mediaNotes, e.mediaNoteIDs[0])
		e.mediaNoteIDs = e.mediaNoteIDs[1:]
	}
}

// mediaNoteFor returns the remembered note of a channel message ("" if none).
func (e *Engine) mediaNoteFor(msgID string) string {
	e.mediaNoteMu.Lock()
	defer e.mediaNoteMu.Unlock()
	return e.mediaNotes[msgID]
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
