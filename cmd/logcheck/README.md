# Direct payload log check

`go run ./cmd/logcheck .` scans non-test Go files and fails on direct uses of `body`, `raw`, `respBody`, `responseBody`, `payload`, `content`, `params`, or `text` in `slog`, `log`, or `fmt.Errorf` calls. It also catches `string(...)`, concatenation, `fmt.Sprint`, `fmt.Sprintf`, and `slog` attribute wrappers. Lengths, types (`%T`), and explicit safe helper calls are accepted.

The check is deliberately syntactic. It cannot trace a sensitive value after renaming or through arbitrary helper calls, and it does not inspect frontend `console.*`. Runtime sentinel tests and code review remain required. No directory or call-site exceptions are configured.
