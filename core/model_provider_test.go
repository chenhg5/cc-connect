package core

import (
	"context"
	"testing"
)

type providerAwareModelAgent struct {
	stubAgent
	model    string
	provider string
	models   []ModelOption
}

func (a *providerAwareModelAgent) SetModel(model string) { a.model = model }

func (a *providerAwareModelAgent) GetModel() string { return a.model }

func (a *providerAwareModelAgent) AvailableModels(context.Context) []ModelOption {
	return append([]ModelOption(nil), a.models...)
}

func (a *providerAwareModelAgent) SetModelForProvider(provider, model string) {
	a.provider = provider
	a.model = model
}

func (a *providerAwareModelAgent) GetModelProvider() string { return a.provider }

func TestCmdModel_PreservesCatalogProvider(t *testing.T) {
	agent := &providerAwareModelAgent{models: []ModelOption{
		{Name: "same-model", Provider: "openai"},
		{Name: "same-model", Provider: "openrouter"},
	}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	if !e.handleCommand(p, msg, "/model 2") {
		t.Fatal("/model 2 should be handled")
	}
	if agent.model != "same-model" || agent.provider != "openrouter" {
		t.Fatalf("selection = %s/%s, want openrouter/same-model", agent.provider, agent.model)
	}
}
