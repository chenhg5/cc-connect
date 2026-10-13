package core

import (
	"context"
	"crypto/rand"
	"log/slog"
	"strconv"
)

// InteractiveStreamingCard keeps questions, approval controls and terminal
// states on the same message as the streamed answer. Finish is idempotent;
// an empty content argument preserves the most recently displayed content.
type InteractiveStreamingCard interface {
	StreamingCard
	SetPrompt(context.Context, *Card) error
	SetNotice(context.Context, string) error
	Finish(context.Context, string, CardStatus) error
}

// ProgressSummaryCard optionally separates the always-visible progress from
// the complete execution details. Setting the summary does not send a message;
// Update/Finish flush both together. Empty disables the structured summary.
type ProgressSummaryCard interface{ SetProgressSummary(string) }

// FinalAnswerCard can finish the process message and publish a separate answer.
// FinalizeWithAnswer must be idempotent and an empty answer must stay silent.
type FinalAnswerCard interface {
	SeparateFinalAnswer() bool
	FinalizeWithAnswer(ctx context.Context, progress, answer string) error
}

func separateFinalAnswerCard(card StreamingCard) FinalAnswerCard {
	if c, ok := card.(FinalAnswerCard); ok && c.SeparateFinalAnswer() {
		return c
	}
	return nil
}

// SingleQueryCardPlatform lets queue acknowledgements use the active card.
type SingleQueryCardPlatform interface{ SingleQueryCardEnabled() bool }

func singleQueryCardEnabled(p Platform) bool {
	c, ok := p.(SingleQueryCardPlatform)
	return ok && c.SingleQueryCardEnabled()
}

func queryCardNotice(ctx context.Context, card InteractiveStreamingCard, text string) {
	if err := card.SetNotice(ctx, text); err != nil {
		slog.Warn("query card notice update failed", "error", err)
	}
}

func (e *Engine) queryCardForSession(key string) InteractiveStreamingCard {
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.queryCard
}

func (e *Engine) showQueryPrompt(pending *pendingPermission, toolInput string) error {
	var card *Card
	if len(pending.Questions) > 0 {
		card = e.buildAskQuestionCard(pending.Questions, pending.CurrentQuestion)
	} else {
		card = e.buildPermissionCard(pending.ToolName, toolInput)
	}
	if !pending.ExpiresAt.IsZero() {
		card.Elements = append(card.Elements, CardNote{Text: e.i18n.Tf(MsgDecisionDeadline, pending.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))})
	}
	// Unique per question, not just the CLI's reusable JSON-RPC request ID.
	pending.CardToken = rand.Text()
	if len(pending.Questions) > 0 {
		card.DecisionToken = pending.CardToken
	}
	for i, element := range card.Elements {
		if row, ok := element.(CardActions); ok {
			if pending.RequesterID != "" {
				kept := []CardButton{}
				for _, b := range row.Buttons {
					if b.Value != "perm:allow_all" {
						kept = append(kept, b)
					}
				}
				row.Buttons = kept
			}
			for j := range row.Buttons {
				if row.Buttons[j].Extra == nil {
					row.Buttons[j].Extra = map[string]string{}
				}
				row.Buttons[j].Extra["query_card"] = "true"
				row.Buttons[j].Extra["permission_token"] = pending.CardToken
				row.Buttons[j].Extra["question_index"] = strconv.Itoa(pending.CurrentQuestion)
			}
			card.Elements[i] = row
		}
	}
	if err := pending.Card.SetPrompt(e.ctx, card); err != nil {
		slog.Warn("query card prompt update failed", "error", err)
		return err
	}
	return nil
}

// A permission request that could not be shown must not leave the agent
// waiting indefinitely. Denial never authorizes the requested operation.
func (e *Engine) failQueryPrompt(state *interactiveState, pending *pendingPermission, cause error) {
	if err := state.agentSession.RespondPermission(pending.RequestID, PermissionResult{Behavior: "deny", Message: "Could not display approval prompt"}); err != nil {
		slog.Warn("deny undisplayable approval failed", "error", err)
	}
	state.mu.Lock()
	if state.pending == pending {
		state.pending = nil
	}
	state.mu.Unlock()
	pending.Expired = true
	if pending.Card != nil {
		queryCardNotice(e.ctx, pending.Card, e.i18n.Tf(MsgError, cause))
		_ = pending.Card.SetPrompt(e.ctx, nil)
	} else {
		slog.Warn("approval unavailable before card creation", "error", cause)
	}
	pending.resolve()
}
