//go:build darwin

package runtimeidentity

import (
	"reflect"
	"testing"
)

func TestKeychainSaveArgumentsIncludesPasswordValue(t *testing.T) {
	want := []string{"add-generic-password", "-U", "-s", "cc-connect-runtime", "-a", "/state", "-w", "encoded-key"}
	if got := keychainSaveArguments("/state", "encoded-key"); !reflect.DeepEqual(got, want) {
		t.Fatalf("keychainSaveArguments() = %#v, want %#v", got, want)
	}
}
