package core

import (
	"errors"
	"io"
	"testing"
)

func TestNativeAcceptanceUnknownErrorSupportsErrorsIsAndAs(t *testing.T) {
	err := &NativeAcceptanceUnknownError{Operation: "turn/start", Cause: io.EOF}
	if !IsNativeAcceptanceUnknown(err) || !errors.Is(err, ErrNativeAcceptanceUnknown) {
		t.Fatalf("acceptance unknown was not detectable: %v", err)
	}
	var typed *NativeAcceptanceUnknownError
	if !errors.As(err, &typed) || typed.Operation != "turn/start" {
		t.Fatalf("errors.As() = %#v, want turn/start", typed)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("cause was not preserved: %v", err)
	}
}
