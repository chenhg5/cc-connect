package feishu

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Full records are content-addressed private blobs. The small card index is
// bounded independently of output size and never embeds logs in callback data.
func (p *Platform) storeExecutionRecord(b core.ExecutionBlock) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	path := filepath.Join(p.queryCardStateDir, "records", hash+".json")
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return hash, writePrivateJSON(path, b)
}
func (p *Platform) readExecutionRecord(hash string) (core.ExecutionBlock, error) {
	var b core.ExecutionBlock
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		return b, fmt.Errorf("invalid execution record reference")
	}
	data, err := os.ReadFile(filepath.Join(p.queryCardStateDir, "records", hash+".json"))
	if err != nil {
		return b, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		return b, fmt.Errorf("execution record checksum mismatch")
	}
	err = json.Unmarshal(data, &b)
	return b, err
}
func (c *queryCard) archiveExecutionSnapshot(s *queryCardSnapshot) error {
	if !c.native {
		return nil
	}
	s.RecordRefs = map[string]string{}
	for key, b := range c.records {
		ref, err := c.p.storeExecutionRecord(b)
		if err != nil {
			return err
		}
		s.RecordRefs[key] = ref
	}
	// Upgrade old cards or empty streams safely.
	for _, b := range c.blocks {
		if _, ok := s.RecordRefs[b.Key]; !ok {
			ref, err := c.p.storeExecutionRecord(b)
			if err != nil {
				return err
			}
			s.RecordRefs[b.Key] = ref
		}
	}
	s.Blocks = make([]core.ExecutionBlock, len(c.blocks))
	for i, b := range c.blocks {
		s.Blocks[i] = core.ExecutionBlock{Key: b.Key}
	}
	s.View = c.view
	if c.view.Key != "" {
		ref, err := c.p.storeExecutionRecord(c.view.Snapshot)
		if err != nil {
			return err
		}
		s.ViewRef = ref
		s.View.Snapshot = core.ExecutionBlock{}
	}
	return nil
}
func (p *Platform) restoreExecutionSnapshot(s *queryCardSnapshot, c *queryCard) error {
	if !c.native {
		return nil
	}
	c.records = map[string]core.ExecutionBlock{}
	c.view = s.View
	if s.RecordRefs == nil {
		c.blocks = executionKeys(c.blocks)
		c.syncExecutionRecords(c.blocks)
		return nil
	}
	for key, ref := range s.RecordRefs {
		b, err := p.readExecutionRecord(ref)
		if err != nil {
			return err
		}
		if b.Key != key {
			return fmt.Errorf("execution record identity mismatch")
		}
		c.records[key] = b
	}
	for i, b := range c.blocks {
		record, ok := c.records[b.Key]
		if !ok {
			return fmt.Errorf("execution record unavailable")
		}
		c.blocks[i] = record
	}
	if s.ViewRef != "" {
		b, err := p.readExecutionRecord(s.ViewRef)
		if err != nil {
			return err
		}
		c.view.Snapshot = b
	}
	return nil
}
