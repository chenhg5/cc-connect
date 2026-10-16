#!/usr/bin/python3
"""Codex desktop completion callback and durable notification worker."""
import argparse
import hashlib
import fcntl
import http.client
import json
import logging
import os
from pathlib import Path
import socket
import sqlite3
import sys
import tempfile
import threading
import time

ROOT = Path(os.environ.get("CC_CONNECT_DESKTOP_STATE_DIR", str(Path.home() / ".cc-connect" / "codex-desktop"))).expanduser()
CODEX_HOME = Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex")))


class WeChatThrottled(OSError):
    pass


def is_root_desktop(thread):
    databases = list(CODEX_HOME.glob("state_*.sqlite"))
    if not databases:
        raise OSError("Codex thread database is unavailable")
    database = max(databases, key=lambda p: int(p.stem.split("_")[-1]))
    with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as connection:
        row = connection.execute("SELECT source FROM threads WHERE id = ?", (thread,)).fetchone()
    return row is not None and row[0] == "vscode"


def save(path, data):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as f:
        tmp = Path(f.name)
        json.dump(data, f, ensure_ascii=False)
    tmp.replace(path)


def queue(key, event, root=ROOT):
    with (root / "delivery.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        path = root / "outbox" / (key + ".json")
        if path.exists() or (root / "done" / key).exists():
            return False
        save(path, event)
        return True


def enqueue(event, root=ROOT):
    if event.get("type") != "agent-turn-complete" or event.get("client") not in ("Codex Desktop", "codex_desktop"):
        return False
    thread, directory = event.get("thread-id"), event.get("cwd")
    if not isinstance(thread, str) or not thread or not isinstance(directory, str) or not Path(directory).is_absolute():
        raise ValueError("invalid thread or directory in completion callback")
    if not is_root_desktop(thread):
        return False
    turn = event.get("turn-id")
    key = hashlib.sha256(json.dumps([thread, turn or event], sort_keys=True).encode()).hexdigest()
    return queue(key, event, root)


def post(body, root=ROOT):
    connection = http.client.HTTPConnection("localhost", timeout=45)
    connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.sock.settimeout(45)
    try:
        connection.sock.connect(json.loads((root / "settings.json").read_text())["api_socket"])
        connection.request("POST", "/send", body=json.dumps(body).encode(),
                           headers={"Content-Type": "application/json"})
        response = connection.getresponse()
        detail = response.read()
        if response.status != 200:
            if b"ret=-2" in detail:
                raise WeChatThrottled("WeChat rejected proactive sending (ret=-2); cooling down")
            raise OSError("cc-connect send status %s" % response.status)
    finally:
        connection.close()


def drain(root=ROOT):
    with (root / "delivery.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        _drain(root)


def _drain(root):
    cooldown = root / "send-cooldown.json"
    if cooldown.exists() and json.loads(cooldown.read_text())["retry_after"] > time.time():
        return
    settings = json.loads((root / "settings.json").read_text())
    jobs = sorted((root / "outbox").glob("*.json"), key=lambda p: p.stat().st_mtime_ns)
    for path in jobs:
        if (root / "done" / path.stem).exists():
            path.unlink()
            continue
        try:
            event = json.loads(path.read_text())
            if not isinstance(event, dict):
                raise ValueError("notification must be an object")
            if event.get('human-key') and not (root / 'pending' / (event['human-key'] + '.json')).exists():
                path.unlink()
                continue
            kind = {"agent-progress": "progress", "agent-turn-complete": "completed", "human-input-required": "request"}[event["type"]]
            if not event["thread-id"] or not Path(event["cwd"]).is_absolute():
                raise ValueError("invalid thread or directory")
            body = {"project": settings["project"], "session_key": settings["session_key"],
                    "reply_thread_id": event["thread-id"], "work_dir": event["cwd"],
                    "desktop_event": kind,
                    "message": str(event.get("notification") or event.get("last-assistant-message") or "")}
            if kind == 'request':
                body['desktop_request_id'] = event['human-key']
        except (ValueError, KeyError, TypeError):
            (root / "invalid").mkdir(exist_ok=True, mode=0o700)
            path.replace(root / "invalid" / path.name)
            logging.warning("invalid notification quarantined")
            continue
        try:
            post(body, root)
        except WeChatThrottled as error:
            # ponytail: one account-wide cooldown; per-account state if adding bots.
            save(cooldown, {"retry_after": time.time() + 3600})
            logging.warning("WeChat sending paused for one hour: %s", error)
            break
        except (OSError, http.client.HTTPException) as error:
            logging.warning("delivery deferred for turn %s: %s", event.get("turn-id"), error)
            continue
        refs = root / "threads.json"
        threads = json.loads(refs.read_text()) if refs.exists() else {}
        threads[event["thread-id"]] = {k: v for k, v in body.items() if k not in ("message", "desktop_event", "desktop_request_id")}
        save(refs, threads)
        (root / "done").mkdir(exist_ok=True, mode=0o700)
        (root / "done" / path.stem).touch(mode=0o600)
        path.unlink()
        logging.info("delivered %s %s", event["type"], event.get("human-key") or event.get("turn-id"))


def configure(project, session_key, api_socket, root=ROOT):
    if not project or not session_key or not Path(api_socket).is_absolute():
        raise ValueError("project, session key and absolute API socket are required")
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if root.stat().st_uid != os.getuid() or root.stat().st_mode & 0o077:
        raise ValueError("desktop state directory must be owner-only (mode 700)")
    settings = root / "settings.json"
    enabled = json.loads(settings.read_text()).get("enabled_at_ms", time.time() * 1000) if settings.exists() else time.time() * 1000
    if settings.exists():
        previous = json.loads(settings.read_text())
        if (previous["project"], previous["session_key"]) != (project, session_key):
            raise ValueError("use a new state directory for a different destination")
    save(settings, {"project": project, "session_key": session_key,
                    "api_socket": api_socket, "enabled_at_ms": enabled})


def worker(root=ROOT):
    import desktop
    import replies
    threading.Thread(target=desktop.watch, daemon=True, name="desktop-listener").start()
    restored_socket = None
    while True:
        try:
            settings = json.loads((root / "settings.json").read_text())
            refs = root / "threads.json"
            stat = Path(settings["api_socket"]).stat()
            socket_id = (stat.st_ino, stat.st_ctime_ns)
            if refs.exists() and restored_socket != socket_id:
                restored = True
                for body in json.loads(refs.read_text()).values():
                    try:
                        post(body, root)
                    except (OSError, http.client.HTTPException):
                        restored = False
                        logging.warning("desktop route registration deferred")
                if restored:
                    restored_socket = socket_id
            replies.drain(root)
            drain(root)
        except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException):
            logging.exception("desktop delivery worker will retry")
        time.sleep(3)


def main():
    os.umask(0o077)
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command")
    config = commands.add_parser("configure")
    config.add_argument("--project", required=True)
    config.add_argument("--session-key", required=True)
    config.add_argument("--api-socket", default=str(Path.home() / ".cc-connect" / "run" / "api.sock"))
    commands.add_parser("watch")
    callback = commands.add_parser("notify")
    callback.add_argument("event")
    args = parser.parse_args()
    if args.command == "configure":
        configure(args.project, args.session_key, args.api_socket)
    elif args.command == "watch":
        worker()
    elif args.command == "notify":
        enqueue(json.loads(args.event))
    else:
        parser.error("choose configure, watch or notify")


if __name__ == "__main__":
    main()
