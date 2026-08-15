package core

import (
	"context"
	"errors"
	"testing"
)

type stubPlatform struct{ n string }

func (s *stubPlatform) Name() string                                           { return s.n }
func (s *stubPlatform) Start(MessageHandler) error                             { return nil }
func (s *stubPlatform) Reply(_ context.Context, _ any, _ string) error         { return nil }
func (s *stubPlatform) Send(_ context.Context, _ any, _ string) error          { return nil }
func (s *stubPlatform) Stop() error                                            { return nil }

func TestRegisterAndCreatePlatform(t *testing.T) {
	RegisterPlatform("test-plat", func(opts map[string]any) (Platform, error) {
		return &stubPlatform{n: "test-plat"}, nil
	})

	p, err := CreatePlatform("test-plat", nil)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if p.Name() != "test-plat" {
		t.Errorf("Name() = %q, want test-plat", p.Name())
	}
}

func TestCreatePlatform_Unknown(t *testing.T) {
	_, err := CreatePlatform("nonexistent-xyz", nil)
	if err == nil {
		t.Error("expected error for unknown platform")
	}
}

func TestValidatePlatformOptions_DoesNotConstructPlugin(t *testing.T) {
	constructed := 0
	RegisterPlatform("test-validated-plat", func(opts map[string]any) (Platform, error) {
		constructed++
		return &stubPlatform{n: "test-validated-plat"}, nil
	})
	RegisterPlatformOptionsValidator("test-validated-plat", func(opts map[string]any) error {
		return errors.New("invalid test options")
	})

	if err := ValidatePlatformOptions("test-validated-plat", map[string]any{"invalid": true}); err == nil {
		t.Fatal("ValidatePlatformOptions() accepted invalid options")
	}
	if constructed != 0 {
		t.Fatalf("side-effect-free validation constructed plugin %d times", constructed)
	}
	if _, err := CreatePlatform("test-validated-plat", map[string]any{"invalid": true}); err == nil {
		t.Fatal("CreatePlatform() bypassed the registered option validator")
	}
	if constructed != 0 {
		t.Fatalf("CreatePlatform() constructed plugin after validation failure %d times", constructed)
	}
}

func TestCreateAgent_Unknown(t *testing.T) {
	_, err := CreateAgent("nonexistent-xyz", nil)
	if err == nil {
		t.Error("expected error for unknown agent")
	}
}
