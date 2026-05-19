#!/usr/bin/env node
import readline from "node:readline";
import { stdin as input, stdout as output, stderr } from "node:process";

let sdkPromise;

function write(message) {
  output.write(JSON.stringify(message) + "\n");
}

async function loadSDK() {
  if (!sdkPromise) {
    sdkPromise = import("@cursor/sdk");
  }
  return sdkPromise;
}

const sessions = new Map();
let activeRequestID = "";

function errorMessage(err) {
  const cause = err && err.cause && err.cause.code ? ` (${err.cause.code})` : "";
  return ((err && err.message) || String(err)) + cause;
}

process.on("unhandledRejection", (err) => {
  stderr.write(`[cursor-sdk-sidecar] unhandled rejection: ${(err && err.stack) || err}\n`);
  write({ id: activeRequestID, event: "error", error: errorMessage(err) });
});

process.on("uncaughtException", (err) => {
  stderr.write(`[cursor-sdk-sidecar] uncaught exception: ${(err && err.stack) || err}\n`);
  write({ id: activeRequestID, event: "error", error: errorMessage(err) });
});

function putSession(agentID, patch) {
  const existing = sessions.get(agentID) || {};
  const next = Object.assign({}, existing, patch);
  if (next.idleTimer) clearTimeout(next.idleTimer);
  if (next.idleTtlMs && next.idleTtlMs > 0) {
    next.idleTimer = setTimeout(async () => {
      const current = sessions.get(agentID);
      if (!current) return;
      try {
        if (current.agent && current.agent[Symbol.asyncDispose]) {
          await current.agent[Symbol.asyncDispose]();
        } else if (current.agent && current.agent.close) {
          await current.agent.close();
        }
      } catch (err) {
        stderr.write(`[cursor-sdk-sidecar] idle dispose failed: ${(err && err.message) || err}\n`);
      }
      sessions.delete(agentID);
    }, next.idleTtlMs);
    if (next.idleTimer.unref) next.idleTimer.unref();
  }
  sessions.set(agentID, next);
}

function modelOption(model) {
  return model ? { id: model } : { id: "auto" };
}

function buildOptions(req) {
  const opts = {
    model: modelOption(req.model),
    local: { cwd: req.cwd },
  };
  if (req.apiKey) {
    opts.apiKey = req.apiKey;
  } else if (process.env.CURSOR_API_KEY) {
    opts.apiKey = process.env.CURSOR_API_KEY;
  }
  return opts;
}

async function createOrResume(req) {
  const { Agent } = await loadSDK();
  const wantedID = req.agentId || req.sessionId || "";
  if (wantedID && sessions.has(wantedID)) {
    return sessions.get(wantedID).agent;
  }

  const opts = buildOptions(req);
  let agent;
  if (wantedID) {
    try {
      agent = await Agent.resume(wantedID, opts);
    } catch (err) {
      if (!/not found/i.test(errorMessage(err))) {
        throw err;
      }
      stderr.write(`[cursor-sdk-sidecar] resume failed, creating fresh agent: ${errorMessage(err)}\n`);
      agent = await Agent.create(opts);
    }
  } else {
    agent = await Agent.create(opts);
  }
  const agentID = agent.agentId || agent.id || wantedID;
  if (agentID) {
    putSession(agentID, { agent, activeRun: null, idleTtlMs: 0 });
  }
  return agent;
}

function getAgentID(agent, fallback = "") {
  return agent.agentId || agent.id || fallback;
}

function extractTextFromAssistant(event) {
  const blocks = (event && event.message && event.message.content) || (event && event.content) || [];
  if (!Array.isArray(blocks)) {
    return typeof blocks === "string" ? blocks : "";
  }
  let out = "";
  for (const block of blocks) {
    if (block && block.type === "text" && typeof block.text === "string") {
      out += block.text;
    }
  }
  return out;
}

function extractToolEvent(event) {
  const name = (event && event.toolName) || (event && event.name) || (event && event.tool && event.tool.name) || "";
  const input = (event && event.input) || (event && event.args) || (event && event.toolInput) || "";
  if (!name) return null;
  return {
    name,
    input: typeof input === "string" ? input : JSON.stringify(input),
  };
}

function resultContent(result, fallback) {
  if (!result) return fallback;
  if (typeof result.result === "string") return result.result;
  if (typeof result.content === "string") return result.content;
  if (typeof result.text === "string") return result.text;
  return fallback;
}

function resultTokens(result, key) {
  const usage = (result && result.usage) || (result && result.tokenUsage) || (result && result.tokens) || {};
  const aliases = {
    input: ["inputTokens", "input_tokens", "promptTokens", "prompt_tokens"],
    output: ["outputTokens", "output_tokens", "completionTokens", "completion_tokens"],
  }[key];
  for (const alias of aliases) {
    if (Number.isFinite(usage[alias])) return usage[alias];
    if (result && Number.isFinite(result[alias])) return result[alias];
  }
  return 0;
}

function truncate(str, max) {
  if (str == null) return "";
  const s = typeof str === "string" ? str : String(str);
  if (s.length <= max) return s;
  return s.slice(0, max) + "…";
}

function safeStr(val, max = 800) {
  if (val == null) return "";
  if (typeof val === "string") return truncate(val, max);
  try {
    return truncate(JSON.stringify(val), max);
  } catch {
    return truncate(String(val), max);
  }
}

function collectStreamDiagnostics(event, diagnostics) {
  if (!event || !event.type) return;
  if (event.type === "status") {
    const st = event.status;
    if (st === "ERROR" || st === "CANCELLED" || st === "EXPIRED") {
      const m = event.message ? `: ${event.message}` : "";
      diagnostics.push(`sdk_status ${st}${m}`);
    }
    return;
  }
  if (event.type === "tool_call" && event.status === "error") {
    const tail = safeStr(event.result, 600);
    diagnostics.push(`tool_error ${event.name || "?"}${tail ? ": " + tail : ""}`);
    return;
  }
  if (event.type === "task" && event.status) {
    const st = String(event.status).toLowerCase();
    if (st.includes("fail") || st.includes("error") || st === "cancelled") {
      diagnostics.push(`task ${truncate(String(event.text || event.status), 400)}`);
    }
  }
}

function stepSummarize(step) {
  if (!step || !step.type) return "";
  if (step.type === "assistantMessage" && step.message && typeof step.message.text === "string") {
    return truncate(step.message.text.replace(/\s+/g, " ").trim(), 220);
  }
  if (step.type === "toolCall" && step.message) {
    const m = step.message;
    const tag = m.type || "tool";
    const res = m.result;
    if (res && res.status === "error") {
      return `${tag}_error:${safeStr(res.error, 400)}`;
    }
    if (res && res.status === "success" && res.value && Number.isFinite(res.value.exitCode) && res.value.exitCode !== 0) {
      const errTail = truncate((res.value.stderr || "").trim() || `exit ${res.value.exitCode}`, 200);
      return `${tag}_exit:${res.value.exitCode}:${errTail}`;
    }
    return tag;
  }
  return "";
}

function summarizeTurnsTail(turns, maxLen = 1800) {
  if (!Array.isArray(turns) || turns.length === 0) return "";
  for (let i = turns.length - 1; i >= 0; i--) {
    const t = turns[i];
    if (t && t.type === "agentConversationTurn" && t.turn && Array.isArray(t.turn.steps)) {
      const steps = t.turn.steps;
      const tail = steps
        .slice(-4)
        .map(stepSummarize)
        .filter(Boolean);
      if (tail.length) return `conversation_tail: ${truncate(tail.join(" → "), maxLen)}`;
    }
  }
  return "";
}

async function tryConversationHint(run) {
  if (!run || !run.supports || !run.supports("conversation")) return "";
  try {
    const turns = await run.conversation();
    return summarizeTurnsTail(turns);
  } catch (err) {
    return `conversation_error: ${errorMessage(err)}`;
  }
}

async function formatRunFailedError(run, result, streamDiagnostics) {
  const lines = [];
  const rid = (run && run.id) || (result && result.id) || "";
  lines.push(rid ? `run failed: ${rid}` : "run failed");
  if (result && result.result) {
    lines.push(`detail: ${truncate(result.result, 2500)}`);
  }
  if (streamDiagnostics.length) {
    lines.push(`during_stream: ${streamDiagnostics.join(" | ")}`);
  }
  const conv = await tryConversationHint(run);
  if (conv) lines.push(conv);
  const meta = {};
  if (result && result.model) {
    const mid = result.model.id || result.model;
    if (mid) meta.model = mid;
  }
  if (result && Number.isFinite(result.durationMs)) meta.durationMs = result.durationMs;
  if (Object.keys(meta).length) lines.push(`meta: ${JSON.stringify(meta)}`);
  return lines.join("\n");
}

async function doSend(req, agent, agentID) {
  const prompt = req.prompt || "";
  const sendOptions = req.mode === "force" ? { local: { force: true } } : undefined;
  const run = await agent.send(prompt, sendOptions);
  if (run && run.id) {
    write({ id: req.id, event: "run", runId: run.id, sessionId: agentID });
  }

  putSession(agentID || req.id, { agent, activeRun: run, idleTtlMs: 0 });

  let streamed = "";
  const streamDiagnostics = [];
  if (run && run.stream) {
    for await (const event of run.stream()) {
      collectStreamDiagnostics(event, streamDiagnostics);
      if (event && event.type === "assistant") {
        const text = extractTextFromAssistant(event);
        if (text) {
          streamed += text;
          write({ id: req.id, event: "text", text, sessionId: agentID });
        }
        continue;
      }
      const tool = extractToolEvent(event);
      if (tool) {
        write({
          id: req.id,
          event: "tool",
          toolName: tool.name,
          toolInput: tool.input,
          sessionId: agentID,
        });
      }
    }
  }

  const result = run && run.wait ? await run.wait() : undefined;
  return { run, result, streamDiagnostics, streamed };
}

function isSessionExpiredError(streamDiagnostics) {
  return streamDiagnostics.some((d) => d.startsWith("sdk_status ERROR") || d.startsWith("sdk_status EXPIRED"));
}

async function handleSend(req) {
  let agent;
  let agentID = "";
  activeRequestID = req.id || "";
  try {
    agent = await createOrResume(req);
    agentID = getAgentID(agent, req.agentId || req.sessionId || "");
    if (agentID) {
      putSession(agentID, { agent, idleTtlMs: 0 });
      write({ id: req.id, event: "session", sessionId: agentID });
    }

    let { run, result, streamDiagnostics, streamed } = await doSend(req, agent, agentID);

    // Session expired or invalid: retry once with a fresh agent
    if (result && result.status === "error" && isSessionExpiredError(streamDiagnostics)) {
      stderr.write(`[cursor-sdk-sidecar] session expired (${streamDiagnostics.join("|")}), retrying with fresh agent\n`);
      if (agentID) sessions.delete(agentID);
      const { Agent } = await loadSDK();
      agent = await Agent.create(buildOptions(req));
      const newID = getAgentID(agent, "");
      agentID = newID || agentID;
      if (agentID) {
        putSession(agentID, { agent, idleTtlMs: 0 });
        write({ id: req.id, event: "session", sessionId: agentID });
      }
      ({ run, result, streamDiagnostics, streamed } = await doSend(req, agent, agentID));
    }

    if (result && result.status === "error") {
      const detail = await formatRunFailedError(run, result, streamDiagnostics);
      stderr.write(`[cursor-sdk-sidecar] ${detail.replace(/\n/g, " | ")}\n`);
      write({
        id: req.id,
        event: "error",
        error: detail,
        sessionId: agentID,
      });
      if (agentID) {
        sessions.delete(agentID);
      }
      return;
    }

    write({
      id: req.id,
      event: "result",
      text: resultContent(result, streamed),
      sessionId: agentID,
      inputTokens: resultTokens(result, "input"),
      outputTokens: resultTokens(result, "output"),
    });
  } catch (err) {
    write({
      id: req.id,
      event: "error",
      error: errorMessage(err),
      sessionId: agentID,
    });
  } finally {
    if (agentID && agent) {
      putSession(agentID, { agent, activeRun: null, idleTtlMs: req.idleTtlMs || 0 });
    }
    activeRequestID = "";
  }
}

async function handleClose(req) {
  const key = req.sessionId || req.agentId;
  const entry = key ? sessions.get(key) : undefined;
  const agent = (entry && entry.agent) || entry;
  if (entry && entry.idleTimer) clearTimeout(entry.idleTimer);
  if (agent && agent[Symbol.asyncDispose]) {
    await agent[Symbol.asyncDispose]();
  } else if (agent && agent.close) {
    await agent.close();
  }
  if (key) sessions.delete(key);
  write({ id: req.id, event: "closed", sessionId: key || "" });
}

async function handleCancel(req) {
  const key = req.sessionId || req.agentId;
  const entry = key ? sessions.get(key) : undefined;
  const run = entry && entry.activeRun;
  if (run && run.supports && run.supports("cancel")) {
    await run.cancel();
  }
  write({ id: req.id, event: "cancelled", sessionId: key || "" });
}

async function handleList(req) {
  write({
    id: req.id,
    event: "list",
    sessions: [...sessions.keys()].map((sessionId) => ({ sessionId })),
  });
}

async function handle(req) {
  switch (req.op) {
    case "send":
      await handleSend(req);
      break;
    case "close":
      await handleClose(req);
      break;
    case "cancel":
      await handleCancel(req);
      break;
    case "list":
      await handleList(req);
      break;
    default:
      write({ id: req.id, event: "error", error: `unknown op: ${req.op}` });
  }
}

async function main() {
  const rl = readline.createInterface({ input, crlfDelay: Infinity });
  for await (const line of rl) {
    if (!line.trim()) continue;
    let req;
    try {
      req = JSON.parse(line);
    } catch (err) {
      write({ id: "", event: "error", error: `invalid json: ${err.message}` });
      continue;
    }
    try {
      await handle(req);
    } catch (err) {
      stderr.write(`[cursor-sdk-sidecar] ${(err && err.stack) || err}\n`);
      write({ id: req.id || "", event: "error", error: (err && err.message) || String(err) });
    }
  }
}

main().catch((err) => {
  stderr.write(`[cursor-sdk-sidecar] fatal: ${(err && err.stack) || err}\n`);
  process.exit(1);
});
