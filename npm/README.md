# cc-connect-next

Bridge local AI coding agents (Claude Code, Cursor, Gemini CLI, Codex) to messaging platforms (Feishu/Lark, DingTalk, Slack, Telegram, Discord, LINE, WeChat Work).

Chat with your AI dev assistant from anywhere.

## Install

Install the stable channel:

```bash
npm install -g cc-connect-next
```

Opt into the public beta channel explicitly:

```bash
npm install -g cc-connect-next@beta
```

The installer verifies the matching GitHub Release archive against `checksums.txt` before extraction.

Update an existing npm or standalone installation to the latest stable release:

```bash
cc-connect-next update
```

## Usage

```bash
# Create config
cc-connect-next --version

# Edit config.toml, then run
cc-connect-next
cc-connect-next --config /path/to/config.toml
```

## Documentation

See full documentation at: https://github.com/timmyagentic/cc-connect-next
