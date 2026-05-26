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
