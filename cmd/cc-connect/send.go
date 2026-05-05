package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func runSend(args []string) {
	req, dataDir, err := parseSendArgs(args)
	if err != nil {
		if errors.Is(err, errSendUsage) {
			printSendUsage()
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printSendUsage()
		os.Exit(1)
	}

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	payload, err := buildSendPayload(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to encode send payload: %v\n", err)
		os.Exit(1)
	}

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

	fmt.Println("Message sent successfully.")
}

var errSendUsage = errors.New("show send usage")

func parseSendArgs(args []string) (core.SendRequest, string, error) {
	var req core.SendRequest
	var dataDir string
	var useStdin bool
	var imagePaths []string
	var filePaths []string
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--project requires a value")
			}
			i++
			req.Project = args[i]
		case "--session", "-s":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--session requires a value")
			}
			i++
			req.SessionKey = args[i]
		case "--message", "-m":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--message requires a value")
			}
			i++
			req.Message = args[i]
		case "--image":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--image requires a path")
			}
			i++
			imagePaths = append(imagePaths, args[i])
		case "--file":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--file requires a path")
			}
			i++
			filePaths = append(filePaths, args[i])
		case "--stdin":
			useStdin = true
		case "--data-dir":
			if i+1 >= len(args) {
				return req, "", fmt.Errorf("--data-dir requires a value")
			}
			i++
			dataDir = args[i]
		case "--as-prompt":
			req.AsPrompt = true
		case "--new-thread":
			req.NewThread = true
		case "--help", "-h":
			return req, "", errSendUsage
		default:
			positional = append(positional, args[i])
		}
	}

	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return req, "", fmt.Errorf("reading stdin: %w", err)
		}
		req.Message = strings.TrimSpace(string(data))
	}
	if req.Project == "" {
		req.Project = strings.TrimSpace(os.Getenv("CC_PROJECT"))
	}
	if req.SessionKey == "" {
		req.SessionKey = strings.TrimSpace(os.Getenv("CC_SESSION_KEY"))
	}
	if req.Message == "" {
		req.Message = strings.Join(positional, " ")
	}

	images, err := loadImageAttachments(imagePaths)
	if err != nil {
		return req, "", err
	}
	files, err := loadFileAttachments(filePaths)
	if err != nil {
		return req, "", err
	}
	req.Images = images
	req.Files = files

	if req.Message == "" && len(req.Images) == 0 && len(req.Files) == 0 {
		return req, "", fmt.Errorf("message or attachment is required")
	}

	return req, dataDir, nil
}

func loadImageAttachments(paths []string) ([]core.ImageAttachment, error) {
	images := make([]core.ImageAttachment, 0, len(paths))
	for _, path := range paths {
		data, fileName, mimeType, err := readAttachment(path)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, fmt.Errorf("%s is not an image (detected mime: %s)", path, mimeType)
		}
		images = append(images, core.ImageAttachment{MimeType: mimeType, Data: data, FileName: fileName})
	}
	return images, nil
}

func loadFileAttachments(paths []string) ([]core.FileAttachment, error) {
	files := make([]core.FileAttachment, 0, len(paths))
	for _, path := range paths {
		data, fileName, mimeType, err := readAttachment(path)
		if err != nil {
			return nil, err
		}
		files = append(files, core.FileAttachment{MimeType: mimeType, Data: data, FileName: fileName})
	}
	return files, nil
}

const maxAttachmentSize = 50 << 20 // 50 MB

func readAttachment(path string) ([]byte, string, string, error) {
	cleaned := filepath.Clean(path)

	info, err := os.Stat(cleaned)
	if err != nil {
		return nil, "", "", fmt.Errorf("read attachment %s: %w", path, err)
	}
	if info.Size() > maxAttachmentSize {
		return nil, "", "", fmt.Errorf("attachment %s exceeds size limit (%d MB)", path, maxAttachmentSize>>20)
	}

	data, err := os.ReadFile(cleaned)
	if err != nil {
		return nil, "", "", fmt.Errorf("read attachment %s: %w", path, err)
	}
	fileName := filepath.Base(cleaned)
	return data, fileName, detectAttachmentMimeType(fileName, data), nil
}

func detectAttachmentMimeType(fileName string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	}
	if byExt := mime.TypeByExtension(ext); byExt != "" {
		return byExt
	}
	if len(data) == 0 {
		return "application/octet-stream"
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	return http.DetectContentType(sniff)
}

func buildSendPayload(req core.SendRequest) ([]byte, error) {
	return json.Marshal(req)
}

func decodeSendPayload(data []byte, req *core.SendRequest) error {
	return json.Unmarshal(data, req)
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
       cc-connect send [options] --stdin < file
       cc-connect send [options] --image <path>
       cc-connect send [options] --file <path>
       echo "msg" | cc-connect send [options] --stdin

Send a message or attachment to an active cc-connect session.

Options:
  -m, --message <text>     Message to send (preferred over positional args)
      --image <path>       Send an image attachment (repeatable)
      --file <path>        Send a file attachment (repeatable)
      --stdin              Read message from stdin (best for long/special-char messages)
      --as-prompt          Inject message into agent session as a prompt
      --new-thread         Post to platform as a new thread (Slack only)
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Target session key (optional, picks first active)
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Prompt Injection (--as-prompt):
  When --as-prompt is set, the message is injected directly into the agent's
  session as if the user sent it. The agent will process it and respond.
  Useful for programmatic triggering of agent actions.

New Thread (--new-thread):
  When --new-thread is set, the message is posted as a new top-level thread
  instead of in the existing thread. Useful for Slack notifications that
  should not clutter the main conversation.

Combined (--as-prompt --new-thread):
  Both flags can be combined: the message is posted to a new thread AND
  injected into the agent session. Subsequent agent responses route to
  that thread. Ideal for completion watchers that need isolated threads.

Examples:
  cc-connect send "Daily summary: ..."
  cc-connect send -m "Build completed successfully"
  cc-connect send --message "Chart generated" --image /tmp/chart.png
  cc-connect send --file /tmp/report.pdf
  cc-connect send --as-prompt "Check the latest commits"
  cc-connect send --new-thread "Notification: build completed"
  cc-connect send --as-prompt --new-thread "Process this in a new thread"
  cc-connect send --stdin <<'EOF'
    Long message with "special" chars, $variables, and newlines
  EOF`)
}
