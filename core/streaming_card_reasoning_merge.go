package core

import "strings"

// isCumulativeReasoning reports whether current is a later, more complete
// snapshot of previous. Some agents emit the whole reasoning buffer again
// after each token/step, so a later snapshot can contain an earlier one
// rather than being byte-for-byte identical. Shared by mergeCardStepText
// (streaming card panel lanes) and BuildStreamingCardPayload (progress card
// thinking entries) so the cumulative-snapshot rule lives in one place.
func isCumulativeReasoning(previous, current string) bool {
	previous = strings.TrimSpace(previous)
	current = strings.TrimSpace(current)
	if previous == "" || len(current) <= len(previous) {
		return false
	}
	if strings.HasPrefix(current, previous) {
		return true
	}
	// Models may revise the punctuation at the snapshot boundary while
	// continuing the same sentence ("claims." -> "claims against...").
	trimmed := strings.TrimRight(previous, ".,;:!?…。；：！？")
	return len(trimmed) >= 20 && strings.HasPrefix(current, trimmed)
}

// mergeCardStepText collapses cumulative reasoning snapshots. Some agents emit
// the whole reasoning buffer again after each token/step, so a later snapshot
// can contain an earlier one rather than being byte-for-byte identical. Keep
// the longest snapshot for that lane position; never keep the shorter prefix.
func mergeCardStepText(lane []string, text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return lane
	}
	for i, item := range lane {
		old := strings.TrimSpace(item)
		if old == text {
			return lane
		}
		if isCumulativeReasoning(old, text) {
			lane[i] = text
			return lane
		}
		if strings.HasPrefix(old, text) {
			return lane
		}
	}
	return append(lane, text)
}
