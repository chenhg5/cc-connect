// Pure helpers extracted from CronList so they can be unit-tested without
// rendering the React component. Kept side-effect-free and framework-free so
// vitest can load them without a DOM.
//
// Related to Issue #1857: empty / invalid session_key was previously
// accepted by the form UI but rejected by the backend with a generic error.

import type { Session } from '../../api/sessions';

export interface CronFormShape {
  project?: string;
  session_key?: string;
  cron_expr?: string;
  prompt?: string;
  exec?: string;
  description?: string;
  silent?: boolean;
  enabled?: boolean;
  mode?: string;
}

export interface CronFormErrors {
  sessionKey?: 'required';
}

export const EMPTY_FORM_ERRORS: CronFormErrors = Object.freeze({});

/**
 * Validate a cron-job form payload before sending it to the backend.
 *
 * Returns an object describing any client-side errors. Empty result means
 * the form is acceptable. Today the only rule is "session_key is required"
 * (Issue #1857).
 */
export function validateCronForm(form: CronFormShape): CronFormErrors {
  const errors: CronFormErrors = {};
  const key = (form.session_key ?? '').trim();
  if (!key) {
    errors.sessionKey = 'required';
  }
  return errors;
}

/**
 * Pick the most recently active session from a list, using `updated_at` as
 * the recency signal. Sessions without a `session_key` are ignored (a
 * session_key-less session cannot be assigned to a cron job).
 *
 * Returns undefined when no usable session is found — callers use that to
 * disable the session picker and show the empty-state hint (Issue #1857).
 */
export function pickMostRecentSession(
  sessions: ReadonlyArray<Session>,
): Session | undefined {
  const withKey = sessions.filter(s => !!s.session_key);
  if (withKey.length === 0) return undefined;
  const sorted = [...withKey].sort((a, b) =>
    (b.updated_at ?? '').localeCompare(a.updated_at ?? ''),
  );
  return sorted[0];
}