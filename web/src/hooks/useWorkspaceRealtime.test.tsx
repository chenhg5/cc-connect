import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { WorkspaceChatClientRequest } from '@/api/workspaceChat';
import { useWorkspaceRealtime } from './useWorkspaceRealtime';

class FakeTrack {
  stop = vi.fn();
}

class FakeMediaStream {
  readonly track = new FakeTrack();
  getAudioTracks() { return [this.track] as unknown as MediaStreamTrack[]; }
  getTracks() { return [this.track] as unknown as MediaStreamTrack[]; }
}

class FakePeerConnection {
  static instances: FakePeerConnection[] = [];
  iceGatheringState: RTCIceGatheringState = 'complete';
  connectionState: RTCPeerConnectionState = 'new';
  localDescription: RTCSessionDescriptionInit | null = null;
  ontrack: ((event: RTCTrackEvent) => void) | null = null;
  onconnectionstatechange: (() => void) | null = null;
  close = vi.fn();
  addTrack = vi.fn();
  createDataChannel = vi.fn();
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  setRemoteDescription = vi.fn(async () => undefined);

  constructor() { FakePeerConnection.instances.push(this); }
  async createOffer() { return { type: 'offer' as RTCSdpType, sdp: 'local-sdp' }; }
  async setLocalDescription(value: RTCSessionDescriptionInit) { this.localDescription = value; }
}

function Harness({
  send,
  conversationID = 'thread-1',
  failedRequestID,
}: {
  send: (request: WorkspaceChatClientRequest) => boolean;
  conversationID?: string;
  failedRequestID?: string;
}) {
  const realtime = useWorkspaceRealtime({
    workspaceRef: 'workspace-1',
    conversation: { kind: 'thread', id: conversationID },
    supported: true,
    send,
  });
  return (
    <>
      <span>{realtime.status}</span>
      <button type="button" onClick={() => void realtime.start({ voice: 'alloy', version: 'v2' })}>start</button>
      <button type="button" onClick={() => realtime.handleNativeEvent({ method: 'thread/realtime/sdp', payload: { sdp: 'remote-sdp' }, occurred_at: 'now' })}>answer</button>
      <button type="button" onClick={realtime.stop}>stop</button>
      <button type="button" onClick={() => realtime.handleRequestError(failedRequestID, 'realtime start rejected')}>request error</button>
      <audio ref={realtime.audioRef} />
    </>
  );
}

describe('useWorkspaceRealtime', () => {
  it('完成 WebRTC 信令并在停止时释放 peer 和麦克风', async () => {
    FakePeerConnection.instances = [];
    const stream = new FakeMediaStream();
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection);
    vi.stubGlobal('MediaStream', FakeMediaStream);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(async () => stream) },
    });
    const send = vi.fn((_request: WorkspaceChatClientRequest) => true);
    const view = render(<Harness send={send} />);

    fireEvent.click(view.getByText('start'));
    await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({
      type: 'realtime_start',
      sdp: 'local-sdp',
      voice: 'alloy',
      version: 'v2',
      conversation: { kind: 'thread', id: 'thread-1' },
    })));

    fireEvent.click(view.getByText('answer'));
    await waitFor(() => expect(view.getByText('connected')).toBeTruthy());
    expect(FakePeerConnection.instances[0].setRemoteDescription).toHaveBeenCalledWith({ type: 'answer', sdp: 'remote-sdp' });

    act(() => fireEvent.click(view.getByText('stop')));
    expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_stop' }));
    expect(FakePeerConnection.instances[0].close).toHaveBeenCalled();
    expect(stream.track.stop).toHaveBeenCalled();
  });

  it('连接失败后停止服务端 realtime', async () => {
    FakePeerConnection.instances = [];
    const stream = new FakeMediaStream();
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(async () => stream) },
    });
    const send = vi.fn(() => true);
    const view = render(<Harness send={send} />);

    fireEvent.click(view.getByText('start'));
    await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_start' })));
    const peer = FakePeerConnection.instances[0];
    peer.connectionState = 'failed';
    act(() => peer.onconnectionstatechange?.());

    expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_stop' }));
    expect(peer.close).toHaveBeenCalled();
    expect(stream.track.stop).toHaveBeenCalled();
  });

  it('卸载时停止已启动的服务端 realtime', async () => {
    FakePeerConnection.instances = [];
    const stream = new FakeMediaStream();
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(async () => stream) },
    });
    const send = vi.fn(() => true);
    const view = render(<Harness send={send} />);

    fireEvent.click(view.getByText('start'));
    await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_start' })));
    view.unmount();

    expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_stop' }));
    expect(stream.track.stop).toHaveBeenCalled();
  });

  it('切换会话后丢弃尚未返回的麦克风授权并停止旧媒体', async () => {
    FakePeerConnection.instances = [];
    const stream = new FakeMediaStream();
    let resolveMedia!: (value: MediaStream) => void;
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(() => new Promise<MediaStream>((resolve) => { resolveMedia = resolve; })) },
    });
    const send = vi.fn(() => true);
    const view = render(<Harness send={send} />);

    fireEvent.click(view.getByText('start'));
    await waitFor(() => expect(view.getByText('requesting_microphone')).toBeTruthy());
    view.rerender(<Harness send={send} conversationID="thread-2" />);
    await act(async () => resolveMedia(stream as unknown as MediaStream));

    await waitFor(() => expect(view.getByText('idle')).toBeTruthy());
    expect(stream.track.stop).toHaveBeenCalled();
    expect(FakePeerConnection.instances).toHaveLength(0);
    expect(send).not.toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_start' }));
  });

  it('按 realtime_start 的 request_id 收口异步服务端错误并释放资源', async () => {
    FakePeerConnection.instances = [];
    const stream = new FakeMediaStream();
    vi.stubGlobal('RTCPeerConnection', FakePeerConnection);
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(async () => stream) },
    });
    const send = vi.fn((_request: WorkspaceChatClientRequest) => true);
    const view = render(<Harness send={send} />);

    fireEvent.click(view.getByText('start'));
    await waitFor(() => expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: 'realtime_start' })));
    const startRequest = send.mock.calls.find(([request]) => request.type === 'realtime_start')?.[0];
    view.rerender(<Harness send={send} failedRequestID={startRequest?.request_id} />);
    fireEvent.click(view.getByText('request error'));

    await waitFor(() => expect(view.getByText('idle')).toBeTruthy());
    expect(FakePeerConnection.instances[0].close).toHaveBeenCalled();
    expect(stream.track.stop).toHaveBeenCalled();
  });
});
