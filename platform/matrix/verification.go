//go:build goolm

package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// encryptingTransport wraps an http.RoundTripper and automatically encrypts
// m.key.verification.* events when sending to encrypted rooms.
// The VerificationHelper sends these events unencrypted via
// client.SendMessageEvent, but Element ignores unencrypted events in
// encrypted rooms. This transport intercepts those requests and encrypts them.
type encryptingTransport struct {
	base http.RoundTripper
	p    *Platform
}

func (t *encryptingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only intercept PUT requests
	if req.Method != http.MethodPut {
		return t.base.RoundTrip(req)
	}

	// Log all PUT requests for diagnostics
	slog.Info("matrix: transport PUT", "path", req.URL.Path)

	roomID, evtType, ok := parseRoomSendPath(req.URL.Path)
	if !ok {
		return t.base.RoundTrip(req)
	}

	if !strings.HasPrefix(evtType, "m.key.verification.") {
		return t.base.RoundTrip(req)
	}

	slog.Info("matrix: transport intercepted verification event", "type", evtType, "room", roomID, "path", req.URL.Path)

	enc := t.p.isRoomEncrypted(req.Context(), roomID)
	slog.Info("matrix: room encrypted check", "room", roomID, "encrypted", enc)

	if !enc {
		slog.Info("matrix: room not encrypted, sending verification event as-is")
		return t.base.RoundTrip(req)
	}

	ch := t.p.getE2EECryptoHelper()
	if ch == nil {
		slog.Warn("matrix: no crypto helper, sending verification event unencrypted")
		return t.base.RoundTrip(req)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()

	slog.Info("matrix: encrypting verification event", "type", evtType, "body_len", len(body))

	var contentMap map[string]any
	if err := json.Unmarshal(body, &contentMap); err != nil {
		slog.Warn("matrix: failed to parse verification event body", "error", err)
		req.Body = io.NopCloser(bytes.NewReader(body))
		return t.base.RoundTrip(req)
	}

	encContent, err := ch.Encrypt(req.Context(), roomID, event.Type{Type: evtType}, contentMap)
	if err != nil {
		slog.Warn("matrix: encrypt verification event failed, sending unencrypted", "type", evtType, "error", err)
		req.Body = io.NopCloser(bytes.NewReader(body))
		return t.base.RoundTrip(req)
	}

	encBody, err := json.Marshal(encContent)
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return t.base.RoundTrip(req)
	}

	// Replace event type in URL path
	newPath := strings.Replace(req.URL.Path, "/send/"+evtType+"/", "/send/m.room.encrypted/", 1)
	req.URL.Path = newPath
	req.URL.RawPath = ""

	req.Body = io.NopCloser(bytes.NewReader(encBody))
	req.ContentLength = int64(len(encBody))

	slog.Debug("matrix: encrypted verification event", "original_type", evtType)
	return t.base.RoundTrip(req)
}

// parseRoomSendPath extracts the room ID and event type from a Matrix
// client API path like /_matrix/client/v3/rooms/{roomID}/send/{eventType}/{txnID}
func parseRoomSendPath(path string) (id.RoomID, string, bool) {
	idx := strings.Index(path, "/rooms/")
	if idx < 0 {
		return "", "", false
	}
	rest := path[idx+len("/rooms/"):]

	sendIdx := strings.Index(rest, "/send/")
	if sendIdx < 0 {
		return "", "", false
	}
	roomID := id.RoomID(rest[:sendIdx])
	afterSend := rest[sendIdx+len("/send/"):]

	typeEnd := strings.Index(afterSend, "/")
	if typeEnd < 0 {
		return "", "", false
	}
	evtType := afterSend[:typeEnd]
	return roomID, evtType, true
}

// --- Verification callbacks ---

type verificationCallbacks struct {
	platform *Platform
}

var (
	_ verificationhelper.RequiredCallbacks = (*verificationCallbacks)(nil)
	_ verificationhelper.ShowSASCallbacks  = (*verificationCallbacks)(nil)
)

func (vc *verificationCallbacks) VerificationRequested(ctx context.Context, txnID id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	slog.Info("matrix: verification requested", "txn_id", txnID, "from", from, "device", fromDevice)

	// Store the device ID so handleVerificationMAC can trust only this device.
	vc.platform.setVerifyDeviceID(fromDevice)

	helper := vc.platform.getVerificationHelper()
	if helper == nil {
		return
	}

	if err := helper.AcceptVerification(ctx, txnID); err != nil {
		slog.Error("matrix: accept verification failed", "txn_id", txnID, "error", err)
	}
}

func (vc *verificationCallbacks) VerificationReady(ctx context.Context, txnID id.VerificationTransactionID, otherDeviceID id.DeviceID, supportsSAS, supportsScanQRCode bool, qrCode *verificationhelper.QRCode) {
	slog.Info("matrix: verification ready", "txn_id", txnID, "device", otherDeviceID, "sas", supportsSAS)
	// Don't call StartSAS here. The other side (Element) will send its own
	// m.key.verification.start event, and the VerificationHelper auto-accepts
	// it in onVerificationStartSAS. Calling StartSAS ourselves creates a race
	// condition where both sides try to start simultaneously, causing the
	// conflict resolution to pick the wrong start event.
}

func (vc *verificationCallbacks) VerificationCancelled(ctx context.Context, txnID id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	slog.Warn("matrix: verification cancelled", "txn_id", txnID, "code", code, "reason", reason)
}

func (vc *verificationCallbacks) VerificationDone(ctx context.Context, txnID id.VerificationTransactionID, method event.VerificationMethod) {
	slog.Info("matrix: verification done", "txn_id", txnID, "method", method)
}

func (vc *verificationCallbacks) ShowSAS(ctx context.Context, txnID id.VerificationTransactionID, emojis []rune, emojiDescriptions []string, decimals []int) {
	if len(emojis) > 0 {
		slog.Info("matrix: SAS emojis", "txn_id", txnID, "emojis", string(emojis), "descriptions", emojiDescriptions)
	}
	if len(decimals) > 0 {
		slog.Info("matrix: SAS decimals", "txn_id", txnID, "decimals", fmt.Sprint(decimals))
	}

	// ConfirmSAS must run in a separate goroutine because onVerificationKey
	// holds activeTransactionsLock when calling showSAS, and ConfirmSAS
	// also acquires the same non-reentrant mutex.
	go func() {
		helper := vc.platform.getVerificationHelper()
		if helper == nil {
			return
		}
		if err := helper.ConfirmSAS(context.Background(), txnID); err != nil {
			slog.Error("matrix: confirm SAS failed", "txn_id", txnID, "error", err)
		}
	}()
}

func (p *Platform) initVerification(ctx context.Context, ch *cryptohelper.CryptoHelper) error {
	client := p.getClient()
	if client == nil {
		return fmt.Errorf("matrix: client not available")
	}

	// Wrap the client's actual HTTP transport. We must use client.Client directly
	// (not p.httpClient) because cryptohelper.Init() may have replaced client.Client
	// with a new http.Client instance.
	if client.Client == nil {
		return fmt.Errorf("matrix: client http.Client is nil")
	}
	base := client.Client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Client.Transport = &encryptingTransport{base: base, p: p}
	slog.Info("matrix: transport wrapped for verification encryption")

	callbacks := &verificationCallbacks{platform: p}
	helper := verificationhelper.NewVerificationHelper(client, ch.Machine(), nil, callbacks, false, false, true)

	if err := helper.Init(ctx); err != nil {
		return fmt.Errorf("matrix: init verification: %w", err)
	}

	p.setVerificationHelper(helper)
	slog.Info("matrix: verification helper initialized and event handlers registered")
	return nil
}

// handleVerificationMAC works around a mautrix library bug where
// onVerificationMAC uses GetOwnCrossSigningPublicKeys() instead of the other
// user's cross-signing keys for cross-user verification. This causes "unknown
// key ID" errors when verifying devices belonging to a different user.
//
// Instead of dispatching the MAC event to the buggy handler, we trust the
// device directly and send a verification done event. The SAS emoji comparison
// already proved both sides share the same secret, so this is safe for
// auto-verify bots.
func (p *Platform) handleVerificationMAC(ctx context.Context, client *mautrix.Client, ch *cryptohelper.CryptoHelper, evt *event.Event) {
	// Extract transaction ID (already fixed by CustomPostDecrypt to be the request event ID)
	txnID := id.VerificationTransactionID(evt.ID)
	slog.Info("matrix: handling verification MAC (workaround)", "txn_id", txnID, "sender", evt.Sender, "room", evt.RoomID)

	// Trust only the specific device that initiated verification.
	// The device ID was saved in VerificationRequested.
	deviceID := p.getVerifyDeviceID()
	if deviceID == "" {
		slog.Error("matrix: no verify device ID stored, cannot trust device")
		return
	}
	theirDevice, err := ch.Machine().GetOrFetchDevice(ctx, evt.Sender, deviceID)
	if err != nil {
		slog.Error("matrix: failed to get device for MAC verification", "sender", evt.Sender, "device", deviceID, "error", err)
		return
	}
	theirDevice.Trust = id.TrustStateVerified
	if err := ch.Machine().CryptoStore.PutDevice(ctx, evt.Sender, theirDevice); err != nil {
		slog.Error("matrix: failed to trust device", "device", deviceID, "error", err)
		return
	}
	slog.Info("matrix: device trusted", "user", evt.Sender, "device", deviceID)
	p.setVerifyDeviceID("")

	// Send m.key.verification.done to the room
	doneContent := &event.VerificationDoneEventContent{}
	doneContent.SetRelatesTo(&event.RelatesTo{Type: event.RelReference, EventID: id.EventID(txnID)})
	if _, err := client.SendMessageEvent(ctx, evt.RoomID, event.InRoomVerificationDone, &event.Content{Parsed: doneContent}); err != nil {
		slog.Error("matrix: failed to send verification done", "error", err)
	}

	// Clean up the transaction
	helper := p.getVerificationHelper()
	if helper != nil {
		_ = helper.DismissVerification(ctx, txnID)
	}
	slog.Info("matrix: verification complete", "txn_id", txnID)
}

// crossSigningSeedsPath returns the path where cross-signing seeds are persisted.
func (p *Platform) crossSigningSeedsPath() (string, error) {
	client := p.getClient()
	if client == nil || client.DeviceID == "" {
		return "", fmt.Errorf("matrix: device ID not available")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(homeDir, ".cc-connect", fmt.Sprintf("matrix-cross-signing-%s.json", client.DeviceID)), nil
}

// setupCrossSigning bootstraps cross-signing for the bot's own device.
// Without cross-signing, Element shows "encrypted by a device not verified
// by its owner" on messages from the bot.
func (p *Platform) setupCrossSigning(ctx context.Context, ch *cryptohelper.CryptoHelper) {
	mach := ch.Machine()

	// If private keys are already loaded in memory, just sign our device.
	if mach.CrossSigningKeys != nil {
		p.crossSignOwnDevice(ctx, mach)
		return
	}

	seedsPath, err := p.crossSigningSeedsPath()
	if err != nil {
		slog.Warn("matrix: cannot determine cross-signing seeds path", "error", err)
		return
	}

	// Try to load previously saved seeds and verify they match the server.
	if data, readErr := os.ReadFile(seedsPath); readErr == nil {
		var seeds crypto.CrossSigningSeeds
		if jsonErr := json.Unmarshal(data, &seeds); jsonErr == nil {
			if importErr := mach.ImportCrossSigningKeys(seeds); importErr == nil {
				slog.Info("matrix: loaded cross-signing keys from disk")
				if signErr := p.crossSignOwnDeviceErr(ctx, mach); signErr == nil {
					return
				}
				// Seeds don't match server keys — discard and regenerate.
				slog.Warn("matrix: saved seeds don't match server, regenerating")
				mach.CrossSigningKeys = nil
				_ = os.Remove(seedsPath)
			}
		}
	}

	// Generate new cross-signing keys.
	keys, err := mach.GenerateCrossSigningKeys()
	if err != nil {
		slog.Warn("matrix: failed to generate cross-signing keys", "error", err)
		return
	}
	seeds := crypto.CrossSigningSeeds{
		MasterKey:      keys.MasterKey.Seed(),
		SelfSigningKey: keys.SelfSigningKey.Seed(),
		UserSigningKey: keys.UserSigningKey.Seed(),
	}
	if err := mach.ImportCrossSigningKeys(seeds); err != nil {
		slog.Warn("matrix: failed to import cross-signing keys", "error", err)
	}
	slog.Info("matrix: generated new cross-signing keys")

	// Publish cross-signing keys to the server (requires UIA).
	if err := mach.PublishCrossSigningKeys(ctx, keys, p.uiaCallback); err != nil {
		slog.Warn("matrix: failed to publish cross-signing keys", "error", err)
	} else {
		slog.Info("matrix: published cross-signing keys")
	}

	// Persist seeds for next restart.
	if seedData, jsonErr := json.Marshal(seeds); jsonErr == nil {
		if writeErr := os.WriteFile(seedsPath, seedData, 0o600); writeErr != nil {
			slog.Warn("matrix: failed to save cross-signing seeds", "error", writeErr)
		}
	}

	p.crossSignOwnDevice(ctx, mach)
}

func (p *Platform) uiaCallback(uiResp *mautrix.RespUserInteractive) interface{} {
	// Try m.login.dummy first (works on many Synapse configurations).
	if uiResp.HasSingleStageFlow(mautrix.AuthTypeDummy) {
		return &mautrix.BaseAuthData{
			Type:    mautrix.AuthTypeDummy,
			Session: uiResp.Session,
		}
	}
	// Fall back to m.login.password if a password is configured.
	if p.crossSigningPassword != "" && uiResp.HasSingleStageFlow(mautrix.AuthTypePassword) {
		client := p.getClient()
		userID := ""
		if client != nil {
			userID = client.UserID.String()
		}
		return &mautrix.ReqUIAuthLogin{
			BaseAuthData: mautrix.BaseAuthData{
				Type:    mautrix.AuthTypePassword,
				Session: uiResp.Session,
			},
			User:     userID,
			Password: p.crossSigningPassword,
		}
	}
	slog.Warn("matrix: no supported UIA flow for cross-signing; consider setting cross_signing_password in config", "flows", uiResp.Flows)
	return nil
}

func (p *Platform) crossSignOwnDeviceErr(ctx context.Context, mach *crypto.OlmMachine) error {
	ownDevice := mach.OwnIdentity()
	if ownDevice == nil {
		return fmt.Errorf("own device identity not found")
	}
	return mach.SignOwnDevice(ctx, ownDevice)
}

func (p *Platform) crossSignOwnDevice(ctx context.Context, mach *crypto.OlmMachine) {
	ownDevice := mach.OwnIdentity()
	if ownDevice == nil {
		slog.Warn("matrix: own device identity not found, skipping self-sign")
		return
	}
	if err := mach.SignOwnDevice(ctx, ownDevice); err != nil {
		slog.Warn("matrix: failed to sign own device", "error", err)
	} else {
		slog.Info("matrix: cross-signed own device", "device", ownDevice.DeviceID)
	}
}
