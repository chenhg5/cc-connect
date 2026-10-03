import json
import tempfile
from pathlib import Path
from unittest.mock import patch
import desktop
import notify

class Native:
    def __init__(self, state, mode):
        self.state, self.mode, self.sent = state, mode, 0
    def request(self, method, version, params, target=None):
        if method == 'thread-owner-discovery': return {'handledByClientId': 'owner'}
        assert method == 'thread-follower-command-approval-decision'
        assert params['decision'] == 'accept' and params['requestId'] == 'approval'
        self.sent += 1
        if self.mode == 'unknown': raise TimeoutError('acceptance unknown')
        if self.mode == 'rejected': raise desktop.DesktopRejected('rejected')
        self.state['requests'] = []
        if self.mode == 'malformed': self.state.pop('requests')
        return {'resultType': 'success'}
    def snapshot(self, thread, owner): return self.state
    def close(self): pass

for mode in ('accepted', 'unknown', 'rejected', 'desktop-first', 'malformed'):
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        notify.configure('example', 'test:owner', str(root / 'api.sock'), root)
        request = {'id': 'approval', 'method': 'item/commandExecution/requestApproval', 'params': {'command': 'example'}}
        state = {'cwd': tmp, 'requests': [request], 'turns': []}
        with patch.object(notify, 'is_root_desktop', return_value=True): desktop.capture('thread', state, root)
        key = desktop.request_key('thread', request)
        if mode == 'desktop-first': state['requests'] = []
        ipc = Native(state, mode)
        try:
            desktop.answer('thread', key, 'approve', root, lambda: ipc)
            assert mode == 'accepted'
        except (ValueError, TimeoutError): assert mode != 'accepted'
        receipt = root / 'answered' / (key + '.json')
        assert receipt.exists() == (mode in ('accepted', 'unknown', 'malformed'))
        if mode != 'rejected':
            try:
                desktop.answer('thread', key, 'approve', root, lambda: ipc)
                raise AssertionError('duplicate accepted')
            except ValueError: pass
            assert ipc.sent == (0 if mode == 'desktop-first' else 1)
print('native approvals: desktop-first, receipts, explicit rejection and unknown acceptance passed')
