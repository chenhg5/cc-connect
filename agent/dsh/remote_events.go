package dsh

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type remoteSessionPump struct {
	client    *rpcClient
	ctx       context.Context
	sessionID string
	frames    chan muxFrame
	text      strings.Builder
}

func (p *remoteSessionPump) emit(frame muxFrame) bool {
	select {
	case p.frames <- frame:
		return true
	case <-p.ctx.Done():
		return false
	}
}
func (p *remoteSessionPump) event(kind string, data any) bool {
	raw, _ := json.Marshal(data)
	encoded, _ := json.Marshal(sessionEvent{Type: kind, Data: raw})
	return p.emit(muxFrame{Type: "session/event", SessionID: p.sessionID, Event: encoded})
}
func (p *remoteSessionPump) chunk(chunk *streamChunk) bool {
	if chunk.Type == "text-delta" {
		p.text.WriteString(chunk.Text)
	}
	return p.event("assistant/chunk", map[string]any{"chunk": chunk})
}
func (p *remoteSessionPump) restore(snapshot remoteFollowFrame) bool {
	if snapshot.AssistantStream == nil || snapshot.AssistantStream.ActiveAttempt == nil {
		return true
	}
	active := snapshot.AssistantStream.ActiveAttempt
	if !p.event("turn/start", map[string]any{"turn": active.Turn}) {
		return false
	}
	for _, record := range active.Stream {
		switch record.Type {
		case "text-chunks", "reasoning-chunks":
			kind := "text-delta"
			if record.Type == "reasoning-chunks" {
				kind = "reasoning-delta"
			}
			for _, text := range record.Texts {
				if !p.chunk(&streamChunk{Type: kind, Text: text}) {
					return false
				}
			}
		case "tool-call-chunks":
			for _, args := range record.Args {
				if !p.chunk(&streamChunk{Type: "tool-call-delta", Index: record.Index, ID: record.ID, Name: record.Name, ArgsDelt: args}) {
					return false
				}
			}
		case "chunk":
			if record.Chunk != nil && !p.chunk(record.Chunk) {
				return false
			}
		}
	}
	return true
}
func (p *remoteSessionPump) follow(raw json.RawMessage) bool {
	var frame remoteFollowFrame
	if json.Unmarshal(raw, &frame) != nil {
		p.emit(muxFrame{Type: "stream/error", Error: &rpcError{Message: "invalid Remote session frame"}})
		return false
	}
	if frame.Type == "assistant-stream" && frame.Frame.Type == "chunk" && frame.Frame.Chunk != nil {
		return p.chunk(frame.Frame.Chunk)
	}
	if frame.Type != "event" {
		return true
	}
	var event sessionEvent
	if json.Unmarshal(frame.Event, &event) != nil {
		p.emit(muxFrame{Type: "stream/error", Error: &rpcError{Message: "invalid Remote session event"}})
		return false
	}
	switch event.Type {
	case "turn/start":
		p.text.Reset()
	case "assistant/message":
		full := eventMessageText(event.Data)
		if strings.HasPrefix(full, p.text.String()) {
			if rest := strings.TrimPrefix(full, p.text.String()); rest != "" && !p.chunk(&streamChunk{Type: "text-delta", Text: rest}) {
				return false
			}
		} else if full != "" {
			p.emit(muxFrame{Type: "stream/error", Error: &rpcError{Message: "Remote assistant text disagrees with its durable message"}})
			return false
		}
		p.text.Reset()
	}
	return p.emit(muxFrame{Type: "session/event", SessionID: p.sessionID, Event: frame.Event})
}
func (p *remoteSessionPump) interaction(raw json.RawMessage) bool {
	var event struct {
		Type, Event, EventID, AgentID string
		Request                       struct {
			ToolName, CallID, Reason string
			Questions                []wireQuestion
		}
	}
	if json.Unmarshal(raw, &event) != nil {
		return false
	}
	if event.Type == "cancel" {
		return p.emit(muxFrame{Type: "interaction/cancelled", RPCID: event.EventID, SessionID: p.sessionID})
	}
	if event.Type != "waterfall" {
		return true
	}
	// A foreign peer must not settle an owner's pending approval while the
	// owner reconnects. DSH broadcasts waterfalls to every answering peer.
	if event.AgentID != p.sessionID {
		return true
	}
	if event.Event != "approval/request" && event.Event != "user-questions/request" {
		ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
		defer cancel()
		return p.client.remoteEventResult(ctx, event.EventID, map[string]any{"kind": "next"}) == nil
	}
	frame := muxFrame{SessionID: p.sessionID, RPCID: event.EventID, ApprovalID: event.EventID, ToolName: event.Request.ToolName, CallID: event.Request.CallID, Reason: event.Request.Reason, Questions: event.Request.Questions, Type: "approval/requested"}
	if event.Event == "user-questions/request" {
		frame.Type = "question/requested"
	}
	return p.emit(frame)
}
func (p *remoteSessionPump) message(msg remoteMessage, handler func(json.RawMessage) bool) bool {
	if msg.Type == "item" {
		return handler(msg.Value)
	}
	if msg.Error != nil {
		p.emit(muxFrame{Type: "stream/error", Error: msg.Error})
	}
	return false
}
func (c *rpcClient) pumpRemote(ctx context.Context, sid string, events, follow *remoteStream, snapshot remoteFollowFrame, frames chan muxFrame) {
	defer close(frames)
	defer c.closeRemote()
	pump := &remoteSessionPump{client: c, ctx: ctx, sessionID: sid, frames: frames}
	if !pump.restore(snapshot) {
		return
	}
	followMessages, eventMessages := follow.messages, events.messages
	for followMessages != nil || eventMessages != nil {
		select {
		case message, ok := <-followMessages:
			if !ok {
				followMessages = nil
				continue
			}
			if !pump.message(message, pump.follow) {
				return
			}
		case message, ok := <-eventMessages:
			if !ok {
				eventMessages = nil
				continue
			}
			if !pump.message(message, pump.interaction) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
