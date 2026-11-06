package pi

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func startModernRPCSend(t *testing.T, s *piSession, w *issue2161UnitWire, text string, images []core.ImageAttachment, files []core.FileAttachment) (map[string]any, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- s.Send(text, "", images, files)
	}()
	t.Cleanup(func() {
		s.cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("Send did not stop after session cancellation")
		}
	})
	select {
	case cmd := <-w.commands:
		if cmd["type"] != "prompt" || cmd["id"] == nil || cmd["id"] == "" {
			t.Fatalf("expected correlated RPC prompt, got %v", cmd)
		}
		select {
		case err := <-done:
			t.Fatalf("Send returned before its ACK: %v", err)
		default:
		}
		return cmd, done
	case err := <-done:
		t.Fatalf("Send returned without writing a prompt: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not write a prompt")
	}
	return nil, nil
}

func TestModernRPCSend_InvalidImagesPreserveTextAndValidAttachments(t *testing.T) {
	first := core.ImageAttachment{FileName: "first.png", MimeType: "image/png", Data: []byte("first image")}
	last := core.ImageAttachment{FileName: "last.jpg", MimeType: "image/jpeg", Data: []byte("last image")}
	emptyMIME := core.ImageAttachment{FileName: "unknown.png", Data: []byte("unknown image")}
	octetStream := core.ImageAttachment{MimeType: "application/octet-stream", Data: []byte("unrecognized image")}
	emptyData := core.ImageAttachment{MimeType: "image/png"}
	for _, tc := range []struct {
		name   string
		images []core.ImageAttachment
		valid  []core.ImageAttachment
		files  []core.FileAttachment
	}{
		{name: "empty_mime", images: []core.ImageAttachment{emptyMIME}},
		{name: "octet_stream", images: []core.ImageAttachment{octetStream}},
		{name: "empty_data", images: []core.ImageAttachment{emptyData}},
		{name: "all_invalid", images: []core.ImageAttachment{emptyMIME, octetStream, emptyData}},
		{name: "invalid_first", images: []core.ImageAttachment{emptyMIME, first, last}, valid: []core.ImageAttachment{first, last}},
		{name: "invalid_middle", images: []core.ImageAttachment{first, octetStream, last}, valid: []core.ImageAttachment{first, last}},
		{name: "invalid_last", images: []core.ImageAttachment{first, last, emptyData}, valid: []core.ImageAttachment{first, last}},
	} {
		// Mixed cases also prove that skipping an image preserves file delivery.
		if len(tc.valid) > 0 {
			tc.files = []core.FileAttachment{{FileName: "notes.txt", Data: []byte("file contents")}}
		}
		t.Run(tc.name, func(t *testing.T) {
			s, w := issue2161UnitSession(t)
			s.attachDir = t.TempDir()
			before := make([]core.ImageAttachment, len(tc.images))
			for i, image := range tc.images {
				before[i] = image
				before[i].Data = append([]byte(nil), image.Data...)
			}
			const text = "  keep this explanation\nwith its original whitespace  "
			cmd, done := startModernRPCSend(t, s, w, text, tc.images, tc.files)
			s.handleEvent(issue2161UnitResponse(cmd, true, "started"))
			issue2161UnitWaitSend(t, done, "")
			if !reflect.DeepEqual(tc.images, before) {
				t.Fatalf("Send modified its input images: got %+v, want %+v", tc.images, before)
			}
			native, hasImages := cmd["images"]
			if len(tc.valid) == 0 {
				if hasImages {
					t.Fatalf("all-invalid input should omit images: %v", cmd)
				}
			} else {
				var want []any
				for _, image := range tc.valid {
					want = append(want, map[string]any{"type": "image", "mimeType": image.MimeType, "data": base64.StdEncoding.EncodeToString(image.Data)})
				}
				if !reflect.DeepEqual(native, want) {
					t.Fatalf("native images = %v, want %v", native, want)
				}
			}
			var paths []string
			if err := filepath.Walk(s.attachDir, func(path string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					paths = append(paths, path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(paths) != len(tc.files) {
				t.Fatalf("saved files = %v, want %d", paths, len(tc.files))
			}
			for i, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != string(tc.files[i].Data) {
					t.Fatalf("saved file = %q, %v", data, err)
				}
			}
			if cmd["message"] != promptWithFileRefs(text, paths) {
				t.Fatalf("prompt lost text or file references: %v", cmd)
			}
			select {
			case extra := <-w.commands:
				t.Fatalf("Send wrote more than one prompt: %v", extra)
			default:
			}
		})
	}
}

func TestModernRPCSend_InvalidImageOnlyStillUsesACK(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		name := "accepted"
		if rejected {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			s, w := issue2161UnitSession(t)
			cmd, done := startModernRPCSend(t, s, w, "", []core.ImageAttachment{{Data: []byte("unknown image")}}, nil)
			if cmd["message"] != "" || cmd["images"] != nil {
				t.Fatalf("filtered input gained placeholder content: %v", cmd)
			}
			detail, wantError := "started", ""
			if rejected {
				detail, wantError = "peer rejected empty prompt", "peer rejected empty prompt"
			}
			s.handleEvent(issue2161UnitResponse(cmd, !rejected, detail))
			issue2161UnitWaitSend(t, done, wantError)
		})
	}
}

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
	for _, tc := range []struct {
		name   string
		images []core.ImageAttachment
	}{
		{name: "without_images"},
		{name: "with_skipped_image", images: []core.ImageAttachment{{Data: []byte("unknown image")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w := issue2161UnitSession(t)
			root := filepath.Join(t.TempDir(), "not-directory")
			if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			s.attachDir = root
			err := s.Send("", "", tc.images, []core.FileAttachment{{FileName: "file", Data: []byte("data")}})
			var rejected *core.SteerNotAcceptedError
			if !errors.As(err, &rejected) || !strings.Contains(err.Error(), "prepare attachments directory") {
				t.Fatalf("error=%v, want attachment directory rejection", err)
			}
			select {
			case cmd := <-w.commands:
				t.Fatalf("partial prompt sent: %v", cmd)
			default:
			}
		})
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
	for _, tc := range []struct {
		name  string
		image core.ImageAttachment
	}{
		{"empty_data", core.ImageAttachment{MimeType: "image/png"}},
		{"empty_mime", core.ImageAttachment{Data: []byte("image")}},
		{"octet_stream", core.ImageAttachment{MimeType: "application/octet-stream", Data: []byte("image")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, invalidFirst := range []bool{true, false} {
				s, w := issue2161UnitSession(t)
				s.attachDir = t.TempDir()
				s.handleEvent(map[string]any{"type": "agent_start"})
				images := []core.ImageAttachment{tc.image, {MimeType: "image/png", Data: []byte("valid")}}
				if !invalidFirst {
					images[0], images[1] = images[1], images[0]
				}
				_, err := s.SendBusySteer(context.Background(), "must not send", "", images, []core.FileAttachment{{FileName: "ok", Data: []byte("valid")}})
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
				issue2161UnitNoTerminal(t, s, "rejected image supplement")
				if !s.Alive() {
					t.Fatal("image rejection ended the original session")
				}
				s.handleEvent(map[string]any{"type": "agent_settled"})
				events := drainEvents(s)
				if len(events) != 1 || events[0].Type != core.EventResult || !events[0].Done {
					t.Fatalf("original task did not finish normally: %+v", events)
				}
			}
		})
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
