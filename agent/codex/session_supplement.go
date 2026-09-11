package codex

// SupportsLegacySupplement prevents /ps from starting a second exec process
// while the current turn is running. Native steering uses app-server instead.
func (cs *codexSession) SupportsLegacySupplement() bool { return false }
