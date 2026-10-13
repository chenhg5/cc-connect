package feishu

import (
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestBotCreation_PrivateHumanOnly(t *testing.T) {
	p := &Platform{}
	for _, tt := range []struct {
		chat, sender string
		want         bool
	}{
		{"p2p", "user", true}, {"group", "user", false}, {"p2p", "app", false}, {"p2p", "bot", false}, {"p2p", "", false}, {"", "user", false},
	} {
		if got := p.IsPrivateUserMessage(&core.Message{ReplyCtx: replyContext{chatType: tt.chat, senderType: tt.sender}}); got != tt.want {
			t.Errorf("%s/%s=%v", tt.chat, tt.sender, got)
		}
	}
	if p.IsPrivateUserMessage(&core.Message{ReplyCtx: "p2p:user"}) {
		t.Fatal("trusted arbitrary context")
	}
}
