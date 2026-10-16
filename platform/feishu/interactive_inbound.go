package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// This file implements inbound interactive (card) message support.
//
// Background: when a user sends or forwards a card message to the bot,
// Feishu delivers it via im.message.receive_v1 with message_type
// "interactive". The event body does NOT carry the raw card JSON that was
// used at send time; instead it carries a simplified receive format:
//
//	{"title": "...", "elements": [[{"tag":"text",...},{"tag":"at",...}], ...]}
//
// Element tags observed in the wild (see "Receive message content structure"
// in the Feishu open docs): text, a, at, button, img, hr, note,
// select_static, overflow, date_picker.
//
// The entry points below translate that structure into the same
// core.Message shape the "text"/"post" branches produce, so @bot mentions,
// quoted replies, group history, and merge_forward all work for cards:
//
//   - extractInteractiveReceiveParts — pure parser, no network.
//   - extractInteractiveReceiveText — pure text renderer (history + quotes).
//   - parseInteractiveContent — parser + image download (live dispatch).
//   - downloadInteractiveImages — best-effort image fetch, failures skipped.

// interactiveReceiveElement is one cell of the simplified receive-format
// card body. Only the fields the inbound path needs are modelled; unknown
// fields are ignored so future tags degrade gracefully instead of failing.
type interactiveReceiveElement struct {
	Tag         string          `json:"tag"`
	Text        string          `json:"text"`
	Href        string          `json:"href"`
	UserID      string          `json:"user_id"`
	UserName    string          `json:"user_name"`
	ImageKey    string          `json:"image_key"`
	Placeholder string          `json:"placeholder"`
	InitialDate string          `json:"initial_date"`
	Options     json.RawMessage `json:"options"`
	Elements    json.RawMessage `json:"elements"` // note: nested elements
}

// flattenInteractiveRows normalises the two element layouts Feishu emits:
// rows of cells ([[elem...], ...]) and a flat cell list ([elem...]).
func flattenInteractiveRows(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err == nil {
		var flat []json.RawMessage
		for _, row := range rows {
			flat = append(flat, row...)
		}
		return flat
	}
	var flat []json.RawMessage
	if err := json.Unmarshal(raw, &flat); err == nil {
		return flat
	}
	return nil
}

// interactiveOptionLabels renders select_static/overflow options. The
// receive format documents plain strings, but object options are tolerated.
func interactiveOptionLabels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err == nil {
		return strs
	}
	var objs []struct {
		Text  string `json:"text"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		var out []string
		for _, o := range objs {
			if o.Text != "" {
				out = append(out, o.Text)
			} else if o.Value != "" {
				out = append(out, o.Value)
			}
		}
		return out
	}
	return nil
}

// appendInteractiveReceiveElement renders one receive-format element. At
// mentions are emitted as their "@_user_N" token so the caller can resolve
// them through the event mentions list (stripMentions/replaceMentions),
// exactly like the text-message path does.
func appendInteractiveReceiveElement(raw json.RawMessage, parts *[]string, imageKeys *[]string) {
	var elem interactiveReceiveElement
	if err := json.Unmarshal(raw, &elem); err != nil {
		return
	}
	switch elem.Tag {
	case "text":
		if elem.Text != "" {
			*parts = append(*parts, elem.Text)
		}
	case "a":
		switch {
		case elem.Text != "" && elem.Href != "":
			*parts = append(*parts, fmt.Sprintf("[%s](%s)", elem.Text, elem.Href))
		case elem.Text != "":
			*parts = append(*parts, elem.Text)
		case elem.Href != "":
			*parts = append(*parts, elem.Href)
		}
	case "at":
		switch {
		case elem.UserID == "all":
			*parts = append(*parts, "@all")
		case elem.UserID != "":
			*parts = append(*parts, elem.UserID)
		case elem.UserName != "":
			*parts = append(*parts, "@"+elem.UserName)
		}
	case "button":
		if elem.Text != "" {
			*parts = append(*parts, elem.Text)
		}
	case "img":
		if elem.ImageKey != "" {
			*imageKeys = append(*imageKeys, elem.ImageKey)
		}
	case "hr":
		*parts = append(*parts, "---")
	case "note":
		for _, nested := range flattenInteractiveRows(elem.Elements) {
			appendInteractiveReceiveElement(nested, parts, imageKeys)
		}
	case "select_static", "overflow":
		if elem.Placeholder != "" {
			*parts = append(*parts, elem.Placeholder)
		}
		if labels := interactiveOptionLabels(elem.Options); len(labels) > 0 {
			*parts = append(*parts, "[options: "+strings.Join(labels, ", ")+"]")
		}
	case "date_picker":
		switch {
		case elem.Placeholder != "":
			*parts = append(*parts, elem.Placeholder)
		case elem.InitialDate != "":
			*parts = append(*parts, elem.InitialDate)
		default:
			*parts = append(*parts, "[date picker]")
		}
	default:
		// Unknown tags: keep any text payload instead of dropping the
		// element silently, so new Feishu element kinds still reach the
		// agent as (partial) text.
		if elem.Text != "" {
			*parts = append(*parts, elem.Text)
		}
	}
}

// extractInteractiveReceiveParts parses an inbound interactive message body
// in Feishu's simplified receive format. Returns readable text parts plus
// image_keys for the caller to download. Returns nil, nil when the body is
// not in receive format (the caller falls back to extractInteractiveCardText,
// which covers schema 2.0 and raw_card_content payloads).
func extractInteractiveReceiveParts(content string) (parts []string, imageKeys []string) {
	var card struct {
		Title    string          `json:"title"`
		Elements json.RawMessage `json:"elements"`
		CardLink json.RawMessage `json:"card_link"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		return nil, nil
	}
	// Tolerate locale-wrapped bodies ({"zh_cn": {...}}) the same way the
	// post parser does: retry with the first object value that parses.
	if len(card.Elements) == 0 && card.Title == "" {
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal([]byte(content), &wrapper); err == nil {
			for _, v := range wrapper {
				var inner struct {
					Title    string          `json:"title"`
					Elements json.RawMessage `json:"elements"`
				}
				if json.Unmarshal(v, &inner) == nil && (len(inner.Elements) > 0 || inner.Title != "") {
					card.Title, card.Elements = inner.Title, inner.Elements
					break
				}
			}
		}
	}
	if len(card.Elements) == 0 && card.Title == "" {
		return nil, nil
	}
	if card.Title != "" {
		parts = append(parts, card.Title)
	}
	for _, raw := range flattenInteractiveRows(card.Elements) {
		appendInteractiveReceiveElement(raw, &parts, &imageKeys)
	}
	appendCardLinkRaw(card.CardLink, &parts)
	return parts, imageKeys
}

// extractInteractiveReceiveText renders an inbound interactive card body to
// plain text without network access. Image slots become "[image]" markers,
// matching the post-quote convention. Falls back to extractInteractiveCardText
// (schema 2.0 / raw_card_content / legacy) when the body is not in receive
// format. Returns "" only when nothing readable could be extracted.
func extractInteractiveReceiveText(content string) string {
	parts, imageKeys := extractInteractiveReceiveParts(content)
	if len(parts) == 0 && len(imageKeys) == 0 {
		return extractInteractiveCardText(content)
	}
	for range imageKeys {
		parts = append(parts, "[image]")
	}
	return strings.Join(parts, "\n")
}

// fetchRawCardContent returns a card message's original payload via the
// documented user_card_content parameter (Card 1.0 original JSON, or the
// Card 2.0 original with schema/body/header). The default event/API body
// for 2.0 cards is a lossy fallback (title + upgrade notice + placeholder
// image on clients < 7.20), so live dispatch prefers this source and only
// falls back to the event body when the fetch fails. Returns "" on any
// failure; callers must fall back rather than drop the turn.
func (p *Platform) fetchRawCardContent(ctx context.Context, messageID string) string {
	if strings.TrimSpace(messageID) == "" || p.client == nil {
		return ""
	}
	apiPath := fmt.Sprintf("/open-apis/im/v1/messages/%s?card_msg_content_type=user_card_content", messageID)
	apiResp, err := p.client.Get(ctx, apiPath, nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		slog.Debug(p.tag()+": fetch raw card failed", "message_id", messageID, "error", err)
		return ""
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(apiResp.RawBody, &resp); err != nil || resp.Code != 0 ||
		len(resp.Data.Items) == 0 || resp.Data.Items[0].Body.Content == "" {
		slog.Debug(p.tag()+": fetch raw card: empty or error payload", "message_id", messageID)
		return ""
	}
	return resp.Data.Items[0].Body.Content
}

// downloadInteractiveImages fetches card image_keys best-effort: a failed
// image is logged and skipped so one expired image_key does not drop the
// whole card turn.
func (p *Platform) downloadInteractiveImages(messageID string, imageKeys []string) []core.ImageAttachment {
	var images []core.ImageAttachment
	for _, key := range imageKeys {
		if key == "" {
			continue
		}
		data, mimeType, err := p.downloadImage(messageID, key)
		if err != nil {
			slog.Error(p.tag()+": download interactive image failed", "error", err, "key", key)
			continue
		}
		images = append(images, core.ImageAttachment{MimeType: mimeType, Data: data})
	}
	return images
}

// parseInteractiveContent parses an inbound interactive (card) message into
// text parts and downloaded images for live dispatch. @bot tokens are left
// for the caller to strip via stripMentions, mirroring the text/post flow.
// Falls back to extractInteractiveCardText when the body is not in receive
// format (e.g. a raw schema-2.0 card delivered verbatim).
func (p *Platform) parseInteractiveContent(messageID, raw string, mentions []*larkim.MentionEvent) ([]string, []core.ImageAttachment) {
	parts, imageKeys := extractInteractiveReceiveParts(raw)
	if len(parts) == 0 && len(imageKeys) == 0 {
		if fallback := extractInteractiveCardText(raw); fallback != "" {
			return []string{stripMentions(fallback, mentions, p.getBotOpenID())}, nil
		}
		return nil, nil
	}
	return parts, p.downloadInteractiveImages(messageID, imageKeys)
}

// mapMentionKeys indexes event mentions by every user ID form so Card 2.0
// raw payloads (which carry real open_ids, not @_user_N placeholders) can
// be rewritten into the placeholder space stripMentions understands.
func mapMentionKeys(mentions []*larkim.MentionEvent) map[string]string {
	out := make(map[string]string)
	for _, m := range mentions {
		if m == nil || m.Key == nil || *m.Key == "" || m.Id == nil {
			continue
		}
		for _, id := range []string{
			stringValue2(m.Id.OpenId), stringValue2(m.Id.UserId), stringValue2(m.Id.UnionId),
		} {
			if id != "" {
				out[id] = *m.Key
			}
		}
	}
	return out
}

// mapAPIMentionKeys is mapMentionKeys for the API-shape mentions
// ([]*larkim.Mention, plain open_id in Id) found on Get-message responses.
func mapAPIMentionKeys(mentions []*larkim.Mention) map[string]string {
	out := make(map[string]string)
	for _, m := range mentions {
		if m == nil || m.Key == nil || *m.Key == "" || m.Id == nil || *m.Id == "" {
			continue
		}
		out[*m.Id] = *m.Key
	}
	return out
}

func stringValue2(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Card 2.0 raw-DSL element. Field names cover both the raw json_card /
// user_card_content serialization (camelCase under property) and the
// send-style schema 2.0 (snake_case); encoding/json matches keys
// case-insensitively, and genuinely different spellings get both fields.
type card2Element struct {
	Tag         string            `json:"tag"`
	Text        string            `json:"text"`
	Content     string            `json:"content"`
	Href        string            `json:"href"`
	UserID      string            `json:"user_id"`
	UserName    string            `json:"user_name"`
	ImageKey    string            `json:"image_key"`
	Placeholder string            `json:"placeholder"`
	URL         string            `json:"url"`
	Level       int               `json:"level"`
	Property    card2Property     `json:"property"`
	Elements    []json.RawMessage `json:"elements"`
	// Flat-serialization containers mount children directly (no property
	// wrapper); see user_card_content for Card 2.0 originals.
	Columns []json.RawMessage `json:"columns"`
	Rows    json.RawMessage   `json:"rows"`
}

// card2Property is the component payload of a Card 2.0 raw-DSL element.
type card2Property struct {
	Content   string            `json:"content"`
	ImageKey  string            `json:"image_key"`
	ImageKeyC string            `json:"imageKey"`
	UserID    string            `json:"userID"`
	Level     int               `json:"level"`
	Elements  []json.RawMessage `json:"elements"`
	Columns   []json.RawMessage `json:"columns"`
	Rows      json.RawMessage   `json:"rows"`
	Text      json.RawMessage   `json:"text"`
	Behaviors json.RawMessage   `json:"behaviors"`
}

// appendCard2Element renders one Card 2.0 raw-DSL component. At mentions
// carry real user IDs, so they are rewritten to @_user_N placeholders via
// keyMap (built from the event mentions) for stripMentions downstream.
func appendCard2Element(raw json.RawMessage, parts *[]string, imageKeys *[]string, keyMap map[string]string) {
	var elem card2Element
	if err := json.Unmarshal(raw, &elem); err != nil {
		return
	}
	prop := elem.Property
	switch elem.Tag {
	case "plain_text", "text":
		if c := firstNonEmpty(prop.Content, elem.Content, elem.Text); c != "" {
			*parts = append(*parts, cleanCard2Text(c, keyMap))
		}
	case "heading":
		var inner []string
		for _, nested := range prop.Elements {
			var tmp []string
			appendCard2Element(nested, &tmp, imageKeys, keyMap)
			inner = append(inner, tmp...)
		}
		text := strings.Join(inner, "")
		if text == "" {
			text = firstNonEmpty(prop.Content, elem.Content)
		}
		if text == "" {
			return
		}
		if prop.Level >= 1 && prop.Level <= 6 {
			text = strings.Repeat("#", prop.Level) + " " + text
		}
		*parts = append(*parts, text)
	case "markdown":
		if len(prop.Elements) > 0 {
			for _, nested := range prop.Elements {
				appendCard2Element(nested, parts, imageKeys, keyMap)
			}
			return
		}
		if c := firstNonEmpty(prop.Content, elem.Content, elem.Text); c != "" {
			*parts = append(*parts, cleanCard2Text(c, keyMap))
		}
	case "a":
		switch t, h := cleanCard2Text(firstNonEmpty(prop.Content, elem.Text), keyMap), elem.Href; {
		case t != "" && h != "":
			*parts = append(*parts, fmt.Sprintf("[%s](%s)", t, h))
		case t != "":
			*parts = append(*parts, t)
		case h != "":
			*parts = append(*parts, h)
		}
	case "at":
		uid := firstNonEmpty(prop.UserID, elem.UserID)
		switch {
		case uid == "all":
			*parts = append(*parts, "@all")
		case uid != "":
			if key, ok := keyMap[uid]; ok {
				*parts = append(*parts, key)
			} else {
				*parts = append(*parts, "@"+uid)
			}
		case elem.UserName != "":
			*parts = append(*parts, "@"+elem.UserName)
		}
	case "column_set":
		cols := prop.Columns
		if len(cols) == 0 {
			cols = elem.Columns
		}
		for _, col := range cols {
			appendCard2Element(col, parts, imageKeys, keyMap)
		}
	case "column":
		nested := prop.Elements
		if len(nested) == 0 {
			nested = elem.Elements
		}
		for _, el := range nested {
			appendCard2Element(el, parts, imageKeys, keyMap)
		}
	case "table":
		colsRaw := mustMarshal(prop.Columns)
		if string(colsRaw) == "null" || string(colsRaw) == "[]" {
			colsRaw = mustMarshal(elem.Columns)
		}
		rowsRaw := prop.Rows
		if len(rowsRaw) == 0 {
			rowsRaw = elem.Rows
		}
		appendCard2Table(colsRaw, rowsRaw, parts, keyMap)
	case "img", "image":
		if k := firstNonEmpty(elem.ImageKey, prop.ImageKey, prop.ImageKeyC); k != "" {
			*imageKeys = append(*imageKeys, k)
		}
	case "button", "action":
		label := prop.Content
		if label == "" && len(prop.Text) > 0 {
			var tmp []string
			appendCard2Element(prop.Text, &tmp, imageKeys, keyMap)
			label = strings.Join(tmp, "")
		}
		if label == "" {
			label = firstNonEmpty(elem.Content, elem.Text)
		}
		openURL := ""
		if len(prop.Behaviors) > 0 {
			var behaviors []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			}
			if json.Unmarshal(prop.Behaviors, &behaviors) == nil {
				for _, b := range behaviors {
					if (b.Type == "open_url" || b.Type == "openUrl") && b.URL != "" {
						openURL = b.URL
						break
					}
				}
			}
		}
		switch {
		case label != "" && openURL != "":
			*parts = append(*parts, fmt.Sprintf("[%s](%s)", label, openURL))
		case label != "" && elem.URL != "":
			*parts = append(*parts, fmt.Sprintf("[%s](%s)", label, elem.URL))
		case label != "":
			*parts = append(*parts, label)
		}
	case "divider", "hr":
		*parts = append(*parts, "---")
	case "card_header", "ud_icon", "icon":
		// Decorative only; the header title is extracted at the top level.
	default:
		// Unknown or container tags (form, chart, note, body, ...): descend
		// into nested element lists so new component kinds degrade to
		// partial text instead of silence.
		for _, nested := range prop.Elements {
			appendCard2Element(nested, parts, imageKeys, keyMap)
		}
		for _, col := range prop.Columns {
			appendCard2Element(col, parts, imageKeys, keyMap)
		}
		if len(prop.Text) > 0 {
			appendCard2Element(prop.Text, parts, imageKeys, keyMap)
		}
		for _, nested := range elem.Elements {
			appendCard2Element(nested, parts, imageKeys, keyMap)
		}
		if c := firstNonEmpty(prop.Content, elem.Content, elem.Text); c != "" &&
			len(prop.Elements) == 0 && len(prop.Columns) == 0 && len(prop.Text) == 0 && len(elem.Elements) == 0 {
			*parts = append(*parts, c)
		}
	}
}

// Card 2.0 markdown mention syntax: <at id="ou_xxx">name</at>. Rewritten to
// @_user_N placeholders (via keyMap) so stripMentions resolves names and
// drops the bot token downstream.
var card2AtRe = regexp.MustCompile(`(?i)<at\s+id\s*=\s*"?([^"\s>]+)"?[^>]*>(.*?)</at>`)

// Fallback @-IDs that never entered the placeholder space (raw open_ids
// with no event mention entry), resolved to display names best-effort.
// Covers the documented user-ID prefixes (ou_ = open_id, on_ = union_id);
// a trailing ".domain" forces email treatment so "a@on_call.com" is never
// rewritten. Bare user_id (employee_id) has no distinguishable prefix and
// can only resolve through the mention list, never this regex.
var card2RawAtRe = regexp.MustCompile(`@((?:ou|on)_[A-Za-z0-9_]+)(\.[A-Za-z0-9.-]*)?`)

// Lightweight formatting tags kept as plain text (inner content preserved).
var card2BrRe = regexp.MustCompile(`(?i)<br\s*/?>`)
var card2FmtRe = regexp.MustCompile(`(?i)</?(font|b|strong|i|em|u|s|strike|del|span|div)(\s[^>]*)?>`)

// cleanCard2Text normalizes Card 2.0 rich-text content for the agent:
// inline <at> mentions become resolvable tokens, presentational tags are
// stripped but their inner text is kept.
func cleanCard2Text(s string, keyMap map[string]string) string {
	s = card2AtRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := card2AtRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		uid := sub[1]
		if uid == "all" {
			return "@all"
		}
		if key, ok := keyMap[uid]; ok {
			return key
		}
		return "@" + uid
	})
	s = card2BrRe.ReplaceAllString(s, "\n")
	return card2FmtRe.ReplaceAllString(s, "")
}

// resolveRawAtIDs rewrites leftover "@ou_xxx" tokens (card @-mentions with
// no event mention entry, so stripMentions could not resolve them) to
// "@name" via the mention list first and the contact API second. Unknown
// IDs are left verbatim so no information is lost.
func (p *Platform) resolveRawAtIDs(text string, mentions []*larkim.MentionEvent) string {
	if !card2RawAtRe.MatchString(text) {
		return text
	}
	nameByID := make(map[string]string)
	for _, m := range mentions {
		if m == nil || m.Id == nil || m.Name == nil || *m.Name == "" {
			continue
		}
		for _, id := range []string{stringValue2(m.Id.OpenId), stringValue2(m.Id.UserId), stringValue2(m.Id.UnionId)} {
			if id != "" {
				nameByID[id] = *m.Name
			}
		}
	}
	return card2RawAtRe.ReplaceAllStringFunc(text, func(token string) string {
		sub := card2RawAtRe.FindStringSubmatch(token)
		if len(sub) < 2 {
			return token
		}
		if sub[2] != "" {
			return token // email-like, leave untouched
		}
		id := sub[1]
		if name, ok := nameByID[id]; ok {
			return "@" + name
		}
		if p.client == nil {
			return token
		}
		if resolved := p.resolveUserName(id); resolved != id {
			return "@" + resolved
		}
		// union_id is only resolvable under its own user_id_type.
		if strings.HasPrefix(id, "on_") {
			if resolved := p.resolveUserNameAs(id, "union_id"); resolved != id {
				return "@" + resolved
			}
		}
		return token
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// appendCard2Table renders a Card 2.0 raw-DSL table as a markdown table.
// Unlike the send-style table (whose cells hold element objects), raw-DSL
// cells usually hold plain strings ({"col": {"data": "..."}}); element
// objects are still supported via the generic walker. Column headers accept
// both displayName (nested DSL) and display_name (flat DSL).
func appendCard2Table(columnsRaw, rowsRaw json.RawMessage, parts *[]string, keyMap map[string]string) {
	var columns []struct {
		DisplayName  string `json:"displayName"`
		DisplayName2 string `json:"display_name"`
		Name         string `json:"name"`
	}
	if err := json.Unmarshal(columnsRaw, &columns); err != nil || len(columns) == 0 {
		return
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(rowsRaw, &rows); err != nil {
		return
	}
	header := make([]string, len(columns))
	for i, col := range columns {
		header[i] = firstNonEmpty(col.DisplayName, col.DisplayName2, col.Name)
	}
	*parts = append(*parts, "| "+strings.Join(header, " | ")+" |")
	sep := make([]string, len(columns))
	for i := range sep {
		sep[i] = "---"
	}
	*parts = append(*parts, "| "+strings.Join(sep, " | ")+" |")
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cell := row[col.Name]
			// Nested DSL wraps values ({"col": {"data": "..."}}); flat
			// DSL stores them bare ({"col": "..."}).
			var wrapped struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(cell, &wrapped) == nil && len(wrapped.Data) > 0 {
				cell = wrapped.Data
			}
			var s string
			if json.Unmarshal(cell, &s) == nil {
				cells[i] = cleanCard2Text(s, keyMap)
				continue
			}
			var num float64
			if json.Unmarshal(cell, &num) == nil {
				cells[i] = strconv.FormatFloat(num, 'f', -1, 64)
				continue
			}
			var cellParts []string
			appendCard2Element(cell, &cellParts, nil, keyMap)
			cells[i] = strings.Join(cellParts, " ")
		}
		*parts = append(*parts, "| "+strings.Join(cells, " | ")+" |")
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// appendCardLink extracts the whole-card jump link (card_link.url, with
// per-platform fallbacks) so the agent knows where tapping the card leads.
// Rendered after the body content to keep the card text contiguous.
func appendCardLinkRaw(raw json.RawMessage, parts *[]string) {
	if len(raw) == 0 {
		return
	}
	var link struct {
		URL        string `json:"url"`
		PCURL      string `json:"pc_url"`
		PCURLC     string `json:"pcURL"`
		AndroidURL string `json:"android_url"`
		IOSURL     string `json:"ios_url"`
	}
	if json.Unmarshal(raw, &link) != nil {
		return
	}
	if url := firstNonEmpty(link.URL, link.PCURL, link.PCURLC, link.AndroidURL, link.IOSURL); url != "" {
		*parts = append(*parts, "[卡片链接]("+url+")")
	}
}

func appendCardLink(card map[string]json.RawMessage, parts *[]string) {
	appendCardLinkRaw(card["card_link"], parts)
}

// extractCard2RawParts parses a Card 2.0 original payload: either the direct
// card JSON (user_card_content) or the {"json_card": "..."} wrapper
// (raw_card_content). Returns nil when the input is not a 2.0 card so the
// caller can fall back to the receive-format / legacy parsers.
func extractCard2RawParts(cardJSON string, keyMap map[string]string) (parts []string, imageKeys []string) {
	raw := cardJSON
	var wrapper struct {
		JsonCard string `json:"json_card"`
	}
	if json.Unmarshal([]byte(cardJSON), &wrapper) == nil && wrapper.JsonCard != "" {
		raw = wrapper.JsonCard
	}
	var card map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return nil, nil
	}
	// Raw originals always carry body and/or schema. The lossy
	// receive-format envelope ({title, elements}) carries neither: it must
	// fall through to the receive-format parser, which understands it.
	if _, ok := card["body"]; !ok {
		if _, ok := card["schema"]; !ok {
			return nil, nil
		}
	}

	// Header title (raw DSL nests it under property.title).
	if h, ok := card["header"]; ok {
		var header struct {
			Property struct {
				Title struct {
					Property struct {
						Content string `json:"content"`
					} `json:"property"`
					Content string `json:"content"`
				} `json:"title"`
			} `json:"property"`
			Title struct {
				Content string `json:"content"`
			} `json:"title"`
		}
		if json.Unmarshal(h, &header) == nil {
			if c := firstNonEmpty(header.Property.Title.Property.Content, header.Property.Title.Content, header.Title.Content); c != "" {
				parts = append(parts, c)
			}
		}
	}
	// Legacy flat title (Card 1.0 originals served via the same parameter).
	if len(parts) == 0 {
		if t, ok := card["title"]; ok {
			var title string
			if json.Unmarshal(t, &title) == nil && title != "" {
				parts = append(parts, title)
			}
		}
	}

	body, ok := card["body"]
	if !ok {
		if len(parts) == 0 {
			return nil, nil
		}
		return parts, imageKeys
	}
	// body may be a component ({tag, property.elements}) or bare elements.
	var bodyElem card2Element
	if json.Unmarshal(body, &bodyElem) != nil {
		if len(parts) == 0 {
			return nil, nil
		}
		return parts, imageKeys
	}
	if len(bodyElem.Property.Elements) > 0 {
		for _, el := range bodyElem.Property.Elements {
			appendCard2Element(el, &parts, &imageKeys, keyMap)
		}
	} else if len(bodyElem.Elements) > 0 {
		for _, el := range bodyElem.Elements {
			appendCard2Element(el, &parts, &imageKeys, keyMap)
		}
	} else {
		appendCard2Element(body, &parts, &imageKeys, keyMap)
	}
	appendCardLink(card, &parts)
	if len(parts) == 0 && len(imageKeys) == 0 {
		return nil, nil
	}
	return parts, imageKeys
}
