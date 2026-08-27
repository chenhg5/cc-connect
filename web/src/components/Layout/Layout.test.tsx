import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getStatus } from '@/api/status';
import i18n from '@/i18n';
import Layout from './Layout';

vi.mock('@/api/status', () => ({
  getStatus: vi.fn(),
}));

function renderLayout() {
  render(
    <MemoryRouter>
      <Routes>
        <Route element={<Layout />}>
          <Route index element={<div>Dashboard content</div>} />
          <Route path="projects" element={<div>Projects content</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe('Layout responsive navigation', () => {
  beforeEach(async () => {
    vi.mocked(getStatus).mockResolvedValue({ version: 'v-test' } as Awaited<ReturnType<typeof getStatus>>);
    await i18n.changeLanguage('en');
  });

  it('keeps content full-width on mobile and exposes navigation as a closeable drawer', () => {
    renderLayout();

    const navigation = screen.getByRole('complementary', { name: 'Navigation' });
    const main = screen.getByRole('main');
    expect(navigation.className).toContain('-translate-x-full');
    expect(navigation.className).toContain('md:static');
    expect(navigation.className).toContain('md:translate-x-0');
    expect(main.className).toContain('p-3');
    expect(main.className).toContain('md:p-6');

    fireEvent.click(screen.getByRole('button', { name: 'Open navigation' }));
    expect(navigation.className).toContain('translate-x-0');
    expect(navigation.className).not.toContain('-translate-x-full');

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(navigation.className).toContain('-translate-x-full');

    fireEvent.click(screen.getByRole('button', { name: 'Open navigation' }));
    fireEvent.click(screen.getByRole('link', { name: 'Projects' }));
    expect(navigation.className).toContain('-translate-x-full');
    expect(screen.getByText('Projects content')).toBeTruthy();
  });
});
