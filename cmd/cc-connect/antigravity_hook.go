package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chenhg5/cc-connect/agent/antigravityhook"
)

func runAntigravityPermissionHook() {
	err := antigravityhook.Relay(
		os.Stdin,
		os.Stdout,
		os.Getenv(antigravityhook.EnvAddress),
		os.Getenv(antigravityhook.EnvToken),
	)
	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, "cc-connect Agy permission hook unavailable")
	_ = json.NewEncoder(os.Stdout).Encode(antigravityhook.BridgeResponse{
		Decision: "deny",
		Reason:   "cc-connect permission bridge is unavailable",
	})
}
