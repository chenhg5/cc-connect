package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	alioss "github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// OSSConfig holds configuration for Alibaba Cloud OSS image upload.
type OSSConfig struct {
	Enabled           bool   `toml:"enabled"`
	Endpoint          string `toml:"endpoint"`
	AccessKeyID       string `toml:"access_key_id"`
	AccessKeySecret   string `toml:"access_key_secret"`
	Bucket            string `toml:"bucket"`
	URLPrefix         string `toml:"url_prefix"`          // public URL prefix, e.g. https://bucket.oss-cn-guangzhou.aliyuncs.com
	DeleteAfterUpload bool   `toml:"delete_after_upload"` // delete local file after successful upload
}

// OSSService handles uploading images to Alibaba Cloud OSS.
type OSSService struct {
	Config OSSConfig
	client *alioss.Client
	bucket *alioss.Bucket
}

// NewOSSService creates a new OSSService. Returns (nil, nil) if OSS is disabled.
func NewOSSService(cfg OSSConfig) (*OSSService, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	client, err := alioss.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create OSS client: %w", err)
	}

	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("failed to get OSS bucket: %w", err)
	}

	return &OSSService{
		Config: cfg,
		client: client,
		bucket: bucket,
	}, nil
}

// Upload uploads an image to OSS and returns the public URL.
func (s *OSSService) Upload(ctx context.Context, img ImageAttachment) (string, error) {
	if s == nil || s.bucket == nil {
		return "", fmt.Errorf("OSS service not initialized")
	}

	// Generate object key: images/YYYY-MM-DD/filename
	now := time.Now()
	objectKey := fmt.Sprintf("images/%s/%s", now.Format("2006-01-02"), img.FileName)

	// Upload
	err := s.bucket.PutObject(objectKey, bytes.NewReader(img.Data))
	if err != nil {
		return "", fmt.Errorf("OSS upload failed: %w", err)
	}

	// Build public URL
	url := fmt.Sprintf("%s/%s", strings.TrimRight(s.Config.URLPrefix, "/"), objectKey)

	return url, nil
}

// CleanupLocal removes the local file. Only call after confirmed successful OSS upload.
func (s *OSSService) CleanupLocal(localPath string) error {
	if localPath == "" {
		return nil
	}
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove local file %s: %w", localPath, err)
	}
	slog.Info("cleaned up local image after OSS upload", "path", localPath)

	// Remove the parent directory (thread_id level) if it's now empty.
	dir := filepath.Dir(localPath)
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		os.Remove(dir) // best-effort; ignore errors
	}

	return nil
}
