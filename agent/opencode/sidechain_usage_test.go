package opencode

import (
	"database/sql"
	"path/filepath"
	"testing"
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
		 ('one','child','{"role":"assistant","time":{"completed":1},"tokens":{"input":10,"output":20,"cache":{"read":30,"write":40}}}'),
		 ('two','nested','{"role":"assistant","time":{"completed":1},"tokens":{"input":5,"output":6}}'),
		 ('provisional','nested','{"role":"assistant","time":{},"tokens":{"input":999}}'),
		 ('other','foreign','{"role":"assistant","time":{"completed":1},"tokens":{"input":999}}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var meter opencodeSidechainMeter
	meter.begin(dir, "parent", false)
	got := meter.collect()
	if got == nil || got.InputTokens != 15 || got.OutputTokens != 26 || got.CacheReadInputTokens != 30 || got.CacheCreationInputTokens != 40 {
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
