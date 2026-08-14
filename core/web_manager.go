package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateToken creates a random hex token.
func GenerateToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("cc-connect-next-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
