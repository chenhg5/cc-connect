package mongodb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Config controls the MongoDB audit sink.
type Config struct {
	URI               string
	Database          string
	Collection        string
	AutoCreateIndexes *bool
}

// DefaultConfig returns a sensible MongoDB sink configuration.
func DefaultConfig() Config {
	return Config{
		Collection: "audit_records",
	}
}

type collectionAPI interface {
	InsertOne(ctx context.Context, document any) error
	CreateIndexes(ctx context.Context) error
}

type mongoCollection struct {
	collection *mongo.Collection
}

func (c *mongoCollection) InsertOne(ctx context.Context, document any) error {
	_, err := c.collection.InsertOne(ctx, document)
	if err != nil {
		return fmt.Errorf("mongodb audit sink: insert: %w", err)
	}
	return nil
}

func (c *mongoCollection) CreateIndexes(ctx context.Context) error {
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "project", Value: 1}, {Key: "timestamp", Value: -1}},
			Options: options.Index().SetName("project_timestamp_desc"),
		},
		{
			Keys:    bson.D{{Key: "session_key", Value: 1}, {Key: "timestamp", Value: -1}},
			Options: options.Index().SetName("session_timestamp_desc"),
		},
		{
			Keys:    bson.D{{Key: "inbound_message_id", Value: 1}},
			Options: options.Index().SetName("inbound_message_id"),
		},
		{
			Keys:    bson.D{{Key: "outbound_message_id", Value: 1}},
			Options: options.Index().SetName("outbound_message_id"),
		},
	}
	if _, err := c.collection.Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("mongodb audit sink: create indexes: %w", err)
	}
	return nil
}

// Sink writes audit records to MongoDB.
type Sink struct {
	client         *mongo.Client
	collection     collectionAPI
	database       string
	collectionName string
}

// New opens a MongoDB-backed audit sink using the official Go driver.
func New(cfg Config) (*Sink, error) {
	cfg = normalizeConfig(cfg)
	if strings.TrimSpace(cfg.URI) == "" {
		return nil, fmt.Errorf("mongodb audit sink: uri is required")
	}
	if strings.TrimSpace(cfg.Database) == "" {
		return nil, fmt.Errorf("mongodb audit sink: database is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(cfg.URI))
	if err != nil {
		return nil, fmt.Errorf("mongodb audit sink: connect: %w", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongodb audit sink: ping: %w", err)
	}

	collection := &mongoCollection{
		collection: client.Database(cfg.Database).Collection(cfg.Collection),
	}
	sink, err := newWithCollection(client, collection, cfg)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return sink, nil
}

func newWithCollection(client *mongo.Client, collection collectionAPI, cfg Config) (*Sink, error) {
	cfg = normalizeConfig(cfg)
	if strings.TrimSpace(cfg.Database) == "" {
		return nil, fmt.Errorf("mongodb audit sink: database is required")
	}

	sink := &Sink{
		client:         client,
		collection:     collection,
		database:       cfg.Database,
		collectionName: cfg.Collection,
	}
	if autoCreateIndexesEnabled(cfg.AutoCreateIndexes) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.collection.CreateIndexes(ctx); err != nil {
			return nil, err
		}
	}
	return sink, nil
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(cfg.Collection) == "" {
		cfg.Collection = defaults.Collection
	}
	if cfg.AutoCreateIndexes == nil {
		def := true
		cfg.AutoCreateIndexes = &def
	}
	return cfg
}

func autoCreateIndexesEnabled(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func (s *Sink) Name() string {
	return "mongodb"
}

func (s *Sink) Write(ctx context.Context, record *core.AuditRecord) error {
	if record == nil {
		return nil
	}
	doc := bson.M{
		"kind":                string(record.Kind),
		"timestamp":           record.Timestamp.UTC(),
		"project":             record.Project,
		"platform":            record.Platform,
		"agent":               record.Agent,
		"session_key":         record.SessionKey,
		"user_id":             record.UserID,
		"user_name":           record.UserName,
		"chat_name":           record.ChatName,
		"channel_key":         record.ChannelKey,
		"thread_id":           record.ThreadID,
		"inbound_message_id":  record.InboundMessageID,
		"parent_message_id":   record.ParentMessageID,
		"root_message_id":     record.RootMessageID,
		"reply_to_message_id": record.ReplyToMessageID,
		"outbound_message_id": record.OutboundMessageID,
		"content_original":    record.ContentOriginal,
		"extra_content":       record.ExtraContent,
		"content_to_agent":    record.ContentToAgent,
		"agent_output":        record.AgentOutput,
		"content_sent":        record.ContentSent,
		"error":               record.Error,
		"extra":               record.Extra,
	}
	return s.collection.InsertOne(ctx, doc)
}

func (s *Sink) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.client.Disconnect(ctx); err != nil {
		return fmt.Errorf("mongodb audit sink: disconnect: %w", err)
	}
	return nil
}
