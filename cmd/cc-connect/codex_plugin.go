package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/chenhg5/cc-connect/core"
	"github.com/chenhg5/cc-connect/daemon"
)

func runCodexPlugin() {
	meta, err := daemon.LoadMeta()
	if runtime.GOOS != "windows" || err != nil || !meta.StartWithCodex {
		fmt.Fprintln(os.Stderr, core.NewI18n(core.LangEnglish).T(core.MsgPluginDaemonRequired))
		os.Exit(1)
	}
	mgr, err := daemon.NewManager()
	if err == nil {
		err = serveCodexPlugin(os.Stdin, os.Stdout, mgr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Each Codex session can own an MCP connection. Start is idempotent; EOF must
// not stop other sessions. The existing Windows supervisor owns app shutdown.
func serveCodexPlugin(input io.Reader, output io.Writer, mgr daemon.Manager) error {
	decoder, encoder := json.NewDecoder(input), json.NewEncoder(output)
	initialized := false
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("MCP decode: %w", err)
		}
		if len(request.ID) == 0 { // Notifications have no response.
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		var result any
		var callErr error
		code := -32603
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				callErr, code = err, -32602
				break
			}
			protocol := params.ProtocolVersion
			switch protocol {
			case "2024-11-05", "2025-03-26", "2025-06-18":
			default:
				protocol = "2024-11-05"
			}
			if !initialized {
				callErr = mgr.Start()
				initialized = callErr == nil
			}
			result = map[string]any{
				"protocolVersion": protocol,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "cc-connect", "version": version},
			}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "connection_status",
				"description": core.NewI18n(core.LangEnglish).T(core.MsgDaemonStatus),
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
				"annotations": map[string]bool{"readOnlyHint": true},
			}}}
		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				callErr, code = err, -32602
			} else if params.Name != "connection_status" {
				callErr, code = fmt.Errorf("unknown tool: %s", params.Name), -32602
			} else {
				var status *daemon.Status
				status, callErr = mgr.Status()
				if callErr == nil {
					data, err := json.Marshal(status)
					callErr = err
					result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(data)}}}
				}
			}
		default:
			callErr, code = fmt.Errorf("unknown method: %s", request.Method), -32601
		}
		if callErr != nil {
			response["error"] = map[string]any{"code": code, "message": callErr.Error()}
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("MCP encode: %w", err)
		}
	}
}
