package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestToolPipeline_MarkdownWrappedInputRetainsValues(t *testing.T) {
	for _, tc := range []struct {
		tool, input, want string
	}{
		{"grep", "`" + `{"include":"*.go","path":"platform/feishu","pattern":"ToolInput"}` + "`", "pattern: ToolInput"},
		{"bash", "```command\n" + `{"command":"ls -la","description":"inspect directory"}` + "\n```", "command: ls -la"},
		{"Bash", "```bash\n" + `{"command":"ls -la","description":"inspect directory"}` + "\n```", "command: ls -la"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			element := renderProgressEntryElement(core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: tc.tool, Text: tc.input}, "zh")
			b, err := json.Marshal(element)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tc.want) {
				t.Fatalf("actual card lost parameter value; want %q, got %s", tc.want, b)
			}
		})
	}
}
