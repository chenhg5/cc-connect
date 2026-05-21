package core

import "testing"

func TestEngine_GetBridgePublishedCommands_IncludesBuiltinsAndCustoms(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.AddCommand("deploy", "Deploy app", "ship it", "", "", "config")

	commands := e.GetBridgePublishedCommands()
	if len(commands) == 0 {
		t.Fatal("expected published bridge commands")
	}

	foundHelp := false
	foundDeploy := false
	for _, cmd := range commands {
		switch cmd.Name {
		case "help":
			foundHelp = true
			if cmd.Source != bridgeCommandSourceBuiltin {
				t.Fatalf("help source = %q, want %q", cmd.Source, bridgeCommandSourceBuiltin)
			}
		case "deploy":
			foundDeploy = true
			if cmd.Source != bridgeCommandSourceCustom {
				t.Fatalf("deploy source = %q, want %q", cmd.Source, bridgeCommandSourceCustom)
			}
		}
		if cmd.ArgsMode != bridgeCommandArgsModeText {
			t.Fatalf("command %q args_mode = %q, want %q", cmd.Name, cmd.ArgsMode, bridgeCommandArgsModeText)
		}
	}

	if !foundHelp {
		t.Fatal("expected builtin help command in bridge catalog")
	}
	if !foundDeploy {
		t.Fatal("expected custom deploy command in bridge catalog")
	}
}

func TestEngine_GetBridgePublishedCommands_SkipsDisabledAndBuiltinCollisions(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.AddCommand("help", "custom help", "override", "", "", "config")
	e.AddCommand("deploy", "Deploy app", "ship it", "", "", "config")
	e.SetDisabledCommands([]string{"help", "deploy"})

	commands := e.GetBridgePublishedCommands()
	for _, cmd := range commands {
		if cmd.Name == "help" || cmd.Name == "deploy" {
			t.Fatalf("unexpected disabled command %q in bridge catalog", cmd.Name)
		}
	}
}
