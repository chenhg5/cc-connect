package cloudweb

import "context"

type inboundHandler func(raw []byte)

type transport interface {
	Start(ctx context.Context, onInbound inboundHandler) error
	Stop() error
	Send(ctx context.Context, msg map[string]any) error
	Capabilities() map[string]bool
}
