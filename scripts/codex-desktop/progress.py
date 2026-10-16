"""Read-only progress from the desktop owner, or explicitly marked saved history."""
from datetime import datetime
import json
import math
from pathlib import Path
import sqlite3
import time
import uuid
import desktop as human
import notify


def describe(thread, state, now=None, root=notify.ROOT, live=True):
    now = time.time() if now is None else now
    history = state.get('turnHistory', {}).get('history', {})
    turns = list(history.get('entitiesByKey', {}).values()) or state.get('turns', [])
    turn = max(turns, key=lambda t: t.get('turnStartedAtMs') or 0) if turns else {}
    items = turn.get('items', [])
    status = {'inProgress': 'running', 'completed': 'completed', 'failed': 'failed',
              'interrupted': 'interrupted'}.get(turn.get('status'), 'unknown')
    started = turn.get('turnStartedAtMs')
    elapsed = max(0, int(now - started / 1000)) if started else 0
    if status != 'running':
        elapsed = int((turn.get('durationMs') or 0) / 1000)
    plan = next((item.get('plan', []) for item in reversed(items) if item.get('type') == 'todo-list'), [])
    completed = sum(step.get('status') == 'completed' for step in plan)
    pending = []
    if live:
        requests = human.pending_requests(state)
        if requests:
            status = 'waiting'
        requests += [request for _, request in human.async_requests(state)]
        for request in requests:
            key = human.request_key(thread, request)
            if request['method'] == 'request_user_input_async' and not (root / 'pending' / (key + '.json')).exists():
                continue  # Only monitored, still-answerable async questions; not historical cards.
            receipt = root / 'answered' / (key + '.json')
            result = json.loads(receipt.read_text()) if receipt.exists() else {}
            if result.get('status') != 'accepted':
                pending.append({'request_id': key, 'method': request['method'],
                                'answerable': not receipt.exists()})
    elif status in ('running', 'waiting'):
        status = 'unknown'  # Saved task_started is not evidence that its owner is still running.
    remaining = None
    if live and status == 'running' and not pending and started and elapsed > 0 and 0 < completed < len(plan):
        # ponytail: equal-weight plan steps; use explicit per-step estimates if available later.
        remaining = math.ceil(elapsed / completed * (len(plan) - completed))
    summary = next((item['text'] for item in reversed(items)
                    if item.get('type') == 'agentMessage' and item.get('channel') in (None, 'commentary', 'final') and not item.get('questions') and item.get('phase') in ('commentary', 'final_answer') and item.get('text') and (not live or (turn.get('agentMessageCompletedAtMsById', {}).get(item.get('id')) or 0) > 0)), '')
    return {'thread_id': thread, 'title': state.get('title') or '', 'cwd': state.get('cwd') or '',
            'live': live, 'status': status, 'elapsed_seconds': elapsed,
            'summary': summary, 'plan': plan, 'completed_steps': completed,
            'remaining_seconds': remaining, 'pending': pending}


def saved_state(thread, home=notify.CODEX_HOME):
    databases = list(home.glob('state_*.sqlite'))
    if not databases:
        raise ValueError('Codex thread database is unavailable')
    database = max(databases, key=lambda p: int(p.stem.split('_')[-1]))
    with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True) as connection:
        row = connection.execute('SELECT title, cwd, rollout_path, source FROM threads WHERE id = ?', (thread,)).fetchone()
    if not row or row[3] != 'vscode':
        raise ValueError('UUID is not a root desktop conversation')
    state = {'title': row[0], 'cwd': row[1], 'turns': []}
    turn = None
    with Path(row[2]).open() as stream:
        for line in stream:
            try:
                event = json.loads(line)
            except ValueError:
                continue  # A concurrently appended final line may be incomplete.
            payload = event.get('payload', {})
            kind = payload.get('type')
            if event.get('type') == 'event_msg' and kind == 'task_started':
                started = datetime.fromisoformat(event['timestamp'].replace('Z', '+00:00')).timestamp() * 1000
                turn = {'status': 'inProgress', 'turnStartedAtMs': started, 'items': []}
                state['turns'] = [turn]
            elif turn and event.get('type') == 'event_msg' and kind in ('task_complete', 'turn_aborted'):
                ended = datetime.fromisoformat(event['timestamp'].replace('Z', '+00:00')).timestamp() * 1000
                turn.update(status='completed' if kind == 'task_complete' else 'interrupted',
                            durationMs=max(0, ended - turn['turnStartedAtMs']))
            elif turn and event.get('type') == 'response_item' and kind == 'message' and payload.get('role') == 'assistant' and payload.get('channel') in (None, 'commentary', 'final') and (payload.get('phase') in ('commentary', 'final_answer') or payload.get('channel') in ('commentary', 'final')):
                text = '\n'.join(part.get('text', '') for part in payload.get('content', []) if part.get('type') == 'output_text')
                if text:
                    turn['items'].append({'type': 'agentMessage', 'phase': payload.get('phase') or ('commentary' if payload.get('channel') == 'commentary' else 'final_answer'), 'text': text})
    return state


def query(thread, ipc_factory=human.IPC):
    if str(uuid.UUID(thread)) != thread:
        raise ValueError('invalid UUID')
    if not notify.is_root_desktop(thread):
        raise ValueError('UUID is not a root desktop conversation')
    ipc = None
    try:
        ipc = ipc_factory()
        owner = ipc.request('thread-owner-discovery', 1, {'hostId': 'local', 'conversationId': thread})['handledByClientId']
        return describe(thread, ipc.snapshot(thread, owner))
    except (OSError, ValueError, KeyError):
        return describe(thread, saved_state(thread), live=False)
    finally:
        if ipc:
            ipc.close()
