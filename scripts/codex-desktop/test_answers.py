import json
import tempfile
from pathlib import Path
from unittest.mock import patch
import desktop as human
import notify


class Desktop:
    def __init__(self, state, fail=False):
        self.state, self.sent, self.fail = state, [], fail
    def request(self, method, version, params, target=None):
        if method == 'thread-owner-discovery':
            return {'handledByClientId': 'owner'}
        self.sent.append((method, version, params, target))
        if self.fail:
            if self.fail == 'malformed':
                raise ValueError('invalid acknowledgement JSON')
            raise TimeoutError('acceptance unknown')
        turn = self.state['turns'][0]
        turn['items'].append({'type': 'steeringUserMessage', 'status': 'accepted'})
        return {'resultType': 'success'}
    def snapshot(self, thread, owner):
        assert thread == 'thread' and owner == 'owner'
        return self.state
    def close(self):
        pass


for status in ('inProgress', 'completed', 'desktop_answered', 'timeout', 'malformed'):
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        notify.configure("example", "test:owner", str(root / "api.sock"), root)
        item = {'type': 'agentMessage', 'id': 'ask', 'questions': [{'title': 'Continue?'}]}
        turn = {'status': 'inProgress', 'items': [item]}
        state = {'cwd': tmp, 'requests': [], 'turns': [turn]}
        with patch.object(notify, 'is_root_desktop', return_value=True):
            human.capture('thread', state, root)
        event = json.loads(next((root / 'outbox').glob('*.json')).read_text())
        key = event['human-key']
        assert 'questions' in event['notification']
        ipc = Desktop(state, status if status in ('timeout', 'malformed') else False)
        if status == 'completed':
            turn['status'] = 'completed'
        elif status == 'desktop_answered':
            state['turns'].append({'status': 'inProgress', 'items': [{'type': 'userMessage'}]})
        try:
            human.answer('thread', key, 'yes', root, lambda: ipc)
            assert status not in ('desktop_answered', 'timeout', 'malformed')
        except (ValueError, TimeoutError):
            assert status in ('desktop_answered', 'timeout', 'malformed')
        if status == 'desktop_answered':
            assert not ipc.sent, 'desktop answer must suppress mobile dispatch'
        else:
            method, version, payload, owner = ipc.sent[0]
            assert owner == 'owner'
            assert method == ('thread-follower-start-turn' if status == 'completed' else 'thread-follower-steer-turn')
            assert version == (2 if status == 'completed' else 1)
            request = payload['turnStart']['request'] if status == 'completed' else payload
            assert 'Continue?' in request['input'][0]['text'] and 'yes' in request['input'][0]['text']
            if status != 'completed':
                assert payload['restoreMessage']['context']['turnTrigger'] == 'send_user_message_async_question'
        try:
            human.answer('thread', key, 'yes again', root, lambda: ipc)
            raise AssertionError('duplicate answer accepted')
        except ValueError:
            pass
        assert len(ipc.sent) == (0 if status == 'desktop_answered' else 1)
print('desktop-first/mobile-first, active/completed routing, timeout and duplicate suppression: passed')

request = {'method': 'request_user_input_async', 'params': {'questions': [
    {'id': '1', 'title': '目录？'}, {'id': '2', 'title': '预算？'}]}}
assert 'Answer: 8' in human.response_for(request, '{"1":"当前目录","2":"8"}')[1]['text']
for invalid in ('只回答一题', '{"1":"当前目录"}', '{"1":"","2":"8"}'):
    try:
        human.response_for(request, invalid)
        raise AssertionError('incomplete answer accepted')
    except ValueError:
        pass
print('async multiple questions require complete, nonempty answers: passed')
