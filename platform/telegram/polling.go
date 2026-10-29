package telegram

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type pollingLifecycle struct {
	mu   sync.Mutex
	stop context.CancelFunc
}

// start returns to the platform's reconnect loop on a polling failure,
// allowing its downtime drain to establish a new recovery cutoff.
func (p *pollingLifecycle) start(ctx context.Context, bot *tgbot.Bot, onUpdate func(context.Context, *models.Update)) {
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	p.stop = cancel
	p.mu.Unlock()
	// The unbuffered, synchronous worker may still be handling the last live
	// update when the next poll fails. Let that handler finish using the
	// platform lifetime context, rather than cancelling its replies mid-turn.
	tgbot.WithDefaultHandler(func(_ context.Context, _ *tgbot.Bot, update *models.Update) {
		onUpdate(ctx, update)
	})(bot)
	bot.Start(pollCtx)
}

func (p *pollingLifecycle) errorHandler(token string) tgbot.ErrorsHandler {
	return func(err error) {
		slog.Warn("telegram: bot error", "error", core.RedactToken(err.Error(), token))
		// go-telegram reports getUpdates failures here and otherwise retries
		// them internally. Cancel synchronously, before it can fetch backlog
		// without the platform's recovery cutoff. Other errors (including
		// response-body close failures from sends) must not restart polling.
		if strings.HasPrefix(err.Error(), "error get updates, ") {
			p.mu.Lock()
			stop := p.stop
			p.mu.Unlock()
			if stop != nil {
				stop()
			}
		}
	}
}
