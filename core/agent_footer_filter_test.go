package core

import (
	"strings"
	"testing"
)

func TestAgentFooterStreamFilter_HidesCRLFFooterSplitAtCarriageReturn(t *testing.T) {
	filter := newAgentFooterStreamFilter(true)
	chunks := []string{
		"answer\r\n",
		"*gpt-5.5 · xhigh · out 864 · in 177.7k cr 175.5k · ctx 69%*\r",
		"\n",
	}

	var rendered strings.Builder
	for _, chunk := range chunks {
		rendered.WriteString(filter.Push(chunk))
	}
	rendered.WriteString(filter.Flush())

	if got, want := rendered.String(), "answer\r\n"; got != want {
		t.Fatalf("filtered CRLF stream = %q, want %q", got, want)
	}
}
