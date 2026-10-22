package core

import (
	"context"
	"log/slog"
	"time"
)

// skillsForAgent takes a fresh catalog or directory snapshot. Registries are request-local:
// changing a work directory or rebinding a channel cannot leave stale skill
// contents in another channel's cache. No model session is started here.
func (e *Engine) skillsForAgent(agent Agent) *SkillRegistry {
	registry, err := e.loadSkillsForAgent(agent)
	if err != nil {
		slog.Warn("skill: load agent catalog", "agent", agent.Name(), "error", err)
		return NewSkillRegistry()
	}
	return registry
}

func (e *Engine) loadSkillsForAgent(agent Agent) (*SkillRegistry, error) {
	if provider, ok := agent.(SkillCatalogProvider); ok {
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		skills, err := provider.ListSkills(ctx)
		if err != nil {
			return nil, err
		}
		registry := NewSkillRegistry()
		registry.SetSkills(skills)
		return registry, nil
	}
	if sp, ok := agent.(SkillProvider); ok {
		registry := NewSkillRegistry()
		registry.SetDirs(sp.SkillDirs())
		return registry, nil
	}
	if agent == e.agent {
		return e.skills, nil
	}
	return NewSkillRegistry(), nil
}

func (e *Engine) skillsForMessage(p Platform, msg *Message) (*SkillRegistry, error) {
	agent, _, _, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		return nil, err
	}
	return e.loadSkillsForAgent(agent)
}

func (e *Engine) skillsCardForSession(sessionKey string) *Card {
	p := e.platformForName(extractPlatformName(sessionKey))
	registry, err := e.skillsForMessage(p, &Message{SessionKey: sessionKey, Platform: p.Name()})
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleSkills), "purple", e.i18n.Tf(MsgWsResolutionError, err))
	}
	return e.renderSkillsCard(registry)
}
