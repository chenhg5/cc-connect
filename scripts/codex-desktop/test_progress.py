"""Checks that status queries never infer success or expose tool/reasoning output."""
import copy
import tempfile
from pathlib import Path
import progress
import desktop as human
import notify
from unittest.mock import patch
import json
import sqlite3

state = {'title': 'Example task', 'cwd': '/tmp/work', 'requests': [], 'turns': [{
    'agentMessageCompletedAtMsById': {'public': 1100000}, 'turnId': 'turn', 'status': 'inProgress', 'turnStartedAtMs': 1000000,
    'items': [
        {'type': 'todo-list', 'plan': [
            {'step': 'Investigate', 'status': 'completed'},
            {'step': 'Fix', 'status': 'in_progress'},
            {'step': 'Verify', 'status': 'pending'}]},
        {'type': 'agentMessage', 'id': 'public', 'phase': 'commentary', 'text': 'Working on the fix.'},
        {'type': 'agentMessage', 'id': 'partial', 'phase': 'commentary', 'text': 'PRIVATE partial'},
        {'type': 'reasoning', 'text': 'PRIVATE reasoning'},
        {'type': 'commandExecution', 'aggregatedOutput': 'PRIVATE source'}]}]}
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    notify.configure("example", "test:owner", str(root / "api.sock"), root)
    r = progress.describe('thread', state, now=1120, root=root)
    assert r['status'] == 'running' and r['elapsed_seconds'] == 120
    assert r['remaining_seconds'] == 240 and r['completed_steps'] == 1
    assert r['summary'] == 'Working on the fix.' and 'PRIVATE' not in str(r)
    blocked = copy.deepcopy(state)
    blocked['requests'] = [{'id': 'approval', 'method': 'item/fileChange/requestApproval'}]
    r = progress.describe('thread', blocked, now=1120, root=root)
    assert r['status'] == 'waiting' and r['remaining_seconds'] is None
    assert r['pending'][0]['request_id'] == human.request_key('thread', blocked['requests'][0])
    done = copy.deepcopy(state)
    done['turns'][0].update(status='completed', durationMs=90000)
    r = progress.describe('thread', done, now=1120, root=root)
    assert r['status'] == 'completed' and r['elapsed_seconds'] == 90
    assert r['remaining_seconds'] is None, 'turn completion does not prove all implementation steps done'
    new_turn = copy.deepcopy(state)
    new_turn['turns'].append({'status': 'inProgress', 'turnStartedAtMs': 1100000, 'items': []})
    assert not progress.describe('thread', new_turn, now=1120, root=root)['plan'], 'previous plan must not leak into new turn'
    question = copy.deepcopy(state)
    question['turns'][0]['items'].append({'type': 'agentMessage', 'id': 'ask', 'text': 'Need confirmation', 'questions': [{'title': 'Which directory?'}]})
    assert not progress.describe('thread', question, now=1120, root=root)['pending'], 'historical questions must not get invalid answer commands'
    with patch.object(notify, 'is_root_desktop', return_value=True): human.capture('thread', question, root)
    r = progress.describe('thread', question, now=1120, root=root)
    assert r['status'] == 'running' and r['pending'] and r['remaining_seconds'] is None
    assert r['pending'][0]['method'] == 'request_user_input_async'
    key = human.request_key('thread', human.async_requests(question)[0][1])
    notify.save(root / 'answered' / (key + '.json'), {'status': 'acceptance-unknown'})
    r = progress.describe('thread', question, now=1120, root=root)
    assert r['pending'] and not r['pending'][0]['answerable'] and r['remaining_seconds'] is None
    failed = copy.deepcopy(state)
    failed['turns'][0]['status'] = 'failed'
    assert progress.describe('thread', failed, now=1120, root=root)['status'] == 'failed'

    transcript = root / 'rollout.jsonl'
    with sqlite3.connect(str(root / 'state_5.sqlite')) as c:
        c.execute('CREATE TABLE threads (id, title, cwd, rollout_path, source)')
        c.execute('INSERT INTO threads VALUES (?,?,?,?,?)', ('thread', 'Task', '/tmp/work', str(transcript), 'vscode'))
    events = [
        {'timestamp': '2026-10-03T01:00:00Z', 'type': 'event_msg', 'payload': {'type': 'task_started'}},
        {'type': 'response_item', 'payload': {'type': 'reasoning', 'summary': 'PRIVATE'}},
        {'type': 'response_item', 'payload': {'type': 'message', 'role': 'assistant', 'phase': 'final_answer', 'content': [{'type': 'output_text', 'text': 'Checks passed'}]}},
        {'timestamp': '2026-10-03T01:02:00Z', 'type': 'event_msg', 'payload': {'type': 'task_complete'}}]
    events.insert(-1, {'type': 'response_item', 'payload': {'type': 'message', 'role': 'assistant', 'channel': 'analysis', 'content': [{'type': 'output_text', 'text': 'PRIVATE analysis'}]}})
    events.insert(-1, {'type': 'response_item', 'payload': {'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'PRIVATE unclassified'}]}})
    transcript.write_text('\n'.join(json.dumps(e) for e in events) + '\n{"incomplete')
    r = progress.describe('thread', progress.saved_state('thread', root), live=False)
    assert not r['live'] and r['status'] == 'completed' and r['elapsed_seconds'] == 120 and r['summary'] == 'Checks passed'
    transcript.write_text(json.dumps(events[0]) + '\n')
    r = progress.describe('thread', progress.saved_state('thread', root), live=False)
    assert r['status'] == 'unknown' and r['remaining_seconds'] is None
    try:
        progress.saved_state('unknown', root)
        raise AssertionError('unknown UUID accepted')
    except ValueError:
        pass
print('progress: live states, elapsed/ETA, pending answers, current-turn isolation and privacy passed')
