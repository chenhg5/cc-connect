package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var ErrProjectContextRequired = errors.New("explicit project context required; this legacy dispatch entry is unavailable for Bots")

// ProjectRouter owns one private Engine per project. Sharing the transport does
// not share agent instances, session stores, pending requests, or reply routes.
type ProjectRouter struct {
	mu       sync.Mutex
	botID    string
	manager  bool
	projects map[string]BusinessProject
	selected map[string]string
	children map[string]*Engine
	path     string
	factory  func(BusinessProject) (*Engine, error)
	stopped  bool
}

func NewProjectRouter(botID string, manager bool, projects []BusinessProject, path string, factory func(BusinessProject) (*Engine, error)) (*ProjectRouter, error) {
	if !ValidBotName(botID) || factory == nil {
		return nil, fmt.Errorf("invalid project router")
	}
	r := &ProjectRouter{botID: botID, manager: manager, projects: map[string]BusinessProject{}, selected: map[string]string{}, children: map[string]*Engine{}, path: path, factory: factory}
	for _, p := range projects {
		if err := p.Validate(); err != nil {
			return nil, err
		}
		for _, b := range p.BotIDs {
			if b == botID {
				if _, exists := r.projects[p.ID]; exists {
					return nil, fmt.Errorf("duplicate project %q", p.ID)
				}
				p.Directories = append([]ProjectDirectory(nil), p.Directories...)
				p.BotIDs = append([]string(nil), p.BotIDs...)
				r.projects[p.ID] = p
			}
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			var state struct {
				Version  int               `json:"version"`
				Selected map[string]string `json:"selected"`
			}
			if err = json.Unmarshal(data, &state); err != nil {
				return nil, fmt.Errorf("read project selections: %w", err)
			}
			if state.Version != 1 {
				return nil, fmt.Errorf("unsupported project selection version %d", state.Version)
			}
			for key, id := range state.Selected {
				if _, ok := r.projects[id]; ok {
					r.selected[key] = id
				}
			}
		}
	}
	return r, nil
}

func projectSelectionKey(msg *Message) string {
	// The original chat key alone may be shared by every member of a group.
	data, _ := json.Marshal([]string{msg.Platform, msg.SessionKey, msg.UserID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *ProjectRouter) selectProject(key, id string) error {
	next := make(map[string]string, len(r.selected)+1)
	for k, v := range r.selected {
		next[k] = v
	}
	if id == "" {
		delete(next, key)
	} else {
		next[key] = id
	}
	if r.path != "" {
		data, err := json.Marshal(struct {
			Version  int               `json:"version"`
			Selected map[string]string `json:"selected"`
		}{1, next})
		if err != nil {
			return err
		}
		// A rename on the same filesystem is required; never truncate a live file.
		if err = writeProjectState(r.path, data); err != nil {
			return err
		}
	}
	r.selected = next
	return nil
}

func writeProjectState(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".project-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (r *ProjectRouter) engine(id string) (*Engine, error) {
	if child := r.children[id]; child != nil {
		return child, nil
	}
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("project not available to this bot")
	}
	child, err := r.factory(p)
	if err != nil {
		return nil, err
	}
	child.sharedPlatforms = true
	child.strictProjectContext = true
	child.filterExternalSessions = true
	r.children[id] = child
	return child, nil
}

// route fixes the target before processing; changing the selection later cannot
// move a running turn to another engine. Calls to the child run without r.mu.
func (r *ProjectRouter) route(owner *Engine, p Platform, msg *Message) bool {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return true
	}
	if msg.IsPermissionResponse {
		r.mu.Unlock()
		owner.reply(p, msg.ReplyCtx, owner.i18n.T(MsgProjectCallbackUnsupported))
		return true
	}
	key := projectSelectionKey(msg)
	fields := strings.Fields(msg.Content)
	id := r.selected[key]
	if len(fields) > 0 && fields[0] == "/project" {
		var reply string
		switch {
		case len(fields) == 1 || (len(fields) == 2 && fields[1] == "list"):
			ids := make([]string, 0, len(r.projects))
			for k := range r.projects {
				ids = append(ids, k)
			}
			sort.Strings(ids)
			var lines []string
			for _, k := range ids {
				marker := "  "
				if k == id {
					marker = "* "
				}
				lines = append(lines, marker+k+" — "+r.projects[k].Name)
			}
			reply = owner.i18n.T(MsgProjectUsage) + "\n" + strings.Join(lines, "\n")
		case len(fields) == 3 && fields[1] == "use":
			target := fields[2]
			if _, err := r.engine(target); err != nil {
				reply = owner.i18n.Tf(MsgError, err)
			} else if err := r.selectProject(key, target); err != nil {
				reply = owner.i18n.Tf(MsgError, err)
			} else {
				reply = owner.i18n.Tf(MsgProjectSelected, target)
			}
		case len(fields) == 2 && fields[1] == "clear":
			if err := r.selectProject(key, ""); err != nil {
				reply = owner.i18n.Tf(MsgError, err)
			} else {
				reply = owner.i18n.T(MsgProjectCleared)
			}
		default:
			reply = owner.i18n.T(MsgProjectUsage)
		}
		r.mu.Unlock()
		owner.reply(p, msg.ReplyCtx, reply)
		return true
	}
	if id == "" {
		r.mu.Unlock()
		if r.manager {
			return false
		}
		owner.reply(p, msg.ReplyCtx, owner.i18n.T(MsgProjectRequired))
		return true
	}
	child, err := r.engine(id)
	r.mu.Unlock()
	if err != nil {
		owner.reply(p, msg.ReplyCtx, owner.i18n.Tf(MsgError, err))
		return true
	}
	copied := *msg
	// Keep chat syntax for backend metadata and suffix each user deterministically.
	copied.SessionKey = msg.SessionKey + ":u:" + key
	child.ReceiveMessage(&projectTextPlatform{delegate: p}, &copied)
	return true
}

func (r *ProjectRouter) stop() error {
	r.mu.Lock()
	r.stopped = true
	children := r.children
	r.children = map[string]*Engine{}
	r.mu.Unlock()
	var errs []error
	for _, child := range children {
		if err := child.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) SetProjectRouter(r *ProjectRouter) {
	e.projectRouter = r
	e.strictProjectContext = true
}
func (e *Engine) SetSharedPlatforms() { e.sharedPlatforms = true; e.strictProjectContext = true }

// Project contexts deliberately exclude legacy directory/config mutation
// commands until their persistence is context-aware. Aliases are resolved first.
func (e *Engine) rejectUnscopedProjectCommand(p Platform, msg *Message) bool {
	if !e.strictProjectContext {
		return false
	}
	content := e.resolveAlias(strings.TrimSpace(msg.Content))
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false
	}
	if !strings.HasPrefix(content, "/") && !strings.HasPrefix(content, "!") {
		return false
	}
	switch fields[0] {
	case "/new":
		if len(fields) > 1 && fields[1] == "bot" {
			break
		}
		return false
	case "/history", "/current", "/list", "/switch", "/stop", "/compress", "/help", "/allow", "/deny":
		return false
	}
	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, fields[0]))
	return true
}

// Text commands carry the selected context. Legacy cards only carry a chat key,
// so project runtimes use text until callbacks can authenticate project scope.
type projectTextPlatform struct{ delegate Platform }

func (p *projectTextPlatform) Name() string               { return p.delegate.Name() }
func (p *projectTextPlatform) Start(MessageHandler) error { return nil }
func (p *projectTextPlatform) Stop() error                { return nil }
func (p *projectTextPlatform) Reply(ctx context.Context, reply any, text string) error {
	return p.delegate.Reply(ctx, reply, text)
}
func (p *projectTextPlatform) Send(ctx context.Context, reply any, text string) error {
	return p.delegate.Send(ctx, reply, text)
}

// ValidateProjectSessionStore protects versioned project histories from the
// legacy loader's recover-as-empty behavior on corrupt or future snapshots.
func ValidateProjectSessionStore(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state sessionSnapshot
	if err = json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("invalid project session store: %w", err)
	}
	if state.Version != snapshotVersion || state.Sessions == nil || state.ActiveSession == nil {
		return fmt.Errorf("unsupported or incomplete project session snapshot")
	}
	for id, s := range state.Sessions {
		if s == nil || s.ID != id {
			return fmt.Errorf("invalid project session identity")
		}
	}
	for _, id := range state.ActiveSession {
		if state.Sessions[id] == nil {
			return fmt.Errorf("missing active project session")
		}
	}
	return nil
}
