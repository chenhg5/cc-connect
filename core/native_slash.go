package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
)

type nativeSlashState struct {
	agent            Agent
	agentName        string
	authoritative    bool
	workspacePath    string
	commands         []NativeSlashCommand
	fallbackCommands []*CustomCommand
	fallbackSkills   []*Skill
}

type nativeSlashEntry struct {
	agentKey string
	command  NativeSlashCommand
}

type scopedFallbackCommandEntry struct {
	agentKey string
	command  *CustomCommand
}

type nativeSlashRefreshRequest struct {
	agent   Agent
	pending bool
}

func nativeSlashAgentKey(agent Agent) string {
	if agent == nil {
		return ""
	}
	key := agent.Name()
	if switcher, ok := agent.(WorkDirSwitcher); ok {
		if workDir := strings.TrimSpace(switcher.GetWorkDir()); workDir != "" {
			key += "\x00" + canonicalDiscoveryPath(workDir)
		}
	}
	return key
}

// refreshNativeSlashCommands replaces only this agent/workspace's discovery
// result. A successful empty result is authoritative. An error removes the
// stale entry and lets the caller enable filesystem fallback where safe.
func (e *Engine) refreshNativeSlashCommands(ctx context.Context, agent Agent) bool {
	provider, ok := agent.(NativeSlashCommandProvider)
	if !ok {
		return false
	}
	key := nativeSlashAgentKey(agent)
	commands, err := provider.NativeSlashCommands(ctx)
	if nativeSlashAgentKey(agent) != key {
		// The provider result belongs to the work directory captured before
		// discovery; never publish it under a concurrently changed directory.
		slog.Warn("native slash discovery discarded after work directory changed", "agent", agent.Name())
		return false
	}
	if err != nil {
		fallbackCommands, fallbackSkills := discoverScopedFilesystemCapabilities(agent)
		e.storeNativeSlashState(key, nativeSlashState{
			agent:            agent,
			agentName:        agent.Name(),
			fallbackCommands: fallbackCommands,
			fallbackSkills:   fallbackSkills,
		})
		// Provider errors may contain CLI output or environment-derived data.
		// Keep the warning intentionally concise and redacted.
		slog.Warn("native slash discovery failed; using filesystem fallback", "agent", agent.Name())
		return false
	}

	seen := make(map[string]bool)
	clean := make([]NativeSlashCommand, 0, len(commands))
	for _, command := range commands {
		target := strings.TrimSpace(command.Target)
		if target == "" {
			target = strings.TrimSpace(command.Name)
		}
		seenKey := strings.ToLower(target)
		if target == "" || strings.ContainsAny(target, " \t\r\n") || seen[seenKey] {
			continue
		}
		seen[seenKey] = true
		command.Target = target
		if strings.TrimSpace(command.Name) == "" {
			command.Name = target
		}
		clean = append(clean, command)
	}

	e.storeNativeSlashState(key, nativeSlashState{
		agent:         agent,
		agentName:     agent.Name(),
		authoritative: true,
		commands:      append([]NativeSlashCommand(nil), clean...),
	})
	return true
}

func (e *Engine) scheduleNativeSlashRefresh(agent Agent) {
	if agent == nil {
		return
	}
	if _, ok := agent.(NativeSlashCommandProvider); !ok {
		return
	}
	key := nativeSlashAgentKey(agent)
	e.nativeSlashRefreshMu.Lock()
	if request := e.nativeSlashRefreshes[key]; request != nil {
		request.agent = agent
		request.pending = true
		e.nativeSlashRefreshMu.Unlock()
		return
	}
	request := &nativeSlashRefreshRequest{agent: agent}
	e.nativeSlashRefreshes[key] = request
	e.nativeSlashRefreshMu.Unlock()

	go func() {
		for {
			e.nativeSlashRefreshMu.Lock()
			agent := request.agent
			e.nativeSlashRefreshMu.Unlock()
			e.refreshNativeSlashCommands(e.ctx, agent)
			e.refreshReadyPlatformCommands()

			e.nativeSlashRefreshMu.Lock()
			if request.pending && e.ctx.Err() == nil {
				request.pending = false
				e.nativeSlashRefreshMu.Unlock()
				continue
			}
			delete(e.nativeSlashRefreshes, key)
			e.nativeSlashRefreshMu.Unlock()
			return
		}
	}()
}

func nativeSlashChangesCapabilities(target string) bool {
	target = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(target), "_", "-"))
	return target == "plugins" || target == "reload-plugins"
}

func (e *Engine) storeNativeSlashState(key string, state nativeSlashState) {
	e.nativeSlashMu.Lock()
	if existing, exists := e.nativeSlashByAgent[key]; !exists {
		e.nativeSlashOrder = append(e.nativeSlashOrder, key)
	} else if sameAgentInstance(existing.agent, state.agent) {
		state.workspacePath = existing.workspacePath
	}
	e.nativeSlashByAgent[key] = state
	e.nativeSlashMu.Unlock()
}

func (e *Engine) markNativeSlashWorkspaceState(key string, agent Agent, workspace string) {
	e.nativeSlashMu.Lock()
	state, exists := e.nativeSlashByAgent[key]
	if exists && sameAgentInstance(state.agent, agent) {
		state.workspacePath = canonicalDiscoveryPath(workspace)
		e.nativeSlashByAgent[key] = state
	}
	e.nativeSlashMu.Unlock()
}

func discoverScopedFilesystemCapabilities(agent Agent) ([]*CustomCommand, []*Skill) {
	var config *SkillDiscoveryConfig
	skillRegistry := NewSkillRegistry()
	if provider, ok := agent.(SkillDiscoveryConfigProvider); ok {
		value := provider.SkillDiscoveryConfig()
		config = &value
		skillRegistry.SetDiscoveryConfig(value)
	} else if provider, ok := agent.(SkillProvider); ok {
		skillRegistry.SetDirs(provider.SkillDirs())
	}

	commandRegistry := NewCommandRegistry()
	if provider, ok := agent.(CommandProvider); ok {
		commandRegistry.SetAgentDirs(provider.CommandDirs())
		if config != nil {
			commandRegistry.SetAgentFileFilters(config.IgnorePaths, config.DisabledNames)
		}
	}
	listedCommands := commandRegistry.ListAll()
	commands := make([]*CustomCommand, 0, len(listedCommands))
	for _, listed := range listedCommands {
		resolved, ok := commandRegistry.Resolve(listed.Name)
		if !ok {
			continue
		}
		copy := *resolved
		commands = append(commands, &copy)
	}
	skills := skillRegistry.ListAll()
	return commands, append([]*Skill(nil), skills...)
}

func (e *Engine) removeNativeSlashState(key string) {
	if key == "" {
		return
	}
	e.nativeSlashMu.Lock()
	if _, exists := e.nativeSlashByAgent[key]; exists {
		delete(e.nativeSlashByAgent, key)
		for i, existing := range e.nativeSlashOrder {
			if existing == key {
				e.nativeSlashOrder = append(e.nativeSlashOrder[:i], e.nativeSlashOrder[i+1:]...)
				break
			}
		}
	}
	e.nativeSlashMu.Unlock()
}

func sameAgentInstance(left, right Agent) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	return leftValue.Type().Comparable() && leftValue.Interface() == rightValue.Interface()
}

// removeNativeSlashStateForAgent removes key only when it still belongs to the
// reaped agent instance. A replacement workspace may publish the same key
// between pool removal and capability cleanup.
func (e *Engine) removeNativeSlashStateForAgent(key string, expected Agent) bool {
	if key == "" || expected == nil {
		return false
	}
	e.nativeSlashMu.Lock()
	defer e.nativeSlashMu.Unlock()
	state, exists := e.nativeSlashByAgent[key]
	if !exists || !sameAgentInstance(state.agent, expected) {
		return false
	}
	delete(e.nativeSlashByAgent, key)
	for i, existing := range e.nativeSlashOrder {
		if existing == key {
			e.nativeSlashOrder = append(e.nativeSlashOrder[:i], e.nativeSlashOrder[i+1:]...)
			break
		}
	}
	return true
}

func (e *Engine) configureFilesystemCapabilities(agent Agent) {
	// Clear previous agent-scoped discovery first so switching between agents
	// cannot retain stale directories when an optional provider is absent.
	e.skills.SetDiscoveryConfig(SkillDiscoveryConfig{})
	e.commands.SetAgentDirs(nil)
	e.commands.SetAgentFileFilters(nil, nil)
	if provider, ok := agent.(SkillDiscoveryConfigProvider); ok {
		config := provider.SkillDiscoveryConfig()
		e.skills.SetDiscoveryConfig(config)
		if commandProvider, ok := agent.(CommandProvider); ok {
			e.commands.SetAgentDirs(commandProvider.CommandDirs())
			e.commands.SetAgentFileFilters(config.IgnorePaths, config.DisabledNames)
		}
		return
	}
	if provider, ok := agent.(SkillProvider); ok {
		e.skills.SetDirs(provider.SkillDirs())
	}
	if provider, ok := agent.(CommandProvider); ok {
		e.commands.SetAgentDirs(provider.CommandDirs())
		e.commands.SetAgentFileFilters(nil, nil)
	}
}

func (e *Engine) clearFilesystemCapabilities() {
	e.skills.SetDiscoveryConfig(SkillDiscoveryConfig{})
	e.commands.SetAgentDirs(nil)
	e.commands.SetAgentFileFilters(nil, nil)
}

func nativeAliasBase(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	underscore := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			underscore = false
		default:
			if builder.Len() > 0 && !underscore {
				builder.WriteByte('_')
				underscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "_")
}

func nativeAliasHash(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])[:6]
}

func fitNativeAlias(seed, identity string) string {
	alias := nativeAliasBase(seed)
	if alias == "" {
		alias = "native"
	}
	if alias[0] < 'a' || alias[0] > 'z' {
		alias = "native_" + alias
	}
	if len(alias) <= 32 {
		return alias
	}
	suffix := "_" + nativeAliasHash(identity)
	return strings.TrimRight(alias[:32-len(suffix)], "_") + suffix
}

func nativeReservedAliases(customCommands []*CustomCommand) map[string]bool {
	reserved := make(map[string]bool)
	for _, command := range builtinCommands {
		reserved[fitNativeAlias(command.id, command.id)] = true
		for _, name := range command.names {
			reserved[fitNativeAlias(name, name)] = true
		}
	}
	for _, command := range customCommands {
		if command.Source == "agent" {
			continue
		}
		reserved[fitNativeAlias(command.Name, command.Name)] = true
	}
	return reserved
}

func allocateNativeAlias(target, agentName string, reserved, used map[string]bool) string {
	base := fitNativeAlias(target, target)
	available := func(alias string) bool {
		return !reserved[alias] && !used[alias] && matchPrefix(alias, builtinCommands) == ""
	}
	if available(base) {
		return base
	}
	prefix := fitNativeAlias(agentName, agentName)
	if prefix == "" || prefix == "native" {
		prefix = "native"
	}
	candidate := fitNativeAlias(prefix+"_"+base, agentName+"\x00"+target)
	if available(candidate) {
		return candidate
	}
	for attempt := 0; ; attempt++ {
		suffix := "_" + nativeAliasHash(agentName+"\x00"+target+"\x00"+string(rune(attempt)))
		stem := nativeAliasBase(prefix + "_" + base)
		if len(stem) > 32-len(suffix) {
			stem = strings.TrimRight(stem[:32-len(suffix)], "_")
		}
		candidate = stem + suffix
		if available(candidate) {
			return candidate
		}
	}
}

// nativeSlashEntries returns a deterministic union of all discovered
// workspaces. Identical targets for the same agent share one alias; collisions
// with platform-illegal names, cc-connect built-ins, and custom commands get a
// stable, readable agent-prefixed alias.
func (e *Engine) nativeSlashEntries() []nativeSlashEntry {
	customCommands := e.commands.ListAll()
	reserved := nativeReservedAliases(customCommands)

	e.nativeSlashMu.RLock()
	order := append([]string(nil), e.nativeSlashOrder...)
	states := make(map[string]nativeSlashState, len(e.nativeSlashByAgent))
	for key, state := range e.nativeSlashByAgent {
		state.commands = append([]NativeSlashCommand(nil), state.commands...)
		state.fallbackCommands = append([]*CustomCommand(nil), state.fallbackCommands...)
		state.fallbackSkills = append([]*Skill(nil), state.fallbackSkills...)
		states[key] = state
	}
	e.nativeSlashMu.RUnlock()

	used := make(map[string]bool)
	assigned := make(map[string]string)
	var entries []nativeSlashEntry
	for _, key := range order {
		state, ok := states[key]
		if !ok {
			continue
		}
		for _, command := range state.commands {
			identity := state.agentName + "\x00" + command.Target
			alias := assigned[identity]
			if alias == "" {
				alias = allocateNativeAlias(command.Target, state.agentName, reserved, used)
				assigned[identity] = alias
				used[alias] = true
			}
			command.Name = alias
			entries = append(entries, nativeSlashEntry{agentKey: key, command: command})
		}
	}
	return entries
}

func (e *Engine) scopedFallbackCommandEntries() []scopedFallbackCommandEntry {
	e.nativeSlashMu.RLock()
	defer e.nativeSlashMu.RUnlock()
	var entries []scopedFallbackCommandEntry
	for _, key := range e.nativeSlashOrder {
		state, ok := e.nativeSlashByAgent[key]
		if !ok {
			continue
		}
		for _, command := range state.fallbackCommands {
			copy := *command
			entries = append(entries, scopedFallbackCommandEntry{agentKey: key, command: &copy})
		}
	}
	return entries
}

func (e *Engine) resolveScopedFallbackCommand(agent Agent, name string) (*CustomCommand, bool) {
	key := nativeSlashAgentKey(agent)
	normalized := normalizeCommandName(name)
	for _, entry := range e.scopedFallbackCommandEntries() {
		if entry.agentKey == key && normalizeCommandName(entry.command.Name) == normalized {
			return entry.command, true
		}
	}
	return nil, false
}

func (e *Engine) scopedFallbackSkillsForAgent(agent Agent) []*Skill {
	key := nativeSlashAgentKey(agent)
	e.nativeSlashMu.RLock()
	defer e.nativeSlashMu.RUnlock()
	state, ok := e.nativeSlashByAgent[key]
	if !ok {
		return nil
	}
	return append([]*Skill(nil), state.fallbackSkills...)
}

func (e *Engine) scopedFallbackSkills() []*Skill {
	e.nativeSlashMu.RLock()
	defer e.nativeSlashMu.RUnlock()
	var skills []*Skill
	for _, key := range e.nativeSlashOrder {
		state, ok := e.nativeSlashByAgent[key]
		if !ok {
			continue
		}
		skills = append(skills, state.fallbackSkills...)
	}
	return append([]*Skill(nil), skills...)
}

func (e *Engine) resolveScopedFallbackSkill(agent Agent, name string) *Skill {
	normalized := normalizeCommandName(name)
	for _, skill := range e.scopedFallbackSkillsForAgent(agent) {
		if normalizeCommandName(skill.Name) == normalized {
			return skill
		}
	}
	return nil
}

func (e *Engine) resolveNativeSlashCommand(agent Agent, name string) (NativeSlashCommand, bool) {
	key := nativeSlashAgentKey(agent)
	for _, entry := range e.nativeSlashEntries() {
		if entry.agentKey != key {
			continue
		}
		if strings.EqualFold(entry.command.Name, name) || strings.EqualFold(entry.command.Target, name) {
			return entry.command, true
		}
	}
	return NativeSlashCommand{}, false
}

func (e *Engine) resolveNativeSlashCommandOrPolicy(agent Agent, name string) (NativeSlashCommand, bool) {
	if command, ok := e.resolveNativeSlashCommand(agent, name); ok {
		return command, true
	}
	key := nativeSlashAgentKey(agent)
	e.nativeSlashMu.RLock()
	state, exists := e.nativeSlashByAgent[key]
	e.nativeSlashMu.RUnlock()
	if !exists || state.authoritative {
		return NativeSlashCommand{}, false
	}
	provider, ok := agent.(NativeSlashPolicyProvider)
	if !ok {
		return NativeSlashCommand{}, false
	}
	command, ok := provider.NativeSlashPolicy(name)
	if !ok {
		return NativeSlashCommand{}, false
	}
	if command.Target == "" {
		command.Target = command.Name
	}
	if command.Name == "" {
		command.Name = command.Target
	}
	return command, command.Target != ""
}

func nativeSlashCommandDisabled(command NativeSlashCommand, disabled map[string]bool) bool {
	if disabled["*"] {
		return true
	}
	for _, name := range []string{command.Name, command.Target, command.PolicyCommand} {
		if name != "" && disabled[strings.ToLower(name)] {
			return true
		}
	}
	return false
}

// handleNativeSlashCommand applies policy and rewrites a native command for
// direct agent passthrough. matched reports whether name belongs to the actual
// workspace agent; consumed is true when a policy reply handled the message.
func (e *Engine) handleNativeSlashCommand(p Platform, msg *Message, raw, name string, disabled map[string]bool) (matched, consumed bool) {
	agent, _, _, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return true, true
	}
	native, ok := e.resolveNativeSlashCommandOrPolicy(agent, name)
	if !ok {
		return false, false
	}
	if nativeSlashCommandDisabled(native, disabled) {
		slog.Info("audit: command_blocked",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", native.Target, "reason", "disabled")
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/"+native.Name))
		return true, true
	}
	if native.AdminOnly && !e.isAdmin(msg.UserID) {
		slog.Info("audit: command_blocked",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", native.Target, "reason", "unauthorized")
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgAdminRequired, "/"+native.Name))
		return true, true
	}
	if replacement := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(native.ReplacementCommand), "/")); replacement != "" {
		parts := splitCommandArgs(replacement)
		if len(parts) == 0 || matchPrefix(strings.ToLower(parts[0]), builtinCommands) == "" {
			slog.Error("invalid native slash replacement",
				"agent", agent.Name(), "command", native.Target, "replacement", native.ReplacementCommand)
			e.send(p, msg.ReplyCtx, fmt.Sprintf(e.i18n.T(MsgUnknownCommand), "/"+name))
			return true, true
		}
		slog.Info("audit: command_replaced",
			"user_id", msg.UserID, "platform", msg.Platform,
			"project", e.name, "command", native.Target, "replacement", parts[0])
		msg.Content = "/" + replacement
		msg.nativeSlash = false
		msg.nativeSlashRefresh = false
		return true, e.handleCommand(p, msg, msg.Content)
	}
	slog.Info("audit: command_executed",
		"user_id", msg.UserID, "platform", msg.Platform,
		"project", e.name, "command", native.Target, "type", "native")
	// Native slash parsing requires '/' to be the first prompt byte. Preserve
	// the raw suffix exactly but intentionally exclude platform reply quotes;
	// sender metadata is suppressed later via nativeSlash.
	msg.Content = rewriteNativeSlashCommand(raw, native.Target)
	msg.nativeSlash = true
	msg.nativeSlashRefresh = nativeSlashChangesCapabilities(native.Target)
	return true, false
}

func rewriteNativeSlashCommand(raw, target string) string {
	if raw == "" || raw[0] != '/' {
		return raw
	}
	end := len(raw)
	for i, r := range raw {
		if i > 0 && (r == ' ' || r == '\t' || r == '\r' || r == '\n') {
			end = i
			break
		}
	}
	return "/" + target + raw[end:]
}

func slashCommandName(raw string) string {
	if raw == "" || raw[0] != '/' {
		return ""
	}
	end := len(raw)
	for i, r := range raw {
		if i > 0 && (r == ' ' || r == '\t' || r == '\r' || r == '\n') {
			end = i
			break
		}
	}
	return strings.TrimPrefix(raw[:end], "/")
}

func (e *Engine) nativeSkillsForAgent(agent Agent) []*Skill {
	key := nativeSlashAgentKey(agent)
	seen := make(map[string]bool)
	var skills []*Skill
	for _, entry := range e.nativeSlashEntries() {
		if entry.agentKey != key {
			continue
		}
		command := entry.command
		if !command.IsSkill || seen[command.Name] {
			continue
		}
		seen[command.Name] = true
		description := command.Description
		if description == "" {
			description = "Skill"
		}
		skills = append(skills, &Skill{
			Name:        command.Name,
			DisplayName: command.Target,
			Description: description,
			Source:      "native:" + command.Target,
		})
	}
	return skills
}
