package core

// maomao fork: the channel task board ("频道任务看板").
//
// One pinned message per Discord channel shows what every bot of this process
// is doing there: the turns running now (with the progress card's current step
// and the Claude Code bot's sub-agents under it), what is waiting (messages
// queued behind a busy turn, the channel's cron/timer jobs), and the last few
// turns finished today. All bots of the process share the one message; the
// project with channel_board = true (橘) posts, pins and edits it. Changes in a
// channel within a short window are merged into one edit. The message ID is kept
// in <data_dir>/channel_board.json, so after a restart the same message is
// edited again; when it is gone a new one is posted and pinned.
//
// The board is an overview; the progress card and the sub-agent table stay as
// they are (live detail).
//
// User 2026-10-21: 「就那个置顶来显示我们当前频道的任务队列或者说任务情况」.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChannelBoardPayloadPrefix marks a structured channel board message.
const ChannelBoardPayloadPrefix = "__cc_connect_channel_board_v1__:"

// channelBoardDoneMax: rows kept in "今天做完的".
// 来源：橘定，用户 2026-10-21 认可"只留最近几条"。
const channelBoardDoneMax = 5

// channelBoardFile is the board state under the writer's data dir.
const channelBoardFile = "channel_board.json"

// ErrChannelBoardGone is returned by ChannelBoardPoster.EditChannelBoard when
// the board message no longer exists (deleted): the hub posts a new one.
var ErrChannelBoardGone = errors.New("channel board message gone")

// ChannelBoardPoster is implemented by platforms that can host the board
// (Discord). Content is a ChannelBoardPayload string.
type ChannelBoardPoster interface {
	PostChannelBoard(ctx context.Context, channelID, content string) (messageID string, err error)
	EditChannelBoard(ctx context.Context, channelID, messageID, content string) error
	PinChannelBoard(ctx context.Context, channelID, messageID string) error
	// FindChannelBoard looks for a board this bot already posted in the
	// channel (pinned first, then the latest messages), for when the stored ID
	// was lost or a post's response never arrived. "" = none.
	FindChannelBoard(ctx context.Context, channelID string) (messageID string, pinned bool, err error)
}

// Board outcomes of a finished turn.
const (
	BoardDoneCompleted = "completed"
	BoardDoneFailed    = "failed"
	BoardDoneStopped   = "stopped"
	// boardDoneSilent: the turn ended with NO_REPLY (the bot chose not to
	// speak). It leaves "在做的" but is not listed as done work.
	boardDoneSilent = "silent"
)

// BoardRunning is one bot's turn in progress.
type BoardRunning struct {
	Bot       string             `json:"bot"`
	Desc      string             `json:"desc,omitempty"`
	Step      *ProgressCardEntry `json:"step,omitempty"` // the progress card's newest item
	StartedAt time.Time          `json:"started_at,omitzero"`
	Subs      []SubTask          `json:"subs,omitempty"` // sub-agents still running
}

// BoardQueued is one waiting item: a message queued behind a busy turn, or a
// cron/timer job of this channel that has not fired yet.
type BoardQueued struct {
	Bot  string    `json:"bot"`
	Kind string    `json:"kind"` // "message" | "cron" | "timer"
	Desc string    `json:"desc,omitempty"`
	At   time.Time `json:"at,omitzero"` // next run (cron/timer)
}

// BoardDone is a finished turn.
type BoardDone struct {
	Bot     string    `json:"bot"`
	Desc    string    `json:"desc,omitempty"`
	EndedAt time.Time `json:"ended_at"`
	Outcome string    `json:"outcome"`
}

// ChannelBoardPayload is the transport form of the board.
type ChannelBoardPayload struct {
	Lang    string         `json:"lang,omitempty"`
	Running []BoardRunning `json:"running"`
	Queued  []BoardQueued  `json:"queued"`
	Done    []BoardDone    `json:"done"`
}

// BuildChannelBoardPayload encodes the board.
func BuildChannelBoardPayload(p ChannelBoardPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return ChannelBoardPayloadPrefix + string(b)
}

// ParseChannelBoardPayload decodes a payload built by BuildChannelBoardPayload.
func ParseChannelBoardPayload(content string) (*ChannelBoardPayload, bool) {
	if !strings.HasPrefix(content, ChannelBoardPayloadPrefix) {
		return nil, false
	}
	var p ChannelBoardPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(content, ChannelBoardPayloadPrefix)), &p); err != nil {
		return nil, false
	}
	return &p, true
}

// boardChannelOf returns the Discord channel ID of a session or interactive
// key ("discord:<channel>[:<user>]", maybe with a workspace prefix or a
// "#cron:<id>" suffix); "" for other platforms.
func boardChannelOf(key string) string {
	i := strings.Index(key, "discord:")
	if i < 0 || (i > 0 && key[i-1] != ':') {
		return ""
	}
	key = key[i:]
	if j := strings.Index(key, "#"); j >= 0 {
		key = key[:j]
	}
	return extractChannelID(key)
}

// boardDescOf is the board's short text for the turn msg starts: the cron/timer
// label, else what the sender wrote (without backfill), one line.
func boardDescOf(msg *Message) string {
	if msg.boardLabel != "" {
		return msg.boardLabel
	}
	return boardShort(userTextOf(msg))
}

// boardShort collapses whitespace and cuts to the team status task length
// (teamTaskMaxRunes, team.go), so the board and 队友状态 say the same thing.
func boardShort(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > teamTaskMaxRunes {
		s = string(r[:teamTaskMaxRunes]) + "…"
	}
	return s
}

type boardTurn struct {
	engine    *Engine
	key       string // interactive key
	channel   string
	seq       uint64
	desc      string
	step      *ProgressCardEntry
	startedAt time.Time
	// held: the turn hit "model at capacity" and will be re-sent (retry or
	// after the cooldown); the re-sent turn takes this row over, so a retry
	// that gets through leaves one row and no failure.
	held bool
}

type boardChannel struct {
	MessageID string      `json:"message_id,omitempty"`
	Pinned    bool        `json:"pinned,omitempty"`
	Done      []BoardDone `json:"done,omitempty"`

	dirty      bool
	working    bool
	lastSent   string
	pinRetried bool
	backoff    time.Duration // wait before retrying a failed send (0 = none failed)
}

// channelBoardHub is process-wide: every engine reports to it, the writer
// engine's platform posts.
type channelBoardHub struct {
	mu       sync.Mutex
	sources  []*Engine
	writer   *Engine
	poster   ChannelBoardPoster
	path     string
	window   time.Duration
	ctx      context.Context // cancelled = workers stop waiting (disable, shutdown)
	cancel   context.CancelFunc
	workers  sync.WaitGroup // running channel workers (shutdown waits for them)
	stopping bool           // shutdown under way: no new workers
	// shutdownTimeout bounds the whole shutdown (tests shorten it).
	shutdownTimeout time.Duration
	seq             uint64
	turns           map[string]*boardTurn // engine name + "\x00" + interactive key
	channels        map[string]*boardChannel
	now             func() time.Time
}

func newChannelBoardHub() *channelBoardHub {
	return &channelBoardHub{turns: map[string]*boardTurn{}, channels: map[string]*boardChannel{}, now: time.Now,
		shutdownTimeout: compactProgressAPITimeout}
}

// defaultChannelBoard is shared by all engines of the process (the four bots
// run in one cc-connect).
var defaultChannelBoard = newChannelBoardHub()

func (h *channelBoardHub) addSource(e *Engine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sources {
		if s == e {
			return
		}
	}
	h.sources = append(h.sources, e)
}

// removeSource drops a stopping engine; its turns still running end as
// stopped (its agents die with it).
func (h *channelBoardHub) removeSource(e *Engine) {
	h.mu.Lock()
	out := h.sources[:0]
	for _, s := range h.sources {
		if s != e {
			out = append(out, s)
		}
	}
	h.sources = out
	var chans []string
	for k, t := range h.turns {
		if t.engine == e {
			h.addDoneLocked(t.channel, BoardDone{Bot: e.boardName(), Desc: t.desc, EndedAt: h.now(), Outcome: BoardDoneStopped})
			delete(h.turns, k)
			chans = append(chans, t.channel)
		}
	}
	if len(chans) > 0 {
		h.saveLocked()
	}
	h.mu.Unlock()
	for _, ch := range chans {
		h.touch(ch)
	}
}

// SetChannelBoard (maomao fork) makes this project post the channel board
// (config channel_board = true). Only one project of the process should; the
// last one enabled wins. Only its Discord platform is used.
func (e *Engine) SetChannelBoard(enabled bool) {
	h := e.board
	if h == nil {
		return
	}
	if !enabled {
		h.mu.Lock()
		if h.writer == e {
			h.disableLocked()
		}
		h.mu.Unlock()
		return
	}
	var poster ChannelBoardPoster
	var window time.Duration
	for _, p := range e.platforms {
		if bp, ok := p.(ChannelBoardPoster); ok && p.Name() == "discord" {
			poster = bp
			// Merge window: 沿用 the progress card's edit interval on this platform
			// (platform/discord ProgressUpdateInterval, 2s). Discord publishes no
			// fixed number for message edits — "rate limits should not be hard
			// coded into your app" (docs.discord.com/developers/topics/rate-limits);
			// discordgo waits on the per-route bucket headers and 429s itself.
			if th, ok := p.(ProgressUpdateThrottler); ok {
				window = th.ProgressUpdateInterval()
			}
			break
		}
	}
	if poster == nil {
		slog.Warn("channel board: project has no Discord platform; board stays off", "project", e.name)
		return
	}
	path := ""
	if e.dataDir != "" {
		path = filepath.Join(e.dataDir, channelBoardFile)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writer == e && h.poster == poster && h.path == path {
		return // config reload, unchanged
	}
	h.disableLocked()
	h.writer, h.poster, h.path, h.window = e, poster, path, window
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.loadLocked()
	slog.Info("channel board enabled", "project", e.name, "state", path)
}

func (h *channelBoardHub) disableLocked() {
	if h.cancel != nil {
		h.cancel()
	}
	h.writer, h.poster, h.cancel, h.ctx, h.stopping = nil, nil, nil, nil, false
	h.turns = map[string]*boardTurn{}
	h.channels = map[string]*boardChannel{}
}

func (h *channelBoardHub) loadLocked() {
	if h.path == "" {
		return
	}
	raw, err := os.ReadFile(h.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("channel board: read state failed", "path", h.path, "error", err)
		}
		return
	}
	var saved map[string]*boardChannel
	if err := json.Unmarshal(raw, &saved); err != nil {
		slog.Warn("channel board: bad state file", "path", h.path, "error", err)
		return
	}
	for id, c := range saved {
		if c != nil {
			h.channels[id] = c
		}
	}
}

func (h *channelBoardHub) saveLocked() {
	if h.path == "" {
		return
	}
	out := make(map[string]*boardChannel, len(h.channels))
	for id, c := range h.channels {
		if c.MessageID != "" || len(c.Done) > 0 {
			out[id] = c
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	// 0o644 沿用 team.go's status files.
	if err := AtomicWriteFile(h.path, b, 0o644); err != nil {
		slog.Warn("channel board: save state failed", "path", h.path, "error", err)
	}
}

func (h *channelBoardHub) channelLocked(id string) *boardChannel {
	c := h.channels[id]
	if c == nil {
		c = &boardChannel{}
		h.channels[id] = c
	}
	return c
}

// beginTurn records that engine e started a turn on interactive key key.
// Returns the turn's sequence number (0 when the board is off or the key is
// not a Discord channel).
func (h *channelBoardHub) beginTurn(e *Engine, key, desc string) uint64 {
	ch := boardChannelOf(key)
	if ch == "" {
		return 0
	}
	h.mu.Lock()
	if h.writer == nil {
		h.mu.Unlock()
		return 0
	}
	tk := e.name + "\x00" + key
	if old := h.turns[tk]; old != nil && old.held && old.desc == desc {
		old.held = false // the capacity retry of the same work: same row
		seq := old.seq
		h.mu.Unlock()
		h.touch(ch)
		return seq
	}
	h.seq++
	t := &boardTurn{engine: e, key: key, channel: ch, seq: h.seq, desc: desc, startedAt: h.now()}
	h.turns[tk] = t
	seq := t.seq
	h.mu.Unlock()
	h.touch(ch)
	return seq
}

func (h *channelBoardHub) stepTurn(e *Engine, key string, seq uint64, item ProgressCardEntry) {
	if seq == 0 {
		return
	}
	h.mu.Lock()
	t := h.turns[e.name+"\x00"+key]
	if t == nil || t.seq != seq {
		h.mu.Unlock()
		return
	}
	it := item
	t.step = &it
	ch := t.channel
	h.mu.Unlock()
	h.touch(ch)
}

// endTurn moves the turn to "今天做完的" (unless silent). Only the turn with
// this seq is ended, so a late call never ends the next turn.
func (h *channelBoardHub) endTurn(e *Engine, key string, seq uint64, outcome string) {
	if seq == 0 {
		return
	}
	h.mu.Lock()
	tk := e.name + "\x00" + key
	t := h.turns[tk]
	if t == nil || t.seq != seq {
		h.mu.Unlock()
		return
	}
	delete(h.turns, tk)
	if outcome != boardDoneSilent {
		h.addDoneLocked(t.channel, BoardDone{Bot: e.boardName(), Desc: t.desc, EndedAt: h.now(), Outcome: outcome})
		h.saveLocked()
	}
	ch := t.channel
	h.mu.Unlock()
	h.touch(ch)
}

func (h *channelBoardHub) addDoneLocked(ch string, d BoardDone) {
	c := h.channelLocked(ch)
	c.Done = append([]BoardDone{d}, c.Done...)
	if len(c.Done) > channelBoardDoneMax {
		c.Done = c.Done[:channelBoardDoneMax]
	}
}

// touchKey marks the channel of a session/interactive key changed.
func (h *channelBoardHub) touchKey(key string) {
	if ch := boardChannelOf(key); ch != "" {
		h.touch(ch)
	}
}

// touch marks a channel changed; its worker edits the board once the merge
// window is over. One worker per channel sends in order, so an older snapshot
// never lands after a newer one.
func (h *channelBoardHub) touch(ch string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writer == nil || h.stopping || ch == "" {
		return
	}
	c := h.channelLocked(ch)
	c.dirty = true
	if c.working {
		return
	}
	c.working = true
	h.workers.Add(1)
	go h.work(h.ctx, ch)
}

// channelBoardRetryMax caps the backoff between retries of a failed send.
// 来源：代码审查建议，2026-10-21（失败重试，窗口逐次翻倍，封顶 1 分钟）。
const channelBoardRetryMax = time.Minute

func (h *channelBoardHub) work(ctx context.Context, ch string) {
	defer h.workers.Done()
	stop := func() {
		h.mu.Lock()
		if c := h.channels[ch]; c != nil {
			c.working = false
		}
		h.mu.Unlock()
	}
	for {
		h.mu.Lock()
		wait := h.window
		if c := h.channels[ch]; c != nil && c.backoff > 0 {
			wait = c.backoff
		}
		h.mu.Unlock()
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				stop()
				return
			case <-t.C:
			}
		}
		h.mu.Lock()
		c := h.channels[ch]
		if c == nil || ctx.Err() != nil {
			if c != nil {
				c.working = false
			}
			h.mu.Unlock()
			return
		}
		if !c.dirty {
			c.working = false
			h.mu.Unlock()
			return
		}
		c.dirty = false
		window := h.window
		h.mu.Unlock()
		// The call is not tied to ctx: a send already under way finishes (a
		// cancelled POST could have been delivered without us learning its ID);
		// shutdown waits for it before its own last edit.
		callCtx, cancel := context.WithTimeout(context.Background(), compactProgressAPITimeout)
		ok := h.sync(callCtx, ch, nil)
		cancel()
		h.mu.Lock()
		if c := h.channels[ch]; c != nil {
			if ok {
				c.backoff = 0
			} else {
				// Retry even if nothing else changes in the channel: the merge
				// window, doubled on every failure, at most channelBoardRetryMax.
				c.dirty = true
				switch {
				case c.backoff == 0:
					c.backoff = window
				case c.backoff < channelBoardRetryMax:
					c.backoff *= 2
				}
				if c.backoff > channelBoardRetryMax {
					c.backoff = channelBoardRetryMax
				}
				if c.backoff <= 0 { // no merge window: retry on the next change
					c.dirty = false
				}
			}
		}
		h.mu.Unlock()
	}
}

// sync renders the channel's board and sends it: edit the known message,
// else find or post one (and pin it). override, when set, changes the payload
// before it is sent (shutdown drops sub-agents). It reports whether the board
// now shows this content (false = a send failed; the caller retries).
func (h *channelBoardHub) sync(ctx context.Context, ch string, override func(*ChannelBoardPayload)) bool {
	payload := h.render(ch)
	if override != nil {
		override(&payload)
	}
	content := BuildChannelBoardPayload(payload)
	h.mu.Lock()
	poster := h.poster
	c := h.channels[ch]
	if poster == nil || c == nil || content == "" || content == c.lastSent {
		h.mu.Unlock()
		return true
	}
	id, pinned, pinRetried := c.MessageID, c.Pinned, c.pinRetried
	h.mu.Unlock()

	if id == "" {
		found, foundPinned, err := poster.FindChannelBoard(ctx, ch)
		if err != nil {
			slog.Warn("channel board: look for an existing board failed", "channel", ch, "error", err)
		}
		id, pinned = found, foundPinned
	}
	if id != "" {
		err := poster.EditChannelBoard(ctx, ch, id, content)
		if errors.Is(err, ErrChannelBoardGone) {
			slog.Info("channel board: message gone, posting a new one", "channel", ch, "message", id)
			id, pinned, pinRetried = "", false, false
		} else if err != nil {
			slog.Warn("channel board: edit failed", "channel", ch, "message", id, "error", err)
			return false
		}
	}
	posted := false
	if id == "" {
		newID, err := poster.PostChannelBoard(ctx, ch, content)
		if err != nil || newID == "" {
			slog.Warn("channel board: post failed", "channel", ch, "error", err)
			return false
		}
		id, posted = newID, true
	}
	if !pinned && (posted || !pinRetried) {
		// A pin failure (no Manage Messages / Pin Messages) never stops the
		// board: it is still posted and edited, just not pinned. An unpinned
		// board found at start is tried once more per process (so after the
		// permission is given, a restart pins it).
		if err := poster.PinChannelBoard(ctx, ch, id); err != nil {
			slog.Warn("channel board: pin failed (bot needs Manage Messages / Pin Messages); board stays unpinned", "channel", ch, "message", id, "error", err)
		} else {
			pinned = true
		}
		pinRetried = true
	}
	h.mu.Lock()
	if c := h.channels[ch]; c != nil {
		changed := c.MessageID != id || c.Pinned != pinned
		c.MessageID, c.Pinned, c.pinRetried, c.lastSent = id, pinned, pinRetried, content
		if changed {
			h.saveLocked()
		}
	}
	h.mu.Unlock()
	return true
}

// render builds the channel's board from the hub (running turns, done list)
// and from the engines (queues, sub-agents, cron/timer jobs). No hub lock is
// held while engines are asked.
func (h *channelBoardHub) render(ch string) ChannelBoardPayload {
	h.mu.Lock()
	writer := h.writer
	sources := append([]*Engine(nil), h.sources...)
	var turns []*boardTurn
	for _, t := range h.turns {
		if t.channel == ch {
			cp := *t
			turns = append(turns, &cp)
		}
	}
	var done []BoardDone
	if c := h.channels[ch]; c != nil {
		done = append(done, c.Done...)
	}
	now := h.now()
	h.mu.Unlock()

	out := ChannelBoardPayload{Running: []BoardRunning{}, Queued: []BoardQueued{}, Done: []BoardDone{}}
	if writer != nil {
		out.Lang = string(writer.i18n.CurrentLang())
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].startedAt.Before(turns[j].startedAt) })
	states := map[*Engine][]boardStateInfo{}
	queuedDesc := map[string]bool{} // engine name + key + desc of queued messages
	for _, e := range sources {
		states[e] = e.boardStates(ch)
		for _, s := range states[e] {
			for _, q := range s.queued {
				queuedDesc[e.name+"\x00"+s.key+"\x00"+q] = true
			}
		}
	}
	rowOf := map[string]int{} // engine name + key → index in Running
	for _, t := range turns {
		if t.held && queuedDesc[t.engine.name+"\x00"+t.key+"\x00"+t.desc] {
			continue // re-sent after a capacity cooldown and waiting in the queue: listed there
		}
		rowOf[t.engine.name+"\x00"+t.key] = len(out.Running)
		out.Running = append(out.Running, BoardRunning{Bot: t.engine.boardName(), Desc: t.desc, Step: t.step, StartedAt: t.startedAt})
	}
	byName := map[string]*Engine{}
	for _, e := range sources {
		byName[e.name] = e
		for _, s := range states[e] {
			if len(s.subs) > 0 {
				i, ok := rowOf[e.name+"\x00"+s.key]
				if !ok { // main turn over, background sub-agents still working
					i = len(out.Running)
					rowOf[e.name+"\x00"+s.key] = i
					out.Running = append(out.Running, BoardRunning{Bot: e.boardName()})
				}
				out.Running[i].Subs = s.subs
			}
			for _, q := range s.queued {
				out.Queued = append(out.Queued, BoardQueued{Bot: e.boardName(), Kind: "message", Desc: q})
			}
		}
	}
	out.Queued = append(out.Queued, boardScheduled(ch, sources, byName)...)
	y, m, d := now.Date()
	for _, dn := range done {
		if ey, em, ed := dn.EndedAt.In(now.Location()).Date(); ey == y && em == m && ed == d {
			out.Done = append(out.Done, dn)
		}
	}
	return out
}

type boardStateInfo struct {
	key    string
	queued []string
	subs   []SubTask
}

// boardStates lists this engine's sessions in channel ch: queued messages and
// live sub-agents.
func (e *Engine) boardStates(ch string) []boardStateInfo {
	e.interactiveMu.Lock()
	type kv struct {
		key string
		st  *interactiveState
	}
	var states []kv
	for k, st := range e.interactiveStates {
		if st != nil && boardChannelOf(k) == ch {
			states = append(states, kv{k, st})
		}
	}
	e.interactiveMu.Unlock()
	sort.Slice(states, func(i, j int) bool { return states[i].key < states[j].key })
	var out []boardStateInfo
	for _, s := range states {
		info := boardStateInfo{key: s.key}
		s.st.mu.Lock()
		for _, q := range s.st.pendingMessages {
			info.queued = append(info.queued, q.boardDesc)
		}
		as := s.st.agentSession
		s.st.mu.Unlock()
		if r, ok := as.(SubTaskReporter); ok {
			_, tasks, _ := r.SubTasks()
			for _, t := range tasks {
				if t.Live() {
					info.subs = append(info.subs, t)
				}
			}
		}
		if len(info.queued) > 0 || len(info.subs) > 0 {
			out = append(out, info)
		}
	}
	return out
}

// boardScheduled lists the channel's enabled cron jobs (next run) and pending
// timers, soonest first.
func boardScheduled(ch string, sources []*Engine, byName map[string]*Engine) []BoardQueued {
	var cs *CronScheduler
	var ts *TimerScheduler
	for _, e := range sources {
		if cs == nil {
			cs = e.cronScheduler
		}
		if ts == nil {
			ts = e.timerScheduler
		}
	}
	bot := func(project string) string {
		if e := byName[project]; e != nil {
			return e.boardName()
		}
		return project
	}
	var out []BoardQueued
	if cs != nil {
		for _, j := range cs.Store().List() {
			if !j.Enabled || boardChannelOf(j.SessionKey) != ch {
				continue
			}
			out = append(out, BoardQueued{Bot: bot(j.Project), Kind: "cron", Desc: boardJobDesc(j.Description, j.Prompt, j.Exec), At: cs.NextRun(j.ID)})
		}
	}
	if ts != nil {
		for _, j := range ts.Store().ListPending() {
			if boardChannelOf(j.SessionKey) != ch {
				continue
			}
			out = append(out, BoardQueued{Bot: bot(j.Project), Kind: "timer", Desc: boardJobDesc(j.Description, j.Prompt, j.Exec), At: j.ScheduledAt})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].At, out[j].At
		if a.IsZero() != b.IsZero() {
			return !a.IsZero()
		}
		return a.Before(b)
	})
	return out
}

// boardJobDesc matches the "⏰ <desc>" notice of ExecuteCronJob/ExecuteTimerJob.
func boardJobDesc(desc, prompt, exec string) string {
	if desc != "" {
		return boardShort(desc)
	}
	if exec != "" {
		return boardShort(exec)
	}
	return boardShort(prompt)
}

// shutdown gives every channel with a board one last edit, running turns moved
// to done as stopped (the agents die with the process), then turns the hub off.
func (h *channelBoardHub) shutdown(e *Engine) {
	h.mu.Lock()
	if h.writer != e {
		h.mu.Unlock()
		return
	}
	h.stopping = true // no new workers from here (WaitGroup below)
	now := h.now()
	var chans []string
	for k, t := range h.turns {
		h.addDoneLocked(t.channel, BoardDone{Bot: t.engine.boardName(), Desc: t.desc, EndedAt: now, Outcome: BoardDoneStopped})
		delete(h.turns, k)
		chans = append(chans, t.channel)
	}
	for id, c := range h.channels {
		if c.MessageID != "" && (c.dirty || c.working) {
			chans = append(chans, id)
		}
	}
	h.saveLocked()
	if h.cancel != nil {
		h.cancel() // workers stop waiting; one already sending finishes that send
	}
	h.mu.Unlock()

	// One deadline for the whole shutdown, 沿用 the progress card's API timeout
	// (compactProgressAPITimeout), so a Discord outage delays a restart by at
	// most that, whatever the number of channels.
	ctx, cancel := context.WithTimeout(context.Background(), h.shutdownTimeout)
	defer cancel()
	// Wait for workers first: a POST still under way would otherwise race the
	// last edit below and could post a second board.
	idle := make(chan struct{})
	go func() { h.workers.Wait(); close(idle) }()
	select {
	case <-idle:
	case <-ctx.Done():
		slog.Warn("channel board: workers still sending at shutdown; skipping the last edit")
	}
	if ctx.Err() == nil {
		seen := map[string]bool{}
		var wg sync.WaitGroup
		for _, ch := range chans {
			if seen[ch] {
				continue
			}
			seen[ch] = true
			wg.Add(1)
			go func(ch string) {
				defer wg.Done()
				h.sync(ctx, ch, func(p *ChannelBoardPayload) {
					// Sub-agents die with the process too.
					kept := p.Running[:0]
					for _, r := range p.Running {
						r.Subs = nil
						if r.Desc != "" || r.Step != nil {
							kept = append(kept, r)
						}
					}
					p.Running = kept
				})
			}(ch)
		}
		wg.Wait()
	}
	h.mu.Lock()
	if h.writer == e {
		h.disableLocked()
	}
	h.mu.Unlock()
}

// boardName is the bot's name on the board: the team status name (team_name,
// e.g. 橘), else the project name.
func (e *Engine) boardName() string {
	if t := e.team; t != nil && t.name != "" {
		return t.name
	}
	return e.name
}

// holdTurn keeps the row of a turn that hit "model at capacity" (see
// boardTurn.held).
func (h *channelBoardHub) holdTurn(e *Engine, key string, seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.turns[e.name+"\x00"+key]; t != nil && t.seq == seq {
		t.held = true
	}
}

// endHeld ends a held row whose work will not be re-sent after all.
func (h *channelBoardHub) endHeld(e *Engine, key, outcome string) {
	h.mu.Lock()
	t := h.turns[e.name+"\x00"+key]
	if t == nil || !t.held {
		h.mu.Unlock()
		return
	}
	seq := t.seq
	h.mu.Unlock()
	h.endTurn(e, key, seq, outcome)
}

// boardDropped: queued messages of key were dropped (stale, or the session
// ended). A held row for one of them ends as failed — it will not be re-sent —
// and the queue on the board shrinks. Safe under state.mu (hub lock only).
func (e *Engine) boardDropped(key string, descs []string) {
	h := e.board
	if h == nil {
		return
	}
	h.mu.Lock()
	t := h.turns[e.name+"\x00"+key]
	end := uint64(0)
	if t != nil && t.held {
		for _, d := range descs {
			if d == t.desc {
				end = t.seq
				break
			}
		}
	}
	h.mu.Unlock()
	if end != 0 {
		h.endTurn(e, key, end, BoardDoneFailed)
	} else {
		h.touchKey(key)
	}
}

// boardEndHeld: see channelBoardHub.endHeld.
func (e *Engine) boardEndHeld(key, outcome string) {
	if e.board != nil {
		e.board.endHeld(e, key, outcome)
	}
}

// boardTurnRef ties one turn of processInteractiveEvents to the board.
type boardTurnRef struct {
	h    *channelBoardHub
	e    *Engine
	key  string
	seq  uint64
	held bool
}

// hold keeps the row for the capacity retry; later finish calls do nothing.
func (r *boardTurnRef) hold() {
	if r == nil {
		return
	}
	r.held = true
	r.h.holdTurn(r.e, r.key, r.seq)
}

// boardBegin starts a board row for the turn about to run on state (desc from
// state.turnDesc).
func (e *Engine) boardBegin(state *interactiveState, key string) *boardTurnRef {
	h := e.board
	if h == nil {
		return nil
	}
	state.mu.Lock()
	desc := state.turnDesc
	p := state.platform
	state.mu.Unlock()
	if p == nil || p.Name() != "discord" {
		return nil
	}
	seq := h.beginTurn(e, key, desc)
	if seq == 0 {
		return nil
	}
	state.mu.Lock()
	state.boardChannel = boardChannelOf(key)
	state.boardKey = key
	state.mu.Unlock()
	return &boardTurnRef{h: h, e: e, key: key, seq: seq}
}

func (r *boardTurnRef) step(item ProgressCardEntry) {
	if r != nil {
		r.h.stepTurn(r.e, r.key, r.seq, item)
	}
}

// finish ends the row; later calls for the same turn do nothing.
func (r *boardTurnRef) finish(state *interactiveState, outcome string) {
	if r == nil || r.held {
		return
	}
	if state != nil && state.isStopped() && outcome != boardDoneSilent {
		outcome = BoardDoneStopped
	}
	r.h.endTurn(r.e, r.key, r.seq, outcome)
}

// boardTouchState marks the state's channel changed (queue, sub-agents).
func (e *Engine) boardTouchState(state *interactiveState) {
	if e.board == nil || state == nil {
		return
	}
	state.mu.Lock()
	ch := state.boardChannel
	state.mu.Unlock()
	if ch != "" {
		e.board.touch(ch)
	}
}

// boardTouchJob refreshes the board of a cron/timer job's channel.
func boardTouchJob(e *Engine, sessionKey string) {
	if e != nil && e.board != nil {
		e.board.touchKey(sessionKey)
	}
}
