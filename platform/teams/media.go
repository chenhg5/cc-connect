package teams

import (
	"encoding/base64"

	"github.com/chenhg5/cc-connect/core"
)

// imageActivity builds a message activity carrying one image as an inline Bot
// Framework attachment: contentType is the image mime and contentUrl is a
// "data:<mime>;base64,<...>" URI (the documented inline-image mechanism, which
// keeps the connector free of any file-hosting surface). Missing mime/filename
// default to PNG, matching other platform senders.
func imageActivity(rc replyContext, img core.ImageAttachment) outboundActivity {
	mime := img.MimeType
	if mime == "" {
		mime = "image/png"
	}
	name := img.FileName
	if name == "" {
		name = "image.png"
	}
	dataURI := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
	a := newActivity(rc, "message")
	a.Attachments = []attachment{{ContentType: mime, ContentURL: dataURI, Name: name}}
	return a
}
