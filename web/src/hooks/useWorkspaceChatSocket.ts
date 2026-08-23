import { useCallback, useEffect, useRef, useState } from 'react';
import {
  workspaceChatWebSocketURL,
  type WorkspaceChatEvent,
} from '@/api/workspaceChat';

export type WorkspaceSocketStatus = 'idle' | 'connecting' | 'connected' | 'disconnected';

interface Options {
  workspaceRef?: string;
  threadId?: string;
  onEvent: (event: WorkspaceChatEvent) => void;
}

export function useWorkspaceChatSocket({ workspaceRef, threadId, onEvent }: Options) {
  const [status, setStatus] = useState<WorkspaceSocketStatus>('idle');
  const socketRef = useRef<WebSocket | null>(null);
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!workspaceRef || !threadId) {
      setStatus('idle');
      return;
    }
    let disposed = false;
    let retryTimer: number | undefined;

    const connect = () => {
      if (disposed) return;
      setStatus('connecting');
      const socket = new WebSocket(workspaceChatWebSocketURL());
      socketRef.current = socket;
      socket.onopen = () => {
        if (disposed) return;
        setStatus('connected');
        socket.send(JSON.stringify({
          type: 'subscribe',
          request_id: crypto.randomUUID(),
          workspace_ref: workspaceRef,
          thread_id: threadId,
        }));
      };
      socket.onmessage = (message) => {
        try {
          onEventRef.current(JSON.parse(message.data) as WorkspaceChatEvent);
        } catch {
          onEventRef.current({ type: 'error', error: 'workspace_chat_invalid_event' });
        }
      };
      socket.onclose = () => {
        if (socketRef.current === socket) socketRef.current = null;
        if (!disposed) {
          setStatus('disconnected');
          retryTimer = window.setTimeout(connect, 1500);
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
  }, [workspaceRef, threadId]);

  const send = useCallback((value: Record<string, unknown>) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return false;
    socket.send(JSON.stringify(value));
    return true;
  }, []);

  return { status, send };
}
