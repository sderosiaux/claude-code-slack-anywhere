# claudeslack

> Control [Claude Code](https://docs.anthropic.com/en/docs/claude-code) from your phone via Slack. Each channel = one project folder on your machine.

**[Website](https://sderosiaux.github.io/claudeslack)** · **[GitHub](https://github.com/sderosiaux/claudeslack)**

![claudeslack Interface](docs/slack-interface.png)

```mermaid
flowchart LR
    subgraph phone[" "]
        A[📱 Slack]
        C1[#project-a]
        C2[#project-b]
    end

    subgraph pc["Your PC"]
        B[claudeslack]
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

## Why Slack? (vs SSH + tmux)

| SSH + tmux | claudeslack |
|------------|-------------|
| ✅ No extra dependencies | ✅ **Push notifications when done** |
| ✅ Direct terminal access | ✅ **Fire & forget** - check later |
| ✅ Full session control | ✅ **Unread threads = task inbox** |
| ❌ Requires VPN/Tailscale | ✅ **Drop images/screenshots** directly |
| ❌ Terminal on phone = pain | ✅ Voice input while walking |
| ❌ Must poll for completion | ✅ Native mobile UX (swipe, scroll) |
| ❌ No notifications when done | ❌ Adds Slack as dependency |
| ❌ scp/rsync for files = friction | ❌ Data goes through Slack servers |

**Use SSH** if you have Tailscale and don't mind terminal on mobile.
**Use claudeslack** if you want to fire off tasks and get notified when done.

### Other Slack benefits

- **Familiar UX** - No new app to learn
- **Shareable** - Invite teammates to project channels (if you trust them!)
- **Rich formatting** - Code blocks, threads, reactions, file uploads
- **Free tier** - Works with Slack's free plan

## Features

| Feature | Description |
|---------|-------------|
| **Multi-Project** | Each Slack channel maps to a project folder |
| **Session Memory** | Conversations persist across messages |
| **Visual Status** | 👀 processing → ✅ done (or ❌ error) |
| **File Uploads** | Drop images or code files - saved to project's `.slack-uploads/` folder |
| **Interactive** | Answer Claude's questions via buttons |

## Requirements

- macOS, Linux, or Windows (WSL)
- Go 1.21+
- [Claude Code](https://claude.ai/claude-code) installed
- Slack workspace (free tier works!)

## Installation

```bash
git clone https://github.com/sderosiaux/claudeslack.git
cd claudeslack
go build -o claudeslack .
mv claudeslack ~/bin/  # or anywhere in PATH
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
claudeslack setup xoxb-YOUR-BOT-TOKEN xapp-YOUR-APP-TOKEN
```

Get your User ID: Slack → Profile → **...** → **Copy member ID**

### 3. Start the Listener

```bash
claudeslack listen
```

Or with CLI options (override config file):
```bash
claudeslack listen \
  --config ~/.ccsa.json \
  --projects-dir ~/code/ai-projects \
  --bot-token xoxb-... \
  --app-token xapp-... \
  --user-ids U03UHMKRX,U12345678
```

Keep this running (or [set up as a service](#running-as-a-service-macos)). That's it! Now control Claude entirely from Slack.

## Usage

### Slack Commands

Type these in any channel where the bot is present:

| Command | Description |
|---------|-------------|
| `!new <name>` | Create new session + channel |
| `!kill` | Remove session and archive channel (use in any channel) |
| `!reset` | Reset Claude's conversation memory |
| `!list` | List active sessions |
| `!ping` | Check if bot is alive |
| `!help` | Show all commands |
| `!c <cmd>` | Run shell command on your machine |

### In a Session Channel

| Input | Description |
|-------|-------------|
| Any message | Sent directly to Claude |
| `!claude_compact` | Summarize conversation (reduce tokens) |
| `!claude_clear` | Clear session and start fresh |
| `!claude_help` | Show Claude-specific commands |

### Auto-Session Detection

No need to use `!new` if a project folder already exists. Just send a message in a Slack channel that matches a folder name in your `projects_dir`:

```
Slack channel: #my-cool-project
Project folder: ~/code/ai-projects/my-cool-project  (or "my cool project")
→ Auto-detected! Session starts automatically.
```

The bot handles hyphen/space conversion (Slack uses hyphens, folders may use spaces).

### File Uploads

Drop files directly in Slack messages - they're automatically downloaded to your project and passed to Claude:

```
You: [attaches screenshot.png] "What's wrong with this error?"
→ File saved to ~/projects/my-webapp/.slack-uploads/screenshot.png
→ Claude analyzes the image and responds

You: [attaches config.yaml] "Review this config"
→ File saved and content passed to Claude
```

- **Images** (PNG, JPG, GIF, WebP) - Claude sees them visually
- **Code/text files** - Content is read and included in the prompt
- Files persist in `.slack-uploads/` so Claude can reference them later
- Visible in Slack AND accessible in your workspace

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
  "user_ids": ["U01234567", "U98765432"],
  "projects_dir": "~/code/ai-projects"
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Slack Bot User OAuth Token (xoxb-...) |
| `app_token` | Slack App-Level Token (xapp-...) |
| `user_ids` | Authorized Slack member IDs (array) |
| `projects_dir` | **Required.** Base directory for projects |

> **Note:** `user_id` (singular string) is still supported for backward compatibility.

## Security & Threat Model

### What Actually Happens to Your Data

| Data | Where it goes |
|------|---------------|
| Your prompt | Slack servers → Your machine |
| File contents Claude reads | Your machine → Anthropic API |
| Claude's response | Anthropic → Your machine → Slack servers |
| File writes | Local only |

**Translation:** Your code *files* stay local, but their *contents* go to Anthropic when Claude reads them. Your prompts and responses are stored in Slack's infrastructure.

### What This Is

- A weekend hack, not enterprise software
- Convenience tool for personal use
- Uses `--dangerously-skip-permissions`
- Single-user, single-machine design

### Safeguards

- Allowlist of Slack user IDs
- Config stored with `0600` permissions
- Socket Mode (no public webhook URL)
- Open source - audit the code

### Don't Use For

- ❌ Production codebases
- ❌ Code under NDA
- ❌ Client work with confidentiality clauses
- ❌ Shared workspaces without consent

### Other Risks

- `!c <cmd>` executes shell commands - disable if you don't need it
- Anyone with access to your Slack channels can see conversations

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
