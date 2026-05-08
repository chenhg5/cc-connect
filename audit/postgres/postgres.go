// Package postgres implements the PostgreSQL-backed audit sink.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IndexProfile controls how aggressively the sink indexes audit data.
type IndexProfile string

const (
	// IndexProfileMinimal keeps only the lowest-cost timeline indexes.
	IndexProfileMinimal IndexProfile = "minimal"
	// IndexProfileLookup adds exact message ID lookup indexes.
	IndexProfileLookup IndexProfile = "lookup"
	// IndexProfileFull adds thread-correlation indexes on top of lookup indexes.
	IndexProfileFull IndexProfile = "full"
)

// Config controls the PostgreSQL audit sink.
type Config struct {
	DSN             string
	Table           string
	AutoCreate      *bool
	IndexProfile    string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultConfig returns a sensible PostgreSQL sink configuration.
func DefaultConfig() Config {
	return Config{
		Table:           "audit_records",
		IndexProfile:    string(IndexProfileMinimal),
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

// Sink writes audit records to PostgreSQL.
type Sink struct {
	db           *sql.DB
	table        string
	indexProfile IndexProfile
}

// New opens a PostgreSQL-backed audit sink using the pgx stdlib driver.
func New(cfg Config) (*Sink, error) {
	cfg = normalizeConfig(cfg)
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, fmt.Errorf("postgres audit sink: dsn is required")
	}
	if err := validateIdentifier(cfg.Table); err != nil {
		return nil, fmt.Errorf("postgres audit sink: table: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres audit sink: open: %w", err)
	}
	sink, err := newWithDB(db, cfg)
	if err != nil {
		db.Close()
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres audit sink: ping: %w", err)
	}
	return sink, nil
}

// newWithDB wires the sink to an existing sql.DB and optionally bootstraps schema.
func newWithDB(db *sql.DB, cfg Config) (*Sink, error) {
	cfg = normalizeConfig(cfg)
	if err := validateIdentifier(cfg.Table); err != nil {
		return nil, fmt.Errorf("postgres audit sink: table: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	sink := &Sink{
		db:           db,
		table:        cfg.Table,
		indexProfile: normalizeIndexProfile(cfg.IndexProfile),
	}
	if autoCreateEnabled(cfg.AutoCreate) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.ensureSchema(ctx); err != nil {
			return nil, err
		}
	}
	return sink, nil
}

// normalizeConfig fills unset fields with PostgreSQL sink defaults.
func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Table) == "" {
		cfg.Table = defaults.Table
	}
	if strings.TrimSpace(cfg.IndexProfile) == "" {
		cfg.IndexProfile = defaults.IndexProfile
	}
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = defaults.MaxOpenConns
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = defaults.MaxIdleConns
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = defaults.ConnMaxLifetime
	}
	if cfg.AutoCreate == nil {
		def := true
		cfg.AutoCreate = &def
	}
	return cfg
}

// normalizeIndexProfile collapses empty or unknown values to the default profile.
func normalizeIndexProfile(value string) IndexProfile {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(IndexProfileLookup):
		return IndexProfileLookup
	case string(IndexProfileFull):
		return IndexProfileFull
	default:
		return IndexProfileMinimal
	}
}

// autoCreateEnabled resolves the optional auto-create flag with a default of true.
func autoCreateEnabled(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

// validateIdentifier rejects unsafe table and index identifiers.
func validateIdentifier(name string) error {
	if !identifierPattern.MatchString(strings.TrimSpace(name)) {
		return fmt.Errorf("%q contains invalid characters", name)
	}
	return nil
}

// ensureSchema creates the audit table and supporting indexes when enabled.
func (s *Sink) ensureSchema(ctx context.Context) error {
	table := quoteIdent(s.table)
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id BIGSERIAL PRIMARY KEY,
			kind TEXT NOT NULL,
			timestamp TIMESTAMPTZ NOT NULL,
			project TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			agent TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			user_name TEXT NOT NULL DEFAULT '',
			chat_name TEXT NOT NULL DEFAULT '',
			channel_key TEXT NOT NULL DEFAULT '',
			thread_id TEXT NOT NULL DEFAULT '',
			inbound_message_id TEXT NOT NULL DEFAULT '',
			parent_message_id TEXT NOT NULL DEFAULT '',
			root_message_id TEXT NOT NULL DEFAULT '',
			reply_to_message_id TEXT NOT NULL DEFAULT '',
			outbound_message_id TEXT NOT NULL DEFAULT '',
			content_original TEXT NOT NULL DEFAULT '',
			extra_content TEXT NOT NULL DEFAULT '',
			content_to_agent TEXT NOT NULL DEFAULT '',
			agent_output TEXT NOT NULL DEFAULT '',
			content_sent TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			extra JSONB NOT NULL DEFAULT '{}'::jsonb
		)`, table),
	}
	stmts = append(stmts, indexStatements(s.table, s.indexProfile)...)
	stmts = append(stmts, auditCommentStatements(s.table)...)
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres audit sink: ensure schema: %w", err)
		}
	}
	return nil
}

// indexStatements returns the managed index DDL for the configured profile.
func indexStatements(tableName string, profile IndexProfile) []string {
	table := quoteIdent(tableName)
	indexPrefix := tableName
	stmts := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (project, timestamp DESC)`, quoteIdent(indexPrefix+"_project_ts_idx"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (session_key, timestamp DESC) WHERE session_key <> ''`, quoteIdent(indexPrefix+"_session_ts_idx"), table),
	}

	if profile == IndexProfileLookup || profile == IndexProfileFull {
		stmts = append(stmts,
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (inbound_message_id) WHERE inbound_message_id <> ''`, quoteIdent(indexPrefix+"_inbound_msg_idx"), table),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (outbound_message_id) WHERE outbound_message_id <> ''`, quoteIdent(indexPrefix+"_outbound_msg_idx"), table),
		)
	}
	if profile == IndexProfileFull {
		stmts = append(stmts,
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (root_message_id, timestamp DESC) WHERE root_message_id <> ''`, quoteIdent(indexPrefix+"_root_ts_idx"), table),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (reply_to_message_id) WHERE reply_to_message_id <> ''`, quoteIdent(indexPrefix+"_reply_to_msg_idx"), table),
		)
	}
	return stmts
}

// quoteIdent quotes an identifier for interpolation into DDL statements.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral quotes a string literal for interpolation into COMMENT statements.
func quoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

// auditCommentStatements returns COMMENT statements for the audit table and columns.
func auditCommentStatements(tableName string) []string {
	table := quoteIdent(tableName)
	comments := []struct {
		column      string
		description string
	}{
		{column: "id", description: "Synthetic primary key for the audit row."},
		{column: "kind", description: "Lifecycle stage represented by the audit row."},
		{column: "timestamp", description: "UTC timestamp when the audit row was recorded."},
		{column: "project", description: "cc-connect project name that emitted the record."},
		{column: "platform", description: "Normalized platform identifier, such as feishu."},
		{column: "agent", description: "Normalized agent identifier, such as codex."},
		{column: "session_key", description: "Platform-scoped conversation key used by cc-connect."},
		{column: "user_id", description: "Platform-native user identifier captured for the turn."},
		{column: "user_name", description: "Best-effort human-readable user name."},
		{column: "chat_name", description: "Best-effort human-readable chat or room name."},
		{column: "channel_key", description: "Platform-specific channel or room identifier used for routing."},
		{column: "thread_id", description: "Platform-native thread or topic identifier when available."},
		{column: "inbound_message_id", description: "Incoming platform message identifier for the turn."},
		{column: "parent_message_id", description: "Direct parent or replied-to message identifier when available."},
		{column: "root_message_id", description: "Root message identifier for the enclosing thread when available."},
		{column: "reply_to_message_id", description: "Platform message identifier that outbound content replied to."},
		{column: "outbound_message_id", description: "Platform message identifier returned after delivery."},
		{column: "content_original", description: "User-authored text before cc-connect enrichment."},
		{column: "extra_content", description: "Platform-enriched prefix added before the user-authored text."},
		{column: "content_to_agent", description: "Final text payload sent to the agent."},
		{column: "agent_output", description: "Normalized final text returned by the agent."},
		{column: "content_sent", description: "Final text payload emitted back to the platform."},
		{column: "error", description: "Terminal delivery or processing error when one occurred."},
		{column: "extra", description: "Structured metadata that does not warrant first-class columns."},
	}

	stmts := []string{
		fmt.Sprintf("COMMENT ON TABLE %s IS %s", table, quoteLiteral("Normalized audit records emitted by cc-connect.")),
	}
	for _, comment := range comments {
		stmts = append(stmts,
			fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s", table, quoteIdent(comment.column), quoteLiteral(comment.description)),
		)
	}
	return stmts
}

// Name returns the stable sink identifier used in logs and diagnostics.
func (s *Sink) Name() string {
	return "postgres"
}

// Write persists a single normalized audit record into PostgreSQL.
func (s *Sink) Write(ctx context.Context, record *core.AuditRecord) error {
	if record == nil {
		return nil
	}
	extraJSON := []byte("{}")
	if len(record.Extra) > 0 {
		data, err := json.Marshal(record.Extra)
		if err != nil {
			return fmt.Errorf("postgres audit sink: marshal extra: %w", err)
		}
		extraJSON = data
	}

	query := fmt.Sprintf(`INSERT INTO %s (
		kind, timestamp, project, platform, agent, session_key, user_id, user_name,
		chat_name, channel_key, thread_id, inbound_message_id, parent_message_id,
		root_message_id, reply_to_message_id, outbound_message_id, content_original,
		extra_content, content_to_agent, agent_output, content_sent, error, extra
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8,
		$9, $10, $11, $12, $13,
		$14, $15, $16, $17,
		$18, $19, $20, $21, $22, $23
	)`, quoteIdent(s.table))

	_, err := s.db.ExecContext(ctx, query,
		string(record.Kind),
		record.Timestamp.UTC(),
		record.Project,
		record.Platform,
		record.Agent,
		record.SessionKey,
		record.UserID,
		record.UserName,
		record.ChatName,
		record.ChannelKey,
		record.ThreadID,
		record.InboundMessageID,
		record.ParentMessageID,
		record.RootMessageID,
		record.ReplyToMessageID,
		record.OutboundMessageID,
		record.ContentOriginal,
		record.ExtraContent,
		record.ContentToAgent,
		record.AgentOutput,
		record.ContentSent,
		record.Error,
		extraJSON,
	)
	if err != nil {
		return fmt.Errorf("postgres audit sink: insert: %w", err)
	}
	return nil
}

// Close releases the underlying database handle.
func (s *Sink) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
