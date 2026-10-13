package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Project settings serialize against routing and background dispatch across all
// bots hosted by this service. A busy project is rejected, never waited on.
func (s *TeamProjectStore) projectGuard(id string) *sync.RWMutex {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.guards == nil {
		s.guards = map[string]*sync.RWMutex{}
	}
	if s.guards[id] == nil {
		s.guards[id] = &sync.RWMutex{}
	}
	return s.guards[id]
}
func (s *TeamProjectStore) projectEngines(p TeamProject) []*Engine {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	var out []*Engine
	for _, bot := range p.Bots {
		if e := s.engines[bot]; e != nil {
			out = append(out, e)
		}
	}
	return out
}
func (e *Engine) lockProjectDispatch(session string) func() {
	p, err := e.teamProject(session)
	if err != nil || p == nil {
		return func() {}
	}
	g := e.teamProjects.projectGuard(p.ID)
	g.RLock()
	return g.RUnlock
}
func projectSessionKey(key string, p TeamProject) bool {
	prefix := p.Platform + ":" + p.ChatID
	return key == prefix || strings.HasPrefix(key, prefix+":") || strings.Contains(key, ":"+prefix+":")
}
func (e *Engine) legacyProjectDirectories(p TeamProject) []string {
	if p.DirectoryUnified {
		return nil
	}
	found := map[string]bool{}
	primary := normalizeWorkspacePath(p.Directories[0])
	add := func(dir string) {
		if dir != "" && normalizeWorkspacePath(dir) != primary {
			found[dir] = true
		}
	}
	// All bot bindings share one store, including bots not currently running.
	if e.workspaceBindings != nil {
		for _, bot := range append(append([]string{}, p.Bots...), "") {
			scope := "project:" + bot
			if bot == "" {
				scope = sharedWorkspaceBindingsKey
			}
			for key, b := range e.workspaceBindings.ListByProject(scope) {
				if key == p.ChatID || projectSessionKey(key, p) {
					add(b.Workspace)
				}
			}
		}
	}
	for _, bot := range e.teamProjects.projectEngines(p) {
		if bot.projectState == nil {
			continue
		}
		bot.projectState.mu.RLock()
		for key, dir := range bot.projectState.state.WorkspaceDirOverrides {
			if projectSessionKey(key, p) {
				add(dir)
			}
		}
		bot.projectState.mu.RUnlock()
	}
	var out []string
	for dir := range found {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}
func (e *Engine) projectDirectoryConflict(p TeamProject) string {
	if dirs := e.legacyProjectDirectories(p); len(dirs) > 0 {
		return e.i18n.Tf(MsgProjectDirectoryConflict, p.Directories[0], strings.Join(dirs, "\n"))
	}
	return ""
}
func (e *Engine) projectBusy(p TeamProject) bool {
	for _, bot := range e.teamProjects.projectEngines(p) {
		bot.interactiveMu.Lock()
		pool := bot.workspacePool
		bot.interactiveMu.Unlock()
		if pool == nil {
			continue
		}
		for key, ws := range pool.All() {
			suffix := "\x1fproject:" + p.ID
			if !strings.HasSuffix(key, suffix) && !strings.Contains(key, suffix+"\x1frev:") {
				continue
			}
			ws.mu.Lock()
			sm, active := ws.sessions, ws.activeTurns
			ws.mu.Unlock()
			if active > 0 {
				return true
			}
			if sm != nil {
				for _, s := range sm.AllSessions() {
					if s.Busy() {
						return true
					}
				}
			}
		}
	}
	return false
}

// Project-owned directory changes always go through this path. Old bindings
// remain recoverable on disk, but no longer participate after explicit choice.
func (e *Engine) updateProjectSettings(id, field, value string) error {
	return e.updateProjectSettingsWithDirectories(id, field, value, nil, nil)
}

func (e *Engine) updateProjectSettingsWithDirectories(id, field, value string, directories []string, revision *uint64) error {
	g := e.teamProjects.projectGuard(id)
	if !g.TryLock() {
		return fmt.Errorf("%s", e.i18n.T(MsgProjectBusy))
	}
	defer g.Unlock()
	all, err := e.teamProjects.All()
	if err != nil {
		return err
	}
	p, ok := all[id]
	if !ok {
		return fmt.Errorf("%s", e.i18n.T(MsgProjectGroupRequired))
	}
	if revision != nil && p.ContextRevision != *revision {
		return fmt.Errorf("%s", e.i18n.T(MsgFormExpired))
	}
	if p.State == "ended" {
		return fmt.Errorf("%s", e.i18n.T(MsgProjectEnded))
	}
	if p.State != "ready" {
		return fmt.Errorf("%s", e.i18n.T(MsgTeamProjectPending))
	}
	if e.projectBusy(p) {
		return fmt.Errorf("%s", e.i18n.T(MsgProjectBusy))
	}
	changedContext := false
	switch field {
	case "directory", "directories":
		if field == "directory" {
			directories = append([]string{value}, p.Directories[1:]...)
		}
		if len(directories) == 0 {
			return fmt.Errorf("%s", e.i18n.T(MsgProjectDirectoriesHelp))
		}
		for _, dir := range directories {
			if !filepath.IsAbs(dir) {
				return fmt.Errorf("%s", e.i18n.Tf(MsgWsRouteAbsoluteRequired, dir))
			}
			if _, err := NormalizeProjectDirectories(dir, nil); err != nil {
				return fmt.Errorf("%s", e.i18n.Tf(MsgDirInvalidPath, dir))
			}
		}
		dirs, err := NormalizeProjectDirectories(directories[0], directories[1:])
		if err != nil {
			return err
		}
		if len(dirs) > 1 {
			engines := e.teamProjects.projectEngines(p)
			ready := map[string]bool{}
			for _, bot := range engines {
				ready[bot.name] = SupportsProjectDirectories(bot.agent)
			}
			for _, bot := range p.Bots {
				if !ready[bot] {
					return fmt.Errorf("%s", e.i18n.Tf(MsgProjectDirectoriesUnsupported, bot))
				}
			}
		}
		changedContext = !slices.Equal(p.Directories, dirs) || e.projectDirectoryConflict(p) != ""
		p.Directories, p.DirectoryUnified = dirs, true
	case "name":
		p.Name = value
	case "goal":
		changedContext = p.Goal != value
		p.Goal = value
	default:
		return fmt.Errorf("%s", e.i18n.T(MsgProjectSettingsUsage))
	}
	if changedContext {
		p.ContextRevision++
	}
	if err := e.teamProjects.Put(p); err != nil {
		return err
	}
	if changedContext {
		for _, bot := range e.teamProjects.projectEngines(p) {
			bot.interactiveMu.Lock()
			var keys []string
			for key := range bot.interactiveStates {
				if projectSessionKey(key, p) {
					keys = append(keys, key)
				}
			}
			bot.interactiveMu.Unlock()
			for _, key := range keys {
				bot.cleanupInteractiveState(key)
			}
		}
	}
	return nil
}

func (e *Engine) renderProjectSettings(p TeamProject) *Card {
	if p.State == "ended" {
		return e.boardCard(p, false)
	}
	cb := NewCard().Title(e.i18n.Tf(MsgProjectSettingsTitle, p.Name), "blue").
		Markdown(e.i18n.Tf(MsgProjectSettingsBody, p.ID, p.Goal, p.Team, p.Manager, strings.Join(p.Bots, ", "), strings.Join(p.Directories, "\n")))
	if conflict := e.projectDirectoryConflict(p); conflict != "" {
		cb.Markdown(conflict)
	}
	cb.Markdown(e.i18n.T(MsgWorkflowHelp)).Buttons(DangerBtn(e.i18n.T(MsgProjectFinish), "cmd:/project finish"))
	card := cb.ButtonsEqual(DefaultBtn(e.i18n.T(MsgProjectEditInfo), "cmd:/project edit"), DefaultBtn(e.i18n.T(MsgProjectEditDirectory), "cmd:/project directory")).
		ButtonsEqual(DefaultBtn(e.i18n.T(MsgProjectViewMembers), "cmd:/project members"), DefaultBtn(e.i18n.T(MsgProjectBotSettings), "cmd:/project bot")).
		Note(e.i18n.T(MsgProjectDirectoryScope)).Divider().Buttons(DefaultBtn(e.i18n.T(MsgCardBack), "nav:/help project")).Build()
	card.Reusable = true
	return card
}
func (e *Engine) renderProjectBotSettings(p TeamProject, session string) *Card {
	agent, _, _, _, err := e.workspaceContext(p.Directories[0], session)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgProjectBotSettings), "red", err.Error())
	}
	model := "—"
	if a, ok := agent.(ModelSwitcher); ok {
		model = a.GetModel()
	}
	source := e.i18n.T(MsgProjectModelInherited)
	if e.projectState != nil && e.projectState.WorkspaceModelOverride(e.teamWorkspaceKey(p.Directories[0], session)) != "" {
		source = e.i18n.T(MsgProjectModelOverride)
	}
	card := NewCard().Title(e.i18n.T(MsgProjectBotSettings), "blue").
		Markdown(e.i18n.Tf(MsgProjectBotSettingsBody, e.name, agent.Name(), model, source)).
		ButtonsEqual(DefaultBtn(e.i18n.T(MsgProjectChangeModel), "cmd:/model"), DefaultBtn(e.i18n.T(MsgProjectResetModel), "cmd:/project model reset")).
		Markdown(e.i18n.T(MsgProjectSharedSettings)).Buttons(DefaultBtn(e.i18n.T(MsgProjectViewMembers), "cmd:/team members")).
		Divider().Buttons(DefaultBtn(e.i18n.T(MsgCardBack), "cmd:/project settings")).Build()
	card.Reusable = true
	return card
}
func (e *Engine) handleProjectSettings(platform Platform, msg *Message, args []string) bool {
	action := "settings"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "settings", "directory", "directories", "_directories", "edit", "name", "goal", "members", "bot", "model":
	default:
		return false
	}
	p, err := e.teamProject(msg.SessionKey)
	if err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if p == nil {
		// Bare /project outside a project group retains the creation/list help.
		if len(args) == 0 {
			return false
		}
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectGroupRequired))
		return true
	}
	if action == "settings" {
		e.replyWithCard(platform, msg.ReplyCtx, e.renderProjectSettings(*p))
		return true
	}
	if p.State == "ended" && action != "members" {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectEnded))
		return true
	}
	if action == "_directories" || action == "directories" || (action == "directory" && len(args) == 1) {
		return e.handleProjectDirectories(platform, msg, *p, args)
	}
	if action == "edit" {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectSettingsUsage))
		return true
	}
	if action == "members" {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgProjectMembersBody, p.Manager, strings.Join(p.Bots, ", ")))
		return true
	}
	if action == "bot" {
		unlock := e.lockProjectDispatch(msg.SessionKey)
		defer unlock()
		p, err = e.teamProject(msg.SessionKey)
		if err != nil || p == nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectGroupRequired))
			return true
		}
		if err := e.validateTeamSessionContext(msg.SessionKey); err != nil {
			e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		} else {
			e.replyWithCard(platform, msg.ReplyCtx, e.renderProjectBotSettings(*p, msg.SessionKey))
		}
		return true
	}
	if action == "model" {
		return e.resetProjectModel(platform, msg, p, args)
	}
	if len(args) < 2 {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectSettingsUsage))
		return true
	}
	if err := e.updateProjectSettings(p.ID, action, strings.Join(args[1:], " ")); err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	updated, err := e.teamProject(msg.SessionKey)
	if err != nil || updated == nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	card := e.renderProjectSettings(*updated)
	card.Elements = append(card.Elements, CardNote{Text: e.i18n.T(MsgProjectSettingsSaved)})
	e.replyWithCard(platform, msg.ReplyCtx, card)
	return true
}

// Route both directory entrypoints, including old help buttons, into the same
// project settings flow. Outside project groups the native handlers remain.
func (e *Engine) routeProjectDirectoryCommand(p Platform, msg *Message, command string, args []string) bool {
	project, err := e.teamProject(msg.SessionKey)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if project == nil {
		return false
	}
	if !e.isAdmin(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/project"))
		return true
	}
	// Also honor an explicitly disabled Project command on aliases.
	e.userRolesMu.RLock()
	disabled := e.disabledCmds
	if e.userRoles != nil {
		if r := e.userRoles.ResolveRole(msg.UserID); r != nil {
			disabled = r.DisabledCmds
		}
	}
	blocked := disabled["project"] || disabled[command]
	e.userRolesMu.RUnlock()
	if blocked {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/project"))
		return true
	}
	if len(args) == 0 {
		return e.handleProjectSettings(p, msg, []string{"settings"})
	}
	if command == "workspace" {
		sub := matchSubCommand(args[0], []string{"bind", "route", "list", "init", "shared", "unbind"})
		if sub == "list" {
			return e.handleProjectSettings(p, msg, []string{"settings"})
		}
		if (sub == "route" || sub == "bind") && len(args) > 1 {
			path := strings.Join(args[1:], " ")
			if sub == "bind" && !filepath.IsAbs(path) {
				path = filepath.Join(e.baseDir, path)
			}
			return e.handleProjectSettings(p, msg, []string{"directory", path})
		}
	} else if filepath.IsAbs(strings.Join(args, " ")) {
		return e.handleProjectSettings(p, msg, append([]string{"directory"}, args...))
	}
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgProjectSettingsUsage))
	return true
}

func (e *Engine) renderHelpForSession(group, session string) *Card {
	card := e.renderHelpGroupCard(group)
	if p, err := e.teamProject(session); err != nil || p == nil {
		return card
	}
	var elements []CardElement
	for _, element := range card.Elements {
		if item, ok := element.(CardListItem); ok {
			if strings.HasPrefix(item.Text, "**/workspace**") || strings.HasPrefix(item.Text, "**/dir**") {
				continue
			}
		}
		elements = append(elements, element)
	}
	card.Elements = elements
	return card
}

func (e *Engine) projectSettingsCommandDisabled(msg *Message, command string) bool {
	e.userRolesMu.RLock()
	defer e.userRolesMu.RUnlock()
	disabled := e.disabledCmds
	if e.userRoles != nil {
		if role := e.userRoles.ResolveRole(msg.UserID); role != nil {
			disabled = role.DisabledCmds
		}
	}
	return disabled[command]
}

func (e *Engine) resetProjectModel(platform Platform, msg *Message, p *TeamProject, args []string) bool {
	var err error
	if len(args) != 2 || args[1] != "reset" {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectSettingsUsage))
		return true
	}
	if e.projectSettingsCommandDisabled(msg, "model") {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/model"))
		return true
	}
	g := e.teamProjects.projectGuard(p.ID)
	if !g.TryLock() {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectBusy))
		return true
	}
	defer g.Unlock()
	p, err = e.teamProject(msg.SessionKey)
	if err != nil || p == nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectGroupRequired))
		return true
	}
	if err := e.validateTeamSessionContext(msg.SessionKey); err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if e.projectBusy(*p) {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgProjectBusy))
		return true
	}
	base, ok := e.agent.(ModelSwitcher)
	if !ok {
		e.reply(platform, msg.ReplyCtx, e.i18n.T(MsgModelNotSupported))
		return true
	}
	agent, _, _, _, err := e.commandContextWithWorkspace(platform, msg)
	if err == nil {
		_, err = e.switchModelOnAgent(agent, base.GetModel(), false)
	}
	if err != nil {
		e.reply(platform, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return true
	}
	if e.projectState != nil {
		e.projectState.ClearWorkspaceModelOverride(e.teamWorkspaceKey(p.Directories[0], msg.SessionKey))
		e.projectState.Save()
	}
	e.cleanupInteractiveState(p.Directories[0] + ":" + msg.SessionKey)
	e.replyWithCard(platform, msg.ReplyCtx, e.renderProjectBotSettings(*p, msg.SessionKey))
	return true
}
