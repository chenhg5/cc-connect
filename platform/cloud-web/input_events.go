package cloudweb

import "context"

// ReportInjected acknowledges that this input joined an existing native turn.
func (p *Platform) ReportInjected(ctx context.Context, reply any, targetMessageID string) {
	if targetMessageID == "" {
		// Delivery succeeded, but there is no turn ID for an injected
		// receipt. Do not recover the already-delivered input as uncertain.
		p.ReportInputStarted(ctx, reply)
		return
	}
	p.inputReceipt(ctx, reply, "input_injected", targetMessageID, "", false)
}
