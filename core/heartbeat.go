package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HeartbeatConfig holds runtime heartbeat settings for a single project.
type HeartbeatConfig struct {
	Enabled      bool
	IntervalMins int
	OnlyWhenIdle bool
	SessionKey   string
	Prompt       string // explicit prompt; empty = read HEARTBEAT.md
	Silent       bool   // suppress "💓" notification
	TimeoutMins  int
}

// HeartbeatScheduler manages periodic heartbeat execution across projects.
type HeartbeatScheduler struct {
	mu      sync.Mutex
	entries []*heartbeatEntry
	stopCh  chan struct{}
}

type heartbeatEntry struct {
	project string
	config  HeartbeatConfig
	engine  *Engine
	workDir string // agent work_dir, for locating HEARTBEAT.md
	ticker  *time.Ticker
	stopCh  chan struct{}
}

func NewHeartbeatScheduler() *HeartbeatScheduler {
	return &HeartbeatScheduler{
		stopCh: make(chan struct{}),
	}
}

// Register adds a heartbeat entry for a project. Call before Start().
func (hs *HeartbeatScheduler) Register(project string, cfg HeartbeatConfig, engine *Engine, workDir string) {
	if !cfg.Enabled || cfg.SessionKey == "" {
		return
	}
	if cfg.IntervalMins <= 0 {
		cfg.IntervalMins = 30
	}
	if cfg.TimeoutMins <= 0 {
		cfg.TimeoutMins = 30
	}
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.entries = append(hs.entries, &heartbeatEntry{
		project: project,
		config:  cfg,
		engine:  engine,
		workDir: workDir,
		stopCh:  make(chan struct{}),
	})
}

// Start begins all registered heartbeat tickers.
func (hs *HeartbeatScheduler) Start() {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for _, entry := range hs.entries {
		interval := time.Duration(entry.config.IntervalMins) * time.Minute
		entry.ticker = time.NewTicker(interval)
		go hs.run(entry)
		slog.Info("heartbeat: started",
			"project", entry.project,
			"interval", interval,
			"session_key", entry.config.SessionKey,
			"only_when_idle", entry.config.OnlyWhenIdle,
		)
	}
	if len(hs.entries) > 0 {
		slog.Info("heartbeat: scheduler started", "entries", len(hs.entries))
	}
}

// Stop halts all heartbeat tickers.
func (hs *HeartbeatScheduler) Stop() {
	close(hs.stopCh)
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for _, entry := range hs.entries {
		if entry.ticker != nil {
			entry.ticker.Stop()
		}
		close(entry.stopCh)
	}
}

func (hs *HeartbeatScheduler) run(entry *heartbeatEntry) {
	for {
		select {
		case <-hs.stopCh:
			return
		case <-entry.stopCh:
			return
		case <-entry.ticker.C:
			hs.execute(entry)
		}
	}
}

func (hs *HeartbeatScheduler) execute(entry *heartbeatEntry) {
	cfg := entry.config

	if cfg.OnlyWhenIdle {
		session := entry.engine.sessions.GetOrCreateActive(cfg.SessionKey)
		if !session.TryLock() {
			slog.Debug("heartbeat: session busy, skipping", "project", entry.project, "session_key", cfg.SessionKey)
			return
		}
		// We got the lock; the actual execution path (ExecuteHeartbeat) will
		// use this same session. We must unlock here because ExecuteHeartbeat
		// will try to lock again via its own flow.
		session.Unlock()
	}

	prompt := cfg.Prompt
	if prompt == "" {
		prompt = readHeartbeatMD(entry.workDir)
	}
	if prompt == "" {
		prompt = defaultHeartbeatPrompt
	}

	slog.Info("heartbeat: executing", "project", entry.project, "session_key", cfg.SessionKey, "prompt_len", len(prompt))

	timeout := time.Duration(cfg.TimeoutMins) * time.Minute
	done := make(chan error, 1)
	go func() {
		done <- entry.engine.ExecuteHeartbeat(cfg.SessionKey, prompt, cfg.Silent)
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(timeout):
		err = fmt.Errorf("heartbeat timed out after %v", timeout)
	}

	if err != nil {
		slog.Error("heartbeat: execution failed", "project", entry.project, "error", err)
	} else {
		slog.Info("heartbeat: execution completed", "project", entry.project)
	}
}

const defaultHeartbeatPrompt = `This is a periodic heartbeat check. Please briefly review:
- Any pending tasks or unfinished work
- Current project status
If nothing needs attention, respond briefly that all is well.`

func readHeartbeatMD(workDir string) string {
	if workDir == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(workDir, "HEARTBEAT.md"),
		filepath.Join(workDir, "heartbeat.md"),
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				slog.Debug("heartbeat: loaded prompt from file", "path", path)
				return content
			}
		}
	}
	return ""
}
