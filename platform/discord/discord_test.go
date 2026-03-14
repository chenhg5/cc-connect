package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

type fakeThreadOps struct {
	resolveChannel func(channelID string) (*discordgo.Channel, error)
	startThread    func(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error)
	joinThread     func(threadID string) error
}

func (f fakeThreadOps) ResolveChannel(channelID string) (*discordgo.Channel, error) {
	if f.resolveChannel == nil {
		return nil, nil
	}
	return f.resolveChannel(channelID)
}

func (f fakeThreadOps) StartThread(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error) {
	if f.startThread == nil {
		return nil, nil
	}
	return f.startThread(channelID, messageID, name, archiveDuration)
}

func (f fakeThreadOps) JoinThread(threadID string) error {
	if f.joinThread == nil {
		return nil
	}
	return f.joinThread(threadID)
}

func TestResolveThreadReplyContext_UsesExistingThreadChannel(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPublicThread}, nil
		},
	}

	joinedThread := ""
	ops.joinThread = func(threadID string) error {
		joinedThread = threadID
		return nil
	}

	msg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "m1",
			ChannelID: "thread-1",
			GuildID:   "guild-1",
			Author:    &discordgo.User{ID: "u1", Username: "jun"},
		},
	}

	sessionKey, rc, err := resolveThreadReplyContext(msg, "bot-1", ops)
	if err != nil {
		t.Fatalf("resolveThreadReplyContext() error = %v", err)
	}
	if sessionKey != "discord:thread-1" {
		t.Fatalf("sessionKey = %q, want discord:thread-1", sessionKey)
	}
	if rc.channelID != "thread-1" || rc.threadID != "thread-1" {
		t.Fatalf("replyContext = %#v, want thread channel routing", rc)
	}
	if joinedThread != "thread-1" {
		t.Fatalf("joinedThread = %q, want thread-1", joinedThread)
	}
}

func TestResolveThreadReplyContext_CreatesThreadForGuildMessage(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildText}, nil
		},
	}

	var (
		startChannelID string
		startMessageID string
		startName      string
		joinedThread   string
	)
	ops.startThread = func(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error) {
		startChannelID = channelID
		startMessageID = messageID
		startName = name
		if archiveDuration != 1440 {
			t.Fatalf("archiveDuration = %d, want 1440", archiveDuration)
		}
		return &discordgo.Channel{ID: "thread-99", Type: discordgo.ChannelTypeGuildPublicThread}, nil
	}
	ops.joinThread = func(threadID string) error {
		joinedThread = threadID
		return nil
	}

	msg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-42",
			ChannelID: "channel-1",
			GuildID:   "guild-1",
			Content:   "<@bot-1> investigate build failure",
			Author:    &discordgo.User{ID: "u1", Username: "jun"},
		},
	}

	sessionKey, rc, err := resolveThreadReplyContext(msg, "bot-1", ops)
	if err != nil {
		t.Fatalf("resolveThreadReplyContext() error = %v", err)
	}
	if sessionKey != "discord:thread-99" {
		t.Fatalf("sessionKey = %q, want discord:thread-99", sessionKey)
	}
	if rc.channelID != "thread-99" || rc.threadID != "thread-99" {
		t.Fatalf("replyContext = %#v, want thread channel routing", rc)
	}
	if startChannelID != "channel-1" || startMessageID != "msg-42" {
		t.Fatalf("thread start args = (%q, %q), want (channel-1, msg-42)", startChannelID, startMessageID)
	}
	if startName != "investigate build failure" {
		t.Fatalf("thread name = %q, want sanitized content", startName)
	}
	if joinedThread != "thread-99" {
		t.Fatalf("joinedThread = %q, want thread-99", joinedThread)
	}
}

func TestSessionKeyForChannel_UsesThreadKeyWhenChannelIsThread(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPrivateThread}, nil
		},
	}

	if got := resolveSessionKeyForChannel("thread-7", "user-1", false, true, ops); got != "discord:thread-7" {
		t.Fatalf("resolveSessionKeyForChannel() = %q, want discord:thread-7", got)
	}
}

func TestReconstructReplyCtx_ThreadSessionKey(t *testing.T) {
	p := &Platform{}

	rctx, err := p.ReconstructReplyCtx("discord:thread-7")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.channelID != "thread-7" || rc.threadID != "thread-7" {
		t.Fatalf("replyContext = %#v, want thread reply context", rc)
	}
}
