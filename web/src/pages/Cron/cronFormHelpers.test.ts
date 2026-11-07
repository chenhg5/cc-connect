// Unit tests for the pure helpers used by the cron-job form (Issue #1857).
// These cover the three behaviors the dev task asked to verify:
//   1. submit-time validation rejects empty session_key
//   2. auto-select picks the most recently active session
//   3. submit-time validation rejects whitespace-only session_key
//
// The required-marker placeholder change is a pure visual change in
// CronList.tsx (a red "*" next to the label); we assert it via the
// TypeScript build (`pnpm build`) and manual smoke test rather than DOM
// rendering, because the web/ project doesn't currently have a React
// testing-library setup and adding one for a single component would
// outweigh the fix.

import { describe, it, expect } from 'vitest';
import type { Session } from '../../api/sessions';
import {
  validateCronForm,
  pickMostRecentSession,
  EMPTY_FORM_ERRORS,
} from './cronFormHelpers';

describe('validateCronForm — Issue #1857 submit validation', () => {
  it('rejects an empty session_key (case 3a)', () => {
    const errs = validateCronForm({ session_key: '' });
    expect(errs.sessionKey).toBe('required');
  });

  it('rejects a whitespace-only session_key (case 3b)', () => {
    const errs = validateCronForm({ session_key: '   \t\n ' });
    expect(errs.sessionKey).toBe('required');
  });

  it('rejects an undefined session_key (defensive)', () => {
    const errs = validateCronForm({});
    expect(errs.sessionKey).toBe('required');
  });

  it('accepts a non-empty session_key (regression: not over-rejecting)', () => {
    const errs = validateCronForm({ session_key: 'web-session-1' });
    expect(errs).toEqual(EMPTY_FORM_ERRORS);
    expect(errs.sessionKey).toBeUndefined();
  });
});

describe('pickMostRecentSession — Issue #1857 auto-select', () => {
  const make = (key: string, updated_at: string): Session => ({
    id: key,
    session_key: key,
    name: key,
    platform: 'test',
    agent_type: 'claudecode',
    active: false,
    live: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at,
    history_count: 0,
    last_message: null,
  });

  it('returns undefined when there are no sessions', () => {
    expect(pickMostRecentSession([])).toBeUndefined();
  });

  it('returns undefined when sessions exist but none have a session_key', () => {
    const list = [
      { ...make('a', '2026-09-01T00:00:00Z'), session_key: '' },
      { ...make('b', '2026-08-01T00:00:00Z'), session_key: '' },
    ];
    expect(pickMostRecentSession(list)).toBeUndefined();
  });

  it('picks the session with the latest updated_at (case 2)', () => {
    const list = [
      make('old',    '2026-01-01T00:00:00Z'),
      make('newest', '2026-09-17T00:00:00Z'),
      make('middle', '2026-06-01T00:00:00Z'),
    ];
    const picked = pickMostRecentSession(list);
    expect(picked?.session_key).toBe('newest');
  });

  it('does not mutate the input array', () => {
    const list = [
      make('a', '2026-01-01T00:00:00Z'),
      make('b', '2026-09-01T00:00:00Z'),
    ];
    const before = list.map(s => s.session_key);
    pickMostRecentSession(list);
    expect(list.map(s => s.session_key)).toEqual(before);
  });

  it('falls back gracefully when updated_at is empty', () => {
    const list = [
      make('a', ''),
      make('b', '2026-09-01T00:00:00Z'),
      make('c', ''),
    ];
    // empty < real ISO string in localeCompare, so 'b' wins.
    const picked = pickMostRecentSession(list);
    expect(picked?.session_key).toBe('b');
  });
});