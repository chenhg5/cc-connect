package feishu

import "testing"

func TestExtractPostPlainTextPicksSameLocaleEveryCall(t *testing.T) {
	body := `{"zh_cn":{"title":"","content":[[{"tag":"text","text":"你好"}]]},"en_us":{"title":"","content":[[{"tag":"text","text":"hello"}]]}}`
	want := extractPostPlainText(body)
	for i := 0; i < 200; i++ {
		if got := extractPostPlainText(body); got != want {
			t.Fatalf("iteration %d: got %q, first call returned %q", i, got, want)
		}
	}
	if want != "hello" {
		t.Fatalf("got %q, want the first locale in sorted order (en_us)", want)
	}
}

func TestDetectMimeTypeShortRIFFDoesNotPanic(t *testing.T) {
	data := []byte("RIFF\x00\x00\x00\x00WE")
	if got := detectMimeType(data); got != "image/png" {
		t.Fatalf("got %q, want image/png fallback", got)
	}
}
