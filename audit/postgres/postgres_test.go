package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/chenhg5/cc-connect/core"
)

func boolPtr(v bool) *bool {
	return &v
}

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
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS "audit_records_inbound_msg_idx"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`CREATE INDEX IF NOT EXISTS "audit_records_outbound_msg_idx"`)).
		WillReturnResult(sqlmock.NewResult(0, 0))

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
