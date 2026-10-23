import json
import sqlite3
import tempfile
from pathlib import Path
from unittest.mock import patch
import notify
import desktop

with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    settings = {'project': 'example', 'session_key': 'test:owner', 'api_socket': str(root / 'api.sock'), 'enabled_at_ms': 1000}
    notify.save(root / 'settings.json', settings)
    stamp = 2000
    state = {'cwd': tmp, 'requests': [], 'turns': [{'turnId': 'turn', 'status': 'inProgress', 'items': [
        {'id': 'public', 'type': 'agentMessage', 'phase': 'commentary', 'text': 'progress\n' + 'x' * 5000},
        {'id': 'partial', 'type': 'agentMessage', 'phase': 'commentary', 'text': 'unfinished'},
        {'id': 'old', 'type': 'agentMessage', 'phase': 'commentary', 'text': 'old'},
        {'id': 'final', 'type': 'agentMessage', 'phase': 'final_answer', 'text': 'finished'},
        {'id': 'hidden', 'type': 'agentMessage', 'phase': 'commentary', 'channel': 'analysis', 'text': 'private'},
        {'type': 'reasoning', 'text': 'private'}],
        'agentMessageCompletedAtMsById': {'public': stamp, 'old': 500, 'final': stamp, 'hidden': stamp}}]}
    with patch.object(notify, 'is_root_desktop', return_value=True):
        desktop.capture('thread', state, root)
        jobs = list((root / 'outbox').glob('*.json'))
        assert len(jobs) == 1, 'public update missing or private/partial/history/final leaked'
        desktop.capture('thread', state, root)
        assert len(list((root / 'outbox').glob('*.json'))) == 1
        sent = []
        with patch.object(notify, 'post', side_effect=lambda body, root: sent.append(body)):
            notify.drain(root)
        assert len(sent) == 1 and sent[0]['desktop_event'] == 'progress'
        assert state['turns'][0]['items'][0]['text'] == sent[0]['message']
        desktop.capture('thread', state, root)
        assert not list((root / 'outbox').glob('*.json')), 'completed delivery was replayed'
        event = {'type': 'agent-turn-complete', 'client': 'Codex Desktop', 'thread-id': 'thread', 'turn-id': 'turn', 'cwd': tmp, 'last-assistant-message': 'complete'}
        assert notify.enqueue(event, root)
        with patch.object(notify, 'post', side_effect=OSError('offline')):
            notify.drain(root)
        assert list((root / 'outbox').glob('*.json')), 'failed send lost'
        with patch.object(notify, 'post', side_effect=lambda body, root: sent.append(body)):
            notify.drain(root)
        assert sent[-1]['desktop_event'] == 'completed' and sent[-1]['message'] == 'complete'
        assert not notify.enqueue(event, root)
    with patch.object(notify, 'is_root_desktop', return_value=False):
        assert not notify.enqueue(dict(event, **{'thread-id': 'child'}), root)
print('desktop notifications: public completion, full text, filters, dedup and retry passed')

with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    notify.configure('example', 'test:owner', str(root / 'api.sock'), root)
    try:
        notify.configure('example', 'test:other', str(root / 'api.sock'), root)
        raise AssertionError('changing destinations would leak queued messages')
    except ValueError:
        pass
    for name in ('old', 'new'):
        notify.save(root / 'outbox' / (name + '.json'), {'type': 'agent-turn-complete', 'thread-id': name, 'turn-id': name, 'cwd': tmp})
    calls = []
    def send(body, root):
        calls.append(body['reply_thread_id'])
        if body['reply_thread_id'] == 'old': raise OSError('unavailable')
    with patch.object(notify, 'post', side_effect=send):
        notify.drain(root)
    assert calls == ['old', 'new'] and (root / 'outbox' / 'old.json').exists()
    with patch.object(notify, 'post', side_effect=notify.WeChatThrottled('ret=-2')) as send:
        notify.drain(root)
        notify.drain(root)
        assert send.call_count == 1, 'cooldown retried platform'
print('destination immutability, independent queue failures and cooldown passed')

with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    notify.configure('example', 'test:owner', str(root / 'api.sock'), root)
    event = {'type': 'agent-progress', 'thread-id': 'thread', 'cwd': tmp}
    notify.save(root / 'outbox' / 'bad.json', {'type': 'unsupported'})
    (root / 'outbox' / 'broken.json').write_text('{')
    (root / 'outbox' / 'list.json').write_text('[]')
    (root / 'outbox' / 'null.json').write_text('null')
    notify.save(root / 'outbox' / 'done.json', event)
    (root / 'done').mkdir()
    (root / 'done' / 'done').touch()
    assert not notify.queue('done', event, root)
    assert notify.queue('good', event, root)
    sent = []
    with patch.object(notify, 'post', side_effect=lambda body, root: sent.append(body)):
        notify.drain(root)
    assert len(sent) == 1 and len(list((root / 'invalid').glob('*.json'))) == 4
print('malformed jobs isolated and delivered jobs rechecked: passed')
