package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// ensureRole reuses an existing identity first. New identities use the existing
// registration flow, with authorization delivered only to the manager's owner.
func (s *teamProjectService) ensureRole(ctx context.Context, p core.Platform, project core.TeamProject, role, backend, reason string) (string, error) {
	cfg, err := config.Load(s.path)
	if err != nil {
		return "", err
	}
	team, _ := cfg.TeamForBot(project.Manager)
	if team == nil || !team.AutoCoordinate || team.ID != project.Team {
		return "", fmt.Errorf("project coordination is not enabled")
	}
	if _, err := core.AgentRoleByID(role); err != nil {
		return "", err
	}
	if role == "manager" {
		return "", fmt.Errorf("project already has a manager")
	}
	// Prefer a role already in this project, then another qualified team member.
	for _, inProject := range []bool{true, false} {
		for _, m := range team.Members {
			member := false
			for _, b := range project.Bots {
				if b == m.Bot {
					member = true
				}
			}
			if m.Role == role && member == inProject {
				if s.creator != nil {
					if err := s.creator.start(ctx, m.Bot); err != nil {
						return "", err
					}
				}
				if err := s.attachRole(ctx, cfg, p, project, m.Bot); err != nil {
					return "", err
				}
				return "ready: " + m.Bot + " (" + role + ")", nil
			}
		}
	}
	if backend == "" {
		backend = "codex"
	}
	if backend != "codex" && backend != "claudecode" && backend != "trae" {
		return "", fmt.Errorf("backend must be codex, claudecode or trae")
	}
	i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 2000 {
		return "", fmt.Errorf("%s", i18n.T(core.MsgTeamRoleReasonRequired))
	}
	private, ok := p.(core.PrivateUserMessenger)
	if !ok || s.creator == nil {
		return "", fmt.Errorf("private bot registration unavailable")
	}
	manager, _, err := teamBotPlatform(cfg, project.Manager, project.Platform)
	if err != nil {
		return "", err
	}
	if manager.BotOwnerID == "" {
		return "", fmt.Errorf("manager requires a registered owner")
	}
	sum := sha256.Sum256([]byte(project.ID + "\x00" + role))
	name := fmt.Sprintf("role-%s-%x", role, sum[:8])
	c := s.creator
	c.mu.Lock()
	if c.busy[name] {
		c.mu.Unlock()
		return "awaiting_authorization: " + name, nil
	}
	c.busy[name] = true
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); delete(c.busy, name); c.mu.Unlock() }()
		workCtx, cancel := context.WithCancel(c.ctx)
		defer cancel()
		notify := func(key core.MsgKey, values ...any) {
			text := roleRegistrationNotice(i18n, project, role, backend, reason, key, values...)
			if err := private.SendPrivateUser(workCtx, manager.BotOwnerID, text); err != nil {
				slog.Warn("private role registration notification failed", "error", err)
				cancel()
			}
		}
		r, err := c.prepareWithOptions(workCtx, project.Manager, manager.BotOwnerID, name, true, notify, role)
		if err == nil {
			if r.TeamBackend != "" && (r.TeamRole != role || r.TeamBackend != backend) {
				err = fmt.Errorf("retry with original role/backend")
			}
		}
		if err == nil {
			r.TeamRole, r.TeamBackend = role, backend
			err = c.saveRecord(r)
		}
		if err == nil {
			c.finish(workCtx, r, notify)
			c.mu.Lock()
			started := c.started[name]
			c.mu.Unlock()
			if !started {
				err = fmt.Errorf("role identity saved but runtime is not ready")
			}
		}
		if err == nil {
			var fresh *config.Config
			fresh, err = config.Load(s.path)
			if err == nil {
				err = s.attachRole(workCtx, fresh, p, project, name)
			}
		}
		result := "ready: " + name + " (" + role + ") has joined the project. It can now receive tasks."
		if err != nil {
			result = "role provisioning failed: " + name + ": " + err.Error()
		}
		s.store.NotifyManager(project.ID, result)
		if err != nil {
			notify(core.MsgTeamRoleProvisionFailed)
		}
	}()
	return "awaiting_authorization: " + name + ". Authorization is sent privately to the owner; wait for the provisioning result before dispatching.", nil
}

func roleRegistrationNotice(i18n *core.I18n, project core.TeamProject, role, backend, reason string, key core.MsgKey, values ...any) string {
	projectName := project.Name
	if projectName == "" {
		projectName = project.ID
	}
	roleName := role
	for _, option := range teamRoleOptions(i18n) {
		if option.Value == role {
			roleName = option.Text
			break
		}
	}
	backendName := map[string]string{"codex": "Codex", "claudecode": "Claude Code", "trae": "Trae"}[backend]
	if backendName == "" {
		backendName = backend
	}
	header := i18n.Tf(core.MsgTeamRoleCreationContext, projectName, project.ID, roleName, backendName, reason)
	if key == core.MsgBotAuthorizeAutomatic {
		key = core.MsgTeamRoleAuthorize
	}
	return header + "\n\n" + i18n.Tf(key, values...)
}

func (s *teamProjectService) attachRole(ctx context.Context, cfg *config.Config, p core.Platform, project core.TeamProject, bot string) error {
	groups, ok := p.(core.ProjectGroupCreator)
	if !ok {
		return core.ErrNotSupported
	}
	bc, pc, err := teamBotPlatform(cfg, bot, project.Platform)
	if err != nil {
		return err
	}
	if len(project.Directories) > 1 {
		effective, err := cfg.EffectiveProject(bc)
		if err != nil {
			return err
		}
		a, err := core.CreateAgent(effective.Agent.Type, effective.Agent.Options)
		if err != nil {
			return err
		}
		supported := core.SupportsProjectDirectories(a)
		a.Stop()
		if !supported {
			return fmt.Errorf("bot %q does not support multiple directories", bot)
		}
	}
	app, _ := pc.Options["app_id"].(string)
	if app == "" {
		return fmt.Errorf("role has no application identity")
	}
	// Admission is idempotent. Confirm the new bot before publishing membership.
	if err := groups.EnsureProjectGroupBots(ctx, project.ChatID, []string{project.ManagerApp, app}); err != nil {
		return err
	}
	present, err := groups.ProjectGroupHasBot(ctx, project.ChatID, project.ManagerApp)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("required bot has not joined the project group")
	}
	if err := s.confirmRoleMembership(ctx, pc, project.ChatID, app); err != nil {
		i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
		return fmt.Errorf("%s: %w", i18n.Tf(core.MsgTeamRoleMembershipFailed, bot, app), err)
	}
	return s.store.AddMember(project.ID, bot, app)
}

// Membership lookup answers whether the calling application itself is in the
// chat. Admission uses the manager, but confirmation must use the role's own
// identity. This API-only platform never starts another message listener.
func (s *teamProjectService) confirmRoleMembership(ctx context.Context, pc config.PlatformConfig, chat, app string) error {
	factory := s.groupPlatformFactory
	if factory == nil {
		factory = core.CreatePlatform
	}
	p, err := factory(pc.Type, pc.Options)
	if err != nil {
		return err
	}
	defer func() {
		if err := p.Stop(); err != nil {
			slog.Warn("role membership client cleanup failed", "error", err)
		}
	}()
	groups, ok := p.(core.ProjectGroupCreator)
	if !ok {
		return core.ErrNotSupported
	}
	present, err := groups.ProjectGroupHasBot(ctx, chat, app)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("required bot has not joined the project group")
	}
	return nil
}
