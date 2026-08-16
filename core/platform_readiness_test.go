package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEngine_PlatformStatuses_ReportWhatHasNotConnected(t *testing.T) {
	ready := &stubPlatformEngine{n: "ready"}
	pending := &stubPlatformEngine{n: "pending"}
	e := NewEngine("test", &stubAgent{}, []Platform{ready, pending}, "", LangEnglish)

	before := e.PlatformStatuses()
	if got, want := statusNames(before), []string{"ready", "pending"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %v, want configuration order %v", got, want)
	}
	for _, s := range before {
		if s.Usable() {
			t.Fatalf("platform %q reported usable before it connected", s.Name)
		}
	}

	e.OnPlatformReady(ready)

	for _, s := range e.PlatformStatuses() {
		if s.Name == "ready" && !s.Usable() {
			t.Fatalf("connected platform reported unusable: %+v", s)
		}
		if s.Name == "pending" && s.Usable() {
			t.Fatalf("platform that never connected reported usable: %+v", s)
		}
	}
}

func TestEngine_PlatformStatuses_SurfaceAConnectionThatDied(t *testing.T) {
	// A platform whose long connection fails after Start still counts as
	// ready to the engine — that is exactly the case where the process looks
	// healthy and delivers nothing.
	broken := &stubUnhealthyPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}, err: errors.New("app_id is invalid")}
	e := NewEngine("test", &stubAgent{}, []Platform{broken}, "", LangEnglish)
	e.OnPlatformReady(broken)

	statuses := e.PlatformStatuses()
	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want one", statuses)
	}
	if !statuses[0].Ready {
		t.Fatalf("status = %+v, want ready", statuses[0])
	}
	if statuses[0].Err == nil || statuses[0].Usable() {
		t.Fatalf("status = %+v, want an unusable platform carrying its connection error", statuses[0])
	}
}

func statusNames(statuses []PlatformStatus) []string {
	names := make([]string, 0, len(statuses))
	for _, s := range statuses {
		names = append(names, s.Name)
	}
	return names
}

type stubUnhealthyPlatform struct {
	stubPlatformEngine
	err error
}

func (p *stubUnhealthyPlatform) ConnectionError() error { return p.err }

func TestRunDoctorChecksWithPlatformResults_UsesTheCallerSuppliedPlatformSection(t *testing.T) {
	// A caller with no live connections — the CLI — must be able to describe
	// the platforms itself. The live path claims "connected", which would be
	// a lie exactly when the user runs doctor because nothing connects.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	supplied := []DoctorCheckResult{{
		Name:   "Platform (feishu)",
		Status: DoctorWarn,
		Detail: "configured, not contacted by doctor",
	}}

	results := RunDoctorChecksWithPlatformResults(ctx, &stubAgent{}, supplied)

	found := false
	for _, r := range results {
		if r.Name != "Platform (feishu)" {
			continue
		}
		found = true
		if r.Status != DoctorWarn || r.Detail != supplied[0].Detail {
			t.Fatalf("platform result = %+v, want the supplied one", r)
		}
	}
	if !found {
		t.Fatalf("supplied platform section missing from results: %+v", results)
	}
	for _, r := range results {
		if r.Detail == "connected" {
			t.Fatalf("doctor fabricated a connected platform: %+v", r)
		}
	}
}

func TestRunDoctorChecks_KeepsReportingLivePlatformsAsConnected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := RunDoctorChecks(ctx, &stubAgent{}, []Platform{&stubPlatformEngine{n: "feishu"}})

	for _, r := range results {
		if r.Name == "Platform (feishu)" && r.Detail == "connected" {
			return
		}
	}
	t.Fatalf("live platform section changed: %+v", results)
}
