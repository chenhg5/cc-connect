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

// wsEvent is the Mercury "conversation.activity" envelope delivered over the
// Webex Device WebSocket. The message body in the frame is end-to-end
// encrypted, so we use the activity ID to fetch the decrypted message via REST.
type wsEvent struct {
	Data struct {
		EventType string `json:"eventType"`
		Activity  struct {
			ID    string `json:"id"`
			Verb  string `json:"verb"`
			Actor struct {
				ID           string `json:"id"`
				EmailAddress string `json:"emailAddress"`
			} `json:"actor"`
		} `json:"activity"`
	} `json:"data"`
}

// downloadedFile is a fetched attachment with metadata.
type downloadedFile struct {
	Data     []byte
	MimeType string
	FileName string
}
