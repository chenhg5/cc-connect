import { useEffect, useRef, useCallback, useState } from 'react';
import api from '@/api/client';

export type BridgeIncoming =
  | { type: 'register_ack'; ok: boolean; error?: string }
  | { type: 'reply'; session_key: string; reply_ctx: string; content: string; format?: string }
  | { type: 'reply_stream'; session_key: string; reply_ctx: string; delta: string; full_text: string; preview_handle?: string; done: boolean }
  | { type: 'card'; session_key: string; reply_ctx: string; card: any }
  | { type: 'buttons'; session_key: string; reply_ctx: string; content: string; buttons: { text: string; data: string }[][] }
  | { type: 'typing_start'; session_key: string }
  | { type: 'typing_stop'; session_key: string }
  | { type: 'preview_start'; ref_id: string; session_key: string; reply_ctx: string; content: string }
  | { type: 'update_message'; session_key: string; preview_handle: string; content: string }
  | { type: 'delete_message'; session_key: string; preview_handle: string }
  | { type: 'error'; code: string; message: string }
  | { type: 'pong'; ts: number }
  | { type: string; [key: string]: any };

export interface BridgeConfig {
  port: number;
  path: string;
  token: string;
}

export type BridgeStatus = 'connecting' | 'registering' | 'connected' | 'disconnected' | 'error';

export interface UseBridgeSocketOptions {
  bridgeCfg: BridgeConfig | null;
  platformName?: string;
  sessionKey: string;
  projectName?: string;
  onMessage: (msg: BridgeIncoming) => void;
}

export function useBridgeSocket({ bridgeCfg, platformName = 'web', sessionKey, projectName, onMessage }: UseBridgeSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const onMessageRef = useRef(onMessage);
  onMessageRef.current = onMessage;
  const pingRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const [status, setStatus] = useState<BridgeStatus>('disconnected');

  const send = useCallback((data: Record<string, any>) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(data));
    }
  }, []);

  const sendMessage = useCallback((content: string) => {
    send({
      type: 'message',
      msg_id: `web-${Date.now()}`,
      session_key: sessionKey,
      user_id: 'web-admin',
      user_name: 'Web Admin',
      content,
      reply_ctx: sessionKey,
      project: projectName || '',
    });
  }, [send, sessionKey, projectName]);

  const sendCardAction = useCallback((action: string) => {
    send({
      type: 'card_action',
      session_key: sessionKey,
      action,
      reply_ctx: sessionKey,
      project: projectName || '',
    });
  }, [send, sessionKey, projectName]);

  const sendPreviewAck = useCallback((refId: string, handle: string) => {
    send({ type: 'preview_ack', ref_id: refId, preview_handle: handle });
  }, [send]);

  useEffect(() => {
    if (!bridgeCfg) return;

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    // Use current page host:port so the request goes through the Vite/nginx proxy
    // instead of directly hitting the bridge port (which may not be reachable).
    const wsUrl = `${proto}//${window.location.host}${bridgeCfg.path}?token=${encodeURIComponent(bridgeCfg.token)}`;

    let ws: WebSocket;
    let reconnectTimer: ReturnType<typeof setTimeout>;
    let alive = true;

    const connect = () => {
      if (!alive) return;
      setStatus('connecting');
      ws = new WebSocket(wsUrl);
      wsRef.current = ws;

      ws.onopen = () => {
        setStatus('registering');
        ws.send(JSON.stringify({
          type: 'register',
          platform: platformName,
          capabilities: ['text', 'card', 'buttons', 'typing', 'update_message', 'preview', 'reconstruct_reply'],
          metadata: { version: '1.0.0', description: 'Web Admin Dashboard' },
        }));
      };

      ws.onmessage = (evt) => {
        try {
          const msg = JSON.parse(evt.data) as BridgeIncoming;
          if (msg.type === 'register_ack') {
            if (msg.ok) {
              setStatus('connected');
              pingRef.current = setInterval(() => {
                send({ type: 'ping', ts: Date.now() });
              }, 25000);
            } else {
              setStatus('error');
            }
          }
          onMessageRef.current(msg);
        } catch { /* ignore parse errors */ }
      };

      ws.onclose = () => {
        setStatus('disconnected');
        wsRef.current = null;
        if (pingRef.current) clearInterval(pingRef.current);
        if (alive) reconnectTimer = setTimeout(connect, 3000);
      };

      ws.onerror = () => {
        setStatus('error');
      };
    };

    connect();

    return () => {
      alive = false;
      clearTimeout(reconnectTimer);
      if (pingRef.current) clearInterval(pingRef.current);
      if (wsRef.current) {
        wsRef.current.onclose = null;
        wsRef.current.close();
        wsRef.current = null;
      }
      setStatus('disconnected');
    };
  }, [bridgeCfg, platformName, send]);

  return { status, send, sendMessage, sendCardAction, sendPreviewAck };
}

// Fetch bridge config from the management API status endpoint.
export async function fetchBridgeConfig(): Promise<BridgeConfig | null> {
  try {
    const status = await api.get<any>('/status');
    if (status.bridge?.enabled) {
      return {
        port: status.bridge.port,
        path: status.bridge.path,
        token: status.bridge.token,
      };
    }
  } catch { /* bridge not available */ }
  return null;
}
