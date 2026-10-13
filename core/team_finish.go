package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

var errProjectFinishChanged = errors.New("project changed since finish preview")
var errProjectEnded = errors.New("project has ended")

// Bind confirmation to the project and the work the user actually reviewed.
// Streaming output alone does not invalidate a preview; task transitions do.
func projectFinishFingerprint(p TeamProject) string {
	parts := []string{p.ID, p.State, fmt.Sprint(p.WorkflowEpoch), fmt.Sprint(p.Paused), fmt.Sprint(projectRound(p)), p.RoundState}
	for _, t := range sortedTasks(p) {
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%d", t.ID, t.Attempt, t.Status, len(t.Followups)))
	}
	var reports []string
	for bot, r := range p.Reports {
		reports = append(reports, bot+":"+r.ID+":"+r.Status)
	}
	sort.Strings(reports)
	parts = append(parts, reports...)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))[:16]
}

func projectFinishCounts(p TeamProject) (accepted, active, planned, unresolved int) {
	for _, t := range currentRoundProject(p).Tasks {
		switch {
		case taskActive(t.Status) || len(t.Followups) > 0:
			active++
		case t.Status == "planned":
			planned++
		case t.Status == "accepted":
			accepted++
		case t.Status != "cancelled":
			unresolved++
		}
	}
	return
}

// Fence work before closing processes. "ending" remains durable until every
// member's teardown has completed; the group itself remains ready.
func (s *TeamProjectStore) finishProject(id, actor, fingerprint string) (TeamProject, error) {
	var ended TeamProject
	err := s.mutateProject(id, func(p *TeamProject) error {
		if p.State == "ended" {
			return errProjectEnded
		}
		if p.RoundState == "idle" {
			ended = *p
			return nil
		}
		if p.State != "ready" || actor == "" {
			return fmt.Errorf("only a ready project round can be ended by a user")
		}
		if projectFinishFingerprint(*p) != fingerprint {
			return errProjectFinishChanged
		}
		if p.RoundState == "ending" {
			ended = *p
			return nil
		}
		p.Round = projectRound(*p)
		p.RoundState, p.Paused = "ending", false
		p.RoundEndedBy = actor
		p.WorkflowEpoch++
		for id, task := range p.Tasks {
			if taskRound(task) != p.Round {
				continue
			}
			if task.Status == "planned" {
				task.Status = "cancelled"
			} else if taskActive(task.Status) || len(task.Followups) > 0 {
				cancelTaskFollowups(&task)
				task.Status = "stopped"
			} else {
				continue
			}
			task.UpdatedAt = time.Now().UTC()
			p.Tasks[id] = task
		}
		for bot, report := range p.Reports {
			if taskActive(report.Status) {
				report.Status, report.UpdatedAt = "stopped", time.Now().UTC()
				p.Reports[bot] = report
			}
		}
		ended = *p
		return nil
	})
	return ended, err
}
func (s *TeamProjectStore) completeProjectRound(id string, round uint64) (TeamProject, error) {
	var out TeamProject
	err := s.mutateProject(id, func(p *TeamProject) error {
		if projectRound(*p) != round || p.RoundState != "ending" {
			return errProjectFinishChanged
		}
		p.RoundEndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		snapshot := TeamRound{Number: round, Goal: roundGoal(*p), StartedAt: p.RoundStartedAt, EndedAt: p.RoundEndedAt, EndedBy: p.RoundEndedBy, Tasks: currentRoundProject(*p).Tasks, Reports: p.Reports}
		p.Rounds = append(p.Rounds, snapshot)
		p.RoundState = "idle"
		out = *p
		return nil
	})
	return out, err
}

// Retain failed close handles for an explicit retry. Session resume identities
// in SessionManager are never reset by ending a round.
type projectCloseTarget struct {
	engine  *Engine
	key     string
	session AgentSession
}

func (e *Engine) captureProjectCloses(p TeamProject) []projectCloseTarget {
	var targets []projectCloseTarget
	for _, bot := range e.teamProjects.projectEngines(p) {
		seen := map[string]bool{}
		bot.interactiveMu.Lock()
		for key, st := range bot.interactiveStates {
			st.mu.Lock()
			source, agent := st.sourceSessionKey, st.agentSession
			st.mu.Unlock()
			if projectSessionKey(key, p) || projectSessionKey(source, p) {
				targets = append(targets, projectCloseTarget{bot, key, agent})
				seen[key] = true
			}
		}
		bot.interactiveMu.Unlock()
		bot.closingMu.Lock()
		for key := range bot.closingSessions {
			if !seen[key] && projectSessionKey(key, p) {
				targets = append(targets, projectCloseTarget{engine: bot, key: key})
				seen[key] = true
			}
		}
		for key, unsafe := range bot.unsafeResume {
			if unsafe && !seen[key] && projectSessionKey(key, p) {
				targets = append(targets, projectCloseTarget{engine: bot, key: key})
			}
		}
		bot.closingMu.Unlock()
	}
	return targets
}
func (e *Engine) waitProjectCloses(ctx context.Context, p TeamProject, targets []projectCloseTarget) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(closeTimeout + 30*time.Second)
	defer deadline.Stop()
	for {
		pending := false
		for _, target := range targets {
			target.engine.closingMu.Lock()
			closing := target.engine.closingSessions[target.key] != nil
			unsafe := target.engine.unsafeResume[target.key]
			target.engine.closingMu.Unlock()
			if closing {
				pending = true
				continue
			}
			if unsafe || (target.session != nil && target.session.Alive()) {
				return fmt.Errorf("%s", target.engine.name)
			}
		}
		if !pending && !e.projectBusy(p) {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("member execution did not exit")
		}
	}
}

func (e *Engine) projectFinishCard(p TeamProject) *Card {
	a, running, planned, unresolved := projectFinishCounts(p)
	command := "/project finish confirm " + projectFinishFingerprint(p)
	return reusableCard(NewCard().Title(e.i18n.T(MsgProjectFinish), "orange").
		Markdown(e.i18n.Tf(MsgRoundCurrent, projectRound(p), truncateIf(roundGoal(p), 1000))).
		Markdown(e.i18n.Tf(MsgProjectFinishPreview, p.Name, a, len(currentRoundProject(p).Tasks), running, planned, unresolved)).
		Markdown(e.i18n.T(MsgProjectFinishConsequences)).
		Buttons(DangerBtn(e.i18n.T(MsgProjectFinishConfirm), "cmd:"+command), DefaultBtn(e.i18n.T(MsgProjectFinishCancel), "cmd:/project dashboard")).
		Note(e.i18n.Tf(MsgProjectFinishTextCommand, command)).Build())
}

func (e *Engine) handleProjectFinish(platform Platform, msg *Message, p TeamProject, args []string) error {
	if p.State == "ended" || p.RoundState == "idle" {
		e.sendProjectPanel(platform, msg, e.dashboardCard(p, 0))
		return nil
	}
	if p.State != "ready" {
		return fmt.Errorf("%s", e.i18n.T(MsgTeamProjectPending))
	}
	if len(args) != 3 || args[1] != "confirm" {
		e.sendProjectPanel(platform, msg, e.projectFinishCard(p))
		return nil
	}
	guard := e.teamProjects.projectGuard(p.ID)
	guard.Lock()
	ended, err := e.teamProjects.finishProject(p.ID, msg.UserID, args[2])
	if err != nil {
		guard.Unlock()
		if errors.Is(err, errProjectFinishChanged) {
			current, lookupErr := e.teamProject(msg.SessionKey)
			if lookupErr != nil || current == nil {
				return err
			}
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectFinishChanged))
			e.sendProjectPanel(platform, msg, e.projectFinishCard(*current))
			return nil
		}
		return err
	}
	if ended.RoundState != "ending" {
		guard.Unlock()
		e.sendProjectPanel(platform, msg, e.dashboardCard(ended, 0))
		return nil
	}
	store := e.teamProjects
	store.runtimeMu.Lock()
	if store.roundStopping == nil {
		store.roundStopping = map[string]bool{}
	}
	if store.roundStopping[p.ID] {
		store.runtimeMu.Unlock()
		guard.Unlock()
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgRoundEnding))
		return nil
	}
	store.roundStopping[p.ID] = true
	targets := store.roundStops[p.ID]
	store.runtimeMu.Unlock()
	if len(targets) == 0 {
		targets = e.captureProjectCloses(ended)
	} else {
		for _, t := range targets {
			if t.session != nil {
				t.engine.closingMu.Lock()
				delete(t.engine.unsafeResume, t.key)
				t.engine.closingMu.Unlock()
				t.engine.closeAgentSessionAsync(t.key, t.session, nil, nil)
			}
		}
	}
	store.runtimeMu.Lock()
	if store.roundStops == nil {
		store.roundStops = map[string][]projectCloseTarget{}
	}
	store.roundStops[p.ID] = targets
	store.runtimeMu.Unlock()
	e.stopProject(ended)
	guard.Unlock()
	e.sendProjectPanel(platform, msg, e.dashboardCard(ended, 0))
	go func() {
		defer func() { store.runtimeMu.Lock(); delete(store.roundStopping, p.ID); store.runtimeMu.Unlock() }()
		if err := e.waitProjectCloses(e.ctx, ended, targets); err != nil {
			slog.Warn("round close incomplete", "project", p.ID, "error", err)
			c := NewCard().Title(e.i18n.T(MsgRoundEnding), "orange").Markdown(e.i18n.Tf(MsgRoundStopFailed, err)).Buttons(DefaultBtn(e.i18n.T(MsgRoundRetry), "cmd:/project finish")).Build()
			e.sendProjectPanel(platform, msg, c)
			return
		}
		guard.Lock()
		finished, err := store.completeProjectRound(p.ID, projectRound(ended))
		guard.Unlock()
		if err != nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
		store.runtimeMu.Lock()
		delete(store.roundStops, p.ID)
		store.runtimeMu.Unlock()
		e.sendProjectPanel(platform, msg, e.dashboardCard(finished, 0))
	}()
	return nil
}
