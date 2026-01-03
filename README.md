# ccc - Claude Code Companion

> Control [Claude Code](https://claude.ai/claude-code) remotely via Slack. Start sessions from your phone, interact with Claude, and receive notifications when tasks complete.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Why ccc?

Ever wanted to:
- Start a Claude Code session from your phone while away from your computer?
- Continue a session seamlessly between your phone and PC?
- Get notified when Claude finishes a long-running task?

**ccc** bridges Claude Code with Slack, letting you control sessions from anywhere.

## Features

- **100% Self-Hosted** - Runs entirely on your machine, no third-party servers
- **Privacy First** - Your code and conversations never leave your computer
- **Remote Control** - Start and manage Claude Code sessions from Slack
- **Multi-Session** - Run multiple concurrent sessions, each with its own Slack channel
- **Seamless Handoff** - Start on phone, continue on PC (or vice versa)
- **Interactive Buttons** - Answer Claude's questions with Block Kit buttons
- **Notifications** - Get Claude's responses in Slack when away
- **tmux Integration** - Sessions persist and can be attached from any terminal

## Demo Workflow

```
Slack (phone/desktop)           PC (Terminal)
───────────────────────────────────────────────────
1. !new myproject
   → Creates #myproject channel + session

2. "Fix the auth bug"
   → Claude starts working

3. Claude responds in channel
   :white_check_mark: myproject
   Fixed the auth bug by...

                                4. cd ~/myproject && ccc
                                   → Attaches to same session

                                5. Continue working with Claude
```

## Requirements

- macOS, Linux, or Windows (WSL)
- Go 1.21+
- [tmux](https://github.com/tmux/tmux)
- [Claude Code](https://claude.ai/claude-code) installed
- Slack workspace (free tier works!)

## Installation

### From Source

```bash
git clone https://github.com/sderosiaux/claude-slack-relay-anywhere.git
cd claude-slack-relay-anywhere
go mod tidy
make install
```

### Verify Installation

```bash
ccc --version
# ccc version 2.0.0
```

## Quick Start

### 1. Create a Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App**
2. Choose **From scratch**, name it (e.g., "Claude Companion"), select your workspace

3. **Enable Socket Mode:**
   - Go to **Socket Mode** in the sidebar
   - Toggle it ON
   - Create an App-Level Token with `connections:write` scope
   - Save the token (starts with `xapp-...`)

4. **Add Bot Scopes:**
   - Go to **OAuth & Permissions**
   - Under **Bot Token Scopes**, add:
     - `channels:manage` - Create channels
     - `channels:history` - Read messages
     - `channels:read` - List channels
     - `chat:write` - Send messages
     - `users:read` - Get user info

5. **Subscribe to Events:**
   - Go to **Event Subscriptions**
   - Toggle ON
   - Under **Subscribe to bot events**, add:
     - `message.channels` - Messages in public channels

6. **Enable Interactivity:**
   - Go to **Interactivity & Shortcuts**
   - Toggle ON (no URL needed for Socket Mode)

7. **Install to Workspace:**
   - Go to **Install App**
   - Click **Install to Workspace**
   - Copy the **Bot User OAuth Token** (starts with `xoxb-...`)

### 2. Run Setup

```bash
ccc setup xoxb-YOUR-BOT-TOKEN xapp-YOUR-APP-TOKEN
```

When prompted, enter your Slack User ID:
- In Slack, click your profile picture
- Click **Profile**
- Click **...** (more) → **Copy member ID**

### 3. Start Using

```bash
cd ~/myproject
ccc
```

That's it! You're ready to control Claude Code from Slack.

## Usage

### Terminal Commands

| Command | Description |
|---------|-------------|
| `ccc` | Start/attach Claude session in current directory |
| `ccc -c` | Continue previous session |
| `ccc "message"` | Send notification (if away mode on) |
| `ccc doctor` | Check all dependencies |
| `ccc --help` | Show help |

### Slack Commands

Type these in any channel where the bot is present:

| Command | Description |
|---------|-------------|
| `!new <name>` | Create new session + channel |
| `!continue <name>` | Continue existing session |
| `!kill <name>` | Kill a session |
| `!list` | List active sessions |
| `!ping` | Check if bot is alive |
| `!away` | Toggle away mode |
| `!c <cmd>` | Run shell command on your machine |

**In a project channel:**
- Just type messages - they go directly to Claude

**Interactive buttons:**
- When Claude asks questions, buttons appear in Slack
- Click to select your answer

### Example Session

```bash
# On your PC - start working on a project
cd ~/myproject
ccc
# Claude session starts in tmux

# Later, from phone - check on progress
# Slack: Send message in #myproject channel
# Claude responds in the channel

# Back on PC - continue where you left off
cd ~/myproject
ccc
# Attaches to existing session
```

## Configuration

Config is stored in `~/.ccc.json`:

```json
{
  "bot_token": "xoxb-your-bot-token",
  "app_token": "xapp-your-app-token",
  "user_id": "U01234567",
  "sessions": {
    "myproject": "C01234567",
    "another-project": "C89012345"
  },
  "away": false
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Slack Bot User OAuth Token (xoxb-...) |
| `app_token` | Slack App-Level Token (xapp-...) |
| `user_id` | Your Slack member ID (for authorization) |
| `sessions` | Map of session names to channel IDs |
| `away` | When true, notifications are sent |

## How It Works

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│    Slack    │────▶│     ccc     │────▶│    tmux     │
│   (phone)   │◀────│   listen    │◀────│   session   │
└─────────────┘     └─────────────┘     └─────────────┘
      ▲                    │                   │
      │                    │                   ▼
      │              Socket Mode         ┌─────────────┐
      └──────────────────────────────────│ Claude Code │
                    hooks                └─────────────┘
```

1. `ccc listen` runs as a service, connected via Slack Socket Mode
2. Messages in channels are forwarded to the corresponding tmux session
3. Claude Code runs inside tmux with hooks that send responses back
4. Interactive buttons (Block Kit) handle Claude's questions
5. You can attach to any session from terminal with `ccc`

## Privacy & Security

### Privacy

**ccc runs 100% on your machine.** There are no external servers, no analytics, no data collection.

- Your code stays on your computer
- Claude Code runs locally via Anthropic's official CLI
- Only messages you explicitly send go through Slack
- No telemetry, no tracking, no cloud dependencies

The only external communication is:
1. **Slack API** - For sending/receiving your messages (your workspace, your control)
2. **Anthropic API** - Claude Code's own connection (handled by Claude Code itself)

### Security

- **Authorization**: Bot only accepts messages from the configured `user_id`
- **Config permissions**: `~/.ccc.json` is created with `0600` (owner-only)
- **Socket Mode**: No public URL needed, connection initiated from your machine
- **Open source**: Full code transparency, audit it yourself

> Note: Uses `--dangerously-skip-permissions` for automation - understand the implications

## Troubleshooting

Run `ccc doctor` to check all dependencies and configuration.

**Bot not responding?**
- Check if `ccc listen` is running
- Verify tokens in `~/.ccc.json`
- Check logs: `tail -f ~/.ccc.log`

**Session not starting?**
- Ensure tmux is installed: `which tmux`
- Check if Claude Code is installed: `which claude`

**Messages not reaching Claude?**
- Verify you're in a session channel
- Check if session exists: `!list`
- Try restarting: `!new <name>`

**Socket Mode connection issues?**
- Verify App Token has `connections:write` scope
- Check Event Subscriptions are enabled
- Restart: `launchctl unload ~/Library/LaunchAgents/com.ccc.plist && launchctl load ~/Library/LaunchAgents/com.ccc.plist`

## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Run tests: `go test ./...`
4. Submit a PR

## License

[MIT License](LICENSE) - feel free to use in your projects!

---

Made with Claude Code
