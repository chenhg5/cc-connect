package core

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Goal is the agent's persisted objective and its native usage accounting.
type Goal struct {
	ThreadID        string `json:"threadId"`
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokenBudget     *int64 `json:"tokenBudget"`
	TokensUsed      int64  `json:"tokensUsed"`
	TimeUsedSeconds int64  `json:"timeUsedSeconds"`
}

// GoalUpdate changes only explicitly supplied fields. A nil budget leaves it
// unchanged; ClearTokenBudget explicitly removes the budget.
type GoalUpdate struct {
	Objective        *string
	Status           string
	TokenBudget      *int64
	ClearTokenBudget bool
}

// GoalSupporter lets the engine check the configured backend before spawning.
type GoalSupporter interface {
	SupportsGoals() bool
}

// GoalManager manages native goals without interpreting ordinary model text.
// GoalActive must be nonblocking; an active goal is not an idle session.
type GoalManager interface {
	GetGoal() (*Goal, error)
	SetGoal(GoalUpdate) (*Goal, error)
	ClearGoal() error
	GoalActive() bool
}

type goalCommand struct {
	action      string
	objective   string
	budget      *int64
	clearBudget bool
}

func parseGoalCommand(args []string) (goalCommand, error) {
	c := goalCommand{action: "show"}
	if len(args) == 0 {
		return c, nil
	}
	c.action = strings.ToLower(args[0])
	switch c.action {
	case "show", "pause", "resume", "clear":
		if len(args) != 1 {
			return c, fmt.Errorf("unexpected goal arguments")
		}
		return c, nil
	case "budget":
		if len(args) != 2 {
			return c, fmt.Errorf("budget requires one value")
		}
		if strings.EqualFold(args[1], "none") {
			c.clearBudget = true
			return c, nil
		}
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("budget must be positive")
		}
		c.budget = &n
		return c, nil
	case "set", "edit":
		args = args[1:]
	default:
		c.action = "set"
	}
	var text []string
	for i := 0; i < len(args); i++ {
		if args[i] != "--budget" {
			text = append(text, args[i])
			continue
		}
		if c.budget != nil || i+1 == len(args) {
			return c, fmt.Errorf("invalid goal budget")
		}
		i++
		n, err := strconv.ParseInt(args[i], 10, 64)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("budget must be positive")
		}
		c.budget = &n
	}
	c.objective = strings.TrimSpace(strings.Join(text, " "))
	if c.objective == "" || utf8.RuneCountInString(c.objective) > 4000 {
		return c, fmt.Errorf("goal objective must contain 1 to 4000 characters")
	}
	return c, nil
}

func (e *Engine) cmdGoal(p Platform, msg *Message, args []string) {
	c, err := parseGoalCommand(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGoalUsage))
		return
	}
	agent, sessions, key, workspace, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	if a, ok := agent.(GoalSupporter); !ok || !a.SupportsGoals() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGoalUnsupported))
		return
	}
	session := sessions.GetOrCreateActive(msg.SessionKey)
	session.mu.Lock()
	stopGeneration := session.goalStopGeneration
	session.mu.Unlock()
	gen, locked := session.TryLock()
	var state *interactiveState
	if locked {
		defer func() {
			if state == nil {
				session.Unlock(gen)
				return
			}
			defer e.scheduleAgentSessionIdleClose(key, state)
			// Match the normal turn's atomic queue-empty/unlock handoff.
			state.mu.Lock()
			session.mu.Lock()
			ownsLock := session.busy && session.lockGen == gen
			session.mu.Unlock()
			if !ownsLock {
				state.mu.Unlock()
				return
			}
			if len(state.pendingMessages) == 0 {
				session.Unlock(gen)
				state.mu.Unlock()
				return
			}
			state.mu.Unlock()
			e.drainOrphanedQueue(session, sessions, key, agent, workspace, gen)
		}()
		state = e.getOrCreateInteractiveStateWithResumePolicy(key, p, msg.ReplyCtx, session, sessions, agent, msg.SessionKey, false)
	} else {
		e.interactiveMu.Lock()
		state = e.interactiveStates[key]
		e.interactiveMu.Unlock()
	}
	if state == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPreviousProcessing))
		return
	}
	state.goalMu.Lock()
	defer state.goalMu.Unlock()
	session.mu.Lock()
	cancelled := session.goalStopGeneration != stopGeneration
	session.mu.Unlock()
	if cancelled {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgExecutionStopped))
		return
	}
	state.mu.Lock()
	live := state.agentSession
	state.mu.Unlock()
	if live == nil || !live.Alive() {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgFailedToStartAgentSession))
		return
	}
	m, ok := live.(GoalManager)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGoalUnsupported))
		return
	}
	state.mu.Lock()
	state.goalSessionStore = sessions
	state.mu.Unlock()
	e.cancelAgentSessionIdleClose(state)
	if locked {
		state.mu.Lock()
		state.eventsNeedResync = false
		state.mu.Unlock()
		e.startUnsolicitedReader(state, session, sessions, key, workspace)
	}
	goal, err := applyGoalCommand(m, c)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgGoalError, err))
		return
	}
	session.SetResumeGoal(goal != nil && goal.Status == "active")
	sessions.Save()
	if c.action == "clear" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgGoalCleared))
		return
	}
	e.reply(p, msg.ReplyCtx, e.renderGoal(goal))
}

func (e *Engine) relayGoalEvent(state *interactiveState, session *Session, sessions *SessionManager, event Event) {
	session.SetResumeGoal(event.Goal != nil && event.Goal.Status == "active")
	sessions.Save()
	state.mu.Lock()
	state.goalSessionStore = sessions
	p, replyCtx := state.platform, state.replyCtx
	state.mu.Unlock()
	e.send(p, replyCtx, e.renderGoal(event.Goal))
}

// Restore only the selected conversation of each chat. Loading the native
// thread reconciles stale intent with completion, pause and budget state.
func (e *Engine) restoreNativeGoals(p Platform) {
	rc, ok := p.(ReplyContextReconstructor)
	if !ok {
		return
	}
	stores := []*SessionManager{e.sessions}
	if e.multiWorkspace && e.workspaceBindings != nil {
		seen := map[string]bool{}
		for _, project := range []string{"project:" + e.name, sharedWorkspaceBindingsKey} {
			for _, binding := range e.workspaceBindings.ListByProject(project) {
				if seen[binding.Workspace] {
					continue
				}
				seen[binding.Workspace] = true
				_, store, err := e.getOrCreateWorkspaceAgent(binding.Workspace)
				if err != nil {
					slog.Warn("goal workspace recovery failed", "error", err)
					continue
				}
				stores = append(stores, store)
			}
		}
	}
	for _, store := range stores {
		keys, active := store.SessionKeyMap()
		for id, key := range keys {
			if !active[id] || !strings.HasPrefix(key, p.Name()+":") {
				continue
			}
			session := store.FindByID(id)
			if session == nil || !session.GetResumeGoal() || session.GetAgentSessionID() == "" {
				continue
			}
			ctx, err := rc.ReconstructReplyCtx(key)
			if err != nil {
				slog.Warn("goal reply context recovery failed", "session_key", key, "error", err)
				continue
			}
			e.cmdGoal(p, &Message{SessionKey: key, Platform: p.Name(), ChannelKey: extractWorkspaceChannelKey(key), ReplyCtx: ctx}, nil)
		}
	}
}

func applyGoalCommand(m GoalManager, c goalCommand) (*Goal, error) {
	if c.action == "clear" {
		return nil, m.ClearGoal()
	}
	if c.action == "set" {
		return m.SetGoal(GoalUpdate{Objective: &c.objective, Status: "active", TokenBudget: c.budget})
	}
	g, err := m.GetGoal()
	if err != nil || c.action == "show" || g == nil {
		return g, err
	}
	u := GoalUpdate{TokenBudget: c.budget, ClearTokenBudget: c.clearBudget}
	switch c.action {
	case "edit":
		u.Objective = &c.objective
		u.Status = g.Status
		if u.TokenBudget == nil {
			u.TokenBudget = g.TokenBudget
		}
	case "pause":
		u.Status = "paused"
	case "resume":
		u.Status = "active"
	}
	return m.SetGoal(u)
}

func (e *Engine) renderGoal(g *Goal) string {
	if g == nil {
		return e.i18n.T(MsgGoalNone)
	}
	budget := e.i18n.T(MsgGoalUnlimited)
	if g.TokenBudget != nil {
		budget = strconv.FormatInt(*g.TokenBudget, 10)
	}
	return e.i18n.Tf(MsgGoalStatus, g.Status, g.Objective, g.TokensUsed, budget, g.TimeUsedSeconds)
}

func activeSessionGoal(s AgentSession) bool {
	m, ok := s.(GoalManager)
	return ok && m.GoalActive()
}
