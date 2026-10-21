//go:build !goolm

package matrix

import (
	"context"
	"log/slog"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (p *Platform) initE2EE(_ context.Context, _ *mautrix.Client) {
	slog.Info("matrix: E2EE not available (build with -tags goolm to enable)")
}

// tryEncryptAndSend returns (false, nil) to indicate E2EE is not available;
// the caller should send the event unencrypted.
func (p *Platform) tryEncryptAndSend(_ context.Context, _ *mautrix.Client, _ id.RoomID, _ event.Type, _ any) (bool, error) {
	return false, nil
}

func (p *Platform) closeCryptoHelper() {}
