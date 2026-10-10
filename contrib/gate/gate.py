# /// script
# requires-python = ">=3.12"
# dependencies = ["mlx-lm"]
# ///
"""猫猫的快脑：cc-connect（maomao 分支）每条用户消息先问这里，回 reply / insert / silent。

先走确定性规则，规则定不了再让本地 Qwen3.5-4B 读 A/B/C 三个选项的概率（约 30ms）。
每次判断写进 GATE_LOG（默认 ~/.cc-connect/gate.jsonl），用来调规则或以后做微调。
POST /decide  {content, mentioned, busy, has_images, has_files, buffered[]} → {action, reason}
"""
import json, os, re, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import mlx.core as mx
from mlx_lm import load

PORT = 9930
MODEL = os.environ.get("GATE_MODEL", "mlx-community/Qwen3.5-4B-MLX-4bit")
LOG = Path(os.environ.get("GATE_LOG", Path.home() / ".cc-connect/gate.jsonl"))

# 叫它、明确要它看/答
CALL = re.compile(r"猫猫|你看看|你看下|看一下|帮我|你觉得|你怎么看|怎么看|怎么办|能不能|可以吗|为什么|是什么|吗[？?]?$|[？?]\s*$")
URL = re.compile(r"https?://\S+")
# 纯应答/语气词 → 不出声
ACK = re.compile(r"^(好的?|嗯+|哦+|噢+|ok|OK|行|收到|知道了|谢谢|谢了|thx|👌|👍|哈+|嘿+|[.。!！~～]+)$")
LINKS_FOR_REPLY = 3      # 本条 + 缓存里累计这么多链接 → 该看了
BUFFER_FOR_REPLY = 6     # 攒了这么多条没回 → 该回了

PROMPT = """你是聊天助手「猫猫」的"要不要出声"判断器。用户在跟猫猫聊天，猫猫背后是一个能干活的 AI。
猫猫{state}。
{history}用户刚发：「{msg}」

判断猫猫该怎么做：
A 不回：闲聊、语气词、自言自语、还没说完、和猫猫无关
B 插入：给出了新信息、新观点、新资料或补充/更正，猫猫应该知道，但用户没要求回答
C 回复：用户在问猫猫、要猫猫做事或给意见

只答一个字母。"""

lock = threading.Lock()
model, tok = load(MODEL)
OPTS = [tok.encode(k, add_special_tokens=False)[0] for k in "ABC"]
ACTIONS = ["silent", "insert", "reply"]


def judge(content, busy, buffered):
    state = "正在干活（处理用户之前的请求）" if busy else "现在空闲"
    history = ""
    if buffered:
        history = "之前还没回的消息：\n" + "\n".join(f"- {b}" for b in buffered[-6:]) + "\n"
    text = PROMPT.format(state=state, history=history, msg=content[:1500])
    p = tok.apply_chat_template([{"role": "user", "content": text}], add_generation_prompt=True,
                                tokenize=False, enable_thinking=False) + "答案："
    ids = mx.array(tok.encode(p))[None]
    with lock:
        probs = mx.softmax(model(ids)[0, -1][mx.array(OPTS)])
        mx.eval(probs)
    probs = [round(float(x), 3) for x in probs]
    return ACTIONS[max(range(3), key=probs.__getitem__)], probs


def decide(req):
    content = (req.get("content") or "").strip()
    buffered = req.get("buffered") or []
    busy = bool(req.get("busy"))
    if req.get("mentioned"):
        return "reply", "rule: @猫猫", None
    if content.startswith("/"):
        return "reply", "rule: command", None
    if ACK.match(content):
        return "silent", "rule: 应答", None
    if CALL.search(content):
        return "reply", "rule: 叫我/提问", None
    links = len(URL.findall(content)) + sum(len(URL.findall(b)) for b in buffered)
    if links >= LINKS_FOR_REPLY:
        return "reply", f"rule: {links} 个链接", None
    if links and not URL.sub("", content).strip():
        return "silent", f"rule: 只有链接，等凑够 {LINKS_FOR_REPLY} 个", None
    if len(buffered) + 1 >= BUFFER_FOR_REPLY:
        return "reply", f"rule: 攒了 {len(buffered) + 1} 条", None
    if not content:
        return "insert", "rule: 只有附件", None
    action, probs = judge(content, busy, buffered)
    return action, "model", probs


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/decide":
            self.send_error(404); return
        req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        t = time.time()
        action, reason, probs = decide(req)
        ms = round((time.time() - t) * 1000)
        with LOG.open("a") as f:
            f.write(json.dumps({"t": time.strftime("%F %T"), "busy": req.get("busy"), "mentioned": req.get("mentioned"),
                                "content": (req.get("content") or "")[:300], "buffered": len(req.get("buffered") or []),
                                "action": action, "reason": reason, "probs": probs, "ms": ms}, ensure_ascii=False) + "\n")
        body = json.dumps({"action": action, "reason": f"{reason} {probs or ''} {ms}ms".strip()}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    judge("你好", False, [])  # warm up (first forward compiles kernels)
    print(f"gate on 127.0.0.1:{PORT}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
