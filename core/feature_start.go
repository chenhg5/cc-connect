package core

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	featureChefSeat      = "chef-seat"
	featureChefFlashSeat = "chef-flash-seat"
	featureImplSeat      = "dev-deepseek"
	featureCounselSeat   = "counsel-seat"
	featureReviewSeat    = "reviewer-seat"
)

type featureStartOptions struct {
	Title string
}

type featureStartDispatch struct {
	agent          Agent
	sessions       *SessionManager
	interactiveKey string
	workspaceDir   string
	ccSessionKey   string
	session        *Session
}

func parseFeatureStartArgs(args []string) (featureStartOptions, error) {
	var opts featureStartOptions
	titleParts := make([]string, 0, len(args))
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "":
			continue
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
	if e == nil {
		return
	}
	if e.name != featureChefSeat {
		if e.name == featureChefFlashSeat {
			e.reply(p, msg.ReplyCtx, "❌ `/feature-start` is only authorized on chef-seat. Please use `/feature-start@Chef_Resonova_bot` instead.")
		}
		return
	}
	opts, err := parseFeatureStartArgs(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, "Usage: `/feature-start <title>`")
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
	dispatch, err := chef.prepareFeatureStartDispatch(chefPlatform, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef cold-start packet failed: %v", err))
		return
	}
	dispatched := false
	defer func() {
		if !dispatched && dispatch != nil && dispatch.session != nil {
			dispatch.session.UnlockWithoutUpdate()
		}
	}()

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
	if err := chef.refreshFeatureSeatSessionForDispatch(dispatch, msg, task); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef refresh failed: %v", err))
		return
	}
	if err := boardStore.MarkSeatRefreshed(task.TaskID, featureChefSeat, msg.SessionKey); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ feature board update failed: %v", err))
		return
	}
	refreshed = append(refreshed, featureChefSeat)

	secondaryRefreshed, secondaryWarnings := chef.refreshSecondaryFeatureStartSeats(chefPlatform, msg, task, boardStore, seatNames)
	refreshed = append(refreshed, secondaryRefreshed...)
	pending := pendingFeatureSeats(seatNames, refreshed)

	packet := chef.buildFeatureStartPacket(task, boardStore.Path(), refreshed, pending)
	if err := chef.injectFeatureStartPacketWithDispatch(chefPlatform, msg, packet, dispatch); err != nil {
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("❌ Chef cold-start packet failed: %v", err))
		return
	}
	dispatched = true

	reply := fmt.Sprintf("✅ Feature started: %s\nTask: `%s`\nBoard: `%s`\nRefreshed: %s",
		task.Title, task.TaskID, boardStore.Path(), strings.Join(refreshed, ", "))
	reply += "\nLazy refresh pending: " + strings.Join(pending, ", ")
	if len(secondaryWarnings) > 0 {
		reply += "\nWarnings: " + strings.Join(secondaryWarnings, "; ")
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

func pendingFeatureSeats(seatNames []string, refreshedSeats []string) []string {
	refreshed := map[string]bool{}
	for _, name := range refreshedSeats {
		refreshed[strings.TrimSpace(name)] = true
	}
	var pending []string
	for _, name := range seatNames {
		name = strings.TrimSpace(name)
		if name == "" || refreshed[name] {
			continue
		}
		pending = append(pending, name)
	}
	sort.Strings(pending)
	return pending
}

func (e *Engine) refreshSecondaryFeatureStartSeats(p Platform, msg *Message, task *FeatureTask, boardStore *FeatureBoardStore, seatNames []string) ([]string, []string) {
	known := map[string]bool{}
	for _, name := range seatNames {
		known[strings.TrimSpace(name)] = true
	}
	if !known[featureChefFlashSeat] {
		return nil, nil
	}
	flash := e.featureEngineByName(featureChefFlashSeat)
	if flash == nil {
		return nil, []string{featureChefFlashSeat + " not registered"}
	}
	flashPlatform := flash.platformByName(msg.Platform)
	if flashPlatform == nil {
		return nil, []string{featureChefFlashSeat + " has no " + msg.Platform + " platform"}
	}
	dispatch, err := flash.prepareFeatureStartDispatch(flashPlatform, msg)
	if err != nil {
		return nil, []string{featureChefFlashSeat + " refresh failed: " + err.Error()}
	}
	dispatched := false
	defer func() {
		if !dispatched && dispatch != nil && dispatch.session != nil {
			dispatch.session.UnlockWithoutUpdate()
		}
	}()
	if err := flash.refreshFeatureSeatSessionForDispatch(dispatch, msg, task); err != nil {
		return nil, []string{featureChefFlashSeat + " refresh failed: " + err.Error()}
	}
	if err := boardStore.MarkSeatRefreshed(task.TaskID, featureChefFlashSeat, msg.SessionKey); err != nil {
		return nil, []string{featureChefFlashSeat + " board update failed: " + err.Error()}
	}
	packet := flash.buildFeatureStartPacket(task, boardStore.Path(), []string{featureChefSeat, featureChefFlashSeat}, pendingFeatureSeats(seatNames, []string{featureChefSeat, featureChefFlashSeat}))
	if err := flash.injectFeatureStartPacketWithDispatch(flashPlatform, msg, packet, dispatch); err != nil {
		return nil, []string{featureChefFlashSeat + " cold-start packet failed: " + err.Error()}
	}
	dispatched = true
	return []string{featureChefFlashSeat}, nil
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

func (e *Engine) prepareFeatureStartDispatch(p Platform, msg *Message) (*featureStartDispatch, error) {
	agent, sessions, interactiveKey, workspaceDir, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		return nil, err
	}
	if sessions == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	session := sessions.GetOrCreateActive(msg.SessionKey)
	if !session.TryLock() {
		return nil, fmt.Errorf("chef session is still processing")
	}
	return &featureStartDispatch{
		agent:          agent,
		sessions:       sessions,
		interactiveKey: interactiveKey,
		workspaceDir:   workspaceDir,
		ccSessionKey:   msg.SessionKey,
		session:        session,
	}, nil
}

func (e *Engine) refreshFeatureSeatSessionForDispatch(dispatch *featureStartDispatch, msg *Message, task *FeatureTask) error {
	if e == nil {
		return fmt.Errorf("engine is nil")
	}
	if dispatch == nil || dispatch.sessions == nil || dispatch.session == nil {
		return fmt.Errorf("feature-start dispatch is not prepared")
	}
	e.cleanupInteractiveState(dispatch.interactiveKey)

	old := dispatch.session
	old.SetAgentSessionID("", "")
	old.ClearHistory()
	dispatch.sessions.Save()

	name := fmt.Sprintf("feature-start %s %s", task.TaskID, task.Title)
	next := dispatch.sessions.NewSession(msg.SessionKey, name)
	if !next.TryLock() {
		return fmt.Errorf("chef session is still processing")
	}
	old.UnlockWithoutUpdate()
	dispatch.session = next
	return nil
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

func (e *Engine) injectFeatureStartPacketWithDispatch(p Platform, msg *Message, packet string, dispatch *featureStartDispatch) error {
	if dispatch == nil || dispatch.session == nil || dispatch.sessions == nil {
		return fmt.Errorf("feature-start dispatch is not prepared")
	}
	packetMsg := *msg
	packetMsg.Content = packet
	packetMsg.Images = nil
	packetMsg.Files = nil
	go e.processInteractiveMessageWith(p, &packetMsg, dispatch.session, dispatch.agent, dispatch.sessions, dispatch.interactiveKey, dispatch.workspaceDir, dispatch.ccSessionKey)
	return nil
}

func (e *Engine) buildFeatureStartPacket(task *FeatureTask, boardPath string, refreshed, pending []string) string {
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
	b.WriteString("- chef-seat remains the /feature-start authority.\n")
	b.WriteString("- chef-seat and chef-flash-seat are refreshed immediately when both are active.\n")
	b.WriteString("- Other seats are marked stale-for-this-feature and will be refreshed lazily on first actual use, mention, or relay.\n")
	b.WriteString("- Lazy refresh must attach this feature context to the first real task; it must not create a standalone task for unused seats.\n")
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
