# /// script
# requires-python = ">=3.12"
# dependencies = ["mlx-lm"]
# ///
"""scorer 回归测试：uv run contrib/gate/test_scorer.py（会再加载一份 4B，约 3GB，十几秒）。"""
import os, sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from scorer import Scorer

MODEL = os.environ.get("GATE_MODEL", "mlx-community/Qwen3.5-4B-MLX-4bit")
PREFIX = "判断用户这句话是 A 闲聊、B 提供信息、还是 C 提问。只答一个字母。\n\n"
INPUTS = ["哈哈哈今天好开心", "补充一下，接口是 v2 版本的", "这个怎么部署？", "等等我再找找", "英伟达财报超预期"]
fails = []


def check(name, ok, detail=""):
    print(("ok   " if ok else "FAIL ") + name + (f"  {detail}" if detail else ""))
    if not ok:
        fails.append(name)


s = Scorer(MODEL, max_input_tokens=64)
t = s.prepare(PREFIX, ["A", "B", "C"], prefill="答案：")
check("前缀被缓存", len(t.ids) > 10, f"{len(t.ids)} tok")

# 1. 用缓存 vs 整段重算：交替不同输入，结果一致（bf16 误差内）
first = {}
for rnd in range(2):
    for x in INPUTS + INPUTS[::-1]:
        a, b = t.score(x), t.score(x, use_cache=False)
        diff = max(abs(p - q) for p, q in zip(a.probs, b.probs))
        margin = sorted(b.probs)[-1] - sorted(b.probs)[-2]
        check(f"缓存=重算 {x!r}", a.cached_tokens > 0 and diff < 0.06 and (a.choice == b.choice or margin < 0.06),
              f"{a.probs} vs {b.probs}")
        # 2. 缓存不被污染：同一输入无论前面跑过什么，结果完全相同
        if x in first:
            check(f"缓存未被污染 {x!r}", a.logprobs == first[x].logprobs, f"{a.logprobs} vs {first[x].logprobs}")
        else:
            first[x] = a

# 3. 字段自洽
sc = t.score(INPUTS[0])
check("probs 归一化", abs(sum(sc.probs) - 1) < 0.01, str(sc.probs))
check("option_mass 在 (0,1]", 0 < sc.option_mass <= 1.001, str(sc.option_mass))
check("logprobs ≤ 0", all(x <= 0 for x in sc.logprobs), str(sc.logprobs))

# 4. 多 token 选项报错
try:
    s.prepare(PREFIX, ["闲聊一下", "B"])
    check("多 token 选项报错", False)
except ValueError as e:
    check("多 token 选项报错", True, str(e))

# 5. 超长输入报错
try:
    t.score("很长的消息" * 100)
    check("超长输入报错", False)
except ValueError as e:
    check("超长输入报错", True, str(e))

print(f"\n{'全部通过' if not fails else f'{len(fails)} 项失败'}")
sys.exit(1 if fails else 0)
