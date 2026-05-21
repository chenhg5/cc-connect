package weixin

// JSON shapes mirror the ilink bot HTTP API (Weixin / personal bridge).

const (
	messageTypeUser = 1
	messageTypeBot  = 2

	messageItemText  = 1
	messageItemImage = 2
	messageItemVoice = 3
	messageItemFile  = 4
	messageItemVideo = 5

	messageStateFinish = 2

	sessionExpiredErrcode = -14

	uploadMediaImage = 1
	uploadMediaVideo = 2
	uploadMediaFile  = 3

	typingStatusStart = 1
	typingStatusStop  = 2
)

type baseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type getUpdatesReq struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      baseInfo `json:"base_info"`
}

type getUpdatesResp struct {
	Ret                  int             `json:"ret"`
	Errcode              int             `json:"errcode"`
	Errmsg               string          `json:"errmsg"`
	Msgs                 []weixinMessage `json:"msgs"`
	GetUpdatesBuf        string          `json:"get_updates_buf"`
	LongpollingTimeoutMs int             `json:"longpolling_timeout_ms"`
}

type textItem struct {
	Text string `json:"text,omitempty"`
}

// cdnMedia mirrors CDNMedia in the ilink JSON API.
type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

type imageItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	ThumbMedia *cdnMedia `json:"thumb_media,omitempty"`
	AESKeyHex  string    `json:"aeskey,omitempty"` // inbound: raw key as hex (16 bytes)
	MidSize    int       `json:"mid_size,omitempty"`
}

type fileItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type videoItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	ThumbMedia *cdnMedia `json:"thumb_media,omitempty"`
	VideoSize  int       `json:"video_size,omitempty"`
}

type refMessage struct {
	MessageItem *messageItem `json:"message_item,omitempty"`
	Title       string       `json:"title,omitempty"`
}

type messageItem struct {
	Type      int         `json:"type,omitempty"`
	TextItem  *textItem   `json:"text_item,omitempty"`
	VoiceItem *voiceItem  `json:"voice_item,omitempty"`
	ImageItem *imageItem  `json:"image_item,omitempty"`
	FileItem  *fileItem   `json:"file_item,omitempty"`
	VideoItem *videoItem  `json:"video_item,omitempty"`
	RefMsg    *refMessage `json:"ref_msg,omitempty"`
}

type voiceItem struct {
	Media      *cdnMedia `json:"media,omitempty"`
	Text       string    `json:"text,omitempty"`
	EncodeType int       `json:"encode_type,omitempty"`
}

type getUploadURLRequest struct {
	Filekey     string   `json:"filekey,omitempty"`
	MediaType   int      `json:"media_type,omitempty"`
	ToUserID    string   `json:"to_user_id,omitempty"`
	Rawsize     int      `json:"rawsize,omitempty"`
	Rawfilemd5  string   `json:"rawfilemd5,omitempty"`
	Filesize    int      `json:"filesize,omitempty"`
	NoNeedThumb bool     `json:"no_need_thumb,omitempty"`
	Aeskey      string   `json:"aeskey,omitempty"`
	BaseInfo    baseInfo `json:"base_info"`
}

type getUploadURLResponse struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}

type weixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMs int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []messageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

type sendMessageReq struct {
	Msg      weixinOutboundMsg `json:"msg"`
	BaseInfo baseInfo          `json:"base_info"`
}

// sendMessageResp is the JSON body returned by ilink/bot/sendmessage on HTTP 200.
type sendMessageResp struct {
	Ret     int    `json:"ret"`
	Errcode int    `json:"errcode"`
	Errmsg  string `json:"errmsg"`
}

type sendTypingReq struct {
	IlinkUserID   string   `json:"ilink_user_id"`
	TypingTicket  string   `json:"typing_ticket"`
	Status        int      `json:"status"`
	BaseInfo      baseInfo `json:"base_info"`
}

type getConfigReq struct {
	UserID       string   `json:"user_id"`
	ContextToken string   `json:"context_token,omitempty"`
	BaseInfo     baseInfo `json:"base_info"`
}

type getConfigResp struct {
	Ret          int    `json:"ret"`
	Errcode      int    `json:"errcode"`
	Errmsg       string `json:"errmsg"`
	TypingTicket string `json:"typing_ticket"`
}

type weixinOutboundMsg struct {
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id"`
	ClientID     string        `json:"client_id"`
	MessageType  int           `json:"message_type"`
	MessageState int           `json:"message_state"`
	ItemList     []messageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}
