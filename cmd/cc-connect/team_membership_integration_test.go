package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
)

// Opt-in read-only check against an already configured application. It never
// creates a bot, invites members, changes project state, or starts a listener.
func TestTeamCoordination_LiveRoleMembership(t *testing.T) {
	path := os.Getenv("CC_MEMBERSHIP_CHECK_CONFIG")
	bot, chat := os.Getenv("CC_MEMBERSHIP_CHECK_BOT"), os.Getenv("CC_MEMBERSHIP_CHECK_CHAT")
	if path == "" || bot == "" || chat == "" {
		t.Skip("opt-in membership diagnostic")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, pc, err := teamBotPlatform(cfg, bot, "feishu")
	if err != nil {
		t.Fatal(err)
	}
	app, _ := pc.Options["app_id"].(string)
	if app == "" {
		t.Fatal("application identity missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s := newTeamProjectService(path, filepath.Dir(path))
	if err := s.confirmRoleMembership(ctx, pc, chat, app); err != nil {
		t.Fatal(err)
	}
	t.Log("existing role confirmed in group using its own application identity")
}
