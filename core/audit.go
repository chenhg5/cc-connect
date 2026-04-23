package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// AuditRecordKind identifies the type of lifecycle record written to an audit sink.
type AuditRecordKind string

const (
	AuditKindInboundReceived AuditRecordKind = "inbound_received"
	AuditKindAgentResult     AuditRecordKind = "agent_result"
	AuditKindOutboundSent    AuditRecordKind = "outbound_sent"
	AuditKindOutboundFailed  AuditRecordKind = "outbound_failed"
)

// AuditRecord is the normalized audit payload shared across all sinks.
type AuditRecord struct {
	Kind              AuditRecordKind `json:"kind"`
	Timestamp         time.Time       `json:"timestamp"`
	Project           string          `json:"project"`
	Platform          string          `json:"platform,omitempty"`
	Agent             string          `json:"agent,omitempty"`
	SessionKey        string          `json:"session_key,omitempty"`
	UserID            string          `json:"user_id,omitempty"`
	UserName          string          `json:"user_name,omitempty"`
	ChatName          string          `json:"chat_name,omitempty"`
	ChannelKey        string          `json:"channel_key,omitempty"`
	ThreadID          string          `json:"thread_id,omitempty"`
	InboundMessageID  string          `json:"inbound_message_id,omitempty"`
	ParentMessageID   string          `json:"parent_message_id,omitempty"`
	RootMessageID     string          `json:"root_message_id,omitempty"`
	ReplyToMessageID  string          `json:"reply_to_message_id,omitempty"`
	OutboundMessageID string          `json:"outbound_message_id,omitempty"`
	ContentOriginal   string          `json:"content_original,omitempty"`
	ExtraContent      string          `json:"extra_content,omitempty"`
	ContentToAgent    string          `json:"content_to_agent,omitempty"`
	AgentOutput       string          `json:"agent_output,omitempty"`
	ContentSent       string          `json:"content_sent,omitempty"`
	Error             string          `json:"error,omitempty"`
	Extra             map[string]any  `json:"extra,omitempty"`
}

// AuditSink stores audit records in a concrete backend such as PostgreSQL.
type AuditSink interface {
	Name() string
	Write(ctx context.Context, record *AuditRecord) error
	Close() error
}

// AuditReplyMetadata is optional platform-provided metadata derived from replyCtx.
type AuditReplyMetadata struct {
	SessionKey       string
	UserID           string
	UserName         string
	ChatName         string
	ChannelKey       string
	ReplyToMessageID string
	ParentMessageID  string
	RootMessageID    string
	ThreadID         string
	Extra            map[string]any
}

// SendReceipt captures platform-specific delivery details for a sent message.
type SendReceipt struct {
	MessageID       string
	ParentMessageID string
	RootMessageID   string
	ThreadID        string
	Extra           map[string]any
}

// AuditReplyMetadataProvider is an optional platform capability for exposing
// audit metadata derived from the reply context.
type AuditReplyMetadataProvider interface {
	AuditReplyMetadata(replyCtx any) AuditReplyMetadata
}

// DeliveryReporter is an optional platform capability that returns a receipt
// for delivered messages while preserving the existing Send/Reply behavior.
type DeliveryReporter interface {
	SendWithReceipt(ctx context.Context, replyCtx any, content string) (*SendReceipt, error)
	ReplyWithReceipt(ctx context.Context, replyCtx any, content string) (*SendReceipt, error)
}

// PreviewReceiptProvider is an optional platform capability for translating a
// preview handle into a delivery receipt when a streamed preview becomes the
// final visible response.
type PreviewReceiptProvider interface {
	PreviewReceipt(previewHandle any) *SendReceipt
}

// Auditor fans out audit records to one or more sinks with a per-record timeout.
type Auditor struct {
	timeout time.Duration
	sinks   []AuditSink
	mu      sync.Mutex
	closed  bool
}

// NewAuditor constructs an auditor for the provided sinks. Nil sinks are ignored.
func NewAuditor(timeout time.Duration, sinks ...AuditSink) *Auditor {
	filtered := make([]AuditSink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &Auditor{timeout: timeout, sinks: filtered}
}

// Record writes an audit record to every configured sink. Errors are logged and
// do not interrupt engine execution.
func (a *Auditor) Record(parent context.Context, record AuditRecord) {
	if a == nil {
		return
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	sinks := append([]AuditSink(nil), a.sinks...)
	a.mu.Unlock()

	record = cloneAuditRecord(record)
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	} else {
		record.Timestamp = record.Timestamp.UTC()
	}

	for _, sink := range sinks {
		ctx := parent
		if ctx == nil {
			ctx = context.Background()
		}
		var cancel context.CancelFunc
		if a.timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, a.timeout)
		}
		err := sink.Write(ctx, &record)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			slog.Warn("audit sink write failed",
				"sink", sink.Name(),
				"kind", record.Kind,
				"project", record.Project,
				"platform", record.Platform,
				"error", err,
			)
		}
	}
}

// Close releases sink resources. Close is idempotent.
func (a *Auditor) Close() error {
	if a == nil {
		return nil
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	sinks := append([]AuditSink(nil), a.sinks...)
	a.mu.Unlock()

	var errs []error
	for _, sink := range sinks {
		if err := sink.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sink.Name(), err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func cloneAuditRecord(record AuditRecord) AuditRecord {
	record.Extra = CloneAuditExtra(record.Extra)
	return record
}

// CloneAuditExtra makes a shallow copy of an audit extra map.
func CloneAuditExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// MergeAuditExtra merges audit extra maps left-to-right. Later values override earlier ones.
func MergeAuditExtra(extras ...map[string]any) map[string]any {
	var merged map[string]any
	for _, extra := range extras {
		if len(extra) == 0 {
			continue
		}
		if merged == nil {
			merged = make(map[string]any, len(extra))
		}
		for k, v := range extra {
			merged[k] = v
		}
	}
	return merged
}
