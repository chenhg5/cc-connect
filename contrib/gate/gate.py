# /// script
# requires-python = ">=3.12"
# dependencies = ["mlx-lm"]
# ///
"""橘的快脑：cc-connect（maomao 分支）每条用户消息先问这里，回 reply / insert / silent。

先走确定性规则，规则定不了再让本地 Qwen3.5-4B 读 A/B/C 三个选项的概率（约 30ms，见 scorer.py）。
每次判断写进 GATE_LOG（默认 ~/.cc-connect/gate.jsonl），用来调规则或以后做微调。
POST /decide  {content, mentioned, busy, has_images, has_files, buffered[]} → {action, reason}

主 + sub：橘（主，/decide）按下面的规则和 4B 决定回/插入/不回，要别人开口就 @ 她们。
sub（/decide?bot=longniang 等）听群里每句话，但只在被 @ 时回；没 @ 时空闲就记作增量（context，
下次开口一起带上），忙时由 4B 判断这句和她手上的活有没有关系：有就插入，没有就记作增量。
"""
import json, os, re, time, urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from scorer import Scorer

PORT = 9930
MODEL = os.environ.get("GATE_MODEL", "mlx-community/Qwen3.5-4B-MLX-4bit")
LOG = Path(os.environ.get("GATE_LOG", Path.home() / ".cc-connect/gate.jsonl"))

# 叫它、明确要它看/答
CALL = re.compile(r"猫猫|橘|你看看|你看下|看一下|帮我|你觉得|你怎么看|怎么看|怎么办|能不能|可以吗|为什么|是什么|吗[？?]?$|[？?]\s*$")
URL = re.compile(r"https?://\S+")
# 纯应答/语气词 → 不出声
ACK = re.compile(r"^(好的?|嗯+|哦+|噢+|ok|OK|行|收到|知道了|谢谢|谢了|thx|👌|👍|哈+|嘿+|[.。!！~～]+)$")
LINKS_FOR_REPLY = 3      # 本条 + 缓存里累计这么多链接 → 该看了
BUFFER_FOR_REPLY = 6     # 攒了这么多条没回 → 该回了

# 指令在前（每次一样，启动时缓存），状态和消息在后（每次只算这段）
PROMPT = """你是群聊里「橘」的"要不要出声"判断器。橘是群里的主 AI，背后能干活；群里还有龙娘、鲸鱼娘、彩虹猫。
根据下面的状态和消息，判断橘该怎么做：
A 不回：闲聊、语气词、自言自语、还没说完、和橘无关
B 插入：给出了新信息、新资料，或者对橘正在做的事的更正、追加、叫停，橘应该马上知道，但用户没要求回答
C 回复：用户在问橘、要橘做事或给意见、问大家的看法
只答一个字母。

"""
SUFFIX = "橘{state}。\n{history}用户刚发：「{msg}」\n\n判断（A 不回 / B 插入 / C 回复）："  # 末尾重提选项：不加的话偏向 A

ACTIONS = ["silent", "insert", "reply"]
scorer = Scorer(MODEL)
task = scorer.prepare(PROMPT, options=["A", "B", "C"], prefill="答案：")

# sub：忙时这句要不要打断她
SUBS = {"longniang": "龙娘", "jingyu": "鲸鱼娘", "caihongmao": "彩虹猫"}
SUB_PROMPT = """一个 AI 成员正在处理派给她的任务，这时群里有人发了新消息。判断这条消息怎么给她：
A 增量：普通群聊、和她手上的任务无关，先记着，等她下次开口时再一起看
B 插入：和她正在做的任务直接相关（更正、补充要求、改方向、叫停、叫她名字问进度），应该马上告诉她
群里还有橘、龙娘、鲸鱼娘、彩虹猫；叫的是别人的名字，就是在跟别人说话，不是对她。
只答一个字母。

"""
SUB_SUFFIX = "她是「{name}」，正在做的任务：「{task}」\n群里刚发：「{msg}」\n\n判断（A 增量 / B 插入）："
sub_task = scorer.prepare(SUB_PROMPT, options=["A", "B"], prefill="答案：")
# 多频道：每个频道是她的一个独立会话（share_session_in_channel），手上的活按 (bot, session_key) 记
current_task = {}  # (bot, session_key) → 最近一次被叫时的内容

# 点名：消息开头就是名字（"龙娘，帮我…" / "龙娘鲸鱼娘你们看看"）= 在叫她，直接回；
# 名字出现在句中（"龙娘刚才写的有 bug"）可能只是在说她，交给她自己的 4B 判断。
NAMES = {"longniang": ["龙娘"], "jingyu": ["鲸鱼娘", "鲸鱼", "大肥鱼"], "caihongmao": ["彩虹猫"], "": ["橘", "猫猫"]}
_ALL_NAMES = sorted((n for ns in NAMES.values() for n in ns), key=len, reverse=True)
_NAME = "(?:" + "|".join(_ALL_NAMES) + ")"
# 名字后面得是标点、结尾或呼唤词（你/帮/来…），"龙娘刚才写的…"不算叫她
LEADING = re.compile(r"^\s*" + _NAME + r"(?:[\s，,、和跟与/]*" + _NAME + r")*"
                     r"(?=$|[\s，,、:：!！?？~～.。]|你|帮|来|去|看|在|呢|快|给|说|过来|出来|有空)")
NAMED_THRESHOLD = 0.7  # 句中提到她时，4B 判"叫她"的概率要过这个线才开口（宁可漏，不和橘抢话）
NAMED_PROMPT = """群里有人发了一条消息，里面提到了某个 AI 成员的名字。判断这句话是不是在叫她回话或干活：
A 只是提到她：在跟别人说她、转述她、评价她，并没有要她回应
B 在叫她：问她、要她做事、要她表态
群里有橘、龙娘、鲸鱼娘、彩虹猫和用户。只答一个字母。

"""
NAMED_SUFFIX = "她是「{name}」。\n群里刚发：「{msg}」\n\n判断（A 只是提到 / B 在叫她）："
named_task = scorer.prepare(NAMED_PROMPT, options=["A", "B"], prefill="答案：")


def called_names(content):
    """开头连着的名字 → 被叫的成员 key 集合（"" = 橘）。"""
    m = LEADING.match(content)
    if not m:
        return set()
    lead = m.group(0)
    return {k for k, ns in NAMES.items() if any(n in lead for n in ns)}


def mentions_name(content, bot):
    return any(n in content for n in NAMES[bot])


def judge(content, busy, buffered):
    state = "正在干活（处理用户之前的请求）" if busy else "现在空闲"
    history = ""
    if buffered:
        history = "之前还没回的消息：\n" + "\n".join(f"- {b[:300]}" for b in buffered[-6:]) + "\n"
    return task.score(SUFFIX.format(state=state, history=history, msg=content[:1500]))


def decide_sub(req, bot):
    content = (req.get("content") or "").strip()
    key = (bot, req.get("session_key") or "")
    if req.get("mentioned"):
        current_task[key] = content[-500:]
        return "reply", "sub: 被@", None
    if re.search(r"<@!?&?\d+>", content):  # @ 了别人：不是对她说
        return "context", "sub: @了别人", None
    if bot in called_names(content):
        current_task[key] = content[-500:]
        return "reply", "sub: 被点名", None
    if mentions_name(content, bot):
        sc = named_task.score(NAMED_SUFFIX.format(name=SUBS[bot], msg=content[:1500]))
        if sc.probs[1] >= NAMED_THRESHOLD:
            current_task[key] = content[-500:]
            return "reply", "sub model: 提到且在叫她", sc
        if not req.get("busy"):
            return "context", "sub model: 只是提到", sc
    if not req.get("busy") or not current_task.get(key):
        return "context", "sub: 空闲，记作增量", None
    sc = sub_task.score(SUB_SUFFIX.format(name=SUBS[bot], task=current_task[key], msg=content[:1500]))
    return ("context", "insert")[sc.choice], "sub model", sc


def decide(req):
    content = (req.get("content") or "").strip()
    buffered = req.get("buffered") or []
    busy = bool(req.get("busy"))
    if req.get("mentioned"):
        return "reply", "rule: @橘", None
    if re.search(r"<@!?&?\d+>", content):  # @ 了别的 bot/人（自己的 @ 已被 cc-connect 去掉）→ 不是叫橘
        return "silent", "rule: @了别人", None
    called = called_names(content)
    if called and "" not in called:  # 开头叫的是 sub，让她自己接
        return "silent", "rule: 点名 sub", None
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
    try:
        sc = judge(content, busy, buffered)
    except ValueError as e:  # 输入超过 scorer 上限：和 cc-connect 出错时一样按 reply
        return "reply", f"error: {e}", None
    return ACTIONS[sc.choice], "model", sc


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        url = urllib.parse.urlsplit(self.path)
        bot = urllib.parse.parse_qs(url.query).get("bot", [""])[0]
        if url.path != "/decide" or (bot and bot not in SUBS):
            self.send_error(404); return
        req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        t = time.time()
        try:
            action, reason, sc = decide_sub(req, bot) if bot else decide(req)
        except ValueError as e:  # 输入超过 scorer 上限
            action, reason, sc = ("context" if bot else "reply"), f"error: {e}", None
        probs = sc.probs if sc else None
        ms = round((time.time() - t) * 1000)
        with LOG.open("a") as f:
            f.write(json.dumps({"t": time.strftime("%F %T"), "bot": bot or "橘", "busy": req.get("busy"), "mentioned": req.get("mentioned"),
                                "content": (req.get("content") or "")[:300], "buffered": len(req.get("buffered") or []),
                                "action": action, "reason": reason, "probs": probs, "ms": ms,
                                # option_mass 低 = 模型想答的不是 A/B/C（prompt 有问题）；logprobs 为全词表 log 概率
                                **({"option_mass": sc.option_mass, "logprobs": sc.logprobs,
                                    "cached_tokens": sc.cached_tokens} if sc else {})}, ensure_ascii=False) + "\n")
        body = json.dumps({"action": action, "reason": f"{reason} {probs or ''} {ms}ms".strip()}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    judge("你好", False, [])  # warm up (first forward compiles kernels)
    print(f"gate on 127.0.0.1:{PORT}", flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
