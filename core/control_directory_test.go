package core

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlSocket_UsesServerDirectoryInsteadOfSessionContext(t *testing.T) {
	dir, err := os.MkdirTemp("", "cc-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	api, err := NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Stop)
	e, a := newRuntimeConfigEngine(t)
	e.SetDataDir(filepath.Join(dir, "contexts", "manager", "_control", strings.Repeat("a", 32)))
	api.RegisterEngine("manager", e)
	api.Start()
	e.injectSessionEnv(a, "test:group:member:manager")
	var injected string
	for _, v := range a.sessionEnv {
		if strings.HasPrefix(v, "CC_DATA_DIR=") {
			injected = strings.TrimPrefix(v, "CC_DATA_DIR=")
		}
	}
	socket := filepath.Join(injected, "run", "api.sock")
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Get("http://unix/sessions")
	if err != nil {
		t.Fatalf("injected control endpoint is unusable: %v", err)
	}
	defer response.Body.Close()
	client.CloseIdleConnections()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
}
