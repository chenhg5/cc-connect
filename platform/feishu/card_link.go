package feishu

import "encoding/json"

// cardLinkURL accepts send-style strings and raw-card URL objects.
type cardLinkURL string

func (u *cardLinkURL) UnmarshalJSON(raw []byte) error {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		*u = cardLinkURL(value)
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		*u = cardLinkURL(obj.URL)
	}
	return nil
}

func cardTextContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var elem struct {
		Content  string `json:"content"`
		Property struct {
			Content string `json:"content"`
		} `json:"property"`
	}
	if json.Unmarshal(raw, &elem) != nil {
		return ""
	}
	if elem.Property.Content != "" {
		return elem.Property.Content
	}
	return elem.Content
}
