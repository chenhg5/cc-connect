"""Exercise the actual HTTP client and durable outbox against a local API."""
import contextlib
import http.server
import json
from pathlib import Path
import socketserver
import tempfile
import threading
import time
import unittest
import notify


@contextlib.contextmanager
def api(status):
    with tempfile.TemporaryDirectory() as tmp:
        root, requests = Path(tmp), []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
                self.send_response(status)
                self.end_headers()
                self.wfile.write(b'test response')

            def log_message(self, *_):
                pass

        with socketserver.UnixStreamServer(str(root / 'api.sock'), Handler) as server:
            notify.configure('example', 'test:owner', str(root / 'api.sock'), root)
            thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=0.01))
            thread.start()
            try:
                yield root, requests
            finally:
                server.shutdown()
                thread.join()


class NotificationFailures(unittest.TestCase):
    def queue(self, root):
        return notify.queue('event', {'type': 'agent-progress', 'thread-id': 'thread', 'cwd': str(root), 'notification': 'hello'}, root)

    def test_permanent_http_failures_are_quarantined_without_retries(self):
        for status in (400, 404, 409, 422):
            with self.subTest(status=status), api(status) as (root, requests):
                self.queue(root)
                notify.drain(root)
                notify.drain(root)
                self.assertEqual(len(requests), 1)
                self.assertFalse((root / 'outbox' / 'event.json').exists())
                self.assertTrue((root / 'invalid' / 'event.json').exists())
                self.assertFalse((root / 'done' / 'event').exists())
                self.assertFalse(self.queue(root), 'redelivered callback bypassed quarantine')

    def test_transient_http_failures_keep_the_job_retryable(self):
        for status in (408, 429, 500, 503):
            with self.subTest(status=status), api(status) as (root, requests):
                self.queue(root)
                notify.drain(root)
                notify.drain(root)
                self.assertEqual(len(requests), 2)
                self.assertTrue((root / 'outbox' / 'event.json').exists())
                self.assertFalse((root / 'done' / 'event').exists())

    def test_success_persists_the_original_route_deadline(self):
        with api(200) as (root, requests):
            self.queue(root)
            notify.drain(root)
            refs = json.loads((root / 'threads.json').read_text())
            self.assertGreater(requests[0].get('desktop_route_expires_at', 0), time.time())
            self.assertEqual(refs['thread']['desktop_route_expires_at'], requests[0]['desktop_route_expires_at'])

    def test_restore_removes_permanent_failures_and_expired_routes(self):
        restore = getattr(notify, 'restore_routes', None)
        self.assertTrue(callable(restore), 'route restoration must classify permanent failures')
        with api(409) as (root, requests):
            notify.save(root / 'threads.json', {
                'expired': {'reply_thread_id': 'expired', 'desktop_route_expires_at': int(time.time()) - 1},
                'live': {'reply_thread_id': 'live', 'desktop_route_expires_at': int(time.time()) + 60},
                'legacy': {'reply_thread_id': 'legacy'},
                'malformed': {'reply_thread_id': 'malformed', 'desktop_route_expires_at': 'bad'},
                'null': None,
            })
            self.assertTrue(restore(root))
            self.assertEqual([r['reply_thread_id'] for r in requests], ['live'])
            self.assertEqual(json.loads((root / 'threads.json').read_text()), {})
            self.assertTrue(restore(root))
            self.assertEqual(len(requests), 1)


if __name__ == '__main__':
    unittest.main()
