import { render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ProviderList from './ProviderList';

const mocks = vi.hoisted(() => ({
  listGlobalProviders: vi.fn(),
}));

vi.mock('@/api/providers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/providers')>();
  return { ...actual, listGlobalProviders: mocks.listGlobalProviders };
});

describe('ProviderList', () => {
  beforeEach(() => {
    mocks.listGlobalProviders.mockResolvedValue({
      providers: [{ name: 'browser-validation-provider' }],
    });
  });

  it('为服务商编辑和删除图标提供可访问名称', async () => {
    const view = render(<ProviderList />);

    expect((await view.findByRole('button', { name: 'Edit Provider' })).getAttribute('title')).toBe('Edit Provider');
    expect(view.getByRole('button', { name: 'Delete' }).getAttribute('title')).toBe('Delete');
  });
});
