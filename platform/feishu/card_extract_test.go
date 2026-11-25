package feishu

import "testing"

// TestExtractInteractiveCardText locks the parsing behaviour for the card
// shapes we see in production. Fixtures are sanitised copies of real alarm
// cards: the 2.0 div case used to degrade to "[interactive card]", and the
// 2.0 header used to be dropped on the quoted (raw_card_content) path.
func TestExtractInteractiveCardText(t *testing.T) {
	cases := []struct {
		name string
		card string
		want string
	}{
		{
			name: "v1 title with text rows",
			card: `{"title":"🚩 容器销毁报警","elements":[[{"tag":"text","text":"时间"},{"tag":"text","text":": 2026-11-25 08:10:28"}]]}`,
			want: "🚩 容器销毁报警\n时间\n: 2026-11-25 08:10:28",
		},
		{
			name: "v1 text as nested object",
			card: `{"title":"🚩 容器销毁报警","elements":[[{"tag":"text","text":{"tag":"plain_text","content":"时间"}},{"tag":"text","text":{"tag":"plain_text","content":": 2026-11-25 08:10:28"}}]]}`,
			want: "🚩 容器销毁报警\n时间\n: 2026-11-25 08:10:28",
		},
		{
			name: "v1 div fields",
			card: `{"title":"🚩 容器销毁报警","elements":[{"tag":"div","fields":[{"is_short":true,"text":{"tag":"lark_md","content":"**appCode:** demo-app"}},{"is_short":true,"text":{"tag":"lark_md","content":"**env:** prod"}}]}]}`,
			want: "🚩 容器销毁报警\n**appCode:** demo-app\n**env:** prod",
		},
		{
			name: "2.0 body with markdown element",
			card: `{"schema":"2.0","body":{"tag":"body","elements":[{"tag":"markdown","property":{"content":"hello v2"}}]}}`,
			want: "hello v2",
		},
		{
			name: "2.0 div wrapping markdown plain_text keeps header",
			card: `{"schema":"2.0","header":{"tag":"card_header","property":{"title":{"tag":"plain_text","property":{"content":"🚩 容器销毁报警"}}}},"body":{"tag":"body","elements":[{"tag":"div","property":{"text":{"tag":"markdown","property":{"elements":[{"tag":"plain_text","property":{"content":"时间"}},{"tag":"plain_text","property":{"content":": 2026-11-25 08:10:28"}}]}}}},{"tag":"div","property":{"text":{"tag":"markdown","property":{"elements":[{"tag":"plain_text","property":{"content":"appCode"}},{"tag":"plain_text","property":{"content":": demo-app"}}]}}}}]}}`,
			want: "🚩 容器销毁报警\n时间\n: 2026-11-25 08:10:28\nappCode\n: demo-app",
		},
		{
			name: "2.0 header only falls back to title",
			card: `{"schema":"2.0","header":{"tag":"card_header","property":{"title":{"tag":"plain_text","property":{"content":"🚩 容器销毁报警"}}}},"body":{"tag":"body","elements":[]}}`,
			want: "🚩 容器销毁报警",
		},
		{
			name: "2.0 column_set alarm card",
			card: `{"schema":"2.0","body":{"tag":"body","elements":[{"tag":"column_set","property":{"columns":[{"elements":[{"tag":"markdown","property":{"elements":[{"tag":"plain_text","property":{"content":"报警ID"}},{"tag":"plain_text","property":{"content":"：123"}}]}}]}]}}]}}`,
			want: "报警ID\n：123",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractInteractiveCardText(c.card)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCardTextLooksIncomplete(t *testing.T) {
	full := `{"title":"🚩 容器销毁报警","elements":[[{"tag":"text","text":"时间"}]]}`
	cases := []struct {
		name    string
		content string
		text    string
		want    bool
	}{
		{"empty", full, "", true},
		{"placeholder", full, "[interactive card]", true},
		{"title only", full, "🚩 容器销毁报警", true},
		{"title plus body", full, "🚩 容器销毁报警\n时间", false},
		{"no title in payload", `{"body":{"tag":"body","elements":[]}}`, "some text", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cardTextLooksIncomplete(c.content, c.text); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
