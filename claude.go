package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HookData represents data received from Claude hook
type HookData struct {
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	Prompt         string `json:"prompt"`
	ToolInput      struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	} `json:"tool_input"`
}

// ClaudeResponse represents the final response from Claude
type ClaudeResponse struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Usage     struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	DurationMs   int  `json:"duration_ms"`
	IsError      bool `json:"is_error"`
	NumTurns     int  `json:"num_turns"`
	NeedsCompact bool `json:"-"` // Internal flag for auto-compact
}

// ============================================================================
// Claude Code stream-json Event Types (inspired by vibe-kanban)
// ============================================================================

// StreamEvent represents any JSON event from Claude's stream-json output
type StreamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Message   *ClaudeMessage  `json:"message,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Error     string          `json:"error,omitempty"`
	Usage     *ClaudeUsage    `json:"usage,omitempty"`
	DurationMs int            `json:"duration_ms,omitempty"`
	NumTurns  int             `json:"num_turns,omitempty"`
	// For tool_use events
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"input,omitempty"`
	// For system events
	Cwd   string   `json:"cwd,omitempty"`
	Model string   `json:"model,omitempty"`
	Tools []string `json:"tools,omitempty"`
}

// ClaudeMessage represents an assistant or user message
type ClaudeMessage struct {
	ID         string              `json:"id,omitempty"`
	Type       string              `json:"type,omitempty"`
	Role       string              `json:"role"`
	Model      string              `json:"model,omitempty"`
	Content    []ClaudeContentItem `json:"content"`
	StopReason string              `json:"stop_reason,omitempty"`
}

// ClaudeContentItem represents a content block (text, thinking, tool_use, tool_result)
type ClaudeContentItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ClaudeUsage represents token usage
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// claudeSessionIDs stores Claude session IDs by Slack channel ID
var claudeSessionIDs sync.Map // channelID (string) -> sessionID (string)

// getSessionFilePath returns the path to the sessions file (~/.ccsa/sessions.json)
func getSessionFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccsa", "sessions.json")
}

// loadSessionsFromDisk loads persisted sessions from disk
func loadSessionsFromDisk() {
	sessionFilePath := getSessionFilePath()
	data, err := os.ReadFile(sessionFilePath)
	if err != nil {
		return // File doesn't exist yet, that's fine
	}
	var sessions map[string]string
	if err := json.Unmarshal(data, &sessions); err != nil {
		return
	}
	for k, v := range sessions {
		claudeSessionIDs.Store(k, v)
	}
}

// saveSessionsToDisk persists sessions to disk
func saveSessionsToDisk() {
	sessionFilePath := getSessionFilePath()
	// Ensure directory exists
	dir := filepath.Dir(sessionFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	sessions := make(map[string]string)
	claudeSessionIDs.Range(func(key, value interface{}) bool {
		sessions[key.(string)] = value.(string)
		return true
	})
	data, err := json.Marshal(sessions)
	if err != nil {
		return
	}
	os.WriteFile(sessionFilePath, data, 0600)
}

// Paths for binaries
var (
	binPath    string
	claudePath string
)

// Active Claude processes per channel (for !cancel)
var activeProcesses sync.Map // channelID -> *exec.Cmd

// Verbose mode per channel (default: true = verbose)
var verboseMode sync.Map // channelID -> bool

// IsVerbose returns whether verbose mode is enabled for a channel (default: true)
func IsVerbose(channelID string) bool {
	if v, ok := verboseMode.Load(channelID); ok {
		return v.(bool)
	}
	return true // default verbose
}

// SetVerbose sets the verbose mode for a channel
func SetVerbose(channelID string, verbose bool) {
	verboseMode.Store(channelID, verbose)
}

// CancelClaudeProcess cancels any running Claude process for a channel
func CancelClaudeProcess(channelID string) bool {
	if cmd, ok := activeProcesses.Load(channelID); ok {
		if process := cmd.(*exec.Cmd); process != nil && process.Process != nil {
			process.Process.Kill()
			activeProcesses.Delete(channelID)
			return true
		}
	}
	return false
}

func init() {
	if exe, err := os.Executable(); err == nil {
		binPath = exe
	}

	home, _ := os.UserHomeDir()
	claudePaths := []string{
		filepath.Join(home, ".claude", "local", "claude"),
		"/usr/local/bin/claude",
	}

	nvmDir := filepath.Join(home, ".nvm", "versions", "node")
	if entries, err := os.ReadDir(nvmDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				claudePaths = append(claudePaths, filepath.Join(nvmDir, entry.Name(), "bin", "claude"))
			}
		}
	}

	for _, p := range claudePaths {
		if _, err := os.Stat(p); err == nil {
			claudePath = p
			break
		}
	}

	if claudePath == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			claudePath = p
		}
	}

	// Load persisted sessions
	loadSessionsFromDisk()
}

func runClaudeRaw(continueSession bool) error {
	if claudePath == "" {
		return fmt.Errorf("claude binary not found")
	}

	args := []string{"--dangerously-skip-permissions"}
	if continueSession {
		args = append(args, "-c")
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// One-shot Claude run
func runClaude(prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	config, _ := loadConfig()
	baseDir := getProjectsDir(config)
	workDir := baseDir

	words := strings.Fields(prompt)
	if len(words) > 0 {
		firstWord := words[0]
		potentialDir := filepath.Join(baseDir, firstWord)
		if info, err := os.Stat(potentialDir); err == nil && info.IsDir() {
			workDir = potentialDir
			prompt = strings.TrimSpace(strings.TrimPrefix(prompt, firstWord))
			if prompt == "" {
				return "Error: no prompt provided after directory name", nil
			}
		}
	}

	if claudePath == "" {
		return "Error: claude binary not found", fmt.Errorf("claude not found")
	}
	cmd := exec.CommandContext(ctx, claudePath, "--dangerously-skip-permissions", "-p", prompt)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}

// callClaudeJSON calls Claude in headless mode with JSON output
func callClaudeJSON(prompt string, channelID string, workDir string) (*ClaudeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if claudePath == "" {
		return nil, fmt.Errorf("claude binary not found")
	}

	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "json",
		"--append-system-prompt", SlackSystemPromptAppend,
	}

	if sid, ok := claudeSessionIDs.Load(channelID); ok {
		args = append(args, "--resume", sid.(string))
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("claude error: %w - %s", err, stderr.String())
		}
		return nil, fmt.Errorf("claude error: %w", err)
	}

	var resp ClaudeResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return &ClaudeResponse{
			Result:  stdout.String(),
			IsError: true,
		}, fmt.Errorf("JSON parse error: %w - raw: %s", err, stdout.String())
	}

	if resp.SessionID != "" {
		claudeSessionIDs.Store(channelID, resp.SessionID)
		saveSessionsToDisk()
	}

	return &resp, nil
}

// ============================================================================
// Slack Thread Manager - posts separate messages for each event type
// ============================================================================

// SlackThreadManager manages messages in a Slack thread, posting separate messages per type
type SlackThreadManager struct {
	config    *Config
	channelID string
	threadTS  string

	// Current assistant message accumulator
	currentAssistantTS      string
	currentAssistantContent strings.Builder
	lastAssistantUpdate     time.Time

	// Track tool calls in progress
	activeTools map[string]string // tool_use_id -> messageTS

	// Tool batching (accumulate same tool calls within 1s window)
	batchedToolName   string
	batchedToolInputs []string
	batchedToolTimer  *time.Timer

	// Track if system init was already posted
	systemInitPosted bool

	// Heartbeat timer for long operations
	heartbeatTicker  *time.Ticker
	heartbeatStop    chan struct{}
	heartbeatTS      string // Message TS for the heartbeat message
	lastActivityTime time.Time

	// Track if any assistant text was posted (to avoid double-posting from result)
	assistantTextPosted bool

	mu sync.Mutex
}

// NewSlackThreadManager creates a new thread manager
func NewSlackThreadManager(config *Config, channelID, threadTS string) *SlackThreadManager {
	m := &SlackThreadManager{
		config:           config,
		channelID:        channelID,
		threadTS:         threadTS,
		activeTools:      make(map[string]string),
		lastActivityTime: time.Now(),
	}
	m.startHeartbeat()
	return m
}

// startHeartbeat starts the heartbeat ticker
func (m *SlackThreadManager) startHeartbeat() {
	m.heartbeatTicker = time.NewTicker(1 * time.Second)
	m.heartbeatStop = make(chan struct{})

	go func() {
		for {
			select {
			case <-m.heartbeatTicker.C:
				m.mu.Lock()
				elapsed := time.Since(m.lastActivityTime)
				// Only show heartbeat after 5s of silence
				if elapsed >= 5*time.Second {
					elapsedStr := formatDuration(elapsed)
					heartbeatMsg := fmt.Sprintf(":hourglass_flowing_sand: Working... (%s)", elapsedStr)
					if m.heartbeatTS == "" {
						// Create new heartbeat message
						ts, _ := sendMessageToThreadGetTS(m.config, m.channelID, m.threadTS, heartbeatMsg)
						m.heartbeatTS = ts
					} else {
						// Update existing heartbeat message
						updateMessage(m.config, m.channelID, m.heartbeatTS, heartbeatMsg)
					}
				}
				m.mu.Unlock()
			case <-m.heartbeatStop:
				return
			}
		}
	}()
}

// stopHeartbeat stops the heartbeat ticker and removes the heartbeat message
func (m *SlackThreadManager) stopHeartbeat() {
	if m.heartbeatTicker != nil {
		m.heartbeatTicker.Stop()
		close(m.heartbeatStop)
	}
	// Delete heartbeat message if it exists
	if m.heartbeatTS != "" {
		deleteMessage(m.config, m.channelID, m.heartbeatTS)
		m.heartbeatTS = ""
	}
}

// recordActivity records that activity happened (resets heartbeat timer)
func (m *SlackThreadManager) recordActivity() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordActivityLocked()
}

// recordActivityLocked is the unlocked version (caller must hold mutex)
func (m *SlackThreadManager) recordActivityLocked() {
	m.lastActivityTime = time.Now()
	// If heartbeat message was shown, delete it since we have activity now
	if m.heartbeatTS != "" {
		deleteMessage(m.config, m.channelID, m.heartbeatTS)
		m.heartbeatTS = ""
	}
}

// formatDuration formats a duration as "Xs" or "Xm Ys"
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// PostThinking posts a thinking indicator
func (m *SlackThreadManager) PostThinking() {
	m.mu.Lock()
	defer m.mu.Unlock()

	ts, _ := sendMessageToThreadGetTS(m.config, m.channelID, m.threadTS, ":hourglass_flowing_sand: _Thinking..._")
	m.currentAssistantTS = ts
}

// PostSystemInit posts system initialization info (only once per session)
func (m *SlackThreadManager) PostSystemInit(event *StreamEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Only post once
	if m.systemInitPosted {
		return
	}
	m.systemInitPosted = true

	// Delete the "Thinking..." message and replace with system init
	if m.currentAssistantTS != "" {
		deleteMessage(m.config, m.channelID, m.currentAssistantTS)
		m.currentAssistantTS = ""
	}

	// Compact format on one line
	msg := fmt.Sprintf(":zap: `%s` · %s · `%s`",
		event.SessionID[:8], event.Model, event.Cwd)
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)
}

// UpdateAssistantText accumulates and updates assistant text (batched)
func (m *SlackThreadManager) UpdateAssistantText(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record activity to reset heartbeat
	m.recordActivityLocked()

	// Flush any pending tool batch before assistant text
	m.flushToolBatchLocked()

	m.currentAssistantContent.WriteString(text)

	// Micro-batch: update every 500ms or 500 chars
	sinceLastUpdate := time.Since(m.lastAssistantUpdate)
	contentLen := m.currentAssistantContent.Len()

	shouldUpdate := m.currentAssistantTS == "" || // First content
		sinceLastUpdate >= 500*time.Millisecond ||
		(contentLen >= 500 && sinceLastUpdate >= 200*time.Millisecond)

	if shouldUpdate && contentLen > 0 {
		m.flushAssistantText(false)
	}
}

// flushAssistantText sends accumulated text to Slack
func (m *SlackThreadManager) flushAssistantText(final bool) {
	content := m.currentAssistantContent.String()
	if content == "" {
		return
	}

	displayContent := markdownToSlack(content)
	if !final {
		displayContent += "\n\n_..._"
	}

	if m.currentAssistantTS == "" {
		ts, _ := sendMessageToThreadGetTS(m.config, m.channelID, m.threadTS, displayContent)
		m.currentAssistantTS = ts
	} else {
		updateMessage(m.config, m.channelID, m.currentAssistantTS, displayContent)
	}

	m.lastAssistantUpdate = time.Now()
	m.assistantTextPosted = true
}

// FinalizeAssistantText finalizes the current assistant message
func (m *SlackThreadManager) FinalizeAssistantText() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.flushAssistantText(true)
	// Reset for next assistant message
	m.currentAssistantTS = ""
	m.currentAssistantContent.Reset()
}

// PostThinkingBlock posts a thinking block as a separate collapsed message
func (m *SlackThreadManager) PostThinkingBlock(thinking string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Truncate if too long
	if len(thinking) > 500 {
		thinking = thinking[:500] + "..."
	}

	msg := fmt.Sprintf(":brain: _Thinking..._\n```\n%s\n```", thinking)
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)
}

// getToolBatchGroup returns the batch group for a tool (tools in same group are batched together)
func getToolBatchGroup(toolName string) string {
	switch toolName {
	case "Read", "Bash", "Grep", "Glob":
		return "read"
	case "Write", "Edit":
		return "write"
	case "WebFetch", "WebSearch":
		return "web"
	}
	return toolName // Each other tool is its own group
}

// PostToolUseStart posts a tool use start message (batched for similar tools within 1s)
func (m *SlackThreadManager) PostToolUseStart(toolName string, toolID string, input json.RawMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record activity
	m.recordActivityLocked()

	// In quiet mode, skip read-only tools (Bash, Read, Grep, Glob)
	// Only show write operations (Edit, Write) and important tools
	if !IsVerbose(m.channelID) {
		group := getToolBatchGroup(toolName)
		if group == "read" {
			return // Skip read tools in quiet mode
		}
	}

	// First, finalize any pending assistant text
	if m.currentAssistantContent.Len() > 0 {
		m.flushAssistantText(true)
		m.currentAssistantTS = ""
		m.currentAssistantContent.Reset()
	}

	inputStr := formatToolInput(toolName, input)

	// Check if we can batch this tool with the current batch (same group)
	canBatch := m.batchedToolName != "" && getToolBatchGroup(m.batchedToolName) == getToolBatchGroup(toolName)

	if canBatch {
		m.batchedToolInputs = append(m.batchedToolInputs, fmt.Sprintf("%s %s", getToolEmoji(toolName), inputStr))
		// Reset timer
		if m.batchedToolTimer != nil {
			m.batchedToolTimer.Stop()
		}
		m.batchedToolTimer = time.AfterFunc(1*time.Second, func() {
			m.flushToolBatch()
		})
		return
	}

	// Different tool category - flush existing batch first
	if m.batchedToolName != "" {
		m.flushToolBatchLocked()
	}

	// Start new batch
	m.batchedToolName = toolName
	m.batchedToolInputs = []string{fmt.Sprintf("%s %s", getToolEmoji(toolName), inputStr)}
	m.batchedToolTimer = time.AfterFunc(1*time.Second, func() {
		m.flushToolBatch()
	})
}

// flushToolBatch flushes the batched tool calls (acquires lock)
func (m *SlackThreadManager) flushToolBatch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushToolBatchLocked()
}

// flushToolBatchLocked flushes the batched tool calls (must hold lock)
func (m *SlackThreadManager) flushToolBatchLocked() {
	if m.batchedToolName == "" || len(m.batchedToolInputs) == 0 {
		return
	}

	if m.batchedToolTimer != nil {
		m.batchedToolTimer.Stop()
		m.batchedToolTimer = nil
	}

	// Each input already has its emoji prefix, just join them
	msg := strings.Join(m.batchedToolInputs, "\n")
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)

	m.batchedToolName = ""
	m.batchedToolInputs = nil
}

// PostToolResult posts a tool result
func (m *SlackThreadManager) PostToolResult(toolUseID string, result json.RawMessage, isError bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record activity
	m.recordActivityLocked()

	// In quiet mode, skip non-error results
	if !IsVerbose(m.channelID) && !isError {
		return
	}

	// Flush any pending tool batch
	m.flushToolBatchLocked()

	// Format result
	fullResult := string(result)
	const previewLimit = 500
	const snippetThreshold = 1000

	var msg string
	if isError {
		// For errors, show more context
		resultStr := fullResult
		if len(resultStr) > 1000 {
			resultStr = resultStr[:1000] + "..."
		}
		msg = fmt.Sprintf(":x: *Error*\n```\n%s\n```", resultStr)
	} else if len(fullResult) > snippetThreshold {
		// Long output: upload as snippet, show preview
		preview := fullResult[:previewLimit] + "..."
		msg = fmt.Sprintf(":white_check_mark: ```\n%s\n```\n_(%d chars total - uploading full output...)_", preview, len(fullResult))
		sendMessageToThread(m.config, m.channelID, m.threadTS, msg)

		// Upload full result as snippet (async, outside lock)
		go func() {
			_, err := uploadSnippet(m.config, m.channelID, m.threadTS, "output.txt", fullResult, "Full output")
			if err != nil {
				logf("Failed to upload snippet: %v", err)
			}
		}()

		// Clean up and return early since we already sent the message
		if _, ok := m.activeTools[toolUseID]; ok {
			delete(m.activeTools, toolUseID)
		}
		return
	} else {
		// Short output: show inline
		msg = fmt.Sprintf(":white_check_mark: ```\n%s\n```", fullResult)
	}

	// Update the tool message if we have it, otherwise post new
	if _, ok := m.activeTools[toolUseID]; ok {
		delete(m.activeTools, toolUseID)
	}
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)
}

// PostFinalResult posts the final result with stats
func (m *SlackThreadManager) PostFinalResult(resp *ClaudeResponse) {
	// Stop heartbeat first (outside lock to avoid deadlock)
	m.stopHeartbeat()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Flush any pending tool batch
	m.flushToolBatchLocked()

	// Finalize any pending assistant text first
	if m.currentAssistantContent.Len() > 0 {
		m.flushAssistantText(true)
	}

	// If we have a result string and haven't posted any assistant text, post it now
	// This handles cases where Claude returns text directly in the result without streaming
	if resp.Result != "" && !m.assistantTextPosted {
		text := convertBold(resp.Result)
		sendMessageToThread(m.config, m.channelID, m.threadTS, text)
	}

	// Check if context is getting large (warn at 150k tokens, typical limit is ~200k)
	totalTokens := resp.Usage.InputTokens + resp.Usage.OutputTokens
	var warningMsg string
	if totalTokens > 150000 {
		warningMsg = "\n:warning: *Context getting large!* Use `!claude_compact` to summarize."
	} else if totalTokens > 100000 {
		warningMsg = "\n:bulb: _Context: " + fmt.Sprintf("%dk", totalTokens/1000) + " tokens_"
	}

	// Post stats - format duration as seconds or minutes
	var durationStr string
	if resp.DurationMs >= 60000 {
		durationStr = fmt.Sprintf("%.1fmin", float64(resp.DurationMs)/60000)
	} else {
		durationStr = fmt.Sprintf("%.1fs", float64(resp.DurationMs)/1000)
	}
	statsMsg := fmt.Sprintf(":checkered_flag: *Done* | %d turns | %d tokens in | %d tokens out | %s%s",
		resp.NumTurns,
		resp.Usage.InputTokens,
		resp.Usage.OutputTokens,
		durationStr,
		warningMsg)

	sendMessageToThread(m.config, m.channelID, m.threadTS, statsMsg)
}

// PostError posts an error message
func (m *SlackThreadManager) PostError(errMsg string) {
	// Stop heartbeat first (outside lock to avoid deadlock)
	m.stopHeartbeat()

	m.mu.Lock()
	defer m.mu.Unlock()

	msg := fmt.Sprintf(":rotating_light: *Error*\n```\n%s\n```", errMsg)
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)
}

// PostAutoCompactNotice posts a notice that auto-compact will be triggered
func (m *SlackThreadManager) PostAutoCompactNotice() {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg := ":warning: *Context too long!* Auto-compacting conversation..."
	sendMessageToThread(m.config, m.channelID, m.threadTS, msg)
}

// getToolEmoji returns an emoji for a tool name
func getToolEmoji(toolName string) string {
	switch strings.ToLower(toolName) {
	case "bash", "execute", "command", "bashoutput":
		return "" // No emoji for bash - command itself is self-explanatory
	case "read", "readfile":
		return ":page_facing_up:"
	case "write", "writefile", "edit":
		return ":pencil:"
	case "glob", "find":
		return ":mag:"
	case "grep", "search":
		return ":mag_right:"
	case "task", "subagent":
		return ":robot_face:"
	case "webfetch", "webrequest":
		return ":globe_with_meridians:"
	case "todowrite":
		return "" // No emoji - formatted list is self-explanatory
	case "askuserquestion":
		return ":question:"
	case "websearch":
		return ":mag:"
	default:
		return ""
	}
}

// formatToolInput formats tool input for display
func formatToolInput(toolName string, input json.RawMessage) string {
	var data map[string]interface{}
	if err := json.Unmarshal(input, &data); err != nil {
		return ""
	}

	toolLower := strings.ToLower(toolName)

	// Handle TodoWrite specially - check if data has "todos" key regardless of tool name
	if _, hasTodos := data["todos"]; hasTodos {
		toolLower = "todowrite"
	}

	switch toolLower {
	case "bash", "execute":
		if cmd, ok := data["command"].(string); ok {
			if len(cmd) > 200 {
				cmd = cmd[:200] + "..."
			}
			return fmt.Sprintf("```\n%s\n```", cmd)
		}
	case "bashoutput":
		if bashID, ok := data["bash_id"].(string); ok {
			return fmt.Sprintf("reading output `%s`", bashID)
		}
	case "read", "readfile":
		if path, ok := data["file_path"].(string); ok {
			return fmt.Sprintf("`%s`", path)
		}
	case "write", "writefile":
		if path, ok := data["file_path"].(string); ok {
			return fmt.Sprintf("`%s`", path)
		}
	case "edit":
		if path, ok := data["file_path"].(string); ok {
			// Show a preview of the change
			oldStr, _ := data["old_string"].(string)
			newStr, _ := data["new_string"].(string)

			// Truncate for display
			if len(oldStr) > 50 {
				oldStr = oldStr[:50] + "..."
			}
			if len(newStr) > 50 {
				newStr = newStr[:50] + "..."
			}

			// Escape backticks and newlines for inline display
			oldStr = strings.ReplaceAll(strings.ReplaceAll(oldStr, "`", "'"), "\n", "↵")
			newStr = strings.ReplaceAll(strings.ReplaceAll(newStr, "`", "'"), "\n", "↵")

			if oldStr != "" && newStr != "" {
				return fmt.Sprintf("`%s`\n`-%s`\n`+%s`", path, oldStr, newStr)
			}
			return fmt.Sprintf("`%s`", path)
		}
	case "glob":
		if pattern, ok := data["pattern"].(string); ok {
			return fmt.Sprintf("`%s`", pattern)
		}
	case "grep":
		if pattern, ok := data["pattern"].(string); ok {
			return fmt.Sprintf("`%s`", pattern)
		}
	case "task":
		if desc, ok := data["description"].(string); ok {
			return fmt.Sprintf("_%s_", desc)
		}
	case "webfetch":
		if url, ok := data["url"].(string); ok {
			return fmt.Sprintf("<%s>", url)
		}
	case "websearch":
		if query, ok := data["query"].(string); ok {
			return fmt.Sprintf("_%s_", query)
		}
	case "todowrite":
		if todos, ok := data["todos"].([]interface{}); ok && len(todos) > 0 {
			var items []string
			for _, t := range todos {
				if todo, ok := t.(map[string]interface{}); ok {
					content, _ := todo["content"].(string)
					status, _ := todo["status"].(string)
					activeForm, _ := todo["activeForm"].(string)
					// Use activeForm if in_progress, otherwise content
					displayText := content
					if status == "in_progress" && activeForm != "" {
						displayText = activeForm
					}
					emoji := "☐"
					switch status {
					case "completed":
						emoji = "☑"
					case "in_progress":
						emoji = "▶"
					}
					items = append(items, fmt.Sprintf("%s %s", emoji, displayText))
				}
			}
			if len(items) > 0 {
				return strings.Join(items, "\n")
			}
		}
		return "_updating tasks_"
	}

	// MCP tools - check by data shape rather than tool name
	// mcp__context7__resolve-library-id
	if libraryName, ok := data["libraryName"].(string); ok {
		if query, ok := data["query"].(string); ok {
			return fmt.Sprintf(":books: `%s` _%s_", libraryName, query)
		}
		return fmt.Sprintf(":books: `%s`", libraryName)
	}
	// mcp__context7__query-docs
	if libraryId, ok := data["libraryId"].(string); ok {
		if query, ok := data["query"].(string); ok {
			return fmt.Sprintf(":book: `%s` _%s_", libraryId, query)
		}
		return fmt.Sprintf(":book: `%s`", libraryId)
	}

	// Default: show tool name and human-readable params
	var parts []string
	parts = append(parts, fmt.Sprintf("*%s*", toolName))
	for key, val := range data {
		switch v := val.(type) {
		case string:
			if len(v) > 100 {
				v = v[:100] + "..."
			}
			parts = append(parts, fmt.Sprintf("• %s: `%s`", key, v))
		case bool:
			parts = append(parts, fmt.Sprintf("• %s: %v", key, v))
		case float64:
			parts = append(parts, fmt.Sprintf("• %s: %v", key, v))
		case []interface{}:
			parts = append(parts, fmt.Sprintf("• %s: (%d items)", key, len(v)))
		case map[string]interface{}:
			parts = append(parts, fmt.Sprintf("• %s: {%d keys}", key, len(v)))
		default:
			parts = append(parts, fmt.Sprintf("• %s: %v", key, v))
		}
	}
	return strings.Join(parts, "\n")
}

// Main streaming function
// ============================================================================

// ClaudeStreamingOptions contains options for callClaudeStreamingWithOptions
type ClaudeStreamingOptions struct {
	ForkFromChannel string // If set, fork session from this channel instead of resuming
}

// callClaudeStreaming calls Claude with streaming output and posts separate Slack messages
func callClaudeStreaming(prompt string, channelID string, threadTS string, workDir string, config *Config) (*ClaudeResponse, error) {
	return callClaudeStreamingWithOptions(prompt, channelID, threadTS, workDir, config, nil)
}

// callClaudeStreamingForked forks a session from sourceChannel and runs in a new thread
func callClaudeStreamingForked(prompt string, channelID string, threadTS string, workDir string, config *Config, sourceChannelID string) (*ClaudeResponse, error) {
	return callClaudeStreamingWithOptions(prompt, channelID, threadTS, workDir, config, &ClaudeStreamingOptions{
		ForkFromChannel: sourceChannelID,
	})
}

// callClaudeStreamingWithOptions is the main implementation with options
func callClaudeStreamingWithOptions(prompt string, channelID string, threadTS string, workDir string, config *Config, opts *ClaudeStreamingOptions) (*ClaudeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if claudePath == "" {
		return nil, fmt.Errorf("claude binary not found")
	}

	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--append-system-prompt", SlackSystemPromptAppend,
	}

	// Handle fork vs normal resume
	if opts != nil && opts.ForkFromChannel != "" {
		// Fork: resume from source channel's session but create new session ID
		if sid, ok := claudeSessionIDs.Load(opts.ForkFromChannel); ok {
			args = append(args, "--resume", sid.(string), "--fork-session")
		}
	} else {
		// Normal: resume from this channel's session
		if sid, ok := claudeSessionIDs.Load(channelID); ok {
			args = append(args, "--resume", sid.(string))
		}
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude: %w", err)
	}

	// Store process for !cancel
	activeProcesses.Store(channelID, cmd)
	defer activeProcesses.Delete(channelID)

	// Create thread manager for separate messages
	manager := NewSlackThreadManager(config, channelID, threadTS)
	manager.PostThinking()

	var finalResponse ClaudeResponse
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Store session ID
		if event.SessionID != "" && finalResponse.SessionID == "" {
			finalResponse.SessionID = event.SessionID
			claudeSessionIDs.Store(channelID, event.SessionID)
			saveSessionsToDisk()
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" && event.Model != "" {
				manager.PostSystemInit(&event)
			}

		case "assistant":
			if event.Message != nil {
				for _, content := range event.Message.Content {
					switch content.Type {
					case "text":
						if content.Text != "" {
							manager.UpdateAssistantText(content.Text)
						}
					case "thinking":
						if content.Thinking != "" {
							manager.PostThinkingBlock(content.Thinking)
						}
					case "tool_use":
						manager.FinalizeAssistantText()
						manager.PostToolUseStart(content.Name, content.ID, content.Input)
					case "tool_result":
						manager.PostToolResult(content.ToolUseID, content.Content, content.IsError)
					}
				}
			}

		case "tool_use":
			manager.FinalizeAssistantText()
			manager.PostToolUseStart(event.ToolName, "", event.ToolInput)

		case "tool_result":
			manager.PostToolResult("", event.Result, event.IsError)

		case "result":
			finalResponse.IsError = event.IsError
			finalResponse.DurationMs = event.DurationMs
			finalResponse.NumTurns = event.NumTurns
			if event.Usage != nil {
				finalResponse.Usage.InputTokens = event.Usage.InputTokens
				finalResponse.Usage.OutputTokens = event.Usage.OutputTokens
				finalResponse.Usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
				finalResponse.Usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
			}
			if event.Error != "" {
				// Check if context is too long - trigger auto-compact
				if strings.Contains(event.Error, "Prompt is too long") || strings.Contains(event.Error, "too long") {
					manager.PostAutoCompactNotice()
					finalResponse.NeedsCompact = true
				} else {
					manager.PostError(event.Error)
				}
			}
			// Try to extract result string
			if len(event.Result) > 0 {
				var resultStr string
				if err := json.Unmarshal(event.Result, &resultStr); err == nil {
					finalResponse.Result = resultStr
				}
			}
		}
	}

	cmd.Wait()

	// Finalize any remaining content
	manager.FinalizeAssistantText()
	manager.PostFinalResult(&finalResponse)

	return &finalResponse, nil
}

// resetClaudeSession removes the stored session ID for a channel
func resetClaudeSession(channelID string) {
	claudeSessionIDs.Delete(channelID)
	saveSessionsToDisk()
}

// getClaudeSessionID returns the current session ID for a channel, if any
func getClaudeSessionID(channelID string) (string, bool) {
	if sid, ok := claudeSessionIDs.Load(channelID); ok {
		return sid.(string), true
	}
	return "", false
}

// convertBold converts markdown **bold** to Slack *bold*
func convertBold(text string) string {
	for strings.Contains(text, "**") {
		start := strings.Index(text, "**")
		end := strings.Index(text[start+2:], "**")
		if end == -1 {
			break
		}
		end += start + 2
		content := text[start+2 : end]
		text = text[:start] + "*" + content + "*" + text[end+2:]
	}
	return text
}

// markdownToSlack converts GitHub-flavored markdown to Slack mrkdwn
func markdownToSlack(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	var tableRows [][]string // Store rows as slices of cells

	flushTable := func() {
		if len(tableRows) == 0 {
			return
		}

		// Calculate max width for each column
		colWidths := make([]int, 0)
		for _, row := range tableRows {
			for i, cell := range row {
				if i >= len(colWidths) {
					colWidths = append(colWidths, len(cell))
				} else if len(cell) > colWidths[i] {
					colWidths[i] = len(cell)
				}
			}
		}

		// Format each row with padding
		result = append(result, "```")
		for _, row := range tableRows {
			var paddedCells []string
			for i, cell := range row {
				// Don't pad the last column
				if i == len(row)-1 {
					paddedCells = append(paddedCells, cell)
				} else {
					width := 0
					if i < len(colWidths) {
						width = colWidths[i]
					}
					paddedCells = append(paddedCells, fmt.Sprintf("%-*s", width, cell))
				}
			}
			result = append(result, strings.Join(paddedCells, " | "))
		}
		result = append(result, "```")
		tableRows = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			flushTable()
			inCodeBlock = !inCodeBlock
			result = append(result, line)
			continue
		}
		if inCodeBlock {
			result = append(result, line)
			continue
		}

		trimmed := strings.TrimSpace(line)

		// Check if this is a table line
		isTableLine := strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")

		// If we were in a table and this line isn't a table line, flush
		if len(tableRows) > 0 && !isTableLine {
			flushTable()
		}

		// Convert horizontal rules (---, ***, ___) to Unicode line
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			result = append(result, "───────────────────────")
			continue
		}

		// Convert markdown headers
		if strings.HasPrefix(trimmed, "#") {
			headerText := strings.TrimLeft(trimmed, "#")
			headerText = strings.TrimSpace(headerText)
			// Remove surrounding ** if present (e.g., ## **Title** -> Title)
			headerText = strings.TrimPrefix(headerText, "**")
			headerText = strings.TrimSuffix(headerText, "**")
			if headerText != "" {
				result = append(result, "*"+headerText+"*")
				continue
			}
		}

		// Collect table rows
		if isTableLine {
			// Skip separator lines (|---|---|)
			if strings.Contains(trimmed, "---") {
				continue
			}
			// Parse cells
			cells := strings.Split(trimmed, "|")
			var cleanCells []string
			for _, cell := range cells {
				cell = strings.TrimSpace(cell)
				if cell == "" {
					continue // Skip empty cells from leading/trailing |
				}
				// Remove markdown bold markers
				cell = strings.TrimPrefix(cell, "**")
				cell = strings.TrimSuffix(cell, "**")
				cleanCells = append(cleanCells, cell)
			}
			tableRows = append(tableRows, cleanCells)
			continue
		}

		// Convert bold markdown to Slack format
		line = convertBold(line)

		result = append(result, line)
	}

	// Flush any remaining table
	flushTable()

	return strings.Join(result, "\n")
}

// sendClaudeResponse formats and sends a Claude response to Slack
func sendClaudeResponse(config *Config, channelID, threadTS string, resp *ClaudeResponse) {
	result := resp.Result
	if result == "" {
		result = "(no response)"
	}

	result = markdownToSlack(result)

	footer := fmt.Sprintf("\n\n_tokens: %d in / %d out | %dms_",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.DurationMs)

	const maxLen = 3500

	if len(result)+len(footer) < maxLen {
		sendMessageToThread(config, channelID, threadTS, result+footer)
		return
	}

	chunks := splitMessageIntoChunks(result, maxLen)
	for i, chunk := range chunks {
		msg := chunk
		if i == len(chunks)-1 {
			msg += footer
		}
		sendMessageToThread(config, channelID, threadTS, msg)
	}
}

// splitMessageIntoChunks splits a message into chunks of maxLen
func splitMessageIntoChunks(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		breakPoint := maxLen
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			breakPoint = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			breakPoint = idx + 1
		}

		chunks = append(chunks, remaining[:breakPoint])
		remaining = remaining[breakPoint:]
	}

	return chunks
}

// Execute shell command
func executeCommand(cmdStr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	cmd.Dir, _ = os.UserHomeDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}
