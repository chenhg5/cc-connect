//go:build goolm

package matrix

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	// Register pure-Go olm backend (no CGO/libolm required)
	_ "maunium.net/go/mautrix/crypto/goolm"
)

// e2eePlatform extends Platform with E2EE-specific fields.
type e2eePlatform struct {
	verificationHelper *verificationhelper.VerificationHelper
	verifyDeviceID     id.DeviceID
}

var e2eeMu sync.Mutex

func (p *Platform) e2ee() *e2eePlatform {
	// Stored alongside cryptoHelper as the concrete value
	if ch, ok := p.cryptoHelper.(e2eeState); ok {
		return ch.e2ee
	}
	return nil
}

type e2eeState struct {
	ch   *cryptohelper.CryptoHelper
	e2ee *e2eePlatform
}

func (p *Platform) getE2EECryptoHelper() *cryptohelper.CryptoHelper {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if s, ok := p.cryptoHelper.(e2eeState); ok {
		return s.ch
	}
	return nil
}

func (p *Platform) setCryptoHelperE2EE(ch *cryptohelper.CryptoHelper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var state e2eeState
	if existing, ok := p.cryptoHelper.(e2eeState); ok {
		state = existing
	}
	state.ch = ch
	if state.e2ee == nil {
		state.e2ee = &e2eePlatform{}
	}
	p.cryptoHelper = state
}

func (p *Platform) getVerificationHelper() *verificationhelper.VerificationHelper {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e := p.e2ee()
	if e == nil {
		return nil
	}
	return e.verificationHelper
}

func (p *Platform) setVerificationHelper(vh *verificationhelper.VerificationHelper) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.e2ee()
	if e == nil {
		return
	}
	e.verificationHelper = vh
}

func (p *Platform) getVerifyDeviceID() id.DeviceID {
	e := p.e2ee()
	if e == nil {
		return ""
	}
	return e.verifyDeviceID
}

func (p *Platform) setVerifyDeviceID(d id.DeviceID) {
	e := p.e2ee()
	if e == nil {
		return
	}
	e.verifyDeviceID = d
}

func (p *Platform) closeCryptoHelper() {
	p.mu.Lock()
	s, ok := p.cryptoHelper.(e2eeState)
	p.cryptoHelper = nil
	p.mu.Unlock()

	if ok && s.ch != nil {
		s.ch.Close()
	}
}

func (p *Platform) initE2EE(ctx context.Context, client *mautrix.Client) {
	ch, err := p.initCrypto(ctx, client)
	if err != nil {
		slog.Warn("matrix: E2EE not available, encrypted rooms won't work", "error", err)
		return
	}

	ch.DecryptErrorCallback = func(evt *event.Event, decryptErr error) {
		slog.Warn("matrix: decrypt failed", "event_id", evt.ID, "sender", evt.Sender, "room", evt.RoomID, "error", decryptErr)
	}
	ch.CustomPostDecrypt = func(ctx context.Context, evt *event.Event) {
		slog.Debug("matrix: decrypted event", "type", evt.Type.Type, "event_id", evt.ID, "sender", evt.Sender, "room", evt.RoomID)

		// Fix transaction ID for in-room verification events from other users.
		if evt.RoomID != "" && evt.Sender != client.UserID && strings.HasPrefix(evt.Type.Type, "m.key.verification.") {
			if relatable, ok := evt.Content.Parsed.(event.Relatable); ok {
				if rel := relatable.OptionalGetRelatesTo(); rel != nil && rel.EventID != "" {
					slog.Debug("matrix: fixing verification event txn ID", "original_id", evt.ID, "txn_id", rel.EventID)
					evt.ID = rel.EventID
				}
			}
		}

		// Workaround for mautrix library bug: onVerificationMAC uses
		// GetOwnCrossSigningPublicKeys() instead of GetCrossSigningPublicKeys(ctx, theirUserID)
		// for cross-user verification, causing "unknown key ID" errors.
		if evt.Type.Type == "m.key.verification.mac" && evt.RoomID != "" && evt.Sender != client.UserID {
			p.handleVerificationMAC(ctx, client, ch, evt)
			return
		}
		client.Syncer.(mautrix.DispatchableSyncer).Dispatch(ctx, evt)
	}
	p.setCryptoHelperE2EE(ch)
	slog.Info("matrix: E2EE enabled", "device_id", client.DeviceID)

	// Bootstrap cross-signing
	p.setupCrossSigning(ctx, ch)

	// client.Crypto must be set for VerificationHelper
	client.Crypto = ch

	// Initialize SAS verification helper
	if p.autoVerify {
		if vErr := p.initVerification(ctx, ch); vErr != nil {
			slog.Warn("matrix: verification helper not available", "error", vErr)
		} else {
			slog.Info("matrix: SAS verification enabled", "mode", "auto-verify")
		}
	}
}

func (p *Platform) initCrypto(ctx context.Context, client *mautrix.Client) (*cryptohelper.CryptoHelper, error) {
	if client.DeviceID == "" {
		return nil, fmt.Errorf("device ID not available from whoami")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	cryptoDir := filepath.Join(homeDir, ".cc-connect")
	if err := os.MkdirAll(cryptoDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(cryptoDir, fmt.Sprintf("matrix-crypto-%s.db", client.DeviceID))

	// Derive a stable pickle key from the access token
	h := sha256.Sum256([]byte(p.accessToken))
	pickleKey := make([]byte, 32)
	copy(pickleKey, h[:])

	return p.tryInitCrypto(ctx, client, pickleKey, dbPath, false)
}

func (p *Platform) tryInitCrypto(ctx context.Context, client *mautrix.Client, pickleKey []byte, dbPath string, isRetry bool) (*cryptohelper.CryptoHelper, error) {
	ch, err := cryptohelper.NewCryptoHelper(client, pickleKey, dbPath)
	if err != nil {
		return nil, fmt.Errorf("create crypto helper: %w", err)
	}
	ch.DBAccountID = client.UserID.String()

	if err := ch.Init(ctx); err != nil {
		if !isRetry && strings.Contains(err.Error(), "not marked as shared") {
			slog.Warn("matrix: stale device keys on server, force-uploading new keys")
			func() {
				defer func() { recover() }()
				if mach := ch.Machine(); mach != nil {
					if shareErr := mach.ShareKeys(ctx, -1); shareErr != nil {
						slog.Error("matrix: failed to force-share keys", "error", shareErr)
					}
				}
			}()
			ch.Close()
			client.StateStore = nil
			client.Store = mautrix.NewMemorySyncStore()
			return p.tryInitCrypto(ctx, client, pickleKey, dbPath, true)
		}
		p.cleanupFailedCrypto(client, ch)
		return nil, fmt.Errorf("init crypto: %w", err)
	}
	return ch, nil
}

func (p *Platform) cleanupFailedCrypto(client *mautrix.Client, ch *cryptohelper.CryptoHelper) {
	ch.Close()
	client.StateStore = nil
	client.Store = mautrix.NewMemorySyncStore()
}

// tryEncryptAndSend attempts to encrypt and send an event if E2EE is available.
// Returns (true, nil) if handled, (true, err) if handled with error, (false, nil) if not handled.
func (p *Platform) tryEncryptAndSend(ctx context.Context, client *mautrix.Client, roomID id.RoomID, evtType event.Type, content any) (bool, error) {
	ch := p.getE2EECryptoHelper()
	if ch == nil {
		return false, nil
	}
	if !p.isRoomEncrypted(ctx, roomID) {
		return false, nil
	}

	encContent, err := ch.Encrypt(ctx, roomID, evtType, content)
	if err != nil {
		return true, fmt.Errorf("matrix: encrypt: %w", err)
	}
	_, err = client.SendMessageEvent(ctx, roomID, event.EventEncrypted, encContent)
	if err != nil {
		return true, fmt.Errorf("matrix: send encrypted: %w", err)
	}
	return true, nil
}

func (p *Platform) isRoomEncrypted(ctx context.Context, roomID id.RoomID) bool {
	client := p.getClient()
	if client == nil || client.StateStore == nil {
		return false
	}
	ss, ok := client.StateStore.(crypto.StateStore)
	if !ok {
		return false
	}
	enc, err := ss.IsEncrypted(ctx, roomID)
	if err != nil {
		return false
	}
	return enc
}
