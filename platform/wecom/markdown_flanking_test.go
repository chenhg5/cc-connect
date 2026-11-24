package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Regression: WeCom rendered literal asterisks for "**标题：**内容" — the
// closing ** is preceded by the fullwidth colon (punctuation) and followed
// by a CJK letter, so it fails CommonMark flanking rules. The fix moves the
// punctuation outside the bold span.
func TestRepairBoldFlanking_FullwidthColon(t *testing.T) {
	got := repairBoldFlanking("**标题：**内容")
	want := "**标题**：内容"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRepairBoldFlanking(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The exact card_content pattern emitted by the writing agent.
		{"label_colon", "**标题：**某文章", "**标题**：某文章"},
		{"originality_line", "**是否可标注原创：**建议人工审核后决定", "**是否可标注原创**：建议人工审核后决定"},
		{"exclamation", "**注意！！**马上", "**注意**！！马上"},
		{"mixed_trailing_punct", "**要点：，。**下一句", "**要点**：，。下一句"},
		{"multiple_spans_one_line", "**标题：**A\n**时段：**B", "**标题**：A\n**时段**：B"},
		{"ascii_colon_followed_by_text", "**note:**rest", "**note**:rest"},
		// Paired closer with the opener inside the span: keep the pair inside
		// and append a space so the closing ** stays flanking-valid.
		{"paired_paren_then_text", "**说明（附录）**内容", "**说明（附录）** 内容"},
		{"paired_book_title", "**读《三体》**有感", "**读《三体》** 有感"},
		// Opener OUTSIDE the span → the closer is a normal stray, move it out.
		{"paren_outside", "**注**（值）", "**注**（值）"},

		// Already-correct forms must be untouched. A closing delimiter at EOL
		// or before whitespace is already right-flanking.
		{"correct_colon_outside", "**标题**：内容", "**标题**：内容"},
		{"official_example", "**广州**今日天气：29度", "**广州**今日天气：29度"},
		{"colon_inside_bold", "**a：b**", "**a：b**"},
		{"bold_then_ascii", "**bold** normal", "**bold** normal"},
		{"no_bold", "plain text 没有星号", "plain text 没有星号"},
		{"single_star_italic", "*斜体* 不动", "*斜体* 不动"},
		{"already_valid_eol", "**Note.**", "**Note.**"},
		{"already_valid_space", "**Note.** next", "**Note.** next"},
		{"paired_paren_eol", "**字段（值）**", "**字段（值）**"},
		{"ascii_colon_before_space", "**note:** rest", "**note:** rest"},
		{"link_then_repair", "[点击预览](https://mp.weixin.qq.com/x) 与 **标题：**内容", "[点击预览](https://mp.weixin.qq.com/x) 与 **标题**：内容"},
		{"triple_asterisk", "***粗斜体：***", "***粗斜体：***"},

		// Code contexts must never be rewritten — inside code, ** is literal
		// by design and mutating it changes the displayed code sample.
		{"inline_code", "`代码 **x：**` 和 **标题：**内容", "`代码 **x：**` 和 **标题**：内容"},
		{"inline_code_only", "`代码 **x：**`", "`代码 **x：**`"},
		{"fenced_block", "```\n**x：**\n```\n**y：**尾", "```\n**x：**\n```\n**y**：尾"},
		{"tilde_fence", "~~~\n**x：**\n~~~\n**y：**尾", "~~~\n**x：**\n~~~\n**y**：尾"},
		{"single_line_triple_backtick", "```code```\n**标题：**内容", "```code```\n**标题**：内容"},

		// A bold span may cross an inline code span; the code text itself must
		// stay byte-identical.
		{"bold_crosses_inline_code", "**标题 `code` 内容：**下一句", "**标题 `code` 内容**：下一句"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := repairBoldFlanking(c.in); got != c.want {
				t.Fatalf("in:  %q\ngot: %q\nwant:%q", c.in, got, c.want)
			}
		})
	}
}

func TestRepairBoldFlanking_FastPath(t *testing.T) {
	if repairBoldFlanking("没有双星号的文本") != "没有双星号的文本" {
		t.Fatal("fast path should return input unchanged")
	}
}

// TestCallbackReply_RepairsBoldFlanking covers the default (callback) WeCom
// platform. It uses the same markdown payload as the WebSocket path, so the
// repair must run there too.
func TestCallbackReply_RepairsBoldFlanking(t *testing.T) {
	payloads := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payloads <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer server.Close()

	p := &Platform{
		apiBaseURL:     server.URL,
		apiClient:      server.Client(),
		agentID:        "1",
		enableMarkdown: true,
	}
	p.tokenCache.token = "test-token"
	p.tokenCache.expiresAt = time.Now().Add(time.Hour)

	if err := p.Reply(context.Background(), replyContext{userID: "u1"}, "**标题：**内容"); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case body := <-payloads:
		if body["msgtype"] != "markdown" {
			t.Fatalf("msgtype = %v, want markdown", body["msgtype"])
		}
		markdown, ok := body["markdown"].(map[string]any)
		if !ok {
			t.Fatalf("markdown payload = %#v, want object", body["markdown"])
		}
		if markdown["content"] != "**标题**：内容" {
			t.Fatalf("markdown.content = %q, want repaired content", markdown["content"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback payload")
	}
}

// TestReply_RepairsBoldFlanking pins the outbound call site: every stream
// reply must go through the repair, not just the helper's unit tests.
func TestReply_RepairsBoldFlanking(t *testing.T) {
	type result struct {
		content string
		err     error
	}
	results := make(chan result, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			results <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()

		var frame wsFrame
		if err := conn.ReadJSON(&frame); err != nil {
			results <- result{err: err}
			return
		}
		var body struct {
			Stream struct {
				Content string `json:"content"`
			} `json:"stream"`
		}
		if err := json.Unmarshal(frame.Body, &body); err != nil {
			results <- result{err: err}
			return
		}
		results <- result{content: body.Stream.Content}
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	p := &WSPlatform{conn: conn}
	if err := p.Reply(context.Background(), wsReplyContext{reqID: "req-1"}, "**标题：**内容"); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case got := <-results:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.content != "**标题**：内容" {
			t.Fatalf("reply content = %q, want %q", got.content, "**标题**：内容")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reply frame")
	}
}
