package core

// Tests for issue #864 — sanitize metadata.user_id to satisfy the
// Anthropic API pattern ^[a-zA-Z0-9_-]+$ before forwarding to upstream
// providers (e.g. DeepSeek Anthropic-compatible endpoint).
//
// The fix lives in core/providerproxy.go (rewriteThinkingInRequest +
// sanitizeAnthropicUserID). These tests pin the contract:
//   - pure-ASCII user_ids are returned unchanged
//   - non-ASCII (Chinese, emoji, accented Latin, special chars) are
//     replaced with a deterministic sha256-based hash prefixed with "h_"
//   - empty strings are returned unchanged (so the field is preserved
//     for upstream providers that accept or ignore it)
//   - strings over 256 bytes are also replaced
//   - the rewrite hook processes a full /v1/messages request body,
//     including the metadata.user_id field, without disturbing the
//     rest of the payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestSanitizeAnthropicUserID_PureASCIIPassThrough is the no-op case:
// any user_id that already satisfies ^[a-zA-Z0-9_-]+$ must be returned
// verbatim. Otherwise we would silently corrupt session identifiers
// for providers that use them for analytics.
func TestSanitizeAnthropicUserID_PureASCIIPassThrough(t *testing.T) {
	cases := []string{
		"alice",
		"user_123",
		"team-alpha-pod-3",
		"0",
		"_underscored_",
		strings.Repeat("a", maxAnthropicUserIDLen), // exactly 256
	}
	for _, in := range cases {
		if got := sanitizeAnthropicUserID(in); got != in {
			t.Errorf("sanitizeAnthropicUserID(%q) = %q, want unchanged", in, got)
		}
	}
}

// TestSanitizeAnthropicUserID_NonASCIIStringsHashed covers the issue
// reporter's actual case (Feishu open_id, plus a few related shapes).
// Inputs that contain any rune outside [a-zA-Z0-9_-] are replaced with
// the deterministic "h_" + 32-hex hash form.
func TestSanitizeAnthropicUserID_NonASCIIStringsHashed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"chinese", "张三_abc"},
		{"feishu_open_id_with_unicode_prefix", "ou_张三_3c2f1a"},
		{"chinese_with_digit_prefix", "user_李四_42"},
		{"emoji", "user_🚀_42"},
		{"emoji_only", "🔥"},
		{"emoji_compound", "👨‍👩‍👧‍👦_family"},
		{"accented_latin", "café_naïve"},
		{"cyrillic", "ivan_иван"},
		{"japanese", "山田太郎"},
		{"korean", "홍길동"},
		{"special_chars_dot", "user.with.dots"},
		{"special_chars_at", "user@host"},
		{"special_chars_slash", "team/lead"},
		{"special_chars_space", "alice bob"},
		{"tab_and_newline", "alice\tbob\n"},
		{"unicode_quote", "alice’s_laptop"},
		{"trailing_non_ascii", "alice_🙂"},
		{"leading_non_ascii", "🙂_alice"},
		{"single_non_ascii_rune", "é"},
		{"null_byte", "alice\x00bob"},
		{"overlong_input", strings.Repeat("a", maxAnthropicUserIDLen) + "中文"},
		{"only_non_ascii_long", strings.Repeat("中", 100)},
	}
	hashedRe := regexp.MustCompile(`^h_[0-9a-f]{32}$`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAnthropicUserID(tc.in)
			if got == tc.in {
				t.Errorf("sanitizeAnthropicUserID(%q) returned unchanged, want hashed form", tc.in)
			}
			if !hashedRe.MatchString(got) {
				t.Errorf("sanitizeAnthropicUserID(%q) = %q, want h_<32 hex chars>", tc.in, got)
			}
			if len(got) > maxAnthropicUserIDLen {
				t.Errorf("sanitizeAnthropicUserID(%q) length = %d, want ≤ %d", tc.in, len(got), maxAnthropicUserIDLen)
			}
		})
	}
}

// TestSanitizeAnthropicUserID_HashIsDeterministic pins the contract that
// the same input always produces the same hash. This is what lets
// operators cross-reference logs.
func TestSanitizeAnthropicUserID_HashIsDeterministic(t *testing.T) {
	in := "张三_abc"
	first := sanitizeAnthropicUserID(in)
	second := sanitizeAnthropicUserID(in)
	if first != second {
		t.Errorf("sanitizeAnthropicUserID is not deterministic: %q vs %q", first, second)
	}

	// And the hash must match what we expect from sha256(in)[:16] hex.
	sum := sha256.Sum256([]byte(in))
	want := "h_" + hex.EncodeToString(sum[:16])
	if first != want {
		t.Errorf("sanitizeAnthropicUserID(%q) = %q, want %q", in, first, want)
	}
}

// TestSanitizeAnthropicUserID_EmptyStringPassThrough: the helper must
// not hash an empty string. The proxy code path also short-circuits
// on empty (so the field is preserved), but the helper itself should
// also be a no-op for "" so it's safe to call elsewhere.
func TestSanitizeAnthropicUserID_EmptyStringPassThrough(t *testing.T) {
	if got := sanitizeAnthropicUserID(""); got != "" {
		t.Errorf("sanitizeAnthropicUserID(\"\") = %q, want \"\"", got)
	}
}

// TestSanitizeAnthropicUserID_OverlongReplaced guards the 256-byte cap.
// A pure-ASCII string of 257 bytes still fails the pattern check on
// length alone and must be replaced.
func TestSanitizeAnthropicUserID_OverlongReplaced(t *testing.T) {
	in := strings.Repeat("a", maxAnthropicUserIDLen+1)
	got := sanitizeAnthropicUserID(in)
	if got == in {
		t.Errorf("expected overlong ASCII user_id to be replaced, got unchanged")
	}
	if got != "h_"+hex.EncodeToString(sha256SumFirst16Bytes(in)) {
		t.Errorf("overlong hash mismatch")
	}
}

func sha256SumFirst16Bytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	out := make([]byte, 16)
	copy(out, sum[:16])
	return out
}

// TestRewriteThinkingInRequest_SanitizesUserID exercises the full
// request-body hook end to end: build a Claude /v1/messages payload
// whose metadata.user_id is a Feishu open_id, run it through
// rewriteThinkingInRequest, and verify the rewritten body has a
// sanitized user_id and the rest of the fields are byte-identical.
func TestRewriteThinkingInRequest_SanitizesUserID(t *testing.T) {
	origBody := []byte(`{
  "model": "deepseek-chat",
  "messages": [{"role": "user", "content": "hi"}],
  "metadata": {"user_id": "张三_abc", "session_id": "s-1"}
}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	// rewriteThinkingInRequest reads the body and re-installs it; we then
	// re-read the body ourselves to inspect the result.
	rewriteThinkingInRequest(req, "enabled")

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}

	var got struct {
		Model    string `json:"model"`
		Messages []any  `json:"messages"`
		Metadata struct {
			UserID    string `json:"user_id"`
			SessionID string `json:"session_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal rewritten body: %v\nbody=%s", err, gotBody)
	}
	if got.Model != "deepseek-chat" {
		t.Errorf("model = %q, want %q (untouched)", got.Model, "deepseek-chat")
	}
	if len(got.Messages) != 1 {
		t.Errorf("messages = %d, want 1 (untouched)", len(got.Messages))
	}
	if got.Metadata.SessionID != "s-1" {
		t.Errorf("session_id = %q, want %q (untouched)", got.Metadata.SessionID, "s-1")
	}
	if got.Metadata.UserID == "张三_abc" {
		t.Errorf("user_id = %q, want sanitized (still non-ASCII)", got.Metadata.UserID)
	}
	if !regexp.MustCompile(`^h_[0-9a-f]{32}$`).MatchString(got.Metadata.UserID) {
		t.Errorf("user_id = %q, want h_<32 hex chars>", got.Metadata.UserID)
	}
}

// TestRewriteThinkingInRequest_LeavesCleanUserIDAlone is the regression
// guard for the constraint "不影响其他 API ... 的 user_id 行为". A
// already-compliant user_id must come out byte-identical.
func TestRewriteThinkingInRequest_LeavesCleanUserIDAlone(t *testing.T) {
	origBody := []byte(`{
  "model": "claude-opus-4-7",
  "messages": [{"role": "user", "content": "hi"}],
  "metadata": {"user_id": "user_abc-123"}
}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	rewriteThinkingInRequest(req, "")

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	if !bytes.Contains(gotBody, []byte(`"user_id": "user_abc-123"`)) {
		t.Errorf("expected user_id unchanged, got body:\n%s", gotBody)
	}
}

// TestRewriteThinkingInRequest_EmptyUserIDLeftAlone: per Anthropic spec
// the field is optional, and the proxy code path only sanitizes a
// non-empty user_id. The helper itself also returns "" for "". This
// test pins both behaviors at the request-body level.
func TestRewriteThinkingInRequest_EmptyUserIDLeftAlone(t *testing.T) {
	origBody := []byte(`{"model": "x", "messages": [], "metadata": {"user_id": ""}}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	rewriteThinkingInRequest(req, "")

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	if !bytes.Contains(gotBody, []byte(`"user_id": ""`)) {
		t.Errorf("expected empty user_id preserved, got body:\n%s", gotBody)
	}
}

// TestRewriteThinkingInRequest_NoMetadataBlock is a no-op path: the
// request has no metadata field at all. The body must be re-installed
// unchanged (and the test would catch a regression that drops the body
// entirely).
func TestRewriteThinkingInRequest_NoMetadataBlock(t *testing.T) {
	origBody := []byte(`{"model": "x", "messages": []}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	rewriteThinkingInRequest(req, "")

	gotBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	if !bytes.Equal(gotBody, origBody) {
		t.Errorf("body changed despite no metadata block:\nwant: %s\ngot:  %s", origBody, gotBody)
	}
}

// TestIsAnthropicUserIDCompliant spot-checks the predicate directly so a
// future refactor of sanitizeAnthropicUserID can't drift from the
// documented pattern.
func TestIsAnthropicUserIDCompliant(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"a", true},
		{"Z_0-9", true},
		{"a.b", false},
		{"alice bob", false},
		{"张三", false},
		{"user_🚀", false},
		{strings.Repeat("a", maxAnthropicUserIDLen), true},
		{strings.Repeat("a", maxAnthropicUserIDLen+1), false},
	}
	for _, tc := range cases {
		if got := isAnthropicUserIDCompliant(tc.in); got != tc.want {
			t.Errorf("isAnthropicUserIDCompliant(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRewriteThinkingInRequest_LogsWarningWhenSanitized exercises the
// "log warning when sanitized" acceptance criterion. We capture the
// global slog default logger output to a buffer for the duration of
// the call and assert that a Warn-level line was emitted with the
// expected field shape (no need to pin the exact sanitized value).
func TestRewriteThinkingInRequest_LogsWarningWhenSanitized(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	origBody := []byte(`{"model":"x","messages":[],"metadata":{"user_id":"张三_abc"}}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	rewriteThinkingInRequest(req, "")

	out := logBuf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN-level log, got:\n%s", out)
	}
	if !strings.Contains(out, "sanitized metadata.user_id") {
		t.Errorf("expected log message about sanitized user_id, got:\n%s", out)
	}
	if !strings.Contains(out, "original_len=") {
		t.Errorf("expected original_len field, got:\n%s", out)
	}
}

// TestRewriteThinkingInRequest_NoLogWhenClean: a clean user_id must
// NOT emit any log line. This is the "不打扰" half of "便于诊断, 不打扰".
func TestRewriteThinkingInRequest_NoLogWhenClean(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	origBody := []byte(`{"model":"x","messages":[],"metadata":{"user_id":"user_abc-123"}}`)
	req := httptest.NewRequest("POST", "http://proxy/v1/messages", bytes.NewReader(origBody))
	rewriteThinkingInRequest(req, "")

	if logBuf.Len() != 0 {
		t.Errorf("expected no log output for clean user_id, got:\n%s", logBuf.String())
	}
}
