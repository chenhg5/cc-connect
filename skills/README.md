# cc-connect Skills

Claude Code skills that ship with cc-connect. Install by symlinking or copying into your Claude Code skills directory.

## Available Skills

| Skill | Description |
|-------|-------------|
| [file-watcher](file-watcher/SKILL.md) | Install a systemd file watcher that triggers a cc-connect agent via `--as-prompt` |

## Installation

```bash
# Symlink into Claude Code skills directory
ln -s /path/to/cc-connect/skills/file-watcher ~/.claude/skills/file-watcher

# Or copy
cp -r /path/to/cc-connect/skills/file-watcher ~/.claude/skills/
```

## Custom Chat Command

You can also expose watcher management as a `/watch` chat command by adding this to your cc-connect `config.toml`:

```toml
[[projects.commands]]
name = "watch"
description = "Set up or manage a file watcher for this project"
prompt = """
The user wants to set up or manage a file watcher for this project.
Use the file-watcher skill to guide setup. Key parameters to ask about:
- Which directory to watch
- What file pattern to match (e.g. *.md, *.json)
- What prompt to send when a file arrives
- What service name to use

If the user says "status", check: systemctl status <service>.service
If the user says "logs", show: journalctl -u <service>.service --since "1 hour ago"
If the user says "stop" or "remove", guide uninstallation.
"""
```

This lets users type `/watch` in chat to trigger watcher setup or management.
