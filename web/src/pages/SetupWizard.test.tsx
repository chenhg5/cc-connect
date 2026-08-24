import { fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import SetupWizard from './SetupWizard';

const mocks = vi.hoisted(() => ({
  dashboard: vi.fn(),
  pairing: vi.fn(),
  configure: vi.fn(),
  saveURL: vi.fn(),
}));

vi.mock('@/api/control', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/control')>();
  return {
    ...actual,
    getControlDashboard: mocks.dashboard,
    createPairingCode: mocks.pairing,
    configureServer: mocks.configure,
    savePublicURL: mocks.saveURL,
  };
});

describe('SetupWizard', () => {
  beforeEach(() => {
    mocks.dashboard.mockResolvedValue({
      service: { running: false },
      devices: [{ id: 'device-1', name: 'Mac', paired_at: new Date().toISOString(), online: true }],
      runs: [], runtime_updates: [], runtime_contract_hash: 'contract', control_schema: 4,
      current_release_tag: 'v0.1.0', configured: false, public_url: 'https://cc.example.com', workspace_count: 2,
    });
    mocks.pairing.mockResolvedValue({ code: 'pair-code', expires_at: new Date(Date.now() + 60_000).toISOString() });
    mocks.configure.mockResolvedValue({ running: true });
    mocks.saveURL.mockResolvedValue({ public_url: 'https://cc.example.com' });
  });

  it('生成指向 control 内置安装器且锁定当前 Release 的配对命令', async () => {
    const view = render(<MemoryRouter><SetupWizard /></MemoryRouter>);
    fireEvent.click(await view.findByRole('button', { name: 'Create pairing code' }));
    await waitFor(() => expect(mocks.pairing).toHaveBeenCalled());
    const command = await view.findByText(/runtime\/v1\/install\.sh/);
    expect(command.textContent).toContain("--tag 'v0.1.0'");
    expect(view.getByText('Validate Codex and project catalog')).toBeTruthy();
    expect(view.getAllByText('Generate configuration and start').length).toBeGreaterThan(0);
  });
});
