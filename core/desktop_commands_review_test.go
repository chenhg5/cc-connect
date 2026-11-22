package core

import (
	"context"
	"strings"
	"testing"
)

func TestDesktopCommandsRequireAdmin(t *testing.T) {
	for _, command := range []string{"/reply thread text", "/answer thread request text"} {
		env := newCUJEnv(t)
		env.engine.agent = &desktopCommandAgent{}
		for _, admins := range []string{"", "alice"} {
			env.engine.SetAdminFrom(admins)
			env.userSends("bob", command)
			sent := env.plat.getSent()
			if !strings.Contains(sent[len(sent)-1], "admin_from") {
				t.Fatal("desktop mutation bypassed admin authorization", sent)
			}
		}
		if env.engine.workspacePool != nil {
			t.Fatal("unauthorized command created a workspace")
		}
	}
}

func TestDesktopCommandsRejectAttachments(t *testing.T) {
	for _, media := range []string{"image", "file", "location"} {
		t.Run(media, func(t *testing.T) {
			env := newCUJEnv(t)
			a := &desktopCommandAgent{}
			env.engine.agent = a
			RegisterAgent("stub", func(map[string]any) (Agent, error) { return a, nil })
			env.engine.SetAdminFrom("alice")
			if err := env.engine.registerThreadNotification("test:alice", t.TempDir(), "thread", 0); err != nil {
				t.Fatal(err)
			}
			msg := &Message{SessionKey: "test:alice", UserID: "alice", Content: "/reply thread text", ReplyCtx: "ctx"}
			switch media {
			case "image":
				msg.Images = []ImageAttachment{{}}
			case "file":
				msg.Files = []FileAttachment{{}}
			case "location":
				msg.Location = &LocationAttachment{}
			}
			env.engine.ReceiveMessage(env.plat, msg)
			sent := env.plat.getSent()
			if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "text only") {
				t.Fatal("attachment was not explicitly rejected", sent)
			}
			if a.input != "" || env.engine.workspacePool != nil {
				t.Fatal("attachment command reached the desktop helper")
			}
		})
	}
}

type desktopRejectingAgent struct {
	desktopCommandAgent
	reject bool
}

func (a *desktopRejectingAgent) ValidateDesktopThreadAt(id, directory string) error {
	if a.reject {
		return context.Canceled
	}
	return a.desktopCommandAgent.ValidateDesktopThreadAt(id, directory)
}

func TestDesktopCommandsPreflightBeforeCreatingWorkspace(t *testing.T) {
	for _, command := range []string{"/reply thread text", "/answer thread request text"} {
		for _, unsupported := range []bool{false, true} {
			env := newCUJEnv(t)
			a := &desktopRejectingAgent{}
			env.engine.agent = a
			if unsupported {
				env.engine.agent = &desktopHistoryAgent{}
			}
			env.engine.SetAdminFrom("alice")
			if err := env.engine.registerThreadNotification("test:alice", t.TempDir(), "thread", 0); err != nil {
				t.Fatal(err)
			}
			a.reject = true
			env.userSends("alice", command)
			if env.engine.workspacePool != nil {
				t.Fatal("failed capability/metadata preflight materialized a workspace", command, unsupported)
			}
			if a.input != "" || a.answer != "" {
				t.Fatal("failed preflight mutated the thread")
			}
			if len(env.plat.getSent()) == 0 {
				t.Fatal("preflight error was hidden")
			}
		}
	}
}

func TestDesktopCommandsRespectPendingAsyncInput(t *testing.T) {
	env := newCUJEnv(t)
	a := &desktopCommandAgent{}
	env.engine.agent = a
	env.engine.SetAdminFrom("alice")
	s := &asyncInputSession{active: true}
	state := &interactiveState{agentSession: s}
	env.engine.interactiveStates["test:alice"] = state
	env.engine.showAsyncUserInput(state, env.plat, "ctx", Event{RequestID: "q", Questions: []UserQuestion{{Question: "Next action?"}}})
	env.userSends("alice", "/reply thread answer")
	if len(s.steered) != 1 || !strings.Contains(s.steered[0], "/reply thread answer") {
		t.Fatal("pending question was bypassed", s.steered)
	}
	if a.input != "" {
		t.Fatal("question answer became a desktop mutation")
	}
	sent := env.plat.getSent()
	if !strings.Contains(sent[len(sent)-1], "Next action?") {
		t.Fatal("question acknowledgement missing", sent)
	}
}
