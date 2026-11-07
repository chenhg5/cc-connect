package pi

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestModernRPCSendUsesNativeImagesAndDurableFiles(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.attachDir = t.TempDir()
	done := make(chan error, 1)
	images := []core.ImageAttachment{{MimeType: "image/png", Data: []byte("image bytes")}}
	files := []core.FileAttachment{{FileName: "same.txt", Data: []byte("first secret")}, {FileName: "same.txt", Data: []byte("second secret")}}
	go func() { done <- s.Send("", "", images, files) }()
	var cmd map[string]any
	select {
	case cmd = <-w.commands:
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt")
	}
	s.handleEvent(issue2161UnitResponse(cmd, true, "started"))
	issue2161UnitWaitSend(t, done, "")
	native, ok := cmd["images"].([]any)
	if !ok || len(native) != 1 {
		t.Fatalf("missing native RPC images: %v", cmd)
	}
	image := native[0].(map[string]any)
	if image["type"] != "image" || image["mimeType"] != "image/png" || image["data"] != base64.StdEncoding.EncodeToString(images[0].Data) {
		t.Fatalf("invalid image: %v", image)
	}
	message := cmd["message"].(string)
	if strings.Contains(message, "secret") || strings.Contains(message, "Attachments:") {
		t.Fatalf("inlined attachment: %q", message)
	}
	var saved []string
	if err := filepath.Walk(s.attachDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			saved = append(saved, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %v, want two distinct files", saved)
	}
	for _, path := range saved {
		if !strings.Contains(message, path) {
			t.Fatalf("missing file reference %q", path)
		}
	}
}

func TestModernRPCSendPreparationFailureDoesNotWrite(t *testing.T) {
	s, w := issue2161UnitSession(t)
	root := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.attachDir = root
	err := s.Send("", "", nil, []core.FileAttachment{{FileName: "file", Data: []byte("data")}})
	var rejected *core.SteerNotAcceptedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error=%v, want known rejection", err)
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("partial prompt sent: %v", cmd)
	default:
	}
}

// Empty/reused transport IDs must not replace inputs an active run still reads.
func TestModernRPCBusyAttachmentsPreserveEarlierInputs(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.attachDir = t.TempDir()
	s.handleEvent(map[string]any{"type": "agent_start"})
	var paths []string
	for _, id := range []string{"", "", "same", "same"} {
		done := make(chan error, 1)
		go func() {
			_, err := s.SendBusySteer(context.Background(), "", id, nil, []core.FileAttachment{{FileName: "same.txt", Data: []byte(id + " content")}})
			done <- err
		}()
		var cmd map[string]any
		select {
		case cmd = <-w.commands:
		case <-time.After(5 * time.Second):
			t.Fatal("no file-only steer")
		}
		s.handleEvent(issue2161UnitResponse(cmd, true, "queued"))
		issue2161UnitWaitSend(t, done, "")
		message := cmd["message"].(string)
		start := strings.Index(message, "please read them: ") + len("please read them: ")
		path := strings.TrimSuffix(message[start:], ")")
		paths = append(paths, path)
		for i, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := " content"
			if i >= 2 {
				want = "same content"
			}
			if string(data) != want {
				t.Fatalf("earlier file overwritten: %q", data)
			}
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			t.Fatalf("reused path %q", path)
		}
		seen[path] = true
	}
}

func TestModernRPCImageOnlySteerUsesNativePayload(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.handleEvent(map[string]any{"type": "agent_start"})
	done := make(chan error, 1)
	go func() {
		_, err := s.SendBusySteer(context.Background(), "", "", []core.ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpeg")}}, nil)
		done <- err
	}()
	var cmd map[string]any
	select {
	case cmd = <-w.commands:
	case <-time.After(5 * time.Second):
		t.Fatal("no image-only steer")
	}
	s.handleEvent(issue2161UnitResponse(cmd, true, "queued"))
	issue2161UnitWaitSend(t, done, "")
	if cmd["message"] != "" || len(cmd["images"].([]any)) != 1 {
		t.Fatalf("bad image-only payload: %v", cmd)
	}
}

func TestModernRPCInvalidImageRejectsWholeInput(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.attachDir = t.TempDir()
	_, err := s.SendBusySteer(context.Background(), "must not send", "", []core.ImageAttachment{{MimeType: "image/png"}}, []core.FileAttachment{{FileName: "ok", Data: []byte("valid")}})
	var rejected *core.SteerNotAcceptedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error=%v, want preparation rejection", err)
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("partial prompt: %v", cmd)
	default:
	}
	entries, err := os.ReadDir(s.attachDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid input saved files: %v %v", entries, err)
	}
}

func TestModernRPCFilePreparationFailureRemovesOnlyUnsentInput(t *testing.T) {
	s, w := issue2161UnitSession(t)
	s.attachDir = t.TempDir()
	earlier := filepath.Join(s.attachDir, "earlier.txt")
	if err := os.WriteFile(earlier, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.SendBusySteer(context.Background(), "", "", nil, []core.FileAttachment{{FileName: "good.txt", Data: []byte("written first")}, {FileName: "bad\x00.txt", Data: []byte("fails")}})
	var rejected *core.SteerNotAcceptedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error=%v, want preparation rejection", err)
	}
	select {
	case cmd := <-w.commands:
		t.Fatalf("partial prompt: %v", cmd)
	default:
	}
	entries, err := os.ReadDir(s.attachDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "earlier.txt" {
		t.Fatalf("failed input not cleaned or earlier input lost: %v %v", entries, err)
	}
}
