import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { createInstance } from 'i18next';
import { createElement as h, createRef } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import { createServer } from 'vite';

const webRoot = fileURLToPath(new URL('../../../', import.meta.url));
const drawerId = 'app-sidebar-drawer';
const i18n = createInstance();
let server, Layout, Sidebar, Header;

function readLocale(code) {
  return JSON.parse(readFileSync(new URL(`../../i18n/locales/${code}.json`, import.meta.url), 'utf8'));
}

before(async () => {
  await i18n.init({ lng: 'en', showSupportNotice: false, resources: { en: { translation: readLocale('en') } } });
  server = await createServer({
    root: webRoot,
    // Use the same React Router module and context as Node's MemoryRouter.
    ssr: { resolve: { externalConditions: ['node', 'module-sync'] } },
    server: { middlewareMode: true, hmr: false, ws: false, watch: null },
    optimizeDeps: { noDiscovery: true, include: [] },
    appType: 'custom',
    logLevel: 'silent',
  });
  [Layout, Sidebar, Header] = await Promise.all(['Layout', 'Sidebar', 'Header'].map(async (name) =>
    (await server.ssrLoadModule(`/src/components/Layout/${name}.tsx`)).default
  ));
});

after(async () => { await server?.close(); });

function render(Component, props) {
  return renderToStaticMarkup(h(I18nextProvider, { i18n }, h(MemoryRouter, null, h(Component, props))));
}

// Inspect React's serialized opening tags so each assertion targets one element.
function tag(html, name, attribute, value) {
  const tags = html.match(new RegExp(`<${name}\\b[^>]*>`, 'g')) ?? [];
  const result = tags.find((item) => !attribute || item.includes(` ${attribute}="${value}"`));
  assert.ok(result, `Missing <${name}> with ${attribute ?? 'tag name'} ${value ?? name}`);
  return result;
}

function classes(element) {
  return element.match(/ class="([^"]*)"/)?.[1].split(/\s+/) ?? [];
}

function hasClasses(element, expected) {
  const actual = classes(element);
  for (const token of expected.split(' ')) {
    assert.ok(actual.includes(token), `Missing class ${token} on ${element}`);
  }
}

test('Layout starts with a closed mobile drawer and an accessible content area', () => {
  const html = render(Layout);
  const aside = tag(html, 'aside', 'id', drawerId);
  hasClasses(aside, 'fixed inset-y-0 left-0 z-[70] -translate-x-full invisible pointer-events-none transition-[transform,visibility]');
  hasClasses(aside, 'md:static md:z-auto md:translate-x-0 md:visible md:pointer-events-auto md:transition-all md:duration-300 md:ease-out md:w-56');
  assert.doesNotMatch(aside, / (?:role="dialog"|aria-modal=)/);

  hasClasses(tag(html, 'div', 'aria-hidden', 'true'), 'md:hidden z-[60] touch-none opacity-0 pointer-events-none');
  const content = html.match(/(<div\b[^>]*>)<header\b/)?.[1];
  assert.ok(content, 'Header should be inside the content container');
  assert.doesNotMatch(content, / inert(?:[\s=>])/);
  hasClasses(tag(html, 'main'), 'p-4 md:p-6');

  const header = html.match(/<header\b[^>]*>[\s\S]*?<\/header>/)?.[0];
  assert.ok(header);
  const trigger = tag(header, 'button', 'aria-label', 'Open menu');
  assert.match(trigger, / aria-expanded="false"/);
  assert.ok(trigger.includes(` aria-controls="${drawerId}"`));
  const parent = header.slice(0, header.indexOf(trigger)).trimEnd().match(/<[\w-]+\b[^>]*>$/)?.[0];
  assert.ok(
    classes(trigger).includes('md:hidden') || classes(parent ?? '').includes('md:hidden'),
    'Expected md:hidden on the menu button or its parent',
  );
});

test('open Sidebar exposes the dialog, mobile close button, and navigation links', () => {
  const html = render(Sidebar, { id: drawerId, mobileOpen: true, onClose() {}, onNavigate() {} });
  const aside = tag(html, 'aside', 'id', drawerId);
  assert.match(aside, / role="dialog"/);
  assert.match(aside, / aria-modal="true"/);
  assert.match(aside, / aria-label="Navigation"/);
  hasClasses(aside, 'translate-x-0 visible transition-transform');
  assert.ok(!classes(aside).includes('transition-[transform,visibility]'));
  hasClasses(tag(html, 'button', 'aria-label', 'Close menu'), 'md:hidden');

  const collapseWrapper = html.match(/<\/nav>(<div\b[^>]*>)<button\b/)?.[1];
  assert.ok(collapseWrapper, 'Expected the desktop collapse button wrapper after navigation');
  hasClasses(collapseWrapper, 'hidden md:block');

  hasClasses(tag(html, 'nav'), 'overscroll-contain');
  const nav = html.match(/<nav\b[^>]*>([\s\S]*?)<\/nav>/)[1];
  const labels = Array.from(nav.matchAll(/<a\b[^>]*>([\s\S]*?)<\/a>/g), ([, body]) => body.replace(/<[^>]+>/g, '').trim());
  assert.deepEqual(labels.sort(), ['Chat', 'Cron', 'Dashboard', 'Projects', 'Providers', 'Skills', 'System']);
});

test('open Header exposes the expanded trigger with the larger touch target', () => {
  const html = render(Header, { menuOpen: true, menuControlsId: drawerId, menuButtonRef: createRef(), onMenuClick() {} });
  const trigger = tag(html, 'button', 'aria-controls', drawerId);
  assert.match(trigger, / aria-expanded="true"/);
  hasClasses(trigger, 'p-2.5');
  assert.ok(!classes(trigger).includes('p-2'));
});

function leafPaths(value, path = '') {
  if (value === null || typeof value !== 'object') return [path];
  return Object.entries(value).flatMap(([key, child]) => leafPaths(child, path ? `${path}.${key}` : key));
}

test('all seven locales share leaf keys and translate the drawer labels', () => {
  const expected = leafPaths(readLocale('en')).sort();
  for (const code of ['en', 'zh', 'zh-TW', 'ja', 'ko', 'es', 'ru']) {
    const locale = readLocale(code);
    assert.deepEqual(leafPaths(locale).sort(), expected, code);
    for (const key of ['navigation', 'openMenu', 'closeMenu']) {
      assert.equal(typeof locale.nav[key], 'string', `${code} nav.${key}`);
      assert.ok(locale.nav[key].trim(), `${code} nav.${key} is empty`);
    }
  }
});
