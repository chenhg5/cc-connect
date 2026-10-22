package telegram

import (
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

// enrichLocation converts a location attachment into text content that AI agents
// can understand. Returns the enriched content string, or empty string if nothing to add.
func enrichLocation(msg *core.Message) string {
	if msg.Location == nil {
		return ""
	}
	return fmt.Sprintf("[Location] Latitude: %.6f, Longitude: %.6f",
		msg.Location.Latitude, msg.Location.Longitude)
}
