package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type sendRequest struct {
	Project    string `json:"project"`
	SessionKey string `json:"session_key,omitempty"`
	Message    string `json:"message"`
}

func main() {
	var (
		project    = flag.String("project", "", "target cc-connect project name")
		sessionKey = flag.String("session", "", "optional session key; auto-picks single active session if empty")
		agent      = flag.String("agent", "codex", "agent name for render gating")
		platform   = flag.String("platform", "feishu", "platform name for render gating")
		workspace  = flag.String("workspace", "", "workspace dir used for relative path rendering")
		socket     = flag.String("socket", "", "unix socket path; defaults to ~/.cc-connect/run/api.sock")
		dataDir    = flag.String("data-dir", "", "data dir used to resolve socket if --socket is empty")
		fixture    = flag.String("fixture", "", "path to fixture text file; defaults to stdin")
		presets    = flag.String("presets", "absolute-none-none,relative-emoji-code,basename-ascii-bracket,dirname-basename-none-angle,smart-emoji-fullwidth,dirname-basename-ascii-code", "comma-separated preset names")
		dryRun     = flag.Bool("dry-run", false, "print rendered variants instead of sending")
	)
	flag.Parse()

	if *project == "" {
		fmt.Fprintln(os.Stderr, "Error: --project is required")
		os.Exit(1)
	}
	if strings.TrimSpace(*workspace) == "" {
		fmt.Fprintln(os.Stderr, "Error: --workspace is required")
		os.Exit(1)
	}

	text, err := readFixture(*fixture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: read fixture: %v\n", err)
		os.Exit(1)
	}

	names := parsePresetList(*presets)
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no presets selected")
		os.Exit(1)
	}

	variants := make([]string, 0, len(names))
	for _, name := range names {
		cfg, ok := presetCfg(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: unknown preset %q\n", name)
			os.Exit(1)
		}
		rendered := core.TransformLocalReferences(text, cfg, *agent, *platform, *workspace)
		msg := fmt.Sprintf("[ref-render matrix]\npreset: %s\nagent: %s\nplatform: %s\nworkspace: %s\n\n%s",
			name, *agent, *platform, *workspace, rendered)
		variants = append(variants, msg)
	}

	if *dryRun {
		for i, v := range variants {
			if i > 0 {
				fmt.Println("\n---\n")
			}
			fmt.Println(v)
		}
		return
	}

	sock := *socket
	if sock == "" {
		sock = resolveSocketPath(*dataDir)
	}
	for _, v := range variants {
		if err := sendUnix(sock, sendRequest{
			Project:    *project,
			SessionKey: *sessionKey,
			Message:    v,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: send failed: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Sent %d rendered variants.\n", len(variants))
}

func readFixture(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func parsePresetList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func presetCfg(name string) (core.ReferenceRenderCfg, bool) {
	base := core.ReferenceRenderCfg{
		NormalizeAgents: []string{"all"},
		RenderPlatforms: []string{"all"},
	}
	switch name {
	case "absolute-none-none":
		base.DisplayPath = "absolute"
		base.MarkerStyle = "none"
		base.EnclosureStyle = "none"
	case "relative-emoji-code":
		base.DisplayPath = "relative"
		base.MarkerStyle = "emoji"
		base.EnclosureStyle = "code"
	case "basename-ascii-bracket":
		base.DisplayPath = "basename"
		base.MarkerStyle = "ascii"
		base.EnclosureStyle = "bracket"
	case "dirname-basename-none-angle":
		base.DisplayPath = "dirname_basename"
		base.MarkerStyle = "none"
		base.EnclosureStyle = "angle"
	case "smart-emoji-fullwidth":
		base.DisplayPath = "smart"
		base.MarkerStyle = "emoji"
		base.EnclosureStyle = "fullwidth"
	case "dirname-basename-ascii-code":
		base.DisplayPath = "dirname_basename"
		base.MarkerStyle = "ascii"
		base.EnclosureStyle = "code"
	default:
		return core.ReferenceRenderCfg{}, false
	}
	return base, true
}

func resolveSocketPath(dataDir string) string {
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(dataDir, "run", "api.sock")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect", "run", "api.sock")
	}
	return filepath.Join(".cc-connect", "run", "api.sock")
}

func sendUnix(sock string, req sendRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
	resp, err := client.Post("http://unix/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%s body=%s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
