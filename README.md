# Claude Code Slack Anywhere

> Control [Claude Code](https://claude.ai/claude-code) remotely via Slack. Start sessions from your phone, interact with Claude, and receive notifications when tasks complete.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Why?

Ever wanted to:
- Start a Claude Code session from your phone while away from your computer?
- Get notified when Claude finishes a long-running task?
- Control Claude Code without being at your desk?

**Claude Code Slack Anywhere** bridges Claude Code with Slack, letting you control sessions from anywhere.

## Features

- **100% Self-Hosted** - Runs entirely on your machine, no third-party servers
- **Privacy First** - Your code and conversations never leave your computer
- **Remote Control** - Start and manage Claude Code sessions entirely from Slack
- **Live Streaming** - See Claude's output in real-time as a single updating message
- **Reaction Status** - Visual feedback: 👀 (processing) → ✅ (done)
- **Multi-Session** - Run multiple concurrent sessions, each with its own Slack channel
- **Interactive Buttons** - Answer Claude's questions with Block Kit buttons
- **tmux Integration** - Sessions persist in tmux

## Demo Workflow

```
Slack (phone/desktop)
─────────────────────────────────────
1. !new myproject
   → Creates #myproject channel + Claude session

2. "Fix the auth bug"
   → 👀 reaction appears on your message
   → Claude's response streams to thread

3. Response complete
   → ✅ reaction replaces 👀
   → Full output visible in thread

4. Continue chatting in the channel
   → Each message goes to Claude
   → Responses stream back
```

## Requirements

- macOS, Linux, or Windows (WSL)
- Go 1.21+
- [tmux](https://github.com/tmux/tmux)
- [Claude Code](https://claude.ai/claude-code) installed
- Slack workspace (free tier works!)

## Installation

```bash
git clone https://github.com/sderosiaux/claude-code-slack-anywhere.git
cd claude-code-slack-anywhere
go build -o claude-code-slack-anywhere .
mv claude-code-slack-anywhere ~/bin/  # or anywhere in PATH
```

## Quick Start

### 1. Create a Slack App

Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**

| Setting | Location | Value |
|---------|----------|-------|
| Socket Mode | Socket Mode | **ON** + create token with `connections:write` → save `xapp-...` |
| Bot Scopes | OAuth & Permissions | `channels:manage`, `channels:history`, `channels:read`, `chat:write`, `reactions:write`, `users:read` |
| Events | Event Subscriptions | **ON** + add `message.channels` |
| Interactivity | Interactivity & Shortcuts | **ON** |
| Install | Install App | Click install → copy `xoxb-...` token |

> **Important:** `reactions:write` is required for the 👀/✅ status indicators

### 2. Run Setup

```bash
claude-code-slack-anywhere setup xoxb-YOUR-BOT-TOKEN xapp-YOUR-APP-TOKEN
```

Get your User ID: Slack → Profile → **...** → **Copy member ID**

### 3. Start the Listener

```bash
claude-code-slack-anywhere listen
```

Keep this running (or [set up as a service](#running-as-a-service-macos)). That's it! Now control Claude entirely from Slack.

## Usage

### Slack Commands

Type these in any channel where the bot is present:

| Command | Description |
|---------|-------------|
| `!new <name>` | Create new session + channel |
| `!continue [name]` | Continue existing session (name optional in session channel) |
| `!kill [name]` | Kill a session (name optional in session channel) |
| `!list` | List active sessions |
| `!output [lines]` | Capture Claude's screen (default: 100 lines) |
| `!ping` | Check if bot is alive |
| `!help` | Show all commands |
| `!c <cmd>` | Run shell command on your machine |

### In a Session Channel

| Input | Description |
|-------|-------------|
| Any message | Sent directly to Claude |
| `//help` | Claude's `/help` command (use `//` because Slack intercepts `/`) |
| `//compact` | Claude's `/compact` command |
| `//clear` | Claude's `/clear` command |

> **Note:** Use double-slash `//` for Claude slash commands. Single `/` is intercepted by Slack.

### Reaction Status

When you send a message in a session channel:

| Reaction | Meaning |
|----------|---------|
| 👀 | Message received, Claude is processing |
| ✅ | Claude finished successfully |
| 🛑 | Session ended |
| ❌ | Error occurred |

## Configuration

Config is stored in `~/.ccsa.json`:

```json
{
  "bot_token": "xoxb-your-bot-token",
  "app_token": "xapp-your-app-token",
  "user_id": "U01234567",
  "projects_dir": "~/Desktop/ai-projects",
  "sessions": {
    "myproject": "C01234567"
  }
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Slack Bot User OAuth Token (xoxb-...) |
| `app_token` | Slack App-Level Token (xapp-...) |
| `user_id` | Your Slack member ID (for authorization) |
| `projects_dir` | Base directory for projects (default: `~/Desktop/ai-projects`) |
| `sessions` | Map of session names to channel IDs |

## How It Works

```
┌─────────────┐     Socket Mode      ┌─────────────┐     send-keys     ┌─────────────┐
│    Slack    │◄────────────────────►│   Listener  │─────────────────►│    tmux     │
│   (phone)   │                      │             │                   │   session   │
└─────────────┘                      │             │                   └──────┬──────┘
      ▲                              │             │                          │
      │                              │             │◄── capture-pane ─────────┤
      │                              │             │    (poll every 2s)       ▼
      │                              └─────────────┘                   ┌─────────────┐
      │                                    │                           │ Claude Code │
      └────────────────────────────────────┘                           └─────────────┘
              updateMessage() with streamed output
```

1. `claude-code-slack-anywhere listen` connects to Slack via Socket Mode
2. `!new` creates a tmux session running Claude Code
3. Messages in session channels are forwarded to tmux
4. Listener polls tmux output every 2 seconds
5. New output is streamed back as an updating Slack message
6. Reactions indicate status (👀 → ✅)

## Privacy & Security

### Privacy

**This tool runs 100% on your machine.** There are no external servers, no analytics, no data collection.

- Your code stays on your computer
- Claude Code runs locally via Anthropic's official CLI
- Only messages you explicitly send go through Slack
- No telemetry, no tracking, no cloud dependencies

The only external communication is:
1. **Slack API** - For sending/receiving your messages (your workspace, your control)
2. **Anthropic API** - Claude Code's own connection (handled by Claude Code itself)

### Security

- **Authorization**: Bot only accepts messages from the configured `user_id`
- **Config permissions**: `~/.ccsa.json` is created with `0600` (owner-only)
- **Socket Mode**: No public URL needed, connection initiated from your machine
- **Open source**: Full code transparency, audit it yourself

> Note: Uses `--dangerously-skip-permissions` for automation - understand the implications

## Running as a Service (macOS)

Create `~/Library/LaunchAgents/com.ccsa.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ccsa</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_USERNAME/bin/claude-code-slack-anywhere</string>
        <string>listen</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOUR_USERNAME/.ccsa.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOUR_USERNAME/.ccsa.log</string>
</dict>
</plist>
```

Then:
```bash
launchctl load ~/Library/LaunchAgents/com.ccsa.plist
```

## Contributing

Contributions welcome! See [TODO.md](TODO.md) for planned features.

## License

[MIT License](LICENSE)

---

Made with Claude Code
