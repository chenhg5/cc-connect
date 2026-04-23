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

// Config controls the PostgreSQL audit sink.
type Config struct {
	DSN             string
	Table           string
	AutoCreate      *bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultConfig returns a sensible PostgreSQL sink configuration.
func DefaultConfig() Config {
	return Config{
		Table:           "audit_records",
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

// Sink writes audit records to PostgreSQL.
type Sink struct {
	db    *sql.DB
	table string
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

func newWithDB(db *sql.DB, cfg Config) (*Sink, error) {
	cfg = normalizeConfig(cfg)
	if err := validateIdentifier(cfg.Table); err != nil {
		return nil, fmt.Errorf("postgres audit sink: table: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	sink := &Sink{db: db, table: cfg.Table}
	if autoCreateEnabled(cfg.AutoCreate) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.ensureSchema(ctx); err != nil {
			return nil, err
		}
	}
	return sink, nil
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Table) == "" {
		cfg.Table = defaults.Table
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

func autoCreateEnabled(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func validateIdentifier(name string) error {
	if !identifierPattern.MatchString(strings.TrimSpace(name)) {
		return fmt.Errorf("%q contains invalid characters", name)
	}
	return nil
}

func (s *Sink) ensureSchema(ctx context.Context) error {
	table := quoteIdent(s.table)
	indexPrefix := s.table
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
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (project, timestamp DESC)`, quoteIdent(indexPrefix+"_project_ts_idx"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (session_key, timestamp DESC)`, quoteIdent(indexPrefix+"_session_ts_idx"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (inbound_message_id)`, quoteIdent(indexPrefix+"_inbound_msg_idx"), table),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (outbound_message_id)`, quoteIdent(indexPrefix+"_outbound_msg_idx"), table),
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres audit sink: ensure schema: %w", err)
		}
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (s *Sink) Name() string {
	return "postgres"
}

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

func (s *Sink) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
