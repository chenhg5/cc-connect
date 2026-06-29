package core

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	featureChefSeat    = "chef-seat"
	featureImplSeat    = "dev-deepseek"
	featureCounselSeat = "counsel-seat"
	featureReviewSeat  = "reviewer-seat"
)

type featureStartOptions struct {
	Title  string
	Impl   bool
	Risk   bool
	Review bool
}

func parseFeatureStartArgs(args []string) (featureStartOptions, error) {
	var opts featureStartOptions
	titleParts := make([]string, 0, len(args))
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
			continue
		case "--impl":
			opts.Impl = true
		case "--risk":
			opts.Risk = true
		case "--review":
			opts.Review = true
		default:
			if strings.HasPrefix(arg, "--") {
				return opts, fmt.Errorf("unknown flag %s", arg)
			}
			titleParts = append(titleParts, arg)
		}
	}
	opts.Title = strings.TrimSpace(strings.Join(titleParts, " "))
	if opts.Title == "" {
		return opts, fmt.Errorf("feature title is required")
	}
	return opts, nil
}

func (e *Engine) cmdFeatureStart(p Platform, msg *Message, args []string) {
	opts, err := parseFeatureStartArgs(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, "Usage: `/feature-start <title> [--impl] [--risk] [--review]`")
		return
	}

	chef := e.featureChefEngine()
	if chef == nil {
		e.reply(p, msg.ReplyCtx, "❌ /feature-start requires a running chef-seat engine")
		return
	}
	chefPlatform := chef.platformByName(msg.Platform)
	if chefPlatform == nil {
		if chef == e {
			chefPlatform = p
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ chef-seat has no %s platform available", msg.Platform))
			return
		}
	}
	if strings.TrimSpace(chef.dataDir) == "" {
		e.reply(p, msg.ReplyCtx, "❌ /feature-start requires data_dir so it can write the local board")
		return
	}

	boardStore := NewFeatureBoardStore(chef.dataDir)
	repoWorktree := chef.commandWorkDir(chef.agent, msg)
	seatNames := chef.featureSeatNames()
	task, err := boardStore.Create(
		opts.Title,
		featureChefSeat,
		repoWorktree,
		"Chef scope feature and decide whether counsel/dev/reviewer seats are needed.",
		msg.SessionKey,
		seatNames,
	)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ feature board write failed: %v", err))
		return
	}

	refreshed := []string{}
	if _, err := chef.refreshFeatureSeatSession(msg, task); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef refresh failed: %v", err))
		return
	}
	if err := boardStore.MarkSeatRefreshed(task.TaskID, featureChefSeat, msg.SessionKey); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ feature board update failed: %v", err))
		return
	}
	refreshed = append(refreshed, featureChefSeat)

	packet := chef.buildFeatureStartPacket(task, boardStore.Path(), opts, refreshed, pendingFeatureSeats(seatNames, featureChefSeat))
	if err := chef.injectFeatureStartPacket(chefPlatform, msg, packet); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef cold-start packet failed: %v", err))
		return
	}

	if opts.Risk {
		chef.launchFeatureCounselAudit(msg, task, boardStore.Path())
	}

	reply := fmt.Sprintf("✅ Feature started: %s\nTask: `%s`\nBoard: `%s`\nRefreshed: %s",
		task.Title, task.TaskID, boardStore.Path(), strings.Join(refreshed, ", "))
	reply += "\nLazy refresh pending: " + strings.Join(pendingFeatureSeats(seatNames, featureChefSeat), ", ")
	if opts.Risk {
		reply += "\nRisk flag: counsel-seat audit requested through relay."
	}
	e.reply(p, msg.ReplyCtx, reply)
}

func (e *Engine) featureChefEngine() *Engine {
	if e.name == featureChefSeat {
		return e
	}
	return e.featureEngineByName(featureChefSeat)
}

func (e *Engine) featureEngineByName(name string) *Engine {
	if e != nil && e.name == name {
		return e
	}
	if e == nil || e.relayManager == nil {
		return nil
	}
	return e.relayManager.Engine(name)
}

func (e *Engine) featureSeatNames() []string {
	seen := map[string]bool{}
	if e != nil && strings.TrimSpace(e.name) != "" {
		seen[e.name] = true
	}
	if e != nil && e.relayManager != nil {
		for _, name := range e.relayManager.ListEngineNames() {
			if strings.TrimSpace(name) != "" {
				seen[name] = true
			}
		}
	}
	if !seen[featureChefSeat] {
		seen[featureChefSeat] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func pendingFeatureSeats(seatNames []string, refreshedSeat string) []string {
	var pending []string
	for _, name := range seatNames {
		if name == refreshedSeat || strings.TrimSpace(name) == "" {
			continue
		}
		pending = append(pending, name)
	}
	sort.Strings(pending)
	return pending
}

func (e *Engine) platformByName(name string) Platform {
	for _, p := range e.platforms {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func (e *Engine) refreshFeatureSeatSession(msg *Message, task *FeatureTask) (*Session, error) {
	_, sessions := e.sessionContextForKey(msg.SessionKey)
	return e.refreshFeatureSeatSessionForKey(sessions, msg.SessionKey, e.interactiveKeyForSessionKey(msg.SessionKey), task)
}

func (e *Engine) refreshFeatureSeatSessionForKey(sessions *SessionManager, sessionKey, interactiveKey string, task *FeatureTask) (*Session, error) {
	if e == nil {
		return nil, fmt.Errorf("engine is nil")
	}
	if sessions == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	e.cleanupInteractiveState(interactiveKey)

	old := sessions.GetOrCreateActive(sessionKey)
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	sessions.Save()

	name := fmt.Sprintf("feature-start %s %s", task.TaskID, task.Title)
	return sessions.NewSession(sessionKey, name), nil
}

func (e *Engine) injectFeatureStartPacket(p Platform, msg *Message, packet string) error {
	agent, sessions, interactiveKey, workspaceDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		return err
	}
	session := sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		return fmt.Errorf("chef session is still processing")
	}
	packetMsg := *msg
	packetMsg.Content = packet
	packetMsg.Images = nil
	packetMsg.Files = nil
	go e.processInteractiveMessageWith(p, &packetMsg, session, agent, sessions, interactiveKey, workspaceDir, msg.SessionKey)
	return nil
}

func (e *Engine) buildFeatureStartPacket(task *FeatureTask, boardPath string, opts featureStartOptions, refreshed, pending []string) string {
	nexusRoot := e.featureNexusRoot()
	wakePath := filepath.Join(nexusRoot, "WAKE.md")
	handoffPath := filepath.Join(nexusRoot, "HANDOFF.md")
	var b strings.Builder
	b.WriteString("[FEATURE-START]\n")
	b.WriteString("This is a clean cold-start packet created by cc-connect /feature-start.\n\n")
	b.WriteString("User request/title:\n")
	b.WriteString(task.Title)
	b.WriteString("\n\nBoard item:\n")
	b.WriteString(fmt.Sprintf("- task_id: %s\n", task.TaskID))
	b.WriteString(fmt.Sprintf("- title: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("- owner: %s\n", task.Owner))
	b.WriteString(fmt.Sprintf("- status: %s\n", task.Status))
	b.WriteString(fmt.Sprintf("- repo/worktree: %s\n", task.RepoWorktree))
	b.WriteString(fmt.Sprintf("- blocker: %s\n", task.Blocker))
	b.WriteString(fmt.Sprintf("- last_heartbeat: %s\n", task.LastHeartbeat.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- evidence: %v\n", task.Evidence))
	b.WriteString(fmt.Sprintf("- handback_state: %s\n", task.HandbackState))
	b.WriteString(fmt.Sprintf("- next_action: %s\n", task.NextAction))
	b.WriteString(fmt.Sprintf("- board_path: %s\n\n", boardPath))
	b.WriteString("Context files:\n")
	b.WriteString(fmt.Sprintf("- WAKE: %s\n", wakePath))
	b.WriteString(fmt.Sprintf("- HANDOFF: %s\n\n", handoffPath))
	b.WriteString("Feature context loading:\n")
	b.WriteString("- Chef is refreshed immediately because Chef is the entry point.\n")
	b.WriteString("- Other seats are marked stale-for-this-feature and will be refreshed lazily on first actual use, mention, or relay.\n")
	b.WriteString("- Lazy refresh must attach this feature context to the first real task; it must not create a standalone task for unused seats.\n")
	b.WriteString(fmt.Sprintf("- --impl: %t\n", opts.Impl))
	b.WriteString(fmt.Sprintf("- --risk: %t\n", opts.Risk))
	b.WriteString(fmt.Sprintf("- --review: %t\n", opts.Review))
	b.WriteString(fmt.Sprintf("- refreshed seats: %s\n", strings.Join(refreshed, ", ")))
	if len(pending) > 0 {
		b.WriteString(fmt.Sprintf("- lazy-refresh pending seats: %s\n", strings.Join(pending, ", ")))
	}
	b.WriteString("\nInstructions:\n")
	b.WriteString("- Scope the feature before implementation.\n")
	b.WriteString("- Decide whether counsel-seat, dev-deepseek, reviewer-seat, or another seat is needed.\n")
	b.WriteString("- Mark pricing, API capability, product, architecture, security, and other uncertain assumptions as verified or speculative.\n")
	b.WriteString("- Speculative price/API capability claims require spike evidence or counsel/reviewer audit before implementation.\n")
	b.WriteString("- Do not say \"I'll monitor\" unless there is a real board item, watcher, heartbeat check, scheduled follow-up, or other durable follow-through mechanism.\n")
	if opts.Risk {
		b.WriteString("- Risk flag is set: wait for or request counsel-seat adversarial audit before pushing implementation forward.\n")
	}
	if opts.Impl {
		b.WriteString("- Impl flag is set: dev-deepseek should be considered when implementation starts; it will refresh lazily on first actual use.\n")
	}
	if opts.Review {
		b.WriteString("- Review flag is set: reviewer-seat should be considered when review starts; it will refresh lazily on first actual use.\n")
	}
	return b.String()
}

func (e *Engine) featureNexusRoot() string {
	dataDir := strings.TrimSpace(e.dataDir)
	if dataDir != "" {
		return filepath.Dir(dataDir)
	}
	configPath := strings.TrimSpace(e.configPath)
	if configPath != "" {
		return filepath.Dir(configPath)
	}
	return ""
}

func (e *Engine) applyLazyFeatureContextToMessage(msg *Message, sessions *SessionManager, interactiveKey string) (*Session, bool, error) {
	if e == nil || msg == nil || sessions == nil || strings.TrimSpace(e.dataDir) == "" {
		return nil, false, nil
	}
	store := NewFeatureBoardStore(e.dataDir)
	task, shouldRefresh, err := store.ActiveTaskForSeat(e.name)
	if err != nil || !shouldRefresh {
		return nil, shouldRefresh, err
	}
	session, err := e.refreshFeatureSeatSessionForKey(sessions, msg.SessionKey, interactiveKey, task)
	if err != nil {
		return nil, true, err
	}
	if err := store.MarkSeatRefreshed(task.TaskID, e.name, msg.SessionKey); err != nil {
		return nil, true, err
	}
	msg.Content = e.prependFeatureContext(task, store.Path(), msg.Content)
	slog.Info("feature-start: lazy refreshed seat", "project", e.name, "task_id", task.TaskID, "session", msg.SessionKey)
	return session, true, nil
}

func (e *Engine) applyLazyFeatureContextToRelayMessage(sessions *SessionManager, relaySessionKey, sourceSessionKey string, message *string) error {
	if e == nil || sessions == nil || message == nil || strings.TrimSpace(e.dataDir) == "" {
		return nil
	}
	store := NewFeatureBoardStore(e.dataDir)
	task, shouldRefresh, err := store.ActiveTaskForSeat(e.name)
	if err != nil || !shouldRefresh {
		return err
	}
	if _, err := e.refreshFeatureSeatSessionForKey(sessions, relaySessionKey, relaySessionKey, task); err != nil {
		return err
	}
	if err := store.MarkSeatRefreshed(task.TaskID, e.name, relaySessionKey); err != nil {
		return err
	}
	*message = e.prependFeatureContext(task, store.Path(), *message)
	slog.Info("feature-start: lazy refreshed relay seat",
		"project", e.name,
		"task_id", task.TaskID,
		"relay_session", relaySessionKey,
		"source_session", sourceSessionKey,
	)
	return nil
}

func (e *Engine) prependFeatureContext(task *FeatureTask, boardPath, content string) string {
	if task == nil {
		return content
	}
	var b strings.Builder
	b.WriteString("[FEATURE-CONTEXT]\n")
	b.WriteString("This seat has just been lazily refreshed for the active Nexus feature.\n")
	b.WriteString("Do not treat this context block alone as a task. Process the actual message after the separator.\n")
	b.WriteString(fmt.Sprintf("Task ID: %s\n", task.TaskID))
	b.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("Board: %s\n", boardPath))
	b.WriteString(fmt.Sprintf("Seat: %s\n", e.name))
	if task.NextAction != "" {
		b.WriteString(fmt.Sprintf("Board next_action: %s\n", task.NextAction))
	}
	b.WriteString("[/FEATURE-CONTEXT]\n---\n")
	b.WriteString(content)
	return b.String()
}

func (e *Engine) launchFeatureCounselAudit(msg *Message, task *FeatureTask, boardPath string) {
	if e == nil || e.relayManager == nil {
		return
	}
	prompt := fmt.Sprintf(`[FEATURE-START COUNSEL AUDIT]
Task ID: %s
Title: %s
Board: %s

Chef requested adversarial audit because /feature-start used --risk.
Check pricing, API capability, product, architecture, security, and implementation-assumption risk.
Return concise findings with verified/speculative labels and any blocker that should stop implementation.`,
		task.TaskID, task.Title, boardPath)
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Minute)
		defer cancel()
		if _, err := e.relayManager.Send(ctx, RelayRequest{
			From:       featureChefSeat,
			To:         featureCounselSeat,
			SessionKey: msg.SessionKey,
			Message:    prompt,
		}); err != nil {
			slog.Warn("feature-start: counsel audit relay failed", "task_id", task.TaskID, "error", err)
		}
	}()
}
