// Package mocks provides mock implementations for testing cc-connect components.
package mocks

import (
	"context"

	"github.com/chenhg5/cc-connect/core"
	"github.com/stretchr/testify/mock"
)

// MockPlatform is a mock implementation of the core.Platform interface.
type MockPlatform struct {
	mock.Mock
}

func (m *MockPlatform) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockPlatform) Start(handler core.MessageHandler) error {
	args := m.Called(handler)
	return args.Error(0)
}

func (m *MockPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	args := m.Called(ctx, replyCtx, content)
	return args.Error(0)
}

func (m *MockPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	args := m.Called(ctx, replyCtx, content)
	return args.Error(0)
}

func (m *MockPlatform) Stop() error {
	args := m.Called()
	return args.Error(0)
}

// MockPlatformWithReplyCtxReconstructor is a mock platform that also implements
// ReplyContextReconstructor for testing cron job scenarios.
type MockPlatformWithReplyCtxReconstructor struct {
	*MockPlatform
}

func (m *MockPlatformWithReplyCtxReconstructor) ReconstructReplyCtx(sessionKey string) (any, error) {
	args := m.Called(sessionKey)
	return args.Get(0), args.Error(1)
}
