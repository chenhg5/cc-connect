package config

import "testing"

func TestAgentChoicesValidateReferencesAndNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choices map[string]string
		valid   bool
	}{
		{"valid", map[string]string{"first": "one", "second": "two"}, true},
		{"unknown", map[string]string{"third": "missing"}, false},
		{"reserved", map[string]string{"list": "one"}, false},
		{"spaces", map[string]string{"two words": "two"}, false},
		{"case collision", map[string]string{"First": "one", "first": "two"}, false},
		{"default redirected", map[string]string{"default": "two"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Projects: []ProjectConfig{{Name: "one", Agent: AgentConfig{Type: "stub"}, AgentChoices: tc.choices}, {Name: "two", Agent: AgentConfig{Type: "stub"}}}}
			err := cfg.validatePermissive()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
