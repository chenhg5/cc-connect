package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type galleryTestPlatform struct {
	stubMediaPlatform
	groups     [][]ImageAttachment
	contexts   []any
	size       int
	failAt     int
	attempts   int
	afterGroup func()
}

var errGalleryRejected = errors.New("gallery rejected")

func (p *galleryTestPlatform) ImageBatchSize() int { return p.size }
func (p *galleryTestPlatform) SendImageBatch(_ context.Context, ctx any, images []ImageAttachment) error {
	p.attempts++
	if p.failAt > 0 && p.attempts == p.failAt {
		return errGalleryRejected
	}
	p.groups = append(p.groups, append([]ImageAttachment(nil), images...))
	p.contexts = append(p.contexts, ctx)
	if p.afterGroup != nil {
		p.afterGroup()
	}
	return p.Send(context.Background(), ctx, fmt.Sprintf("gallery:%d", len(images)))
}

func gallerySendPath(t *testing.T, p Platform, workspace bool) (*Engine, func([]ImageAttachment, []FileAttachment) error) {
	t.Helper()
	if workspace {
		e := newTestEngineWithMultiWorkspaceAgent(t, t.TempDir())
		e.platforms = []Platform{p}
		e.interactiveStates["s"] = &interactiveState{platform: p, replyCtx: "topic-context"}
		dir := t.TempDir()
		return e, func(images []ImageAttachment, files []FileAttachment) error {
			return e.SendToSessionWithOptions("s", "", images, files, SendOptions{WorkDir: dir})
		}
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.interactiveStates["s"] = &interactiveState{platform: p, replyCtx: "topic-context"}
	return e, func(images []ImageAttachment, files []FileAttachment) error {
		return e.SendToSessionWithAttachments("s", "", images, files, nil, false)
	}
}

func TestEngineSendImages_PartialFailureStopsBothSendPaths(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, failAt := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("workspace=%t/fail=%d", workspace, failAt), func(t *testing.T) {
				p := &galleryTestPlatform{size: 5, failAt: failAt}
				p.n = "test"
				_, send := gallerySendPath(t, p, workspace)
				err := send(galleryTestImages(11), []FileAttachment{{FileName: "after.txt"}})
				var partial *ImageDeliveryError
				if !errors.As(err, &partial) || partial.Sent != (failAt-1)*5 || partial.Total != 11 || !errors.Is(err, errGalleryRejected) {
					t.Fatalf("wrong confirmed progress: %v", err)
				}
				if p.attempts != failAt || len(p.groups) != failAt-1 || len(p.images) != 0 || len(p.files) != 0 {
					t.Fatalf("failed delivery replayed or continued: attempts=%d groups=%d singles=%d files=%d", p.attempts, len(p.groups), len(p.images), len(p.files))
				}
			})
		}
	}
}

func TestEngineSendImages_CompatibilityAndAttachmentSwitch(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, size := range []int{-1, 0, 1, 5} {
			t.Run(fmt.Sprintf("workspace=%t/size=%d", workspace, size), func(t *testing.T) {
				p := &galleryTestPlatform{size: size}
				p.n = "test"
				e, send := gallerySendPath(t, p, workspace)
				images := galleryTestImages(10)
				files := []FileAttachment{{FileName: "after.txt", Data: []byte("file")}}
				if err := send(images, files); err != nil {
					t.Fatal(err)
				}
				if size <= 1 {
					if !reflect.DeepEqual(p.images, images) || len(p.groups) != 0 {
						t.Fatal("disabled grouping changed individual sends")
					}
				} else if len(p.groups) != 2 || len(p.images) != 0 || !reflect.DeepEqual(p.contexts, []any{"topic-context", "topic-context"}) {
					t.Fatal("grouping or target changed")
				}
				if !reflect.DeepEqual(p.files, files) {
					t.Fatal("following file was not sent")
				}
				e.attachmentSendEnabled = false
				beforeGroups, beforeImages := len(p.groups), len(p.images)
				if err := send(images, nil); !errors.Is(err, ErrAttachmentSendDisabled) {
					t.Fatal(err)
				}
				if len(p.groups) != beforeGroups || len(p.images) != beforeImages {
					t.Fatal("disabled attachments leaked")
				}
			})
		}
		p := &stubMediaPlatform{}
		p.n = "test"
		e, send := gallerySendPath(t, p, workspace)
		images := galleryTestImages(10)
		if err := send(images, nil); err != nil || !reflect.DeepEqual(p.images, images) {
			t.Fatalf("platform without batch support changed: %v", err)
		}
		if err := e.SendToSession("s", "text only"); err != nil || len(p.getSent()) != 1 {
			t.Fatalf("text-only send changed: %v", err)
		}
	}
}

func TestEngineSendImages_CancellationStopsWithoutReplay(t *testing.T) {
	for _, afterFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(afterFirst), func(t *testing.T) {
			p := &galleryTestPlatform{size: 5}
			p.n = "test"
			e, send := gallerySendPath(t, p, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.ctx = ctx
			want := 0
			if afterFirst {
				p.afterGroup = cancel
				want = 5
			} else {
				cancel()
			}
			err := send(galleryTestImages(11), []FileAttachment{{FileName: "after.txt"}})
			var partial *ImageDeliveryError
			if !errors.As(err, &partial) || partial.Sent != want || partial.Total != 11 || !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong cancellation progress: %v", err)
			}
			if p.attempts != want/5 || len(p.files) != 0 {
				t.Fatal("canceled send continued")
			}
		})
	}
}

func TestEngineSendImages_RateLimitCountsMessages(t *testing.T) {
	p := &galleryTestPlatform{size: 5}
	p.n = "test"
	e, send := gallerySendPath(t, p, false)
	e.SetOutgoingRateLimitCfg(OutgoingRateLimitCfg{MaxPerSecond: 0.01, Burst: 2}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.ctx = ctx
	if err := send(galleryTestImages(10), nil); err != nil {
		t.Fatalf("two gallery messages should fit the two-message budget: %v", err)
	}
	err := send(galleryTestImages(1), nil)
	var partial *ImageDeliveryError
	if !errors.As(err, &partial) || partial.Sent != 0 || partial.Total != 1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rate-limit wait should cancel before the third message: %v", err)
	}
	if len(p.groups) != 2 || len(p.images) != 0 {
		t.Fatal("delivery escaped the message budget")
	}
}
func galleryTestImages(n int) []ImageAttachment {
	images := make([]ImageAttachment, n)
	for i := range images {
		images[i] = ImageAttachment{FileName: fmt.Sprintf("%d.png", i), Data: []byte{byte(i)}}
	}
	return images
}
func TestEngineSendImages_GalleryInsteadOfIndividualMessages(t *testing.T) {
	for _, n := range []int{1, 2, 5, 6, 10, 11} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			p := &galleryTestPlatform{size: 5}
			p.n = "test"
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.interactiveStates["s"] = &interactiveState{platform: p, replyCtx: "topic-context"}
			images := galleryTestImages(n)
			if err := e.SendToSessionWithAttachments("s", "", images, nil, nil, false); err != nil {
				t.Fatal(err)
			}
			if n == 1 {
				if len(p.images) != 1 || len(p.groups) != 0 {
					t.Fatal("single image changed")
				}
				return
			}
			if len(p.images) != 0 || len(p.groups) != (n+4)/5 {
				t.Fatalf("single=%d groups=%d", len(p.images), len(p.groups))
			}
			var got []ImageAttachment
			for i, g := range p.groups {
				if len(g) > 5 || p.contexts[i] != "topic-context" {
					t.Fatal("batch size or target changed")
				}
				got = append(got, g...)
			}
			if !reflect.DeepEqual(got, images) {
				t.Fatal("image order or bytes changed")
			}
		})
	}
}
