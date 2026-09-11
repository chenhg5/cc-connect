package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExportSteerCardPreview generates review artifacts from the production card
// builder. It is opt-in, uses synthetic content, and never contacts a platform.
func TestExportSteerCardPreview(t *testing.T) {
	dir := os.Getenv("CC_STEER_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set CC_STEER_PREVIEW_DIR to export the card preview")
	}
	engine := &Engine{i18n: NewI18n(LangChinese)}
	type example struct {
		State    string `json:"state"`
		Scenario string `json:"scenario"`
		Card     *Card  `json:"card"`
	}
	examples := []example{}
	for _, state := range []struct {
		name, scenario string
		status         queuedTaskStatus
	}{
		{"queued", "普通发送，等待当前任务完成", taskQueued},
		{"accepted", "点击补充，当前轮已接受输入", taskAccepted},
		{"unknown", "提交结果不明，停止自动执行该消息", taskUnknown},
		{"cancelled", "取消这条排队消息", taskCancelled},
	} {
		action := &queuedTaskAction{queued: queuedMessage{content: "先不要改代码，只检查登录失败的原因，并给出定位结果。"}, status: state.status}
		examples = append(examples, example{State: state.name, Scenario: state.scenario, Card: engine.queuedTaskCard(action, "synthetic-preview-token")})
	}
	data, err := json.MarshalIndent(examples, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(filepath.Join("..", "docs", "images", "feishu-steer-preview.html"))
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("<!-- CARD_FIXTURE -->")
	if bytes.Count(template, marker) != 1 {
		t.Fatal("preview template must contain exactly one fixture marker")
	}
	html := bytes.Replace(template, marker, data, 1)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"feishu-steer-cards.json": data, "feishu-steer-preview.html": html} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("exported actual queuedTaskCard examples and a standalone HTML preview")
}
