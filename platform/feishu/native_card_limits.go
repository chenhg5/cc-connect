package feishu

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// CardKit full updates and inline IM cards both document a 30 KB limit;
// schema 2.0 permits 200 tagged components/elements, including nested text.
// Use decimal KB and measure the serialized card AND the inline fallback
// request, so JSON escaping and message metadata are accounted for as well.
// https://open.feishu.cn/document/cardkit-v1/card/update
// https://open.feishu.cn/document/server-docs/im-v1/message/reply
const nativeCardMaxBytes = 30000
const nativeCardMaxElements = 200

var errNativeCardCapacity = errors.New("native card exceeds Feishu capacity")

func nativeElementCount(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["tag"]; ok {
			n++
		}
		for _, child := range x {
			n += nativeElementCount(child)
		}
	case []any:
		for _, child := range x {
			n += nativeElementCount(child)
		}
	case []map[string]any:
		for _, child := range x {
			n += nativeElementCount(child)
		}
	}
	return n
}

func (c *queryCard) validateNativeCard(payload map[string]any, data []byte) error {
	if len(data) > nativeCardMaxBytes {
		return fmt.Errorf("%w: JSON %d bytes", errNativeCardCapacity, len(data))
	}
	if n := nativeElementCount(payload); n > nativeCardMaxElements {
		return fmt.Errorf("%w: %d elements", errNativeCardCapacity, n)
	}
	var request any = map[string]any{"receive_id": c.rc.chatID, "msg_type": "interactive", "content": string(data)}
	if c.p.shouldUseThreadOrReplyAPI(c.rc) {
		request = c.p.buildReplyMessageReqBody(c.rc, "interactive", string(data))
	}
	wire, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(wire) > nativeCardMaxBytes {
		return fmt.Errorf("%w: inline request %d bytes", errNativeCardCapacity, len(wire))
	}
	return nil
}

// Render the same envelope used by delivery, rather than estimating a fixed
// number of bytes per tool. Check both active and sealed headers because a
// page can become a completed continuation without any change to its body.
func (c *queryCardStream) nativePageFits(blocks []core.ExecutionBlock, page int, controls bool) (bool, error) {
	id := strings.Repeat("a", 69)
	if page < len(c.cards) {
		id = c.cards[page].id
	} else if c.taskKey != "" {
		id = c.taskPartID(page + 1)
	}
	part := &queryCard{p: c.p, rc: c.rc, id: id, native: true, blocks: blocks, status: c.status, done: c.done}
	if controls {
		part.notice, part.prompt = c.notice, c.prompt
	}
	for _, sealed := range []bool{false, true} {
		if sealed {
			part.done, part.status = true, core.CardStatusDone
		}
		elements := part.executionSummaryElements(blocks)
		// Reserve actual navigation controls, not a guessed fixed text quota.
		elements = append(elements, part.executionPagingReserve()...)
		if _, err := part.executionPayload(elements); err != nil {
			if errors.Is(err, errNativeCardCapacity) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}
