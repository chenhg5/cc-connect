package core

import "strings"

const maxAgentFooterModelTokenLen = 128

// agentFooterStreamFilter reconstructs EventText lines before deciding whether
// they are private Agent status footers. Transport chunks are not line
// boundaries: filtering each chunk independently can either expose a fragmented
// footer or delete legitimate prose whose continuation arrives later.
//
// Most answer text passes through immediately. Only a line prefix that can still
// become the model-token + middle-dot footer signature is held until it is
// disambiguated, terminated by a newline, or flushed at a semantic event
// boundary.
type agentFooterStreamFilter struct {
	enabled     bool
	pendingLine string
	passthrough bool
}

func newAgentFooterStreamFilter(enabled bool) *agentFooterStreamFilter {
	return &agentFooterStreamFilter{enabled: enabled}
}

func (f *agentFooterStreamFilter) Reset(enabled bool) {
	f.enabled = enabled
	f.pendingLine = ""
	f.passthrough = false
}

func (f *agentFooterStreamFilter) Push(chunk string) string {
	if !f.enabled || chunk == "" {
		return chunk
	}

	var output strings.Builder
	for chunk != "" {
		if f.passthrough {
			newline := strings.IndexByte(chunk, '\n')
			if newline < 0 {
				output.WriteString(chunk)
				break
			}
			output.WriteString(chunk[:newline+1])
			chunk = chunk[newline+1:]
			f.passthrough = false
			continue
		}

		newline := strings.IndexByte(chunk, '\n')
		if newline < 0 {
			f.pendingLine += chunk
			if !couldBeAgentFooterLinePrefix(f.pendingLine) {
				output.WriteString(f.pendingLine)
				f.pendingLine = ""
				f.passthrough = true
			}
			break
		}

		f.pendingLine += chunk[:newline]
		if !agentFooterLineRe.MatchString(f.pendingLine) {
			output.WriteString(f.pendingLine)
			output.WriteByte('\n')
		}
		f.pendingLine = ""
		chunk = chunk[newline+1:]
	}
	return output.String()
}

// Flush resolves an unterminated line at a semantic event boundary. Exact
// footer lines are omitted; incomplete candidates and footer-shaped prose are
// returned verbatim.
func (f *agentFooterStreamFilter) Flush() string {
	if !f.enabled {
		return ""
	}
	line := f.pendingLine
	f.pendingLine = ""
	f.passthrough = false
	if line == "" || agentFooterLineRe.MatchString(line) {
		return ""
	}
	return line
}

func couldBeAgentFooterLinePrefix(line string) bool {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return true
	}
	if line[i] == '*' {
		i++
		if i == len(line) {
			return true
		}
	}
	if !isASCIIAlphaNumeric(line[i]) {
		return false
	}

	modelLen := 0
	for i < len(line) && line[i] != ' ' && line[i] != '\t' {
		modelLen++
		if modelLen > maxAgentFooterModelTokenLen {
			return false
		}
		i++
	}
	if i == len(line) {
		return true
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return true
	}

	remainder := line[i:]
	return strings.HasPrefix(remainder, "·") || strings.HasPrefix("·", remainder)
}

func isASCIIAlphaNumeric(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
