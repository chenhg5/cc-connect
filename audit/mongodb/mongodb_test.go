package mongodb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// boolPtr keeps test configs concise when optional booleans are needed.
func boolPtr(v bool) *bool {
	return &v
}

// fakeCollection records sink operations without requiring a live MongoDB server.
type fakeCollection struct {
	indexCreated bool
	indexProfile IndexProfile
	insertedDoc  any
}

// InsertOne captures the inserted document for assertions.
func (c *fakeCollection) InsertOne(_ context.Context, document any) error {
	c.insertedDoc = document
	return nil
}

// CreateIndexes records that index creation was requested.
func (c *fakeCollection) CreateIndexes(_ context.Context, profile IndexProfile) error {
	c.indexCreated = true
	c.indexProfile = profile
	return nil
}

// TestNewWithCollectionCreatesIndexes verifies that index creation is enabled by default.
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
	if coll.indexProfile != IndexProfileMinimal {
		t.Fatalf("index profile = %q, want %q", coll.indexProfile, IndexProfileMinimal)
	}
}

// TestSinkWriteInsertsAuditRecord verifies the shape of the inserted audit document.
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

// TestIndexModels verifies that each index profile expands to the expected models.
func TestIndexModels(t *testing.T) {
	tests := []struct {
		name        string
		profile     IndexProfile
		wantCount   int
		mustHave    []string
		mustNotHave []string
	}{
		{
			name:      "minimal",
			profile:   IndexProfileMinimal,
			wantCount: 2,
			mustHave: []string{
				"project_timestamp_desc",
				"session_timestamp_desc",
			},
			mustNotHave: []string{
				"inbound_message_id",
				"outbound_message_id",
				"root_message_id_timestamp_desc",
				"reply_to_message_id",
			},
		},
		{
			name:      "lookup",
			profile:   IndexProfileLookup,
			wantCount: 4,
			mustHave: []string{
				"inbound_message_id",
				"outbound_message_id",
			},
		},
		{
			name:      "full",
			profile:   IndexProfileFull,
			wantCount: 6,
			mustHave: []string{
				"root_message_id_timestamp_desc",
				"reply_to_message_id",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := indexModels(tt.profile)
			if len(models) != tt.wantCount {
				t.Fatalf("len(indexModels()) = %d, want %d", len(models), tt.wantCount)
			}

			names := make([]string, 0, len(models))
			for _, model := range models {
				resolved := options.IndexOptions{}
				for _, apply := range model.Options.List() {
					if err := apply(&resolved); err != nil {
						t.Fatalf("apply index option error = %v", err)
					}
				}
				if resolved.Name == nil {
					t.Fatalf("index options missing name: %#v", resolved)
				}
				names = append(names, *resolved.Name)
			}
			joined := strings.Join(names, "\n")
			for _, want := range tt.mustHave {
				if !strings.Contains(joined, want) {
					t.Fatalf("index models missing %q:\n%s", want, joined)
				}
			}
			for _, forbidden := range tt.mustNotHave {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("index models unexpectedly contain %q:\n%s", forbidden, joined)
				}
			}
		})
	}
}
