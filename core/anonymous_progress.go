package core

import (
	"context"
	"log/slog"
	"time"
)

// AnonymousRichCardSupporter builds a rich card from anonymous activity counts.
// It deliberately cannot receive reasoning, tool names, inputs, results, or a
// status footer. Answer markdown and explicit permission prompts remain intact.
type AnonymousRichCardSupporter interface {
	BuildAnonymousRichCard(status CardStatus, toolCount int, markdown string, streaming bool, lang Language) string
}

type anonymousRichCardAdapter struct {
	renderer AnonymousRichCardSupporter
	lang     Language
}

// A foreground turn creates its placeholder before potentially slow agent
// startup, then transfers ownership to the event loop under state.mu.
type anonymousProgressStart struct {
	handle any
	lang   Language
}

func (a anonymousRichCardAdapter) BuildRichCard(status CardStatus, _ string, steps []ToolStep, markdown string, streaming bool, _ string) string {
	tools := 0
	for _, step := range steps {
		if step.Kind != ToolStepKindThinking {
			tools++
		}
	}
	return a.renderer.BuildAnonymousRichCard(status, tools, markdown, streaming, a.lang)
}

func richCardSupporterForMode(p Platform, mode string, lang Language) (RichCardSupporter, bool) {
	switch mode {
	case "rich":
		supporter, ok := p.(RichCardSupporter)
		return supporter, ok
	case "rich-anonymous":
		if supporter, ok := p.(AnonymousRichCardSupporter); ok {
			return anonymousRichCardAdapter{renderer: supporter, lang: lang}, true
		}
	}
	return nil, false
}

func startAnonymousRichCard(ctx context.Context, p Platform, replyCtx any, mode string, lang Language) any {
	if mode != "rich-anonymous" {
		return nil
	}
	supporter, supported := richCardSupporterForMode(p, mode, lang)
	starter, canStart := p.(PreviewStarter)
	if !supported || !canStart {
		return nil
	}
	card := supporter.BuildRichCard(CardStatusThinking, "", nil, "", true, "")
	handle, err := starter.SendPreviewStart(ctx, replyCtx, card)
	if err != nil {
		slog.Debug("anonymous progress: initial card failed", "platform", p.Name(), "error", err)
		return nil
	}
	return handle
}

func interruptAnonymousRichCard(ctx context.Context, p Platform, handle any, lang Language, steps []ToolStep, body string) bool {
	supporter, supported := richCardSupporterForMode(p, "rich-anonymous", lang)
	updater, canUpdate := p.(MessageUpdater)
	if handle == nil || !supported || !canUpdate {
		return false
	}
	if stripped, ok := stripTrailingSilent(body); ok {
		body = stripped
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	card := supporter.BuildRichCard(CardStatusError, "", steps, body, false, "")
	if err := updater.UpdateMessage(ctx, handle, card); err != nil {
		slog.Debug("anonymous progress: interrupted card update failed", "platform", p.Name(), "error", err)
		return false
	}
	return true
}
