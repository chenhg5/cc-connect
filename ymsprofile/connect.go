package ymsprofile

import (
	"strings"
)

// ParseConnectTarget extracts the target connection name from a `/connect <name>`
// command prompt sent through cc-connect. It strips an optional
// inject_sender header line of the form `[cc-connect ...]` that the engine
// prepends when SetInjectSender(true) is enabled (see core/engine.go).
//
// Examples:
//
//	"/connect yms-dev"                                       -> ("yms-dev", true)
//	"  /connect yms-dev  "                                   -> ("yms-dev", true)
//	"[cc-connect sender_id=u1 platform=feishu]\n/connect ym" -> ("ym", true)
//	"/connect"                                               -> ("", false)
//	"hello"                                                  -> ("", false)
//
// Returns ("", false) when the prompt is not a connect command.
func ParseConnectTarget(prompt string) (string, bool) {
	body := stripInjectSenderHeader(prompt)
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "/connect") {
		return "", false
	}
	rest := strings.TrimPrefix(body, "/connect")
	if rest == "" {
		// "/connect" with no separator after — not a connect command.
		return "", false
	}
	// First char after "/connect" must be whitespace; otherwise it's a
	// different slash command that happens to share the prefix.
	if rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	// Take the first whitespace-separated token as the connection name.
	if idx := strings.IndexAny(rest, " \t\n\r"); idx > 0 {
		rest = rest[:idx]
	}
	// Strip surrounding quotes if the user typed `/connect "yms-dev"`.
	rest = strings.Trim(rest, `"'`)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// stripInjectSenderHeader removes a single leading `[cc-connect ...]` line
// (plus its trailing newline) if present. It only inspects the first line
// and only strips it when it both starts with `[cc-connect` and ends with
// `]` on the same line. Multi-line headers are not supported because the
// engine never emits one.
func stripInjectSenderHeader(prompt string) string {
	trimmed := strings.TrimLeft(prompt, " \t")
	if !strings.HasPrefix(trimmed, "[cc-connect") {
		return prompt
	}
	newline := strings.Index(trimmed, "\n")
	if newline < 0 {
		// header but no body — nothing useful left.
		return ""
	}
	headerLine := trimmed[:newline]
	// Require the header to end with `]` so we don't accidentally consume
	// a multi-line value containing `[cc-connect ...` text.
	if !strings.HasSuffix(strings.TrimRight(headerLine, " \t"), "]") {
		return prompt
	}
	return trimmed[newline+1:]
}
