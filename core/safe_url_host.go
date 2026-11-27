package core

import "net/url"

// SafeURLHost returns only the host of a URL for diagnostics. Paths, userinfo,
// query values and fragments may contain credentials and must not be logged.
func SafeURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Hostname() == "" {
		return "unknown"
	}
	return u.Hostname()
}
