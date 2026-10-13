package discord

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// linkTeamMentions turns a plain-text "@name" of a teammate bot running in this process into a
// real Discord mention (<@id>). Models often write "@Claude橘" instead of the <@id> syntax;
// Discord shows that as text and the addressed bot never sees it as a mention (maomao fork).
// Aliases are each bot's username plus the username without its leading ASCII part
// ("Claude橘" → "橘"). Text inside ``` fences is left alone.
func linkTeamMentions(content, selfID string) string {
	if !strings.Contains(content, "@") {
		return content
	}
	alias := map[string]string{}
	teamPlatforms.Range(func(k, v any) bool {
		id, _ := k.(string)
		p, _ := v.(*Platform)
		if id == "" || id == selfID || p == nil || p.botName == "" {
			return true
		}
		alias[p.botName] = id
		if short := strings.TrimLeftFunc(p.botName, func(r rune) bool { return r < unicode.MaxASCII }); short != "" {
			if _, taken := alias[short]; !taken {
				alias[short] = id
			}
		}
		return true
	})
	return replaceMentions(content, alias)
}

func replaceMentions(content string, alias map[string]string) string {
	if len(alias) == 0 {
		return content
	}
	names := make([]string, 0, len(alias))
	for n := range alias {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) }) // longest first
	parts := strings.Split(content, "```")
	for i := 0; i < len(parts); i += 2 { // even parts are outside code fences
		var b strings.Builder
		s := parts[i]
		for len(s) > 0 {
			at := strings.IndexByte(s, '@')
			if at < 0 {
				b.WriteString(s)
				break
			}
			b.WriteString(s[:at])
			s = s[at+1:]
			matched := false
			for _, n := range names {
				if strings.HasPrefix(s, n) {
					b.WriteString("<@" + alias[n] + ">")
					s = s[len(n):]
					matched = true
					break
				}
			}
			if !matched {
				b.WriteByte('@')
			}
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, "```")
}

var otherMentionRe = regexp.MustCompile(`<@[!&]?\d+>|@everyone|@here`)

// markSelfMention removes this bot's own mention, as upstream does, unless the message also
// addresses someone else. Then the own mention becomes "@name(你)": with it stripped, a message
// that @-ed two bots read as "@ the other one" and the agent stayed silent (maomao fork).
func markSelfMention(text, botID, botRoleID, name string) string {
	stripped := stripDiscordMentionWithRole(text, botID, botRoleID)
	if !otherMentionRe.MatchString(stripped) {
		return stripped
	}
	self := "@" + name + "(你)"
	text = strings.ReplaceAll(text, "<@!"+botID+">", self)
	text = strings.ReplaceAll(text, "<@"+botID+">", self)
	if botRoleID != "" {
		text = strings.ReplaceAll(text, "<@&"+botRoleID+">", self)
	}
	return strings.TrimSpace(text)
}
