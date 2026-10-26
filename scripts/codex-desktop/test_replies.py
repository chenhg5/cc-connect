import json
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest.mock import patch
import desktop as human
import replies as reply


class FakeIPC:
    state = {}
    sent = []
    failure = None

    def request(self, method, version, params, target=None):
        if method == 'thread-owner-discovery':
            return {'handledByClientId': 'owner'}
        self.sent.append((method, version, params, target))
        if self.failure:
            raise self.failure
        return {'resultType': 'success'}

    def snapshot(self, thread, owner):
        return self.state

    def close(self):
        pass


class ReplyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        FakeIPC.state = {'requests': [], 'cwd': '/workspace', 'turns': [
            {'turnId': 'running', 'status': 'inProgress', 'items': []}]}
        FakeIPC.sent = []
        FakeIPC.failure = None

    def send(self, mode='queue', message='incoming-1'):
        with patch.object(reply.notify, 'is_root_desktop', return_value=True):
            return reply.submit('thread-1', mode, message, '继续工作', self.root, FakeIPC)

    def drain(self):
        reply.drain(self.root, FakeIPC)

    def test_queue_waits_for_idle_then_submits_once(self):
        self.assertEqual(self.send()['status'], 'queued')
        self.drain()
        self.assertEqual(FakeIPC.sent, [])
        FakeIPC.state['turns'][0]['status'] = 'completed'
        self.drain()
        self.drain()
        self.assertEqual(len(FakeIPC.sent), 1)
        self.assertEqual(FakeIPC.sent[0][0], 'thread-follower-start-turn')
        self.assertEqual(self.send()['status'], 'accepted')
        self.assertEqual(len(FakeIPC.sent), 1)

    def test_now_uses_native_steer_with_same_id_not_start_turn(self):
        self.assertEqual(self.send('now')['status'], 'accepted')
        method, version, params, owner = FakeIPC.sent[0]
        self.assertEqual((method, version, owner), ('thread-follower-steer-turn', 1, 'owner'))
        self.assertEqual(params['clientUserMessageId'], params['restoreMessage']['id'])
        self.assertEqual(params['restoreMessage']['text'], '继续工作')
        self.send('now')
        self.assertEqual(len(FakeIPC.sent), 1)

    def test_unknown_ack_is_never_replayed_after_restart(self):
        FakeIPC.failure = OSError('lost acknowledgement')
        with self.assertRaises(OSError):
            self.send('now')
        FakeIPC.failure = None
        with self.assertRaises(ValueError):
            self.send('now')
        self.drain()
        self.assertEqual(len(FakeIPC.sent), 1)

    def test_queue_fifo_and_pending_questions_block_dispatch(self):
        self.send(message='incoming-1')
        self.send(message='incoming-2')
        FakeIPC.state['turns'][0]['status'] = 'completed'
        FakeIPC.state['requests'] = [{'id': 1, 'method': 'item/tool/requestUserInput', 'completed': False}]
        self.drain()
        self.assertEqual(FakeIPC.sent, [])
        FakeIPC.state['requests'] = []
        self.drain()
        self.assertEqual(len(FakeIPC.sent), 1)
        FakeIPC.state['turns'][0]['status'] = 'inProgress'
        self.drain()
        self.assertEqual(len(FakeIPC.sent), 1)

    def test_validation_and_payload_conflict(self):
        with self.assertRaises(ValueError):
            self.send('unknown')
        self.send('now')
        with patch.object(reply.notify, 'is_root_desktop', return_value=True):
            with self.assertRaises(ValueError):
                reply.submit('thread-1', 'now', 'incoming-1', '不同内容', self.root, FakeIPC)

    def test_concurrent_redelivery_dispatches_only_once(self):
        with ThreadPoolExecutor(max_workers=4) as pool:
            with patch.object(reply.notify, 'is_root_desktop', return_value=True):
                futures = [pool.submit(reply.submit, 'thread-1', 'now', 'same-id',
                                       '继续工作', self.root, FakeIPC) for _ in range(8)]
                for future in futures:
                    self.assertEqual(future.result()['status'], 'accepted')
        self.assertEqual(len(FakeIPC.sent), 1)

    def test_canonical_active_history_and_runtime_never_start_new_turn(self):
        FakeIPC.state = {'requests': [], 'cwd': '/workspace', 'turns': [], 'threadRuntimeStatus': {'type': 'active'}}
        self.send('now')
        self.assertEqual(FakeIPC.sent[0][0], 'thread-follower-steer-turn')
        FakeIPC.sent = []
        FakeIPC.state = {'requests': [], 'cwd': '/workspace', 'turns': [], 'turnHistory': {
            'history': {'entitiesByKey': {'turn:1': {'status': 'inProgress'}}}}}
        self.send(message='queue-id')
        self.drain()
        self.assertEqual(FakeIPC.sent, [])

    def test_now_idle_submits_once_and_rejected_steer_never_falls_back(self):
        FakeIPC.state['turns'][0]['status'] = 'completed'
        self.send('now')
        self.assertEqual(FakeIPC.sent[0][0], 'thread-follower-start-turn')
        FakeIPC.state['turns'][0]['status'] = 'inProgress'
        FakeIPC.failure = human.DesktopRejected('turn finished before steering')
        with self.assertRaises(human.DesktopRejected):
            self.send('now', 'race-id')
        self.assertEqual([s[0] for s in FakeIPC.sent],
                         ['thread-follower-start-turn', 'thread-follower-steer-turn'])

    def test_queue_lost_ack_reports_once_without_replay(self):
        self.send()
        FakeIPC.state['turns'][0]['status'] = 'completed'
        FakeIPC.failure = OSError('lost acknowledgement')
        self.drain()
        notices = list((self.root / 'outbox').glob('*.json'))
        self.assertEqual(len(notices), 1)
        self.assertIn('no automatic replay', json.loads(notices[0].read_text())['notification'])
        notices[0].unlink()
        FakeIPC.failure = None
        self.drain()
        self.assertEqual(len(FakeIPC.sent), 1)
        self.assertEqual(list((self.root / 'outbox').glob('*.json')), [])


if __name__ == '__main__':
    unittest.main()
