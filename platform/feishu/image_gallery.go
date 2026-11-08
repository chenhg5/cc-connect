package feishu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Keep groups to five originals, below the bisect component's six-image limit.
const imageGalleryBatchSize = 5

func (p *Platform) ImageBatchSize() int {
	if !p.enableImageGallery || !p.useInteractiveCard {
		return 1
	}
	return imageGalleryBatchSize
}

func (p *Platform) SendImageBatch(ctx context.Context, rctx any, images []core.ImageAttachment) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: image gallery: invalid reply context type %T", p.tag(), rctx)
	}
	if p.ImageBatchSize() <= 1 || len(images) == 0 || len(images) > imageGalleryBatchSize {
		return fmt.Errorf("%s: image gallery requires enable_image_gallery, cards enabled, and 1-%d images", p.tag(), imageGalleryBatchSize)
	}
	// An explicit topic remains a topic even when ordinary quoted replies are off.
	reply := p.shouldReplyInThread(rc) || p.shouldUseThreadOrReplyAPI(rc)
	if !reply && rc.chatID == "" {
		return fmt.Errorf("%s: image gallery: chatID is empty", p.tag())
	}
	keys := make([]galleryImage, 0, len(images))
	for i, img := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, err := p.uploadImageKey(ctx, img.Data)
		if err != nil {
			return fmt.Errorf("%s: image gallery upload %d/%d: %w", p.tag(), i+1, len(images), err)
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s: image gallery upload %d/%d: empty image_key", p.tag(), i+1, len(images))
		}
		keys = append(keys, galleryImage{Key: key})
	}
	content, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []galleryElement{{Tag: "img_combination", Mode: "bisect", Images: keys}},
		},
	})
	if err != nil {
		return fmt.Errorf("%s: encode image gallery: %w", p.tag(), err)
	}
	// Reuse this ID through network and token-refresh retries. An independent
	// batch/call gets a new ID; uncertain delivery must not be replayed by core.
	id := rand.Text()
	if reply {
		return p.replyMessageWithUUID(ctx, rc, larkim.MsgTypeInteractive, string(content), id)
	}
	return p.createMessageWithUUID(ctx, rc.chatID, larkim.MsgTypeInteractive, string(content), "send image gallery", id)
}

type galleryImage struct {
	Key string `json:"img_key"`
}

type galleryElement struct {
	Tag    string         `json:"tag"`
	Mode   string         `json:"combination_mode"`
	Images []galleryImage `json:"img_list"`
}

var _ core.ImageBatchSender = (*Platform)(nil)
