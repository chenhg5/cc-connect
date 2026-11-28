package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestLocalAPIClientUsesTheServerEndpoint(t *testing.T) {
	dir := t.TempDir()
	api, err := core.NewAPIServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Stop)
	stopped := make(chan struct{}, 1)
	api.SetShutdownHandler(func() { stopped <- struct{}{} })
	api.Start()
	endpoint := resolveSocketPath(dir)
	if endpoint != api.SocketPath() {
		t.Fatalf("client endpoint = %q, server endpoint = %q", endpoint, api.SocketPath())
	}
	client := newLocalAPIClient(endpoint)
	client.Timeout = 2 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get("http://unix/sessions")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sessions status = %d, want 200", response.StatusCode)
	}
	response, err = apiPost(endpoint, "/shutdown", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want 202", response.StatusCode)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("local shutdown did not notify the process owner")
	}
}
