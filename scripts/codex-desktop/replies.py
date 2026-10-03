"""Durable mobile follow-ups; dispatch through the existing desktop owner."""
from contextlib import contextmanager
import fcntl
import hashlib
import json
import logging
import time
import uuid
import desktop as human
import notify


@contextmanager
def locked(root):
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (root / 'reply.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def busy(state):
    history = state.get('turnHistory', {}).get('history', {})
    turns = list(history.get('entitiesByKey', {}).values()) or state.get('turns', [])
    return (state.get('threadRuntimeStatus', {}).get('type') == 'active'
            or bool(state.get('unconfirmedTurnSubmissions'))
            or any(turn.get('status') == 'inProgress' for turn in turns)
            or bool(human.pending_requests(state)))


def dispatch(job, path, ipc_factory):
    ipc = ipc_factory()
    try:
        owner = ipc.request('thread-owner-discovery', 1,
                            {'hostId': 'local', 'conversationId': job['thread']})['handledByClientId']
        if not owner:
            raise ValueError('Desktop owner unavailable; keep Codex open')
        state = ipc.snapshot(job['thread'], owner)
        if job['mode'] == 'queue' and busy(state):
            return
        if state.get('unconfirmedTurnSubmissions'):
            raise ValueError('Desktop has an unconfirmed submission; check it before retrying')
        if job['mode'] == 'now' and human.pending_requests(state):
            raise ValueError('Desktop is waiting for input; use the notified /answer command')
        # Save before the mutation. A disconnect or process crash cannot replay it.
        job.update(status='unknown', cwd=state.get('cwd'))
        notify.save(path, job)
        try:
            human.submit_async(ipc, owner, job['thread'], job['text'], state,
                               message_id=job['id'], trigger='send_user_message')
        except human.DesktopRejected:
            job['status'] = 'failed'
            notify.save(path, job)
            raise
        job['status'] = 'accepted'
        notify.save(path, job)
    finally:
        ipc.close()


def submit(thread, mode, incoming_id, text, root=notify.ROOT, ipc_factory=human.IPC):
    if mode not in ('queue', 'now') or not incoming_id or not text.strip():
        raise ValueError('usage: /reply UUID --queue|--now instruction')
    if not notify.is_root_desktop(thread):
        raise ValueError('Only root desktop conversations can receive follow-ups')
    key = hashlib.sha256(json.dumps([thread, incoming_id]).encode()).hexdigest()
    path = root / 'replies' / (key + '.json')
    with locked(root):
        if path.exists():
            job = json.loads(path.read_text())
            if job['text'] != text or job['mode'] != mode:
                raise ValueError('Message ID conflicts with an earlier payload or mode')
            if job['status'] not in ('queued', 'accepted'):
                raise ValueError('Prior submission is unknown or rejected; check the desktop; no replay')
        else:
            job = {'thread': thread, 'mode': mode, 'text': text,
                   'id': str(uuid.uuid4()), 'created': time.time_ns(), 'status': 'queued'}
            notify.save(path, job)
            if mode == 'now':
                try:
                    dispatch(job, path, ipc_factory)
                except (OSError, ValueError, KeyError):
                    if job['status'] == 'queued':
                        job['status'] = 'failed'
                        notify.save(path, job)
                    raise
        return {'status': job['status'], 'message_id': job['id']}


def drain(root=notify.ROOT, ipc_factory=human.IPC):
    with locked(root):
        paths = list((root / 'replies').glob('*.json'))
        jobs = sorted([(p, json.loads(p.read_text())) for p in paths],
                      key=lambda pair: pair[1]['created'])
        seen = set()
        for path, job in jobs:
            if job['mode'] == 'queue' and job['status'] in ('unknown', 'failed'):
                if job.get('cwd') and not job.get('reported'):
                    report_failure(job, path, root)
                continue
            if job['status'] != 'queued' or job['mode'] != 'queue' or job['thread'] in seen:
                continue
            seen.add(job['thread'])
            try:
                dispatch(job, path, ipc_factory)
            except (OSError, ValueError, KeyError) as error:
                logging.warning('mobile follow-up deferred (%s): %s', job['status'], error)
                if job['status'] in ('unknown', 'failed') and job.get('cwd'):
                    report_failure(job, path, root)


def report_failure(job, path, root):
    notify.queue('reply-' + path.stem, {
        'type': 'agent-progress', 'thread-id': job['thread'], 'cwd': job['cwd'],
        'notification': '⚠️ Desktop follow-up acceptance is unknown or rejected\nUUID: %s\nMessage ID: %s\n'
                        'Check the desktop; no automatic replay was attempted.' %
                        (job['thread'], job['id'])}, root)
    job['reported'] = True
    notify.save(path, job)
