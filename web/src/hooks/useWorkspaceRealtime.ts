import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  ConversationRef,
  NativeEventEnvelope,
  WorkspaceChatClientRequest,
} from '@/api/workspaceChat';

export type WorkspaceRealtimeStatus = 'idle' | 'requesting_microphone' | 'connecting' | 'connected' | 'stopping';

interface RealtimeStartOptions {
  voice?: string;
  version?: string;
}

interface Options {
  workspaceRef?: string;
  conversation?: ConversationRef;
  supported: boolean;
  send: (request: WorkspaceChatClientRequest) => boolean;
}

type RealtimeRequest =
  | { type: 'realtime_start'; sdp: string; voice?: string; version?: string }
  | { type: 'realtime_append_text'; text: string }
  | { type: 'realtime_stop' };

const asRecord = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null;

function waitForIceGathering(peer: RTCPeerConnection): Promise<void> {
  if (peer.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      peer.removeEventListener('icegatheringstatechange', handleState);
      reject(new Error('workspace_chat_realtime_ice_timeout'));
    }, 10_000);
    const handleState = () => {
      if (peer.iceGatheringState !== 'complete') return;
      window.clearTimeout(timeout);
      peer.removeEventListener('icegatheringstatechange', handleState);
      resolve();
    };
    peer.addEventListener('icegatheringstatechange', handleState);
  });
}

export function useWorkspaceRealtime({ workspaceRef, conversation, supported, send }: Options) {
  const [status, setStatus] = useState<WorkspaceRealtimeStatus>('idle');
  const [error, setError] = useState('');
  const peerRef = useRef<RTCPeerConnection | null>(null);
  const streamRef = useRef<MediaStream | null>(null);
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const serverActiveRef = useRef(false);
  const startRequestIDRef = useRef('');
  const operationGenerationRef = useRef(0);
  const conversationKind = conversation?.kind;
  const conversationID = conversation?.id;

  const releaseMedia = useCallback(() => {
    peerRef.current?.close();
    peerRef.current = null;
    streamRef.current?.getTracks().forEach((track) => track.stop());
    streamRef.current = null;
    if (audioRef.current) audioRef.current.srcObject = null;
  }, []);

  const sendBase = useCallback((request: RealtimeRequest, requestID = crypto.randomUUID()) => {
    if (!workspaceRef || !conversationKind || !conversationID) return false;
    return send({
      ...request,
      request_id: requestID,
      workspace_ref: workspaceRef,
      conversation: { kind: conversationKind, id: conversationID },
    } as WorkspaceChatClientRequest);
  }, [conversationID, conversationKind, send, workspaceRef]);

  const stopServer = useCallback(() => {
    if (!serverActiveRef.current) return;
    serverActiveRef.current = false;
    sendBase({ type: 'realtime_stop' });
  }, [sendBase]);

  const stop = useCallback(() => {
    if (serverActiveRef.current) setStatus('stopping');
    operationGenerationRef.current += 1;
    startRequestIDRef.current = '';
    stopServer();
    releaseMedia();
    setStatus('idle');
  }, [releaseMedia, stopServer]);

  const start = useCallback(async ({ voice, version }: RealtimeStartOptions = {}) => {
    if (!supported) throw new Error('workspace_chat_realtime_not_supported');
    if (!navigator.mediaDevices?.getUserMedia || typeof RTCPeerConnection === 'undefined') {
      throw new Error('workspace_chat_realtime_browser_not_supported');
    }
    operationGenerationRef.current += 1;
    const generation = operationGenerationRef.current;
    startRequestIDRef.current = '';
    stopServer();
    releaseMedia();
    setError('');
    setStatus('requesting_microphone');
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      if (generation !== operationGenerationRef.current) {
        stream.getTracks().forEach((track) => track.stop());
        return;
      }
      const peer = new RTCPeerConnection();
      streamRef.current = stream;
      peerRef.current = peer;
      stream.getAudioTracks().forEach((track) => peer.addTrack(track, stream));
      peer.createDataChannel('oai-events');
      peer.ontrack = (event) => {
        if (generation !== operationGenerationRef.current || peerRef.current !== peer) return;
        if (audioRef.current) audioRef.current.srcObject = event.streams[0] || new MediaStream([event.track]);
      };
      peer.onconnectionstatechange = () => {
        if (generation !== operationGenerationRef.current || peerRef.current !== peer) return;
        if (peer.connectionState === 'connected') setStatus('connected');
        if (peer.connectionState === 'failed') {
          setError('workspace_chat_realtime_connection_failed');
          operationGenerationRef.current += 1;
          startRequestIDRef.current = '';
          stopServer();
          releaseMedia();
          setStatus('idle');
        }
      };
      const offer = await peer.createOffer();
      if (generation !== operationGenerationRef.current) return;
      await peer.setLocalDescription(offer);
      if (generation !== operationGenerationRef.current) return;
      await waitForIceGathering(peer);
      if (generation !== operationGenerationRef.current) return;
      const sdp = peer.localDescription?.sdp;
      if (!sdp) throw new Error('workspace_chat_realtime_missing_sdp');
      setStatus('connecting');
      const requestID = crypto.randomUUID();
      startRequestIDRef.current = requestID;
      if (!sendBase({ type: 'realtime_start', sdp, voice, version }, requestID)) {
        startRequestIDRef.current = '';
        throw new Error('workspace_chat_disconnected');
      }
      serverActiveRef.current = true;
    } catch (cause) {
      if (generation !== operationGenerationRef.current) return;
      operationGenerationRef.current += 1;
      startRequestIDRef.current = '';
      stopServer();
      releaseMedia();
      setStatus('idle');
      const message = cause instanceof Error ? cause.message : String(cause);
      setError(message);
      throw cause;
    }
  }, [releaseMedia, sendBase, stopServer, supported]);

  const appendText = useCallback((text: string) => sendBase({ type: 'realtime_append_text', text }), [sendBase]);

  const handleNativeEvent = useCallback((event: NativeEventEnvelope) => {
    const payload = asRecord(event.payload) || {};
    if (event.method === 'thread/realtime/sdp') {
      const sdp = typeof payload.sdp === 'string' ? payload.sdp : '';
      const peer = peerRef.current;
      if (!peer || !sdp) return;
      const generation = operationGenerationRef.current;
      void peer.setRemoteDescription({ type: 'answer', sdp }).then(() => {
        if (generation !== operationGenerationRef.current || peerRef.current !== peer) return;
        setStatus('connected');
      }).catch((cause) => {
        if (generation !== operationGenerationRef.current || peerRef.current !== peer) return;
        setError(cause instanceof Error ? cause.message : String(cause));
        operationGenerationRef.current += 1;
        startRequestIDRef.current = '';
        stopServer();
        releaseMedia();
        setStatus('idle');
      });
      return;
    }
    if (event.method === 'thread/realtime/error') {
      const message = typeof payload.message === 'string' ? payload.message : 'workspace_chat_realtime_error';
      setError(message);
      operationGenerationRef.current += 1;
      startRequestIDRef.current = '';
      stopServer();
      releaseMedia();
      setStatus('idle');
      return;
    }
    if (event.method === 'thread/realtime/closed') {
      serverActiveRef.current = false;
      operationGenerationRef.current += 1;
      startRequestIDRef.current = '';
      const reason = typeof payload.reason === 'string' ? payload.reason : '';
      if (reason) setError(reason);
      releaseMedia();
      setStatus('idle');
    }
  }, [releaseMedia, stopServer]);

  const handleRequestError = useCallback((requestID: string | undefined, message: string) => {
    if (!requestID || requestID !== startRequestIDRef.current) return false;
    startRequestIDRef.current = '';
    serverActiveRef.current = false;
    operationGenerationRef.current += 1;
    releaseMedia();
    setError(message || 'workspace_chat_realtime_error');
    setStatus('idle');
    return true;
  }, [releaseMedia]);

  useEffect(() => {
    setError('');
    setStatus('idle');
    return () => {
      operationGenerationRef.current += 1;
      startRequestIDRef.current = '';
      stopServer();
      releaseMedia();
    };
  }, [conversationID, conversationKind, releaseMedia, stopServer]);

  useEffect(() => {
    if (!supported && status !== 'idle') stop();
  }, [status, stop, supported]);

  return { status, error, audioRef, start, stop, appendText, handleNativeEvent, handleRequestError };
}
