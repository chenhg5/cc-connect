package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

type botStartRequest struct {
	ctx  context.Context
	name string
	done chan error
}

type botCreationRecord struct {
	Version     int
	Parent      string
	Initiator   string
	Name        string
	AutomaticID bool `json:",omitempty"`
	Identity    registrationFlowResult
	ClaimToken  string
	TeamRole    string
	TeamBackend string
	TeamPrompt  string
}

type chatBotCreator struct {
	permissionCheck func(context.Context, registrationFlowResult, string) rolePermissionReport
	beforeCreate    func(context.Context, string, string, string) error
	afterSave       func(context.Context, *botCreationRecord, func(core.MsgKey, ...any))
	configPath      string
	dataDir         string
	ctx             context.Context
	starts          chan<- botStartRequest
	register        func(registrationFlowOptions) (*registrationFlowResult, error)
	readBotName     func(context.Context, registrationFlowResult) (string, error)
	mu              sync.Mutex
	busy            map[string]bool
	started         map[string]bool
}

func newChatBotCreator(path, dataDir string, ctx context.Context, starts chan<- botStartRequest) *chatBotCreator {
	c := &chatBotCreator{configPath: path, dataDir: dataDir, ctx: ctx, starts: starts,
		register: runRegistrationFlow, readBotName: readFeishuBotName, busy: map[string]bool{}, started: map[string]bool{}}
	c.permissionCheck = func(ctx context.Context, id registrationFlowResult, role string) rolePermissionReport {
		return ensureRolePermissions(ctx, id, role, dataDir)
	}
	return c
}

func (c *chatBotCreator) markStarted(name string) {
	c.mu.Lock()
	c.started[name] = true
	c.mu.Unlock()
}

func (c *chatBotCreator) handler(parent string) core.BotCreationFunc {
	return func(ctx context.Context, p core.Platform, userID, name string, notify func(core.MsgKey, ...any)) {
		if p.Name() != "feishu" && p.Name() != "lark" {
			notify(core.MsgBotUnsupported)
			return
		}
		if !core.ValidBotName(name) {
			notify(core.MsgBotUsage)
			return
		}
		c.mu.Lock()
		if c.busy[name] {
			c.mu.Unlock()
			notify(core.MsgBotExists)
			return
		}
		c.busy[name] = true
		c.mu.Unlock()
		defer func() { c.mu.Lock(); delete(c.busy, name); c.mu.Unlock() }()
		record, err := c.prepare(ctx, parent, userID, name, notify)
		if err != nil {
			slog.Warn("chat bot creation did not complete", "parent", parent, "name", name)
			if errors.Is(err, errBotExists) {
				notify(core.MsgBotExists)
			} else if ctx.Err() == nil {
				notify(core.MsgBotFailed)
			}
			return
		}
		c.finish(ctx, record, notify)
	}
}

var errBotExists = errors.New("bot identity already exists")

func (c *chatBotCreator) recordPath(name string) string {
	return filepath.Join(c.dataDir, "bot-registrations", name+".json")
}

func (c *chatBotCreator) prepare(ctx context.Context, parent, userID, name string, notify func(core.MsgKey, ...any)) (*botCreationRecord, error) {
	return c.prepareWithOptions(ctx, parent, userID, name, false, notify)
}

func (c *chatBotCreator) prepareWithOptions(ctx context.Context, parent, userID, name string, automaticID bool, notify func(core.MsgKey, ...any), roles ...string) (*botCreationRecord, error) {
	role := ""
	if len(roles) > 0 {
		role = roles[0]
		if _, err := core.AgentRoleByID(role); err != nil {
			return nil, err
		}
	}
	if c.beforeCreate != nil {
		if err := c.beforeCreate(ctx, parent, userID, name); err != nil {
			return nil, err
		}
	}
	// Resume an authorized creation after a save/start/reply failure without
	// consuming another registration grant or creating a duplicate app.
	var record botCreationRecord
	data, err := os.ReadFile(c.recordPath(name))
	if err == nil {
		if json.Unmarshal(data, &record) != nil || (record.Version != 0 && record.Version != 2) || record.Parent != parent || record.Initiator != userID || record.Name != name {
			return nil, errBotExists
		}
		if role != "" && record.TeamRole != "" && record.TeamRole != role {
			return nil, errBotExists
		}
		if role != "" {
			record.TeamRole = role
		}
		// Older records can still resume through their original form reference.
		record.AutomaticID = record.AutomaticID || automaticID
		return &record, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, err
	}
	for _, p := range cfg.RuntimeProjects() {
		if p.Name == name {
			return nil, errBotExists
		}
	}
	// Verify local persistence before asking the user to create an external app.
	if err := os.MkdirAll(filepath.Dir(c.recordPath(name)), 0700); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(filepath.Dir(c.recordPath(name)), ".probe-")
	if err != nil {
		return nil, err
	}
	probe.Close()
	os.Remove(probe.Name())
	identity, err := c.register(registrationFlowOptions{Context: ctx, TimeoutSeconds: 600, OnVerification: func(link string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if automaticID {
			notify(core.MsgBotAuthorizeAutomatic, link)
		} else {
			notify(core.MsgBotAuthorize, name, link)
		}
		return nil
	}})
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.AppID == "" || identity.AppSecret == "" || (identity.Platform != "feishu" && identity.Platform != "lark") {
		return nil, fmt.Errorf("incomplete bot registration result")
	}
	record = botCreationRecord{Version: 2, Parent: parent, Initiator: userID, Name: name, AutomaticID: automaticID, Identity: *identity, TeamRole: role}
	if err := c.saveRecord(&record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *chatBotCreator) saveRecord(record *botCreationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.recordPath(record.Name)), ".registration-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.recordPath(record.Name))
}

func (c *chatBotCreator) finish(ctx context.Context, r *botCreationRecord, notify func(core.MsgKey, ...any)) {
	displayName := r.Name
	if r.Version == 2 && !validRegistrationOwner(r.Identity.OwnerOpenID) {
		notify(core.MsgBotRegistrationOwnerMissing)
		return
	}
	expected, owner := "", r.Identity.OwnerOpenID
	if r.Version == 0 {
		// Resume pre-upgrade registrations with their original one-time claim.
		owner = ""
		hash := sha256.Sum256([]byte(r.ClaimToken))
		expected = hex.EncodeToString(hash[:])
	}
	cfg, err := config.Load(c.configPath)
	if err != nil {
		notify(core.MsgBotSaveFailed)
		return
	}
	saved := false
	for _, p := range cfg.RuntimeProjects() {
		if p.Name != r.Name {
			continue
		}
		if p.BotOwnerClaimHash != expected || p.BotOwnerID != owner || len(p.Platforms) != 1 || p.Platforms[0].Options["app_id"] != r.Identity.AppID {
			notify(core.MsgBotExists)
			return
		}
		saved = true
	}
	if !saved {
		var err error
		if r.Version == 2 {
			err = config.AddOwnedBotIdentity(r.Parent, r.Name, r.Identity.Platform, r.Identity.AppID, r.Identity.AppSecret, owner)
		} else {
			err = config.AddBotIdentity(r.Parent, r.Name, r.Identity.Platform, r.Identity.AppID, r.Identity.AppSecret, expected)
		}
		if err != nil {
			notify(core.MsgBotSaveFailed)
			return
		}
	}
	if r.TeamRole != "" {
		if err := config.ConfigureTeamBot(r.Parent, r.Name, r.TeamRole, r.TeamBackend, r.TeamPrompt); err != nil {
			notify(core.MsgBotSaveFailed)
			return
		}
	}
	if r.AutomaticID {
		displayName = "Bot"
		actual, err := c.readBotName(ctx, r.Identity)
		if err != nil {
			notify(core.MsgBotNameReadFailed)
		} else {
			displayName = actual
			if r.TeamRole != "" {
				if err := config.ConfigureTeamBotProfile(r.Parent, r.Name, r.TeamRole, "", r.TeamPrompt, actual); err != nil {
					notify(core.MsgBotSaveFailed)
					return
				}
			}
		}
	}
	if r.TeamRole != "" && c.permissionCheck != nil {
		permissionNotice(r.Identity, r.TeamRole, c.permissionCheck(ctx, r.Identity, r.TeamRole), notify)
	}
	if c.afterSave != nil {
		c.afterSave(ctx, r, notify)
	}
	key := core.MsgBotReady
	if r.Version == 2 {
		key = core.MsgBotAutoReady
	}
	if err := c.start(ctx, r.Name); err != nil {
		key = core.MsgBotSavedOffline
		if r.Version == 2 {
			key = core.MsgBotAutoSavedOffline
		}
	}
	// App ID is public. Secrets and the registration device code are never
	// included in replies. Legacy pairing secrets go only to their creator.
	domain := "applink.feishu.cn"
	if r.Identity.Platform == "lark" {
		domain = "applink.larksuite.com"
	}
	link := "https://" + domain + "/client/bot/open?appId=" + r.Identity.AppID
	if r.Version == 2 {
		notify(key, displayName, link)
		return
	}
	notify(key, r.Name, link, r.ClaimToken)
}

func (c *chatBotCreator) start(ctx context.Context, name string) error {
	c.mu.Lock()
	started := c.started[name]
	c.mu.Unlock()
	if started {
		return nil
	}
	req := botStartRequest{ctx: ctx, name: name, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.ctx.Err()
	case c.starts <- req:
	}
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case err := <-req.done:
		if err == nil {
			c.markStarted(name)
		}
		return err
	}
}

type botEngineFactory func(*config.Config, config.ProjectConfig) (*core.Engine, string, error)

// Called only by the main lifecycle loop, which owns the engine collection.
func startCreatedBot(req botStartRequest, path string, factory botEngineFactory, register func(string, *core.Engine, string)) error {
	if err := req.ctx.Err(); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	for _, proj := range cfg.RuntimeProjects() {
		if proj.Name != req.name {
			continue
		}
		isolated := *cfg
		isolated.Bots = nil
		isolated.Projects = []config.ProjectConfig{proj}
		if err := runRunAsUserStartupChecks(req.ctx, &isolated); err != nil {
			return err
		}
		engine, workDir, err := factory(cfg, proj)
		if err != nil {
			return err
		}
		if err := engine.Start(); err != nil {
			engine.Stop()
			return err
		}
		register(proj.Name, engine, workDir)
		return nil
	}
	return fmt.Errorf("new identity missing from configuration")
}
