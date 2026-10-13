package main

import "os"

func startTeamService(configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return handoffTeamService(executable, teamServiceArgs(executable, configPath))
}

// Use normal service arguments so subsequent restarts never re-enter init or
// create a second app. Pass the path as one argument without invoking a shell.
func teamServiceArgs(executable, configPath string) []string {
	return []string{executable, "--config", configPath}
}
