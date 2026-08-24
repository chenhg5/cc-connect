package controlplane

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/controlstore"
	"github.com/chenhg5/cc-connect/runtimeprotocol"
)

func TestBrokerAttachmentIsDeviceBoundSignedAndSingleUse(t *testing.T) {
	ctx := context.Background()
	store, err := controlstore.Open(filepath.Join(t.TempDir(), "control.db"), "setup-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := store.CreatePairingCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	device, err := store.PairDevice(ctx, pairing.Code, "Mac", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceDeviceCatalog(ctx, device.ID, []controlstore.CatalogEntry{{
		DeviceID: device.ID, LocalRef: "local-workspace", GlobalRef: "global-workspace",
		Payload: []byte(`{"local_ref":"local-workspace","project_id":"p","project_name":"P","root_index":0,"root_name":"R","available":true,"order":1}`), Available: true,
	}}); err != nil {
		t.Fatal(err)
	}
	broker, err := NewBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := NewAttachmentStore(filepath.Join(t.TempDir(), "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attachments.Close() })
	broker.setAttachmentStore(attachments)
	broker.connections[device.ID] = &runtimeConnection{deviceID: device.ID}

	references, err := broker.StageAttachments(ctx, runtimeprotocol.AttachmentStageRequest{
		DeviceID: device.ID, WorkspaceRef: "global-workspace",
		Attachments: []runtimeprotocol.AttachmentUpload{{Type: "file", FileName: "../../report.txt", Data: []byte("report")}},
	})
	if err != nil || len(references) != 1 {
		t.Fatalf("StageAttachments() = %#v, %v", references, err)
	}
	if references[0].FileName != "report.txt" {
		t.Fatalf("sanitized filename = %q", references[0].FileName)
	}
	ref := references[0].Ref
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "nonce-1"
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, runtimeprotocol.SignedRequestMessage(
		runtimeprotocol.SignedPurposeAttachmentDownload, device.ID, ref, timestamp, nonce,
	)))
	if _, err := broker.DownloadAttachment(ctx, "another-device", ref, timestamp, nonce, signature); err == nil {
		t.Fatal("attachment accepted a different device identity")
	}
	content, err := broker.DownloadAttachment(ctx, device.ID, ref, timestamp, nonce, signature)
	if err != nil {
		t.Fatal(err)
	}
	if content.WorkspaceRef != "local-workspace" || string(content.Data) != "report" {
		t.Fatalf("downloaded content = %#v", content)
	}
	if _, err := broker.DownloadAttachment(ctx, device.ID, ref, timestamp, nonce, signature); err == nil {
		t.Fatal("single-use attachment or nonce was accepted twice")
	}
}

func TestAttachmentStoreCleansDeviceAndExpiredFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "attachments")
	store, err := NewAttachmentStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now()
	store.now = func() time.Time { return now }
	references, err := store.Stage("device-1", "workspace-1", []runtimeprotocol.AttachmentUpload{{Type: "image", Data: []byte("png")}})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	path := store.entries[references[0].Ref].path
	store.mu.Unlock()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged file info = %#v, %v", info, err)
	}
	store.CleanupDevice("device-1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("device cleanup left attachment file: %v", err)
	}
	references, err = store.Stage("device-2", "workspace-2", []runtimeprotocol.AttachmentUpload{{Type: "audio", Data: []byte("audio")}})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	path = store.entries[references[0].Ref].path
	store.mu.Unlock()
	now = now.Add(attachmentTTL + time.Second)
	store.cleanupExpired()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expiry cleanup left attachment file: %v", err)
	}
}
