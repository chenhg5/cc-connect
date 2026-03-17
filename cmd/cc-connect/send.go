package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func runSend(args []string) {
	var project, sessionKey, dataDir, message, imagePath string
	var useStdin bool

	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--message", "-m":
			if i+1 < len(args) {
				i++
				message = args[i]
			}
		case "--image":
			if i+1 < len(args) {
				i++
				imagePath = args[i]
			}
		case "--stdin":
			useStdin = true
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--help", "-h":
			printSendUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = strings.Join(positional, " ")
	}

	// Resolve image path to absolute.
	if imagePath != "" {
		abs, err := filepath.Abs(imagePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid image path: %v\n", err)
			os.Exit(1)
		}
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: image file not found: %s\n", abs)
			os.Exit(1)
		}
		imagePath = abs
	}

	if message == "" && imagePath == "" {
		fmt.Fprintln(os.Stderr, "Error: message or --image is required")
		printSendUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	reqBody := map[string]string{
		"project":     project,
		"session_key": sessionKey,
	}
	if message != "" {
		reqBody["message"] = message
	}
	if imagePath != "" {
		reqBody["image_path"] = imagePath
	}
	payload, _ := json.Marshal(reqBody)

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Post("http://unix/send", "application/json", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	if imagePath != "" && message != "" {
		fmt.Println("Image and message sent successfully.")
	} else if imagePath != "" {
		fmt.Println("Image sent successfully.")
	} else {
		fmt.Println("Message sent successfully.")
	}
}

func resolveSocketPath(dataDir string) string {
	if dataDir != "" {
		return filepath.Join(dataDir, "run", "api.sock")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cc-connect", "run", "api.sock")
	}
	return filepath.Join(".cc-connect", "run", "api.sock")
}

func printSendUsage() {
	fmt.Println(`Usage: cc-connect send [options] <message>
       cc-connect send [options] -m <message>
       cc-connect send [options] --image <path>
       cc-connect send [options] --stdin < file
       echo "msg" | cc-connect send [options] --stdin

Send a message or image to an active cc-connect session.

Options:
  -m, --message <text>     Message to send (preferred over positional args)
      --image <path>       Send a local image file (platform must support images)
      --stdin              Read message from stdin (best for long/special-char messages)
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Target session key (optional, picks first active)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Examples:
  cc-connect send "Daily summary: ..."
  cc-connect send -m "Build completed successfully"
  cc-connect send --image /tmp/screenshot.png
  cc-connect send --image /tmp/chart.png -m "Here's the latest chart"
  cc-connect send --stdin <<'EOF'
    Long message with "special" chars, $variables, and newlines
  EOF`)
}
