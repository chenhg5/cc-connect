package wecom

import "testing"

func TestStripWeComAtMentions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		ids  []string
		want string
	}{
		{"empty", "", []string{"x"}, ""},
		{"no ids", "允许", nil, "允许"},
		{"suffix mention", "允许 @mybot", []string{"mybot"}, "允许"},
		{"prefix mention", "@MyBot 允许", []string{"mybot"}, "允许"},
		{"fullwidth at", "允许 ＠mybot", []string{"mybot"}, "允许"},
		{"two ids second", "ok @a @b", []string{"a", "b"}, "ok"},
		{"unrelated at", "email x@y.com", []string{"mybot"}, "email x@y.com"},
		{"display mention before slash command", "@小口不休息 /whoami", []string{"robot01"}, "/whoami"},
		{"display mention before bang command", "＠小口不休息 !pwd", []string{"robot01"}, "!pwd"},
		{"display mention before normal text preserved", "@小口不休息 你好", []string{"robot01"}, "@小口不休息 你好"},
		// --- Regression tests: see issue on @bot + /command in group chat. ---
		// Display name with spaces ("Claude Code"): old strip took fields[0]="@Claude",
		// leaving "Code /list" which doesn't start with "/", so /list was never dispatched.
		{"display name with spaces before slash", "@Claude Code /list", []string{"robot01"}, "/list"},
		{"display name with spaces before bang", "@Claude Code !pwd", []string{"robot01"}, "!pwd"},
		// Multiple @-mentions before the command: old strip only removed the first token.
		{"multiple mentions before slash", "@张三 @机器人 /list", []string{"robot01"}, "/list"},
		{"multiple mentions fullwidth before slash", "＠张三 ＠机器人 /list", []string{"robot01"}, "/list"},
		// Display name containing punctuation that strings.Fields wouldn't split on cleanly.
		{"display name with bang inside before slash", "@Wow!Bot /list", []string{"robot01"}, "/list"},
		// Safety: don't strip when the slash isn't at a token boundary (e.g. URLs).
		{"url after mention preserved", "@bot https://example.com", []string{"robot01"}, "@bot https://example.com"},
		{"path-like text without slash command preserved", "@bot 1/2 是分数", []string{"robot01"}, "@bot 1/2 是分数"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripWeComAtMentions(tt.in, tt.ids...)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
