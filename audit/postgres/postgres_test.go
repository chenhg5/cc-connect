package postgres

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/chenhg5/cc-connect/core"
)

// boolPtr keeps test configs concise when optional booleans are needed.
func boolPtr(v bool) *bool {
	return &v
}

// TestNewWithDBEnsureSchema verifies that auto-create runs the expected DDL.
func TestNewWithDBEnsureSchema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta(`CREATE TABLE IF NOT EXISTS "audit_records"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS "audit_records_project_ts_idx"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS "audit_records_session_ts_idx"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	for _, stmt := range auditCommentStatements("audit_records") {
		mock.ExpectExec(regexp.QuoteMeta(stmt)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	sink, err := newWithDB(db, Config{
		Table:      "audit_records",
		AutoCreate: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("newWithDB() error = %v", err)
	}
	if sink == nil {
		t.Fatal("newWithDB() returned nil sink")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestIndexStatements verifies that each index profile expands to the expected DDL.
func TestIndexStatements(t *testing.T) {
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
				`"audit_records_project_ts_idx"`,
				`"audit_records_session_ts_idx"`,
				`WHERE session_key <> ''`,
			},
			mustNotHave: []string{
				`inbound_msg_idx`,
				`outbound_msg_idx`,
				`root_ts_idx`,
				`reply_to_msg_idx`,
			},
		},
		{
			name:      "lookup",
			profile:   IndexProfileLookup,
			wantCount: 4,
			mustHave: []string{
				`"audit_records_inbound_msg_idx"`,
				`"audit_records_outbound_msg_idx"`,
				`WHERE inbound_message_id <> ''`,
				`WHERE outbound_message_id <> ''`,
			},
		},
		{
			name:      "full",
			profile:   IndexProfileFull,
			wantCount: 6,
			mustHave: []string{
				`"audit_records_root_ts_idx"`,
				`"audit_records_reply_to_msg_idx"`,
				`WHERE root_message_id <> ''`,
				`WHERE reply_to_message_id <> ''`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := indexStatements("audit_records", tt.profile)
			if len(stmts) != tt.wantCount {
				t.Fatalf("len(indexStatements()) = %d, want %d", len(stmts), tt.wantCount)
			}
			joined := strings.Join(stmts, "\n")
			for _, want := range tt.mustHave {
				if !strings.Contains(joined, want) {
					t.Fatalf("index statements missing %q:\n%s", want, joined)
				}
			}
			for _, forbidden := range tt.mustNotHave {
				if strings.Contains(joined, forbidden) {
					t.Fatalf("index statements unexpectedly contain %q:\n%s", forbidden, joined)
				}
			}
		})
	}
}

// TestSinkWriteInsertsAuditRecord verifies the shape of the INSERT payload.
func TestSinkWriteInsertsAuditRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	defer db.Close()

	sink, err := newWithDB(db, Config{
		Table:      "audit_records",
		AutoCreate: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("newWithDB() error = %v", err)
	}

	ts := time.Date(2026, 4, 23, 11, 22, 33, 0, time.FixedZone("UTC+8", 8*60*60))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO "audit_records" (`)).
		WithArgs(
			string(core.AuditKindOutboundSent),
			ts.UTC(),
			"proj",
			"feishu",
			"codex",
			"session-1",
			"user-1",
			"Alice",
			"Ops",
			"chat-1",
			"thread-1",
			"in-1",
			"parent-1",
			"root-1",
			"reply-1",
			"out-1",
			"hello",
			"[quoted]",
			"[quoted]\nhello",
			"agent says hi",
			"sent to platform",
			"",
			[]byte(`{"trace":"trace-1"}`),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

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
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
