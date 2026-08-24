//go:build !no_tuitui

package main

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
)

func TestParseTuiTuiArgsPostOptions(t *testing.T) {
	opts, err := parseTuiTuiArgs([]string{
		"--channel", "chan-1",
		"--parent", "root-1",
		"--message", "hello",
		"--project", "bot",
	})
	if err != nil {
		t.Fatalf("parseTuiTuiArgs() error = %v", err)
	}
	if opts.channelID != "chan-1" {
		t.Fatalf("channelID = %q", opts.channelID)
	}
	if opts.parentID != "root-1" {
		t.Fatalf("parentID = %q", opts.parentID)
	}
	if opts.message != "hello" {
		t.Fatalf("message = %q", opts.message)
	}
	if opts.project != "bot" {
		t.Fatalf("project = %q", opts.project)
	}
}

func TestFindTuiTuiOptionsRejectsAmbiguousProjects(t *testing.T) {
	cfg := &config.Config{Projects: []config.ProjectConfig{
		{Name: "one", Platforms: []config.PlatformConfig{{Type: "tuitui", Options: map[string]any{"app_id": "one"}}}},
		{Name: "two", Platforms: []config.PlatformConfig{{Type: "tuitui", Options: map[string]any{"app_id": "two"}}}},
	}}
	_, _, err := findTuiTuiOptions(cfg, tuituiCLIOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple projects") {
		t.Fatalf("findTuiTuiOptions() error = %v", err)
	}
}
