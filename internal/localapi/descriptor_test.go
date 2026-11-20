package localapi

import (
	"strings"
	"testing"
)

func TestDescriptorAcceptsOnlyItsLocalPipe(t *testing.T) {
	const valid = `{"schema_version":1,"transport":"named_pipe","address":"\\\\.\\pipe\\cc-connect-0123456789abcdef0123456789abcdef","instance_id":"0123456789abcdef0123456789abcdef","pid":123}`
	if _, err := decodeDescriptor([]byte(valid)); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	for name, input := range map[string]string{
		"unknown schema":     strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
		"network transport":  strings.Replace(valid, `"named_pipe"`, `"tcp"`, 1),
		"remote pipe":        strings.Replace(valid, `\\\\.\\pipe`, `\\\\server\\pipe`, 1),
		"another instance":   strings.Replace(valid, `cc-connect-0123456789abcdef0123456789abcdef`, `cc-connect-1123456789abcdef0123456789abcdef`, 1),
		"invalid instance":   strings.ReplaceAll(valid, `0123456789abcdef0123456789abcdef`, `0123456789abcdef0123456789abcdeF`),
		"missing pid":        strings.Replace(valid, `,"pid":123`, "", 1),
		"negative pid":       strings.Replace(valid, `"pid":123`, `"pid":-1`, 1),
		"oversized pid":      strings.Replace(valid, `"pid":123`, `"pid":4294967296`, 1),
		"trailing JSON":      valid + `{}`,
		"oversized document": valid + strings.Repeat(" ", 4096),
		"invalid UTF-8":      strings.Replace(valid, `"transport"`, "\"note\":\"\xff\",\"transport\"", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDescriptor([]byte(input)); err == nil {
				t.Fatal("untrusted descriptor accepted")
			}
		})
	}
}
