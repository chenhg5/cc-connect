package core

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Only the external process and delivery boundaries are controlled here. All
// input goes through ReceiveMessage and the real session manager.
type busySteerCUJInput struct {
	text   string
	images []ImageAttachment
	files  []FileAttachment
}

type busySteerCUJSession struct {
	stubAgentSession
	events           chan Event
	inputs           chan busySteerCUJInput
	steers           chan string
	steerInputs      chan busySteerCUJInput
	ack              chan SteerResult
	ackErr           chan error
	firstSendRelease <-chan struct{}
	firstSend        sync.Once
}

func (s *busySteerCUJSession) Events() <-chan Event    { return s.events }
func (s *busySteerCUJSession) SupportsBusySteer() bool { return true }
func (s *busySteerCUJSession) Send(text, _ string, images []ImageAttachment, files []FileAttachment) error {
	s.inputs <- cloneBusySteerCUJInput(text, images, files)
	s.firstSend.Do(func() {
		if s.firstSendRelease != nil {
			<-s.firstSendRelease
		}
	})
	return nil
}
func (s *busySteerCUJSession) SendBusySteer(ctx context.Context, text, _ string, images []ImageAttachment, files []FileAttachment) (SteerResult, error) {
	s.steerInputs <- cloneBusySteerCUJInput(text, images, files)
	s.steers <- text
	select {
	case result := <-s.ack:
		return result, nil
	case err := <-s.ackErr:
		return SteerResult{}, err
	case <-ctx.Done():
		return SteerResult{}, ctx.Err()
	}
}

type busySteerCUJPlatform struct {
	stubPlatformEngine
	blockText  string
	blocked    chan struct{}
	release    chan struct{}
	once       sync.Once
	deliveryMu sync.Mutex
	deliveries []busySteerCUJDelivery
}

func (p *busySteerCUJPlatform) Send(ctx context.Context, replyCtx any, text string) error {
	if p.blockText != "" && strings.Contains(text, p.blockText) {
		p.once.Do(func() { close(p.blocked) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	p.deliveries = append(p.deliveries, busySteerCUJDelivery{replyCtx: replyCtx, text: text})
	return p.stubPlatformEngine.Send(ctx, replyCtx, text)
}

type busySteerCUJDelivery struct {
	replyCtx any
	text     string
}

func (p *busySteerCUJPlatform) Reply(ctx context.Context, replyCtx any, text string) error {
	return p.Send(ctx, replyCtx, text)
}

func (p *busySteerCUJPlatform) deliveredTo(replyCtx string) []string {
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	var result []string
	for _, delivery := range p.deliveries {
		if delivery.replyCtx == replyCtx {
			result = append(result, delivery.text)
		}
	}
	return result
}

type busySteerCUJEnv struct {
	t *testing.T
	e *Engine
	p *busySteerCUJPlatform
	s *busySteerCUJSession
}

func newBusySteerCUJEnv(t *testing.T, blockText string) *busySteerCUJEnv {
	t.Helper()
	p := &busySteerCUJPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, blockText: blockText, blocked: make(chan struct{}), release: make(chan struct{})}
	s := &busySteerCUJSession{events: make(chan Event, 16), inputs: make(chan busySteerCUJInput, 16), steers: make(chan string, 16), steerInputs: make(chan busySteerCUJInput, 16), ack: make(chan SteerResult, 1), ackErr: make(chan error, 1)}
	e := NewEngine("test", &resultAgent{session: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.SetBusyMessageMode("steer")
	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
		if err := e.Stop(); err != nil {
			t.Error(err)
		}
	})
	return &busySteerCUJEnv{t: t, e: e, p: p, s: s}
}

func (env *busySteerCUJEnv) send(text string, images ...ImageAttachment) {
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:steer-user", Platform: "test", UserID: "steer-user", UserName: "Steer User", MessageID: "msg-" + text, Content: text, Images: images, ReplyCtx: text})
}

func (env *busySteerCUJEnv) wait(reason string, ready func() bool) {
	env.t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-tick.C:
		case <-deadline.C:
			env.t.Fatalf("waiting for %s; visible replies: %v", reason, env.p.getSent())
		}
	}
}

func (env *busySteerCUJEnv) input(want string) busySteerCUJInput {
	env.t.Helper()
	select {
	case got := <-env.s.inputs:
		if got.text != want {
			env.t.Fatalf("agent got %q; want %q", got.text, want)
		}
		return got
	case <-time.After(3 * time.Second):
		env.t.Fatalf("agent did not receive %q; visible replies: %v", want, env.p.getSent())
		return busySteerCUJInput{}
	}
}

func (env *busySteerCUJEnv) visible(want string) {
	env.t.Helper()
	env.wait(want, func() bool {
		for _, text := range env.p.getSent() {
			if strings.Contains(text, want) {
				return true
			}
		}
		return false
	})
}

func (env *busySteerCUJEnv) finish(answer string) {
	env.s.events <- Event{Type: EventResult, Content: answer, Done: true}
	env.visible(answer)
}

func (env *busySteerCUJEnv) assertNoNativeSteer() {
	env.t.Helper()
	select {
	case text := <-env.s.steers:
		env.t.Fatalf("queued input unexpectedly steered: %q", text)
	default:
	}
}

// Send reaching the process does not prove that its completion has reached the
// event loop. Wait for that readiness transition before testing native steer;
// behavior assertions below still use platform output and process boundaries.
func (env *busySteerCUJEnv) awaitSteerReady() {
	env.t.Helper()
	env.wait("successful initial send consumed", func() bool {
		env.e.interactiveMu.Lock()
		defer env.e.interactiveMu.Unlock()
		state := env.e.interactiveStates["test:steer-user"]
		if state == nil {
			return false
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		return !state.steerAdmissionClosed
	})
}

func TestCUJ_A12_BusySteerPreservesAttachmentFIFO(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("start task")
	env.input("start task")
	env.awaitSteerReady()
	picture := ImageAttachment{FileName: "picture.png", MimeType: "image/png", Data: []byte("picture")}
	env.send("inspect picture", picture)
	got := env.steer("inspect picture")
	assertBusySteerCUJMedia(t, got, []ImageAttachment{picture}, nil)
	// The explanation arrives while the picture ACK is outstanding. It must
	// follow that ACK through native steer before the original task completes.
	env.send("then explain picture")
	env.assertNoNativeSteer()
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.steer("then explain picture")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.visible(env.e.i18n.T(MsgSteerQueued))
	env.finish("picture and explanation answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A13_BusySteerQueuesDuringFinalDelivery(t *testing.T) {
	env := newBusySteerCUJEnv(t, "first blocked answer")
	env.send("start task")
	env.input("start task")
	env.s.events <- Event{Type: EventResult, Content: "first blocked answer", Done: true}
	select {
	case <-env.p.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("first final delivery did not block")
	}
	env.send("next task")
	env.send("task after next")
	env.assertNoNativeSteer()
	close(env.p.release)
	env.visible("first blocked answer")
	env.input("next task")
	env.steer("task after next")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.visible(env.e.i18n.T(MsgSteerQueued))
	env.finish("next and last answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A14_BusySteerStartedRetainsBufferedOutput(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("start task")
	env.input("start task")
	env.awaitSteerReady()
	accepted := make(chan struct{})
	go func() { env.send("supplement becomes new run"); close(accepted) }()
	select {
	case text := <-env.s.steers:
		if text != "supplement becomes new run" {
			t.Fatalf("unexpected steer %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supplement did not reach native steer")
	}
	// The old terminal arrives before its in-flight prompt ACK. New-run
	// output is already buffered when the ACK releases the handoff barrier.
	env.s.events <- Event{Type: EventResult, Content: "old run answer", Done: true}
	env.wait("old terminal read", func() bool { return len(env.s.events) == 0 })
	env.s.events <- Event{Type: EventText, Content: "new buffered answer"}
	env.s.events <- Event{Type: EventResult, Content: "new buffered answer", Done: true}
	env.s.ack <- SteerResult{Disposition: SteerStarted}
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("steer ACK did not resolve")
	}
	env.visible(env.e.i18n.T(MsgSteerStarted))
	env.visible("old run answer")
	env.visible("new buffered answer")
	// A later input may legitimately steer while a new run remains open.
	// Wait for that run to finish before asserting an ordinary next task.
	env.wait("buffered new run released", func() bool {
		return !env.e.sessions.GetOrCreateActive("test:steer-user").Busy()
	})
	env.send("later queued task")
	env.input("later queued task")
	env.finish("later answer")
	env.send("/history")
	env.visible("📜 History")
	for _, text := range env.p.getSent() {
		if strings.Contains(text, "📜 History") && strings.Count(text, "supplement becomes new run") != 1 {
			t.Fatalf("supplement should appear exactly once in user-visible history: %s", text)
		}
	}
	// Agent-bound observations supplement user-visible assertions: an
	// acknowledged new run must never be replayed through ordinary Send.
	select {
	case input := <-env.s.inputs:
		t.Fatalf("unexpected replay: %q", input.text)
	default:
	}
	for _, answer := range []string{"old run answer", "new buffered answer", "later answer"} {
		count := 0
		for _, text := range env.p.getSent() {
			if strings.HasPrefix(text, answer) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("answer %q delivered %d times; replies: %v", answer, count, env.p.getSent())
		}
	}
}

func TestCUJ_A15_BusySteerFlushesWhileInitialSendStarting(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	release := make(chan struct{})
	env.s.firstSendRelease = release
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	env.send("slow initial send")
	env.input("slow initial send")
	env.send("follow-up during send")
	env.send("third task")
	env.assertNoNativeSteer()
	close(release)
	env.steer("follow-up during send")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.steer("third task")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.visible(env.e.i18n.T(MsgSteerQueued))
	env.finish("initial and supplements answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A16_BusySteerUnknownPausesUntilStop(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("original task")
	env.input("original task")
	env.awaitSteerReady()
	accepted := make(chan struct{})
	go func() { env.send("uncertain supplement"); close(accepted) }()
	select {
	case <-env.s.steers:
	case <-time.After(3 * time.Second):
		t.Fatal("supplement did not reach native steer")
	}
	env.send("queued behind uncertain input")
	env.s.ackErr <- errors.New("transport closed before acknowledgement")
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("failed ACK did not resolve")
	}
	env.visible(env.e.i18n.T(MsgSteerDeliveryUnknown))
	env.visible(env.e.i18n.T(MsgSteerQueueNotDelivered))
	env.send("must remain paused")
	env.visible(env.e.i18n.T(MsgSteerInputPaused))
	env.finish("original task still completes")
	select {
	case input := <-env.s.inputs:
		t.Fatalf("uncertain or queued input replayed: %q", input.text)
	default:
	}
	env.send("/stop")
	env.visible(env.e.i18n.T(MsgExecutionStopped))
	env.send("fresh task after stop")
	env.input("fresh task after stop")
	env.finish("fresh answer after stop")
	env.assertNoNativeSteer()
}

func TestCUJ_A17_BusySteerStartedContinuesSingleTerminalTurn(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("original coalesced task")
	env.input("original coalesced task")
	env.awaitSteerReady()
	accepted := make(chan struct{})
	go func() { env.send("supplement in coalesced turn"); close(accepted) }()
	select {
	case text := <-env.s.steers:
		if text != "supplement in coalesced turn" {
			t.Fatalf("unexpected steer %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supplement did not reach native steer")
	}
	env.s.ack <- SteerResult{Disposition: SteerStarted, ContinuesTurn: true}
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("coalesced steer ACK did not resolve")
	}
	env.visible(env.e.i18n.T(MsgSteerStarted))
	// The adapter emits only one terminal for the original plus native run.
	// Reserving an already-sent second turn here would wait forever for a
	// terminal that cannot arrive and prevent the user's next task progressing.
	env.finish("combined original and supplement answer")
	env.wait("coalesced turn released", func() bool {
		return !env.e.sessions.GetOrCreateActive("test:steer-user").Busy()
	})
	env.send("next normal task")
	env.input("next normal task")
	env.finish("next normal answer")
	env.send("/history")
	env.visible("📜 History")
	for _, text := range env.p.getSent() {
		if strings.Contains(text, "📜 History") && strings.Count(text, "supplement in coalesced turn") != 1 {
			t.Fatalf("coalesced supplement should appear once in history: %s", text)
		}
	}
	select {
	case input := <-env.s.inputs:
		t.Fatalf("coalesced supplement replayed through Send: %q", input.text)
	default:
	}
	env.assertNoNativeSteer()
}

func cloneBusySteerCUJInput(text string, images []ImageAttachment, files []FileAttachment) busySteerCUJInput {
	result := busySteerCUJInput{text: text, images: cloneCUJImages(images)}
	for _, file := range files {
		file.Data = append([]byte(nil), file.Data...)
		result.files = append(result.files, file)
	}
	return result
}

func (env *busySteerCUJEnv) sendMedia(text string, images []ImageAttachment, files []FileAttachment, accepted func()) {
	env.t.Helper()
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:steer-user", Platform: "test", UserID: "steer-user", UserName: "Steer User", MessageID: "msg-" + text, Content: text, Images: images, Files: files, ReplyCtx: text, OnAccepted: accepted})
}

func (env *busySteerCUJEnv) steer(want string) busySteerCUJInput {
	env.t.Helper()
	select {
	case input := <-env.s.steerInputs:
		if input.text != want {
			env.t.Fatalf("native steer got %q; want %q", input.text, want)
		}
		select {
		case text := <-env.s.steers:
			if text != want {
				env.t.Fatalf("native steer notification %q; want %q", text, want)
			}
		case <-time.After(3 * time.Second):
			env.t.Fatal("native steer notification missing")
		}
		return input
	case <-time.After(3 * time.Second):
		env.t.Fatalf("native steer did not receive %q before original completion; replies: %v", want, env.p.getSent())
		return busySteerCUJInput{}
	}
}

func (env *busySteerCUJEnv) assertNoOrdinaryInput() {
	env.t.Helper()
	select {
	case input := <-env.s.inputs:
		env.t.Fatalf("supplement replayed through ordinary Send: %+v", input)
	default:
	}
}

func assertBusySteerCUJMedia(t *testing.T, got busySteerCUJInput, images []ImageAttachment, files []FileAttachment) {
	t.Helper()
	if len(got.images) != len(images) || len(got.files) != len(files) {
		t.Fatalf("native media lost: got %+v, want images=%+v files=%+v", got, images, files)
	}
	for i, image := range images {
		actual := got.images[i]
		if image.FileName != actual.FileName || image.MimeType != actual.MimeType || !bytes.Equal(image.Data, actual.Data) {
			t.Fatalf("image %d corrupted: got %+v; want %+v", i, actual, image)
		}
	}
	for i, file := range files {
		actual := got.files[i]
		if file.FileName != actual.FileName || file.MimeType != actual.MimeType || !bytes.Equal(file.Data, actual.Data) {
			t.Fatalf("file %d corrupted: got %+v; want %+v", i, actual, file)
		}
	}
}

// Once the initial attachment reaches the process, all later attachments and
// instructions must join that still-open task, whatever their arrival order.
func TestCUJ_A18_BusySteerFileImageAndTextJoinOpenTask(t *testing.T) {
	for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		t.Run(strings.Join([]string{"fit"[order[0] : order[0]+1], "fit"[order[1] : order[1]+1], "fit"[order[2] : order[2]+1]}, ""), func(t *testing.T) {
			env := newBusySteerCUJEnv(t, "")
			first := FileAttachment{FileName: "A.pdf", MimeType: "application/pdf", Data: []byte("file A")}
			env.sendMedia("inspect A", nil, []FileAttachment{first}, nil)
			assertBusySteerCUJMedia(t, env.input("inspect A"), nil, []FileAttachment{first})
			env.awaitSteerReady()
			additions := []busySteerCUJInput{
				{text: "also inspect B", files: []FileAttachment{{FileName: "B.pdf", MimeType: "application/pdf", Data: []byte("file B")}}},
				{text: "compare image", images: []ImageAttachment{{FileName: "diagram.png", MimeType: "image/png", Data: []byte("image")}}},
				{text: "explain all attachments"},
			}
			for _, index := range order {
				input := additions[index]
				env.sendMedia(input.text, input.images, input.files, nil)
				got := env.steer(input.text)
				assertBusySteerCUJMedia(t, got, input.images, input.files)
				env.s.ack <- SteerResult{Disposition: SteerQueued}
			}
			env.finish("all attachments answer")
			env.assertNoOrdinaryInput()
			env.assertNoNativeSteer()
		})
	}
}

func TestCUJ_A19_BusySteerTextFirstAcceptsFileAndImageOnly(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("analyze incoming attachments")
	env.input("analyze incoming attachments")
	env.awaitSteerReady()
	files := []FileAttachment{{FileName: "report.pdf", MimeType: "application/pdf", Data: []byte("report")}}
	env.sendMedia("", nil, files, nil)
	assertBusySteerCUJMedia(t, env.steer(""), nil, files)
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	images := []ImageAttachment{{FileName: "plot.png", MimeType: "image/png", Data: []byte("plot")}}
	env.sendMedia("", images, nil, nil)
	assertBusySteerCUJMedia(t, env.steer(""), images, nil)
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.finish("text and attachments answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A20_BusySteerPendingACKFlushesFilesAndTextFIFO(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("original open task")
	env.input("original open task")
	env.awaitSteerReady()
	var accepted atomic.Int32
	addAccepted := func() { accepted.Add(1) }
	env.sendMedia("first supplement", nil, nil, addAccepted)
	env.steer("first supplement")
	files := []FileAttachment{{FileName: "later.txt", MimeType: "text/plain", Data: []byte("later")}}
	env.sendMedia("file while ACK pending", nil, files, addAccepted)
	env.sendMedia("text while ACK pending", nil, nil, addAccepted)
	env.visible(env.e.i18n.T(MsgSteerWaiting))
	for _, reply := range env.p.getSent() {
		if strings.Contains(reply, env.e.i18n.T(MsgSteerQueued)) {
			t.Fatalf("claimed confirmation before ACK: %s", reply)
		}
	}
	env.assertNoNativeSteer()
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	assertBusySteerCUJMedia(t, env.steer("file while ACK pending"), nil, files)
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.steer("text while ACK pending")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.wait("three confirmed receipts", func() bool {
		count := 0
		for _, reply := range env.p.getSent() {
			if strings.Contains(reply, env.e.i18n.T(MsgSteerQueued)) {
				count++
			}
		}
		return count == 3
	})
	if got := accepted.Load(); got != 3 {
		t.Fatalf("accepted callbacks ran %d times, want 3", got)
	}
	env.finish("all pending supplements answer")
	env.send("/history")
	env.visible("📜 History")
	for _, reply := range env.p.getSent() {
		if strings.Contains(reply, "📜 History") {
			for _, want := range []string{"first supplement", "file while ACK pending", "text while ACK pending"} {
				if strings.Count(reply, want) != 1 {
					t.Fatalf("history must include %q exactly once: %s", want, reply)
				}
			}
		}
	}
	env.assertNoOrdinaryInput()
}

func TestCUJ_A21_BusySteerStartupFlushesMediaBeforeOriginalTerminal(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	release := make(chan struct{})
	env.s.firstSendRelease = release
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	env.send("starting task")
	env.input("starting task")
	files := []FileAttachment{{FileName: "startup.pdf", MimeType: "application/pdf", Data: []byte("startup file")}}
	images := []ImageAttachment{{FileName: "startup.png", MimeType: "image/png", Data: []byte("startup image")}}
	env.sendMedia("", images, files, nil)
	env.send("use attachments in current task")
	env.visible(env.e.i18n.T(MsgSteerWaiting))
	env.assertNoNativeSteer()
	close(release)
	assertBusySteerCUJMedia(t, env.steer(""), images, files)
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.steer("use attachments in current task")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.finish("startup attachments answer")
	env.assertNoOrdinaryInput()
}

func (env *busySteerCUJEnv) dispatcherIdle() bool {
	env.e.interactiveMu.Lock()
	state := env.e.interactiveStates["test:steer-user"]
	env.e.interactiveMu.Unlock()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return !state.steerDispatcherRunning && state.steerPending == nil
}

func (env *busySteerCUJEnv) sendAt(text string, timestamp int64, accepted func()) {
	env.e.ReceiveMessage(env.p, &Message{SessionKey: "test:steer-user", Platform: "test", UserID: "steer-user", UserName: "Steer User", MessageID: "msg-" + text, Content: text, ReplyCtx: text, UserMessageTimeMs: timestamp, OnAccepted: accepted})
}

func TestCUJ_A22_BusySteerInFlightInputRejectsOlderDelivery(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.sendAt("original timestamp 10", 10, nil)
	env.input("original timestamp 10")
	env.awaitSteerReady()
	env.sendAt("newer timestamp 100", 100, nil)
	env.steer("newer timestamp 100")
	// The newer input is claimed by the dispatcher and no longer in the queue,
	// but its ACK is still blocked. It must still be part of the watermark.
	var staleAccepted atomic.Int32
	env.sendAt("stale timestamp 50", 50, func() { staleAccepted.Add(1) })
	env.sendAt("newest timestamp 110", 110, nil)
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.steer("newest timestamp 110")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.wait("watermark inputs acknowledged", env.dispatcherIdle)
	if staleAccepted.Load() != 0 {
		t.Fatal("stale input was accepted while newer steer awaited ACK")
	}
	env.assertNoNativeSteer()
	env.finish("newer inputs answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A23_BusySteerReceiptDeliveryDoesNotBlockAgent(t *testing.T) {
	waiting := NewI18n(LangEnglish).T(MsgSteerWaiting)
	env := newBusySteerCUJEnv(t, waiting)
	env.send("original task with slow platform")
	env.input("original task with slow platform")
	env.awaitSteerReady()
	bReturned := make(chan struct{})
	go func() { env.send("supplement B"); close(bReturned) }()
	select {
	case <-env.p.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("waiting receipt did not reach blocked platform")
	}
	// Platform delivery is held until after both native ACKs. Neither ACK nor
	// dispatch of the following input may depend on this unrelated network IO.
	env.steer("supplement B")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	cReturned := make(chan struct{})
	go func() { env.send("supplement C"); close(cReturned) }()
	env.steer("supplement C")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.wait("native ACKs processed while waiting receipts blocked", env.dispatcherIdle)
	close(env.p.release)
	for _, returned := range []<-chan struct{}{bReturned, cReturned} {
		select {
		case <-returned:
		case <-time.After(3 * time.Second):
			t.Fatal("message admission did not return")
		}
	}
	confirmed := env.e.i18n.T(MsgSteerQueued)
	for _, text := range []string{"supplement B", "supplement C"} {
		env.wait("ordered receipts for "+text, func() bool {
			delivered := env.p.deliveredTo(text)
			return len(delivered) >= 2
		})
		delivered := env.p.deliveredTo(text)
		if len(delivered) != 2 || delivered[0] != waiting || delivered[1] != confirmed {
			t.Fatalf("%s receipts = %v; want waiting then confirmation", text, delivered)
		}
	}
	env.finish("slow platform task answer")
	env.assertNoOrdinaryInput()
}

func TestCUJ_A24_BusySteerReloadReenablesBufferedDispatch(t *testing.T) {
	env := newBusySteerCUJEnv(t, "")
	env.send("reload original task")
	env.input("reload original task")
	env.awaitSteerReady()
	env.send("first input before reload")
	env.steer("first input before reload")
	env.send("buffered input before reload")
	env.e.SetBusyMessageMode("queue")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.wait("disabled dispatcher stopped after ACK", env.dispatcherIdle)
	env.assertNoNativeSteer()
	// No new arrival or terminal event is allowed to wake this backlog: the
	// configuration transition itself must make the existing head runnable.
	env.e.SetBusyMessageMode("steer")
	env.steer("buffered input before reload")
	env.s.ack <- SteerResult{Disposition: SteerQueued}
	env.wait("reenabled buffered input acknowledged", env.dispatcherIdle)
	env.finish("reloaded task answer")
	env.assertNoOrdinaryInput()
}
