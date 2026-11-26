package core

import "net/url"

// RedactURL keeps only a URL's scheme and host for diagnostics. Paths, query
// values, userinfo and fragments can all contain credentials.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return "[invalid URL]"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}
