package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	expiredReplyCode = 40034005
	groupReplyWindow = 5 * time.Minute
)

type apiError struct {
	code    int
	message string
	detail  string
}

func (e *apiError) Error() string { return e.detail }

func newAPIError(method, url string, status int, raw []byte, retried bool) error {
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &resp)
	suffix := ""
	if retried {
		suffix = " (after retry)"
	}
	return &apiError{code: resp.Code, message: resp.Message,
		detail: fmt.Sprintf("qqbot: api %s %s returned %d%s: %s", method, url, status, suffix, raw)}
}

var (
	validMemberID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
	mentionTag    = regexp.MustCompile(`(?i)<\s*(?:@|qqbot-at-)`)
)

func (p *Platform) TaskCompletionWorkspace(replyCtx any) (string, bool) {
	rctx, ok := replyCtx.(*replyContext)
	if !ok || rctx == nil || p.completionGroup == "" || rctx.messageType != "group" ||
		rctx.groupOpenID != p.completionGroup || rctx.eventMsgID == "" || !validMemberID.MatchString(rctx.userOpenID) ||
		!filepath.IsAbs(p.completionWorkspace) {
		return "", false
	}
	return p.completionWorkspace, true
}

func (p *Platform) SendTaskCompletion(ctx context.Context, replyCtx any, text string, files []core.FileAttachment) error {
	if _, ok := p.TaskCompletionWorkspace(replyCtx); !ok {
		return core.ErrNotSupported
	}
	rctx := *replyCtx.(*replyContext)
	// Use only the sender from the gateway event for the notification.
	text = mentionTag.ReplaceAllStringFunc(text, func(tag string) string {
		return "＜" + tag[1:]
	})
	chunks := core.SplitMessageCodeFenceAware(text, 1800)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	send := func(sendOne func(*replyContext) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !rctx.eventTime.IsZero() && time.Since(rctx.eventTime) >= groupReplyWindow {
			rctx.eventMsgID = ""
		}
		err := sendOne(&rctx)
		var api *apiError
		if rctx.eventMsgID != "" && errors.As(err, &api) && api.code == expiredReplyCode {
			rctx.eventMsgID = ""
			return sendOne(&rctx)
		}
		return err
	}
	for i, chunk := range chunks {
		if err := send(func(rc *replyContext) error {
			if i == 0 {
				content := fmt.Sprintf(`<qqbot-at-user id="%s" /> %s`, rc.userOpenID, chunk)
				err := p.sendMessageWithFormat(rc, content, true)
				var api *apiError
				if !errors.As(err, &api) || !strings.Contains(strings.ToLower(api.message), "markdown") {
					return err
				}
				slog.Warn("qqbot: completion mention denied, sending plain text", "code", api.code)
			}
			return p.sendMessageWithFormat(rc, chunk, false)
		}); err != nil {
			return err
		}
	}
	var fileErrors []error
	for _, file := range files {
		if err := send(func(rc *replyContext) error {
			return p.SendFile(ctx, rc, file)
		}); err != nil {
			fileErrors = append(fileErrors, fmt.Errorf("upload %s: %w", file.FileName, err))
		}
	}
	if len(fileErrors) != 0 {
		return errors.Join(fileErrors...)
	}
	slog.Info("qqbot: task completion delivered", "active", rctx.eventMsgID == "", "files", len(files))
	return nil
}

var _ core.TaskCompletionSender = (*Platform)(nil)
