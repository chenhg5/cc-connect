package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestParseSendArgs_AttachmentsWithoutMessage(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "chart.png")
	docPath := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(docPath, []byte("hello report"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	req, dataDir, err := parseSendArgs([]string{"--image", imgPath, "--file", docPath})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if dataDir != "" {
		t.Fatalf("dataDir = %q, want empty", dataDir)
	}
	if req.Message != "" {
		t.Fatalf("message = %q, want empty", req.Message)
	}
	if len(req.Images) != 1 {
		t.Fatalf("images len = %d, want 1", len(req.Images))
	}
	if req.Images[0].FileName != "chart.png" {
		t.Fatalf("image filename = %q, want chart.png", req.Images[0].FileName)
	}
	if req.Images[0].MimeType != "image/png" {
		t.Fatalf("image mime = %q, want image/png", req.Images[0].MimeType)
	}
	if len(req.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(req.Files))
	}
	if req.Files[0].FileName != "report.txt" {
		t.Fatalf("file filename = %q, want report.txt", req.Files[0].FileName)
	}
}

func TestParseSendArgs_RequiresMessageOrAttachment(t *testing.T) {
	_, _, err := parseSendArgs(nil)
	if err == nil {
		t.Fatal("expected error for empty send args")
	}
}

func TestParseSendArgs_UsesSessionEnvFallback(t *testing.T) {
	t.Setenv("CC_PROJECT", "demo")
	t.Setenv("CC_SESSION_KEY", "telegram:123:456")

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	req, _, err := parseSendArgs([]string{"--image", imgPath})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if req.Project != "demo" {
		t.Fatalf("project = %q, want demo", req.Project)
	}
	if req.SessionKey != "telegram:123:456" {
		t.Fatalf("session = %q, want telegram:123:456", req.SessionKey)
	}
}

// TestParseSendArgs_AudioPopulatesAudios is the regression for
// t-20260615-cqjbk1: --audio and --video must NOT land in req.Files
// (which would route them through SendFile and skip
// AudioSender / VideoSender). They each get their own slice.
func TestParseSendArgs_AudioPopulatesAudios(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "voice.opus")
	if err := os.WriteFile(audioPath, []byte("opus"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	req, _, err := parseSendArgs([]string{"--audio", audioPath})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if len(req.Files) != 0 {
		t.Fatalf("req.Files len = %d, want 0 (audio must NOT be appended into Files)", len(req.Files))
	}
	if len(req.Audios) != 1 {
		t.Fatalf("req.Audios len = %d, want 1", len(req.Audios))
	}
	if req.Audios[0].FileName != "voice.opus" {
		t.Fatalf("audio filename = %q, want voice.opus", req.Audios[0].FileName)
	}
	if string(req.Audios[0].Data) != "opus" {
		t.Fatalf("audio data = %q, want %q", req.Audios[0].Data, "opus")
	}
}

func TestParseSendArgs_VideoPopulatesVideos(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "demo.mp4")
	if err := os.WriteFile(videoPath, []byte("mp4"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	req, _, err := parseSendArgs([]string{"--video", videoPath})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if len(req.Files) != 0 {
		t.Fatalf("req.Files len = %d, want 0 (video must NOT be appended into Files)", len(req.Files))
	}
	if len(req.Videos) != 1 {
		t.Fatalf("req.Videos len = %d, want 1", len(req.Videos))
	}
	if req.Videos[0].FileName != "demo.mp4" {
		t.Fatalf("video filename = %q, want demo.mp4", req.Videos[0].FileName)
	}
}

func TestParseSendArgs_AudioVideoFileMixed_StaySeparate(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "voice.opus")
	videoPath := filepath.Join(dir, "demo.mp4")
	docPath := filepath.Join(dir, "notes.md")
	for _, f := range [][2]string{
		{audioPath, "opus"},
		{videoPath, "mp4"},
		{docPath, "hello"},
	} {
		if err := os.WriteFile(f[0], []byte(f[1]), 0o644); err != nil {
			t.Fatalf("write %s: %v", f[0], err)
		}
	}

	req, _, err := parseSendArgs([]string{
		"--audio", audioPath,
		"--video", videoPath,
		"--file", docPath,
	})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if len(req.Audios) != 1 || req.Audios[0].FileName != "voice.opus" {
		t.Fatalf("req.Audios = %#v", req.Audios)
	}
	if len(req.Videos) != 1 || req.Videos[0].FileName != "demo.mp4" {
		t.Fatalf("req.Videos = %#v", req.Videos)
	}
	if len(req.Files) != 1 || req.Files[0].FileName != "notes.md" {
		t.Fatalf("req.Files = %#v (only the --file should land here)", req.Files)
	}
}

func TestParseSendArgs_TTSOnly(t *testing.T) {
	t.Setenv("CC_PROJECT", "demo")
	t.Setenv("CC_SESSION_KEY", "telegram:123:456")

	req, _, err := parseSendArgs([]string{"--tts", "hello voice"})
	if err != nil {
		t.Fatalf("parseSendArgs returned error: %v", err)
	}
	if req.Project != "demo" {
		t.Fatalf("project = %q, want demo", req.Project)
	}
	if req.SessionKey != "telegram:123:456" {
		t.Fatalf("session = %q, want telegram:123:456", req.SessionKey)
	}
	if req.TTSText != "hello voice" {
		t.Fatalf("tts text = %q", req.TTSText)
	}
	if req.Message != "" {
		t.Fatalf("message = %q, want empty", req.Message)
	}
}

func TestParseSendArgs_AudioRejectsNonAudio(t *testing.T) {
	dir := t.TempDir()
	docPath := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(docPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}

	_, _, err := parseSendArgs([]string{"--audio", docPath})
	if err == nil {
		t.Fatal("expected non-audio attachment to be rejected")
	}
	if !strings.Contains(err.Error(), "not audio media") {
		t.Fatalf("error = %q, want not audio media", err.Error())
	}
}

func TestDetectAttachmentMimeType_UsesExtensionFallback(t *testing.T) {
	mimeType := detectAttachmentMimeType("note.md", []byte("plain"))
	if mimeType != "text/markdown; charset=utf-8" && mimeType != "text/markdown" {
		t.Fatalf("mimeType = %q, want markdown mime", mimeType)
	}
}

func TestReadAttachment_SizeLimit(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readAttachment(small); err != nil {
		t.Fatalf("small file should succeed: %v", err)
	}

	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAttachmentSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, _, _, err := readAttachment(big); err == nil {
		t.Fatal("oversized file should be rejected")
	}
}

func TestReadAttachment_CleanPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "test.txt")
	if err := os.WriteFile(f, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Path with ../ should still work after cleaning
	dirty := filepath.Join(sub, "..", "sub", "test.txt")
	data, name, _, err := readAttachment(dirty)
	if err != nil {
		t.Fatalf("readAttachment with dirty path: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("unexpected data: %q", data)
	}
	if name != "test.txt" {
		t.Errorf("unexpected filename: %q", name)
	}
}

func TestBuildSendPayload_JSONRoundTrip(t *testing.T) {
	req := core.SendRequest{
		Project:    "demo",
		SessionKey: "telegram:1:2",
		Message:    "done",
		TTSText:    "voice done",
		Images: []core.ImageAttachment{{
			MimeType: "image/png",
			Data:     []byte("img"),
			FileName: "a.png",
		}},
		Files: []core.FileAttachment{{
			MimeType: "text/plain",
			Data:     []byte("doc"),
			FileName: "a.txt",
		}},
	}

	body, err := buildSendPayload(req)
	if err != nil {
		t.Fatalf("buildSendPayload returned error: %v", err)
	}

	var decoded core.SendRequest
	if err := decodeSendPayload(body, &decoded); err != nil {
		t.Fatalf("decodeSendPayload returned error: %v", err)
	}
	if len(decoded.Images) != 1 || string(decoded.Images[0].Data) != "img" {
		t.Fatalf("decoded images = %#v", decoded.Images)
	}
	if decoded.TTSText != "voice done" {
		t.Fatalf("decoded tts_text = %q", decoded.TTSText)
	}
	if len(decoded.Files) != 1 || string(decoded.Files[0].Data) != "doc" {
		t.Fatalf("decoded files = %#v", decoded.Files)
	}
}
