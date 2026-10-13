package main

import "strings"

// The registration API returns the scanning user's open_id together with the
// application's credentials. See the official SDK's RegisterApp return values:
// https://github.com/larksuite/oapi-sdk-go/blob/v3_main/README.zh.md
// Use that identity, never a parent's app-scoped ID or the first message sender.
// Reject malformed IDs before they reach comma-separated administrator lists.
func validRegistrationOwner(id string) bool {
	if !strings.HasPrefix(id, "ou_") || len(id) <= 3 {
		return false
	}
	for _, c := range id[3:] {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}
