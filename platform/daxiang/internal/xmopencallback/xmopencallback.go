// Package xmopencallback contains minimal stubs for the Daxiang open-callback
// Thrift service. Only the subset needed by the cc-connect daxiang platform is
// implemented here; full codegen is not required for the current integration.
package xmopencallback

import (
	"context"
	"fmt"

	thrift "github.com/apache/thrift/lib/go/thrift"
)

// RespStatus mirrors the Thrift-defined RespStatus struct.
type RespStatus struct {
	Code int32
	Msg  string
}

// EmptyResp mirrors the Thrift-defined EmptyResp struct which wraps a status.
type EmptyResp struct {
	Status *RespStatus
}

// Iface is the service interface that callers must implement to handle
// incoming Daxiang callback events.
type Iface interface {
	// EventCallback is called by Daxiang for every push event.
	// eventType is the numeric event code; jsonEvent is the JSON-encoded payload.
	EventCallback(ctx context.Context, eventType int32, jsonEvent string) (*EmptyResp, error)
}

func writeRespStatus(ctx context.Context, out thrift.TProtocol, status *RespStatus) error {
	if err := out.WriteStructBegin(ctx, "RespStatus"); err != nil {
		return err
	}
	if status != nil {
		if err := out.WriteFieldBegin(ctx, "code", thrift.I32, 1); err != nil {
			return err
		}
		if err := out.WriteI32(ctx, status.Code); err != nil {
			return err
		}
		if err := out.WriteFieldEnd(ctx); err != nil {
			return err
		}
		if status.Msg != "" {
			if err := out.WriteFieldBegin(ctx, "msg", thrift.STRING, 2); err != nil {
				return err
			}
			if err := out.WriteString(ctx, status.Msg); err != nil {
				return err
			}
			if err := out.WriteFieldEnd(ctx); err != nil {
				return err
			}
		}
	}
	if err := out.WriteFieldStop(ctx); err != nil {
		return err
	}
	return out.WriteStructEnd(ctx)
}

func writeEmptyResp(ctx context.Context, out thrift.TProtocol, resp *EmptyResp) error {
	if err := out.WriteStructBegin(ctx, "EmptyResp"); err != nil {
		return err
	}
	if resp != nil && resp.Status != nil {
		if err := out.WriteFieldBegin(ctx, "status", thrift.STRUCT, 1); err != nil {
			return err
		}
		if err := writeRespStatus(ctx, out, resp.Status); err != nil {
			return err
		}
		if err := out.WriteFieldEnd(ctx); err != nil {
			return err
		}
	}
	if err := out.WriteFieldStop(ctx); err != nil {
		return err
	}
	return out.WriteStructEnd(ctx)
}

// eventCallbackProcessorFunc implements thrift.TProcessorFunction for the
// eventCallback method.

// eventCallbackProcessorFunc implements thrift.TProcessorFunction for the
// eventCallback method.
type eventCallbackProcessorFunc struct {
	handler Iface
}

func (f *eventCallbackProcessorFunc) Process(ctx context.Context, seqID int32, in, out thrift.TProtocol) (bool, thrift.TException) {
	// Read args struct
	if _, err := in.ReadStructBegin(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}
	var eventType int32
	var jsonEvent string
	for {
		_, ftype, fid, err := in.ReadFieldBegin(ctx)
		if err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
		if ftype == thrift.STOP {
			break
		}
		switch fid {
		case 1: // eventType: i32
			if ftype == thrift.I32 {
				v, err := in.ReadI32(ctx)
				if err != nil {
					return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
				}
				eventType = v
			} else {
				if err := in.Skip(ctx, ftype); err != nil {
					return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
				}
			}
		case 2: // jsonEvent: string
			if ftype == thrift.STRING {
				v, err := in.ReadString(ctx)
				if err != nil {
					return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
				}
				jsonEvent = v
			} else {
				if err := in.Skip(ctx, ftype); err != nil {
					return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
				}
			}
		default:
			if err := in.Skip(ctx, ftype); err != nil {
				return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
			}
		}
		if err := in.ReadFieldEnd(ctx); err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
	}
	if err := in.ReadStructEnd(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}
	if err := in.ReadMessageEnd(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}

	// Invoke handler
	result, handlerErr := f.handler.EventCallback(ctx, eventType, jsonEvent)

	// Write response
	if err := out.WriteMessageBegin(ctx, "eventCallback", thrift.REPLY, seqID); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}
	if handlerErr != nil {
		x := thrift.NewTApplicationException(thrift.INTERNAL_ERROR, handlerErr.Error())
		if err := x.Write(ctx, out); err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
	} else {
		if err := out.WriteStructBegin(ctx, "eventCallback_result"); err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
		if result != nil {
			if err := out.WriteFieldBegin(ctx, "success", thrift.STRUCT, 0); err != nil {
				return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
			}
			if err := writeEmptyResp(ctx, out, result); err != nil {
				return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
			}
			if err := out.WriteFieldEnd(ctx); err != nil {
				return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
			}
		}
		if err := out.WriteFieldStop(ctx); err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
		if err := out.WriteStructEnd(ctx); err != nil {
			return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
		}
	}
	if err := out.WriteMessageEnd(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}
	if err := out.Flush(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.INVALID_DATA, err)
	}
	return true, nil
}

// XmOpenCallbackServiceIProcessor adapts an Iface implementation to the
// thrift.TProcessor interface so it can be served by a TSimpleServer.
type XmOpenCallbackServiceIProcessor struct {
	handler      Iface
	processorMap map[string]thrift.TProcessorFunction
}

// NewXmOpenCallbackServiceIProcessor wraps handler so it can be passed to
// thrift.NewTSimpleServer4 (or similar).
func NewXmOpenCallbackServiceIProcessor(handler Iface) *XmOpenCallbackServiceIProcessor {
	p := &XmOpenCallbackServiceIProcessor{
		handler:      handler,
		processorMap: make(map[string]thrift.TProcessorFunction),
	}
	p.processorMap["eventCallback"] = &eventCallbackProcessorFunc{handler: handler}
	return p
}

// ProcessorMap returns the internal method-to-function map.
func (p *XmOpenCallbackServiceIProcessor) ProcessorMap() map[string]thrift.TProcessorFunction {
	return p.processorMap
}

// AddToProcessorMap adds or replaces a TProcessorFunction at the given key.
func (p *XmOpenCallbackServiceIProcessor) AddToProcessorMap(key string, fn thrift.TProcessorFunction) {
	p.processorMap[key] = fn
}

// Process implements thrift.TProcessor. It reads one method call from in,
// dispatches to the handler, and writes the result to out.
func (p *XmOpenCallbackServiceIProcessor) Process(ctx context.Context, in, out thrift.TProtocol) (bool, thrift.TException) {
	name, _, seqID, err := in.ReadMessageBegin(ctx)
	if err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	if fn, ok := p.processorMap[name]; ok {
		return fn.Process(ctx, seqID, in, out)
	}
	if err := in.Skip(ctx, thrift.STRUCT); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	if err := in.ReadMessageEnd(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	x := thrift.NewTApplicationException(thrift.UNKNOWN_METHOD, fmt.Sprintf("unknown function: %s", name))
	if err := out.WriteMessageBegin(ctx, name, thrift.EXCEPTION, seqID); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	if err := x.Write(ctx, out); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	if err := out.WriteMessageEnd(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	if err := out.Flush(ctx); err != nil {
		return false, thrift.NewTProtocolExceptionWithType(thrift.UNKNOWN_METHOD, err)
	}
	return false, nil
}
