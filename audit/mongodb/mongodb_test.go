package mongodb

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func boolPtr(v bool) *bool {
	return &v
}

type fakeCollection struct {
	indexCreated bool
	insertedDoc  any
}

func (c *fakeCollection) InsertOne(_ context.Context, document any) error {
	c.insertedDoc = document
	return nil
}

func (c *fakeCollection) CreateIndexes(_ context.Context) error {
	c.indexCreated = true
	return nil
}

func TestNewWithCollectionCreatesIndexes(t *testing.T) {
	coll := &fakeCollection{}
	sink, err := newWithCollection(nil, coll, Config{
		Database:          "cc_connect",
		Collection:        "audit_records",
		AutoCreateIndexes: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("newWithCollection() error = %v", err)
	}
	if sink == nil {
		t.Fatal("newWithCollection() returned nil sink")
	}
	if !coll.indexCreated {
		t.Fatal("expected indexes to be created")
	}
}

func TestSinkWriteInsertsAuditRecord(t *testing.T) {
	coll := &fakeCollection{}
	sink, err := newWithCollection(nil, coll, Config{
		Database:          "cc_connect",
		Collection:        "audit_records",
		AutoCreateIndexes: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("newWithCollection() error = %v", err)
	}

	ts := time.Date(2026, 4, 23, 11, 22, 33, 0, time.FixedZone("UTC+8", 8*60*60))
	err = sink.Write(context.Background(), &core.AuditRecord{
		Kind:              core.AuditKindOutboundSent,
		Timestamp:         ts,
		Project:           "proj",
		Platform:          "feishu",
		Agent:             "codex",
		SessionKey:        "session-1",
		UserID:            "user-1",
		UserName:          "Alice",
		ChatName:          "Ops",
		ChannelKey:        "chat-1",
		ThreadID:          "thread-1",
		InboundMessageID:  "in-1",
		ParentMessageID:   "parent-1",
		RootMessageID:     "root-1",
		ReplyToMessageID:  "reply-1",
		OutboundMessageID: "out-1",
		ContentOriginal:   "hello",
		ExtraContent:      "[quoted]",
		ContentToAgent:    "[quoted]\nhello",
		AgentOutput:       "agent says hi",
		ContentSent:       "sent to platform",
		Extra: map[string]any{
			"trace": "trace-1",
		},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	doc, ok := coll.insertedDoc.(bson.M)
	if !ok {
		t.Fatalf("inserted doc type = %T, want bson.M", coll.insertedDoc)
	}
	if got := doc["kind"]; got != string(core.AuditKindOutboundSent) {
		t.Fatalf("kind = %#v, want %q", got, core.AuditKindOutboundSent)
	}
	if got := doc["project"]; got != "proj" {
		t.Fatalf("project = %#v, want %q", got, "proj")
	}
	if got := doc["outbound_message_id"]; got != "out-1" {
		t.Fatalf("outbound_message_id = %#v, want %q", got, "out-1")
	}
	extra, ok := doc["extra"].(map[string]any)
	if !ok {
		t.Fatalf("extra type = %T, want map[string]any", doc["extra"])
	}
	if got := extra["trace"]; got != "trace-1" {
		t.Fatalf("extra.trace = %#v, want %q", got, "trace-1")
	}
}
