package mocks

import (
	"context"
	"io"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/mock"
)

// MockAgent is a mock implementation of the core.Agent interface.
type MockAgent struct {
	mock.Mock
}

func (m *MockAgent) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	args := m.Called(ctx, sessionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(core.AgentSession), args.Error(1)
}

func (m *MockAgent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	args := m.Called(ctx)
	return args.Get(0).([]core.AgentSessionInfo), args.Error(1)
}

func (m *MockAgent) Stop() error {
	args := m.Called()
	return args.Error(0)
}

// MockAgentSession is a mock implementation of the core.AgentSession interface.
type MockAgentSession struct {
	mock.Mock
}

func (m *MockAgentSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	args := m.Called(prompt, images, files)
	return args.Error(0)
}

func (m *MockAgentSession) RespondPermission(requestID string, result core.PermissionResult) error {
	args := m.Called(requestID, result)
	return args.Error(0)
}

func (m *MockAgentSession) Events() <-chan core.Event {
	args := m.Called()
	return args.Get(0).(<-chan core.Event)
}

func (m *MockAgentSession) CurrentSessionID() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgentSession) Alive() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockAgentSession) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockAgentWithProviders is a mock agent that also implements ProviderSwitcher.
type MockAgentWithProviders struct {
	*MockAgent
}

func (m *MockAgentWithProviders) SetProviders(providers []core.ProviderConfig) {
	m.Called(providers)
}

func (m *MockAgentWithProviders) SetActiveProvider(name string) bool {
	args := m.Called(name)
	return args.Bool(0)
}

func (m *MockAgentWithProviders) GetActiveProvider() *core.ProviderConfig {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*core.ProviderConfig)
}

func (m *MockAgentWithProviders) ListProviders() []core.ProviderConfig {
	args := m.Called()
	return args.Get(0).([]core.ProviderConfig)
}

// MockAgentWithModel is a mock agent that also implements ModelSwitcher.
type MockAgentWithModel struct {
	*MockAgent
}

func (m *MockAgentWithModel) SetModel(model string) {
	m.Called(model)
}

func (m *MockAgentWithModel) GetModel() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgentWithModel) AvailableModels(ctx context.Context) []core.ModelOption {
	args := m.Called(ctx)
	return args.Get(0).([]core.ModelOption)
}

// MockAgentWithMode is a mock agent that also implements ModeSwitcher.
type MockAgentWithMode struct {
	*MockAgent
}

func (m *MockAgentWithMode) SetMode(mode string) {
	m.Called(mode)
}

func (m *MockAgentWithMode) GetMode() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgentWithMode) PermissionModes() []core.PermissionModeInfo {
	args := m.Called()
	return args.Get(0).([]core.PermissionModeInfo)
}

// MockAgentWithToolAuth is a mock agent that also implements ToolAuthorizer.
type MockAgentWithToolAuth struct {
	*MockAgent
}

func (m *MockAgentWithToolAuth) AddAllowedTools(tools ...string) error {
	args := m.Called(tools)
	return args.Error(0)
}

func (m *MockAgentWithToolAuth) GetAllowedTools() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

// MockAgentWithHistory is a mock agent that also implements HistoryProvider.
type MockAgentWithHistory struct {
	*MockAgent
}

func (m *MockAgentWithHistory) GetSessionHistory(ctx context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	args := m.Called(ctx, sessionID, limit)
	return args.Get(0).([]core.HistoryEntry), args.Error(1)
}

// MockAgentWithUsage is a mock agent that also implements UsageReporter.
type MockAgentWithUsage struct {
	*MockAgent
}

func (m *MockAgentWithUsage) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.UsageReport), args.Error(1)
}

// MockAgentWithMemory is a mock agent that also implements MemoryFileProvider.
type MockAgentWithMemory struct {
	*MockAgent
}

func (m *MockAgentWithMemory) ProjectMemoryFile() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgentWithMemory) GlobalMemoryFile() string {
	args := m.Called()
	return args.String(0)
}

// MockAgentWithWorkDir is a mock agent that also implements WorkDirSwitcher.
type MockAgentWithWorkDir struct {
	*MockAgent
}

func (m *MockAgentWithWorkDir) SetWorkDir(dir string) {
	m.Called(dir)
}

func (m *MockAgentWithWorkDir) GetWorkDir() string {
	args := m.Called()
	return args.String(0)
}

// MockAgentWithSkill is a mock agent that also implements SkillProvider.
type MockAgentWithSkill struct {
	*MockAgent
}

func (m *MockAgentWithSkill) SkillDirs() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

// MockAgentWithCommand is a mock agent that also implements CommandProvider.
type MockAgentWithCommand struct {
	*MockAgent
}

func (m *MockAgentWithCommand) CommandDirs() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

// MockAgentWithContextCompressor is a mock agent that also implements ContextCompressor.
type MockAgentWithContextCompressor struct {
	*MockAgent
}

func (m *MockAgentWithContextCompressor) CompressCommand() string {
	args := m.Called()
	return args.String(0)
}

// MockAgentWithReasoning is a mock agent that also implements ReasoningEffortSwitcher.
type MockAgentWithReasoning struct {
	*MockAgent
}

func (m *MockAgentWithReasoning) SetReasoningEffort(effort string) {
	m.Called(effort)
}

func (m *MockAgentWithReasoning) GetReasoningEffort() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockAgentWithReasoning) AvailableReasoningEfforts() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

// MockAgentWithSessionDeleter is a mock agent that also implements SessionDeleter.
type MockAgentWithSessionDeleter struct {
	*MockAgent
}

func (m *MockAgentWithSessionDeleter) DeleteSession(ctx context.Context, sessionID string) error {
	args := m.Called(ctx, sessionID)
	return args.Error(0)
}

// MockAgentWithSystemPrompt is a mock agent that also implements SystemPromptSupporter.
type MockAgentWithSystemPrompt struct {
	*MockAgent
}

func (m *MockAgentWithSystemPrompt) HasSystemPromptSupport() bool {
	args := m.Called()
	return args.Bool(0)
}

// MockAgentWithPlatformPrompt is a mock agent that also implements PlatformPromptInjector.
type MockAgentWithPlatformPrompt struct {
	*MockAgent
}

func (m *MockAgentWithPlatformPrompt) SetPlatformPrompt(prompt string) {
	m.Called(prompt)
}

// MockAgentWithSessionEnv is a mock agent that also implements SessionEnvInjector.
type MockAgentWithSessionEnv struct {
	*MockAgent
}

func (m *MockAgentWithSessionEnv) SetSessionEnv(env []string) {
	m.Called(env)
}

// MockAgentFull implements all optional interfaces for comprehensive testing.
type MockAgentFull struct {
	*MockAgent
	*MockAgentWithProviders
	*MockAgentWithModel
	*MockAgentWithMode
	*MockAgentWithToolAuth
	*MockAgentWithHistory
	*MockAgentWithUsage
	*MockAgentWithMemory
	*MockAgentWithWorkDir
	*MockAgentWithSkill
	*MockAgentWithCommand
	*MockAgentWithContextCompressor
	*MockAgentWithReasoning
	*MockAgentWithSessionDeleter
	*MockAgentWithSystemPrompt
	*MockAgentWithPlatformPrompt
	*MockAgentWithSessionEnv
}

func NewMockAgentFull(name string) *MockAgentFull {
	m := &MockAgentFull{
		MockAgent:                    new(MockAgent),
		MockAgentWithProviders:       new(MockAgentWithProviders),
		MockAgentWithModel:           new(MockAgentWithModel),
		MockAgentWithMode:            new(MockAgentWithMode),
		MockAgentWithToolAuth:        new(MockAgentWithToolAuth),
		MockAgentWithHistory:         new(MockAgentWithHistory),
		MockAgentWithUsage:           new(MockAgentWithUsage),
		MockAgentWithMemory:          new(MockAgentWithMemory),
		MockAgentWithWorkDir:         new(MockAgentWithWorkDir),
		MockAgentWithSkill:           new(MockAgentWithSkill),
		MockAgentWithCommand:         new(MockAgentWithCommand),
		MockAgentWithContextCompressor: new(MockAgentWithContextCompressor),
		MockAgentWithReasoning:       new(MockAgentWithReasoning),
		MockAgentWithSessionDeleter:  new(MockAgentWithSessionDeleter),
		MockAgentWithSystemPrompt:    new(MockAgentWithSystemPrompt),
		MockAgentWithPlatformPrompt:  new(MockAgentWithPlatformPrompt),
		MockAgentWithSessionEnv:      new(MockAgentWithSessionEnv),
	}
	m.MockAgent.On("Name").Return(name)
	return m
}

// EventIterator is a helper for simulating agent events in tests.
type EventIterator struct {
	events []core.Event
	index  int
}

func NewEventIterator(events []core.Event) *EventIterator {
	return &EventIterator{events: events, index: 0}
}

func (e *EventIterator) Next() (core.Event, bool) {
	if e.index >= len(e.events) {
		return core.Event{}, false
	}
	event := e.events[e.index]
	e.index++
	return event, true
}

func (e *EventIterator) EventChannel() <-chan core.Event {
	ch := make(chan core.Event, len(e.events))
	for _, event := range e.events {
		ch <- event
	}
	close(ch)
	return ch
}

// NewMockAgentSessionWithEvents creates a mock session that emits predefined events.
func NewMockAgentSessionWithEvents(sessionID string, events []core.Event) *MockAgentSession {
	m := new(MockAgentSession)
	m.On("CurrentSessionID").Return(sessionID)
	m.On("Alive").Return(true)
	m.On("Events").Return(NewEventIterator(events).EventChannel())
	m.On("Close").Return(nil)
	m.On("Send", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	return m
}

// MockEventReader implements io.Reader for testing streaming scenarios.
type MockEventReader struct {
	events []core.Event
	index  int
}

func NewMockEventReader(events []core.Event) *MockEventReader {
	return &MockEventReader{events: events, index: 0}
}

func (r *MockEventReader) Read(p []byte) (n int, err error) {
	if r.index >= len(r.events) {
		return 0, io.EOF
	}
	event := r.events[r.index]
	r.index++
	data := []byte(event.Content)
	copy(p, data)
	return len(data), nil
}
