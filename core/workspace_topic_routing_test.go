package core

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceChannelKeysFromSessionKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want []string
	}{
		{"root", "feishu:oc_chat:root:om_root", []string{"feishu:oc_chat:topic:om_root", "feishu:oc_chat"}},
		{"thread", "feishu:oc_chat:thread:om_root", []string{"feishu:oc_chat:topic:om_root", "feishu:oc_chat"}},
		{"topic", "feishu:oc_chat:topic:om_root", []string{"feishu:oc_chat:topic:om_root", "feishu:oc_chat"}},
		{"chat", "feishu:oc_chat:ou_user", []string{"feishu:oc_chat"}},
		{"missing root", "feishu:oc_chat:root:", []string{"feishu:oc_chat"}},
		{"missing chat", "feishu::root:om_root", nil},
		{"unknown marker", "feishu:oc_chat:other:om_root", []string{"feishu:oc_chat"}},
		{"dingtalk group", "dingtalk:g:cid_group:staff", []string{"dingtalk:cid_group"}},
		{"dingtalk shared group", "dingtalk:g:cid_group", []string{"dingtalk:cid_group"}},
		{"dingtalk channel named root", "dingtalk:g:root:staff", []string{"dingtalk:root"}},
		{"slack thread", "slack:C123:t:123456.789", []string{"slack:C123"}},
		{"slack channel", "slack:C123:U123", []string{"slack:C123"}},
		{"telegram topic", "telegram:-100123:42:12345", []string{"telegram:-100123"}},
		{"telegram shared topic", "telegram:-100123:42", []string{"telegram:-100123"}},
		{"empty", "", nil},
		{"no channel", "feishu", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workspaceChannelKeysFromSessionKey(tt.key); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("workspaceChannelKeysFromSessionKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

type topicRoutingTestAgent struct {
	namedTestAgent
	workspace string
}

func (a *topicRoutingTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return []AgentSessionInfo{{ID: a.workspace, Summary: "workspace " + filepath.Base(a.workspace)}}, nil
}

func TestMultiWorkspaceTopicRouting_ListCardAndInteractiveKey(t *testing.T) {
	tests := []struct {
		name       string
		sessionKey string
		chatKey    string
		topicKey   string
		wantTopic  bool
	}{
		{"topic overrides chat", "feishu:oc_chat:root:om_root", "feishu:oc_chat", "feishu:oc_chat:topic:om_root", true},
		{"thread marker", "feishu:oc_chat:thread:om_root", "feishu:oc_chat", "feishu:oc_chat:topic:om_root", true},
		{"topic marker", "feishu:oc_chat:topic:om_root", "feishu:oc_chat", "feishu:oc_chat:topic:om_root", true},
		{"unbound topic inherits chat", "feishu:oc_chat:root:om_other", "feishu:oc_chat", "feishu:oc_chat:topic:om_root", false},
		{"chat without isolation", "feishu:oc_chat:ou_user", "feishu:oc_chat", "feishu:oc_chat:topic:om_root", false},
		{"dingtalk typed key", "dingtalk:g:cid_group:staff", "dingtalk:cid_group", "dingtalk:g:topic:staff", false},
		{"dingtalk root is channel not marker", "dingtalk:g:root:staff", "dingtalk:root", "dingtalk:g:topic:staff", false},
		{"slack typed thread", "slack:C123:t:123456.789", "slack:C123", "slack:C123:topic:123456.789", false},
		{"telegram numeric topic", "telegram:-100123:42:12345", "telegram:-100123", "telegram:-100123:topic:12345", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseDir := t.TempDir()
			chatDir := t.TempDir()
			topicDir := t.TempDir()
			globalAgent := &topicRoutingTestAgent{namedTestAgent{name: "topic-routing-test-agent"}, "global"}
			RegisterAgent(globalAgent.Name(), func(opts map[string]any) (Agent, error) {
				workspace, _ := opts["work_dir"].(string)
				return &topicRoutingTestAgent{namedTestAgent{name: globalAgent.Name()}, workspace}, nil
			})
			e := NewEngine("test", globalAgent, nil, filepath.Join(baseDir, "sessions.json"), LangEnglish)
			e.SetMultiWorkspace(baseDir, filepath.Join(baseDir, "bindings.json"))
			e.workspaceBindings.Bind("project:test", tt.chatKey, "chat", chatDir)
			e.workspaceBindings.Bind("project:test", tt.topicKey, "topic", topicDir)
			wantDir := normalizeWorkspacePath(chatDir)
			if tt.wantTopic {
				wantDir = normalizeWorkspacePath(topicDir)
			}

			// Resolve before any live state exists: card callbacks only have the session key.
			agent, sessions := e.sessionContextForKey(tt.sessionKey)
			gotAgent, ok := agent.(*topicRoutingTestAgent)
			if !ok || gotAgent.workspace != wantDir {
				t.Fatalf("resolved agent = %#v, want workspace %q", agent, wantDir)
			}
			entry := e.workspacePool.Get(wantDir)
			if entry == nil || sessions != entry.sessions || sessions == e.sessions {
				t.Fatal("resolved session manager does not belong to the expected workspace")
			}
			if got := e.interactiveKeyForSessionKey(tt.sessionKey); got != wantDir+":"+tt.sessionKey {
				t.Fatalf("interactive key = %q, want %q", got, wantDir+":"+tt.sessionKey)
			}
			card, err := e.renderListCard(tt.sessionKey, 1)
			if err != nil {
				t.Fatal(err)
			}
			var rows []CardListItem
			for _, element := range card.Elements {
				if row, ok := element.(CardListItem); ok {
					rows = append(rows, row)
				}
			}
			if len(rows) != 1 || !strings.Contains(rows[0].Text, "workspace "+filepath.Base(wantDir)) || rows[0].BtnValue != "act:/switch 1" {
				t.Fatalf("list card rows = %+v, want one session from %q", rows, wantDir)
			}
		})
	}
}
