# Claude Code Slack Anywhere

> Control [Claude Code](https://claude.ai/claude-code) remotely via Slack. Start sessions from your phone, interact with Claude, and receive notifications when tasks complete.

![CCSA Slack Interface](docs/slack-interface.png)

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

```mermaid
flowchart LR
    subgraph phone[" "]
        A[📱 Slack]
    end

    subgraph pc["Your PC"]
        B[CCSA Listener]
        C[tmux]
        D[Claude Code]
    end

    A -->|"!new project"| B
    A -->|"Fix the bug"| B
    B -->|send-keys| C
    C --- D
    D -->|output| C
    C -->|capture-pane| B
    B -->|"stream response"| A

    style A fill:#4A154B,color:#fff
    style B fill:#2D333B,color:#fff
    style C fill:#1a1a2e,color:#fff
    style D fill:#D97706,color:#fff
```

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
- **Auto-Session Detection** - Send a message in any channel matching a project folder → auto-starts
- **Image Support** - Drop images in Slack to send them to Claude for analysis
- **tmux Integration** - Sessions persist in tmux

## Demo Workflow

```
Slack (phone/desktop)
─────────────────────────────────────
1. !new todo-app
   → Creates #todo-app channel + Claude session

2. "Build a REST API with user auth"
   → 👀 reaction appears on your message
   → Claude's response streams to thread
   → ✅ when done

3. "Add unit tests"
   → Claude continues in the same session
   → Full context preserved
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
| Bot Scopes | OAuth & Permissions | `channels:manage`, `channels:history`, `channels:read`, `chat:write`, `files:read`, `reactions:write`, `users:read` |
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

### Auto-Session Detection

No need to use `!new` if a project folder already exists. Just send a message in a Slack channel that matches a folder name in your `projects_dir`:

```
Slack channel: #my-cool-project
Project folder: ~/Desktop/ai-projects/my-cool-project  (or "my cool project")
→ Auto-detected! Session starts automatically.
```

The bot handles hyphen/space conversion (Slack uses hyphens, folders may use spaces).

### Image Support

Drop images directly in Slack messages. They're downloaded and passed to Claude for analysis:

```
You: [attaches screenshot.png] "What's wrong with this error?"
→ Claude analyzes the image and responds
```

Supported formats: PNG, JPG, GIF, WebP

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
  "projects_dir": "~/Desktop/ai-projects"
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Slack Bot User OAuth Token (xoxb-...) |
| `app_token` | Slack App-Level Token (xapp-...) |
| `user_id` | Your Slack member ID (for authorization) |
| `projects_dir` | Base directory for projects (default: `~/Desktop/ai-projects`) |

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

```bash
./install-service.sh
```

This will:
- Build the binary (if needed)
- Install to `~/bin/`
- Configure and start the launchd service
- Auto-restart on crash or login

**Useful commands:**
```bash
tail -f ~/.ccsa.log                           # View logs
launchctl kickstart -k gui/$(id -u)/com.ccsa  # Restart
launchctl unload ~/Library/LaunchAgents/com.ccsa.plist  # Stop
```

## Contributing

Contributions welcome! See [TODO.md](TODO.md) for planned features.

## License

[MIT License](LICENSE)
