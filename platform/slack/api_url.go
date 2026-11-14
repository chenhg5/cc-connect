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
		return "", fmt.Errorf("slack: base_url %q must use http or https", describeURL(u))
	}
	if u.Host == "" {
		return "", fmt.Errorf("slack: base_url %q has no host", describeURL(u))
	}
	// ForceQuery covers a bare trailing "?": the SDK appends method names to
	// the root, so ".../api/?" would turn every method into a query string.
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("slack: base_url %q must not contain a query or fragment", describeURL(u))
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
			"(e.g. https://slack-gov.com/api/); a bare host gets /api/ appended", describeURL(u))
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

// Every error and log line shows a base_url value through describeURL (when
// it parsed) or logSafe (when it didn't), never verbatim, because a value can
// carry a secret in the authority, the path or the query. The rule is the same
// for both:
//   - the query and fragment are never shown;
//   - the path is shown only when it is exactly "", "/", "/api" or "/api/";
//     a longer path ending in /api/ shows as "/<omitted>/api/" so the shape
//     is still diagnosable, and any other path as "/<path omitted>";
//   - the host is shown when the value parsed (user info was rejected before
//     any of these messages), and masked when it didn't, since the authority
//     of a malformed value can't be told apart from a password.

// describeURL renders a parsed base_url for an error or log line.
func describeURL(u *url.URL) string {
	if u.Opaque != "" {
		// e.g. "token:secret@relay.example.com": no "//", so everything after
		// the scheme is opaque and may be a credential.
		return "<opaque value omitted>"
	}
	var b strings.Builder
	if u.Scheme != "" {
		b.WriteString(u.Scheme + "://")
	}
	b.WriteString(u.Host)
	b.WriteString(safePath(u.Path))
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		b.WriteString("?<omitted>")
	}
	return b.String()
}

// logSafe renders a base_url value that failed to parse.
func logSafe(raw string) string {
	s, cut := raw, false
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s, cut = s[:i], true
	}
	s = redactAuthority(s)
	// Split off the path: it starts at the first "/" after the scheme.
	start := 0
	if i := strings.Index(s, "://"); i >= 0 {
		start = i + len("://")
	}
	if slash := strings.Index(s[start:], "/"); slash >= 0 {
		s = s[:start+slash] + safePath(s[start+slash:])
	}
	if cut {
		s += "?<omitted>"
	}
	return s
}

// describeAPIURL renders an already-normalized Web API root for the startup
// log line, by the same rule as describeURL.
func describeAPIURL(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil {
		return logSafe(apiURL)
	}
	return describeURL(u)
}

// safePath applies the path part of the rule above.
func safePath(p string) string {
	switch {
	case p == "" || p == "/" || p == "/api" || p == "/api/":
		return p
	case strings.HasSuffix(p, "/api") || strings.HasSuffix(p, "/api/"):
		return "/<omitted>/api/"
	default:
		return "/<path omitted>"
	}
}

// redactAuthority masks the authority of an unparseable URL: everything
// between the scheme and the next "/". It deliberately leaves the path alone,
// so it must only be used through logSafe, which collapses the path with
// safePath. A malformed password with an unescaped "/" therefore ends up
// split between the masked authority and a path that safePath hides, and an
// "@" in the path is never mistaken for the end of user info.
func redactAuthority(raw string) string {
	start := 0
	if i := strings.Index(raw, "://"); i >= 0 {
		start = i + len("://")
	}
	rest := raw[start:]
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
