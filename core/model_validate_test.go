package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type validatingModelAgent struct {
	stubModelModeAgent
	sets     int
	validate func(context.Context, string) (string, error)
}

func (a *validatingModelAgent) SetModel(model string) {
	a.sets++
	a.stubModelModeAgent.SetModel(model)
}

func (a *validatingModelAgent) ValidateModel(ctx context.Context, model string) (string, error) {
	if a.validate == nil {
		return model, nil
	}
	return a.validate(ctx, model)
}

func TestCmdModel_ListShowsModels(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	msg := &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}

	for _, args := range [][]string{nil, {"list"}, {"LS"}} {
		p.clearSent()
		e.cmdModel(p, msg, args)
		sent := p.getSent()
		if len(sent) == 0 || !strings.Contains(sent[0], "Available models:") {
			t.Fatalf("args=%v sent=%q", args, sent)
		}
	}
	if agent.model == "list" || agent.model == "LS" {
		t.Fatalf("list persisted as %q", agent.model)
	}

	e.cmdModel(p, msg, []string{"switch", "gpt-4.1"})
	if agent.model != "gpt-4.1" {
		t.Fatalf("switch model = %q", agent.model)
	}
}

func TestCmdModel_ValidatorRejectsWithoutPersist(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &validatingModelAgent{}
	agent.validate = func(context.Context, string) (string, error) {
		return "", errors.New(`unknown Devin model "custom"`)
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	saved := false
	e.SetModelSaveFunc(func(string) error {
		saved = true
		return nil
	})
	e.cmdModel(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"custom"})
	if agent.sets != 0 || saved || agent.model != "" {
		t.Fatalf("sets=%d saved=%v model=%q", agent.sets, saved, agent.model)
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(sent[0], "Failed to change model") {
		t.Fatalf("sent=%q", sent)
	}
}

func TestCmdModel_ValidatorCanonicalizes(t *testing.T) {
	p := &stubPlatformEngine{n: "plain"}
	agent := &validatingModelAgent{}
	agent.validate = func(_ context.Context, model string) (string, error) {
		if model != "custom" {
			t.Errorf("validator saw %q", model)
		}
		return "gpt-4.1", nil
	}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	var saved string
	e.SetModelSaveFunc(func(model string) error {
		saved = model
		return nil
	})
	e.cmdModel(p, &Message{SessionKey: "test:user1", ReplyCtx: "ctx"}, []string{"custom"})
	if agent.model != "gpt-4.1" || saved != "gpt-4.1" {
		t.Fatalf("model=%q saved=%q", agent.model, saved)
	}
}
