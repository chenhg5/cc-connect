//go:build darwin

package runtimeidentity

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type macOSKeychain struct {
	account string
}

func newPlatformKeychain(directory string) (platformKeychain, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("runtime identity: resolve state directory: %w", err)
	}
	return &macOSKeychain{account: absolute}, nil
}

func (k *macOSKeychain) Load() (ed25519.PrivateKey, error) {
	command := exec.Command("/usr/bin/security", "find-generic-password", "-s", "cc-connect-runtime", "-a", k.account, "-w")
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 44 {
			return nil, errKeyNotFound
		}
		return nil, fmt.Errorf("runtime identity: read macOS Keychain: %w", err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(output)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("runtime identity: macOS Keychain value is invalid")
	}
	return ed25519.PrivateKey(raw), nil
}

func (k *macOSKeychain) Save(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("runtime identity: invalid Ed25519 private key")
	}
	encoded := base64.RawStdEncoding.EncodeToString(key)
	// security(1) 的 add-generic-password 需要将 -w 的值作为参数传入；省略该值只会读取已有条目。
	command := exec.Command("/usr/bin/security", keychainSaveArguments(k.account, encoded)...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("runtime identity: save macOS Keychain item: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func keychainSaveArguments(account, encoded string) []string {
	return []string{"add-generic-password", "-U", "-s", "cc-connect-runtime", "-a", account, "-w", encoded}
}
