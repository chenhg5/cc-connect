package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type teamProjectService struct {
	creator              *chatBotCreator
	permissionCheck      func(context.Context, registrationFlowResult, string) rolePermissionReport
	path, dataDir        string
	store                *core.TeamProjectStore
	readBotName          func(context.Context, registrationFlowResult) (string, error)
	labelPlatformFactory func(string, map[string]any) (core.Platform, error)
	groupPlatformFactory func(string, map[string]any) (core.Platform, error)
}

func newTeamProjectService(path, dataDir string) *teamProjectService {
	return &teamProjectService{path: path, dataDir: dataDir, store: core.NewTeamProjectStore(filepath.Join(dataDir, "team_projects.json")), readBotName: readFeishuBotName}
}
func (s *teamProjectService) handler(bot string) core.TeamCommandFunc {
	return func(ctx context.Context, p core.Platform, msg *core.Message, args []string) (answer string, resultErr error) {
		cfg, err := config.Load(s.path)
		if err != nil {
			return "", err
		}
		t, _ := cfg.TeamForBot(bot)
		i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
		if t == nil {
			return "", fmt.Errorf("%s", i18n.T(core.MsgTeamUnavailable))
		}
		if len(args) == 0 || args[0] == "help" {
			return i18n.T(core.MsgTeamChatUsage), nil
		}
		if args[0] == "members" {
			return s.members(ctx, p, msg, cfg, *t, bot, args[1:])
		}
		if bot != t.Manager {
			return "", fmt.Errorf("%s", i18n.T(core.MsgTeamManagerOnly))
		}
		if args[0] == "permissions" {
			return s.permissions(ctx, p, msg, cfg, *t, args[1:])
		}
		if args[0] == "labels" {
			return s.labels(ctx, p, msg, cfg, *t, args[1:])
		}
		automaticID := false
		if (len(args) == 2 || len(args) == 3) && args[0] == "bot" && args[1] == "create" {
			private, ok := p.(core.PrivateUserMessageDetector)
			if !ok || !private.IsPrivateUserMessage(msg) {
				return "", fmt.Errorf("%s", i18n.T(core.MsgBotPrivateOnly))
			}
			card := teamCreationForm(cfg, *t, bot, "bot")
			if err := s.creationLabels(p, msg, cfg, bot, "bot", card); err != nil {
				return "", err
			}
			if len(args) == 3 {
				if !core.ValidBotName(args[2]) {
					return "", fmt.Errorf("invalid bot reference")
				}
				card.Form.Command = "/team bot create " + args[2]
			}
			if msg.FormValues == nil {
				return replyTeamForm(ctx, p, msg, card, core.MsgTeamChatUsage)
			}
			defer func() { retryTeamForm(ctx, p, msg, card, resultErr) }()
			if len(args) != 3 {
				return "", fmt.Errorf("%s", i18n.T(core.MsgFormExpired))
			}
			ctx, err = labelCreationContext(ctx, card, msg.FormValues)
			if err != nil {
				return "", err
			}
			args, err = creationFormArgs(card, msg.FormValues, t.Manager)
			if err != nil {
				return "", err
			}
			automaticID = true
		}
		if len(args) >= 5 && args[0] == "bot" && args[1] == "create" {
			private, ok := p.(core.PrivateUserMessageDetector)
			if !ok || !private.IsPrivateUserMessage(msg) {
				return "", fmt.Errorf("%s", i18n.T(core.MsgBotPrivateOnly))
			}
			if s.creator == nil {
				return "", fmt.Errorf("bot creation unavailable")
			}
			if !core.ValidBotName(args[2]) {
				return "", fmt.Errorf("invalid bot name")
			}
			if _, err := core.AgentRoleByID(args[3]); err != nil {
				return "", err
			}
			if args[4] != "codex" && args[4] != "claudecode" && args[4] != "trae" {
				return "", fmt.Errorf("backend must be codex, claudecode or trae")
			}
			notify := func(key core.MsgKey, values ...any) {
				if err := p.Reply(ctx, msg.ReplyCtx, i18n.Tf(key, values...)); err != nil {
					slog.Error("team bot registration notification failed", "key", key, "error", err)
				}
			}
			s.creator.mu.Lock()
			if s.creator.busy[args[2]] {
				s.creator.mu.Unlock()
				return "", fmt.Errorf("bot registration is already running")
			}
			s.creator.busy[args[2]] = true
			s.creator.mu.Unlock()
			defer func() { s.creator.mu.Lock(); delete(s.creator.busy, args[2]); s.creator.mu.Unlock() }()
			r, err := s.creator.prepareWithOptions(ctx, bot, msg.UserID, args[2], automaticID, notify, args[3])
			if err != nil {
				return "", err
			}
			custom := strings.Join(args[5:], " ")
			if r.TeamBackend != "" && (r.TeamRole != args[3] || r.TeamBackend != args[4] || r.TeamPrompt != custom) {
				return "", fmt.Errorf("retry with the original bot options")
			}
			r.TeamRole, r.TeamBackend, r.TeamPrompt = args[3], args[4], custom
			if err = s.creator.saveRecord(r); err != nil {
				return "", err
			}
			s.creator.finish(ctx, r, notify)
			return "", nil
		}
		return i18n.T(core.MsgTeamChatUsage), nil
	}
}

// projectHandler owns project groups and workspace associations; Team commands
// only manage the reusable bot roster and identities.
func (s *teamProjectService) projectHandler(bot string) core.TeamCommandFunc {
	return func(ctx context.Context, p core.Platform, msg *core.Message, args []string) (answer string, resultErr error) {
		cfg, err := config.Load(s.path)
		if err != nil {
			return "", err
		}
		t, _ := cfg.TeamForBot(bot)
		i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
		if t == nil {
			return "", fmt.Errorf("%s", i18n.T(core.MsgTeamUnavailable))
		}
		if len(args) == 0 || args[0] == "help" {
			return i18n.T(core.MsgProjectGroupUsage), nil
		}
		if args[0] == "list" {
			all, err := s.store.All()
			if err != nil {
				return "", err
			}
			var lines []string
			for _, v := range all {
				if v.Team == t.ID {
					state := v.State
					if state == "ended" {
						state = i18n.T(core.MsgProjectStatusEnded)
					}
					lines = append(lines, v.Name+" · "+state+" · "+v.ChatID)
				}
			}
			sort.Strings(lines)
			return i18n.T(core.MsgTeamProjects) + "\n" + strings.Join(lines, "\n"), nil
		}
		if bot != t.Manager {
			return "", fmt.Errorf("%s", i18n.T(core.MsgProjectManagerOnly))
		}
		formDirectory := ""
		var formNames []string
		if (len(args) == 1 && args[0] == "create") || (len(args) == 2 && args[0] == "_create") {
			s.refreshTeamNames(ctx, cfg, t, "")
			dir, err := projectCreationWorkDir(ctx, cfg, bot)
			if err != nil {
				return "", err
			}
			card := teamCreationForm(cfg, *t, bot, "project", dir)
			if err := s.creationLabels(p, msg, cfg, bot, "project", card); err != nil {
				return "", err
			}
			if len(args) == 2 {
				if !core.ValidBotName(args[1]) {
					return "", fmt.Errorf("%s", i18n.T(core.MsgFormExpired))
				}
				card.Form.Command = "/project _create " + args[1]
			}
			if msg.FormValues == nil {
				return replyTeamForm(ctx, p, msg, card, core.MsgProjectGroupUsage)
			}
			if len(args) != 2 {
				return "", fmt.Errorf("%s", i18n.T(core.MsgFormExpired))
			}
			defer func() { retryTeamForm(ctx, p, msg, card, resultErr) }()
			ctx, err = labelCreationContext(ctx, card, msg.FormValues)
			if err != nil {
				return "", err
			}
			args, err = creationFormArgs(card, msg.FormValues, t.Manager)
			if err != nil {
				return "", err
			}
			// The form accepts one path as data, including commas and spaces.
			formDirectory = args[2]
			formNames = []string{args[5]}
			args = args[:5]
		}
		if len(args) >= 3 && args[0] == "create" {
			creator, ok := p.(core.ProjectGroupCreator)
			if !ok {
				return "", fmt.Errorf("%s", i18n.T(core.MsgTeamGroupUnsupported))
			}
			bots := []string{}
			for _, m := range t.Members {
				bots = append(bots, m.Bot)
			}
			if len(args) > 3 {
				bots = strings.Split(args[3], ",")
			}
			dirs := strings.Split(args[2], ",")
			if formDirectory != "" {
				dirs = []string{formDirectory}
			}
			goal := ""
			if len(args) > 4 {
				goal = strings.Join(args[4:], " ")
			}
			v, err := s.create(ctx, cfg, *t, creator, p.Name(), msg.UserID, args[1], dirs, bots, goal, formNames...)
			if err != nil {
				return "", err
			}
			return i18n.Tf(core.MsgTeamProjectReady, v.Name, creator.ProjectGroupLink(v.ChatID)) + "\n" + s.finishLabelJob(ctx, p, cfg, bot, msg.UserID, "project", v.ID), nil
		}
		return i18n.T(core.MsgProjectGroupUsage), nil
	}
}

func teamBotPlatform(cfg *config.Config, bot, platform string) (config.ProjectConfig, config.PlatformConfig, error) {
	for _, b := range cfg.Projects {
		if b.Name == bot {
			for _, p := range b.Platforms {
				if p.Type == platform {
					return b, p, nil
				}
			}
		}
	}
	return config.ProjectConfig{}, config.PlatformConfig{}, fmt.Errorf("bot %q has no matching platform", bot)
}
func (s *teamProjectService) create(ctx context.Context, cfg *config.Config, t config.TeamConfig, p core.ProjectGroupCreator, platform, user, id string, dirs, bots []string, goal string, names ...string) (*core.TeamProject, error) {
	if !core.ValidBotName(id) || user == "" || len(dirs) == 0 {
		return nil, fmt.Errorf("invalid project id, creator or directories")
	}
	name := id // Legacy text commands continue to use the explicit ID as the name.
	if len(names) > 0 {
		name = strings.TrimSpace(names[0])
		if name == "" {
			i18n := core.NewI18n(core.NormalizeLanguageString(cfg.Language))
			return nil, fmt.Errorf("%s", i18n.Tf(core.MsgFormInvalidField, i18n.T(core.MsgFormProjectName)))
		}
	}
	seen := map[string]bool{}
	botIDs := []string{t.Manager}
	seen[t.Manager] = true
	for _, b := range bots {
		if !seen[b] {
			botIDs = append(botIDs, b)
			seen[b] = true
		}
	}
	apps := []string{}
	managerApp := ""
	for _, b := range botIDs {
		member := false
		for _, m := range t.Members {
			if m.Bot == b {
				member = true
			}
		}
		if !member {
			return nil, fmt.Errorf("bot %q is not in team", b)
		}
		bc, pc, err := teamBotPlatform(cfg, b, platform)
		if err != nil {
			return nil, err
		}
		if len(dirs) > 1 {
			effective, err := cfg.EffectiveProject(bc)
			if err != nil {
				return nil, err
			}
			a, err := core.CreateAgent(effective.Agent.Type, effective.Agent.Options)
			if err != nil {
				return nil, err
			}
			ok := core.SupportsProjectDirectories(a)
			a.Stop()
			if !ok {
				return nil, fmt.Errorf("bot %q does not support multiple directories", b)
			}
		}
		app, _ := pc.Options["app_id"].(string)
		if app == "" {
			return nil, fmt.Errorf("bot %q has no application identity", b)
		}
		apps = append(apps, app)
		if b == t.Manager {
			managerApp = app
		}
	}
	for i, dir := range dirs {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("project directories must be absolute")
		}
		dir = filepath.Clean(dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("invalid project directory")
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		dirs[i] = resolved
	}
	// Covers multiple processes as well as concurrent chat requests.
	lock, err := AcquireInstanceLock(filepath.Join(s.dataDir, "team-project-operations"))
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	all, err := s.store.All()
	if err != nil {
		return nil, err
	}
	v, exists := all[id]
	if exists {
		if v.State == "ended" {
			return nil, fmt.Errorf("%s", core.NewI18n(core.NormalizeLanguageString(cfg.Language)).T(core.MsgProjectEnded))
		}
		if v.Team != t.ID || v.Creator != user || v.Platform != platform || !reflect.DeepEqual(v.Bots, botIDs) || !reflect.DeepEqual(v.Directories, dirs) || v.Goal != goal || (len(names) > 0 && v.Name != name) {
			return nil, fmt.Errorf("project exists with different settings; use its existing group")
		}
	} else {
		v = core.TeamProject{DirectoryUnified: true, ID: id, Name: name, Team: t.ID, Platform: platform, Manager: t.Manager, ManagerApp: managerApp, Bots: botIDs, BotApps: apps, Directories: dirs, Goal: goal, Creator: user, UUID: core.GenerateToken(16), State: "creating"}
		if err = s.store.Put(v); err != nil {
			return nil, err
		}
	}
	if _, ok := p.(core.PersonalLabelManager); ok {
		if err := s.prepareLabelJob(ctx, cfg, t.Manager, user, "project", id); err != nil {
			return nil, err
		}
	}
	if v.ChatID == "" {
		chat, err := p.CreateProjectGroup(ctx, v.Name, user, v.UUID)
		if err != nil {
			return nil, err
		}
		v.ChatID = chat
		v.State = "adding_members"
		if err = s.store.Put(v); err != nil {
			return nil, err
		}
	}
	if err = p.EnsureProjectGroupBots(ctx, v.ChatID, v.BotApps); err != nil {
		return nil, err
	}
	present, err := p.ProjectGroupHasBot(ctx, v.ChatID, v.ManagerApp)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("manager is not in project group")
	}
	v.State = "ready"
	if err = s.store.Put(v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Automatic dispatch checks the manager using the manager application's identity.
func (s *teamProjectService) checkManager(ctx context.Context, v core.TeamProject) error {
	cfg, err := config.Load(s.path)
	if err != nil {
		return err
	}
	_, pc, err := teamBotPlatform(cfg, v.Manager, v.Platform)
	if err != nil {
		return err
	}
	p, err := core.CreatePlatform(pc.Type, pc.Options)
	if err != nil {
		return err
	}
	capability, ok := p.(core.ProjectGroupCreator)
	if !ok {
		return fmt.Errorf("platform has no group membership support")
	}
	present, err := capability.ProjectGroupHasBot(ctx, v.ChatID, v.ManagerApp)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("manager is not in project group; restore it before dispatching tasks")
	}
	return nil
}
