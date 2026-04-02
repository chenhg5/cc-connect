// Package channel contains platform-specific message preprocessing logic.
// Each file in this package handles enrichment/transformation for a specific
// platform or channel type, keeping the core engine agnostic of message format
// differences across platforms.
package channel

import "fmt"

// LocationData represents geographical coordinates, decoupled from core types
// to avoid circular imports.
type LocationData struct {
	Latitude  float64
	Longitude float64
}

// EnrichContent generates platform-specific text content from message attachments.
// For example, it converts location data into text that AI agents can understand.
// Returns the enriched content string, or empty string if nothing to add.
func EnrichContent(platform string, location *LocationData) string {
	if location == nil {
		return ""
	}
	switch platform {
	case "telegram":
		return fmt.Sprintf("[Location] Latitude: %.6f, Longitude: %.6f",
			location.Latitude, location.Longitude)
	default:
		return ""
	}
}
