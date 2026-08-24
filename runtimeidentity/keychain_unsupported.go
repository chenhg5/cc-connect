//go:build !darwin

package runtimeidentity

import (
	"crypto/ed25519"
	"errors"
)

type unsupportedKeychain struct{}

func newPlatformKeychain(string) (platformKeychain, error) { return &unsupportedKeychain{}, nil }
func (*unsupportedKeychain) Load() (ed25519.PrivateKey, error) {
	return nil, errors.New("runtime identity: macOS Keychain is required")
}
func (*unsupportedKeychain) Save(ed25519.PrivateKey) error {
	return errors.New("runtime identity: macOS Keychain is required")
}
