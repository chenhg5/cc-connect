package feishu

import (
	"errors"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.AsyncRecoverablePlatform = (*Platform)(nil)

type lifecycleRecorder struct{ ready, unavailable int }

func (r *lifecycleRecorder) OnPlatformReady(core.Platform)              { r.ready++ }
func (r *lifecycleRecorder) OnPlatformUnavailable(core.Platform, error) { r.unavailable++ }

func TestLifecycle_DeduplicatesAndResets(t *testing.T) {
	p := &Platform{}
	r := &lifecycleRecorder{}
	p.SetLifecycleHandler(r)
	p.notifyReady()
	p.notifyReady()
	for i := 0; i < 100; i++ {
		p.notifyUnavailable(errors.New("disconnect"))
	}
	p.notifyReady()
	p.notifyUnavailable(errors.New("disconnect again"))
	if r.ready != 2 || r.unavailable != 2 {
		t.Fatalf("ready=%d unavailable=%d", r.ready, r.unavailable)
	}
	p.lifecycleStopping = true
	p.notifyReady()
	p.notifyUnavailable(errors.New("stopping"))
	if r.ready != 2 || r.unavailable != 2 {
		t.Fatal("notifications sent while stopping")
	}
}

func TestSharedLifecycle_LateJoinAndReconnect(t *testing.T) {
	p1, p2 := &Platform{}, &Platform{}
	r1, r2 := &lifecycleRecorder{}, &lifecycleRecorder{}
	p1.SetLifecycleHandler(r1)
	p2.SetLifecycleHandler(r2)
	g := &sharedWSGroup{platforms: []*Platform{p1}}
	g.setLifecycle(true, nil)
	g.mu.Lock()
	g.platforms = append(g.platforms, p2)
	g.mu.Unlock()
	g.syncLifecycle(p2)
	g.setLifecycle(false, errors.New("disconnect"))
	g.setLifecycle(false, errors.New("retry"))
	g.setLifecycle(true, nil)
	if r1.ready != 2 || r2.ready != 2 || r1.unavailable != 1 || r2.unavailable != 1 {
		t.Fatalf("primary=%+v secondary=%+v", r1, r2)
	}
	g.stopLifecycle(errors.New("stop"))
	g.setLifecycle(true, nil)
	if r1.ready != 2 || r2.ready != 2 || r1.unavailable != 2 || r2.unavailable != 2 {
		t.Fatal("late callback revived stopped group")
	}
}

func TestWebhookLifecycle_ReadyAfterListen(t *testing.T) {
	p := &Platform{port: "0", callbackPath: "/callback"}
	r := &lifecycleRecorder{}
	p.SetLifecycleHandler(r)
	p.sharedGroup = &sharedWSGroup{platforms: []*Platform{p}}
	if err := p.startWebhookMode(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.server.Close(); err != nil {
			t.Errorf("close server: %v", err)
		}
	}()
	if r.ready != 1 {
		t.Fatalf("ready=%d, want 1", r.ready)
	}
}

func TestSharedLifecycle_ConcurrentTransitions(t *testing.T) {
	p := &Platform{}
	r := &lifecycleRecorder{}
	p.SetLifecycleHandler(r)
	g := &sharedWSGroup{platforms: []*Platform{p}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				g.setLifecycle((i+j)%2 == 0, errors.New("event"))
			}
		}(i)
	}
	wg.Wait()
}
