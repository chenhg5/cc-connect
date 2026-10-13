package core

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type multiDirectoryTestAgent struct {
	teamTestAgent
	roots []string
}

func (a *multiDirectoryTestAgent) Name() string { return "multi-directory-test-agent" }
func (a *multiDirectoryTestAgent) ConfigureProjectDirectories(primary string, additional []string) error {
	dirs, err := NormalizeProjectDirectories(primary, additional)
	if err != nil {
		return err
	}
	a.dir = dirs[0]
	a.roots = dirs[1:]
	return nil
}
func directorySettingsEngines(t *testing.T) (*Engine, *Engine, *stubPlatformEngine) {
	e, dev, p := projectSettingsEngines(t)
	RegisterAgent("multi-directory-test-agent", func(opts map[string]any) (Agent, error) {
		wd, _ := opts["work_dir"].(string)
		return &multiDirectoryTestAgent{teamTestAgent: teamTestAgent{dir: wd}}, nil
	})
	for _, bot := range []*Engine{e, dev} {
		bot.agent = &multiDirectoryTestAgent{teamTestAgent: teamTestAgent{dir: e.baseWorkDir}}
	}
	return e, dev, p
}

func TestProjectDirectories_AtomicValidationRevisionAndIsolation(t *testing.T) {
	e, dev, p := directorySettingsEngines(t)
	before, _ := e.teamProject("test:group-a:owner")
	extra := t.TempDir()
	if err := e.updateProjectSettingsWithDirectories("a", "directories", "", []string{before.Directories[0], extra, extra}, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := e.teamProject("test:group-a:owner")
	if len(after.Directories) != 2 || after.ContextRevision != before.ContextRevision+1 {
		t.Fatalf("bad revision/dedup: %+v", after)
	}
	for _, bot := range []*Engine{e, dev} {
		a, _, _, _, err := bot.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a.(*multiDirectoryTestAgent).roots, []string{extra}) {
			t.Fatal("extra root not propagated")
		}
		other, _, _, _, err := bot.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-b:owner"})
		if err != nil || len(other.(*multiDirectoryTestAgent).roots) != 0 {
			t.Fatal("roots leaked to another project", err)
		}
	}
	for _, dirs := range [][]string{{before.Directories[0], "relative"}, {before.Directories[0], filepath.Join(extra, "missing")}} {
		if err := e.updateProjectSettingsWithDirectories("a", "directories", "", dirs, nil); err == nil {
			t.Fatal("accepted invalid roots")
		}
	}
	if err := e.updateProjectSettingsWithDirectories("a", "directories", "", []string{extra}, &before.ContextRevision); err == nil {
		t.Fatal("stale form accepted")
	}
	current, _ := e.teamProject("test:group-a:owner")
	if !reflect.DeepEqual(current, after) {
		t.Fatal("failed edit mutated stored project")
	}
	// No-op saves do not discard sessions.
	if err := e.updateProjectSettingsWithDirectories("a", "directories", "", after.Directories, nil); err != nil {
		t.Fatal(err)
	}
	current, _ = e.teamProject("test:group-a:owner")
	if current.ContextRevision != after.ContextRevision {
		t.Fatal("no-op reset context")
	}
	teamSend(e, p, "group-a", "/project directories clear")
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
	current, _ = e.teamProject("test:group-a:owner")
	if len(current.Directories) != 1 || current.ContextRevision != after.ContextRevision+1 {
		t.Fatal("failed to clear directories")
	}
	a, _, _, _, err := dev.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
	if err != nil || len(a.(*multiDirectoryTestAgent).roots) != 0 {
		t.Fatal("removed root remains in new runtime", err)
	}
}

func TestProjectDirectories_UnsupportedAndForgedFormDoNotSave(t *testing.T) {
	e, _, p := projectSettingsEngines(t)
	before, _ := e.teamProject("test:group-a:owner")
	teamSend(e, p, "group-a", "/project directories add "+t.TempDir())
	teamReply(t, p, "does not support")
	p.clearSent()
	e.ReceiveMessage(p, &Message{Platform: "test", SessionKey: "test:group-a:owner", UserID: "stranger", Content: fmt.Sprintf("/project _directories a %d", before.ContextRevision), FormValues: map[string][]string{"primary": {t.TempDir()}}, ReplyCtx: "ctx"})
	teamReply(t, p, "admin")
	after, _ := e.teamProject("test:group-a:owner")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unauthorized directory mutation")
	}
}

type directoryFormPlatform struct {
	cujHelpPanelPlatform
	forms []*Card
}

func (p *directoryFormPlatform) ReplyForm(ctx context.Context, m *Message, c *Card) error {
	p.forms = append(p.forms, c)
	return p.ReplyCard(ctx, m.ReplyCtx, c)
}

func TestProjectDirectories_HelpOpensEditableForm(t *testing.T) {
	e, _, _ := projectSettingsEngines(t)
	p := &directoryFormPlatform{cujHelpPanelPlatform: cujHelpPanelPlatform{stubCardPlatform: stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}}
	e.ReceiveMessage(p, &Message{Platform: "test", ChannelKey: "group-a", SessionKey: "test:group-a:owner", UserID: "owner", Content: "/project directory", ReplyCtx: "ctx"})
	if len(p.forms) != 1 || p.forms[0].Form == nil {
		t.Fatalf("directory settings did not open a form: %v", p.getSent())
	}
	form := p.forms[0].Form
	if len(form.Fields) != 2 || form.BackAction != "cmd:/project settings" || !p.forms[0].Reusable {
		t.Fatalf("incomplete directory form: %+v", form)
	}
	if !strings.Contains(form.Command, "_directories") {
		t.Fatal("no directory submission handler")
	}
}
