package claudecode

import (
	"errors"
	"fmt"
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

func sdkResultError(raw map[string]any) error {
	// Do not put the raw SDK payload or provider response into runtime logs.
	// Content remains on the Event for callers that need the original result.
	if status, ok := raw["api_error_status"].(float64); ok && status >= 100 && status < 600 && status == float64(int(status)) {
		return fmt.Errorf("claude code returned an error result (HTTP %d)", int(status))
	}
	return errors.New("claude code returned an error result")
}
