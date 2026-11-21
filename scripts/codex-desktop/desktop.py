#!/usr/bin/python3
"""Read complete public messages from the optional desktop IPC bridge."""
import fcntl
import hashlib
import json
import logging
from pathlib import Path
import re
import select
import socket
import sqlite3
import struct
import sys
import time
import uuid
import notify

class DesktopRejected(ValueError):
    pass


def capture(thread, state, root=notify.ROOT):
    if not notify.is_root_desktop(thread) or not Path(state.get('cwd') or '').is_absolute():
        return
    enabled_at = json.loads((root / 'settings.json').read_text())['enabled_at_ms']
    history = state.get('turnHistory', {}).get('history', {})
    turns = list(history.get('entitiesByKey', {}).values()) or state.get('turns', [])
    for turn in turns:
        completed = turn.get('agentMessageCompletedAtMsById', {})
        for item in turn.get('items', []):
            if (item.get('type') != 'agentMessage' or item.get('phase') != 'commentary'
                    or item.get('channel') not in (None, 'commentary', 'final')
                    or item.get('questions') or not item.get('text')
                    or (completed.get(item.get('id')) or 0) < enabled_at):
                continue  # Only new, complete public messages; never stream fragments or old history.
            key = hashlib.sha256(json.dumps(['agent-progress', thread, turn.get('turnId'), item['id']]).encode()).hexdigest()
            path = root / 'outbox' / (key + '.json')
            if path.exists() or (root / 'done' / key).exists():
                continue
            notify.queue(key, {'type': 'agent-progress', 'thread-id': thread,
                              'turn-id': turn.get('turnId'), 'cwd': state['cwd'], 'notification': item['text']}, root)
            logging.info('queued public update %s for %s', item['id'], thread)


class IPC:
    def __init__(self, home=notify.CODEX_HOME):
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.socket.settimeout(15)
        self.socket.connect(str(home / 'ipc' / 'ipc.sock'))
        self.buffer = b''
        self.events = []
        self.client = 'initializing-client'
        self.client = self.request('initialize', 0, {'clientType': 'cc-connect-human'})['result']['clientId']

    def close(self):
        self.socket.close()

    def send(self, message):
        data = json.dumps(dict(message, sourceClientId=self.client)).encode()
        self.socket.sendall(struct.pack('<I', len(data)) + data)

    def receive(self, timeout=15):
        end = time.monotonic() + timeout
        while True:
            if len(self.buffer) >= 4:
                size = struct.unpack('<I', self.buffer[:4])[0]
                if not 0 < size <= 268435456:
                    raise ValueError('invalid desktop IPC frame')
                if len(self.buffer) >= size + 4:
                    data, self.buffer = self.buffer[4:size + 4], self.buffer[size + 4:]
                    return json.loads(data)
            remaining = end - time.monotonic()
            if remaining <= 0 or not select.select([self.socket], [], [], remaining)[0]:
                raise TimeoutError('desktop IPC response timed out')
            data = self.socket.recv(65536)
            if not data:
                raise OSError('desktop IPC disconnected')
            self.buffer += data

    def request(self, method, version, params, target=None):
        key = str(uuid.uuid4())
        message = {'type': 'request', 'requestId': key, 'method': method,
                   'version': version, 'params': params, 'timeoutMs': 10000}
        if target:
            message['targetClientId'] = target
        self.send(message)
        end = time.monotonic() + 15
        while True:
            response = self.receive(max(0, end - time.monotonic()))
            if response.get('type') == 'response' and response.get('requestId') == key:
                if response.get('resultType') != 'success':
                    raise DesktopRejected('desktop IPC: ' + str(response.get('error')))
                return response
            self.events.append(response)

    def follow(self, thread, target=None):
        message = {'type': 'broadcast', 'method': 'thread-stream-following-changed',
                   'version': 1, 'params': {'hostId': 'local', 'conversationId': thread, 'following': True}}
        if target:
            message['targetClientIds'] = [target]
        self.send(message)

    def snapshot(self, thread, owner):
        self.follow(thread, owner)
        end = time.monotonic() + 15
        while True:
            message = self.receive(max(0, end - time.monotonic()))
            params = message.get('params', {})
            change = params.get('change', {})
            if (message.get('method') == 'thread-stream-state-changed'
                    and message.get('sourceClientId') == owner
                    and params.get('hostId') == 'local' and params.get('conversationId') == thread
                    and change.get('type') == 'snapshot'):
                return change['conversationState']


def active_threads():
    # Existing lock files persist after close, so check the live advisory lock.
    ids = []
    for path in (notify.CODEX_HOME / 'thread-writer-locks').glob('*.lock'):
        if path.name.startswith('.'):
            continue
        with path.open('r') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                ids.append(path.stem)
    return {thread for thread in ids if notify.is_root_desktop(thread)}


def watch():
    while True:
        ipc = None
        try:
            ipc = IPC()
            logging.info('desktop listener connected')
            followed, refreshed = set(), float('-inf')
            while True:
                if time.monotonic() - refreshed > 3:
                    current = active_threads()
                    for thread in current - followed:
                        ipc.follow(thread)
                    followed = current
                    refreshed = time.monotonic()
                try:
                    message = ipc.events.pop(0) if ipc.events else ipc.receive(1)
                except TimeoutError:
                    continue
                params = message.get('params', {})
                if message.get('method') == 'thread-stream-following-status-requested' and params.get('hostId') == 'local':
                    if params.get('conversationId') in followed:
                        ipc.follow(params['conversationId'], message['sourceClientId'])
                    continue
                if message.get('method') == 'client-status-changed' and params.get('status') == 'connected' and params.get('clientType') == 'desktop':
                    for thread in followed:
                        ipc.follow(thread)
                    continue
                if message.get('method') != 'thread-stream-state-changed' or params.get('hostId') != 'local':
                    continue
                thread, change = params.get('conversationId'), params.get('change', {})
                if thread not in followed:
                    continue
                if change.get('type') == 'snapshot':
                    capture(thread, change['conversationState'])
                elif any(not p.get('path') or p['path'][0] in ('requests', 'turns', 'turnHistory') for p in change.get('patches', [])):
                    ipc.follow(thread, message['sourceClientId'])  # Request authoritative pending requests and async questions.
        except (OSError, ValueError, KeyError, sqlite3.Error):
            logging.exception('desktop listener will reconnect')
        finally:
            if ipc:
                ipc.close()
        time.sleep(3)
