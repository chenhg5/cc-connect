# LINE Reply/Push Hybrid Dispatch — Design

> 日期：2026-05-03
> 狀態：Approved，待實作
> 參考：[openabdev/openab gateway/src/adapters/line.rs](https://github.com/openabdev/openab/blob/main/gateway/src/adapters/line.rs)

## 背景

目前 `platform/line/line.go` 的 `Reply()` 一律使用 LINE Messaging API 的 **Push API**（`PushMessage`）。原因是 reply token 約 1 分鐘就過期，而 AI agent 處理長任務常常超過 60s，所以選擇捨棄 Reply 統一用 Push。

代價：每一則回覆都吃 LINE 月配額（免費方案 200 則/月、Light 5000 則/月）。實務上 cc-connect 有不少回覆其實 < 60s（指令、permission 卡、`AlertError` 推播、短任務、錯誤訊息），這些原本可以走免費的 Reply API。

openab 的 gateway 採用 hybrid Reply/Push dispatch：快取 reply token、單次取用、按時效決定走 Reply 或 Push、錯誤分類退回。本 spec 把這套設計移植進 cc-connect。

## 目標

- 當 webhook 與 `Reply()` 之間 < 50s 時，使用 Reply API 節省配額
- 超過 50s、token 不存在、或 Reply API 回「token 無效」時，自動退回 Push
- 不引入重複訊息風險：Reply API 失敗但「可能已送出」的情況下不退回
- 對呼叫端透明：`Reply()` 的介面不變

## 非目標

- 不改 streaming 行為（agent 邊跑邊推 chunk 維持原邏輯，第二段以後自然走 Push）
- 不改 Push 退回時的訊息切分行為
- 不改其他 platform 的 dispatch 邏輯
- 不加新的設定旗標（YAGNI；之後若需要再加）

## 設計

### 資料流

```
LINE webhook ──► handleEvent ──┬─► cache.Store(targetID, token, now)
                               └─► p.handler(...) ──► agent
                                                       │
agent stream ──► p.Reply(rctx, content) ──► dispatch ──┤
                                                       │
                                            ┌──────────┴──────────┐
                                            │ LoadAndDelete token │
                                            │ token < 50s?        │
                                            └──┬─────────────┬────┘
                                              yes           no/missing
                                               │             │
                                          ReplyMessage    PushMessage
                                          (批次最多5段)        │
                                               │             │
                                          400 token         done
                                          無效/過期 ──► PushMessage
                                          其他錯 ──► return err（不退回）
```

### 元件

#### 1. `replyTokenCache`

`platform/line/Platform` 內部欄位：

```go
type tokenEntry struct {
    token string
    at    time.Time
}

// 在 Platform struct 內：
replyTokens sync.Map  // targetID -> tokenEntry
```

**Key 選擇：`targetID`**
- 對應 user / group / room ID（已存在 `replyContext` 內）
- 同一 chat 後到的 webhook 覆寫先到的
- 多人在同一 group 同時 @bot 並且兩個 token 都來得及用是極罕見，覆寫策略「最新的最有可能在 50s 內被回覆」合理

**寫入時機**：`handleEvent` 處理任何 `webhook.MessageEvent` 時，把 `e.ReplyToken` 連同 `time.Now()` 寫進 cache。所有訊息類型（text/image/audio）都寫，因為 `Reply()` 不知道對應的 inbound 是哪一種。

**讀取**：`Reply()` 內部呼叫 `LoadAndDelete(targetID)`，達成單次取用語意（streaming 第一段拿到後，後續 chunk 自動 cache miss 走 Push）。

#### 2. `dispatchReply`

新增私有方法（取代 `Reply()` 中現有的 Push 迴圈）：

```go
func (p *Platform) dispatchReply(rc replyContext, messages []string) error
```

邏輯：
1. `LoadAndDelete(rc.targetID)` 取出 entry
2. 若 entry 存在且 `time.Since(entry.at) < 50*time.Second`：
   - 取 `messages` 的前 N 段（N = min(len(messages), 5)）組成 `[]messaging_api.MessageInterface`
   - 呼叫 `bot.ReplyMessage(&messaging_api.ReplyMessageRequest{ReplyToken, Messages})`
   - 成功 → 若還有剩餘段（>5），剩下走 Push 路徑送出
   - **400 + body 含 "reply token" 或 "expired"** → 全部走 Push（fallback）
   - **其他錯誤** → `return err`，不退回（避免重複；`AlertError` 已有重試）
3. 若 entry 不存在/過期：全部走 Push（現行邏輯）

切分後的 Push 路徑沿用現有 `for _, text := range messages { bot.PushMessage(...) }` 寫法，提取成內部 helper `pushAll(rc, messages)`。

#### 3. 錯誤判定

LINE Go SDK v8 的 `ReplyMessage` 失敗時錯誤型別需於實作時對 SDK 校正（可能是 `*messaging_api.ErrorResponse`，或是 SDK 把 status code 包進 generic error）。判定虛擬碼：

```go
isTokenInvalid := func(err error) bool {
    // 取 HTTP status & body / message
    if !is400(err) { return false }
    msg := strings.ToLower(getErrMessage(err))
    return strings.Contains(msg, "reply token") || strings.Contains(msg, "expired")
}
```

實作時若 SDK 沒有暴露 status code，需要自行 HTTP 呼叫或用 SDK 的 raw response 介面。

#### 4. 背景清掃 goroutine

`Start()` 內啟動：

```go
ctx, cancel := context.WithCancel(context.Background())
p.sweepCancel = cancel
go p.sweepExpiredTokens(ctx)
```

`sweepExpiredTokens`：每 50s 掃 `replyTokens`，刪掉 `time.Since(at) > 50s` 的項。沒有上限保護（cc-connect 規模下單一 platform instance 同時的 active chat 不會到萬級，與 openab 多租戶 gateway 場景不同）。

`Stop()` 內 `cancel()` 收掉。

#### 5. 觀測性

每次 dispatch 記一行 `slog.Debug`：

```go
slog.Debug("line: dispatch",
    "method", "reply" | "push",
    "reason", reason,
    "target_type", rc.targetType,
    // 不記 targetID 全文（隱私），只記前綴或 hash
)
```

`reason` 列舉：
- `fresh_token` — Reply 成功
- `no_token` — cache miss
- `expired` — cache hit 但已過期
- `reply_token_invalid` — Reply 回 400 token 相關，已退回 Push
- `reply_api_error` — Reply 回其他錯，未退回（也記 push 0 次）

不另外做 metric 上報；現有 eventlog 已足夠。

### 介面變更

- `replyContext`：不變（已含 `targetID`、`targetType`，sufficient）
- `Reply()` 簽章：不變
- `Send()` 簽章：不變（仍直接代理 `Reply()`）
- 新增私有方法：`dispatchReply`、`pushAll`、`sweepExpiredTokens`、`isTokenInvalid`

### 設定

無新增 config 欄位。常數：

```go
const (
    replyTokenTTL       = 50 * time.Second
    replyMaxBatch       = 5  // LINE Reply API 一次最多 5 個 message object
    sweepInterval       = 50 * time.Second
)
```

## 測試

於 `platform/line/line_test.go` 新增：

| 測試 | 情境 | 預期 |
|---|---|---|
| `TestDispatch_ReplyFresh` | webhook 後 1s 內回覆 | 走 ReplyMessage，cache 被清空 |
| `TestDispatch_ReplyExpired` | webhook 後 51s 後回覆 | 走 PushMessage |
| `TestDispatch_NoToken` | 沒有對應 webhook 直接回覆 | 走 PushMessage |
| `TestDispatch_LoadAndDeleteSemantics` | 同 targetID 連續兩次 Reply | 第一次 Reply、第二次 Push |
| `TestDispatch_TokenInvalidFallback` | Reply 回 400 invalid reply token | 退回 PushMessage |
| `TestDispatch_NetworkErrorNoFallback` | Reply 回網路錯誤 | return error，**不**呼叫 PushMessage |
| `TestDispatch_BatchUnder5` | 3 段訊息 + 新鮮 token | 一次 Reply 全送 |
| `TestDispatch_BatchOver5` | 7 段訊息 + 新鮮 token | 前 5 段 Reply、後 2 段 Push |
| `TestSweeper_RemovesExpired` | cache 內含過期 entry，跑一輪 sweep | 過期項被清掉、新鮮項保留 |

對 SDK 的 stub：抽象出 `lineBotClient` interface 包住 `ReplyMessage` / `PushMessage`，測試用 mock 取代。若不抽介面，至少把 HTTP 呼叫透過可注入的 `http.Client` 走，測試指向本地 httptest server。

## 變更影響

- 動到的檔案：
  - `platform/line/line.go`：~+150 行（cache 欄位、handleEvent 寫 cache、dispatchReply、pushAll、sweeper、Start/Stop hook）、~30 行修改
  - `platform/line/line_test.go`：~+200 行新測試
- 不動到的檔案：`core/`、其他 platform/agent
- 對外行為改變：
  - 訊息送達語意不變（成功就成功，失敗就失敗）
  - 配額消耗下降（命中 Reply 的部分免費）
  - debug log 多幾行 `line: dispatch` 訊息
- Pre-Commit Checklist（CLAUDE.md）：
  - `go build ./...` 通過
  - `go test ./...` 通過
  - 沒有在 core 加平台特定 hardcode（本變更全在 `platform/line/`）
  - 無新使用者面字串、不需 i18n
  - 無秘密入碼

## 風險與權衡

| 風險 | 緩解 |
|---|---|
| SDK 錯誤判定誤判 → 該退回沒退回，訊息丟失 | 測試覆蓋「token 過期」「網路錯」兩條路徑；先以 conservative 判斷（嚴格匹配 "reply token"/"expired" 字串）；其他 400 視為 ambiguous 不退回。 |
| SDK 把 Reply 成功但 client 認為失敗 → 退回 Push 造成重複 | 即 openab 的 duplicate-risk 場景；只在「token 明確無效」這條路退回，其他路徑寧可丟訊息也不重複。 |
| Sweeper goroutine 漏關 | 走 `Start()` 啟、`Stop()` 內 `cancel()` 對應；既有 platform 已有 server 收尾邏輯，加進去同一條 path。 |
| 多人 group 同時 @bot 互蓋 token | 接受。覆寫策略下「最後到的 webhook 對應的 token 最有可能被用上」。 |
| 多 cc-connect instance 共用同一 LINE channel（罕見） | cache 是 instance local，兩 instance 都會搶 token，誰先 Reply 誰勝；輸的那邊 Reply 會回 token invalid → 走 fallback Push。語意上沒問題。 |

## 之後可能的延伸（不在本 spec）

- Reply 命中率 metric 上報（搭 OTel）
- 動態調 TTL（如果發現 60s 邊界其實還能用）
- 對 streaming 場景的 chunk batching（讓多個快速 chunk 合併走一次 Reply 之類）

均為 YAGNI，等資料說話再做。
