package core

import "fmt"

// ImageDeliveryError reports confirmed progress without claiming a failed
// transport operation was never accepted remotely. Callers must not replay
// confirmed groups when recovering from a partial failure.
type ImageDeliveryError struct {
	Sent, Total int
	Err         error
	message     string
}

func (e *ImageDeliveryError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("image delivery: %d/%d confirmed; remaining delivery incomplete (check the failed group before retrying): %v", e.Sent, e.Total, e.Err)
}

func (e *ImageDeliveryError) Unwrap() error { return e.Err }

func (e *Engine) sendImages(p Platform, replyCtx any, single ImageSender, images []ImageAttachment) error {
	batch, grouped := p.(ImageBatchSender)
	size := 1
	if grouped && len(images) > 1 {
		size = max(1, batch.ImageBatchSize())
	}
	for start := 0; start < len(images); start += size {
		if err := e.ctx.Err(); err != nil {
			return &ImageDeliveryError{Sent: start, Total: len(images), Err: err, message: e.i18n.Tf(MsgImageDeliveryIncomplete, start, len(images), err)}
		}
		if err := e.waitOutgoing(p); err != nil {
			return &ImageDeliveryError{Sent: start, Total: len(images), Err: err, message: e.i18n.Tf(MsgImageDeliveryIncomplete, start, len(images), err)}
		}
		end := min(start+size, len(images))
		var err error
		if size > 1 {
			err = batch.SendImageBatch(e.ctx, replyCtx, images[start:end])
		} else {
			err = single.SendImage(e.ctx, replyCtx, images[start])
		}
		if err != nil {
			return &ImageDeliveryError{Sent: start, Total: len(images), Err: err, message: e.i18n.Tf(MsgImageDeliveryIncomplete, start, len(images), err)}
		}
	}
	return nil
}
