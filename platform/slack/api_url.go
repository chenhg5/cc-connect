package slack

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/slack-go/slack"
)

// knownSlackDomains are the hosts (and their subdomains) that legitimately
// serve the Slack Web API. Any other https host gets a startup warning,
// because both tokens are sent to it.
var knownSlackDomains = []string{"slack.com", "slack-gov.com"}

// apiURLOption reads the optional base_url platform option. A missing key
// means "use the SDK default"; a value of any type other than string is a
// configuration error rather than something to drop silently.
func apiURLOption(opts map[string]any) (string, error) {
	v, present := opts["base_url"]
	if !present || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("slack: base_url must be a string, got %T", v)
	}
	return normalizeAPIURL(s)
}

// normalizeAPIURL validates the optional base_url option, which overrides the
// Slack Web API root (default https://slack.com/api/), e.g. for GovSlack
// (https://slack-gov.com/api/) or a Slack-compatible relay.
//
// The SDK builds request URLs as endpoint + method name, so the root has to
// end in "/api/". A bare host gets "/api/" appended; any other path must end
// in "/api" or "/api/", otherwise the first API call would 404 at runtime.
// Socket Mode needs no separate setting: the SDK opens it with the Web API
// call apps.connections.open against the same root, and the WebSocket URL
// comes back in that response.
//
// An empty value means "use the SDK default" and returns "".
func normalizeAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// The wrapped url.Error, and some of its inner errors (e.g. an invalid
		// escape or port), quote parts of the value, so they are dropped too.
		return "", fmt.Errorf("slack: invalid base_url %q (details omitted: "+
			"the host part is masked because it may contain credentials)", logSafe(raw))
	}
	if u.User != nil {
		// Don't echo the value: it would put the credentials in the logs.
		return "", fmt.Errorf("slack: base_url must not contain user info (user:password@)")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("slack: base_url %q must use http or https", logSafe(raw))
	}
	if u.Host == "" {
		return "", fmt.Errorf("slack: base_url %q has no host", logSafe(raw))
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("slack: base_url %q must not contain a query or fragment", logSafe(raw))
	}

	switch path := u.Path; {
	case path == "" || path == "/":
		u.Path = "/api/"
	case strings.HasSuffix(path, "/api") || strings.HasSuffix(path, "/api/"):
		if !strings.HasSuffix(path, "/") {
			u.Path = path + "/"
		}
	default:
		return "", fmt.Errorf("slack: base_url %q must be the Web API root ending in /api/ "+
			"(e.g. https://slack-gov.com/api/); a bare host gets /api/ appended", logSafe(raw))
	}
	u.RawPath = ""

	host := u.Hostname()
	switch {
	case isLoopbackHost(host):
	case u.Scheme == "http":
		slog.Warn("slack: base_url uses plain http to a non-loopback host; "+
			"the bot and app tokens will be sent unencrypted", "host", host)
	case !isKnownSlackHost(host):
		slog.Warn("slack: base_url host is not a Slack domain; the bot and app tokens "+
			"will be sent to it, so check it is the GovSlack or relay endpoint you intend",
			"host", host)
	}
	return u.String(), nil
}

// isKnownSlackHost reports whether host is one of knownSlackDomains or a
// subdomain of one. A look-alike such as slack.com.example.test does not match.
func isKnownSlackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range knownSlackDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// logSafe returns a version of a base_url value that is safe to put in an
// error or a log line. Every error path goes through it, because a value can
// fail validation while carrying a secret: in the authority (user:secret@...,
// or an opaque "token:secret@host" with no scheme) or in the query
// (?token=...). The query and fragment are dropped entirely, then the
// authority is masked.
func logSafe(raw string) string {
	s, cut := raw, false
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s, cut = s[:i], true
	}
	s = redactAuthority(s)
	if cut {
		s += "?<omitted>"
	}
	return s
}

// redactAuthority masks the part of an unparseable URL that may hold
// credentials, keeping the rest for diagnosis.
//
// Normally that is the authority: everything between the scheme and the next
// "/". When the value contains an "@", the authority can't be located
// reliably (a malformed password may carry an unescaped "/", "?" or "#"), so
// the mask runs greedily up to the last "@" instead.
func redactAuthority(raw string) string {
	start := 0
	if i := strings.Index(raw, "://"); i >= 0 {
		start = i + len("://")
	}
	rest := raw[start:]
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		return raw[:start] + "REDACTED" + rest[at:]
	}
	if slash := strings.Index(rest, "/"); slash >= 0 {
		return raw[:start] + "REDACTED" + rest[slash:]
	}
	return raw[:start] + "REDACTED"
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
