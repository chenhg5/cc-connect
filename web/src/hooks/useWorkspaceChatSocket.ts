import { useCallback, useEffect, useRef, useState } from 'react';
import {
  classifyWorkspaceChatServerEvent,
  isWorkspaceChatServerEvent,
  workspaceChatWebSocketURL,
  type ConversationRef,
  type WorkspaceChatClientRequest,
  type WorkspaceChatEventCursor,
  type WorkspaceChatServerEvent,
} from '@/api/workspaceChat';

export type WorkspaceSocketStatus = 'idle' | 'connecting' | 'connected' | 'disconnected';

interface Options {
  workspaceRef?: string;
  conversation?: ConversationRef;
  onEvent: (event: WorkspaceChatServerEvent) => void;
  onProtocolError?: (error: Error) => void;
}

export function useWorkspaceChatSocket({ workspaceRef, conversation, onEvent, onProtocolError }: Options) {
  const [status, setStatus] = useState<WorkspaceSocketStatus>('idle');
  const socketRef = useRef<WebSocket | null>(null);
  const cursorRef = useRef<WorkspaceChatEventCursor>({});
  const onEventRef = useRef(onEvent);
  const onProtocolErrorRef = useRef(onProtocolError);
  onEventRef.current = onEvent;
  onProtocolErrorRef.current = onProtocolError;

  useEffect(() => {
    cursorRef.current = {};
  }, [workspaceRef, conversation?.kind, conversation?.id]);

  useEffect(() => {
    if (!workspaceRef || !conversation) {
      setStatus('idle');
      return;
    }
    let disposed = false;
    let retryTimer: number | undefined;
    let retryDelay = 1000;

    const connect = () => {
      if (disposed) return;
      setStatus('connecting');
      const socket = new WebSocket(workspaceChatWebSocketURL());
      socketRef.current = socket;
      let resubscribing = false;
      const subscribe = () => {
        const cursor = cursorRef.current;
        const request: WorkspaceChatClientRequest = {
          type: 'subscribe',
          request_id: crypto.randomUUID(),
          workspace_ref: workspaceRef,
          conversation,
          ...(cursor.epoch ? { after_epoch: cursor.epoch, after_sequence: cursor.sequence } : {}),
        };
        socket.send(JSON.stringify(request));
      };
      socket.onopen = () => {
        if (disposed) return;
        retryDelay = 1000;
        setStatus('connected');
        subscribe();
      };
      socket.onmessage = (message) => {
        if (disposed || socketRef.current !== socket) return;
        try {
          const parsed: unknown = JSON.parse(String(message.data));
          if (!isWorkspaceChatServerEvent(parsed)) throw new Error('workspace_chat_invalid_event');
          const current = cursorRef.current;
          const disposition = classifyWorkspaceChatServerEvent(current, parsed);
          if (parsed.type === 'subscribed' || parsed.type === 'protocol_error') resubscribing = false;
          if (disposition === 'resync') {
            if (!resubscribing) {
              resubscribing = true;
              subscribe();
            }
            return;
          }
          if (disposition === 'ignore') return;
          if (parsed.type !== 'protocol_error') {
            cursorRef.current = { epoch: parsed.epoch, sequence: parsed.sequence, eventType: parsed.type };
          }
          onEventRef.current(parsed);
        } catch (cause) {
          onProtocolErrorRef.current?.(cause instanceof Error ? cause : new Error(String(cause)));
        }
      };
      socket.onclose = () => {
        if (socketRef.current === socket) socketRef.current = null;
        if (!disposed) {
          setStatus('disconnected');
          retryTimer = window.setTimeout(connect, retryDelay);
          retryDelay = Math.min(retryDelay * 2, 10_000);
        }
      };
      socket.onerror = () => socket.close();
    };

    connect();
    return () => {
      disposed = true;
      if (retryTimer !== undefined) window.clearTimeout(retryTimer);
      socketRef.current?.close();
      socketRef.current = null;
    };
  }, [workspaceRef, conversation?.kind, conversation?.id]);

  const send = useCallback((value: WorkspaceChatClientRequest) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return false;
    socket.send(JSON.stringify(value));
    return true;
  }, []);

  return { status, send };
}
