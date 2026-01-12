package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

const version = "2.0.0"

// buildTime is set at compile time via -ldflags
var buildTime = "dev"

// User prompt prefix - context for each message
// NOTE: Critical rules are duplicated here because --append-system-prompt may not be respected on resumed sessions
const slackUserPrefix = `[REMOTE via Slack - I cannot see your screen or open files locally. Please show relevant output/content in your responses.]

BEFORE running any server/daemon command, YOU MUST use pm2 or screen. NEVER run npm run dev, npm start, node server.js directly - use: pm2 start "npm run dev" --name app

`

// System prompt additions via --append-system-prompt
const SlackSystemPromptAppend = `
CRITICAL - Process execution rules:
1. NEVER run blocking/long-running processes directly (servers, watchers, tails, etc.)
   - Running "node server.js" or "npm start" will BLOCK the entire session
   - The user will be unable to send more messages until timeout (10 min)

2. ALWAYS use process managers for servers/daemons:
   - pm2 start server.js --name myserver
   - screen -dmS myserver node server.js
   - tmux new-session -d -s myserver 'node server.js'

3. Background processes with & or nohup will be KILLED when session ends

4. To check if a server is running: pm2 list, screen -ls, or curl the endpoint

FORBIDDEN COMMANDS (these will block or die immediately):
- npm run dev, npm start, npm run serve
- node server.js, python app.py, go run main.go
- Any command with & at the end (background processes die on exit)
- tail -f, watch, nodemon, webpack --watch

USE INSTEAD:
- pm2 start "npm run dev" --name myapp
- screen -dmS myapp npm run dev
- tmux new-session -d -s myapp 'npm run dev'
`

// Global config manager, worker pool and message queue
var (
	configMgr    *ConfigManager
	workerPool   *WorkerPool
	messageQueue *ChannelQueue
)

func logf(format string, args ...interface{}) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func getHelpText() string {
	return "*claudeslack - Commands*\n\n" +
		":rocket: *Session Management*\n" +
		"• `!new <name>` - Create new session with channel\n" +
		"• `!reset` - Reset conversation context (start fresh)\n" +
		"• `!kill` - Remove and archive current session\n" +
		"• `!sessions` - List active sessions\n" +
		"• `!projects` - List projects in projects folder\n\n" +
		":computer: *Utilities*\n" +
		"• `!c <cmd>` - Execute shell command\n" +
		"• `!cancel` - Cancel running task\n" +
		"• `!verbose` / `!quiet` - Toggle output verbosity\n\n" +
		":alarm_clock: *Scheduled Tasks*\n" +
		"• `!at <time> <cmd>` - Schedule a task (e.g., `!at 5m run tests`)\n" +
		"• `!scheduled` - List scheduled tasks\n" +
		"• `!unschedule <id>` - Cancel a scheduled task\n\n" +
		":information_source: *Other*\n" +
		"• `!ping` - Check if bot is alive\n" +
		"• `!version` - Show version\n" +
		"• `!help` - Show this help\n\n" +
		":speech_balloon: *In a session channel:*\n" +
		"• Type messages → Claude responds in channel\n" +
		"• `!task <prompt>` - Start a fresh task in a thread\n" +
		"• `!fork <prompt>` - Fork session into a thread (keeps context)\n" +
		"• `!claude_compact` - Summarize conversation (reduce tokens)\n" +
		"• `!claude_clear` - Clear session and start fresh\n" +
		"• `!claude_help` - Show Claude-specific commands"
}

type listenOpts struct {
	configPath  string
	projectsDir string
	botToken    string
	appToken    string
	userIDs     []string
}

// Main listen loop using Socket Mode
func listen(opts listenOpts) error {
	myPid := os.Getpid()
	logf("Starting v%s (build: %s) PID %d", version, buildTime, myPid)

	cmd := exec.Command("pgrep", "-f", "claude-code-slack-anywhere listen")
	output, _ := cmd.Output()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if pid, err := strconv.Atoi(line); err == nil && pid != myPid {
			logf("Killing old instance (PID %d)", pid)
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}

	// Initialize config manager
	configMgr = NewConfigManager(opts.configPath)
	if err := configMgr.Load(); err != nil {
		// If no config file and no CLI tokens, fail
		if opts.botToken == "" || opts.appToken == "" {
			return fmt.Errorf("not configured. Run: claude-code-slack-anywhere setup <bot_token> <app_token>")
		}
		// Create minimal config from CLI args
		configMgr.config = &Config{Sessions: make(map[string]string)}
	}

	config := configMgr.Get()

	// CLI overrides take precedence
	if opts.projectsDir != "" {
		config.ProjectsDir = opts.projectsDir
	}
	if opts.botToken != "" {
		config.BotToken = opts.botToken
	}
	if opts.appToken != "" {
		config.AppToken = opts.appToken
	}
	if len(opts.userIDs) > 0 {
		config.UserIDs = opts.userIDs
	}

	// Validate mandatory config
	if config.ProjectsDir == "" {
		return fmt.Errorf("projects_dir is required: use --projects-dir or set in config file")
	}
	if config.BotToken == "" {
		return fmt.Errorf("bot_token is required: use --bot-token or set in config file")
	}
	if config.AppToken == "" {
		return fmt.Errorf("app_token is required: use --app-token or set in config file")
	}
	logf("Bot listening... (user: %s)", config.UserID)
	logf("Active sessions: %d", len(configMgr.GetAllSessions()))
	fmt.Println("Press Ctrl+C to stop")

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize worker pool (max 50 concurrent handlers)
	workerPool = NewWorkerPool(ctx, 50)

	// Initialize message queue for automatic queuing
	messageQueue = NewChannelQueue()

	// Initialize scheduler for !at commands
	scheduler = NewScheduler(config)

	// WaitGroup for background goroutines
	var wg sync.WaitGroup

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logf("Received signal: %v - Shutting down gracefully...", sig)
		cancel() // Cancel context to stop all goroutines

		// Wait for workers to finish (with timeout)
		done := make(chan struct{})
		go func() {
			workerPool.Wait()
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			logf("Graceful shutdown complete")
		case <-time.After(30 * time.Second):
			logf("Shutdown timeout, forcing exit")
		}
		os.Exit(0)
	}()

	// Connect via Socket Mode
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := connectSocketMode(ctx, configMgr); err != nil {
			fmt.Fprintf(os.Stderr, "Socket Mode error: %v (reconnecting in 5s...)\n", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func connectSocketMode(ctx context.Context, cfgMgr *ConfigManager) error {
	config := cfgMgr.Get()

	// Get WebSocket URL
	req, err := newRequest("POST", "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.AppToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var connResult SlackResponse
	json.NewDecoder(resp.Body).Decode(&connResult)

	if !connResult.OK {
		return fmt.Errorf("failed to open connection: %s", connResult.Error)
	}

	wsURL := connResult.URL
	// Connect WebSocket
	ws, err := websocket.Dial(wsURL, "", "https://slack.com")
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer ws.Close()

	var wsMutex sync.Mutex

	// Handle messages
	for {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			logf("WebSocket: Context cancelled, closing connection")
			return nil
		default:
		}

		var envelope SocketModeEnvelope
		if err := websocket.JSON.Receive(ws, &envelope); err != nil {
			return fmt.Errorf("websocket receive failed: %w", err)
		}

		// Acknowledge envelope
		if envelope.EnvelopeID != "" {
			ack := map[string]string{"envelope_id": envelope.EnvelopeID}
			wsMutex.Lock()
			websocket.JSON.Send(ws, ack)
			wsMutex.Unlock()
		}

		switch envelope.Type {
		case "hello":
			logf("Socket Mode connected")

		case "events_api":
			var eventCallback EventCallback
			json.Unmarshal(envelope.Payload, &eventCallback)

			if eventCallback.Type == "event_callback" {
				// Use worker pool for bounded concurrency
				workerPool.Submit(func() {
					handleSlackEvent(ctx, cfgMgr, eventCallback.Event)
				})
			}

		case "interactive":
			var action BlockActionPayload
			json.Unmarshal(envelope.Payload, &action)
			workerPool.Submit(func() {
				handleBlockAction(cfgMgr.Get(), action)
			})

		case "disconnect":
			return fmt.Errorf("disconnected by server")
		}
	}
}

func handleSlackEvent(ctx context.Context, cfgMgr *ConfigManager, eventData json.RawMessage) {
	var event struct {
		Type     string      `json:"type"`
		Subtype  string      `json:"subtype"`
		Channel  string      `json:"channel"`
		User     string      `json:"user"`
		Text     string      `json:"text"`
		TS       string      `json:"ts"`
		ThreadTS string      `json:"thread_ts"`
		BotID    string      `json:"bot_id"`
		Files    []SlackFile `json:"files"`
	}
	json.Unmarshal(eventData, &event)

	// Debug: log raw event when files are present
	if len(event.Files) > 0 {
		logf("DEBUG: Event with files: %s", string(eventData))
	}

	// Ignore bot messages
	if event.BotID != "" {
		return
	}

	// Ignore system messages (joins, leaves, topic changes, etc.)
	// But allow file_share subtype (when user uploads a file with a message)
	if event.Subtype != "" && event.Subtype != "file_share" {
		return
	}

	// Get current config (thread-safe)
	config := cfgMgr.Get()
	if config == nil {
		logf("Config not loaded")
		return
	}

	// Only accept from authorized user
	if !config.IsAuthorizedUser(event.User) {
		return
	}

	if event.Type != "message" {
		return
	}

	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}

	channelID := event.Channel
	threadTS := event.ThreadTS

	logf("[message] @%s in %s: %s", event.User, channelID, text)

	// Helper to reply in thread if we're in a thread, otherwise in channel
	reply := func(msg string) {
		if threadTS != "" {
			sendMessageToThread(config, channelID, threadTS, msg)
		} else {
			sendMessage(config, channelID, msg)
		}
	}

	// Handle commands
	if strings.HasPrefix(text, "!ping") {
		reply("pong!")
		return
	}

	if strings.HasPrefix(text, "!version") {
		reply(fmt.Sprintf("v%s (build: %s)", version, buildTime))
		return
	}

	if strings.HasPrefix(text, "!help") {
		reply(getHelpText())
		return
	}

	// !task <prompt> - creates a thread for the task (original behavior)
	if strings.HasPrefix(text, "!task ") {
		taskPrompt := strings.TrimSpace(strings.TrimPrefix(text, "!task "))
		if taskPrompt == "" {
			reply("Usage: `!task <prompt>` - start a task in a thread")
			return
		}

		// Check if we're in a session channel
		sessionName := cfgMgr.GetSessionByChannel(channelID)
		if sessionName == "" {
			// Try auto-detect from channel name
			channelName, err := getChannelName(config, channelID)
			if err == nil && channelName != "" {
				baseDir := getProjectsDir(config)
				projectDir := filepath.Join(baseDir, channelName)
				if _, err := os.Stat(projectDir); os.IsNotExist(err) {
					altName := strings.ReplaceAll(channelName, "-", " ")
					altDir := filepath.Join(baseDir, altName)
					if _, err := os.Stat(altDir); err == nil {
						projectDir = altDir
						sessionName = altName
					}
				} else {
					sessionName = channelName
				}
				if sessionName != "" {
					cfgMgr.SetSession(sessionName, channelID)
				}
			}
		}

		if sessionName == "" {
			reply(":x: Not in a session channel. Use in a channel that matches a project folder.")
			return
		}

		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, sessionName)

		addReaction(config, channelID, event.TS, "eyes")
		prompt := slackUserPrefix + taskPrompt

		workerPool.Submit(func() {
			// Pass event.TS as threadTS to create a thread
			resp, err := callClaudeStreaming(prompt, channelID, event.TS, workDir, config)
			if err != nil {
				logf("Claude error: %v", err)
				addReaction(config, channelID, event.TS, "x")
				removeReaction(config, channelID, event.TS, "eyes")
				sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Claude error: %v", err))
				return
			}
			removeReaction(config, channelID, event.TS, "eyes")
			addReaction(config, channelID, event.TS, "white_check_mark")
			logf("Claude responded (session: %s, tokens: %d in / %d out)",
				resp.SessionID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		})
		return
	}

	// !fork <prompt> - fork current session into a new thread
	if strings.HasPrefix(text, "!fork ") {
		// Only works from channel, not from thread
		if threadTS != "" {
			reply(":x: `!fork` only works from the main channel, not from a thread")
			return
		}

		forkPrompt := strings.TrimSpace(strings.TrimPrefix(text, "!fork "))
		if forkPrompt == "" {
			reply("Usage: `!fork <prompt>` - fork session into a new thread")
			return
		}

		// Check if we're in a session channel
		sessionName := cfgMgr.GetSessionByChannel(channelID)
		if sessionName == "" {
			reply(":x: Not in a session channel. Use `!fork` in a session channel.")
			return
		}

		// Check if there's an active session to fork from
		if _, ok := getClaudeSessionID(channelID); !ok {
			reply(":x: No active session to fork. Start a conversation first.")
			return
		}

		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, sessionName)

		addReaction(config, channelID, event.TS, "twisted_rightwards_arrows")
		prompt := slackUserPrefix + forkPrompt

		workerPool.Submit(func() {
			sendMessageToThread(config, channelID, event.TS, ":twisted_rightwards_arrows: *Forked session* - continuing with full context in this thread")

			resp, err := callClaudeStreamingForked(prompt, channelID, event.TS, workDir, config, channelID)
			if err != nil {
				logf("Claude fork error: %v", err)
				addReaction(config, channelID, event.TS, "x")
				removeReaction(config, channelID, event.TS, "twisted_rightwards_arrows")
				sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Fork error: %v", err))
				return
			}
			removeReaction(config, channelID, event.TS, "twisted_rightwards_arrows")
			addReaction(config, channelID, event.TS, "white_check_mark")
			logf("Forked session completed (new session: %s, tokens: %d in / %d out)",
				resp.SessionID, resp.Usage.InputTokens, resp.Usage.OutputTokens)
		})
		return
	}

	if strings.HasPrefix(text, "!sessions") || strings.HasPrefix(text, "!list") {
		sessions := cfgMgr.GetAllSessions()
		if len(sessions) == 0 {
			reply("No active sessions")
		} else {
			var list []string
			for name, cid := range sessions {
				list = append(list, fmt.Sprintf("• `%s` → <#%s>", name, cid))
			}
			reply("*Active Sessions:*\n" + strings.Join(list, "\n"))
		}
		return
	}

	if strings.HasPrefix(text, "!reset") {
		// Reset Claude conversation context for this channel
		sessionName := cfgMgr.GetSessionByChannel(channelID)
		if sessionName == "" {
			reply(":x: Not in a session channel. Use `!reset` in a session channel.")
			return
		}
		resetClaudeSession(channelID)
		reply(":arrows_counterclockwise: Conversation reset! Next message starts a fresh context.")
		return
	}

	if text == "!kill" {
		name := cfgMgr.GetSessionByChannel(channelID)
		// Reset Claude session ID and remove from config if exists
		resetClaudeSession(channelID)
		if name != "" {
			cfgMgr.DeleteSession(name)
		}
		// Archive the channel
		if err := archiveChannel(config, channelID); err != nil {
			logf("Failed to archive channel: %v", err)
			if name != "" {
				reply(fmt.Sprintf(":wastebasket: Session '%s' removed (channel archive failed: %v)", name, err))
			} else {
				reply(fmt.Sprintf(":x: Channel archive failed: %v", err))
			}
		} else {
			if name != "" {
				reply(fmt.Sprintf(":wastebasket: Session '%s' removed and channel archived", name))
			} else {
				reply(":wastebasket: Channel archived")
			}
		}
		return
	}

	if text == "!cancel" {
		if CancelClaudeProcess(channelID) {
			reply(":stop_sign: Task cancelled")
		} else {
			reply(":shrug: No task running in this channel")
		}
		return
	}

	if text == "!verbose" {
		SetVerbose(channelID, true)
		reply(":loud_sound: Verbose mode ON - showing all tool calls")
		return
	}

	if text == "!quiet" {
		SetVerbose(channelID, false)
		reply(":mute: Quiet mode ON - only showing writes and errors")
		return
	}

	// !at <time> <command> - schedule a task
	if strings.HasPrefix(text, "!at ") {
		parts := strings.SplitN(strings.TrimPrefix(text, "!at "), " ", 2)
		if len(parts) < 2 {
			reply("Usage: `!at <time> <command>`\nExamples:\n• `!at 5m run tests`\n• `!at 9am deploy to prod`\n• `!at tomorrow 10am npm run build`")
			return
		}
		timeSpec := parts[0]
		command := parts[1]

		// Get work directory for this channel
		sessionName := cfgMgr.GetSessionByChannel(channelID)
		if sessionName == "" {
			reply(":x: Not in a session channel. Use `!at` in a session channel.")
			return
		}
		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, sessionName)

		taskID, runAt, err := scheduler.Schedule(channelID, threadTS, workDir, timeSpec, command)
		if err != nil {
			reply(fmt.Sprintf(":x: Invalid time: %v", err))
			return
		}

		reply(fmt.Sprintf(":alarm_clock: Scheduled `%s` for *%s* (task: `%s`)",
			command, runAt.Format("Mon Jan 2 15:04"), taskID))
		return
	}

	// !scheduled - list scheduled tasks
	if text == "!scheduled" {
		tasks := scheduler.List(channelID)
		if len(tasks) == 0 {
			reply(":calendar: No scheduled tasks for this channel")
			return
		}
		var lines []string
		for _, t := range tasks {
			lines = append(lines, fmt.Sprintf("• `%s` at *%s*: `%s`",
				t.ID, t.RunAt.Format("Mon 15:04"), t.Command))
		}
		reply(":calendar: *Scheduled tasks:*\n" + strings.Join(lines, "\n"))
		return
	}

	// !unschedule <task-id> - cancel a scheduled task
	if strings.HasPrefix(text, "!unschedule ") {
		taskID := strings.TrimPrefix(text, "!unschedule ")
		if scheduler.Cancel(taskID) {
			reply(fmt.Sprintf(":wastebasket: Cancelled task `%s`", taskID))
		} else {
			reply(fmt.Sprintf(":shrug: Task `%s` not found", taskID))
		}
		return
	}

	if strings.HasPrefix(text, "!c ") {
		cmdStr := strings.TrimPrefix(text, "!c ")
		output, err := executeCommand(cmdStr)
		if err != nil {
			output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
		}
		reply("```\n" + output + "\n```")
		return
	}

	if strings.HasPrefix(text, "!projects") {
		baseDir := getProjectsDir(config)
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			reply(fmt.Sprintf(":x: Cannot read projects dir: %v", err))
			return
		}
		var projects []string
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				projects = append(projects, "• `"+entry.Name()+"`")
			}
		}
		if len(projects) == 0 {
			reply(fmt.Sprintf("No projects in `%s`", baseDir))
		} else {
			reply(fmt.Sprintf("*Projects in `%s`:*\n%s", baseDir, strings.Join(projects, "\n")))
		}
		return
	}

	if strings.HasPrefix(text, "!new ") {
		arg := strings.TrimSpace(strings.TrimPrefix(text, "!new "))
		if arg == "" {
			sendMessage(config, channelID, "Usage: `!new <name>` - create a new session")
			return
		}

		// Create channel if needed
		var targetChannelID string
		isNewChannel := false
		if cid, exists := cfgMgr.GetSession(arg); exists {
			targetChannelID = cid
		} else {
			cid, err := createChannel(config, arg)
			if err != nil {
				sendMessage(config, channelID, fmt.Sprintf(":x: Failed to create channel: %v", err))
				return
			}
			targetChannelID = cid
			if err := cfgMgr.SetSession(arg, cid); err != nil {
				logf("Failed to save session: %v", err)
			}
			isNewChannel = true
		}

		// Send immediate feedback with channel link
		if isNewChannel {
			sendMessage(config, channelID, fmt.Sprintf(":sparkles: Created <#%s> for `%s`", targetChannelID, arg))
		} else {
			sendMessage(config, channelID, fmt.Sprintf(":arrow_right: Using existing <#%s>", targetChannelID))
		}

		// Find or create work directory
		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, arg)
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			if err := os.MkdirAll(workDir, 0755); err != nil {
				sendMessage(config, targetChannelID, fmt.Sprintf(":x: Failed to create directory %s: %v", workDir, err))
				return
			}
			sendMessage(config, targetChannelID, fmt.Sprintf(":file_folder: Created `%s`", workDir))
		} else {
			sendMessage(config, targetChannelID, fmt.Sprintf(":open_file_folder: Using existing `%s`", workDir))
		}

		logf("Session created: %s (dir: %s)", arg, workDir)
		sendMessage(config, targetChannelID, fmt.Sprintf(":rocket: Session '%s' ready!\n\nSend messages here to interact with Claude.", arg))
		return
	}

	// Unknown ! command (except !claude_* which is handled below in session context)
	if strings.HasPrefix(text, "!") && !strings.HasPrefix(text, "!claude_") {
		logf("Unknown command: %s", text)
		reply(fmt.Sprintf(":question: Unknown command `%s`\n\n%s", strings.Split(text, " ")[0], getHelpText()))
		return
	}

	// Check if message is in a session channel
	sessionName := cfgMgr.GetSessionByChannel(channelID)
	if sessionName != "" {
		logf("Session found: %s", sessionName)

		// Handle !claude_* commands (Claude Code slash commands)
		if strings.HasPrefix(text, "!claude_") {
			claudeCmd := strings.TrimPrefix(text, "!claude_")
			switch claudeCmd {
			case "compact":
				addReaction(config, channelID, event.TS, "hourglass_flowing_sand")
				workerPool.Submit(func() {
					baseDir := getProjectsDir(config)
					workDir := filepath.Join(baseDir, sessionName)
					resp, err := callClaudeStreaming("/compact", channelID, event.TS, workDir, config)
					removeReaction(config, channelID, event.TS, "hourglass_flowing_sand")
					if err != nil {
						addReaction(config, channelID, event.TS, "x")
						sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Compact failed: %v", err))
					} else {
						addReaction(config, channelID, event.TS, "white_check_mark")
						sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":broom: *Conversation compacted!*\nNew context: %d tokens", resp.Usage.InputTokens))
					}
				})
				return
			case "clear":
				resetClaudeSession(channelID)
				addReaction(config, channelID, event.TS, "white_check_mark")
				sendMessageToThread(config, channelID, event.TS, ":wastebasket: *Session cleared!* Next message starts fresh.")
				return
			case "help":
				helpMsg := "*Claude Commands:*\n" +
					"• `!claude_compact` - Summarize conversation to reduce context size\n" +
					"• `!claude_clear` - Clear session and start fresh\n" +
					"• `!claude_help` - Show this help"
				sendMessageToThread(config, channelID, event.TS, helpMsg)
				return
			case "raw":
				// Get last response raw (no formatting)
				addReaction(config, channelID, event.TS, "eyes")
				workerPool.Submit(func() {
					baseDir := getProjectsDir(config)
					workDir := filepath.Join(baseDir, sessionName)
					// Ask Claude to repeat last response
					resp, err := callClaudeJSON("Please repeat your last response exactly as you wrote it, without any changes.", channelID, workDir)
					removeReaction(config, channelID, event.TS, "eyes")
					if err != nil {
						addReaction(config, channelID, event.TS, "x")
						sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Error: %v", err))
					} else {
						addReaction(config, channelID, event.TS, "white_check_mark")
						// Send raw response in code block (no markdown conversion)
						sendMessageToThread(config, channelID, event.TS, "```\n"+resp.Result+"\n```")
					}
				})
				return
			default:
				sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":question: Unknown command `!claude_%s`. Try `!claude_help`", claudeCmd))
				return
			}
		}

		addReaction(config, channelID, event.TS, "eyes")
		claudeText := text

		// Find work directory first (needed for file uploads)
		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, sessionName)
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			if err := os.MkdirAll(workDir, 0755); err != nil {
				logf("Failed to create directory %s: %v", workDir, err)
				addReaction(config, channelID, event.TS, "x")
				removeReaction(config, channelID, event.TS, "eyes")
				reply(fmt.Sprintf(":x: Failed to create directory: %v", err))
				return
			}
		}

		// Handle file attachments (images and text files)
		// Save them in workDir/.slack-uploads/ so Claude can access them
		var filePaths []string
		if len(event.Files) > 0 {
			uploadsDir := filepath.Join(workDir, ".slack-uploads")
			os.MkdirAll(uploadsDir, 0755)

			for _, file := range event.Files {
				logf("File attached: name=%s mimetype=%s filetype=%s", file.Name, file.Mimetype, file.Filetype)
				if isImageFile(file) || isTextFile(file) {
					logf("Downloading file: %s (%s)", file.Name, file.Mimetype)
					localPath, err := downloadSlackFileToDir(config, file, uploadsDir)
					if err != nil {
						logf("Failed to download file %s: %v", file.Name, err)
						reply(fmt.Sprintf(":warning: Failed to download file %s: %v", file.Name, err))
						continue
					}
					filePaths = append(filePaths, localPath)
					logf("Saved file to: %s", localPath)
				} else {
					logf("Skipping unsupported file type: %s (mimetype=%s, filetype=%s)", file.Name, file.Mimetype, file.Filetype)
				}
			}
		}

		// Build prompt with file paths (images and text files)
		if len(filePaths) > 0 {
			fileList := strings.Join(filePaths, " ")
			if claudeText == "" {
				claudeText = fmt.Sprintf("Please analyze these files: %s", fileList)
			} else {
				claudeText = fmt.Sprintf("%s\n\nAttached file(s): %s", claudeText, fileList)
			}
			logf("Added %d file(s) to prompt", len(filePaths))
		}

		// Add remote context to help Claude understand the user's situation
		prompt := slackUserPrefix + claudeText

		// Determine threadTS: if already in a thread, continue there; otherwise respond in channel
		// threadTS is already set from event.ThreadTS at the top
		// If not in a thread (threadTS == ""), responses go to channel directly

		// Submit to queue - will process immediately if channel is free, otherwise queue
		msg := &QueuedMessage{
			Text:      prompt,
			ChannelID: channelID,
			ThreadTS:  threadTS,
			EventTS:   event.TS,
			WorkDir:   workDir,
		}

		queued, position := messageQueue.Submit(msg)
		if queued {
			// Message was queued, notify user
			logf("Message queued for channel %s (position: %d)", channelID, position)
			removeReaction(config, channelID, event.TS, "eyes")
			addReaction(config, channelID, event.TS, "hourglass_flowing_sand")
			sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":hourglass: Queued (position %d) - will run after current task", position))
		} else {
			// Process immediately
			logf("Calling Claude in streaming mode for channel %s (thread: %v)", channelID, threadTS != "")
			processClaudeMessage(msg, config, reply)
		}
		return
	}

	// Try to auto-detect session from channel name
	channelName, err := getChannelName(config, channelID)
	if err == nil && channelName != "" {
		baseDir := getProjectsDir(config)

		// Try exact match first, then with spaces instead of hyphens
		projectDir := filepath.Join(baseDir, channelName)
		sessionName := channelName

		if _, err := os.Stat(projectDir); os.IsNotExist(err) {
			// Try with spaces instead of hyphens (Slack converts spaces to hyphens)
			altName := strings.ReplaceAll(channelName, "-", " ")
			altDir := filepath.Join(baseDir, altName)
			if _, err := os.Stat(altDir); err == nil {
				projectDir = altDir
				sessionName = altName
			}
		}

		// Check if project directory exists
		if _, err := os.Stat(projectDir); err == nil {
			logf("Auto-detected session '%s' from channel '%s' (project dir exists)", sessionName, channelName)

			// Auto-add to sessions (use sessionName as key, which may have spaces)
			if err := cfgMgr.SetSession(sessionName, channelID); err != nil {
				logf("Failed to auto-save session: %v", err)
			}

			// Handle as session message using streaming mode
			addReaction(config, channelID, event.TS, "eyes")

			prompt := slackUserPrefix + text

			// Submit to queue
			msg := &QueuedMessage{
				Text:      prompt,
				ChannelID: channelID,
				ThreadTS:  threadTS,
				EventTS:   event.TS,
				WorkDir:   projectDir,
			}

			queued, position := messageQueue.Submit(msg)
			if queued {
				logf("Message queued for channel %s (position: %d)", channelID, position)
				removeReaction(config, channelID, event.TS, "eyes")
				addReaction(config, channelID, event.TS, "hourglass_flowing_sand")
				sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":hourglass: Queued (position %d) - will run after current task", position))
			} else {
				logf("Calling Claude in streaming mode for channel %s (thread: %v)", channelID, threadTS != "")
				processClaudeMessage(msg, config, reply)
			}
			return
		}
	}

	// !claude_* commands outside session context
	if strings.HasPrefix(text, "!claude_") {
		reply(":x: Use `!claude_*` commands in a session channel")
		return
	}

	// Otherwise, run one-shot Claude
	sendMessage(config, channelID, ":robot_face: Running Claude...")
	workerPool.Submit(func() {
		output, err := runClaude(text)
		if err != nil {
			if strings.Contains(err.Error(), "context deadline exceeded") {
				output = fmt.Sprintf(":stopwatch: Timeout (10min)\n\n%s", output)
			} else {
				output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
			}
		}
		sendMessage(config, channelID, output)
	})
}

// processClaudeMessage handles a Claude request and processes the queue
func processClaudeMessage(msg *QueuedMessage, config *Config, reply func(string)) {
	workerPool.Submit(func() {
		// Process the message
		resp, err := callClaudeStreaming(msg.Text, msg.ChannelID, msg.ThreadTS, msg.WorkDir, config)

		// Remove hourglass if it was queued
		removeReaction(config, msg.ChannelID, msg.EventTS, "hourglass_flowing_sand")

		if err != nil {
			logf("Claude error: %v", err)
			addReaction(config, msg.ChannelID, msg.EventTS, "x")
			removeReaction(config, msg.ChannelID, msg.EventTS, "eyes")
			reply(fmt.Sprintf(":x: Claude error: %v", err))
		} else {
			// Success - update reactions (response already sent by streaming)
			removeReaction(config, msg.ChannelID, msg.EventTS, "eyes")
			addReaction(config, msg.ChannelID, msg.EventTS, "white_check_mark")
			logf("Claude responded (session: %s, tokens: %d in / %d out)",
				resp.SessionID, resp.Usage.InputTokens, resp.Usage.OutputTokens)

			// Auto-compact if context was too long, then continue
			if resp.NeedsCompact {
				logf("Auto-compacting session for channel %s", msg.ChannelID)
				compactResp, compactErr := callClaudeStreaming("/compact", msg.ChannelID, msg.ThreadTS, msg.WorkDir, config)
				if compactErr != nil {
					reply(fmt.Sprintf(":x: Auto-compact failed: %v", compactErr))
				} else {
					reply(fmt.Sprintf(":broom: *Auto-compacted!* New context: %d tokens. Continuing...", compactResp.Usage.InputTokens))
					// Auto-continue after compact
					continueResp, continueErr := callClaudeStreaming("continue where you left off", msg.ChannelID, msg.ThreadTS, msg.WorkDir, config)
					if continueErr != nil {
						reply(fmt.Sprintf(":x: Auto-continue failed: %v", continueErr))
					} else {
						logf("Auto-continued after compact (tokens: %d in / %d out)",
							continueResp.Usage.InputTokens, continueResp.Usage.OutputTokens)
					}
				}
			}
		}

		// Process next in queue
		if next := messageQueue.Done(msg.ChannelID); next != nil {
			logf("Processing next queued message for channel %s", msg.ChannelID)
			// Update reaction on the queued message
			removeReaction(config, next.ChannelID, next.EventTS, "hourglass_flowing_sand")
			addReaction(config, next.ChannelID, next.EventTS, "eyes")
			// Create reply function for the queued message
			nextReply := func(text string) {
				sendMessageToThread(config, next.ChannelID, next.ThreadTS, text)
			}
			processClaudeMessage(next, config, nextReply)
		}
	})
}

func handleBlockAction(config *Config, action BlockActionPayload) {
	// Only accept from authorized user
	if !config.IsAuthorizedUser(action.User.ID) {
		return
	}

	if len(action.Actions) == 0 {
		return
	}

	act := action.Actions[0]

	// Update message to show selection
	originalText := action.Message.Text
	newText := fmt.Sprintf("%s\n\n:white_check_mark: Selected option", originalText)
	updateMessage(config, action.Channel.ID, action.Message.TS, newText)
	logf("Button clicked: %s (value: %s)", act.ActionID, act.Value)
}

func printHelp() {
	fmt.Printf(`claude-code-slack-anywhere v%s

Control Claude Code remotely via Slack.

COMMANDS:
    setup <bot> <app>       Complete setup (tokens, hook, service)
    doctor                  Check all dependencies and configuration
    listen [options]        Start the Slack bot listener manually
        --config <path>       Path to config file (default: ~/.ccsa.json)
        --projects-dir <path> Base directory for projects
        --bot-token <token>   Slack bot token (xoxb-...)
        --app-token <token>   Slack app token (xapp-...)
        --user-ids <ids>      Authorized Slack user IDs (comma-separated)
    install                 Install Claude hook manually
    hook                    Handle Claude hook (internal)

SLACK COMMANDS (in any channel):
    !ping                   Check if bot is alive
    !new <name>             Create new session with channel
    !kill                   Remove current session
    !list                   List active sessions
    !reset                  Reset conversation context
    !c <cmd>                Execute shell command

FLAGS:
    -h, --help              Show this help
    -v, --version           Show version

For more info: https://github.com/sderosiaux/claude-code-slack-anywhere
`, version)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}

	switch os.Args[1] {
	case "-h", "--help", "help":
		printHelp()
		return
	case "-v", "--version", "version":
		fmt.Printf("claude-code-slack-anywhere version %s\n", version)
		return

	case "setup":
		if len(os.Args) < 4 {
			fmt.Println("Usage: claude-code-slack-anywhere setup <bot_token> <app_token>")
			fmt.Println()
			fmt.Println("Get tokens from your Slack App:")
			fmt.Println("  1. Create app at https://api.slack.com/apps")
			fmt.Println("  2. Enable Socket Mode (get App Token: xapp-...)")
			fmt.Println("  3. Add Bot Token Scopes: channels:manage, channels:history,")
			fmt.Println("     chat:write, users:read")
			fmt.Println("  4. Install to workspace (get Bot Token: xoxb-...)")
			os.Exit(1)
		}
		if err := setup(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "doctor":
		doctor()

	case "listen":
		var opts listenOpts
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--projects-dir" && i+1 < len(os.Args) {
				opts.projectsDir = os.Args[i+1]
				i++
			} else if os.Args[i] == "--config" && i+1 < len(os.Args) {
				opts.configPath = os.Args[i+1]
				i++
			} else if os.Args[i] == "--bot-token" && i+1 < len(os.Args) {
				opts.botToken = os.Args[i+1]
				i++
			} else if os.Args[i] == "--app-token" && i+1 < len(os.Args) {
				opts.appToken = os.Args[i+1]
				i++
			} else if os.Args[i] == "--user-ids" && i+1 < len(os.Args) {
				opts.userIDs = strings.Split(os.Args[i+1], ",")
				i++
			}
		}
		if err := listen(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook":
		if err := handleHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-permission":
		if err := handlePermissionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-prompt":
		if err := handlePromptHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-question":
		if err := handleQuestionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-output":
		if err := handleOutputHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "install":
		if err := installHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		// Send notification
		config, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: not configured. Run: claude-code-slack-anywhere setup <bot_token> <app_token>\n")
			os.Exit(1)
		}

		// Find session channel for current directory
		cwd, _ := os.Getwd()
		baseDir := getProjectsDir(config)
		message := strings.Join(os.Args[1:], " ")

		for name, channelID := range config.Sessions {
			expectedPath := filepath.Join(baseDir, name)
			if cwd == expectedPath || strings.HasSuffix(cwd, "/"+name) {
				if _, err := sendMessage(config, channelID, message); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}
		}

		fmt.Println("Not in a session directory, notification not sent.")
	}
}

// newRequest is a helper to create HTTP requests
func newRequest(method, urlStr string, body interface{}) (*http.Request, error) {
	return http.NewRequest(method, urlStr, nil)
}
