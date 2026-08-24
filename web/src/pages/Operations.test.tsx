import { fireEvent, render, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import Operations from './Operations';

const mocks = vi.hoisted(() => ({
  dashboard: vi.fn(),
  serviceLogs: vi.fn(),
  startRun: vi.fn(),
  streamRun: vi.fn(),
  pairing: vi.fn(),
  rename: vi.fn(),
  streamDevice: vi.fn(),
}));

vi.mock('@/api/control', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/control')>();
  return {
    ...actual,
    getControlDashboard: mocks.dashboard,
    getServiceLogs: mocks.serviceLogs,
    startDeployRun: mocks.startRun,
    streamDeployRun: mocks.streamRun,
    createPairingCode: mocks.pairing,
    renameDevice: mocks.rename,
    streamDeviceLogs: mocks.streamDevice,
  };
});

describe('Operations', () => {
  beforeEach(() => {
    mocks.dashboard.mockResolvedValue({
      service: { running: true, pid: 42 }, devices: [{ id: 'device-1', name: 'Mac', paired_at: new Date().toISOString(), online: true }], runs: [],
      runtime_contract_hash: 'contract', control_schema: 4, current_release_tag: 'v0.1.0', runtime_updates: [], configured: true, public_url: 'https://cc.example.com', workspace_count: 1,
    });
    mocks.serviceLogs.mockResolvedValue([]);
    mocks.startRun.mockResolvedValue({ id: 'run-1', kind: 'update', status: 'running', target_tag: 'v0.1.0', started_at: new Date().toISOString() });
    mocks.streamRun.mockReset().mockImplementation(async (_id, _after, onLine) => {
      onLine({ sequence: 1, occurred_at: new Date().toISOString(), stream: 'control', line: '签名检查通过' });
    });
    mocks.pairing.mockResolvedValue({ code: 'pair-code', expires_at: new Date(Date.now() + 60_000).toISOString() });
    mocks.rename.mockResolvedValue({ id: 'device-1' });
    mocks.streamDevice.mockReset().mockImplementation(async (_id, _after, onLine) => {
      onLine({ id: 1, occurred_at: new Date().toISOString(), actor: 'runtime:device-1', action: 'runtime_connected', resource: 'device:device-1', outcome: 'succeeded', details: {} });
    });
  });

  it('启动更新后订阅持久化部署日志并展示实时行', async () => {
    const view = render(<Operations />);
    fireEvent.click(await view.findByRole('button', { name: 'Check and update' }));
    await waitFor(() => expect(mocks.startRun).toHaveBeenCalledWith('update'));
    await waitFor(() => expect(mocks.streamRun).toHaveBeenCalledWith('run-1', 0, expect.any(Function), expect.any(AbortSignal)));
    expect(await view.findByText(/签名检查通过/)).toBeTruthy();
  });

  it('为新增设备显示安装命令并提供重命名与连接日志', async () => {
    const view = render(<Operations />);
    fireEvent.click(await view.findByRole('button', { name: 'Pair device' }));
    expect((await view.findByText(/runtime\/v1\/install\.sh/)).textContent).toContain("--tag 'v0.1.0'");

    fireEvent.click(await view.findByRole('button', { name: 'Rename device' }));
    fireEvent.change(view.getByRole('textbox'), { target: { value: 'Studio' } });
    fireEvent.click(view.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(mocks.rename).toHaveBeenCalledWith('device-1', 'Studio'));

    fireEvent.click(view.getByRole('button', { name: 'Connection logs' }));
    await waitFor(() => expect(mocks.streamDevice).toHaveBeenCalledWith('device-1', 0, expect.any(Function), expect.any(AbortSignal)));
    expect(await view.findByText(/runtime_connected/)).toBeTruthy();
  });
});
