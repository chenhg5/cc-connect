package matrix

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestLooksLikeASCIIDiagram(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"box drawing", "┌──┐\n│ a │\n└──┘", true},
		{"arrows only line", "a ──► b ──► c", true},
		{"double line box", "╔══╗\n║ x ║\n╚══╝", true},
		{"plain bash", "echo hello\nls -la | wc -l", false},
		{"plain json", "{\"a\": 1}", false},
		{"empty", "", false},
		{"short arrow no boxes", "a -> b", false},
		{"vertical flow", "Client\n  ↓\nServer\n  ↓\nDB", true},
		{"classic ascii box", "+------+\n|  X   |\n+------+", true},
		{"ascii flow arrows", "| A | --> | B |\n| C |  v   | D |", true},
		{"ascii pipeline", "A --> B --> C", true},
		{"untyped config no art", "server {\n  listen 80;\n}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeASCIIDiagram(tt.code, ""); got != tt.want {
				t.Errorf("looksLikeASCIIDiagram(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestRenderMermaidConvertsASCIIWhenEnabled(t *testing.T) {
	p := &Platform{renderMermaidDiagrams: true, krokiURL: "http://127.0.0.1:8130"}
	content := "До текст.\n```\n┌────┐\n│ X │\n└────┘\n```\nПосле."
	out, imgs := p.renderMermaid(content)
	if strings.Contains(out, "┌") {
		t.Fatalf("ascii block not replaced:\n%s", out)
	}
	if !strings.Contains(out, "во вложении") || len(imgs) != 1 {
		t.Fatalf("expected note + 1 image, out=%q imgs=%d", out, len(imgs))
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(imgs[0].Data))
	if err != nil {
		t.Fatalf("invalid png: %v", err)
	}
	if cfg.Width < 50 || cfg.Height < 30 {
		t.Fatalf("ascii png too small: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestASCIIToPNGWideDiagramNotCropped(t *testing.T) {
	p := &Platform{}
	wide := "+--------+       +--------+       +-------------+\n" +
		"| Client | ----> | Server | ----> | Database DB |\n" +
		"+--------+       +--------+       +-------------+"
	data, err := p.asciiToPNG(wide)
	if err != nil {
		t.Fatalf("asciiToPNG: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("invalid png: %v", err)
	}
	if cfg.Width <= cfg.Height {
		t.Fatalf("wide diagram cropped or padded wrong: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestRenderMermaidDisabledKeepsContent(t *testing.T) {
	p := &Platform{renderMermaidDiagrams: false}
	content := "```\n┌──┐\n```"
	out, imgs := p.renderMermaid(content)
	if out != content || imgs != nil {
		t.Fatalf("content changed while disabled")
	}
}

func TestCanStartThread(t *testing.T) {
	cases := []struct {
		name string
		rel  *event.RelatesTo
		want bool
	}{
		{"nil relatesTo", nil, true},
		{"empty", &event.RelatesTo{}, true},
		{"reply to", &event.RelatesTo{InReplyTo: &event.InReplyTo{EventID: "$abc"}}, false},
		{"replace edit", &event.RelatesTo{Type: "m.replace"}, false},
		{"thread", &event.RelatesTo{Type: "m.thread"}, false},
		{"reaction annotation", &event.RelatesTo{Type: "m.annotation", Key: "👍"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canStartThread(tc.rel); got != tc.want {
				t.Errorf("canStartThread(%v) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}
