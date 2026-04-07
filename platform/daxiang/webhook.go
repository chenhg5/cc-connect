package daxiang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	thrift "github.com/apache/thrift/lib/go/thrift"
	xmopencallback "github.com/chenhg5/cc-connect/platform/daxiang/internal/xmopencallback"

	"github.com/chenhg5/cc-connect/core"
)

func parseCallbackEvent(raw []byte) (callbackEvent, error) {
	var evt callbackEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return callbackEvent{}, fmt.Errorf("daxiang: parse callback event: %w", err)
	}
	return evt, nil
}

func callbackMessageTime(evt callbackEvent) time.Time {
	return time.UnixMilli(evt.Data.CTS)
}

var (
	errUnsupportedEventType   = errors.New("unsupported callback event type")
	errUnsupportedMessageType = errors.New("unsupported callback message type")
)

func normalizeInboundMessage(evt callbackEvent) (*core.Message, error) {
	if evt.EventTypeEnum != robotSingleChatMessage {
		return nil, fmt.Errorf("%w: %s", errUnsupportedEventType, evt.EventTypeEnum)
	}
	if evt.Data.Type != 1 {
		return nil, fmt.Errorf("%w: %d", errUnsupportedMessageType, evt.Data.Type)
	}

	text, err := decodeTextMessage(evt.Data.Message)
	if err != nil {
		return nil, fmt.Errorf("daxiang: decode text message: %w", err)
	}

	rc := replyContext{
		chatID:         evt.Data.ChatID,
		messageID:      evt.Data.MsgID,
		senderID:       evt.Data.FromUID,
		conversationID: evt.Data.ConversationID,
		isDirect:       true,
	}

	return &core.Message{
		SessionKey: evt.Data.ConversationID,
		Platform:   "daxiang",
		MessageID:  strconv.FormatInt(evt.Data.MsgID, 10),
		UserID:     strconv.FormatInt(evt.Data.FromUID, 10),
		UserName:   evt.Data.FromName,
		ChatName:   evt.Data.ConversationID,
		Content:    text,
		ReplyCtx:   rc,
	}, nil
}

// callbackService implements xmopencallback.Iface and bridges incoming Thrift
// callback calls to Platform.handleCallbackEvent.
type callbackService struct {
	platform *Platform
}

// EventCallback is invoked by the Thrift server for each incoming push event.
// eventType is the numeric Daxiang event code; jsonEvent is the JSON payload.
func (s *callbackService) EventCallback(ctx context.Context, eventType int32, jsonEvent string) (*xmopencallback.EmptyResp, error) {
	evt, err := parseCallbackEvent([]byte(jsonEvent))
	if err != nil {
		return nil, fmt.Errorf("daxiang: EventCallback parse: %w", err)
	}
	if err := s.platform.handleCallbackEvent(evt); err != nil {
		return nil, err
	}
	return &xmopencallback.EmptyResp{Status: &xmopencallback.RespStatus{Code: 0, Msg: "ok"}}, nil
}

// startCallbackServer starts the Thrift TCP server on p.callbackAddr.
// It performs the Listen step synchronously so that callers know the port is
// bound before the function returns, then runs the accept loop in the
// background. The server is stored in p.thriftServer so Stop() can shut it down.
func (p *Platform) startCallbackServer() error {
	serverTransport, err := thrift.NewTServerSocket(p.callbackAddr)
	if err != nil {
		return fmt.Errorf("daxiang: create server socket %s: %w", p.callbackAddr, err)
	}

	processor := xmopencallback.NewXmOpenCallbackServiceIProcessor(&callbackService{platform: p})
	transportFactory := thrift.NewTTransportFactory()
	protocolFactory := thrift.NewTBinaryProtocolFactoryConf(nil)
	server := thrift.NewTSimpleServer4(processor, serverTransport, transportFactory, protocolFactory)

	// Listen synchronously so that the port is bound before we return.
	if err := server.Listen(); err != nil {
		return fmt.Errorf("daxiang: listen callback addr %s: %w", p.callbackAddr, err)
	}

	p.callbackListenAddr = serverTransport.Addr().String()
	p.thriftServer = server

	go func() {
		if err := server.AcceptLoop(); err != nil {
			slog.Error("daxiang: callback thrift server exited", "error", err)
		}
	}()
	return nil
}
