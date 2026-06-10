package webex

// person is the subset of GET /v1/people/me we use.
type person struct {
	ID          string   `json:"id"`
	Emails      []string `json:"emails"`
	DisplayName string   `json:"displayName"`
}

// device is the subset of POST /v1/devices response we use.
type device struct {
	URL          string `json:"url"`          // for DELETE on shutdown
	WebSocketURL string `json:"webSocketUrl"` // wss:// endpoint
}

// message is the subset of GET /v1/messages/{id} we use.
type message struct {
	ID              string   `json:"id"`
	RoomID          string   `json:"roomId"`
	RoomType        string   `json:"roomType"` // "direct" | "group"
	Text            string   `json:"text"`
	Markdown        string   `json:"markdown"`
	PersonID        string   `json:"personId"`
	PersonEmail     string   `json:"personEmail"`
	MentionedPeople []string `json:"mentionedPeople"`
	Files           []string `json:"files"`
}

// wsEvent is the message envelope delivered over the WebSocket.
type wsEvent struct {
	Resource string `json:"resource"` // "messages"
	Event    string `json:"event"`    // "created"
	Data     struct {
		ID          string `json:"id"`       // message ID
		RoomID      string `json:"roomId"`
		RoomType    string `json:"roomType"`
		PersonID    string `json:"personId"` // actor
		PersonEmail string `json:"personEmail"`
	} `json:"data"`
}

// downloadedFile is a fetched attachment with metadata.
type downloadedFile struct {
	Data     []byte
	MimeType string
	FileName string
}
