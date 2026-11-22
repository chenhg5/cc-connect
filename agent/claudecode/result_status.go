package claudecode

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

func sdkResultStatus(raw map[string]any) core.ResultStatus {
	isError, hasFlag := raw["is_error"].(bool)
	subtype := resultSubtype(raw)
	// An API error can still carry subtype=success. Explicit failure wins.
	if isError || subtype == "error" || strings.HasPrefix(subtype, "error_") {
		return core.ResultStatusError
	}
	if hasFlag && !isError && subtype == "success" {
		return core.ResultStatusSuccess
	}
	// Keep older result streams working without claiming verified success.
	return core.ResultStatusUnknown
}

var sdkHTTPError = regexp.MustCompile(`(?i)^api error:\s*([1-5][0-9]{2})\b`)

func sdkResultError(raw map[string]any) error {
	// Only recognized, bounded reasons reach users or logs. Provider payloads
	// can echo credentials, so do not append any unrecognized text.
	content, _ := raw["result"].(string)
	message := "claude code returned an error result"
	status, valid := raw["api_error_status"].(float64)
	if !valid || status < 100 || status >= 600 || status != float64(int(status)) {
		status = 0
		if match := sdkHTTPError.FindStringSubmatch(strings.TrimSpace(content)); match != nil {
			code, _ := strconv.Atoi(match[1]) // exactly three digits, bounded by the pattern
			status = float64(code)
		}
	}
	if status != 0 {
		message += fmt.Sprintf(" (HTTP %d)", int(status))
	}
	lower := strings.ToLower(content)
	switch {
	case strings.Contains(lower, "prompt is too long"), strings.Contains(lower, "prompt too long"):
		message += ": Prompt is too long"
	case strings.Contains(lower, "credit balance is too low"), strings.Contains(lower, "credit balance too low"):
		message += ": Credit balance is too low"
	}
	return errors.New(message)
}
