# Antigravity CLI model and reasoning options

With `type = "antigravity"` in `[projects.agent]`, set these values in
`[projects.agent.options]`:

| Option | Meaning |
| --- | --- |
| `model` | Model ID or display label. Choose a value from your installed `agy models` and subscription; omit it to use the CLI default. |
| `reasoning_effort` | Optional reasoning level: `low`, `medium`, `high`, `xhigh`, or `max`. Orchestrator models do not take a separate effort setting. |
| `orchestrator_planner` | Optional planner model for Orchestrator. |
| `orchestrator_executor` | Optional executor model for Orchestrator. |

Recent releases of `agy models` expose pairs of model ID and display label
separated by a tab. When cc-connect cannot read a model catalog, you can still
enter a model name with `/model <name>`; the Antigravity CLI validates it when
the next turn starts.
