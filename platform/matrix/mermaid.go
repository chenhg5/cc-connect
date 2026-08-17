package matrix

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	golli "github.com/eslider/go-ollama"
)

const krokiTimeout = 30 * time.Second

// renderMermaid extracts ```mermaid code blocks, renders each to PNG via
// kroki.io and replaces the block in the text with a short note. Rendered
// images are returned to be attached after the text message. Blocks that fail
// to render are left untouched so no information is lost.
func (p *Platform) renderMermaid(content string) (string, []core.ImageAttachment) {
	if !p.renderMermaidDiagrams || !strings.Contains(content, "```") {
		return content, nil
	}
	blocks := golli.ParseCodeBlock(&content)
	if len(blocks) == 0 {
		return content, nil
	}
	var imgs []core.ImageAttachment
	out := content
	n := 0
	for _, bl := range blocks {
		if bl == nil || strings.TrimSpace(bl.Code) == "" {
			continue
		}
		blkType := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(bl.Type)), "`", "")
		isMermaid := blkType == "mermaid"
		var png []byte
		var err error
		switch {
		case blkType == "mermaid":
			png, err = p.krokiMermaidPNG(bl.Code)
			if err != nil {
				slog.Debug("matrix: mermaid render failed, keeping code block", "error", err)
				continue
			}
		case looksLikeASCIIDiagram(bl.Code, blkType):
			// Free models keep drawing ASCII art despite instructions —
			// rasterize it so diagrams always arrive as images.
			png, err = p.asciiToPNG(bl.Code)
			if err != nil {
				slog.Debug("matrix: ascii render failed, keeping code block", "error", err)
				continue
			}
		default:
			continue
		}
		n++
		note := fmt.Sprintf("*📊 Диаграмма %d — во вложении*", n)
		out = replaceCodeFence(out, blkType, bl.Code, note)
		if isMermaid && !strings.Contains(out, note) {
			// Replacement failed — keep going, block stays visible.
			slog.Debug("matrix: diagram fence not replaced", "type", blkType)
		}
		imgs = append(imgs, core.ImageAttachment{
			MimeType: "image/png",
			Data:     png,
			FileName: fmt.Sprintf("diagram-%d.png", n),
		})
	}
	return out, imgs
}

// krokiMermaidPNG renders mermaid source to PNG using the public kroki.io service.
func (p *Platform) krokiMermaidPNG(mermaidSrc string) ([]byte, error) {
	client := &http.Client{Timeout: krokiTimeout}
	resp, err := client.Post(p.krokiURL+"/mermaid/png", "text/plain", bytes.NewReader([]byte(mermaidSrc)))
	if err != nil {
		return nil, fmt.Errorf("kroki request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kroki status %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("kroki read: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("kroki empty body")
	}
	return buf.Bytes(), nil
}

// boxDrawingRunes are strong markers of ASCII-art diagrams.
const boxDrawingRunes = "─│┌┐└┘├┤┬┴┼═║╔╗╚╝╠╣╦╩╬►◄▲▼→←↑↓"

// asciiBoxBorder matches classic ASCII box borders like "+------+--+".
var asciiBoxBorder = regexp.MustCompile(`\+[-+]{3,}\+`)

// asciiFlowArrows matches ASCII flow arrows like -->, <--, ==>.
var asciiFlowArrows = regexp.MustCompile(`<={1,2}-{0,1}>|-{2,}>|<-{2,}|={2,}>`)

// looksLikeASCIIDiagram reports whether a code block is a plain-text diagram
// rather than executable code. blkType is the fenced language tag; classic
// (+---+ / | / -->) art heuristics only apply to untyped or text blocks.
func looksLikeASCIIDiagram(code, blkType string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	hits := 0
	for _, r := range code {
		if strings.ContainsRune(boxDrawingRunes, r) {
			hits++
			if hits >= 2 {
				return true
			}
		}
	}
	switch blkType {
	case "", "text", "txt", "ascii", "plain":
	default:
		return false
	}
	if asciiBoxBorder.MatchString(code) && strings.Contains(code, "|") {
		return true
	}
	lines := strings.Split(code, "\n")
	pipes := 0
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|") && len(t) > 4 {
			pipes++
			if pipes >= 2 {
				return true
			}
		}
	}
	if strings.Contains(code, "|") && asciiFlowArrows.MatchString(code) {
		return true
	}
	// Arrow chains: >=2 arrow-bearing lines, or one line with >=2 arrows.
	arrowLines, totalArrows := 0, 0
	for _, ln := range lines {
		n := len(asciiFlowArrows.FindAllString(ln, -1))
		if n > 0 {
			arrowLines++
			totalArrows += n
		}
	}
	return arrowLines >= 2 || totalArrows >= 2
}

// asciiToPNG rasterizes an ASCII diagram to a monospace PNG using ImageMagick,
// so text-art never reaches the chat as raw text. Returns nil on any failure.
func (p *Platform) asciiToPNG(code string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "ascii-diagram-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	raw := filepath.Join(dir, "raw.png")
	out := filepath.Join(dir, "out.png")

	// label:<literal> renders multiline text with an auto-sized canvas
	// (the text: coder crops long lines); -trim removes excess padding.
	if err := exec.Command("convert",
		"-font", "DejaVu-Sans-Mono",
		"-pointsize", "14",
		"-density", "144",
		"label:"+code,
		raw,
	).Run(); err != nil {
		return nil, fmt.Errorf("imagemagick label: %w", err)
	}
	if err := exec.Command("convert",
		raw,
		"-trim", "+repage",
		"-bordercolor", "white", "-border", "14x10",
		out,
	).Run(); err != nil {
		return nil, fmt.Errorf("imagemagick trim: %w", err)
	}
	return os.ReadFile(out)
}

// replaceCodeFence swaps a fenced code block for a note, tolerating
// whitespace variations around the closing fence.
func replaceCodeFence(content, blkType, code, note string) string {
	base := "```" + blkType
	for _, candidate := range []string{
		base + code + "```",
		base + code + "\n```",
		base + strings.TrimRight(code, "\n") + "\n```",
		base + strings.TrimRight(code, "\r\n") + "```",
	} {
		if strings.Contains(content, candidate) {
			return strings.Replace(content, candidate, note, 1)
		}
	}
	return content
}
