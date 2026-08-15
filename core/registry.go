package core

import (
	"fmt"
	"strings"
)

// PlatformFactory creates a Platform from config options.
type PlatformFactory func(opts map[string]any) (Platform, error)

// AgentFactory creates an Agent from config options.
type AgentFactory func(opts map[string]any) (Agent, error)

// OptionsValidator performs side-effect-free validation of plugin options.
// It is intentionally separate from a factory: migration and diagnostics must
// be able to prove that a config can be constructed without starting clients,
// touching plugin state, or requiring an external CLI to be installed.
type OptionsValidator func(opts map[string]any) error

var (
	platformFactories  = make(map[string]PlatformFactory)
	agentFactories     = make(map[string]AgentFactory)
	platformValidators = make(map[string]OptionsValidator)
	agentValidators    = make(map[string]OptionsValidator)
)

func RegisterPlatform(name string, factory PlatformFactory) {
	platformFactories[name] = factory
}

func RegisterAgent(name string, factory AgentFactory) {
	agentFactories[name] = factory
}

func RegisterPlatformOptionsValidator(name string, validator OptionsValidator) {
	platformValidators[name] = validator
}

func RegisterAgentOptionsValidator(name string, validator OptionsValidator) {
	agentValidators[name] = validator
}

// RequireStringOptions returns a pure validator for required string options.
// The full declared set is named in the error so users can repair the config
// in one pass even when more than one value is absent.
func RequireStringOptions(plugin string, keys ...string) OptionsValidator {
	return func(opts map[string]any) error {
		for _, key := range keys {
			value, _ := opts[key].(string)
			if value != "" {
				continue
			}
			verb := "is"
			if len(keys) > 1 {
				verb = "are"
			}
			return fmt.Errorf("%s: %s %s required", plugin, joinOptionNames(keys), verb)
		}
		return nil
	}
}

// RequireAnyStringOption returns a pure validator that accepts the first
// non-empty string among aliases such as "cmd" and "command".
func RequireAnyStringOption(plugin, label string, keys ...string) OptionsValidator {
	return func(opts map[string]any) error {
		for _, key := range keys {
			if value, _ := opts[key].(string); strings.TrimSpace(value) != "" {
				return nil
			}
		}
		return fmt.Errorf("%s: %s is required", plugin, label)
	}
}

func joinOptionNames(keys []string) string {
	if len(keys) < 2 {
		return strings.Join(keys, "")
	}
	return strings.Join(keys[:len(keys)-1], ", ") + " and " + keys[len(keys)-1]
}

func CreatePlatform(name string, opts map[string]any) (Platform, error) {
	if err := ValidatePlatformOptions(name, opts); err != nil {
		return nil, err
	}
	f := platformFactories[name]
	return f(opts)
}

func ValidatePlatformOptions(name string, opts map[string]any) error {
	if _, ok := platformFactories[name]; !ok {
		available := make([]string, 0, len(platformFactories))
		for k := range platformFactories {
			available = append(available, k)
		}
		return fmt.Errorf("unknown platform %q, available: %v", name, available)
	}
	if validator := platformValidators[name]; validator != nil {
		return validator(opts)
	}
	return nil
}

func ListRegisteredAgents() []string {
	names := make([]string, 0, len(agentFactories))
	for k := range agentFactories {
		names = append(names, k)
	}
	return names
}

func ListRegisteredPlatforms() []string {
	names := make([]string, 0, len(platformFactories))
	for k := range platformFactories {
		names = append(names, k)
	}
	return names
}

func CreateAgent(name string, opts map[string]any) (Agent, error) {
	if err := ValidateAgentOptions(name, opts); err != nil {
		return nil, err
	}
	f := agentFactories[name]
	return f(opts)
}

func ValidateAgentOptions(name string, opts map[string]any) error {
	if _, ok := agentFactories[name]; !ok {
		available := make([]string, 0, len(agentFactories))
		for k := range agentFactories {
			available = append(available, k)
		}
		return fmt.Errorf("unknown agent %q, available: %v", name, available)
	}
	if validator := agentValidators[name]; validator != nil {
		return validator(opts)
	}
	return nil
}
