"""本地 4B 打分：一段固定前缀 + 每次变的输入，一次前向读几个单 token 选项的概率。

    scorer = Scorer(MODEL)
    task = scorer.prepare(prefix="…指令…", options=["A", "B", "C"], prefill="答案：")
    task.score("…这次的状态和消息…")
    → Score(choice=1, probs=[…], logprobs=[…], option_mass=0.97, cached_tokens=119, input_tokens=39, ms=28.1)

提示词 = chat 模板(user = prefix + input，关思考) + prefill。prepare 时算好 prefix 的 KV/线性注意力状态，
score 时拷一份接着算 input 那段。Qwen3.5 的线性注意力状态不能回退，所以只能这样"从固定位置接着算"。
"""
import copy, threading, time
from dataclasses import dataclass

import mlx.core as mx
from mlx_lm import load
from mlx_lm.models.cache import make_prompt_cache


@dataclass
class Score:
    choice: int            # probs 最大的选项下标
    probs: list            # 只在这几个选项里归一化
    logprobs: list         # 全词表 log 概率
    option_mass: float     # 这几个选项占全词表的概率之和；低说明模型想答的不是这些选项
    cached_tokens: int
    input_tokens: int      # 本次实际算的 token 数
    ms: float


class Scorer:
    def __init__(self, model_path, max_input_tokens=2048):
        self.model, self.tok = load(model_path)
        self.max_input_tokens = max_input_tokens
        self.lock = threading.Lock()  # mlx 前向串行

    def prompt_ids(self, text, prefill=""):
        return self.tok.encode(self.tok.apply_chat_template([{"role": "user", "content": text}], add_generation_prompt=True,
                                                            tokenize=False, enable_thinking=False) + prefill)

    def prepare(self, prefix, options, prefill=""):
        return Task(self, prefix, options, prefill)


class Task:
    def __init__(self, scorer, prefix, options, prefill):
        self.s, self.prefix, self.prefill = scorer, prefix, prefill
        self.options = list(options)
        self.opt_ids = []
        for o in self.options:
            ids = scorer.tok.encode(o, add_special_tokens=False)
            if len(ids) != 1:
                raise ValueError(f"选项 {o!r} 不是单 token：{ids}")
            self.opt_ids.append(ids[0])
        # 缓存的 token 前缀 = 两个不同输入的提示词共同的开头（不单独 encode 前缀，避免分词边界对不上）
        a, b = scorer.prompt_ids(prefix + "\x00", prefill), scorer.prompt_ids(prefix + "\x01", prefill)
        self.ids = a[:next(i for i, (x, y) in enumerate(zip(a, b)) if x != y)]
        with scorer.lock:
            self.cache = make_prompt_cache(scorer.model)
            mx.eval(scorer.model(mx.array(self.ids)[None], cache=self.cache))
            mx.eval([c.state for c in self.cache])

    def score(self, text, use_cache=True):
        t = time.perf_counter()
        ids = self.s.prompt_ids(self.prefix + text, self.prefill)
        n = len(self.ids)
        hit = use_cache and ids[:n] == self.ids
        todo = ids[n:] if hit else ids
        if len(todo) > self.s.max_input_tokens:
            raise ValueError(f"输入 {len(todo)} token，超过上限 {self.s.max_input_tokens}")
        with self.s.lock:
            cache = copy.deepcopy(self.cache) if hit else None
            logits = self.s.model(mx.array(todo)[None], cache=cache)[0, -1].astype(mx.float32)
            lp = (logits - mx.logsumexp(logits))[mx.array(self.opt_ids)]
            mx.eval(lp)
        logprobs = [round(float(x), 4) for x in lp]
        mass = float(mx.exp(lp).sum())
        probs = [round(float(x), 3) for x in mx.softmax(lp)]
        return Score(choice=max(range(len(probs)), key=probs.__getitem__), probs=probs, logprobs=logprobs,
                     option_mass=round(mass, 3), cached_tokens=n if hit else 0, input_tokens=len(todo),
                     ms=round((time.perf_counter() - t) * 1000, 1))
