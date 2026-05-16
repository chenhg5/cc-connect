package ymsagent

import "strings"

func isSlashCommandPrompt(prompt string) bool {
	body := strings.TrimLeft(prompt, " \t\r\n")
	if strings.HasPrefix(body, "[cc-connect") {
		if newline := strings.IndexByte(body, '\n'); newline >= 0 {
			body = strings.TrimLeft(body[newline+1:], " \t\r\n")
		}
	}
	return strings.HasPrefix(body, "/")
}
