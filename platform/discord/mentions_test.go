package discord

import "testing"

func TestReplaceMentions(t *testing.T) {
	alias := map[string]string{"Claude橘": "1", "橘": "1", "龙娘": "2"}
	cases := map[string]string{
		"@Claude橘 顺带一句":            "<@1> 顺带一句",
		"@橘 看看，@龙娘 也看":             "<@1> 看看，<@2> 也看",
		"邮箱 a@b.com 不动":            "邮箱 a@b.com 不动",
		"```\n@橘 in code\n``` @龙娘": "```\n@橘 in code\n``` <@2>",
		"@everyone 其他人":            "@everyone 其他人",
		"<@1> 已经是真 mention":        "<@1> 已经是真 mention",
	}
	for in, want := range cases {
		if got := replaceMentions(in, alias); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestMarkSelfMention(t *testing.T) {
	cases := []struct{ in, want string }{
		// only me: strip as before so "/new" etc. still parse
		{"<@111> /new", "/new"},
		{"<@!111>", ""},
		// me plus another bot: keep a readable marker so the agent knows it was addressed too
		{"<@111> <@222>", "@橘(你) <@222>"},
		{"<@222> <@!111> 看看", "<@222> @橘(你) 看看"},
		{"@everyone <@111> 看看", "@everyone @橘(你) 看看"},
		{"<@&333> <@111>", ""}, // my role + me: still only me
	}
	for _, c := range cases {
		if got := markSelfMention(c.in, "111", "333", "橘"); got != c.want {
			t.Errorf("markSelfMention(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
