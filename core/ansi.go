package core

import "regexp"

var (
	ansiOSCRegexp    = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiCSIRegexp    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiSingleRegexp = regexp.MustCompile(`\x1b[@-Z\\-_]`)
)

// NormalizeOutgoingContent strips ANSI escape sequences from content before it
// is rendered on chat platforms.
func NormalizeOutgoingContent(content string) string {
	content = ansiOSCRegexp.ReplaceAllString(content, "")
	content = ansiCSIRegexp.ReplaceAllString(content, "")
	content = ansiSingleRegexp.ReplaceAllString(content, "")
	return content
}
