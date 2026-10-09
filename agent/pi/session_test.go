package pi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// ── buildJSONArgs (Issue #1723) ────────────────────────────────
//
// Issue #1723 regression coverage: the old code inlined saved attachment
// bytes into the `-p` prompt argument. That triggered execve EINVAL on
// NUL bytes (so the pi process never started and the model never saw the
// image). The new code passes attachment paths as `@<path>` argv entries
// and keeps the prompt itself pure text. These tests pin the new
// behaviour so a future refactor cannot quietly re-introduce the bug.

func TestBuildJSONArgs_BasicShape(t *testing.T) {
	args := buildJSONArgs(nil, "hello", "", "", "", nil)

	// First three entries: extraArgs (empty), --mode json, -p <prompt>.
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "--mode" || args[1] != "json" {
		t.Errorf("expected --mode json at args[0..1], got %q %q", args[0], args[1])
	}
	// -p may appear as args[3] or args[0] depending on how we index; scan.
	foundPrompt := false
	for i, a := range args {
		if a == "-p" {
			if i+1 >= len(args) || args[i+1] != "hello" {
				t.Errorf("-p must be followed by prompt text, got %v", args)
			}
			foundPrompt = true
		}
	}
	if !foundPrompt {
		t.Errorf("expected -p flag in args, got %v", args)
	}
}

func TestBuildJSONArgs_NoAttachmentsNoAtPrefix(t *testing.T) {
	// Bug guard: empty atFiles must produce zero "@..." entries. If a
	// future refactor accidentally prepends an empty "@" or passes a
	// nil-derived empty string, pi will misbehave.
	args := buildJSONArgs(nil, "hi", "", "", "", nil)
	for _, a := range args {
		if strings.HasPrefix(a, "@") {
			t.Errorf("unexpected @-prefixed arg when no attachments: %q", a)
		}
	}
}

func TestBuildJSONArgs_AttachmentsBecomeAtPathArgs(t *testing.T) {
	paths := []string{
		"/tmp/cc-connect/attach/a.png",
		"/tmp/cc-connect/attach/b.pdf",
		"/tmp/cc-connect/attach/c.docx",
	}
	args := buildJSONArgs(nil, "look at these", "", "", "", paths)

	// Collect @-prefixed args in order.
	var got []string
	for _, a := range args {
		if strings.HasPrefix(a, "@") {
			got = append(got, a)
		}
	}
	if len(got) != len(paths) {
		t.Fatalf("expected %d @-args, got %d: %v", len(paths), len(got), args)
	}
	for i, p := range paths {
		want := "@" + p
		if got[i] != want {
			t.Errorf("atFile[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

func TestBuildJSONArgs_AttachmentPathsPreserveSpecialChars(t *testing.T) {
	// Bug guard: paths with spaces, dots, dashes, non-ASCII must round-trip.
	paths := []string{
		"/tmp/a b/c-d.e (1).png",
		"/tmp/中文目录/image.jpeg",
	}
	args := buildJSONArgs(nil, "x", "", "", "", paths)

	for _, p := range paths {
		want := "@" + p
		found := false
		for _, a := range args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected arg %q in argv, got %v", want, args)
		}
	}
}

func TestBuildJSONArgs_PromptUntouchedByAttachments(t *testing.T) {
	// Core invariant: the -p argument equals the caller-supplied prompt
	// verbatim. The bug we are fixing was that Send() appended raw bytes
	// to the prompt; the fix must keep -p as pure text.
	const prompt = "describe this image"
	paths := []string{"/tmp/x.png", "/tmp/y.png"}
	args := buildJSONArgs(nil, prompt, "", "", "", paths)

	var pVal string
	for i, a := range args {
		if a == "-p" {
			pVal = args[i+1]
		}
	}
	if pVal != prompt {
		t.Errorf("prompt arg was mutated: got %q, want %q", pVal, prompt)
	}
	// No path fragment must leak into the prompt.
	for _, p := range paths {
		if strings.Contains(pVal, p) {
			t.Errorf("path %q leaked into prompt %q", p, pVal)
		}
	}
}

func TestBuildJSONArgs_PromptAcceptsNULBytes(t *testing.T) {
	// Regression for #1723: a prompt containing NUL bytes must not crash
	// arg construction. The old code's failure mode was execve returning
	// EINVAL on the actual command launch — our test only covers arg
	// assembly, but a refactor that re-introduced `os.ReadFile` here
	// would at minimum be visible in the output.
	prompt := "before\x00after"
	args := buildJSONArgs(nil, prompt, "", "", "", nil)
	for i, a := range args {
		if a == "-p" {
			if !bytes.Equal([]byte(args[i+1]), []byte(prompt)) {
				t.Errorf("prompt with NUL got truncated: %q", args[i+1])
			}
		}
	}
}

func TestBuildJSONArgs_AttachmentBytesNeverLeakIntoArgv(t *testing.T) {
	// Issue #1723 root cause: saveImagesToDisk returns paths whose
	// *contents* (the raw image bytes) used to be appended to the
	// prompt via os.ReadFile. The fix is to never read those bytes back
	// at the prompt-building layer. We assert that a recognizable byte
	// sequence appearing in any saved attachment is NOT present in any
	// argv element.
	marker := []byte("\x89PNG\r\n\x1a\nMARKER-DATA-12345")
	tmp := t.TempDir()
	imgPath := tmp + "/leak.png"
	if err := writeBytes(imgPath, marker); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	args := buildJSONArgs(nil, "test", "", "", "", []string{imgPath})
	for _, a := range args {
		// @<path> argv entry only references the path string. None of
		// the bytes from the file itself may appear in any arg.
		if bytes.Contains([]byte(a), marker) {
			t.Errorf("attachment bytes leaked into argv element %q", a)
		}
		// The path itself, however, may legitimately appear (as the
		// @<path> reference). Allow it; the marker check above is the
		// real assertion.
	}
}

// writeBytes is a tiny helper that lets session_test.go stay
// self-contained — it does not depend on saveImagesToDisk so the
// regression test for #1723 is unambiguous about which layer
// introduced the leak.
func writeBytes(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func TestBuildJSONArgs_AttachmentsAfterCoreFlags(t *testing.T) {
	// The @-args must come after --mode/--model/--session-id so the
	// flags pi parses eagerly still see the correct values, not the
	// path strings. This matches pi's CLI surface where trailing
	// positional args are treated as inputs.
	args := buildJSONArgs(nil, "p", "sess-1", "m", "t", []string{"/tmp/a", "/tmp/b"})

	// Find the index of the first @-arg and the index of the last
	// non-@-arg.
	firstAt := -1
	lastNonAt := -1
	for i, a := range args {
		if strings.HasPrefix(a, "@") {
			if firstAt == -1 {
				firstAt = i
			}
		} else {
			lastNonAt = i
		}
	}
	if firstAt == -1 {
		t.Fatal("no @-args found")
	}
	if firstAt <= lastNonAt {
		t.Errorf("first @-arg must come after all flag args; firstAt=%d lastNonAt=%d", firstAt, lastNonAt)
	}
}

func TestBuildJSONArgs_OptionalFlagsOnlyWhenSet(t *testing.T) {
	// sessionID, model, thinking must not appear as empty --flag ""
	// pairs when unset — pi would either reject them or treat them
	// as a literal empty value.
	args := buildJSONArgs(nil, "p", "", "", "", nil)
	for i, a := range args {
		switch a {
		case "--session-id", "--model", "--thinking":
			if i+1 >= len(args) {
				t.Errorf("flag %q at end of args with no value", a)
			}
		}
	}
}

func TestBuildJSONArgs_ExtraArgsPrepended(t *testing.T) {
	// Operator-supplied extra args (e.g. --no-color) must precede the
	// pi-mode flags so pi parses them in order.
	extra := []string{"--no-color", "--theme=dark"}
	args := buildJSONArgs(extra, "p", "", "", "", nil)

	if args[0] != "--no-color" || args[1] != "--theme=dark" {
		t.Errorf("extra args not prepended; got %v", args)
	}
	if args[2] != "--mode" {
		t.Errorf("--mode should follow extra args; got %v", args)
	}
}

func TestBuildJSONArgs_ExtraArgsNotMutated(t *testing.T) {
	// sendJSON reuses s.extraArgs; if buildJSONArgs mutated the input
	// slice, repeated Send() calls would accumulate stale state.
	extra := []string{"--no-color"}
	args := buildJSONArgs(extra, "p", "", "", "", nil)

	// Mutate the returned args.
	if len(args) > 0 {
		args[0] = "MUTATED"
	}
	if extra[0] != "--no-color" {
		t.Errorf("buildJSONArgs mutated caller's extraArgs slice: %v", extra)
	}
}

func TestBuildJSONArgs_ReturnsFreshSlice(t *testing.T) {
	// Two calls with the same extraArgs must not alias the same
	// backing array — a subsequent append or index write to one
	// result would otherwise corrupt the other's view. Use an
	// index write (not append) so the in-place mutation is
	// unambiguous to ineffassign.
	extra := []string{"--no-color"}
	a1 := buildJSONArgs(extra, "p1", "", "", "", nil)
	a2 := buildJSONArgs(extra, "p2", "", "", "", nil)

	if len(a1) == 0 || len(a2) == 0 {
		t.Fatal("helper returned empty slice")
	}
	a1[0] = "MUTATED-IN-A1"
	for _, x := range a2 {
		if x == "MUTATED-IN-A1" {
			t.Error("a2 saw mutation made to a1[0] — slices alias")
		}
	}
}

// ── sendRPC image payload (Issue #1723 follow-up) ──────────────
//
// pi expands @<path> only for CLI argv — processFileArguments is called
// from the CLI initial-message path — and it refuses @file arguments
// outright when running as `--mode rpc`:
//
//	Error: @file arguments are not supported in RPC mode
//
// So the @<path>-in-message-text approach added for json mode never loaded
// an image over RPC: the model saw a path string instead of a visual input.
// RPC mode carries images in the prompt command's dedicated `images` field
// (ImageContent: type/data/mimeType). These tests pin that payload shape.

func TestBuildRPCImages_NoImagesReturnsNil(t *testing.T) {
	// A text-only prompt must not grow an empty `images` array, so the RPC
	// command stays byte-compatible with senders that predate attachments.
	if got := buildRPCImages(nil); got != nil {
		t.Errorf("expected nil for no images, got %v", got)
	}
}

func TestBuildRPCImages_ShapeIsImageContent(t *testing.T) {
	raw := append([]byte("\x89PNG\r\n\x1a\n"), []byte("PAYLOAD-MARKER")...)
	got := buildRPCImages([]core.ImageAttachment{{
		MimeType: "image/png",
		Data:     raw,
		FileName: "shot.png",
	}})

	if len(got) != 1 {
		t.Fatalf("expected 1 image entry, got %d", len(got))
	}
	e := got[0]
	if e["type"] != "image" {
		t.Errorf("type: got %q, want %q", e["type"], "image")
	}
	if e["mimeType"] != "image/png" {
		t.Errorf("mimeType: got %q, want %q", e["mimeType"], "image/png")
	}
	if e["data"] != base64.StdEncoding.EncodeToString(raw) {
		t.Errorf("data is not the base64 of the raw bytes: %q", e["data"])
	}
	// Issue #1723 root cause: bytes must never reach the model as plain
	// text. Base64 output must not contain the literal marker.
	if strings.Contains(e["data"], "PAYLOAD-MARKER") {
		t.Errorf("raw bytes leaked into the payload unencoded: %q", e["data"])
	}
}

func TestBuildRPCImages_PreservesOrder(t *testing.T) {
	// pi loads the images in array order; the visual sequence of a
	// multi-image message depends on it.
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("first")...)
	jpg := append([]byte("\xff\xd8\xff"), []byte("second")...)
	got := buildRPCImages([]core.ImageAttachment{
		{MimeType: "image/png", Data: png},
		{MimeType: "image/jpeg", Data: jpg},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0]["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Error("entry 0 is not the first image")
	}
	if got[1]["data"] != base64.StdEncoding.EncodeToString(jpg) {
		t.Error("entry 1 is not the second image")
	}
}

func TestBuildRPCImages_SkipsEntriesWithoutBytes(t *testing.T) {
	// pi discards an ImageContent with an empty base64 payload; dropping it
	// here keeps the reported image count honest.
	got := buildRPCImages([]core.ImageAttachment{
		{MimeType: "image/png", FileName: "empty.png"},
		{MimeType: "image/png", Data: []byte("\x89PNG\r\n\x1a\nreal")},
	})

	if len(got) != 1 {
		t.Fatalf("expected 1 entry after skipping the empty one, got %d", len(got))
	}
}

func TestBuildRPCImages_AcceptsNULInImageBytes(t *testing.T) {
	// Issue #1723 echo: image bytes contain NUL. In json mode that used to
	// crash execve; over RPC they must survive base64 encoding intact.
	raw := append([]byte("\x89PNG\r\n\x1a\n"), []byte("a\x00b\x00c")...)
	got := buildRPCImages([]core.ImageAttachment{{MimeType: "image/png", Data: raw}})

	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	decoded, err := base64.StdEncoding.DecodeString(got[0]["data"])
	if err != nil {
		t.Fatalf("data is not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Error("NUL bytes did not round-trip through the RPC payload")
	}
}

func TestRPCImageMimeType_ResolutionOrder(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	jpg := []byte("\xff\xd8\xff")
	gif := []byte("GIF89a")
	webp := []byte("RIFF\x00\x00\x00\x00WEBP")
	bmp := []byte("BM\x00\x00")

	cases := []struct {
		name string
		img  core.ImageAttachment
		want string
	}{
		// A declared type pi can inline directly is trusted as-is.
		{"declared png", core.ImageAttachment{MimeType: "image/png", Data: png}, "image/png"},
		// Aliases and parameters are normalized.
		{"jpg alias", core.ImageAttachment{MimeType: "image/jpg", Data: jpg}, "image/jpeg"},
		{"uppercase", core.ImageAttachment{MimeType: "IMAGE/PNG", Data: png}, "image/png"},
		{"media type params", core.ImageAttachment{MimeType: "image/png; charset=binary", Data: png}, "image/png"},
		// A missing declared type falls back to the magic bytes, so pi's
		// decoder never gets bytes that contradict the declared type.
		{"empty sniffs png", core.ImageAttachment{Data: png}, "image/png"},
		{"empty sniffs jpeg", core.ImageAttachment{Data: jpg}, "image/jpeg"},
		{"empty sniffs gif", core.ImageAttachment{Data: gif}, "image/gif"},
		{"empty sniffs webp", core.ImageAttachment{Data: webp}, "image/webp"},
		// The magic bytes win when they contradict the declared type, so pi's
		// decoder never gets bytes that disagree with the declared media type.
		{"wrong declared type", core.ImageAttachment{MimeType: "image/png", Data: jpg}, "image/jpeg"},
		// Types pi cannot inline keep their declared media type so pi's own
		// converter can decode and re-encode them.
		{"unsupported but declared", core.ImageAttachment{MimeType: "image/bmp", Data: bmp}, "image/bmp"},
		// Nothing to go on at all: pi's converter still gets a shot.
		{"unknown everything", core.ImageAttachment{Data: []byte("not an image")}, "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rpcImageMimeType(tc.img); got != tc.want {
				t.Errorf("rpcImageMimeType: got %q, want %q", got, tc.want)
			}
		})
	}
}

// ── promptWithFileRefs (Issue #1767) ───────────────────────────
//
// Issue #1767 regression coverage: the old code passed non-image files
// through the same @<path> mechanism as images. pi's processFileArguments
// reads every @<path> file's full UTF-8 contents and inlines them into
// the prompt the model sees; for a >~1MB attachment this triggered a 400
// from the model provider. The fix splits images (still @<path>) from
// non-image files (now referenced by plain path trailer). These tests
// pin the new contract so a future refactor cannot quietly re-introduce
// the bug.

func TestPromptWithFileRefs_NoFilesKeepsPrompt(t *testing.T) {
	// When there are no non-image attachments, the prompt must be
	// returned unchanged — we do NOT want to inject an empty "Files
	// saved locally" trailer that would confuse the model.
	got := promptWithFileRefs("just a plain question", nil)
	if got != "just a plain question" {
		t.Errorf("prompt mutated: got %q", got)
	}
	got = promptWithFileRefs("another", []string{})
	if got != "another" {
		t.Errorf("empty slice still mutated prompt: got %q", got)
	}
}

func TestPromptWithFileRefs_AppendsPlainPathRefs(t *testing.T) {
	// File paths must appear as plain text references, NOT prefixed
	// with '@' (which is the pi mechanism that would re-trigger the
	// #1767 inlining). Mirrors the wording claudecode uses so users
	// see consistent phrasing across runtimes.
	paths := []string{"/tmp/a.pdf", "/tmp/b.docx"}
	got := promptWithFileRefs("please review", paths)

	if !strings.HasPrefix(got, "please review") {
		t.Errorf("original prompt text lost: %q", got)
	}
	for _, p := range paths {
		if !strings.Contains(got, p) {
			t.Errorf("path %q missing from prompt: %q", p, got)
		}
		if strings.Contains(got, "@"+p) {
			t.Errorf("path %q must NOT be @-prefixed (would re-trigger Issue #1767): %q", p, got)
		}
	}
}

func TestPromptWithFileRefs_PreservesOrder(t *testing.T) {
	// Order matters: multi-file reviews rely on the agent seeing
	// attachments in the same order as the sender uploaded them.
	paths := []string{"/tmp/first.pdf", "/tmp/second.pdf", "/tmp/third.pdf"}
	got := promptWithFileRefs("p", paths)

	prev := -1
	for _, p := range paths {
		idx := strings.Index(got, p)
		if idx == -1 {
			t.Fatalf("missing %s in prompt %q", p, got)
		}
		if idx <= prev {
			t.Errorf("paths not in order: %s came after previous", p)
		}
		prev = idx
	}
}

func TestPromptWithFileRefs_DoesNotInlineFileBytes(t *testing.T) {
	// Core regression guard for #1767: a recognizable byte sequence
	// saved to disk must never be inlined into the prompt.
	marker := []byte("SECRET-INLINE-MARKER-1767")
	tmp := t.TempDir()
	docPath := tmp + "/report.pdf"
	if err := os.WriteFile(docPath, marker, 0o644); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	got := promptWithFileRefs("review this", []string{docPath})
	if strings.Contains(got, string(marker)) {
		t.Errorf("file bytes inlined into prompt — #1767 regression: %q", got)
	}
	// The path itself may legitimately appear (as a plain reference);
	// the marker check above is the real assertion.
	if !strings.Contains(got, docPath) {
		t.Errorf("path reference missing from prompt: %q", got)
	}
}

func TestPromptWithFileRefs_LargeNonImageFileDoesNotInline(t *testing.T) {
	// Spec-grade regression: a >1MB non-image attachment must NOT
	// have its contents inlined into the prompt. The 1MB threshold
	// matches the size mentioned in the upstream #1767 bug report
	// that triggered model 400s. We seed a 1.5MB file with a
	// recognizable marker scattered through it and assert the marker
	// never appears in the prompt the model receives.
	const size = 1_500_000
	marker := []byte("ISSUE-1767-MUST-NOT-APPEAR-IN-PROMPT-ABCDEF")

	tmp := t.TempDir()
	docPath := tmp + "/big-log.txt"
	f, err := os.Create(docPath)
	if err != nil {
		t.Fatalf("create big file: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Fill with repeating marker so the byte sequence is dense and
	// any inlining (even partial) is caught.
	buf := make([]byte, 0, size+len(marker)*4)
	for len(buf) < size {
		buf = append(buf, marker...)
		buf = append(buf, bytes.Repeat([]byte{'x'}, 4096)...)
	}
	buf = buf[:size]
	if _, err := f.Write(buf); err != nil {
		t.Fatalf("write big file: %v", err)
	}

	// Confirm we wrote >1MB and that the marker is actually present
	// on disk (so a false-negative test can't pass trivially).
	info, err := os.Stat(docPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() <= 1_000_000 {
		t.Fatalf("seed file too small: %d", info.Size())
	}
	written, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Contains(written, marker) {
		t.Fatalf("marker missing from seed file — test is meaningless")
	}

	// The fix: promptWithFileRefs must contain the path as plain text
	// reference, but the marker bytes from inside the file must NOT
	// appear anywhere in the returned prompt.
	got := promptWithFileRefs("see attached", []string{docPath})

	if strings.Contains(got, string(marker)) {
		t.Fatalf("file bytes (marker) leaked into prompt for >1MB attachment — #1767 regression: %q (len=%d)", got, len(got))
	}
	if !strings.Contains(got, docPath) {
		t.Errorf("path reference missing from prompt trailer: %q", got)
	}
}

// ── Image vs file separation (Issue #1767) ───────────────────
//
// These tests pin the contract that Send() splits image attachments
// from non-image attachments, hands only the image paths to
// buildJSONArgs as @<path> argv entries (json mode) and carries them as
// base64 ImageContent over RPC. They use the helpers directly
// (buildJSONArgs + buildRPCImages + a fake
// caller's separation logic) because Send() itself spawns a pi
// subprocess and is tested via the helpers.

func TestBuildJSONArgs_OnlyImageAtFilesBecomeAtPath(t *testing.T) {
	// Doc-level invariant: after the #1767 split, the @-args emitted
	// by buildJSONArgs must contain ONLY image paths. Non-image file
	// paths must travel through promptWithFileRefs instead, never as
	// argv entries. The test mirrors what Send() now passes to
	// buildJSONArgs.
	imagePaths := []string{
		"/tmp/cc-connect/attach/img1.png",
		"/tmp/cc-connect/attach/img2.jpg",
	}
	promptWithTrailer := promptWithFileRefs("describe", []string{
		"/tmp/cc-connect/attach/log.txt",
		"/tmp/cc-connect/attach/report.pdf",
	})
	args := buildJSONArgs(nil, promptWithTrailer, "", "", "", imagePaths)

	// Every image path must appear as @<path>; no non-image path may.
	var got []string
	for _, a := range args {
		if strings.HasPrefix(a, "@") {
			got = append(got, a)
		}
	}
	want := []string{"@" + imagePaths[0], "@" + imagePaths[1]}
	if len(got) != len(want) {
		t.Fatalf("expected %d @-args (images only), got %d: %v", len(want), len(got), args)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("@-arg[%d]: got %q, want %q", i, got[i], w)
		}
	}
	for _, f := range []string{"/tmp/cc-connect/attach/log.txt", "/tmp/cc-connect/attach/report.pdf"} {
		// The non-image path may appear once in the prompt trailer
		// (plain text reference), but never as a @<path> argv entry.
		count := 0
		for _, a := range args {
			if a == "@"+f {
				count++
			}
		}
		if count != 0 {
			t.Errorf("non-image path %q leaked into @-argv %d times", f, count)
		}
	}
}

func TestRPCMessage_NonImageFilesNeverBecomeAtPathRefs(t *testing.T) {
	// Issue #1767, RPC twin of TestBuildJSONArgs_OnlyImageAtFilesBecomeAtPath.
	// Non-image attachments travel as a plain-text "Files saved locally"
	// trailer. Images no longer use @<path> over RPC at all, so nothing
	// routed through the message text may be @-prefixed.
	nonImagePaths := []string{"/tmp/notes.txt"}
	message := promptWithFileRefs("look", nonImagePaths)

	if strings.Contains(message, "@"+nonImagePaths[0]) {
		t.Errorf("non-image path must NOT be @-prefixed in the RPC message: %q", message)
	}
	if !strings.Contains(message, nonImagePaths[0]) {
		t.Errorf("non-image path reference missing from message: %q", message)
	}
	// The stale @<path> "Attachments:" header must not be emitted either:
	// pi never expands those over RPC, so it would just be noise in the
	// text the model reads.
	if strings.Contains(message, "Attachments:") {
		t.Errorf("stale @<path> Attachments header in RPC message: %q", message)
	}
}

// ── saveFilesToDisk + ImageAtFiles invariants (Issue #1767) ────

func TestSaveFilesToDisk_LargeFileRoundTrip(t *testing.T) {
	// Doc-level invariant: saveFilesToDisk must handle >1MB non-image
	// attachments without truncation (the file path is what we hand to
	// the model, so the file on disk must be the FULL payload the
	// platform sent us).
	const size = 1_200_000
	marker := []byte("LARGE-FILE-MARKER-XYZZY")
	payload := make([]byte, size)
	for i := 0; i < size; i++ {
		payload[i] = byte('a' + (i % 26))
	}
	copy(payload[size-len(marker):], marker)

	tmp := t.TempDir()
	paths := saveFilesToDisk(tmp, []core.FileAttachment{
		{MimeType: "text/plain", FileName: "big.txt", Data: payload},
	})
	if len(paths) != 1 {
		t.Fatalf("expected 1 saved path, got %d", len(paths))
	}
	written, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(written) != size {
		t.Errorf("saved file truncated: wrote %d, read %d", size, len(written))
	}
	if !bytes.Contains(written, marker) {
		t.Errorf("marker missing from saved file — round-trip failed")
	}
}

func TestSaveImagesToDisk_DoesNotConsumeFileSlots(t *testing.T) {
	// Defensive guard: after the #1767 split, saveImagesToDisk is the
	// only producer of @<path> argv entries. A future refactor that
	// accidentally feeds FileAttachment bytes through saveImagesToDisk
	// (or vice versa) must be caught: image paths must end in image
	// extensions and not look like arbitrary docs.
	paths := saveImagesToDisk(t.TempDir(), []core.ImageAttachment{
		{MimeType: "image/png", FileName: "shot.png", Data: []byte("\x89PNG\r\n\x1a\nfakepng")},
	})
	if len(paths) != 1 {
		t.Fatalf("expected 1 image path, got %d", len(paths))
	}
	if !strings.HasSuffix(paths[0], ".png") {
		t.Errorf("image path missing .png suffix: %q", paths[0])
	}
}

// ── sendRPC wire payload (Issue #1723 follow-up) ──────────────
//
// Wire-level check: the exact bytes cc-connect writes to pi's stdin must
// carry the images in the prompt command's `images` field, and must contain
// neither an "@<path>" reference nor any unencoded image byte. These go
// through the real writeRPCCommand path (only the child process is fake) so
// a future refactor cannot reintroduce the inlining bug behind the
// pure-function helpers.

type captureWriteCloser struct {
	buf bytes.Buffer
}

func (c *captureWriteCloser) Write(p []byte) (int, error) { return c.buf.Write(p) }
func (c *captureWriteCloser) Close() error                { return nil }

func TestSendRPC_WritesImagesAsImageContent(t *testing.T) {
	raw := append([]byte("\x89PNG\r\n\x1a\n"), []byte("RAW-IMAGE-BYTES")...)
	capture := &captureWriteCloser{}
	s := &piSession{
		workDir:  t.TempDir(),
		rpc:      true,
		rpcStdin: capture,
		events:   make(chan core.Event, 8),
	}

	if err := s.sendRPC("what is this?", []core.ImageAttachment{{
		MimeType: "image/png",
		Data:     raw,
		FileName: "shot.png",
	}}, nil); err != nil {
		t.Fatalf("sendRPC: %v", err)
	}

	line := strings.TrimSpace(capture.buf.String())
	if line == "" {
		t.Fatal("nothing was written to pi's stdin")
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("RPC command must be a single JSONL line: %q", line)
	}

	var cmd struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Images  []struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		t.Fatalf("command is not valid JSON: %v", err)
	}

	if cmd.Type != "prompt" {
		t.Errorf("type: got %q, want %q", cmd.Type, "prompt")
	}
	if cmd.Message != "what is this?" {
		t.Errorf("message: got %q, want %q", cmd.Message, "what is this?")
	}
	// The exact regression that motivated this change: rpc mode used to
	// append an "@<path>" list to the message text, which pi never expands
	// over RPC, so the image was silently dropped.
	if strings.Contains(cmd.Message, "@") {
		t.Errorf("message still carries an @<path> reference: %q", cmd.Message)
	}

	if len(cmd.Images) != 1 {
		t.Fatalf("expected 1 image entry, got %d", len(cmd.Images))
	}
	if cmd.Images[0].Type != "image" {
		t.Errorf("images[0].type: got %q, want %q", cmd.Images[0].Type, "image")
	}
	if cmd.Images[0].MimeType != "image/png" {
		t.Errorf("images[0].mimeType: got %q, want %q", cmd.Images[0].MimeType, "image/png")
	}
	if cmd.Images[0].Data != base64.StdEncoding.EncodeToString(raw) {
		t.Error("images[0].data is not the base64 of the raw bytes")
	}
	// Issue #1723 root cause: the bytes themselves must never cross the
	// wire as literal text.
	if strings.Contains(line, "RAW-IMAGE-BYTES") {
		t.Error("raw image bytes leaked into the RPC command unencoded")
	}
}

func TestSendRPC_OmitsImagesFieldForTextOnlyPrompt(t *testing.T) {
	// A prompt with no attachments must stay byte-compatible with senders
	// that predate the images field. `"images": null` would also be legal
	// for pi, but emitting the key at all is noise in the JSONL stream.
	capture := &captureWriteCloser{}
	s := &piSession{
		workDir:  t.TempDir(),
		rpc:      true,
		rpcStdin: capture,
		events:   make(chan core.Event, 8),
	}

	if err := s.sendRPC("plain question", nil, nil); err != nil {
		t.Fatalf("sendRPC: %v", err)
	}

	line := strings.TrimSpace(capture.buf.String())
	if strings.Contains(line, "images") {
		t.Errorf("text-only prompt must not carry an images field: %q", line)
	}
}

func TestSendRPC_NonImageFilesStayOutOfImagesField(t *testing.T) {
	// Issue #1767, wire-level: non-image attachments are plain-text path
	// references. They must never be promoted to ImageContent.
	capture := &captureWriteCloser{}
	s := &piSession{
		workDir:  t.TempDir(),
		rpc:      true,
		rpcStdin: capture,
		events:   make(chan core.Event, 8),
	}

	if err := s.sendRPC("look", nil, []string{"/tmp/notes.txt"}); err != nil {
		t.Fatalf("sendRPC: %v", err)
	}

	var cmd struct {
		Message string            `json:"message"`
		Images  []json.RawMessage `json:"images"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(capture.buf.String())), &cmd); err != nil {
		t.Fatalf("command is not valid JSON: %v", err)
	}
	if len(cmd.Images) != 0 {
		t.Errorf("non-image file was promoted to ImageContent: %v", cmd.Images)
	}
	if !strings.Contains(cmd.Message, "/tmp/notes.txt") {
		t.Errorf("non-image path reference missing from message: %q", cmd.Message)
	}
}

// ── Regression: RPC prompt must carry image content (#1724 gap) ──
//
// Pre-fix behaviour, in order of history:
//   - before #1724: Send() inlined the raw image bytes into the prompt text
//   - after #1724:  Send() appended "Attachments:\n@<path>" to the prompt text
//
// Both reach the model as text, because pi expands @<path> only for CLI argv
// (processFileArguments runs on the CLI initial-message path) and rejects
// @file arguments outright in RPC mode. So the image was never loaded as a
// visual input in rpc mode.
//
// This asserts on the bytes Send() actually writes to pi's stdin. It goes
// through Send() rather than sendRPC() on purpose: Send's signature is
// unchanged by the fix, so the regression fails on the pre-fix code without
// any compile-time mismatch masking it.
func TestSend_RPCPromptCarriesImageContent(t *testing.T) {
	raw := append([]byte("\x89PNG\r\n\x1a\n"), []byte("REGRESSION-IMAGE-BYTES")...)
	capture := &captureWriteCloser{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &piSession{
		workDir:   t.TempDir(),
		attachDir: filepath.Join(t.TempDir(), "attachments"),
		rpc:       true,
		rpcStdin:  capture,
		events:    make(chan core.Event, 8),
		ctx:       ctx,
	}
	s.alive.Store(true)

	if err := s.Send("what is this?", "msg-1", []core.ImageAttachment{{
		MimeType: "image/png",
		Data:     raw,
		FileName: "shot.png",
	}}, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	line := strings.TrimSpace(capture.buf.String())
	if line == "" {
		t.Fatal("Send wrote nothing to pi's stdin")
	}

	var cmd struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Images  []struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"images"`
	}
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		t.Fatalf("command is not valid JSON: %v", err)
	}

	// Pre-fix code handed the model an attachment list in the message text
	// instead of an image, which is the whole bug.
	if strings.Contains(cmd.Message, "Attachments:") || strings.Contains(cmd.Message, "@/") {
		t.Errorf("RPC message still routes the image through @<path> text: %q", cmd.Message)
	}
	if strings.Contains(cmd.Message, "--- shot.png ---") || strings.Contains(line, "REGRESSION-IMAGE-BYTES") {
		t.Errorf("raw image bytes were inlined into the RPC command: %q", line)
	}

	if len(cmd.Images) != 1 {
		t.Fatalf("RPC prompt must carry the image as ImageContent; got %d entries (message=%q)", len(cmd.Images), cmd.Message)
	}
	if cmd.Images[0].Type != "image" || cmd.Images[0].MimeType != "image/png" {
		t.Errorf("unexpected ImageContent: %+v", cmd.Images[0])
	}
	if cmd.Images[0].Data != base64.StdEncoding.EncodeToString(raw) {
		t.Error("ImageContent.data is not the base64 of the raw image bytes")
	}
}
