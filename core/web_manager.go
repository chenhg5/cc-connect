package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	defaultMgmtPort   = 9820
	defaultBridgePort = 9810
)

// GenerateToken creates a random hex token.
func GenerateToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cc-connect-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
