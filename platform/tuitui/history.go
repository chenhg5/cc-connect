package tuitui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type HistoryOptions struct {
	RelativeTime string `json:"relative_time,omitempty"`
	StartTime    string `json:"start_time,omitempty"`
	EndTime      string `json:"end_time,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	OrderAsc     *bool  `json:"order_asc,omitempty"`
}

type HistoryResult struct {
	ErrCode     int              `json:"errcode"`
	ErrMsg      string           `json:"errmsg,omitempty"`
	Cursor      string           `json:"cursor,omitempty"`
	HasMore     bool             `json:"has_more,omitempty"`
	CurrentTime any              `json:"current_time,omitempty"`
	Subject     string           `json:"subject,omitempty"`
	Messages    []map[string]any `json:"msgs,omitempty"`
	Threads     []string         `json:"threads,omitempty"`
}

func (p *Platform) FetchHistory(ctx context.Context, chatID, chatType string, opts HistoryOptions) (*HistoryResult, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("tuitui: chat id is required")
	}
	if chatType == "" {
		chatType = guessChatType(chatID)
	}
	switch chatType {
	case chatTypeDirect, chatTypeGroup:
		return p.fetchDirectOrGroupHistory(ctx, chatID, chatType, opts)
	case chatTypeChannel:
		return p.fetchChannelHistory(ctx, chatID, opts)
	default:
		return nil, fmt.Errorf("tuitui: invalid chat type %q", chatType)
	}
}

func (p *Platform) fetchDirectOrGroupHistory(ctx context.Context, chatID, chatType string, opts HistoryOptions) (*HistoryResult, error) {
	payload := map[string]any{"cursor": "0"}
	if chatType == chatTypeDirect {
		payload["user"] = chatID
	} else {
		payload["group_id"] = chatID
	}
	addHistoryOptions(payload, opts)

	apiPath := "/robot/message/group/sync"
	if chatType == chatTypeDirect {
		apiPath = "/robot/message/single/sync"
	}
	var out struct {
		ErrCode int              `json:"errcode"`
		ErrMsg  string           `json:"errmsg"`
		Cursor  string           `json:"cursor"`
		HasMore bool             `json:"has_more"`
		Time    any              `json:"time"`
		Msgs    []map[string]any `json:"msgs"`
	}
	if err := p.postJSON(ctx, apiPath, payload, &out); err != nil {
		return nil, err
	}
	if out.ErrCode != 0 {
		return nil, fmt.Errorf("tuitui: history errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	return &HistoryResult{
		ErrCode:     out.ErrCode,
		ErrMsg:      out.ErrMsg,
		Cursor:      out.Cursor,
		HasMore:     out.HasMore,
		CurrentTime: out.Time,
		Messages:    cleanHistoryMessages(out.Msgs),
	}, nil
}

func (p *Platform) fetchChannelHistory(ctx context.Context, chatID string, opts HistoryOptions) (*HistoryResult, error) {
	parsed := map[string]string{}
	if strings.HasPrefix(chatID, "teams_") {
		parsed = teamsParseChatID(chatID)
	} else {
		parsed["channel_id"] = chatID
	}
	channelID := firstNonEmpty(parsed["channel_id"], chatID)
	teamID := parsed["team_id"]
	subject := ""
	if teamID == "" {
		info, err := p.getChannelInfo(ctx, channelID)
		if err != nil {
			return nil, err
		}
		teamID = stringFromAny(info["team_id"])
		subject = stringFromAny(info["name"])
	}
	if teamID == "" {
		return nil, fmt.Errorf("tuitui: team_id is required for channel history")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	limit = minInt(100, maxInt(1, limit))
	order := "asc"
	if opts.OrderAsc != nil && !*opts.OrderAsc {
		order = "desc"
	}
	payload := map[string]any{
		"channel_id": channelID,
		"team_id":    teamID,
		"size":       limit,
		"sort_type":  "reply",
		"order":      order,
	}
	if opts.RelativeTime != "" {
		if start, end, ok := parseRelativeTime(opts.RelativeTime); ok {
			payload["from_timestamp"] = start.UnixMilli()
			payload["end_timestamp"] = end.UnixMilli()
		}
	} else {
		if opts.StartTime != "" {
			if t, err := parseHistoryTime(opts.StartTime); err == nil {
				payload["from_timestamp"] = t.UnixMilli()
			}
		}
		if opts.EndTime != "" {
			if t, err := parseHistoryTime(opts.EndTime); err == nil {
				payload["end_timestamp"] = t.UnixMilli()
			}
		}
	}
	if opts.Cursor != "" && opts.Cursor != "0" {
		if n, err := strconv.ParseInt(opts.Cursor, 10, 64); err == nil {
			payload["from_timestamp"] = n
		} else {
			payload["from_timestamp"] = opts.Cursor
		}
	}

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Time    any    `json:"time"`
		Datas   struct {
			PostList []map[string]any `json:"post_list"`
		} `json:"datas"`
	}
	if err := p.postJSON(ctx, "/robot/teams/post/topic/list", payload, &out); err != nil {
		return nil, err
	}
	if out.ErrCode != 0 {
		return nil, fmt.Errorf("tuitui: channel history errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	cursor := ""
	if len(out.Datas.PostList) > 0 {
		if topic, _ := out.Datas.PostList[len(out.Datas.PostList)-1]["topic"].(map[string]any); topic != nil {
			if n := numberFromAny(topic["last_reply_time"]); n > 0 {
				cursor = strconv.FormatInt(int64(n)+1, 10)
			}
		}
	}
	return &HistoryResult{
		ErrCode:     out.ErrCode,
		ErrMsg:      out.ErrMsg,
		Cursor:      cursor,
		HasMore:     len(out.Datas.PostList) >= limit,
		CurrentTime: out.Time,
		Subject:     subject,
		Threads:     formatPostThreads(out.Datas.PostList),
	}, nil
}

func addHistoryOptions(payload map[string]any, opts HistoryOptions) {
	if opts.RelativeTime != "" {
		payload["relative_time"] = opts.RelativeTime
	} else {
		if opts.StartTime != "" {
			payload["start_time"] = opts.StartTime
		}
		if opts.EndTime != "" {
			payload["end_time"] = opts.EndTime
		}
	}
	if opts.Cursor != "" {
		payload["cursor"] = opts.Cursor
	}
	if opts.Limit > 0 {
		payload["limit"] = opts.Limit
	}
	if opts.OrderAsc != nil {
		payload["order_asc"] = *opts.OrderAsc
	}
}

func cleanHistoryMessages(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		item := map[string]any{}
		if data, _ := msg["data"].(map[string]any); data != nil {
			for k, v := range data {
				switch k {
				case "at", "msgid", "group_id", "group_name":
					continue
				default:
					item[k] = v
				}
			}
		}
		for _, key := range []string{"user_account", "user_name"} {
			if v, ok := msg[key]; ok {
				item[key] = v
			}
		}
		if n := numberFromAny(msg["timestamp"]); n > 0 {
			item["msg_time"] = time.Unix(int64(n), 0).Format("2006-01-02 15:04:05")
		}
		out = append(out, item)
	}
	return out
}

func (p *Platform) getChannelInfo(ctx context.Context, channelID string) (map[string]any, error) {
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Datas   struct {
			Info map[string]any `json:"info"`
		} `json:"datas"`
	}
	if err := p.postJSON(ctx, "/robot/teams/channel/info", map[string]any{"channel_id": channelID}, &out); err != nil {
		return nil, err
	}
	if out.ErrCode != 0 {
		return nil, fmt.Errorf("tuitui: channel info errcode=%d errmsg=%s", out.ErrCode, out.ErrMsg)
	}
	return out.Datas.Info, nil
}

func formatPostThreads(posts []map[string]any) []string {
	threads := make([]string, 0, len(posts))
	for _, postThread := range posts {
		var items []map[string]any
		if topic, _ := postThread["topic"].(map[string]any); topic != nil {
			items = append(items, topic)
		}
		if replies, _ := postThread["reply_list"].([]any); len(replies) > 0 {
			for i := len(replies) - 1; i >= 0; i-- {
				if reply, _ := replies[i].(map[string]any); reply != nil {
					items = append(items, reply)
				}
			}
		}
		var parts []string
		for i, post := range items {
			label := "[讨论主贴]"
			if i > 0 {
				label = "[讨论回帖]"
			}
			lines := []string{
				label,
				"post_id: " + stringFromAny(post["post_id"]),
				"发言人: " + stringFromAny(post["from_name"]),
				"时间: " + formatPostTime(post["create_time"], post["last_reply_time"]),
				"内容: " + stringFromAny(post["content"]),
			}
			if props, _ := post["properties"].(map[string]any); props != nil {
				lines = append(lines, historyPropertyLines("文件", props["files"])...)
				lines = append(lines, historyPropertyLines("图片", props["images"])...)
			}
			parts = append(parts, strings.Join(lines, "\n"))
		}
		threads = append(threads, strings.Join(parts, "\n\n"))
	}
	return threads
}

func historyPropertyLines(label string, value any) []string {
	items, _ := value.([]any)
	var lines []string
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s: %s", label, stringFromAny(item["name"]), stringFromAny(item["url"])))
	}
	return lines
}

func formatPostTime(values ...any) string {
	for _, value := range values {
		if n := numberFromAny(value); n > 0 {
			return time.UnixMilli(int64(n)).Format("2006-01-02 15:04:05")
		}
	}
	return ""
}

func parseRelativeTime(relativeTime string) (time.Time, time.Time, bool) {
	now := time.Now()
	end := now
	switch relativeTime {
	case "today":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 0, 1), true
	case "yesterday":
		end = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return end.AddDate(0, 0, -1), end, true
	case "day_before_yesterday":
		end = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
		return end.AddDate(0, 0, -1), end, true
	}
	parts := strings.Split(relativeTime, "_")
	if len(parts) != 3 || parts[0] != "last" {
		return time.Time{}, time.Time{}, false
	}
	amount, err := strconv.Atoi(parts[1])
	if err != nil || amount <= 0 {
		return time.Time{}, time.Time{}, false
	}
	start := end
	switch parts[2] {
	case "minutes":
		start = start.Add(-time.Duration(amount) * time.Minute)
	case "hours":
		start = start.Add(-time.Duration(amount) * time.Hour)
	case "days":
		start = start.AddDate(0, 0, -amount)
	case "months":
		start = start.AddDate(0, -amount, 0)
	case "years":
		start = start.AddDate(-amount, 0, 0)
	default:
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

func parseHistoryTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.Local); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", value, time.Local)
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func numberFromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	default:
		return 0
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
