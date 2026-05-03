# LINE Reply/Push Hybrid Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `platform/line` 的 `Reply()` 從「全部 Push」改成「Reply + Push 混合」：webhook 收到的 reply token 存進 cache，Reply() 時若 token 還新鮮（< 50s）走 LINE Reply API（免費），否則走 Push API。

**Architecture:** 在 `platform/line/Platform` 內加一個 `sync.Map`（key=`targetID`, value=`{token, time}`），webhook handler 寫入、`Reply()` 用 `LoadAndDelete` 取出。Reply API 失敗且錯誤訊息明確指出「reply token 無效/過期」才退回 Push；其他錯誤回傳，避免重複送訊息。背景 goroutine 每 50s 清過期 entry。

**Tech Stack:** Go 1.21+, `github.com/line/line-bot-sdk-go/v8/linebot/messaging_api`（已引入），`sync.Map`、`context`、標準 `testing`。

**參考設計：** `docs/superpowers/specs/2026-05-03-line-reply-push-hybrid-design.md`

---

## File Structure

| 檔案 | 動作 | 責任 |
|---|---|---|
| `platform/line/line.go` | 修改 | Platform 結構加欄位、webhook 寫入 cache、`Reply()` 改 dispatch |
| `platform/line/dispatch.go` | **新建** | `lineClient` interface、`tokenEntry`、`dispatchReply`、`pushAll`、`sweepExpiredTokens`、錯誤判定 helpers |
| `platform/line/dispatch_test.go` | **新建** | dispatch 全部單元測試（用 fake client 注入） |
| `platform/line/line_test.go` | 不動 | 既有測試保留 |

決策理由：dispatch 邏輯 ~150 行、有獨立內部介面（`lineClient`），切到 `dispatch.go` 讓 `line.go` 維持「webhook handler + 流程」職責，符合 CLAUDE.md「函式超過 ~80 行就拆」的精神。

## SDK 重點（實作時參考）

- `bot.ReplyMessage(*messaging_api.ReplyMessageRequest) (*messaging_api.ReplyMessageResponse, error)` — 錯誤格式為 `fmt.Errorf("unexpected status code: %d, %s", status, body)`，需要字串解析判定。
- `bot.PushMessage(*messaging_api.PushMessageRequest, retryKey string) (*messaging_api.PushMessageResponse, error)` — `retryKey` 傳 `""` 即可，與現有用法一致。
- `ReplyMessageRequest` 欄位：`ReplyToken string`、`Messages []messaging_api.MessageInterface`、`NotificationDisabled bool`。
- 一次 Reply 最多 5 個 message object（LINE 平台限制）。

---

## Task 1: 抽出 `lineClient` interface（為了可測性）

**Files:**
- Modify: `platform/line/line.go`（Platform.bot 欄位型別、`Start()`）
- Create: `platform/line/dispatch.go`（暫先放介面）

- [ ] **Step 1: 建立 `dispatch.go` 檔案，定義 interface**

新增 `platform/line/dispatch.go`：

```go
package line

import (
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

// lineClient 把 messaging_api.MessagingApiAPI 用到的方法抽成 interface，
// 方便單元測試注入 fake。實作端只有兩個方法，命名與 SDK 一致以便對讀。
type lineClient interface {
	ReplyMessage(req *messaging_api.ReplyMessageRequest) (*messaging_api.ReplyMessageResponse, error)
	PushMessage(req *messaging_api.PushMessageRequest, retryKey string) (*messaging_api.PushMessageResponse, error)
}
```

- [ ] **Step 2: 改 Platform.bot 型別為 `lineClient`**

`platform/line/line.go` 約 line 43，把：

```go
bot            *messaging_api.MessagingApiAPI
```

改為：

```go
bot            lineClient
```

`Start()` 內 `p.bot = bot` 不用改（`*MessagingApiAPI` 自動滿足 interface）。

- [ ] **Step 3: 編譯確認**

Run: `go build ./platform/line/...`
Expected: PASS（無變動行為）

- [ ] **Step 4: 跑既有測試確認不退化**

Run: `go test ./platform/line/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add platform/line/dispatch.go platform/line/line.go
git commit -m "refactor(line): extract lineClient interface for testability"
```

---

## Task 2: 加 token cache 欄位與 webhook 寫入

**Files:**
- Modify: `platform/line/line.go`（Platform 加欄位、`handleEvent` 內寫 cache）
- Modify: `platform/line/dispatch.go`（加 `tokenEntry`、`cacheReplyToken`）
- Create: `platform/line/dispatch_test.go`

- [ ] **Step 1: 在 `dispatch.go` 加 cache 結構與常數**

於 `dispatch.go` interface 之後追加：

```go
import (
	"sync"
	"time"
)

const (
	replyTokenTTL = 50 * time.Second
	replyMaxBatch = 5  // LINE Reply API 一次最多 5 段
	sweepInterval = 50 * time.Second
)

// tokenEntry 是 cache value：reply token 字串 + 入庫時刻。
type tokenEntry struct {
	token string
	at    time.Time
}

// cacheReplyToken 寫入或覆寫指定 targetID 的 token。
// 多人在同一 group 同時 @bot 並且兩個 token 都來得及用是極罕見，
// 後到覆寫策略「最新的最有可能在 50s 內被回覆」合理。
func (p *Platform) cacheReplyToken(targetID, token string) {
	if targetID == "" || token == "" {
		return
	}
	p.replyTokens.Store(targetID, tokenEntry{token: token, at: time.Now()})
}

// loadAndDeleteFreshToken 取出 token；若不存在或已過期，回傳 ok=false。
// 「取出即刪」達成 single-use 語意（streaming 第二段以後自動 cache miss）。
func (p *Platform) loadAndDeleteFreshToken(targetID string) (string, bool) {
	v, ok := p.replyTokens.LoadAndDelete(targetID)
	if !ok {
		return "", false
	}
	entry, ok := v.(tokenEntry)
	if !ok {
		return "", false
	}
	if time.Since(entry.at) >= replyTokenTTL {
		return "", false
	}
	return entry.token, true
}
```

注意：上面 import 區塊放在檔案頂部（與 step 1 的 import 合併）。

- [ ] **Step 2: 在 Platform 加 `replyTokens sync.Map` 欄位**

`platform/line/line.go` Platform struct 末尾加一行：

```go
type Platform struct {
	// ... 既有欄位 ...
	replyTokens   sync.Map // targetID -> tokenEntry  (Reply/Push hybrid dispatch)
}
```

- [ ] **Step 3: 寫測試 — cacheReplyToken / loadAndDeleteFreshToken 行為**

新建 `platform/line/dispatch_test.go`：

```go
package line

import (
	"testing"
	"time"
)

func TestCacheReplyToken_StoresAndReads(t *testing.T) {
	p := &Platform{}
	p.cacheReplyToken("U123", "tok-abc")

	got, ok := p.loadAndDeleteFreshToken("U123")
	if !ok {
		t.Fatal("expected fresh token, got miss")
	}
	if got != "tok-abc" {
		t.Errorf("token = %q, want %q", got, "tok-abc")
	}
}

func TestLoadAndDeleteFreshToken_SingleUse(t *testing.T) {
	p := &Platform{}
	p.cacheReplyToken("U123", "tok-abc")

	if _, ok := p.loadAndDeleteFreshToken("U123"); !ok {
		t.Fatal("first load: expected hit")
	}
	if _, ok := p.loadAndDeleteFreshToken("U123"); ok {
		t.Fatal("second load: expected miss after LoadAndDelete")
	}
}

func TestLoadAndDeleteFreshToken_Expired(t *testing.T) {
	p := &Platform{}
	// Manually inject an expired entry
	p.replyTokens.Store("U123", tokenEntry{
		token: "tok-old",
		at:    time.Now().Add(-2 * replyTokenTTL),
	})

	if _, ok := p.loadAndDeleteFreshToken("U123"); ok {
		t.Fatal("expected expired token to miss")
	}
}

func TestCacheReplyToken_Overwrite(t *testing.T) {
	p := &Platform{}
	p.cacheReplyToken("U123", "tok-1")
	p.cacheReplyToken("U123", "tok-2")

	got, _ := p.loadAndDeleteFreshToken("U123")
	if got != "tok-2" {
		t.Errorf("token = %q, want overwrite to %q", got, "tok-2")
	}
}

func TestCacheReplyToken_EmptyInputsIgnored(t *testing.T) {
	p := &Platform{}
	p.cacheReplyToken("", "tok")
	p.cacheReplyToken("U123", "")

	if _, ok := p.loadAndDeleteFreshToken(""); ok {
		t.Error("empty targetID should not be cached")
	}
	if _, ok := p.loadAndDeleteFreshToken("U123"); ok {
		t.Error("empty token should not be cached")
	}
}
```

- [ ] **Step 4: 跑測試應該 PASS（cache logic 已實作）**

Run: `go test ./platform/line/ -run "TestCacheReplyToken|TestLoadAndDelete" -v`
Expected: 5 個測試全 PASS。

- [ ] **Step 5: 在 `handleEvent` 寫 cache**

`platform/line/line.go` 約 line 162，找到 `targetID, targetType, userID := extractSource(e.Source)` 那一行，**在它之後**插入：

```go
// 記下 reply token，供後續 Reply() 用 LINE Reply API（免費，不吃 push quota）。
// token ~1min 過期且只能用一次；若 agent 處理超過 50s 就會自然 cache miss → Push。
if e.ReplyToken != "" {
	p.cacheReplyToken(targetID, e.ReplyToken)
}
```

- [ ] **Step 6: 編譯與既有測試**

Run: `go build ./platform/line/... && go test ./platform/line/...`
Expected: PASS（行為改變僅限 cache 寫入，尚未消費）

- [ ] **Step 7: Commit**

```bash
git add platform/line/dispatch.go platform/line/dispatch_test.go platform/line/line.go
git commit -m "feat(line): cache reply tokens on inbound webhook events"
```

---

## Task 3: 加 fake client 與 dispatchReply（fresh-token 路徑）

**Files:**
- Modify: `platform/line/dispatch.go`（加 `dispatchReply`、`pushAll` helpers）
- Modify: `platform/line/dispatch_test.go`（加 fake client + dispatch 測試）

- [ ] **Step 1: 寫測試 — fresh token 走 ReplyMessage**

於 `dispatch_test.go` 追加：

```go
import (
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

type replyCall struct {
	token string
	msgs  []messaging_api.MessageInterface
}

type pushCall struct {
	to   string
	msgs []messaging_api.MessageInterface
}

type fakeLineClient struct {
	replyCalls []replyCall
	pushCalls  []pushCall
	replyErr   error
	pushErr    error
}

func (f *fakeLineClient) ReplyMessage(req *messaging_api.ReplyMessageRequest) (*messaging_api.ReplyMessageResponse, error) {
	f.replyCalls = append(f.replyCalls, replyCall{token: req.ReplyToken, msgs: req.Messages})
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	return &messaging_api.ReplyMessageResponse{}, nil
}

func (f *fakeLineClient) PushMessage(req *messaging_api.PushMessageRequest, retryKey string) (*messaging_api.PushMessageResponse, error) {
	f.pushCalls = append(f.pushCalls, pushCall{to: req.To, msgs: req.Messages})
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return &messaging_api.PushMessageResponse{}, nil
}

func TestDispatchReply_FreshToken_UsesReply(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-fresh")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Fatalf("expected 1 ReplyMessage call, got %d", len(fake.replyCalls))
	}
	if fake.replyCalls[0].token != "tok-fresh" {
		t.Errorf("token = %q, want %q", fake.replyCalls[0].token, "tok-fresh")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("expected 0 PushMessage calls, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_NoToken_UsesPush(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}

	rc := replyContext{targetID: "U999", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 0 {
		t.Errorf("expected 0 ReplyMessage calls, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 1 {
		t.Fatalf("expected 1 PushMessage call, got %d", len(fake.pushCalls))
	}
	if fake.pushCalls[0].to != "U999" {
		t.Errorf("to = %q, want %q", fake.pushCalls[0].to, "U999")
	}
}
```

- [ ] **Step 2: 跑測試確認 FAIL**

Run: `go test ./platform/line/ -run TestDispatchReply -v`
Expected: FAIL（`dispatchReply` 尚未實作）

- [ ] **Step 3: 在 `dispatch.go` 實作 `dispatchReply` 與 `pushAll`**

於 `dispatch.go` 追加：

```go
import (
	"fmt"
	"log/slog"
)

// pushAll 把 messages 一段一段用 PushMessage 送出。錯誤即終止並回傳。
func (p *Platform) pushAll(rc replyContext, messages []string, reason string) error {
	for _, text := range messages {
		_, err := p.bot.PushMessage(
			&messaging_api.PushMessageRequest{
				To: rc.targetID,
				Messages: []messaging_api.MessageInterface{
					messaging_api.TextMessage{Text: text},
				},
			}, "",
		)
		if err != nil {
			return fmt.Errorf("line: push message: %w", err)
		}
	}
	slog.Debug("line: dispatch", "method", "push", "reason", reason, "target_type", rc.targetType, "segments", len(messages))
	return nil
}

// dispatchReply 把切好的訊息送出去。若有新鮮 reply token，前 5 段走 Reply API
// （免費，不吃 push quota）；其餘段或 token 過期/缺失時走 Push API。
//
// 錯誤策略：
//   - Reply API 回 token 無效（400 + "reply token"/"expired"）→ 全部退回 Push
//   - Reply API 其他錯誤 → 回傳 error，**不**退回 Push（避免訊息可能已送出又重複）
//   - Push API 任何錯誤 → 回傳 error
func (p *Platform) dispatchReply(rc replyContext, messages []string) error {
	if len(messages) == 0 {
		return nil
	}

	token, ok := p.loadAndDeleteFreshToken(rc.targetID)
	if !ok {
		reason := "no_token"
		// 無法分辨「從未進 cache」與「已過期被 sweep 掉」，統一視為 no_token。
		return p.pushAll(rc, messages, reason)
	}

	// 取前 N 段（最多 5）走 Reply
	batchEnd := len(messages)
	if batchEnd > replyMaxBatch {
		batchEnd = replyMaxBatch
	}
	msgObjs := make([]messaging_api.MessageInterface, 0, batchEnd)
	for _, text := range messages[:batchEnd] {
		msgObjs = append(msgObjs, messaging_api.TextMessage{Text: text})
	}

	_, err := p.bot.ReplyMessage(&messaging_api.ReplyMessageRequest{
		ReplyToken: token,
		Messages:   msgObjs,
	})
	if err != nil {
		// Task 4 會處理 token-invalid fallback，這裡先簡化：任何錯回傳。
		return fmt.Errorf("line: reply message: %w", err)
	}

	slog.Debug("line: dispatch", "method", "reply", "reason", "fresh_token", "target_type", rc.targetType, "segments", batchEnd)

	// 還有剩餘段（>5）走 Push
	if batchEnd < len(messages) {
		return p.pushAll(rc, messages[batchEnd:], "after_reply_overflow")
	}
	return nil
}
```

- [ ] **Step 4: 跑測試確認 PASS**

Run: `go test ./platform/line/ -run TestDispatchReply -v`
Expected: 2 個測試 PASS

- [ ] **Step 5: 整體編譯與既有測試**

Run: `go build ./... && go test ./platform/line/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add platform/line/dispatch.go platform/line/dispatch_test.go
git commit -m "feat(line): dispatchReply with fresh-token Reply API path"
```

---

## Task 4: Token-invalid 時退回 Push；其他錯誤不退回

**Files:**
- Modify: `platform/line/dispatch.go`（加 `isReplyTokenInvalid`、改 `dispatchReply` 錯誤分支）
- Modify: `platform/line/dispatch_test.go`

- [ ] **Step 1: 寫測試 — token invalid 時退回 Push**

`dispatch_test.go` 追加：

```go
import "errors"

func TestDispatchReply_TokenInvalid_FallsBackToPush(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 400, {\"message\":\"Invalid reply token\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-bad")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hello"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Errorf("expected 1 ReplyMessage attempt, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("expected fallback to PushMessage (1 call), got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_TokenExpiredString_FallsBackToPush(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 400, {\"message\":\"The reply token has expired\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-old")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, []string{"hi"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("expected fallback Push, got %d push calls", len(fake.pushCalls))
	}
}

func TestDispatchReply_OtherError_NoFallback(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("network unreachable"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-x")

	rc := replyContext{targetID: "U123", targetType: "user"}
	err := p.dispatchReply(rc, []string{"hello"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("expected NO fallback Push (duplicate risk), got %d push calls", len(fake.pushCalls))
	}
}

func TestDispatchReply_500Error_NoFallback(t *testing.T) {
	fake := &fakeLineClient{
		replyErr: errors.New("unexpected status code: 500, {\"message\":\"internal server error\"}"),
	}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-x")

	rc := replyContext{targetID: "U123", targetType: "user"}
	err := p.dispatchReply(rc, []string{"hello"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("500 should NOT fallback (ambiguous outcome), got %d push calls", len(fake.pushCalls))
	}
}
```

- [ ] **Step 2: 跑測試確認 FAIL**

Run: `go test ./platform/line/ -run "TestDispatchReply_TokenInvalid|TestDispatchReply_TokenExpired|TestDispatchReply_OtherError|TestDispatchReply_500Error" -v`
Expected: FAIL（fallback 邏輯尚未實作；前兩個 fail，後兩個可能已 pass，因為現在的 dispatchReply 在錯誤時就 return）

- [ ] **Step 3: 在 `dispatch.go` 加錯誤判定 helper**

於 `dispatch.go` 追加（在 `dispatchReply` 上方）：

```go
import "strings"

// isReplyTokenInvalid 判斷 ReplyMessage 的錯誤是否「reply token 失效」
// （要走 Push fallback）。判斷邏輯：錯誤訊息含 "400" + ("reply token" 或 "expired")。
//
// 對比下，網路錯、5xx、未知 400 都不判定為 invalid，因為 Reply 可能已送達，
// 再 Push 一次會造成使用者收到重複訊息。
//
// SDK 目前的錯誤格式：fmt.Errorf("unexpected status code: %d, %s", status, body)
// 故用字串 contains 即可；若未來 SDK 改成 typed error，這裡再升級用 errors.As。
func isReplyTokenInvalid(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") {
		return false
	}
	return strings.Contains(msg, "reply token") || strings.Contains(msg, "expired")
}
```

- [ ] **Step 4: 改 `dispatchReply` 內錯誤分支**

把 step 3 (Task 3) 寫的 `dispatchReply` 中的：

```go
	if err != nil {
		// Task 4 會處理 token-invalid fallback，這裡先簡化：任何錯回傳。
		return fmt.Errorf("line: reply message: %w", err)
	}
```

替換為：

```go
	if err != nil {
		if isReplyTokenInvalid(err) {
			slog.Debug("line: dispatch", "method", "push", "reason", "reply_token_invalid", "target_type", rc.targetType)
			return p.pushAll(rc, messages, "reply_token_invalid")
		}
		// 其他錯誤：可能 Reply 已送出但回應失敗，不退回 Push 避免重複。
		slog.Debug("line: dispatch", "method", "reply", "reason", "reply_api_error", "target_type", rc.targetType, "error", err.Error())
		return fmt.Errorf("line: reply message: %w", err)
	}
```

- [ ] **Step 5: 跑測試確認 PASS**

Run: `go test ./platform/line/ -run "TestDispatchReply" -v`
Expected: 全部 6 個 dispatchReply 測試 PASS

- [ ] **Step 6: Commit**

```bash
git add platform/line/dispatch.go platform/line/dispatch_test.go
git commit -m "feat(line): fallback to Push only when reply token is invalid"
```

---

## Task 5: 多段訊息 batch 行為（≤5 一次 Reply、>5 前 5 Reply 後續 Push）

**Files:**
- Modify: `platform/line/dispatch_test.go`

（dispatchReply 已支援 batching，此任務只補測試確認邊界。）

- [ ] **Step 1: 寫邊界測試**

`dispatch_test.go` 追加：

```go
func TestDispatchReply_BatchUnder5(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Fatalf("want 1 ReplyMessage call, got %d", len(fake.replyCalls))
	}
	if got := len(fake.replyCalls[0].msgs); got != 3 {
		t.Errorf("want 3 messages in single Reply, got %d", got)
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("want 0 Push calls, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_BatchExactly5(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c", "d", "e"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 || len(fake.replyCalls[0].msgs) != 5 {
		t.Errorf("want 1 Reply with 5 msgs, got replies=%d msgs=%d", len(fake.replyCalls), len(fake.replyCalls[0].msgs))
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("want 0 Push calls at exactly 5, got %d", len(fake.pushCalls))
	}
}

func TestDispatchReply_BatchOver5_OverflowToPush(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok")

	msgs := []string{"a", "b", "c", "d", "e", "f", "g"}
	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.dispatchReply(rc, msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 || len(fake.replyCalls[0].msgs) != 5 {
		t.Errorf("want 1 Reply with first 5 msgs, got replies=%d msgs=%d", len(fake.replyCalls), len(fake.replyCalls[0].msgs))
	}
	if len(fake.pushCalls) != 2 {
		t.Errorf("want 2 Push calls for overflow, got %d", len(fake.pushCalls))
	}
}
```

- [ ] **Step 2: 跑測試確認 PASS**

Run: `go test ./platform/line/ -run TestDispatchReply_Batch -v`
Expected: 3 個測試 PASS（dispatchReply 已實作 batching）

- [ ] **Step 3: Commit**

```bash
git add platform/line/dispatch_test.go
git commit -m "test(line): batch boundary tests for dispatchReply"
```

---

## Task 6: 背景 sweeper goroutine

**Files:**
- Modify: `platform/line/dispatch.go`（加 `sweepExpiredTokens`）
- Modify: `platform/line/line.go`（Platform 加 cancel 欄位、`Start()` 啟動、`Stop()` 收掉）
- Modify: `platform/line/dispatch_test.go`

- [ ] **Step 1: 寫測試 — sweeper 清掉過期 entry**

`dispatch_test.go` 追加：

```go
func TestSweepExpiredTokens_RemovesExpired(t *testing.T) {
	p := &Platform{}
	p.replyTokens.Store("U_old", tokenEntry{token: "old", at: time.Now().Add(-2 * replyTokenTTL)})
	p.replyTokens.Store("U_new", tokenEntry{token: "new", at: time.Now()})

	p.sweepOnce()

	if _, ok := p.replyTokens.Load("U_old"); ok {
		t.Error("expected expired entry to be removed")
	}
	if _, ok := p.replyTokens.Load("U_new"); !ok {
		t.Error("expected fresh entry to remain")
	}
}
```

- [ ] **Step 2: 跑測試確認 FAIL**

Run: `go test ./platform/line/ -run TestSweepExpiredTokens -v`
Expected: FAIL（`sweepOnce` 尚未存在）

- [ ] **Step 3: 在 `dispatch.go` 實作 `sweepOnce` 與 `sweepExpiredTokens`**

於 `dispatch.go` 追加：

```go
import "context"

// sweepOnce 跑一輪掃描，清掉所有過期的 cache entry。
func (p *Platform) sweepOnce() {
	now := time.Now()
	p.replyTokens.Range(func(k, v any) bool {
		entry, ok := v.(tokenEntry)
		if !ok {
			p.replyTokens.Delete(k)
			return true
		}
		if now.Sub(entry.at) >= replyTokenTTL {
			p.replyTokens.Delete(k)
		}
		return true
	})
}

// sweepExpiredTokens 是 sweeper 主迴圈。Start() 啟動，Stop() 透過 ctx cancel 收掉。
func (p *Platform) sweepExpiredTokens(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepOnce()
		}
	}
}
```

- [ ] **Step 4: 跑 sweeper 測試確認 PASS**

Run: `go test ./platform/line/ -run TestSweepExpiredTokens -v`
Expected: PASS

- [ ] **Step 5: 在 `Platform` 加 `sweepCancel context.CancelFunc` 欄位**

`platform/line/line.go` Platform struct 末尾加：

```go
sweepCancel   context.CancelFunc
```

- [ ] **Step 6: 在 `Start()` 啟動 sweeper**

`Start()` 函式內，於 `go func() { ListenAndServe... }()` **之前**插入：

```go
ctx, cancel := context.WithCancel(context.Background())
p.sweepCancel = cancel
go p.sweepExpiredTokens(ctx)
```

- [ ] **Step 7: 在 `Stop()` 收掉 sweeper**

`Stop()` 函式（line ~461）改為：

```go
func (p *Platform) Stop() error {
	if p.sweepCancel != nil {
		p.sweepCancel()
	}
	if p.server != nil {
		return p.server.Shutdown(context.Background())
	}
	return nil
}
```

- [ ] **Step 8: 編譯與全測試**

Run: `go build ./... && go test ./platform/line/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add platform/line/dispatch.go platform/line/line.go platform/line/dispatch_test.go
git commit -m "feat(line): background sweeper for expired reply tokens"
```

---

## Task 7: 把 `Reply()` 接到 `dispatchReply`（最終整合）

**Files:**
- Modify: `platform/line/line.go`（`Reply()` 內呼叫改成 `dispatchReply`）
- Modify: `platform/line/dispatch_test.go`（end-to-end 整合測試）

- [ ] **Step 1: 寫整合測試 — Reply() 走 dispatchReply 路徑**

`dispatch_test.go` 追加：

```go
import "context"

func TestReply_FreshToken_UsesReplyAPI(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}
	p.cacheReplyToken("U123", "tok-fresh")

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.Reply(context.Background(), rc, "hello world"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 1 {
		t.Errorf("want 1 ReplyMessage, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 0 {
		t.Errorf("want 0 PushMessage, got %d", len(fake.pushCalls))
	}
}

func TestReply_NoToken_UsesPushAPI(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}

	rc := replyContext{targetID: "U999", targetType: "user"}
	if err := p.Reply(context.Background(), rc, "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 0 {
		t.Errorf("want 0 ReplyMessage, got %d", len(fake.replyCalls))
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("want 1 PushMessage, got %d", len(fake.pushCalls))
	}
}

func TestReply_EmptyContent_NoCall(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}

	rc := replyContext{targetID: "U123", targetType: "user"}
	if err := p.Reply(context.Background(), rc, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.replyCalls) != 0 || len(fake.pushCalls) != 0 {
		t.Error("empty content should result in zero API calls")
	}
}

func TestReply_InvalidContext_ReturnsError(t *testing.T) {
	fake := &fakeLineClient{}
	p := &Platform{bot: fake}

	err := p.Reply(context.Background(), "not a replyContext", "hi")
	if err == nil {
		t.Fatal("expected error for invalid reply context type")
	}
}
```

- [ ] **Step 2: 跑測試確認 FAIL（Reply() 還沒接上）**

Run: `go test ./platform/line/ -run TestReply_ -v`
Expected: `TestReply_FreshToken_UsesReplyAPI` FAIL（目前 `Reply()` 直接 PushMessage，不會走 ReplyMessage）

- [ ] **Step 3: 重寫 `Reply()` 用 `dispatchReply`**

`platform/line/line.go` 約 line 376-428 的 `Reply()` 整段替換成：

```go
func (p *Platform) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("line: invalid reply context type %T", rctx)
	}

	if content == "" {
		return nil
	}

	content = core.StripMarkdown(content)

	// Rule 13 violation scan — alert on implementation-leak phrases in outbound text.
	// Does NOT strip the text (observe-only for now); see SKILL.md 硬規則 13.
	if matches := scanRule13Violations(content); len(matches) > 0 {
		preview := content
		if len(preview) > 300 {
			preview = preview[:300] + "…"
		}
		core.AlertError("rule13_violation", "bot 回覆含實作細節洩漏關鍵詞（觀察模式，未修改內容）", map[string]any{
			"target_id":    rc.targetID,
			"target_type":  rc.targetType,
			"matches":      matches,
			"text_preview": preview,
			"text_len":     len(content),
		})
	}

	// LINE text message limit is 5000 characters
	messages := splitMessage(content, 5000)
	if err := p.dispatchReply(rc, messages); err != nil {
		core.AlertError("line_dispatch_failed", "LINE dispatch (Reply/Push) returned error", map[string]any{
			"target_id":   rc.targetID,
			"target_type": rc.targetType,
			"segments":    len(messages),
			"error":       err.Error(),
		})
		return err
	}
	return nil
}
```

注意：原本的 `core.AlertError("line_push_failed", ...)` 改成 `"line_dispatch_failed"` 以反映可能來自 Reply 或 Push。OPS.md 的 error category 清單若有提到 `line_push_failed` 需同步更新（下一步檢查）。

- [ ] **Step 4: 檢查 OPS.md 是否需要更新 error category**

Run: `grep -n "line_push_failed" docs/OPS.md`
Expected：若有命中，需手動把該行改/補成 `line_dispatch_failed`（含 Reply 與 Push 兩種失敗）。若沒命中則跳過。

若有命中：

```bash
# 用 Edit 工具把 docs/OPS.md 內 "line_push_failed" 改為 "line_dispatch_failed"
# （並在說明中補：「Reply 或 Push API 失敗都會走這個 category」）
```

- [ ] **Step 5: 跑全測試確認 PASS**

Run: `go test ./platform/line/... -v`
Expected: 全部 PASS（既有 + 新增）

- [ ] **Step 6: 跑全 repo 測試確認沒踩到別的東西**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 7: Update line.go 上方註解（移除舊註解的誤導）**

`platform/line/line.go` line 28-31 的舊註解：

```go
// replyContext stores the user/group ID for push messages.
// We use PushMessage instead of ReplyMessage because reply tokens
// expire in ~1 minute, which is too short for AI agent processing.
```

替換為：

```go
// replyContext stores the user/group/room ID for outbound dispatch.
// Dispatch策略：see dispatch.go — webhook 收到的 reply token 會以 targetID 為 key
// 暫存 ~50s；Reply() 時若 token 還新鮮就走 LINE Reply API（免費），否則走 Push API。
```

- [ ] **Step 8: Commit**

```bash
git add platform/line/line.go platform/line/dispatch_test.go docs/OPS.md
git commit -m "feat(line): wire Reply() to hybrid Reply/Push dispatcher"
```

---

## Task 8: Pre-merge 檢查

- [ ] **Step 1: `go build ./...`** — Expected: PASS
- [ ] **Step 2: `go test ./...`** — Expected: PASS
- [ ] **Step 3: `go vet ./...`** — Expected: 無 warning
- [ ] **Step 4: 確認沒在 `core/` 加平台特定 hardcode**

Run: `grep -rn '"line"' core/*.go`
Expected: 只有原本就有的（若有），無新增。

- [ ] **Step 5: 看一眼 git log 整體變更**

Run: `git log --oneline -10`
Expected: Task 1–7 各一個 commit，訊息清楚。

- [ ] **Step 6: 完成回報**

跟使用者說：「LINE Reply/Push hybrid dispatch 已完成。新增 dispatch.go (~150 行) + 14 個測試，line.go 改動限縮於 webhook handler、Reply()、Stop()。下一步可選：(a) 部署觀察 reply 命中率、(b) 測試 production 行為（觸發短訊息確認真的走 Reply API）。」

---

## Spec Coverage Self-Check

| Spec 章節 / 要求 | Task |
|---|---|
| `replyTokenCache` (`sync.Map`，targetID key) | Task 2 |
| Webhook 寫入 token | Task 2 |
| `LoadAndDelete` 單次取用 | Task 2（test）+ Task 3（用法）|
| 50s TTL 判定 | Task 2 |
| `dispatchReply`：fresh token 走 Reply | Task 3 |
| `dispatchReply`：no/expired token 走 Push | Task 3 |
| Reply 批次最多 5 段、>5 段後續 Push | Task 3（實作）+ Task 5（測試）|
| 400 + token-related 退回 Push | Task 4 |
| 其他 Reply 錯誤不退回（duplicate-risk） | Task 4 |
| Sweeper goroutine | Task 6 |
| Sweeper 啟停 hook 進 Start/Stop | Task 6 |
| `slog.Debug` 觀測（method/reason） | Task 3 + Task 4 |
| `AlertError` 從 `line_push_failed` 改為 `line_dispatch_failed` | Task 7 |
| 既有 `Reply()` 介面不變、Send() 不變 | Task 7 |

無遺漏。

---

Plan complete and saved to `docs/superpowers/plans/2026-05-03-line-reply-push-hybrid.md`. Two execution options:

1. **Subagent-Driven (recommended)** — 我每個 task 派一個 fresh subagent，做完我 review，一輪一輪推進
2. **Inline Execution** — 在這個 session 直接做，做幾個 task 停一次讓你 review

哪個？
