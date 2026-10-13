package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProjectGroupCreator is optional. Platform credentials stay in the adapter.
// UUID must make retried group creation idempotent on the remote platform.
type ProjectGroupCreator interface {
	CreateProjectGroup(context.Context, string, string, string) (string, error)
	EnsureProjectGroupBots(context.Context, string, []string) error
	ProjectGroupHasBot(context.Context, string, string) (bool, error)
	ProjectGroupLink(string) string
}

type TeamProject struct {
	Round             uint64                        `json:"round,omitempty"`
	RoundState        string                        `json:"round_state,omitempty"`
	RoundGoal         string                        `json:"round_goal,omitempty"`
	RoundStartedAt    string                        `json:"round_started_at,omitempty"`
	RoundEndedAt      string                        `json:"round_ended_at,omitempty"`
	RoundEndedBy      string                        `json:"round_ended_by,omitempty"`
	Rounds            []TeamRound                   `json:"rounds,omitempty"`
	EndedAt           string                        `json:"ended_at,omitempty"`
	EndedBy           string                        `json:"ended_by,omitempty"`
	ManagerContexts   map[string]TeamManagerContext `json:"manager_contexts,omitempty"`
	ResultMessages    map[string]string             `json:"result_messages,omitempty"`
	ManagerThreads    []string                      `json:"manager_threads,omitempty"`
	ManagerSessionKey string                        `json:"manager_session_key,omitempty"`
	RequesterID       string                        `json:"requester_id,omitempty"`
	Tasks             map[string]TeamTask           `json:"tasks,omitempty"`
	TaskSequence      int                           `json:"task_sequence,omitempty"`
	Paused            bool                          `json:"paused,omitempty"`
	WorkflowEpoch     uint64                        `json:"workflow_epoch,omitempty"`
	DisplayMode       string                        `json:"display_mode,omitempty"`
	BoardMessageID    string                        `json:"board_message_id,omitempty"`
	Reports           map[string]TeamMemberReport   `json:"reports,omitempty"`
	DirectoryUnified  bool                          `json:"directory_unified,omitempty"`
	ContextRevision   uint64                        `json:"context_revision,omitempty"`
	ID                string                        `json:"id"`
	Team              string                        `json:"team"`
	Name              string                        `json:"name"`
	Platform          string                        `json:"platform"`
	ChatID            string                        `json:"chat_id"`
	Manager           string                        `json:"manager"`
	ManagerApp        string                        `json:"manager_app"`
	Bots              []string                      `json:"bots"`
	BotApps           []string                      `json:"bot_apps"`
	Directories       []string                      `json:"directories"`
	Goal              string                        `json:"goal"`
	Creator           string                        `json:"creator"`
	UUID              string                        `json:"uuid"`
	State             string                        `json:"state"`
}

type TeamProjectStore struct {
	roundStops     map[string][]projectCloseTarget
	roundStopping  map[string]bool
	decisionRoutes map[string]decisionRouteEntry
	mu             sync.Mutex
	path           string
	runtimeMu      sync.Mutex
	engines        map[string]*Engine
	guards         map[string]*sync.RWMutex
	viewMu         sync.Mutex
	viewContent    map[string]string
	labels         map[string]string
	displayNames   map[string]string
}

func NewTeamProjectStore(path string) *TeamProjectStore { return &TeamProjectStore{path: path} }
func (s *TeamProjectStore) load() (map[string]TeamProject, error) {
	out := map[string]TeamProject{}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var d struct {
		Version  int                    `json:"version"`
		Projects map[string]TeamProject `json:"projects"`
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	if d.Version != 1 {
		return nil, fmt.Errorf("unsupported team project state version")
	}
	for k, p := range d.Projects {
		if k != p.ID || !ValidBotName(p.ID) || len(p.Directories) == 0 || !filepath.IsAbs(p.Directories[0]) {
			return nil, fmt.Errorf("invalid team project record")
		}
		out[k] = p
	}
	return out, nil
}
func (s *TeamProjectStore) All() (map[string]TeamProject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}
func (s *TeamProjectStore) Put(p TeamProject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	if old, ok := all[p.ID]; ok && (old.State == "ended" || old.RoundState != "" || old.Round > 0) {
		return errProjectEnded
	}
	all[p.ID] = p
	return s.save(all)
}

func (s *TeamProjectStore) save(all map[string]TeamProject) error {
	b, err := json.MarshalIndent(struct {
		Version  int                    `json:"version"`
		Projects map[string]TeamProject `json:"projects"`
	}{1, all}, "", "  ")
	if err != nil {
		return err
	}
	return writeProjectState(s.path, b)
}
func (s *TeamProjectStore) ForSession(bot, session string) (*TeamProject, error) {
	if s == nil {
		return nil, nil
	}
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	platform, chat := extractPlatformName(session), extractChannelID(session)
	for _, p := range all {
		if p.Platform == platform && p.ChatID == chat {
			if !stringSliceContains(p.Bots, bot) {
				return nil, fmt.Errorf("bot is not a member of this project")
			}
			q := p
			return &q, nil
		}
	}
	return nil, nil
}

// TeamCommandFunc receives authenticated requests after the ordinary owner and
// admin checks; implementation is supplied by the application layer.
type TeamCommandFunc func(context.Context, Platform, *Message, []string) (string, error)

func (e *Engine) SetTeamProjects(s *TeamProjectStore, fn TeamCommandFunc) {
	e.teamProjects = s
	e.teamCommand = fn
	if s != nil {
		s.runtimeMu.Lock()
		if s.engines == nil {
			s.engines = map[string]*Engine{}
		}
		s.engines[e.name] = e
		s.runtimeMu.Unlock()
	}
}
func (e *Engine) SetProjectCommand(fn TeamCommandFunc) { e.projectCommand = fn }

func (e *Engine) teamProject(session string) (*TeamProject, error) {
	p, err := e.teamProjects.ForSession(e.name, session)
	if p != nil {
		if origin, ok := p.ManagerContexts[session]; ok && origin.RequesterID != "" {
			p.RequesterID = origin.RequesterID
		}
	}
	return p, err
}
func (e *Engine) teamWorkspaceKey(dir, session string) string {
	session = strings.TrimPrefix(session, dir+":")
	p, err := e.teamProject(session)
	if err == nil && p != nil {
		key := dir + "\x1fproject:" + p.ID
		if p.ContextRevision > 0 {
			key += fmt.Sprintf("\x1frev:%d", p.ContextRevision)
		}
		return key
	}
	return dir
}

type projectCommandWorkDirKey struct{}

// ProjectCommandWorkDir is the effective directory of the conversation that
// opened the creation flow, resolved by the Engine rather than platform input.
func ProjectCommandWorkDir(ctx context.Context) string {
	dir, _ := ctx.Value(projectCommandWorkDirKey{}).(string)
	return dir
}

func (e *Engine) handleTeamCommand(p Platform, msg *Message) bool {
	if e.teamProjects == nil && e.teamCommand == nil && e.projectCommand == nil {
		return false
	}
	parts := splitCommandArgs(e.resolveAlias(strings.TrimSpace(msg.Content)))
	if len(parts) == 0 {
		return false
	}
	original := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	if !strings.HasPrefix(parts[0], "/") || (original != "team" && original != "project") {
		return false
	}
	command, args, handler := original, parts[1:], e.teamCommand
	// Keep the old entrypoints as aliases, with the Project permission check.
	if original == "team" && len(args) > 0 {
		switch args[0] {
		case "projects":
			command, args = "project", append([]string{"list"}, args[1:]...)
		case "project":
			command, args = "project", args[1:]
		}
	}
	if command == "project" {
		handler = e.projectCommand
	}
	e.userRolesMu.RLock()
	disabled, roles := e.disabledCmds, e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(msg.UserID); role != nil {
			disabled = role.DisabledCmds
		}
	}
	if disabled[command] || disabled[original] {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/"+command))
		return true
	}
	if handler == nil && !(command == "project" && e.teamProjects != nil) {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgTeamUnavailable))
		return true
	}
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/"+command))
		return true
	}
	if command == "project" && e.handleProjectWorkflow(p, msg, args) {
		return true
	}
	if handler == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWorkflowHelp))
		return true
	}
	if command == "project" && e.handleProjectSettings(p, msg, args) {
		return true
	}
	go func() {
		ctx := e.ctx
		if command == "project" && len(args) == 1 && args[0] == "create" {
			agent, _, _, dir, err := e.commandContextWithWorkspace(p, msg)
			if err != nil {
				e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
				return
			}
			if dir == "" {
				dir = e.commandWorkDir(agent, msg)
			}
			ctx = context.WithValue(ctx, projectCommandWorkDirKey{}, dir)
		}
		answer, err := handler(ctx, p, msg, args)
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
			return
		}
		if answer != "" {
			e.reply(p, msg.ReplyCtx, answer)
		}
	}()
	return true
}

// These entries participate in the ordinary help card and command menus. Their
// visibility is scoped to runtimes with the respective optional handler.
func (e *Engine) managementCommands() []BotCommandInfo {
	var commands []BotCommandInfo
	if e.teamCommand != nil {
		commands = append(commands, BotCommandInfo{Command: "team", Description: e.i18n.T("team")})
	}
	if e.projectCommand != nil {
		commands = append(commands, BotCommandInfo{Command: "project", Description: e.i18n.T("project")})
	}
	return commands
}
func (e *Engine) managementHelpGroups() []helpCardGroup {
	var groups []helpCardGroup
	if e.teamCommand != nil {
		groups = append(groups, helpCardGroup{key: "team", titleKey: MsgHelpTeamSection, items: []helpCardItem{
			{command: "/team members", action: "cmd:/team members"},
			{command: "/team labels", action: "cmd:/team labels"},
			{command: "/team permissions", action: "cmd:/team permissions"},
			{command: "/team bot create", action: "cmd:/team bot create"},
		}})
	}
	if e.projectCommand != nil {
		groups = append(groups, helpCardGroup{key: "project", titleKey: MsgHelpProjectSection, items: []helpCardItem{
			{command: "/project dashboard", action: "cmd:/project dashboard"},
			{command: "/project history", action: "cmd:/project history"},
			{command: "/project tasks", action: "cmd:/project tasks"},
			{command: "/project display", action: "cmd:/project display"},
			{command: "/project pause", action: "cmd:/project pause"},
			{command: "/project resume", action: "cmd:/project resume"},
			{command: "/project finish", action: "cmd:/project finish"},
			{command: "/project settings", action: "cmd:/project settings"},
			{command: "/project list", action: "cmd:/project list"},
			{command: "/project create", action: "cmd:/project create"},
		}})
	}
	return groups
}

// prepareTeamMessage only establishes native routing. It does not consume or
// filter normal commands, replace the platform, or manufacture card callbacks.
func (e *Engine) prepareTeamMessage(p Platform, msg *Message) bool {
	project, err := e.teamProject(msg.SessionKey)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if project == nil {
		return false
	}
	if project.State == "ended" {
		if !msg.teamInternal {
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProjectEnded))
		}
		return true
	}
	if project.State != "ready" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgTeamProjectPending))
		return true
	}
	if e.prepareProjectRound(p, msg, project) {
		return true
	}
	if project.Paused || (msg.teamInternal && msg.teamEpoch != project.WorkflowEpoch) {
		if !strings.HasPrefix(strings.TrimSpace(msg.Content), "/") {
			if !msg.teamInternal {
				e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWorkflowPaused))
			}
			return true
		}
		switch strings.Fields(msg.Content)[0] {
		case "/help", "/stop", "/status", "/current":
		default:
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgWorkflowPaused))
			return true
		}
	}
	if conflict := e.projectDirectoryConflict(*project); conflict != "" {
		e.reply(p, msg.ReplyCtx, conflict)
		return true
	}
	msg.teamEpoch = project.WorkflowEpoch
	if err := e.routeManagerThread(p, msg, project); err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if duplicate, err := e.routeMemberGroupRework(p, msg, project); duplicate || err != nil {
		if err != nil {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		}
		return true
	}
	// Workspace selection belongs to the group. Display routes retain thread IDs;
	// model conversation identity is scoped separately by project and member.
	msg.ChannelKey = project.ChatID
	msg.LegacyChannelKey = ""
	if e.relayManager != nil {
		for _, bot := range project.Bots {
			e.relayManager.AddToBind(project.Platform, project.ChatID, bot)
		}
	}
	return false
}

func (e *Engine) useTeamWorkspace(session string) bool {
	if !e.multiWorkspace {
		return false
	}
	if e.teamProjects == nil {
		return true
	}
	if p, err := e.teamProject(session); p != nil || err != nil {
		return true
	}
	b, _, _ := e.lookupEffectiveWorkspaceBinding(extractWorkspaceChannelKey(session))
	return b != nil
}

func (e *Engine) SetTeamManagerCheck(fn func(context.Context, TeamProject) error) {
	e.teamManagerCheck = fn
}
func (e *Engine) checkTeamDispatch(ctx context.Context, session string) error {
	p, err := e.teamProject(session)
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if p.State == "ended" {
		return fmt.Errorf("%s", e.i18n.T(MsgProjectEnded))
	}
	if p.State != "ready" {
		return fmt.Errorf("project group setup is incomplete")
	}
	if !roundAcceptsWork(*p) {
		return fmt.Errorf("%s", e.i18n.T(MsgRoundIdle))
	}
	if archivedManagerRound(*p, session) {
		return fmt.Errorf("%s", e.i18n.T(MsgRoundArchived))
	}
	if p.Paused {
		return fmt.Errorf("%s", e.i18n.T(MsgWorkflowPaused))
	}
	if e.teamManagerCheck == nil {
		return fmt.Errorf("manager membership check is unavailable")
	}
	return e.teamManagerCheck(ctx, *p)
}

// Validate before cards or scheduled work can use helpers with legacy global
// fallbacks. A missing workspace must never silently select another context.
func (e *Engine) validateTeamSessionContext(session string) error {
	p, err := e.teamProject(session)
	if err != nil || p == nil {
		return err
	}
	if p.State != "ready" {
		return fmt.Errorf("project group setup is incomplete")
	}
	if conflict := e.projectDirectoryConflict(*p); conflict != "" {
		return fmt.Errorf("%s", conflict)
	}
	b, _, usable := e.lookupEffectiveWorkspaceBinding(extractWorkspaceChannelKey(session))
	if b == nil || !usable {
		return fmt.Errorf("project workspace is unavailable")
	}
	_, _, _, _, err = e.workspaceContext(normalizeWorkspacePath(b.Workspace), session)
	return err
}
