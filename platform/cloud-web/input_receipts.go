package cloudweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Only correlation and delivery status are stored here, never prompt/file data.
// A receipt is removed after the gateway acknowledges its idempotent write.
type inputReceipts struct {
	db   *sql.DB
	wake chan struct{}
	done chan struct{}
}

func openInputReceipts(path string) (*inputReceipts, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA synchronous=FULL",
		"CREATE TABLE IF NOT EXISTS receipts(session TEXT NOT NULL,message TEXT NOT NULL,wire TEXT NOT NULL,pending INTEGER NOT NULL,PRIMARY KEY(session,message))",
		// The old process cannot prove that its in-memory queue was executed.
		"UPDATE receipts SET pending=0 WHERE pending=1",
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &inputReceipts{db: db, wake: make(chan struct{}, 1), done: make(chan struct{})}, nil
}

func (q *inputReceipts) put(rc replyContext, wire map[string]any, pending bool) error {
	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	result, err := q.db.Exec(`INSERT INTO receipts VALUES(?,?,?,?) ON CONFLICT(session,message)
DO UPDATE SET wire=excluded.wire,pending=excluded.pending
WHERE receipts.pending=1 OR receipts.wire=excluded.wire`, rc.SessionKey, rc.ReplyCtx, string(encoded), pending)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("input receipt identity changed")
	}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

func (q *inputReceipts) started(rc replyContext) error {
	_, err := q.db.Exec("DELETE FROM receipts WHERE session=? AND message=? AND pending=1", rc.SessionKey, rc.ReplyCtx)
	return err
}

func (p *Platform) flushInputReceipts(ctx context.Context) error {
	q := p.inputReceipts
	rows, err := q.db.Query("SELECT session,message,wire FROM receipts WHERE pending=0 ORDER BY rowid")
	if err != nil {
		return err
	}
	type receipt struct{ session, message, wire string }
	var pending []receipt
	for rows.Next() {
		var r receipt
		if err := rows.Scan(&r.session, &r.message, &r.wire); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failure error
	for _, r := range pending {
		var wire map[string]any
		if err := json.Unmarshal([]byte(r.wire), &wire); err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := p.sendWire(attempt, wire)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			failure = err
			continue
		}
		if _, err := q.db.Exec("DELETE FROM receipts WHERE session=? AND message=? AND wire=? AND pending=0", r.session, r.message, r.wire); err != nil {
			return err
		}
	}
	return failure
}

func (p *Platform) runInputReceipts(ctx context.Context) {
	defer close(p.inputReceipts.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	failed := false
	for {
		if p.hasCap("input_receipts_v1") {
			err := p.flushInputReceipts(ctx)
			if err != nil && !failed && ctx.Err() == nil {
				slog.Warn("cloud_web: input receipts retained for retry")
			}
			failed = err != nil
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.inputReceipts.wake:
		}
	}
}

func (p *Platform) inputReceipt(_ context.Context, reply any, kind, target, reason string, pending bool) {
	if !p.hasCap("input_receipts_v1") {
		return
	}
	rc, err := parseReplyCtx(reply)
	if err != nil {
		return
	}
	if p.inputReceipts == nil {
		slog.Error("cloud_web: input receipt store unavailable")
		return
	}
	wire := map[string]any{"type": kind, "session_key": rc.SessionKey, "reply_ctx": rc.ReplyCtx}
	if target != "" {
		wire["target_message_id"] = target
	}
	if reason != "" {
		wire["reason"] = reason
	}
	if err := p.inputReceipts.put(rc, wire, pending); err != nil {
		slog.Error("cloud_web: cannot persist input receipt")
	}
}

func (p *Platform) ReportDropped(ctx context.Context, reply any, reason string) {
	p.inputReceipt(ctx, reply, "input_dropped", "", reason, false)
}
func (p *Platform) ReportInputQueued(ctx context.Context, reply any) {
	p.inputReceipt(ctx, reply, "input_uncertain", "", "interrupted", true)
}
func (p *Platform) ReportInputStarted(_ context.Context, reply any) {
	if p.inputReceipts == nil {
		return
	}
	rc, err := parseReplyCtx(reply)
	if err != nil {
		return
	}
	if err := p.inputReceipts.started(rc); err != nil {
		slog.Error("cloud_web: cannot retire queued input marker")
	}
}

func inputReceiptPath(dir, project, name string) string {
	digest := sha256.Sum256([]byte(project + "\n" + name))
	return filepath.Join(dir, "cloud-web-inputs", hex.EncodeToString(digest[:])+".sqlite")
}
