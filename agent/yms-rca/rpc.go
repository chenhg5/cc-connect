package ymsagent

import "encoding/base64"

// stdBase64Encode wraps the stdlib helper so the rest of the package has a
// single, easy-to-grep name to use.
func stdBase64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}
