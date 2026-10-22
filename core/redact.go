package core

import "strings"

// RedactEnv returns a copy of env with values of sensitive keys masked.
// Only env vars whose key contains a sensitive substring are redacted.
func RedactEnv(env []string) []string {
	sensitiveKeys := []string{
		"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL",
	}
	out := make([]string, len(env))
	for i, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			out[i] = e
			continue
		}
		key := strings.ToUpper(e[:idx])
		redact := false
		for _, s := range sensitiveKeys {
			if strings.Contains(key, s) {
				redact = true
				break
			}
		}
		if redact {
			out[i] = e[:idx+1] + "***"
		} else {
			out[i] = e
		}
	}
	return out
}

// RedactArgs returns a copy of args with values after sensitive flag names masked.
// Sensitive flags: --api-key, --api_key, --token, --secret, -k, etc.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)

	sensitiveFlags := []string{
		"--api-key", "--api_key", "--apikey",
		"--token", "--secret", "--password",
		"-k",
	}

	for i := 0; i < len(out); i++ {
		arg := strings.ToLower(out[i])

		// --flag=value format
		for _, f := range sensitiveFlags {
			if strings.HasPrefix(arg, f+"=") {
				out[i] = out[i][:strings.Index(out[i], "=")+1] + "***"
				break
			}
		}

		// --flag value format
		for _, f := range sensitiveFlags {
			if arg == f && i+1 < len(out) {
				out[i+1] = "***"
				i++
				break
			}
		}
	}
	return out
}
