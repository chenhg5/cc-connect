# Code Review: Commit 1d15eff — ImageSender for 6 Platforms

## Summary

This commit implements `core.ImageSender` for Discord, Slack, DingTalk, WeChat Work, QQ, and QQBot. Each platform sends images via its native API. The implementation follows existing patterns (Reply/Send, reply context validation, error wrapping).

---

## Platform Implementation Summaries

### Discord (`platform/discord/discord.go`)
- Uses `ChannelMessageSendComplex` with `discordgo.File` attachment
- Supports both `replyContext` (channel messages) and `*interactionReplyCtx` (slash commands)
- Uses `newFile()` factory returning `bytes.NewReader(img.Data)` to avoid io.Reader exhaustion on fallback
- First interaction reply edits the deferred response; subsequent replies use followup messages
- Proper `rc.mu` lock for `firstDone` state machine

### Slack (`platform/slack/slack.go`)
- Uses `UploadFileV2Context` with `Reader`, `FileSize`, `Filename`, `Channel`, `ThreadTimestamp`
- Passes `ctx` for cancellation
- Thread support via `rc.timestamp` (thread_ts)

### DingTalk (`platform/dingtalk/dingtalk.go`)
- Refactored `uploadMedia(ctx, data, fileName, mediaType)` to support both voice and image
- Uploads via `oapi.dingtalk.com/media/upload`, then sends via `oToMessages/batchSend` with `msgKey: "sampleImageMsg"`
- Uses `json.Marshal` for `msgParam` (fixed from `fmt.Sprintf`)
- `defer resp.Body.Close()` and reads body on success for debugging
- Uses `http.NewRequestWithContext` for context propagation

### WeChat Work (`platform/wecom/wecom.go`)
- Two-step: `uploadImageMedia` (multipart to media API) → message send API
- `defer resp.Body.Close()` on both upload and send
- Uses `writer.FormDataContentType()` and `body` correctly (boundary set at creation; body populated after `writer.Close()`)

### QQ / OneBot v11 (`platform/qq/qq.go`)
- Sends base64-encoded image via `send_group_msg` or `send_private_msg` with CQ segment `{"type":"image","data":{"file":"base64://..."}}`
- Uses existing `callAPI` over WebSocket
- No HTTP; no `resp.Body.Close()` concerns

### QQBot / Official API v2 (`platform/qqbot/qqbot.go`)
- Two-step: `uploadRichMedia` (base64 to `/files` API) → POST to `/messages` with `msg_type: 7`
- New `apiRequestJSON` for upload (decodes response into result struct)
- 401 retry with token refresh; `http.NewRequest` error is now handled in `apiRequestJSON` (fixed in commit)
- **Note**: `apiRequest` (used for the final message send) still ignores `http.NewRequest` error on 401 retry path — same bug, different function

---

## Issues Found

### 1. QQBot: `apiRequest` ignores `http.NewRequest` error (401 retry path)
**File:** `platform/qqbot/qqbot.go`  
**Lines:** 799–802

```go
req2, _ := http.NewRequest(method, url, bodyReader)
req2.Header.Set("Authorization", "QQBot "+token)
```

If `http.NewRequest` fails, `req2` is nil and `core.HTTPClient.Do(req2)` will panic. The commit fixed this in `apiRequestJSON` but `apiRequest` (used by `SendImage` for the message send) still has the bug.

**Recommendation:** Add error check:
```go
req2, err := http.NewRequest(method, url, bodyReader)
if err != nil {
    return fmt.Errorf("qqbot: build retry request: %w", err)
}
```

### 2. Ignored `json.Marshal` errors (minor)
**Files:** DingTalk, WeChat Work

- `dingtalk.go:358`: `msgParamBytes, _ := json.Marshal(...)` — error ignored
- `wecom.go:416`: `body, _ := json.Marshal(payload)` — error ignored

Marshal failure is rare for these simple maps, but handling the error would be more robust.

### 3. No validation for empty `img.Data`
**All platforms**

None of the implementations check `len(img.Data) == 0`. Sending empty image data may produce confusing API errors or unexpected behavior. Consider adding:

```go
if len(img.Data) == 0 {
    return fmt.Errorf("platform: SendImage: empty image data")
}
```

### 4. QQBot / DingTalk: Context not passed to HTTP requests
**Files:** `platform/qqbot/qqbot.go`, `platform/dingtalk/dingtalk.go` (upload path)

- QQBot `apiRequest` and `apiRequestJSON` use `http.NewRequest` (no context). `SendImage` receives `ctx` but does not pass it, so cancellation does not propagate.
- DingTalk `SendImage` and `uploadMedia` use `http.NewRequestWithContext(ctx, ...)` — OK.

This matches existing patterns in QQBot; improving it would be a broader refactor.

---

## Logic Correctness

| Platform | API usage | Error handling | Resource cleanup |
|----------|-----------|----------------|------------------|
| Discord | ✓ | ✓ | N/A (no HTTP) |
| Slack | ✓ | ✓ | N/A (client handles) |
| DingTalk | ✓ | ✓ | ✓ `defer resp.Body.Close()` |
| WeChat | ✓ | ✓ | ✓ `defer resp.Body.Close()` |
| QQ | ✓ | ✓ | N/A (WebSocket) |
| QQBot | ✓ | ✓ | ✓ `defer resp.Body.Close()` |

---

## Concurrency Safety

- **Discord**: `interactionReplyCtx` uses `rc.mu` for `firstDone`; lock is held correctly.
- **Slack**: No shared mutable state in `SendImage`.
- **DingTalk**: `getAccessToken` uses `tokenMu`; `uploadMedia` and `SendImage` do not add new shared state.
- **WeChat**: Token cache uses `tokenCache.mu`; `SendImage` does not add new shared state.
- **QQ**: `callAPI` uses `p.mu` for conn; `SendImage` goes through `callAPI` — OK.
- **QQBot**: `nextMsgSeq` uses `msgSeqMu`; `uploadRichMedia` and `apiRequest` do not introduce races.

No new concurrency issues identified.

---

## Security

- **Tokens**: Access tokens are not included in error messages or logs. DingTalk and WeChat put tokens in URLs; those URLs are not logged.
- **Secrets**: No API keys or secrets appear in user-facing errors.
- **Redaction**: Existing `core.RedactToken` is not used in these paths; tokens are not exposed in error strings.

No security issues found.

---

## Breaking Changes

- **Interface**: `ImageSender` is an optional interface; platforms opt in. No changes to existing interfaces.
- **Config**: No config changes.
- **DingTalk**: `uploadMedia` signature changed from `(ctx, audio, format)` to `(ctx, data, fileName, mediaType)`. `SendAudio` was updated accordingly. No external breaking change.

---

## Edge Cases

| Case | Handling |
|------|----------|
| `img.Data` nil | `bytes.NewReader(nil)` yields empty reader; `part.Write(nil)` writes 0 bytes — no panic |
| `img.Data` empty | No explicit check; may produce API errors |
| `img.FileName` empty | All platforms default to `"image.png"` |
| Invalid `rctx` type | All platforms return `fmt.Errorf("...: invalid reply context type %T", rctx)` |
| Large files | No explicit size limits; platform APIs will enforce their own limits |

---

## Code Style

- Consistent with existing platform code: error wrapping with `fmt.Errorf("platform: ...: %w", err)`
- Uses `slog` for logging
- `var _ core.ImageSender = (*Platform)(nil)` compile-time checks present
- Naming and structure match other `Send*` methods

---

## Overall Assessment

**Verdict: Safe to ship with one recommended fix**

The implementation is solid. The only notable issue is the QQBot `apiRequest` 401 retry path ignoring `http.NewRequest` error, which can cause a nil panic in an edge case. Fixing that is a small, low-risk change.

Optional improvements:
- Add `len(img.Data) == 0` validation
- Handle `json.Marshal` errors in DingTalk and WeChat
- Propagate context to QQBot HTTP requests (larger refactor)
