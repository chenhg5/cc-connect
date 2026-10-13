//go:build !agent_team

package main

func withOptionalPlatformCommands(handlers map[string]func([]string)) map[string]func([]string) {
	handlers["tuitui"] = runTuiTui
	handlers["yuanbao"] = runYuanbao
	return handlers
}
