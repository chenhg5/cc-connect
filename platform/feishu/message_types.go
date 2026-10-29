package feishu

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// feishuDisplayLocation is the zone timestamps in message placeholders are
// rendered in. Tests pin it.
var feishuDisplayLocation = time.Local

// maxPlaceholderFieldRunes caps each free-text field (task summary, vote
// topic, …) so a placeholder stays one short line.
const maxPlaceholderFieldRunes = 200

// describeFeishuMessage renders a message type that has no handler of its own
// as one line of text for the agent. Types with a documented content structure
// are summarised; any other type becomes "[unsupported Feishu message: <type>]".
// The raw content JSON is never passed through.
//
// Content structures:
// https://open.feishu.cn/document/server-docs/im-v1/message-content-description/message_content
func describeFeishuMessage(msgType, content string) string {
	var body map[string]json.RawMessage
	_ = json.Unmarshal([]byte(content), &body) // a missing or malformed body degrades to the bare label

	switch msgType {
	case "video_chat":
		return placeholder("video meeting",
			placeholderString(body["topic"]),
			prefixed("meeting no. ", placeholderString(body["meet_number"])),
			prefixed("started ", placeholderTime(body["start_time"])),
			prefixed("ended ", placeholderTime(body["end_time"])))
	case "share_chat":
		return placeholder("shared chat", placeholderString(body["chat_id"]))
	case "share_user":
		return placeholder("shared contact", placeholderString(body["user_id"]))
	case "location":
		name := placeholderString(body["name"])
		lat, lng := placeholderString(body["latitude"]), placeholderString(body["longitude"])
		if lat != "" && lng != "" {
			name = strings.TrimSpace(name + " (" + lat + ", " + lng + ")")
		}
		return placeholder("location", name)
	case "share_calendar_event", "calendar", "general_calendar":
		return placeholder("calendar event",
			placeholderString(body["summary"]),
			prefixed("starts ", placeholderTime(body["start_time"])),
			prefixed("ends ", placeholderTime(body["end_time"])))
	case "todo":
		summary := ""
		if raw := body["summary"]; len(raw) > 0 {
			summary = oneLine(extractPostPlainText(string(raw)))
		}
		return placeholder("task", summary, prefixed("due ", placeholderTime(body["due_time"])))
	case "vote":
		var options []string
		_ = json.Unmarshal(body["options"], &options)
		for i := range options {
			options[i] = oneLine(options[i])
		}
		return placeholder("vote",
			placeholderString(body["topic"]),
			prefixed("options: ", oneLine(strings.Join(options, " / "))))
	}
	return fmt.Sprintf("[unsupported Feishu message: %s]", msgType)
}

// describesFeishuMessageType reports whether describeFeishuMessage has a
// summary for msgType (as opposed to the generic unsupported placeholder).
func describesFeishuMessageType(msgType string) bool {
	switch msgType {
	case "video_chat", "share_chat", "share_user", "location",
		"share_calendar_event", "calendar", "general_calendar", "todo", "vote":
		return true
	}
	return false
}

// placeholder joins the non-empty parts as "[label: a, b]", or "[label]".
func placeholder(label string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return "[" + label + "]"
	}
	return "[" + label + ": " + strings.Join(kept, ", ") + "]"
}

func prefixed(prefix, v string) string {
	if v == "" {
		return ""
	}
	return prefix + v
}

// placeholderString returns a JSON string or number as one line of text, and
// "" for null, absent or structured values.
func placeholderString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return oneLine(s)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// placeholderTime formats a millisecond Unix timestamp (string or number), or
// returns "" when the value is absent, null or not a plausible millisecond
// timestamp. (The documented video_chat example, "6745784522794598413", is not
// one; real cards carry milliseconds.)
func placeholderTime(raw json.RawMessage) string {
	ms, err := strconv.ParseInt(placeholderString(raw), 10, 64)
	if err != nil || ms < 1e12 || ms >= 1e14 {
		return ""
	}
	return time.UnixMilli(ms).In(feishuDisplayLocation).Format("2006-01-02 15:04 -07:00")
}

// oneLine collapses whitespace (including newlines) and caps the length.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxPlaceholderFieldRunes {
		s = string([]rune(s)[:maxPlaceholderFieldRunes]) + "…"
	}
	return s
}
