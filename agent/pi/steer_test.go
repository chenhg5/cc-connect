package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bufWriteCloser struct{ *bytes.Buffer }

func (bufWriteCloser) Close() error { return nil }

func TestSendRPC_SteersWithoutChangingPrompt(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prompt  string
		images  []string
		files   []string
		message string
	}{
		{
			name: "text", prompt: "补充：\"保留测试\"\n第二行",
			message: "补充：\"保留测试\"\n第二行",
		},
		{
			name: "attachments", prompt: "inspect these",
			images: []string{"/tmp/chart.png"}, files: []string{"/tmp/report.txt"},
			message: "inspect these\n\n(Files saved locally, please read them: /tmp/report.txt)\n\nAttachments:\n@/tmp/chart.png\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := &piSession{rpcStdin: bufWriteCloser{&buf}}
			if err := s.sendRPC(tt.prompt, tt.images, tt.files); err != nil {
				t.Fatal(err)
			}
			if strings.Count(buf.String(), "\n") != 1 {
				t.Fatalf("expected a single JSONL command, got %q", buf.String())
			}
			var cmd map[string]any
			if err := json.Unmarshal(buf.Bytes(), &cmd); err != nil {
				t.Fatal(err)
			}
			// A prompt starts an idle agent; streamingBehavior handles the same
			// request if the agent is still running when it receives the command.
			if cmd["type"] != "prompt" || cmd["streamingBehavior"] != "steer" {
				t.Fatalf("expected prompt with streamingBehavior=steer, got %s", buf.String())
			}
			if cmd["message"] != tt.message {
				t.Fatalf("message = %q, want %q", cmd["message"], tt.message)
			}
		})
	}
}

type failingRPCWriter struct{ err error }

func (w failingRPCWriter) Write([]byte) (int, error) { return 0, w.err }
func (failingRPCWriter) Close() error                { return nil }

func TestSendRPC_PropagatesWriteError(t *testing.T) {
	want := errors.New("broken RPC pipe")
	s := &piSession{rpcStdin: failingRPCWriter{want}}
	if err := s.sendRPC("supplement", nil, nil); !errors.Is(err, want) {
		t.Fatalf("sendRPC() = %v, want wrapped %v", err, want)
	}
}

func TestSend_TextSupplementPreservesInFlightAttachments(t *testing.T) {
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(filepath.Join(attachDir, "message-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(attachDir, "image.png"),
		filepath.Join(attachDir, "message-1", "report.txt"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("still needed by the running task"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	s := &piSession{rpc: true, rpcStdin: bufWriteCloser{&buf}, attachDir: attachDir}
	s.alive.Store(true)
	// /ps supplies no message ID or attachments. It must not remove the
	// entire session attachment directory while the agent is reading it.
	if err := s.Send("also add unit tests", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("in-flight attachment removed: %v", err)
		} else if string(got) != "still needed by the running task" {
			t.Errorf("in-flight attachment changed: %q", got)
		}
	}
}
