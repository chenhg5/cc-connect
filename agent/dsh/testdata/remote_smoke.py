#!/usr/bin/env python3
"""Run Remote smoke tests with a real DSH CLI and a local Messages API fixture."""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        if not self.path.endswith("/messages"):
            self.send_error(404, "Fixture uses inline attachments")
            return
        body = json.loads(raw)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()

        def send(kind, **fields):
            self.wfile.write(
                (
                    "event: "
                    + kind
                    + "\ndata: "
                    + json.dumps(dict(type=kind, **fields))
                    + "\n\n"
                ).encode()
            )
            self.wfile.flush()

        try:
            send(
                "message_start",
                message=dict(
                    id="fixture",
                    type="message",
                    role="assistant",
                    content=[],
                    model=body["model"],
                    usage=dict(input_tokens=12, output_tokens=0),
                ),
            )
            parts = body.get("messages", [{}])[-1].get("content", [])
            if isinstance(parts, str):
                parts = [dict(type="text", text=parts)]
            text = "\n".join(p.get("text", "") for p in parts)
            tool = None
            args = None
            has_result = any(p.get("type") == "tool_result" for p in parts)
            if not has_result and body.get("tools") and "DSH_FIXTURE_WRITE " in text:
                tool = "write"
                args = dict(
                    file_path=text.split("DSH_FIXTURE_WRITE ", 1)[1].strip(),
                    content="approved fixture",
                    sandbox_permissions="danger-full-access",
                    justification="Exercise the local smoke approval round trip.",
                )
            elif (
                not has_result and body.get("tools") and "DSH_FIXTURE_QUESTION" in text
            ):
                tool = "ask_user_question"
                args = dict(
                    questions=[
                        dict(
                            id="choice",
                            question="Choose value",
                            options=[dict(label="A"), dict(label="B")],
                        )
                    ]
                )
            if tool:
                send(
                    "content_block_start",
                    index=0,
                    content_block=dict(
                        type="tool_use",
                        id="fixture-" + str(time.monotonic_ns()),
                        name=tool,
                        input={},
                    ),
                )
                send(
                    "content_block_delta",
                    index=0,
                    delta=dict(type="input_json_delta", partial_json=json.dumps(args)),
                )
            else:
                send(
                    "content_block_start",
                    index=0,
                    content_block=dict(type="text", text=""),
                )
                response = (
                    "WAITING."
                    if body.get("tools") and "DSH_FIXTURE_SLOW" in text
                    else "SMOKE_OK"
                )
                if any(p.get("type") == "image" for p in parts):
                    response = "IMAGE_OK"
                send(
                    "content_block_delta",
                    index=0,
                    delta=dict(type="text_delta", text=response),
                )
                if response == "WAITING.":
                    time.sleep(5)
            send("content_block_stop", index=0)
            send(
                "message_delta",
                delta=dict(stop_reason="tool_use" if tool else "end_turn"),
                usage=dict(output_tokens=3),
            )
            send("message_stop")
        except (BrokenPipeError, ConnectionResetError):
            pass


def main():
    cli = Path(os.environ["DSH_CLI"]).resolve()
    repo = Path(__file__).resolve().parents[3]
    provider = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix="dsh-remote-smoke-") as scratch:
        scratch = Path(scratch)
        env = dict(
            os.environ,
            DSH_HOME=str(scratch / "home"),
            DEEPSEEK_API_KEY="local-fixture-key",
            DEEPSEEK_BASE_URL=f"http://127.0.0.1:{provider.server_port}",
        )
        with (scratch / "server.log").open("w+") as log:
            server = subprocess.Popen(
                [str(cli), "web", "--host", "127.0.0.1", "--port", "0"],
                env=env,
                stdout=log,
                stderr=subprocess.STDOUT,
                cwd=repo,
            )
            try:
                deadline = time.monotonic() + 60
                launch = None
                while time.monotonic() < deadline:
                    log.seek(0)
                    match = re.search(
                        r"http://127\.0\.0\.1:\d+/\?token=[^\s]+", log.read()
                    )
                    if match:
                        launch = match[0]
                        break
                    if server.poll() is not None:
                        raise RuntimeError("DSH exited before readiness")
                    time.sleep(0.1)
                if launch is None:
                    raise RuntimeError("DSH readiness timed out")
                project = scratch / "project"
                project.mkdir()
                test_env = dict(
                    env,
                    DSH_SMOKE_URL=launch,
                    DSH_SMOKE_WORKDIR=str(project),
                    DSH_SMOKE_FIXTURE="1",
                )
                go = os.environ.get("GO") or shutil.which("go")
                if not go:
                    raise RuntimeError("Set GO to the Go executable or add it to PATH")
                result = subprocess.run(
                    [
                        go,
                        "test",
                        "-tags",
                        "smoke",
                        "./agent/dsh",
                        "-run",
                        "TestSmoke_",
                        "-count=1",
                        "-timeout=2m",
                        "-v",
                    ],
                    cwd=repo,
                    env=test_env,
                )
                raise SystemExit(result.returncode)
            finally:
                server.terminate()
                try:
                    server.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait()
                provider.shutdown()


if __name__ == "__main__":
    main()
