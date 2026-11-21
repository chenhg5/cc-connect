package pi

import (
	"context"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.BusySteerSender = (*piSession)(nil)

// JSON uses one process per prompt; RPC supports native, acknowledged steering.
func (s *piSession) SupportsBusySteer() bool { return s.rpc }

// SendBusySteer uses the same request registry, ACK parser and terminal owner as
// ordinary Send. Its caller deadline only bounds acceptance, not the model run.
func (s *piSession) SendBusySteer(ctx context.Context, text, messageID string, images []core.ImageAttachment, files []core.FileAttachment) (core.SteerResult, error) {
	if !s.rpc {
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: busy steer requires RPC mode")}
	}
	if err := ctx.Err(); err != nil {
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: err}
	}

	s.sendWg.Add(1)
	defer s.sendWg.Done()
	if !s.alive.Load() {
		return core.SteerResult{}, &core.SteerNotAcceptedError{Err: fmt.Errorf("pi: session is closed")}
	}
	cmd, err := s.prepareModernRPCPrompt(text, images, files, true)
	if err != nil {
		return core.SteerResult{}, err
	}

	return s.sendSettledRPCResult(ctx, cmd)
}
