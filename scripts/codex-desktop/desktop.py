#!/usr/bin/python3
"""Read complete public messages from the optional desktop IPC bridge."""
import fcntl
import hashlib
import json
import logging
import os
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

APPROVALS = {
    'item/commandExecution/requestApproval': 'thread-follower-command-approval-decision',
    'item/fileChange/requestApproval': 'thread-follower-file-approval-decision',
    'item/permissions/requestApproval': 'thread-follower-permissions-request-approval-response',
}


class DesktopRejected(ValueError):
    pass


def forget(path):
    try:
        path.unlink()
    except FileNotFoundError:
        pass


def request_key(thread, request):
    identity = [thread, request['id'], request['method'], request.get('params')]
    return hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()[:24]


def pending_requests(state):
    requests = state.get('requests')
    if not isinstance(requests, list) or any(not isinstance(r, dict) or 'id' not in r or 'method' not in r for r in requests):
        raise ValueError('unsupported desktop request snapshot')
    return [r for r in requests if not r.get('completed')]


def response_for(request, answer):
    method, params = request['method'], request.get('params', {})
    if method in APPROVALS:
        if answer not in ('approve', 'deny'):
            raise ValueError('approval requires approve or deny for this request')
        if method == 'item/permissions/requestApproval':
            return APPROVALS[method], {'response': {'permissions': params.get('permissions', {}) if answer == 'approve' else {}, 'scope': 'turn'}}
        return APPROVALS[method], {'decision': 'accept' if answer == 'approve' else 'decline'}
    if method in ('item/tool/requestUserInput', 'request_user_input_async'):
        questions = params.get('questions', [])
        if not questions:
            raise ValueError('request has no questions')
        if len(questions) == 1 and not answer.lstrip().startswith('{'):
            answers = {questions[0]['id']: answer}
        else:
            try:
                answers = json.loads(answer)
            except ValueError:
                raise ValueError('multiple questions require a JSON object keyed by question ID')
        if not isinstance(answers, dict) or set(answers) != {q['id'] for q in questions}:
            raise ValueError('answer IDs must match all question IDs')
        if any(not isinstance(v, str) or not v.strip() for v in answers.values()):
            raise ValueError('each answer must be nonempty text')
        if method == 'request_user_input_async':
            text = '\n\n'.join('Question %s: %s\nAnswer: %s' % (q['id'], q['title'], answers[q['id']]) for q in questions)
            return 'async', {'text': text}
        return 'thread-follower-submit-user-input', {'response': {'answers': {k: {'answers': [v]} for k, v in answers.items()}}
        }
    if method == 'mcpServer/elicitation/request' and params.get('mode', 'form') == 'form':
        if answer == 'deny':
            response = {'action': 'decline', 'content': None}
        else:
            try:
                content = {} if answer == 'approve' else json.loads(answer)
            except ValueError:
                raise ValueError('MCP form requires a JSON object or deny')
            if not isinstance(content, dict):
                raise ValueError('MCP form must be a JSON object')
            response = {'action': 'accept', 'content': content}
        return 'thread-follower-submit-mcp-server-elicitation-response', {'response': response}
    raise ValueError('unsupported request; answer it in the desktop application')


def notification(thread, key, request):
    params = request.get('params', {})
    if request['method'] == 'mcpServer/elicitation/request' and params.get('mode') not in (None, 'form'):
        params = {k: params[k] for k in ('serverName', 'mode', 'message') if k in params}
    return json.dumps({'method': request['method'], 'params': params}, ensure_ascii=False, indent=2)


def capture(thread, state, root=notify.ROOT):
    if not notify.is_root_desktop(thread) or not Path(state.get('cwd') or '').is_absolute():
        return
    capture_updates(thread, state, root)
    live = set()
    requests = pending_requests(state) + [request for turn, request in async_requests(state)
        if turn.get('status') == 'inProgress' or
        (turn.get('agentMessageCompletedAtMsById', {}).get(request['id']) or 0) >=
        json.loads((root / 'settings.json').read_text())['enabled_at_ms'] or
        (root / 'pending' / (request_key(thread, request) + '.json')).exists()]
    for request in requests:
        if 'id' not in request or 'method' not in request:
            continue
        key = request_key(thread, request)
        live.add(key)
        path = root / 'pending' / (key + '.json')
        if (root / 'answered' / (key + '.json')).exists():
            continue
        notify.save(path, {'thread': thread, 'request': request})
        notify.queue(key, {'type': 'human-input-required', 'thread-id': thread,
                          'cwd': state['cwd'], 'human-key': key,
                          'notification': notification(thread, key, request)}, root)
    for path in (root / 'pending').glob('*.json'):
        if json.loads(path.read_text())['thread'] == thread and path.stem not in live:
            forget(path)


def capture_updates(thread, state, root=notify.ROOT):
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


def async_requests(state):
    history = state.get('turnHistory', {}).get('history', {})
    turns = list(history.get('entitiesByKey', {}).values()) or state.get('turns', [])
    questions = []
    for turn in sorted(turns, key=lambda t: t.get('turnStartedAtMs') or 0):
        for item in turn.get('items', []):
            if item.get('type') == 'userMessage' or (item.get('type') == 'steeringUserMessage' and item.get('status') == 'accepted'):
                # ponytail: the desktop exposes no atomic question-answer claim;
                # any accepted follow-up invalidates older questions conservatively.
                questions = []
            elif item.get('type') == 'agentMessage' and item.get('questions'):
                request = {'id': item['id'], 'method': 'request_user_input_async',
                           'params': {'questions': [dict(q, id=str(n)) for n, q in enumerate(item['questions'], 1)]}}
                questions.append((turn, request))
    return questions


def live_request_keys(thread, state):
    requests = pending_requests(state) + [request for _, request in async_requests(state)]
    return {request_key(thread, request) for request in requests}


def submit_async(ipc, owner, thread, text, state, trigger='send_user_message_async_question', message_id=None):
    message_id = message_id or str(uuid.uuid4())
    inputs = [{'type': 'text', 'text': text, 'text_elements': []}]
    history = state.get('turnHistory', {}).get('history', {})
    turns = list(history.get('entitiesByKey', {}).values()) or state.get('turns', [])
    if (state.get('threadRuntimeStatus', {}).get('type') == 'active'
            or any(turn.get('status') == 'inProgress' for turn in turns)):
        restore = {'id': message_id, 'text': text, 'cwd': state.get('cwd'),
                   'createdAt': int(time.time() * 1000),
                   'context': {'prompt': text, 'turnTrigger': trigger,
                               'workspaceRoots': [state['cwd']]}}
        return ipc.request('thread-follower-steer-turn', 1,
                           {'conversationId': thread, 'clientUserMessageId': message_id,
                            'input': inputs, 'restoreMessage': restore, 'attachments': []}, owner)
    return ipc.request('thread-follower-start-turn', 2,
                       {'conversationId': thread, 'turnStart': {
                           'request': {'threadId': thread, 'clientUserMessageId': message_id, 'input': inputs},
                           'context': {'inheritThreadSettings': True}}}, owner)


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
                    raise DesktopRejected('desktop rejected the request')
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


def answer(thread, key, text, root=notify.ROOT, ipc_factory=IPC):
    if not re.fullmatch(r'[0-9a-f]{24}', key):
        raise ValueError('invalid request ID')
    with (root / 'answer.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        receipt = root / 'answered' / (key + '.json')
        if receipt.exists():
            raise ValueError('answer already submitted; check the desktop if acceptance was unknown')
        path = root / 'pending' / (key + '.json')
        if not path.exists():
            raise ValueError('request handled or expired')
        record = json.loads(path.read_text())
        if record['thread'] != thread:
            raise ValueError('request belongs to another thread')
        request = record['request']
        method, payload = response_for(request, text)
        ipc = ipc_factory()
        try:
            owner = ipc.request('thread-owner-discovery', 1, {'hostId': 'local', 'conversationId': thread})['handledByClientId']
            state = ipc.snapshot(thread, owner)
            if key not in live_request_keys(thread, state):
                forget(path)
                raise ValueError('request handled in the desktop or expired')
            # ponytail: one process-shared answer lock; per-thread locks if throughput matters.
            # Persist before mutation: never replay an unacknowledged request.
            notify.save(receipt, {'status': 'acceptance-unknown'})
            try:
                if method == 'async':
                    submit_async(ipc, owner, thread, payload['text'], state)
                else:
                    payload.update(conversationId=thread, requestId=request['id'])
                    ipc.request(method, 1, payload, owner)
                    state = ipc.snapshot(thread, owner)
                    if any(request_key(thread, r) == key for r in pending_requests(state)):
                        raise ValueError('desktop has not confirmed completion; check the desktop')
            except DesktopRejected:
                forget(receipt)
                raise
            notify.save(receipt, {'status': 'accepted'})
            forget(path)
        finally:
            ipc.close()



if __name__ == '__main__':
    try:
        os.umask(0o077)
        if len(sys.argv) < 3 or str(uuid.UUID(sys.argv[2])) != sys.argv[2] or not notify.is_root_desktop(sys.argv[2]):
            raise ValueError('invalid root desktop thread UUID')
        if len(sys.argv) == 6 and sys.argv[1] == 'reply':
            import replies
            print(json.dumps(replies.submit(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5])))
            sys.exit(0)
        elif len(sys.argv) == 4 and sys.argv[1] == 'reply':
            import replies
            print(json.dumps(replies.submit(sys.argv[2], 'queue', str(uuid.uuid4()), sys.argv[3])))
            sys.exit(0)
        elif len(sys.argv) == 5 and sys.argv[1] == 'answer':
            answer(*sys.argv[2:])
        else:
            raise ValueError('usage: desktop.py reply UUID MODE MESSAGE_ID TEXT | answer UUID REQUEST TEXT')
        print(json.dumps({'status': 'accepted'}))
    except (OSError, ValueError, KeyError, sqlite3.Error) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
