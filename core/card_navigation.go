package core

import "strings"

// CardReturnAction keeps management pages in their own Help section.
func CardReturnAction(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "nav:/help"
	}
	switch fields[0] {
	case "/team":
		if len(fields) > 2 && fields[1] == "permissions" {
			return "cmd:/team permissions"
		}
		if len(fields) > 2 && fields[1] == "labels" {
			return "cmd:/team labels"
		}
		if len(fields) > 2 && fields[1] == "members" {
			return "cmd:/team members"
		}
		return "nav:/help team"
	case "/project":
		if len(fields) > 1 {
			switch fields[1] {
			case "task", "tasks", "history", "finish", "instruction":
				return "cmd:/project dashboard"
			case "bot", "members", "edit", "directory", "directories", "_directories", "model":
				return "cmd:/project settings"
			}
		}
		return "nav:/help project"
	}
	for _, group := range helpCardGroups() {
		for _, item := range group.items {
			if item.command == fields[0] {
				return "nav:/help " + group.key
			}
		}
	}
	return "nav:/help"
}
