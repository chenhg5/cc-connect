# Queue receipt notices

`queue_notice` is a per-project setting, enabled by default. Set it to false in the project's existing `[[projects]]` block to queue busy messages silently:

```toml
[[projects]]
name = "my-project"
queue_notice = false
```

Messages are still processed in their original order. A full queue is reported even when queue notices are disabled. Projects that omit the setting continue to acknowledge queued messages. A configuration reload applies the setting to subsequent messages.

## 排队回执

`queue_notice` 是项目级开关，默认开启。在对应项目的 `[[projects]]` 内设置 `queue_notice = false`，即可关闭排队回执。消息仍按到达顺序处理；队列已满时仍会提示。未设置该开关的项目仍会发送排队回执。重载配置后，后续消息使用新设置。
