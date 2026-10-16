package core

// maomao fork: team status board.
//
// Several projects (one bot each) form a team: a main bot that decides who speaks and sub bots
// it @-s. Each project with team_status_dir set writes <dir>/<project>.json every few seconds:
// which of its sessions (one per Discord channel / DM) are busy, since when, on what task, and
// what finished recently. A project with team_status_inject reads the other projects' files and
// prepends a one-line-per-teammate summary to each turn, so the main bot knows who is busy where
// (across channels) before it hands out work.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	teamPollInterval  = 2 * time.Second
	teamHeartbeat     = 20 * time.Second
	teamStaleAfter    = time.Minute      // a file not rewritten this long = its process is gone
	teamRecentDone    = 10 * time.Minute // finished tasks shown this long
	teamTaskMaxRunes  = 80
	teamStatusFileExt = ".json"
)

type teamState struct {
	dir    string
	name   string // display name, e.g. 龙娘
	inject bool
	mu     sync.Mutex
	seen   map[string]*teamSession // sessionKey → state
}

type teamSession struct {
	session *Session
	chat    string
	task    string
	busy    bool
	since   time.Time
	doneAt  time.Time
}

type teamEntry struct {
	Key    string    `json:"key"`
	Chat   string    `json:"chat,omitempty"`
	Task   string    `json:"task,omitempty"`
	Busy   bool      `json:"busy"`
	Since  time.Time `json:"since,omitzero"`
	DoneAt time.Time `json:"done_at,omitzero"`
}

type teamFile struct {
	Project  string      `json:"project"`
	Name     string      `json:"name"`
	Updated  time.Time   `json:"updated"`
	Sessions []teamEntry `json:"sessions"`
	// CoolingUntil (maomao fork): the bot's model is cooling down after "at
	// capacity" until then; schedulers (outing-tick) hold off.
	CoolingUntil time.Time `json:"cooling_until,omitzero"`
}

// SetTeam enables the status board for this project. dir == "" disables it.
func (e *Engine) SetTeam(dir, name string, inject bool) {
	if dir == "" {
		e.team = nil
		return
	}
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	if name == "" {
		name = e.name
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("team: cannot create status dir", "dir", dir, "error", err)
		return
	}
	old := e.team
	t := &teamState{dir: dir, name: name, inject: inject, seen: map[string]*teamSession{}}
	if old != nil { // config reload: keep what we know
		old.mu.Lock()
		t.seen = old.seen
		old.mu.Unlock()
	}
	e.team = t
	if old == nil {
		go e.runTeamWriter()
	}
	slog.Info("team status enabled", "dir", dir, "name", name, "inject", inject)
}

// teamNoteTurn records that msg starts (or is folded into) a turn of session.
func (e *Engine) teamNoteTurn(msg *Message, session *Session) {
	t := e.team
	if t == nil {
		return
	}
	task := strings.Join(strings.Fields(msg.Content), " ")
	if r := []rune(task); len(r) > teamTaskMaxRunes {
		task = string(r[:teamTaskMaxRunes]) + "…"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ts := t.seen[msg.SessionKey]
	if ts == nil {
		ts = &teamSession{}
		t.seen[msg.SessionKey] = ts
	}
	ts.session = session
	ts.chat = msg.ChatName
	if task != "" {
		ts.task = task
	}
}

func (e *Engine) runTeamWriter() {
	tick := time.NewTicker(teamPollInterval)
	defer tick.Stop()
	var last []byte
	var lastWrite time.Time
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-tick.C:
		}
		t := e.team
		if t == nil {
			continue
		}
		now := time.Now()
		f := teamFile{Project: e.name, Name: t.name}
		t.mu.Lock()
		for key, ts := range t.seen {
			busy := ts.session != nil && ts.session.Busy()
			if busy && !ts.busy {
				ts.since = now
			}
			if !busy && ts.busy {
				ts.doneAt = now
			}
			ts.busy = busy
			if !busy && (ts.doneAt.IsZero() || now.Sub(ts.doneAt) > teamRecentDone) {
				continue
			}
			en := teamEntry{Key: key, Chat: ts.chat, Task: ts.task, Busy: busy}
			if busy {
				en.Since = ts.since
			} else {
				en.DoneAt = ts.doneAt
			}
			f.Sessions = append(f.Sessions, en)
		}
		t.mu.Unlock()
		sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].Key < f.Sessions[j].Key })
		f.CoolingUntil = e.capacityCoolingUntil().Truncate(time.Second) // whole seconds: outing-tick parses it with Python fromisoformat
		body, _ := json.Marshal(struct {
			S []teamEntry
			C time.Time
		}{f.Sessions, f.CoolingUntil})
		if string(body) == string(last) && now.Sub(lastWrite) < teamHeartbeat {
			continue
		}
		f.Updated = now
		out, _ := json.MarshalIndent(f, "", "  ")
		path := filepath.Join(t.dir, e.name+teamStatusFileExt)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o644); err == nil {
			err = os.Rename(tmp, path)
			if err != nil {
				slog.Warn("team: write status failed", "error", err)
			}
		}
		last, lastWrite = body, now
	}
}

// chatOf reduces a session key "<platform>:<chat>[:<user>]" to "<platform>:<chat>", so a
// per-user session and a shared-in-channel session of the same chat compare equal.
func chatOf(key string) string {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1]
	}
	return key
}

// teamSummary describes what the other team members are doing, for a turn in sessionKey.
// Empty when nobody is busy or recently done.
func (e *Engine) teamSummary(sessionKey string) (string, string) {
	t := e.team
	if t == nil || !t.inject {
		return "", ""
	}
	files, _ := filepath.Glob(filepath.Join(t.dir, "*"+teamStatusFileExt))
	here := chatOf(sessionKey)
	now := time.Now()
	var lines, keys []string
	for _, path := range files {
		if filepath.Base(path) == e.name+teamStatusFileExt {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f teamFile
		if json.Unmarshal(raw, &f) != nil || now.Sub(f.Updated) > teamStaleAfter {
			continue
		}
		for _, s := range f.Sessions {
			where := "#" + s.Chat
			if chatOf(s.Key) == here {
				where = "本频道"
			} else if s.Chat == "" {
				where = "私聊/别处"
			}
			task := ""
			if s.Task != "" {
				task = "「" + s.Task + "」"
			}
			if s.Busy {
				lines = append(lines, fmt.Sprintf("%s：在%s忙了 %s %s", f.Name, where, roundDur(now.Sub(s.Since)), task))
			} else {
				lines = append(lines, fmt.Sprintf("%s：%s前在%s做完 %s", f.Name, roundDur(now.Sub(s.DoneAt)), where, task))
			}
			keys = append(keys, fmt.Sprintf("%s|%s|%t|%s", f.Name, where, s.Busy, s.Task))
		}
	}
	if len(lines) == 0 {
		return "", ""
	}
	sort.Strings(lines)
	sort.Strings(keys)
	// The key leaves out elapsed times so an unchanged board is not resent.
	return "（队友状态，系统自动附上，不用回应）\n" + strings.Join(lines, "\n"), strings.Join(keys, "\n")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
}

// isTeamLead reports whether this project is the team lead (the one that reads
// the others' status; maomao fork).
func (e *Engine) isTeamLead() bool {
	return e.team != nil && e.team.inject
}
