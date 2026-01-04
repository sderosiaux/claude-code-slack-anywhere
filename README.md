# Claude Code Slack Anywhere

> Control [Claude Code](https://docs.anthropic.com/en/docs/claude-code) from your phone. Each Slack channel = one project folder on your machine.

![CCSA Slack Interface](docs/slack-interface.png)

```mermaid
flowchart LR
    subgraph phone[" "]
        A[📱 Slack]
        C1[#project-a]
        C2[#project-b]
    end

    subgraph pc["Your PC"]
        B[CCSA Listener]
        D[Claude Code CLI]
        P1[~/projects/project-a]
        P2[~/projects/project-b]
    end

    A --> C1 & C2
    C1 & C2 -->|messages| B
    B -->|"claude -p --resume"| D
    D -->|runs in| P1 & P2
    B -->|"response"| A

    style A fill:#4A154B,color:#fff
    style B fill:#2D333B,color:#fff
    style D fill:#D97706,color:#fff
    style C1 fill:#4A154B,color:#fff
    style C2 fill:#4A154B,color:#fff
    style P1 fill:#1a1a2e,color:#fff
    style P2 fill:#1a1a2e,color:#fff
```

## How It Works

```
📱 Phone                              💻 Your PC
────────────────────────────────────────────────────────────

#my-webapp channel                    ~/projects/my-webapp/
├─ You: "Add dark mode toggle"
│  └─ 👀 processing...               → Claude Code runs here
│                                      → reads/writes files
├─ Claude: "I'll add a theme          → full codebase access
│  toggle to the header..."
│  └─ ✅ done
│
├─ You: "Now add tests for it"        → same session continues
│  └─ 👀                              → context preserved
│
└─ Claude: "Added 3 test cases..."
   └─ ✅
```

## Why Slack?

- **Familiar UX** - No new app to learn, works on phone/desktop
- **Shareable** - Invite teammates to project channels (if you trust them!)
- **Rich formatting** - Code blocks, threads, reactions, file uploads
- **Free tier** - Works with Slack's free plan

## Features

| Feature | Description |
|---------|-------------|
| **Multi-Project** | Each Slack channel maps to a project folder |
| **Session Memory** | Conversations persist across messages |
| **Visual Status** | 👀 processing → ✅ done (or ❌ error) |
| **Images** | Drop screenshots in Slack for Claude to analyze |
| **Interactive** | Answer Claude's questions via buttons |

## Requirements

- macOS, Linux, or Windows (WSL)
- Go 1.21+
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
| `!kill [name]` | Remove a session (name optional in session channel) |
| `!reset` | Reset Claude's conversation memory (in session channel) |
| `!list` | List active sessions |
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

## Security

**What goes where:**
- Your **code files** stay on your machine (Claude Code runs locally)
- Your **prompts and responses** go through Slack's servers
- Claude API calls go to Anthropic (handled by Claude Code)

**Safeguards:**
- Bot only responds to configured `user_id`
- Config stored with `0600` permissions
- Socket Mode (no public webhook URL)
- Open source - audit it yourself

**Heads up:**
- Uses `--dangerously-skip-permissions` for automation
- `!c <cmd>` executes shell commands - disable if you don't need it
- Anyone with access to your Slack workspace channels can see conversations

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
