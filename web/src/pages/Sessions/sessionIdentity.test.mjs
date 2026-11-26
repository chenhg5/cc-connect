import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionOwnerLabel } from './sessionIdentity.js';

test('default-named sessions still identify their sender and chat', () => {
  assert.equal(sessionOwnerLabel({name: 'default', user_name: 'Ada', user_id: 'u9', chat_name: 'Team', chat_id: 'g7'}), 'Team · Ada');
  assert.equal(sessionOwnerLabel({name: 'default', user_id: 'u9', chat_id: 'g7'}), 'g7 · u9');
});
