package feishu

import (
	"log/slog"
	"net/http"

	"github.com/chenhg5/cc-connect/core"
)

// downloadPostFiles fetches the top-level files of a rich-text (post) message.
// A file that cannot be fetched (missing key, folder, download error) becomes a
// localized "attachment unavailable" notice so the agent knows an attachment
// existed instead of silently answering from the text alone; successful
// siblings still reach the normal core file-saving pipeline.
func (p *Platform) downloadPostFiles(messageID, raw, text string) ([]core.FileAttachment, []string) {
	var files []core.FileAttachment
	var notices []string
	i18n := core.NewI18n(core.DetectLanguage(text))
	for _, file := range p.parsePostFiles(raw) {
		name := file.FileName
		if name == "" {
			name = "attachment"
		}
		if file.FileKey == "" || file.IsFolder {
			notices = append(notices, i18n.Tf(core.MsgAttachmentUnavailable, name))
			slog.Warn(p.tag()+": invalid post file metadata", "message_id", messageID, "is_folder", file.IsFolder)
			continue
		}
		data, err := p.downloadResource(messageID, file.FileKey, "file")
		if err != nil {
			slog.Error(p.tag()+": download post file failed", "message_id", messageID, "error", err)
			notices = append(notices, i18n.Tf(core.MsgAttachmentUnavailable, name))
			continue
		}
		files = append(files, core.FileAttachment{FileName: name, Data: data, MimeType: http.DetectContentType(data)})
	}
	return files, notices
}
