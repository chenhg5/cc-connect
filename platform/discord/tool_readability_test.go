package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

func TestToolReadability_MergedDiscordResultKeepsCommand(t *testing.T) {
	var descriptions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Embeds []discordgo.MessageEmbed `json:"embeds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Embeds) != 1 {
			t.Errorf("invalid Discord payload: %#v, %v", payload, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		descriptions = append(descriptions, payload.Embeds[0].Description)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"preview","channel_id":"channel"}`)
	}))
	defer server.Close()
	p := &Platform{session: newTestDiscordSession(t, server), progressStyle: "card"}
	start := core.ProgressCardEntry{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "echo important-command"}
	result := core.ProgressCardEntry{Kind: core.ProgressEntryToolResult, Tool: "Bash", Input: start.Text, Text: "result", Status: "completed"}
	payload := func(row core.ProgressCardEntry, state core.ProgressCardState) string {
		return core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{row}, false, "Codex", core.LangEnglish, state)
	}
	handle, err := p.SendPreviewStart(context.Background(), replyContext{channelID: "channel"}, payload(start, core.ProgressCardStateRunning))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateMessage(context.Background(), handle, payload(result, core.ProgressCardStateCompleted)); err != nil {
		t.Fatal(err)
	}
	if len(descriptions) != 2 {
		t.Fatalf("wanted send and edit, got %#v", descriptions)
	}
	for _, desc := range descriptions {
		if !strings.Contains(desc, start.Text) {
			t.Fatalf("command disappeared after completion: %q", desc)
		}
	}
	if !strings.Contains(descriptions[1], "completed") || !strings.Contains(descriptions[1], "result") {
		t.Fatalf("lost result: %q", descriptions[1])
	}
}
