package opencode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestOpenCodeChildUsageScopesNestedSessionsAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE session(id TEXT PRIMARY KEY,parent_id TEXT)`,
		`CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT,data TEXT)`,
		`INSERT INTO session VALUES('parent',NULL),('child','parent'),('nested','child'),('foreign','other')`,
		`INSERT INTO message VALUES('main','parent','{"role":"assistant","time":{"completed":1},"tokens":{"input":999}}'),
		 ('one','child','{"role":"assistant","time":{"completed":1},"tokens":{"input":10,"output":20,"reasoning":30,"cache":{"read":30,"write":40}}}'),
		 ('two','nested','{"role":"assistant","time":{"completed":1},"tokens":{"input":5,"output":6}}'),
		 ('provisional','nested','{"role":"assistant","time":{},"tokens":{"input":999}}'),
		 ('other','foreign','{"role":"assistant","time":{"completed":1},"tokens":{"input":999}}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var started opencodeSidechainMeter
	started.begin(dir, "parent", false)
	// Restart before the first result has collected any child usage.
	var meter opencodeSidechainMeter
	meter.begin(dir, "parent", true)
	got := meter.collect()
	if got == nil || got.InputTokens != 15 || got.OutputTokens != 56 || got.ReasoningOutputTokens != 30 || got.ByModel[""].OutputTokens != 56 || got.CacheReadInputTokens != 30 || got.CacheCreationInputTokens != 40 {
		t.Fatalf("bad family usage: %+v", got)
	}
	if got := meter.collect(); got != nil {
		t.Fatalf("rebilled requests: %+v", got)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES('late','child','{"role":"assistant","time":{"completed":1},"tokens":{"input":7,"output":8}}')`); err != nil {
		t.Fatal(err)
	}
	var resumed opencodeSidechainMeter
	resumed.begin(dir, "parent", true)
	got = resumed.collect()
	if got == nil || got.InputTokens != 7 || got.OutputTokens != 8 {
		t.Fatalf("restart lost late request or billed history: %+v", got)
	}
}

func TestUnreadableChildUsageIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.db"), []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &opencodeSession{ctx: context.Background(), events: make(chan core.Event, 4), turnUsage: core.TokenUsage{InputTokens: 10, OutputTokens: 20}}
	s.sidechain.begin(dir, "parent", false)
	s.sendEventResult()
	result := <-s.events
	if !result.UsageIncomplete || result.InputTokens != 10 || result.OutputTokens != 20 {
		t.Fatalf("failed child read must retain available usage and mark incomplete: %+v", result)
	}
}

func TestLegacyBaselineWaitsForDatabaseRecovery(t *testing.T) {
	dir := t.TempDir()
	var meter opencodeSidechainMeter
	meter.begin(dir, "parent", true)
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range []string{
		`CREATE TABLE session(id TEXT PRIMARY KEY,parent_id TEXT)`,
		`CREATE TABLE message(id TEXT PRIMARY KEY,session_id TEXT,data TEXT)`,
		`INSERT INTO session VALUES('parent',NULL),('child','parent')`,
		`INSERT INTO message VALUES('old','child','{"role":"assistant","time":{"completed":1},"tokens":{"input":10,"output":20}}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if got := meter.collect(); got != nil || !meter.incomplete {
		t.Fatalf("recovery must exclude unbounded history and report the gap: %+v", got)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES('new','child','{"role":"assistant","time":{"completed":1},"tokens":{"input":3,"output":4}}')`); err != nil {
		t.Fatal(err)
	}
	if got := meter.collect(); got == nil || got.InputTokens != 3 || got.OutputTokens != 4 || meter.incomplete {
		t.Fatalf("recovered baseline lost new usage or rebilled history: %+v", got)
	}
}
