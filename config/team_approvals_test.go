package config

import (
	"reflect"
	"testing"
)

func TestTraeApprovalArgs_ReplacesInheritedBypassAndPreservesOtherFlags(t *testing.T) {
	args := []string{"--permission-mode=auto", "-s", "danger-full-access", "-a=never", "--dangerously-bypass-approvals-and-sandbox", "--model", "keep-model", "acp", "serve", "--yolo"}
	want := []string{"--permission-mode", "default", "--model", "keep-model", "acp", "serve"}
	got := traeApprovalArgs(args)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v", got)
	}
	if args[0] != "--permission-mode=auto" {
		t.Fatal("mutated input args")
	}
	if !reflect.DeepEqual(traeApprovalArgs(got), want) {
		t.Fatal("not idempotent")
	}
}
