# 維運手冊（OPS）

> 這份是給**維運 Claude（cc-connect-ops 跑的那個）**讀的手冊。
> 老闆（Hua Li / 李佳樺、陳修平 Joey）在 Telegram 問你任何關於系統狀態、錯誤、慢、bot 沒回的問題，**先讀這份**再動手。
> 回答一律用**繁體中文**，簡潔、直接報結論，不要把手冊內容貼回去。

---

## 系統拓撲

```
雙 cc-connect process（獨立 launchd agent，獨立 data dir，共享 binary）

┌───────────── cc-connect MAIN ───────────────┐     ┌───────── cc-connect OPS ────────┐
│ PID 從 ~/Library/LaunchAgents/              │     │ ~/Library/LaunchAgents/          │
│   com.shifu.cc-connect.plist 啟             │     │   com.shifu.cc-connect-ops.plist │
│ data_dir = ~/.cc-connect/                   │     │ data_dir = ~/.cc-connect-ops/    │
│ config    = /Users/aishifu/cc-connect/      │     │ config    = /Users/aishifu/      │
│              config.toml                    │     │              cc-connect-ops/     │
│                                             │     │              config.toml         │
│ project: joey-brain                         │     │ project: cc-ops                  │
│ platform: LINE (AI Joey Writer @351cckxq)   │     │ platform: Telegram (@Threader111bot) │
│ work_dir: /Users/aishifu/joey-brain         │     │ work_dir: /Users/aishifu/cc-connect │
│ ports: LINE 8080 / webhook 9111 /           │     │ ports: mgmt 9821                 │
│        mgmt 9820                            │     │                                  │
│ log : /tmp/cc-connect.log                   │     │ log : /tmp/cc-connect-ops.log    │
└─────────────────────────────────────────────┘     └──────────────────────────────────┘
```

- **Main** 是生產，跑 joey-brain 的 LINE Writer 流程。**動 main 要小心**——重啟會砍老闆正在跑的 Claude turn。
- **Ops** 是你（這個 Claude 自己）。你可以放心重啟自己、改 cc-connect 原始碼並重 build、commit、push。但要注意：**binary 是 symlink 指向 main 的二進位**，你 rebuild 會寫新 inode（main 執行中的舊 inode 不受影響，main 下次啟動才會用新版）。

---

## Log 來源總覽

| 類型 | 路徑 | 用途 |
|---|---|---|
| 結構化每輪對話 | `~/.cc-connect/logs/joey-brain/<YYYY-MM-DD>/turns.jsonl` | 老闆說了什麼、bot 回了什麼、duration/tokens |
| 結構化錯誤 | `~/.cc-connect/logs/joey-brain/<YYYY-MM-DD>/errors.jsonl` | 有 category 分類的錯誤事件 |
| 原始 log（main） | `/tmp/cc-connect.log` | slog JSON 混 plain；session 啟停、每則 webhook 都會進 |
| 原始 log（ops） | `/tmp/cc-connect-ops.log` | 同上，ops 自己的 |
| 對話歷史快照 | `~/.cc-connect/sessions/<project>_<hash>.json` | 每個 session 的 user/assistant turns（純文字，只有 user→bot 對話，無中間工具 call） |
| Claude 內部 log | `~/.claude/projects/-Users-aishifu-joey-brain/<uuid>.jsonl` | Claude Code 自己的 stream-json，超詳細（含所有 Bash、Read、Write），但格式難讀 |

> **優先順序**：先看 `errors.jsonl`（結構化）→ 若沒命中就撈 `turns.jsonl` → 最後才翻 `/tmp/*.log`。

---

## errors.jsonl 固定 schema

每行一個 JSON，共通欄位：

```json
{
  "ts": "2026-04-23T14:19:59.767+08:00",
  "category": "slow_agent_send",
  "summary": "agent Send() took unusually long",
  "...category-specific fields...": "..."
}
```

### 已定義 category 一覽

| category | 原因 | 嚴重度 | 常見排查 |
|---|---|---|---|
| `slow_agent_send` | agent Send() 超過閾值（目前 20s） | 低-中 | 觀察 `elapsed_ms`；多次連發可能是網路或 Anthropic API 慢 |
| `webhook_queue_full` | `/hook` 進來但 session busy 且 queue 滿 → prompt 被丟 | 中 | 檢查 session 為何卡、必要時叫老闆重傳 |
| `line_dispatch_failed` | LINE Reply 或 Push API 非 2xx | 高 | 看 `error` 欄位；常見：channel token 失效、target_id 錯、LINE 限流。Reply API token 失效會自動 fallback 到 Push，此 alert 表示連 Push 也失敗 |
| `session_spawn_failed` | agent.StartSession 失敗（包括 resume→fresh fallback 都失敗） | 嚴重 | Claude binary 不存在？mode 配置錯？可能要重啟 main |
| `agent_died_unexpectedly` | Claude 子行程跑到一半 stdout EOF（panic/OOM/被殺） | 嚴重 | 看 `partial_bytes` 判斷跑多遠就掛；`/tmp/cc-connect.log` 前後看有無 panic stack |
| `rule13_violation` | bot 回覆含實作細節字（thread_id/t_jw_/ins_2/CDN 快取/InsForge/phase=awaiting-/context bundle/CloudFront） | 低（觀察用） | 看 `matches` 和 `text_preview`，決定是 skill 邏輯漏洞還是 regex 誤報 |

遇到**非上表 category** 的 error？表示 code 有加新 hook 但這份手冊沒更新，直接把那個 category 的 entry 內容報給老闆，並提醒「這類錯誤還沒登記到 OPS.md」。

---

## turns.jsonl 固定 schema

```json
{
  "ts": "2026-04-23T14:19:59.767+08:00",
  "session_key": "line:U4c66c3685fe4ccb9f52b8380cde7094f",
  "session_id": "s1",
  "agent_session_id": "e632104e-b996-4bb4-8a9d-75f7cf38a78b",
  "msg_id": "610724902265684328",
  "platform": "line",
  "user_name": "李佳樺",
  "user_message": "C",
  "bot_reply": "...（已截至 4000 字）...",
  "tool_count": 2,
  "input_tokens": 8,
  "output_tokens": 5305,
  "duration_ms": 98102
}
```

> `user_message` 最多 2000 字、`bot_reply` 最多 4000 字，超過會加 `…[truncated]` 尾綴。

---

## 老闆常問題型 + 處理 SOP

### Q1：「有錯誤嗎？今天的 / 健康嗎 / process 還在嗎」

**最快**：跑 `scripts/healthcheck.sh`（或加 `--json` 給 cron / 程式吃）

```bash
/Users/aishifu/cc-connect/scripts/healthcheck.sh
```

一次全查完：兩個 instance 的 `/api/v1/status`、errors 按 category 聚合、turns 統計、最慢 turn 預覽。退出碼：
- `0` 全綠
- `1` 有高嚴重度錯誤（session_spawn_failed / agent_died_unexpectedly / line_dispatch_failed）
- `2` 有 instance 不回應

若要用 jq 自己撈特定內容：

```bash
TODAY=$(date +%F)
jq -s 'group_by(.category) | map({category: .[0].category, count: length, latest: (max_by(.ts) | .ts)})' \
  ~/.cc-connect/logs/*/"$TODAY"/errors.jsonl \
  ~/.cc-connect-ops/logs/*/"$TODAY"/errors.jsonl 2>/dev/null
```

回答格式：按 category 分組、報 count + 最新時間 + 一句影響判讀（e.g.「`slow_agent_send` 3 次，屬低嚴重度，應該是網路抖；`rule13_violation` 1 次，內容：XXX」）。

### Q2：「剛老闆傳 X，bot 沒回／回錯」

1. 先確認時間：老闆說「剛」大約近 10 分鐘。
2. 撈 turns.jsonl 該時段該 user 的條目：
   ```bash
   jq -c --arg since "$(date -v-10M -u +%Y-%m-%dT%H:%M:%SZ)" \
      'select(.ts > $since) | select(.user_name | test("老闆名字")) | {ts,duration_ms,user_message,bot_reply:(.bot_reply|.[:200])}' \
      ~/.cc-connect/logs/joey-brain/$(date +%F)/turns.jsonl
   ```
3. 若時段內**沒 turn 紀錄** → 代表根本沒跑完一個 turn，去 `/tmp/cc-connect.log` 抓：
   - `message received` — LINE webhook 有進來嗎？
   - `session busy` — 是否被隊列擋住？
   - `turn complete` — 有完但可能 push 失敗？
4. 若 turn 有紀錄但 bot_reply 看起來怪 → 對照 errors.jsonl 同時間點的 `rule13_violation` / `line_dispatch_failed`。

### Q3：「為什麼慢」

```bash
jq -c 'select(.duration_ms > 60000) | {ts, user_name, duration_ms, tool_count, user_message: .user_message[:40]}' \
  ~/.cc-connect/logs/joey-brain/$(date +%F)/turns.jsonl
```

超過 60s 的 turn 列出來。對照 `slow_agent_send` errors 看是否 Send() 就慢、或是 Send 快但 tool_count 多（工具 call 多所以久）。

### Q4：「最近是不是有 bug 漏掉？」

等同 Q1 但範圍拉寬：
```bash
for d in $(ls ~/.cc-connect/logs/joey-brain/ | tail -7); do
  echo "=== $d ==="
  jq -s 'group_by(.category) | map({cat: .[0].category, n: length})' \
     ~/.cc-connect/logs/joey-brain/$d/errors.jsonl 2>/dev/null
done
```

### Q5：「process 還活著嗎？」

```bash
launchctl list | grep cc-connect
ps aux | grep -E '/cc-connect(-ops)?/cc-connect' | grep -v grep
```

同時看 `/tmp/cc-connect.log` 和 `/tmp/cc-connect-ops.log` 最後幾行確認有沒有 bye / shutting down。

---

## 重啟 / 停機 SOP

### 重啟 Ops（安全）
```bash
launchctl kickstart -k "gui/$UID/com.shifu.cc-connect-ops"
```
砍的只是你自己，main 不受影響。

### 重啟 Main（謹慎）
**先確認沒 in-flight turn**：
```bash
ps aux | grep 'claude --output-format' | grep -v grep  # 有活的就等
tail -3 /tmp/cc-connect.log                            # 看最後一行是否為 turn complete / cc-connect is running
```

確認 idle 後：
```bash
launchctl kickstart -k "gui/$UID/com.shifu.cc-connect"
```

重啟會砍掉 Claude 子行程（agent_died_unexpectedly），但因為 agent_session_id 存在 session 檔裡，下次老闆傳訊息會 resume 同一條對話歷史。只有在 **turn 跑到一半**重啟才會讓老闆看到「沒回」。

### 強制完全重來（session 也清掉）
```bash
# 停 main
launchctl bootout "gui/$UID" ~/Library/LaunchAgents/com.shifu.cc-connect.plist
# 備份並清 session
mv ~/.cc-connect/sessions/joey-brain_*.json ~/.cc-connect/sessions/backup-$(date +%s).json
# 啟
launchctl bootstrap "gui/$UID" ~/Library/LaunchAgents/com.shifu.cc-connect.plist
```

後果：老闆對話記憶歸零、下次訊息會用全新 agent_session_id。**這是最後手段**，做前要問清楚。

---

## 回答原則

- **先看 log 再回答**，不要憑記憶或推測。
- **用繁體中文**。
- **具體時間、具體數字、具體 category**，不要「好像有幾個錯」這種模糊說法。
- **不要把手冊內容（目錄、category 列表）貼回去**給老闆，他是老闆、不是工程師。只回答「結果」。
- **有 rule13_violation 警報給自己看**就算了，別在給老闆的回覆裡重複「thread_id / phase=awaiting / CDN 快取」這些詞，不然你自己也違規。
