package slack

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/slack-go/slack"
)

// normalizeAPIURL validates the optional base_url option, which overrides the
// Slack Web API root (default https://slack.com/api/), e.g. for GovSlack
// (https://slack-gov.com/api/) or a Slack-compatible relay.
//
// The SDK builds request URLs as endpoint + method name, so the result always
// ends with "/". Socket Mode needs no separate setting: the SDK opens it with
// the Web API call apps.connections.open against the same root, and the
// WebSocket URL comes back in that response.
//
// An empty value means "use the SDK default" and returns "".
func normalizeAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("slack: invalid base_url %q: %w", raw, err)
	}
	if u.User != nil {
		// Don't echo the value: it would put the credentials in the logs.
		return "", fmt.Errorf("slack: base_url must not contain user info (user:password@)")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("slack: base_url %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("slack: base_url %q has no host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("slack: base_url %q must not contain a query or fragment", raw)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		slog.Warn("slack: base_url uses plain http to a non-loopback host; "+
			"the bot and app tokens will be sent unencrypted", "base_url", raw)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newClient builds the Web API client, pointing it at base_url when set.
func (p *Platform) newClient() *slack.Client {
	opts := []slack.Option{slack.OptionAppLevelToken(p.appToken)}
	if p.apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(p.apiURL))
	}
	return slack.New(p.botToken, opts...)
}
