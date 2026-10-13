package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func executionClick(t *testing.T, c *queryCard, verb, arg string) {
	t.Helper()
	h := c.handle.(*feishuPreviewHandle)
	action := fmt.Sprintf("exec:%s:%s:%s:%d", c.id, verb, arg, c.view.Revision)
	if err := c.p.navigateExecution(action, c.rc.chatID, h.messageID, "ou_owner", nil); err != nil {
		t.Fatal(err)
	}
}
func assertExecutionFits(t *testing.T, c *queryCard) {
	t.Helper()
	raw, err := c.renderLocked()
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if n := taggedNodes(decoded); n > 200 {
		t.Fatalf("too many components: %d", n)
	}
	wire, _ := json.Marshal(map[string]any{"receive_id": c.rc.chatID, "msg_type": "interactive", "content": raw})
	if len(wire) > 30000 {
		t.Fatalf("request too large: %d", len(wire))
	}
}
func TestExecutionSummaryOmitsLogsAndDetailsPageWithoutNewMessages(t *testing.T) {
	for _, entity := range []bool{false, true} {
		t.Run(fmt.Sprint(entity), func(t *testing.T) {
			p, f := newSplitCardFixture(t, entity)
			c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group", messageID: "task", forceChatReply: true}}
			cleanupExecutionStream(t, c)
			output := strings.Repeat("汉字\"\\\n", 8000) + "OUTPUT_END"
			command := "printf 'command start'\n" + strings.Repeat("long command;", 3000) + "COMMAND_END"
			if err := c.FinishExecution(context.Background(), []core.ExecutionBlock{{Text: "Inspect source", Done: true}, {Tool: "Bash", Command: command, Output: output, Done: true, DurationMS: 200}}, "", core.CardStatusDone); err != nil {
				t.Fatal(err)
			}
			if len(c.cards) != 1 || len(f.files) != 0 {
				t.Fatal("logs split into extra cards/files")
			}
			summary := f.shown("om_1")
			if strings.Contains(summary, "OUTPUT_END") || strings.Contains(summary, "COMMAND_END") || strings.Contains(summary, "CMD001") || !strings.Contains(summary, "0.2s") {
				t.Fatal("summary embeds full commands/logs or lacks compact duration")
			}
			part := c.cards[0]
			executionClick(t, part, "open", "b1")
			pages, err := part.executionDetailPages()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(pages, "") != part.p.cardI18n().T(core.MsgExecCommandLabel)+"\n"+command+"\n\n"+part.p.cardI18n().T(core.MsgExecOutputLabel)+"\n"+output {
				t.Fatal("detail data lost or reordered")
			}
			if len(pages) < 2 {
				t.Fatal("oversized detail not paginated")
			}
			for n := range pages {
				executionClick(t, part, "dpage", fmt.Sprint(n))
				assertExecutionFits(t, part)
			}
			executionClick(t, part, "back", "")
			if len(f.messages) != 1 || !strings.Contains(f.shown("om_1"), "Inspect source") {
				t.Fatal("navigation created a message or lost summary")
			}
			for _, req := range f.requests {
				if req["reply_in_thread"] != false || !strings.HasSuffix(req["path"].(string), "/task/reply") {
					t.Fatal("execution created a thread")
				}
			}
		})
	}
}
func TestExecutionGrowingCallsStayTogetherAndReaderSnapshotStaysPut(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	blocks := []core.ExecutionBlock{{Text: "Check two cases", Done: true}, {Tool: "Bash", ID: "a", Command: "a", Output: "first", Status: "in_progress"}, {Tool: "Bash", ID: "b", Command: "b", Status: "in_progress"}}
	if err := c.UpdateExecution(context.Background(), blocks); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	executionClick(t, part, "open", "b1")
	first := part.view.Snapshot
	blocks[2].Output = "B finished first"
	blocks[2].Done = true
	blocks[1].Output = strings.Repeat("A grows ", 8000)
	blocks = append(blocks, core.ExecutionBlock{Text: "Next explanation", Done: true}, core.ExecutionBlock{Tool: "Bash", Command: "c"})
	if err := c.UpdateExecution(context.Background(), blocks); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 || part.view.Snapshot != first {
		t.Fatal("running update split the group or displaced reading snapshot")
	}
	executionClick(t, part, "refresh", "")
	if part.view.Snapshot.Output != blocks[1].Output {
		t.Fatal("refresh did not read live result")
	}
	executionClick(t, part, "back", "")
	if !strings.Contains(f.shown("om_1"), "Next explanation") {
		t.Fatal("return did not read latest summary")
	}
}
func TestExecutionSplitsAtExplanationAndOldCommandLinkSurvivesMove(t *testing.T) {
	p, f := newSplitCardFixture(t, false)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	c.blocks = []core.ExecutionBlock{{Text: strings.Repeat("A", 14500), Done: true}, {Tool: "Bash", Command: "a", Done: true}, {Text: strings.Repeat("B", 10000), Done: true}, {Tool: "Bash", Command: "b", Done: true}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 {
		t.Fatal("premature split")
	}
	old := c.cards[0]
	c.blocks[2].Text = strings.Repeat("B", 15500)
	c.blocks[3].Output = "new live output"
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 2 || c.cards[1].blocks[0].Tool != "" || c.cards[1].blocks[1].Command != "b" {
		t.Fatal("split disconnected explanation and command")
	}
	if strings.Contains(f.shown("om_2"), "执行记录") {
		t.Fatal("visible continuation numbering")
	}
	executionClick(t, old, "open", "b3")
	if old.view.Snapshot.Output != "new live output" {
		t.Fatal("old link lost the moved call")
	}
}
func TestExecutionOversizedGroupPagesInsideOneCard(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	c.blocks = []core.ExecutionBlock{{Text: strings.Repeat("Large explanation. ", 3000), Done: true}}
	for n := 0; n < 220; n++ {
		c.blocks = append(c.blocks, core.ExecutionBlock{Tool: "Bash", Command: fmt.Sprint("cmd ", n), Output: "done", Done: true})
	}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 || len(f.files) != 0 {
		t.Fatal("oversized single group was split into command-only cards")
	}
	assertExecutionFits(t, c.cards[0])
	executionClick(t, c.cards[0], "spage", "1")
	assertExecutionFits(t, c.cards[0])
	executionClick(t, c.cards[0], "open", "b0")
	pages, err := c.cards[0].executionDetailPages()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pages, "") != c.blocks[0].Text {
		t.Fatal("full explanation lost")
	}
}
func TestExecutionControlsStayOnOriginalCardAndPreserveDetail(t *testing.T) {
	p, f := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group", sessionKey: "feishu:oc_group:root:task"}}
	cleanupExecutionStream(t, c)
	c.blocks = []core.ExecutionBlock{{Text: "Check source", Done: true}, {Tool: "Bash", Command: "cat log", Output: strings.Repeat("x", 50000), Done: true}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	executionClick(t, part, "open", "b1")
	snapshot := part.view.Snapshot
	prompt := core.NewCard().Title("Original question", "orange").Markdown("Which environment?").Build()
	prompt.DecisionToken = "question-token"
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	if len(c.cards) != 1 || !strings.Contains(f.shown("om_1"), "Which environment?") || part.view.Snapshot != snapshot {
		t.Fatal("prompt moved to another card or destroyed reading state")
	}
	part.decisionUser = "ou_owner"
	action := fmt.Sprintf("exec:%s:back::%d", part.id, part.view.Revision)
	if err := p.navigateExecution(action, "oc_group", "om_1", "ou_owner", map[string]any{"answer": "not submitted yet"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.shown("om_1"), "not submitted yet") {
		t.Fatal("navigation lost draft")
	}
	before := f.shown("om_1")
	if err := c.SetNotice(context.Background(), "background heartbeat"); err != nil {
		t.Fatal(err)
	}
	if before != f.shown("om_1") {
		t.Fatal("background update replaced input form")
	}
	if err := c.SetPrompt(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.shown("om_1"), "question-token") {
		t.Fatal("old decision controls still live")
	}
}
func TestExecutionArchiveRestoresReadOnlyWithLargeOutput(t *testing.T) {
	dir := t.TempDir()
	p, _ := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": dir})
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	c.blocks = []core.ExecutionBlock{{Text: "Original explanation", Done: true}, {Tool: "Bash", Command: "cat log", Output: strings.Repeat("wide output\n", 10000)}}
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	executionClick(t, part, "open", "b1")
	part.created = time.Now().Add(-48 * time.Hour)
	if err := part.persistLocked(false); err != nil {
		t.Fatal(err)
	}
	p.queryCardsMu.Lock()
	p.queryCards = nil
	p.queryCardsMu.Unlock()
	restored, err := p.loadQueryCard(part.id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.prompt != nil || !restored.done || restored.records["b1"].Output != c.blocks[1].Output || restored.view.Key != "b1" {
		t.Fatal("restored history revived controls or lost output")
	}
	executionClick(t, restored, "back", "")
	assertExecutionFits(t, restored)
}

func TestExecutionStopAndFailureProduceOneFinalOrdinaryReply(t *testing.T) {
	for _, status := range []core.CardStatus{core.CardStatusStopped, core.CardStatusError} {
		t.Run(string(status), func(t *testing.T) {
			p, f := newSplitCardFixture(t, false)
			c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group", messageID: "source", forceChatReply: true}}
			cleanupExecutionStream(t, c)
			blocks := []core.ExecutionBlock{{Text: "Checking", Done: true}}
			if err := c.UpdateExecution(context.Background(), blocks); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				if err := c.FinishExecution(context.Background(), blocks, "Stopped or failed; no successful result.", status); err != nil {
					t.Fatal(err)
				}
			}
			if len(f.requests) != 2 {
				t.Fatal("missing or duplicated final reply")
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(f.shown("om_1")), &payload); err != nil {
				t.Fatal(err)
			}
			caption := payload["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)["content"].(string)
			if strings.Contains(caption, "green") {
				t.Fatal("unsuccessful execution displayed as success")
			}
			if f.requests[1]["reply_in_thread"] != false || !strings.Contains(f.shown("om_2"), "no successful result") {
				t.Fatal("final reply missing status or source")
			}
		})
	}
}

func TestExecutionRecordArchiveIsIndependentOfSnapshotSizeLimit(t *testing.T) {
	p, _ := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": t.TempDir()})
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	output := strings.Repeat("x", 33<<20) + "TAIL"
	if err := c.FinishExecution(context.Background(), []core.ExecutionBlock{{Tool: "Bash", Command: "read", Output: output, Done: true}}, "", core.CardStatusDone); err != nil {
		t.Fatal(err)
	}
	id := c.cards[0].id
	p.queryCardsMu.Lock()
	p.queryCards = nil
	p.queryCardsMu.Unlock()
	restored, err := p.loadQueryCard(id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.records["b0"].Output != output {
		t.Fatal("large output lost at card snapshot boundary")
	}
}

func TestExecutionHistoryReopensReadOnlyWithoutRevivingApproval(t *testing.T) {
	p, f := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": t.TempDir()})
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}, blocks: []core.ExecutionBlock{{Text: "Original context", Done: true}, {Tool: "Bash", Command: "cat log", Output: "recorded result"}}}
	cleanupExecutionStream(t, c)
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	prompt := core.NewCard().Markdown("Approval scope").Buttons(core.PrimaryBtn("Allow", "perm:allow")).Build()
	if err := c.SetPrompt(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	if err := p.ShowExecutionArchive(context.Background(), replyContext{chatID: "other-group"}, "om_1"); err == nil {
		t.Fatal("archive escaped source group")
	}
	if err := p.ShowExecutionArchive(context.Background(), replyContext{chatID: "oc_group", messageID: "history-request"}, "om_1"); err != nil {
		t.Fatal(err)
	}
	shown := f.shown("om_2")
	if !strings.Contains(shown, "Original context") || strings.Contains(shown, "perm:allow") || strings.Contains(shown, `"template":"green"`) {
		t.Fatal("archived copy lost context, revived permission or claimed success")
	}
	if f.requests[1]["reply_in_thread"] != false {
		t.Fatal("history view created a thread")
	}
	// Browsing the saved copy must not retire the still-active original request.
	if !strings.Contains(f.shown("om_1"), "perm:allow") {
		t.Fatal("read-only view modified original pending request")
	}
}

func TestExecutionDetailsKeepPageBoundaryWhenTurnFinishes(t *testing.T) {
	p, _ := newSplitCardFixture(t, true)
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}, blocks: []core.ExecutionBlock{{Tool: "Bash", Command: "build", Output: strings.Repeat("output ", 8000)}}}
	cleanupExecutionStream(t, c)
	if err := c.writeLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	executionClick(t, part, "open", "b0")
	executionClick(t, part, "dpage", "1")
	pages, err := part.executionDetailPages()
	if err != nil {
		t.Fatal(err)
	}
	before := pages[1]
	if err := c.FinishExecution(context.Background(), c.blocks, "", core.CardStatusDone); err != nil {
		t.Fatal(err)
	}
	after, err := part.executionDetailPages()
	if err != nil {
		t.Fatal(err)
	}
	if part.view.Page != 1 || before != after[1] {
		t.Fatal("completion displaced the reader's snapshot page")
	}
}

func TestExecutionConcurrentStreamingAndNavigation(t *testing.T) {
	p, _ := newSplitCardFixture(t, true, map[string]any{"cc_data_dir": t.TempDir()})
	c := &queryCardStream{p: p.Platform, native: true, rc: replyContext{chatID: "oc_group"}}
	cleanupExecutionStream(t, c)
	blocks := []core.ExecutionBlock{{Text: "Compile", Done: true}, {Tool: "Bash", Command: "build", Output: "start"}}
	if err := c.UpdateExecution(context.Background(), blocks); err != nil {
		t.Fatal(err)
	}
	part := c.cards[0]
	errorsFound := make(chan error, 2)
	go func() {
		for n := 0; n < 25; n++ {
			b := append([]core.ExecutionBlock(nil), blocks...)
			b[1].Output = fmt.Sprintf("output %d", n)
			if err := c.UpdateExecution(context.Background(), b); err != nil {
				errorsFound <- err
				return
			}
		}
		errorsFound <- nil
	}()
	go func() {
		for n := 0; n < 25; n++ {
			part.mu.Lock()
			rev := part.view.Revision
			part.mu.Unlock()
			err := p.navigateExecution(fmt.Sprintf("exec:%s:open:b1:%d", part.id, rev), "oc_group", "om_1", "ou_owner", nil)
			if err != nil && !errors.Is(err, errQueryCardBusy) {
				errorsFound <- err
				return
			}
		}
		errorsFound <- nil
	}()
	for n := 0; n < 2; n++ {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
	}
	executionClick(t, part, "open", "b1")
	if part.view.Snapshot.Output != "output 24" {
		t.Fatal("concurrent reader overwrote live execution data")
	}
}
