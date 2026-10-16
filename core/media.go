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
	// attachQueueLen bounds the attachment jobs waiting for the worker; when full the job is
	// dropped with a log line and the message goes on. Source: the four bots each save the same
	// attachment at once (4 jobs per message); room for 4 such messages back to back = 16 (the
	// "4 messages" is our choice).
	attachQueueLen = 16
	// videoKeep is how many per-message video results are kept (FIFO; running ones are never
	// evicted). Source: same cap as the gate's silent buffer, maxBuffered (30).
	videoKeep = maxBuffered
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

// ArchiveAttachments queues attachment files an agent session has just saved to disk for
// `mediaarchive attach`. One worker runs the jobs in order (mediaarchive serialises on a file lock
// anyway), each bounded by attachTimeout; when attachQueueLen jobs are already waiting the job is
// dropped and logged. Never blocks; failures are only logged. Returns whether the job was queued.
func ArchiveAttachments(paths []string) bool {
	if !mediaEnabled || len(paths) == 0 {
		return false
	}
	attachOnce.Do(func() {
		attachQueue = make(chan attachJob, attachQueueLen)
		go attachWorker(attachQueue)
	})
	// The settings are read here, not in the worker (which outlives the call and, in tests, the settings).
	job := attachJob{paths: append([]string(nil), paths...), run: runMediaArchive, limit: attachTimeout}
	attachInFlight.Add(1)
	select {
	case attachQueue <- job:
		return true
	default:
		attachInFlight.Done()
		slog.Error("mediaarchive: queue full, attachments not archived", "paths", paths, "queue", attachQueueLen)
		return false
	}
}

type attachJob struct {
	paths []string
	run   func(context.Context, []string) (string, string, error)
	limit time.Duration
}

var (
	attachOnce     sync.Once
	attachQueue    chan attachJob
	attachInFlight sync.WaitGroup // queued + running jobs (tests wait on it)
)

func attachWorker(q <-chan attachJob) {
	for job := range q {
		ctx, cancel := withLimit(context.Background(), job.limit)
		_, stderr, err := job.run(ctx, job.paths)
		cancel()
		if err != nil {
			slog.Error("mediaarchive: attachments not archived", "paths", job.paths, "error", err, "stderr", lastLine(stderr))
		} else {
			slog.Info("mediaarchive: attachments archived", "paths", job.paths)
		}
		attachInFlight.Done()
	}
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

var (
	videoMu      sync.Mutex
	videoRunning = map[string]*watchvidRun{} // link key → running watchvid; removed when it ends
	// videoByMsg: platform|channel|message ID|link key → that message's run (failures too), so
	// every bot handling the same message shares one result and a dead link is tried once per
	// message. The next message with the link runs watchvid again (it returns at once when the
	// NAS entry exists), so each note reflects the NAS state at that time. Kept FIFO, at most
	// videoKeep finished entries; running entries are never evicted.
	videoByMsg    = map[string]*watchvidRun{}
	videoMsgOrder []string
)

// parseVideo returns the watchvid result for link in msg. Bots handling the same message share one
// result; concurrent messages with the same link share one running watchvid. Waiting stops at ctx,
// and nothing new is started once ctx is done.
func (e *Engine) parseVideo(ctx context.Context, msg *Message, link videoLink) videoResult {
	videoMu.Lock()
	mk := ""
	if msg.MessageID != "" {
		mk = strings.Join([]string{msg.Platform, mediaChannel(msg), msg.MessageID, link.Key}, "|")
	}
	run := videoByMsg[mk]
	if run != nil {
		slog.Info("watchvid: shared (same message)", "project", e.name, "key", link.Key, "msg_id", msg.MessageID, "finished", run.finished())
	} else {
		if run = videoRunning[link.Key]; run != nil {
			slog.Info("watchvid: shared (already running)", "project", e.name, "key", link.Key)
		} else {
			if ctx.Err() != nil {
				videoMu.Unlock()
				return videoResult{Err: fmt.Sprintf("等待超时（%s），没有开始解析", ctxLimit(ctx))}
			}
			run = &watchvidRun{done: make(chan struct{})}
			videoRunning[link.Key] = run
			slog.Info("watchvid: start", "project", e.name, "key", link.Key, "url", link.URL, "msg_id", msg.MessageID)
			go execWatchvid(e.ctx, run, link, runWatchvid, watchvidTimeout) // engine shutdown also kills it
		}
		if mk != "" {
			videoByMsg[mk] = run
			videoMsgOrder = append(videoMsgOrder, mk)
			evictVideoByMsgLocked()
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

// evictVideoByMsgLocked drops the oldest finished per-message results beyond videoKeep.
func evictVideoByMsgLocked() {
	over := len(videoMsgOrder) - videoKeep
	if over <= 0 {
		return
	}
	kept := videoMsgOrder[:0]
	for _, k := range videoMsgOrder {
		if over > 0 && videoByMsg[k].finished() {
			delete(videoByMsg, k)
			over--
			continue
		}
		kept = append(kept, k)
	}
	videoMsgOrder = kept
}

// execWatchvid gets the runner and limit from its caller (read before the goroutine starts).
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
	evictVideoByMsgLocked()
	videoMu.Unlock()
}

func mediaChannel(msg *Message) string {
	if msg.ChannelID != "" {
		return msg.ChannelID
	}
	return msg.SessionKey
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

// ── message ──────────────────────────────────────────────────

// prepareMedia parses the video links in a user message (one watchvid result per message and
// link, shared by all bots) and appends the result paths; it also notes when attachments can't be
// archived. Only messages with links wait (at most watchvidTimeout); others touch no shared
// state and add no wait. No ordering between messages: a follow-up may reach the gate first.
func (e *Engine) prepareMedia(msg *Message) {
	if !mediaEnabled || msg.FromBot || msg.mediaDone {
		return
	}
	msg.mediaDone = true
	links := findVideoLinks(msg.Content)
	var notes []string
	if len(links) > 0 {
		ctx, cancel := withLimit(e.ctx, watchvidTimeout)
		defer cancel()
		for i, l := range links {
			if ctx.Err() != nil {
				var rest []string
				for _, r := range links[i:] {
					rest = append(rest, r.URL)
				}
				notes = append(notes, fmt.Sprintf("（解析超时（%s），这些链接没有解析：%s）", ctxLimit(ctx), strings.Join(rest, " ")))
				slog.Warn("media: timeout, links left unparsed", "project", e.name, "session", msg.SessionKey, "count", len(rest))
				break
			}
			r := e.parseVideo(ctx, msg, l)
			notes = append(notes, videoNote(l, r))
			slog.Info("media: video note added", "project", e.name, "session", msg.SessionKey, "key", l.Key, "index", r.Index, "error", r.Err)
		}
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
