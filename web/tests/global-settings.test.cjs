const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const React = require('react');
const { act, create } = require('react-test-renderer');
const ts = require('typescript');

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

// Compile the real component and mock only its API, translations and UI chrome.
// This keeps the regression independent of a running server or a browser binary.
function loadSettings(api) {
  const filename = path.join(__dirname, '../src/pages/System/GlobalSettings.tsx');
  const code = ts.transpileModule(fs.readFileSync(filename, 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true },
  }).outputText;
  const t = (key, fallback) => fallback || key;
  const mocks = {
    '@/api/settings': api,
    'react-i18next': { useTranslation: () => ({ t }) },
    '@/lib/utils': { cn: (...classes) => classes.filter(Boolean).join(' ') },
    '@/components/ui': {
      Card: ({ children }) => React.createElement('div', null, children),
      Button: ({ loading, children, ...props }) => React.createElement('button', props, children),
      Input: (props) => React.createElement('input', props),
    },
  };
  const listeners = new Map();
  const module = { exports: {} };
  vm.runInNewContext(code, {
    module, exports: module.exports,
    require: (name) => mocks[name] || require(name),
    window: { addEventListener: (name, fn) => listeners.set(name, fn), removeEventListener: (name) => listeners.delete(name) },
  }, { filename });
  return { Component: module.exports.default, refresh: () => listeners.get('cc:refresh')?.() };
}

for (const initial of [undefined, false, true]) {
  test(`commentary switch loads ${initial}, saves both values and refreshes`, async () => {
    const state = { language: 'en', thinking_messages: false, commentary_messages: initial };
    const saved = [];
    const { Component, refresh } = loadSettings({
      getGlobalSettings: async () => ({ ...state }),
      updateGlobalSettings: async (body) => { saved.push(body); Object.assign(state, body); },
    });
    let view;
    await act(async () => { view = create(React.createElement(Component)); });
    try {
      const toggle = () => view.root.findByProps({ role: 'switch', 'aria-label': 'Commentary messages' });
      const save = () => view.root.findAllByType('button').find((b) => b.children.includes('common.save'));
      const expected = initial ?? true;
      assert.equal(toggle().props['aria-checked'], expected);
      await act(async () => toggle().props.onClick());
      await act(async () => save().props.onClick());
      assert.equal(saved.at(-1).commentary_messages, !expected);
      assert.equal(saved.at(-1).thinking_messages, false);
      await act(async () => refresh());
      assert.equal(toggle().props['aria-checked'], !expected);
      await act(async () => toggle().props.onClick());
      await act(async () => save().props.onClick());
      assert.equal(saved.at(-1).commentary_messages, expected);
    } finally {
      await act(async () => view.unmount());
    }
  });
}

test('every UI locale explains the commentary switch', () => {
  const dir = path.join(__dirname, '../src/i18n/locales');
  for (const file of fs.readdirSync(dir).filter((name) => name.endsWith('.json'))) {
    const { settings } = JSON.parse(fs.readFileSync(path.join(dir, file), 'utf8'));
    assert.ok(settings.commentaryMessages, `${file}: missing label`);
    assert.ok(settings.commentaryMessagesHint, `${file}: missing hint`);
  }
});
