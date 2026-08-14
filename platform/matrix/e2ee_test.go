//go:build goolm

package matrix

import (
	"path/filepath"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

func TestE2EEPersistencePathsUseInjectedDataDir(t *testing.T) {
	dataDir := t.TempDir()
	deviceID := id.DeviceID("DEVICE")
	p := &Platform{
		dataDir: dataDir,
		client:  &mautrix.Client{DeviceID: deviceID},
	}

	dbPath, err := p.cryptoDatabasePath(deviceID)
	if err != nil {
		t.Fatalf("cryptoDatabasePath() error = %v", err)
	}
	if want := filepath.Join(dataDir, "matrix-crypto-DEVICE.db"); dbPath != want {
		t.Fatalf("cryptoDatabasePath() = %q, want %q", dbPath, want)
	}

	seedsPath, err := p.crossSigningSeedsPath()
	if err != nil {
		t.Fatalf("crossSigningSeedsPath() error = %v", err)
	}
	if want := filepath.Join(dataDir, "matrix-cross-signing-DEVICE.json"); seedsPath != want {
		t.Fatalf("crossSigningSeedsPath() = %q, want %q", seedsPath, want)
	}
}
