package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Count tagged nodes independently of the production packing implementation,
// including plain_text nested inside panel headers and button labels.
func taggedNodes(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["tag"]; ok {
			n++
		}
		for _, child := range x {
			n += taggedNodes(child)
		}
	case []any:
		for _, child := range x {
			n += taggedNodes(child)
		}
	}
	return n
}

func TestNativeExecutionCountsAllNestedCardComponents(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	for i := 0; i < 60; i++ {
		c.blocks = append(c.blocks, core.ExecutionBlock{Tool: fmt.Sprintf("T%02d", i), Command: "cmd", Output: "ok", Done: true})
	}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id := range f.messages {
		var card any
		if err := json.Unmarshal([]byte(f.shown(id)), &card); err != nil {
			t.Fatal(err)
		}
		if n := taggedNodes(card); n > 200 {
			t.Fatalf("%s has %d components; maximum is 200", id, n)
		}
	}
	if len(f.files) != 0 {
		t.Fatal("component count overflow should produce continuation cards")
	}
}

func TestNativeCardExactRequestAndComponentBoundaries(t *testing.T) {
	p, _ := newSplitCardFixture(t, true)
	c := &queryCard{p: p.Platform, native: true, unpaged: true, done: true, rc: replyContext{chatID: "oc_group"}, blocks: []core.ExecutionBlock{{Text: "X", Done: true}}}
	s, err := c.executionPayload(c.executionSummaryElements(c.blocks))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"receive_id": "oc_group", "msg_type": "interactive", "content": s})
	c.blocks[0].Text = strings.Repeat("X", 1+30000-len(body))
	if _, err := c.executionPayload(c.executionSummaryElements(c.blocks)); err != nil {
		t.Fatalf("exactly 30,000 request bytes rejected: %v", err)
	}
	c.blocks[0].Text += "X"
	if _, err := c.executionPayload(c.executionSummaryElements(c.blocks)); !errors.Is(err, errNativeCardCapacity) {
		t.Fatalf("30,001 request bytes accepted: %v", err)
	}
	c.blocks = make([]core.ExecutionBlock, 199) // Plus the compact status caption.
	for i := range c.blocks {
		c.blocks[i] = core.ExecutionBlock{Text: "x", Done: true}
	}
	if _, err := c.executionPayload(c.executionSummaryElements(c.blocks)); err != nil {
		t.Fatalf("200 elements rejected: %v", err)
	}
	c.blocks = append(c.blocks, core.ExecutionBlock{Text: "x", Done: true})
	if _, err := c.executionPayload(c.executionSummaryElements(c.blocks)); !errors.Is(err, errNativeCardCapacity) {
		t.Fatalf("201 elements accepted: %v", err)
	}
}
