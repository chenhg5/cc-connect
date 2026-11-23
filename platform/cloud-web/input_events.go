package cloudweb

import "context"

// ReportInjected acknowledges that this input joined an existing native turn.
func (p *Platform) ReportInjected(ctx context.Context, reply any, targetMessageID string) {
	if targetMessageID != "" {
		p.inputReceipt(ctx, reply, "input_injected", targetMessageID, "", false)
	}
}
