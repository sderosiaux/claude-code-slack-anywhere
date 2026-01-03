package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// Config stores bot configuration and session mappings
type Config struct {
	BotToken  string            `json:"bot_token"`  // Slack Bot Token (xoxb-...)
	AppToken  string            `json:"app_token"`  // Slack App Token (xapp-...) for Socket Mode
	UserID    string            `json:"user_id"`    // Authorized Slack user ID
	Sessions  map[string]string `json:"sessions"`   // session name -> channel ID
	Away      bool              `json:"away"`
}

// Slack API types

type SlackResponse struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Channel json.RawMessage `json:"channel,omitempty"`
	TS      string          `json:"ts,omitempty"`
	URL     string          `json:"url,omitempty"` // For Socket Mode connection
}

type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SlackUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SlackMessage struct {
	Type      string `json:"type"`
	Channel   string `json:"channel"`
	User      string `json:"user"`
	Text      string `json:"text"`
	TS        string `json:"ts"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
}

// Socket Mode envelope
type SocketModeEnvelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
	RetryAttempt int           `json:"retry_attempt,omitempty"`
	RetryReason  string        `json:"retry_reason,omitempty"`
}

// Event callback payload
type EventCallback struct {
	Type    string          `json:"type"`
	EventID string          `json:"event_id"`
	Event   json.RawMessage `json:"event"`
}

// Block action payload (button clicks)
type BlockActionPayload struct {
	Type        string `json:"type"`
	User        SlackUser `json:"user"`
	Channel     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	Message     SlackMessage `json:"message"`
	Actions     []BlockAction `json:"actions"`
	ResponseURL string `json:"response_url"`
}

type BlockAction struct {
	ActionID string `json:"action_id"`
	BlockID  string `json:"block_id"`
	Value    string `json:"value"`
	Type     string `json:"type"`
}

// Block Kit types
type Block struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text,omitempty"`
	Elements []Element   `json:"elements,omitempty"`
	BlockID  string      `json:"block_id,omitempty"`
}

type TextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Element struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text,omitempty"`
	ActionID string      `json:"action_id,omitempty"`
	Value    string      `json:"value,omitempty"`
	Style    string      `json:"style,omitempty"`
}

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

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccc.json")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return nil, err
	}
	var config Config
	err = json.Unmarshal(data, &config)
	if config.Sessions == nil {
		config.Sessions = make(map[string]string)
	}
	return &config, err
}

func saveConfig(config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0600)
}

// Slack API helpers

func slackAPI(config *Config, method string, params url.Values) (*SlackResponse, error) {
	apiURL := fmt.Sprintf("https://slack.com/api/%s", method)

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result SlackResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func slackAPIJSON(config *Config, method string, payload interface{}) (*SlackResponse, error) {
	apiURL := fmt.Sprintf("https://slack.com/api/%s", method)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result SlackResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func sendMessage(config *Config, channelID string, text string) error {
	const maxLen = 3000

	messages := splitMessage(text, maxLen)

	for _, msg := range messages {
		params := url.Values{
			"channel": {channelID},
			"text":    {msg},
		}

		result, err := slackAPI(config, "chat.postMessage", params)
		if err != nil {
			return err
		}
		if !result.OK {
			return fmt.Errorf("slack error: %s", result.Error)
		}

		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

func sendMessageWithButtons(config *Config, channelID string, text string, buttons []Element, blockID string) error {
	payload := map[string]interface{}{
		"channel": channelID,
		"text":    text,
		"blocks": []Block{
			{
				Type: "section",
				Text: &TextObject{Type: "mrkdwn", Text: text},
			},
			{
				Type:     "actions",
				BlockID:  blockID,
				Elements: buttons,
			},
		},
	}

	result, err := slackAPIJSON(config, "chat.postMessage", payload)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("slack error: %s", result.Error)
	}
	return nil
}

func updateMessage(config *Config, channelID string, ts string, text string) error {
	payload := map[string]interface{}{
		"channel": channelID,
		"ts":      ts,
		"text":    text,
		"blocks":  []Block{}, // Remove buttons
	}

	result, err := slackAPIJSON(config, "chat.update", payload)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("slack error: %s", result.Error)
	}
	return nil
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var messages []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			messages = append(messages, remaining)
			break
		}

		splitAt := maxLen
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			splitAt = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			splitAt = idx + 1
		}

		messages = append(messages, strings.TrimRight(remaining[:splitAt], " \n"))
		remaining = remaining[splitAt:]
	}

	return messages
}

func createChannel(config *Config, name string) (string, error) {
	// Slack channel names: lowercase, no spaces, max 80 chars
	channelName := strings.ToLower(name)
	channelName = strings.ReplaceAll(channelName, " ", "-")
	if len(channelName) > 80 {
		channelName = channelName[:80]
	}

	params := url.Values{
		"name": {channelName},
	}

	result, err := slackAPI(config, "conversations.create", params)
	if err != nil {
		return "", err
	}
	if !result.OK {
		// Channel might already exist
		if result.Error == "name_taken" {
			// Try to find existing channel
			return findChannelByName(config, channelName)
		}
		return "", fmt.Errorf("failed to create channel: %s", result.Error)
	}

	var channel SlackChannel
	if err := json.Unmarshal(result.Channel, &channel); err != nil {
		return "", fmt.Errorf("failed to parse channel: %w", err)
	}

	return channel.ID, nil
}

func findChannelByName(config *Config, name string) (string, error) {
	params := url.Values{
		"types": {"public_channel,private_channel"},
		"limit": {"1000"},
	}

	req, err := http.NewRequest("GET", "https://slack.com/api/conversations.list?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK       bool           `json:"ok"`
		Channels []SlackChannel `json:"channels"`
		Error    string         `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if !result.OK {
		return "", fmt.Errorf("failed to list channels: %s", result.Error)
	}

	for _, ch := range result.Channels {
		if ch.Name == name {
			return ch.ID, nil
		}
	}

	return "", fmt.Errorf("channel not found: %s", name)
}

// Tmux session management

var (
	tmuxSocket string
	tmuxPath   string
	cccPath    string
	claudePath string
)

func init() {
	tmuxSocket = fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())

	if path, err := exec.LookPath("tmux"); err == nil {
		tmuxPath = path
	} else {
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxPath = p
				break
			}
		}
	}

	if exe, err := os.Executable(); err == nil {
		cccPath = exe
	}

	home, _ := os.UserHomeDir()
	claudePaths := []string{
		filepath.Join(home, ".claude", "local", "claude"),
		"/usr/local/bin/claude",
	}
	for _, p := range claudePaths {
		if _, err := os.Stat(p); err == nil {
			claudePath = p
			break
		}
	}
}

func tmuxSessionExists(name string) bool {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "has-session", "-t", name)
	return cmd.Run() == nil
}

func createTmuxSession(name string, workDir string, continueSession bool) error {
	cccCmd := cccPath + " run"
	if continueSession {
		cccCmd += " -c"
	}

	args := []string{"-S", tmuxSocket, "new-session", "-d", "-s", name, "-c", workDir, "/bin/zsh", "-l", "-c", cccCmd}

	cmd := exec.Command(tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	exec.Command(tmuxPath, "-S", tmuxSocket, "set-option", "-t", name, "mouse", "on").Run()

	return nil
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

func startSession(continueSession bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	name := filepath.Base(cwd)
	tmuxName := "claude-" + name

	config, err := loadConfig()
	if err != nil {
		return runClaudeRaw(continueSession)
	}

	// Create channel if it doesn't exist
	if _, exists := config.Sessions[name]; !exists {
		channelID, err := createChannel(config, name)
		if err == nil {
			config.Sessions[name] = channelID
			saveConfig(config)
			fmt.Printf("Created Slack channel: #%s\n", name)
		}
	}

	if tmuxSessionExists(tmuxName) {
		if os.Getenv("TMUX") != "" {
			cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", tmuxName)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", tmuxName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	if err := createTmuxSession(tmuxName, cwd, continueSession); err != nil {
		return err
	}

	if os.Getenv("TMUX") != "" {
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", tmuxName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", tmuxName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sendToTmux(session string, text string) error {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "-l", text)
	if err := cmd.Run(); err != nil {
		return err
	}

	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "C-m")
	if err := cmd.Run(); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "C-m")
	return cmd.Run()
}

func killTmuxSession(name string) error {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "kill-session", "-t", name)
	return cmd.Run()
}

func listTmuxSessions() ([]string, error) {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var sessions []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name := scanner.Text()
		if strings.HasPrefix(name, "claude-") {
			sessions = append(sessions, strings.TrimPrefix(name, "claude-"))
		}
	}
	return sessions, nil
}

func sessionName(name string) string {
	return "claude-" + name
}

func getSessionByChannel(config *Config, channelID string) string {
	for name, cid := range config.Sessions {
		if cid == channelID {
			return name
		}
	}
	return ""
}

// Hook handling

func handleHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook: no config\n")
		return nil
	}

	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook: decode error: %v\n", err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: cwd=%s transcript=%s\n", hookData.Cwd, hookData.TranscriptPath)

	var sessionName string
	var channelID string
	home, _ := os.UserHomeDir()
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(home, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}
	if sessionName == "" || channelID == "" {
		fmt.Fprintf(os.Stderr, "hook: no session found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: session=%s channel=%s\n", sessionName, channelID)

	lastMessage := "Session ended"
	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			lastMessage = msg
		}
	}

	fmt.Fprintf(os.Stderr, "hook: sending message to slack\n")
	return sendMessage(config, channelID, fmt.Sprintf(":white_check_mark: *%s*\n\n%s", sessionName, lastMessage))
}

func handlePermissionHook() error {
	defer func() { recover() }()

	stdinData := make(chan []byte, 1)
	go func() {
		defer func() { recover() }()
		data, _ := io.ReadAll(os.Stdin)
		stdinData <- data
	}()

	var rawData []byte
	select {
	case rawData = <-stdinData:
	case <-time.After(2 * time.Second):
		return nil
	}

	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	config, err := loadConfig()
	if err != nil || config == nil {
		return nil
	}

	var sessionName string
	var channelID string
	home, _ := os.UserHomeDir()
	for name, cid := range config.Sessions {
		if name == "" {
			continue
		}
		expectedPath := filepath.Join(home, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}

	if sessionName == "" || channelID == "" {
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook-permission: tool=%s questions=%d\n", hookData.ToolName, len(hookData.ToolInput.Questions))
	if hookData.ToolName == "AskUserQuestion" && len(hookData.ToolInput.Questions) > 0 {
		go func() {
			defer func() { recover() }()
			for qIdx, q := range hookData.ToolInput.Questions {
				if q.Question == "" {
					continue
				}
				msg := fmt.Sprintf(":question: *%s*\n\n%s", q.Header, q.Question)

				var buttons []Element
				for i, opt := range q.Options {
					if opt.Label == "" {
						continue
					}
					// Value format: session:questionIndex:optionIndex
					value := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
					buttons = append(buttons, Element{
						Type:     "button",
						Text:     &TextObject{Type: "plain_text", Text: opt.Label},
						ActionID: fmt.Sprintf("option_%d_%d", qIdx, i),
						Value:    value,
					})
				}

				if len(buttons) > 0 {
					blockID := fmt.Sprintf("question_%s_%d", sessionName, qIdx)
					sendMessageWithButtons(config, channelID, msg, buttons, blockID)
				}
			}
		}()
		return nil
	}

	go func() {
		defer func() { recover() }()
		if hookData.ToolName != "" {
			msg := fmt.Sprintf(":lock: Permission requested: %s", hookData.ToolName)
			sendMessage(config, channelID, msg)
		}
	}()

	return nil
}

func getLastAssistantMessage(transcriptPath string) string {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lastMessage string
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] == "assistant" {
			if msg, ok := entry["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok {
					for _, c := range content {
						if block, ok := c.(map[string]interface{}); ok {
							if block["type"] == "text" {
								if text, ok := block["text"].(string); ok {
									lastMessage = text
								}
							}
						}
					}
				}
			}
		}
	}
	return lastMessage
}

func handlePromptHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: no config\n")
		return nil
	}

	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: decode error: %v\n", err)
		return nil
	}

	if hookData.Prompt == "" {
		fmt.Fprintf(os.Stderr, "hook-prompt: empty prompt\n")
		return nil
	}

	var channelID string
	home, _ := os.UserHomeDir()
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(home, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			channelID = cid
			break
		}
	}

	if channelID == "" {
		fmt.Fprintf(os.Stderr, "hook-prompt: no channel found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	prompt := hookData.Prompt
	if len(prompt) > 500 {
		prompt = prompt[:500] + "..."
	}
	fmt.Fprintf(os.Stderr, "hook-prompt: sending to channel %s\n", channelID)
	return sendMessage(config, channelID, fmt.Sprintf(":speech_balloon: %s", prompt))
}

func handleOutputHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	skipTools := map[string]bool{
		"Read": true, "Glob": true, "Grep": true, "LSP": true,
		"TodoWrite": true, "Task": true, "TaskOutput": true,
	}
	if skipTools[hookData.ToolName] {
		return nil
	}

	var channelID string
	home, _ := os.UserHomeDir()
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(home, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			channelID = cid
			break
		}
	}

	if channelID == "" {
		return nil
	}

	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			if len(msg) > 1000 {
				msg = msg[:1000] + "..."
			}
			sendMessage(config, channelID, msg)
		}
	}

	return nil
}

func handleQuestionHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	var sessionName string
	var channelID string
	home, _ := os.UserHomeDir()
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(home, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}

	if sessionName == "" || channelID == "" {
		return nil
	}

	for qIdx, q := range hookData.ToolInput.Questions {
		if q.Question == "" {
			continue
		}
		msg := fmt.Sprintf(":question: *%s*\n\n%s", q.Header, q.Question)

		var buttons []Element
		for i, opt := range q.Options {
			if opt.Label == "" {
				continue
			}
			value := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
			buttons = append(buttons, Element{
				Type:     "button",
				Text:     &TextObject{Type: "plain_text", Text: opt.Label},
				ActionID: fmt.Sprintf("option_%d_%d", qIdx, i),
				Value:    value,
			})
		}

		if len(buttons) > 0 {
			blockID := fmt.Sprintf("question_%s_%d", sessionName, qIdx)
			sendMessageWithButtons(config, channelID, msg, buttons, blockID)
		} else {
			sendMessage(config, channelID, msg)
		}
	}

	return nil
}

// Install hook in Claude settings

func installHook() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	cccPath := filepath.Join(home, "bin", "ccc")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings.json: %w", err)
	}

	stopHook := map[string]interface{}{
		"type":    "command",
		"command": cccPath + " hook",
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
	}
	hooks["Stop"] = []interface{}{stopHook}
	settings["hooks"] = hooks

	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	fmt.Println("Claude hook installed!")
	return nil
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

// One-shot Claude run

func runClaude(prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	home, _ := os.UserHomeDir()
	workDir := home

	words := strings.Fields(prompt)
	if len(words) > 0 {
		firstWord := words[0]
		potentialDir := filepath.Join(home, firstWord)
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

// Setup

func installService() error {
	home, _ := os.UserHomeDir()

	if _, err := os.Stat("/Library"); err == nil {
		return installLaunchdService(home)
	}
	return installSystemdService(home)
}

func installLaunchdService(home string) error {
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}

	plistPath := filepath.Join(plistDir, "com.ccc.plist")
	logPath := filepath.Join(home, ".ccc.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ccc</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>listen</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, cccPath, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write plist: %w", err)
	}

	exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("failed to load service: %w", err)
	}

	fmt.Println("Service installed and started (launchd)")
	return nil
}

func installSystemdService(home string) error {
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return fmt.Errorf("failed to create systemd dir: %w", err)
	}

	servicePath := filepath.Join(serviceDir, "ccc.service")
	service := fmt.Sprintf(`[Unit]
Description=Claude Code Companion
After=network.target

[Service]
ExecStart=%s listen
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, cccPath)

	if err := os.WriteFile(servicePath, []byte(service), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "ccc").Run()
	if err := exec.Command("systemctl", "--user", "start", "ccc").Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	fmt.Println("Service installed and started (systemd)")
	return nil
}

func setup(botToken, appToken string) error {
	fmt.Println("Claude Code Companion Setup (Slack)")
	fmt.Println("====================================")
	fmt.Println()

	config := &Config{
		BotToken: botToken,
		AppToken: appToken,
		Sessions: make(map[string]string),
	}

	// Step 1: Verify tokens and get bot info
	fmt.Println("Step 1/4: Verifying Slack tokens...")

	// Test bot token
	req, _ := http.NewRequest("GET", "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Slack: %w", err)
	}
	defer resp.Body.Close()

	var authResult struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
		User   string `json:"user"`
		BotID  string `json:"bot_id"`
	}
	json.NewDecoder(resp.Body).Decode(&authResult)

	if !authResult.OK {
		return fmt.Errorf("invalid bot token: %s", authResult.Error)
	}
	fmt.Printf("Bot verified: @%s\n\n", authResult.User)

	// Step 2: Get user ID
	fmt.Println("Step 2/4: Send a DM to your bot in Slack...")
	fmt.Println("   Waiting for your message...")

	// We need to use Socket Mode to receive events
	// For setup, we'll use a simpler approach: ask user to input their user ID
	fmt.Print("\nEnter your Slack User ID (find it in your profile > ... > Copy member ID): ")
	reader := bufio.NewReader(os.Stdin)
	userID, _ := reader.ReadString('\n')
	userID = strings.TrimSpace(userID)

	if userID == "" {
		return fmt.Errorf("user ID is required")
	}
	config.UserID = userID

	if err := saveConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("User ID saved: %s\n\n", userID)

	// Step 3: Install Claude hook
	fmt.Println("Step 3/4: Installing Claude hook...")
	if err := installHook(); err != nil {
		fmt.Printf("Hook installation failed: %v\n", err)
		fmt.Println("   You can install it later with: ccc install")
	} else {
		fmt.Println()
	}

	// Step 4: Install service
	fmt.Println("Step 4/4: Installing background service...")
	if err := installService(); err != nil {
		fmt.Printf("Service installation failed: %v\n", err)
		fmt.Println("   You can start manually with: ccc listen")
	} else {
		fmt.Println()
	}

	// Done!
	fmt.Println("====================================")
	fmt.Println("Setup complete!")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  ccc           Start Claude Code in current directory")
	fmt.Println("  ccc -c        Continue previous session")
	fmt.Println()
	fmt.Println("Slack commands (in any channel with the bot):")
	fmt.Println("  /ccc new <name>   Create new session")
	fmt.Println("  /ccc list         List sessions")
	fmt.Println()
	fmt.Println("Or just message in a project channel to interact with Claude.")

	return nil
}

// Doctor - check all dependencies

func doctor() {
	fmt.Println("ccc doctor")
	fmt.Println("==========")
	fmt.Println()

	allGood := true

	fmt.Print("tmux.............. ")
	if tmuxPath != "" {
		fmt.Printf("%s\n", tmuxPath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Install: brew install tmux (macOS) or apt install tmux (Linux)")
		allGood = false
	}

	fmt.Print("claude............ ")
	if claudePath != "" {
		fmt.Printf("%s\n", claudePath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Install: npm install -g @anthropic-ai/claude-code")
		allGood = false
	}

	fmt.Print("ccc in ~/bin...... ")
	home, _ := os.UserHomeDir()
	expectedCccPath := filepath.Join(home, "bin", "ccc")
	if _, err := os.Stat(expectedCccPath); err == nil {
		fmt.Printf("%s\n", expectedCccPath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Run: mkdir -p ~/bin && cp ccc ~/bin/")
		allGood = false
	}

	fmt.Print("config............ ")
	config, err := loadConfig()
	if err != nil {
		fmt.Println("not found")
		fmt.Println("   Run: ccc setup <bot_token> <app_token>")
		allGood = false
	} else {
		fmt.Printf("%s\n", getConfigPath())

		fmt.Print("  bot_token....... ")
		if config.BotToken != "" {
			fmt.Println("configured")
		} else {
			fmt.Println("missing")
			allGood = false
		}

		fmt.Print("  app_token....... ")
		if config.AppToken != "" {
			fmt.Println("configured")
		} else {
			fmt.Println("missing")
			allGood = false
		}

		fmt.Print("  user_id......... ")
		if config.UserID != "" {
			fmt.Printf("%s\n", config.UserID)
		} else {
			fmt.Println("missing")
			allGood = false
		}
	}

	fmt.Print("claude hook....... ")
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
				if _, hasStop := hooks["Stop"]; hasStop {
					fmt.Println("installed")
				} else {
					fmt.Println("not installed")
					fmt.Println("   Run: ccc install")
					allGood = false
				}
			} else {
				fmt.Println("not installed")
				fmt.Println("   Run: ccc install")
				allGood = false
			}
		} else {
			fmt.Println("settings.json parse error")
		}
	} else {
		fmt.Println("~/.claude/settings.json not found")
	}

	fmt.Print("service........... ")
	if _, err := os.Stat("/Library"); err == nil {
		plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.ccc.plist")
		if _, err := os.Stat(plistPath); err == nil {
			cmd := exec.Command("launchctl", "list", "com.ccc")
			if cmd.Run() == nil {
				fmt.Println("running (launchd)")
			} else {
				fmt.Println("installed but not running")
				fmt.Println("   Run: launchctl load ~/Library/LaunchAgents/com.ccc.plist")
			}
		} else {
			fmt.Println("not installed")
			fmt.Println("   Run: ccc setup <bot_token> <app_token>")
			allGood = false
		}
	} else {
		cmd := exec.Command("systemctl", "--user", "is-active", "ccc")
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) == "active" {
			fmt.Println("running (systemd)")
		} else {
			servicePath := filepath.Join(home, ".config", "systemd", "user", "ccc.service")
			if _, err := os.Stat(servicePath); err == nil {
				fmt.Println("installed but not running")
				fmt.Println("   Run: systemctl --user start ccc")
			} else {
				fmt.Println("not installed")
				fmt.Println("   Run: ccc setup <bot_token> <app_token>")
				allGood = false
			}
		}
	}

	fmt.Println()
	if allGood {
		fmt.Println("All checks passed!")
	} else {
		fmt.Println("Some issues found. Fix them and run 'ccc doctor' again.")
	}
}

// Main listen loop using Socket Mode

func listen() error {
	myPid := os.Getpid()
	cmd := exec.Command("pgrep", "-f", "ccc listen")
	output, _ := cmd.Output()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if pid, err := strconv.Atoi(line); err == nil && pid != myPid {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}

	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: ccc setup <bot_token> <app_token>")
	}

	fmt.Printf("Bot listening... (user: %s)\n", config.UserID)
	fmt.Printf("Active sessions: %d\n", len(config.Sessions))
	fmt.Println("Press Ctrl+C to stop")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		os.Exit(0)
	}()

	// Connect via Socket Mode
	for {
		if err := connectSocketMode(config); err != nil {
			fmt.Fprintf(os.Stderr, "Socket Mode error: %v (reconnecting in 5s...)\n", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func connectSocketMode(config *Config) error {
	// Get WebSocket URL
	req, err := http.NewRequest("POST", "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.AppToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
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
	fmt.Printf("Connected to Socket Mode\n")

	// Connect WebSocket
	ws, err := websocket.Dial(wsURL, "", "https://slack.com")
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer ws.Close()

	var wsMutex sync.Mutex

	// Handle messages
	for {
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
			fmt.Println("Socket Mode connected")

		case "events_api":
			var eventCallback EventCallback
			json.Unmarshal(envelope.Payload, &eventCallback)

			if eventCallback.Type == "event_callback" {
				go handleSlackEvent(config, eventCallback.Event)
			}

		case "interactive":
			var action BlockActionPayload
			json.Unmarshal(envelope.Payload, &action)
			go handleBlockAction(config, action)

		case "disconnect":
			return fmt.Errorf("disconnected by server")
		}
	}
}

func handleSlackEvent(config *Config, eventData json.RawMessage) {
	var event struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		User    string `json:"user"`
		Text    string `json:"text"`
		BotID   string `json:"bot_id"`
	}
	json.Unmarshal(eventData, &event)

	// Ignore bot messages
	if event.BotID != "" {
		return
	}

	// Only accept from authorized user
	if event.User != config.UserID {
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

	fmt.Printf("[message] @%s in %s: %s\n", event.User, channelID, text)

	// Reload config
	config, _ = loadConfig()

	// Handle commands
	if strings.HasPrefix(text, "!ping") {
		sendMessage(config, channelID, "pong!")
		return
	}

	if strings.HasPrefix(text, "!away") {
		config.Away = !config.Away
		saveConfig(config)
		if config.Away {
			sendMessage(config, channelID, ":walking: Away mode ON")
		} else {
			sendMessage(config, channelID, ":house: Away mode OFF")
		}
		return
	}

	if strings.HasPrefix(text, "!list") {
		sessions, _ := listTmuxSessions()
		if len(sessions) == 0 {
			sendMessage(config, channelID, "No active sessions")
		} else {
			sendMessage(config, channelID, "Sessions:\n"+strings.Join(sessions, "\n"))
		}
		return
	}

	if strings.HasPrefix(text, "!kill ") {
		name := strings.TrimSpace(strings.TrimPrefix(text, "!kill "))
		if _, exists := config.Sessions[name]; !exists {
			sendMessage(config, channelID, fmt.Sprintf("Session '%s' not found", name))
			return
		}
		killTmuxSession(sessionName(name))
		delete(config.Sessions, name)
		saveConfig(config)
		sendMessage(config, channelID, fmt.Sprintf(":wastebasket: Session '%s' killed", name))
		return
	}

	if strings.HasPrefix(text, "!c ") {
		cmdStr := strings.TrimPrefix(text, "!c ")
		output, err := executeCommand(cmdStr)
		if err != nil {
			output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
		}
		sendMessage(config, channelID, "```\n"+output+"\n```")
		return
	}

	if strings.HasPrefix(text, "!new ") || strings.HasPrefix(text, "!continue ") {
		isNew := strings.HasPrefix(text, "!new ")
		var arg string
		if isNew {
			arg = strings.TrimSpace(strings.TrimPrefix(text, "!new "))
		} else {
			arg = strings.TrimSpace(strings.TrimPrefix(text, "!continue "))
		}
		continueSession := !isNew

		if arg == "" {
			sendMessage(config, channelID, "Usage: !new <name> or !continue <name>")
			return
		}

		// Create channel if needed
		var targetChannelID string
		if cid, exists := config.Sessions[arg]; exists {
			targetChannelID = cid
		} else {
			cid, err := createChannel(config, arg)
			if err != nil {
				sendMessage(config, channelID, fmt.Sprintf(":x: Failed to create channel: %v", err))
				return
			}
			targetChannelID = cid
			config.Sessions[arg] = cid
			saveConfig(config)
		}

		// Find work directory
		home, _ := os.UserHomeDir()
		workDir := filepath.Join(home, arg)
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			workDir = home
		}

		tmuxName := "claude-" + arg

		// Kill existing if running
		if tmuxSessionExists(tmuxName) {
			killTmuxSession(tmuxName)
			time.Sleep(300 * time.Millisecond)
		}

		if err := createTmuxSession(tmuxName, workDir, continueSession); err != nil {
			sendMessage(config, targetChannelID, fmt.Sprintf(":x: Failed to start: %v", err))
			return
		}

		time.Sleep(500 * time.Millisecond)
		if tmuxSessionExists(tmuxName) {
			action := "started"
			if continueSession {
				action = "continued"
			}
			sendMessage(config, targetChannelID, fmt.Sprintf(":rocket: Session '%s' %s!\n\nSend messages here to interact with Claude.", arg, action))
		} else {
			sendMessage(config, targetChannelID, ":warning: Session died immediately. Check if ~/bin/ccc works.")
		}
		return
	}

	// Check if message is in a session channel
	sessionName := getSessionByChannel(config, channelID)
	if sessionName != "" {
		tmuxName := "claude-" + sessionName
		if tmuxSessionExists(tmuxName) {
			if err := sendToTmux(tmuxName, text); err != nil {
				sendMessage(config, channelID, fmt.Sprintf(":x: Failed to send: %v", err))
			}
		} else {
			sendMessage(config, channelID, ":warning: Session not running. Use !new or !continue to restart.")
		}
		return
	}

	// Otherwise, run one-shot Claude
	sendMessage(config, channelID, ":robot_face: Running Claude...")
	go func(p string, cid string) {
		defer func() {
			if r := recover(); r != nil {
				sendMessage(config, cid, fmt.Sprintf(":boom: Panic: %v", r))
			}
		}()
		output, err := runClaude(p)
		if err != nil {
			if strings.Contains(err.Error(), "context deadline exceeded") {
				output = fmt.Sprintf(":stopwatch: Timeout (10min)\n\n%s", output)
			} else {
				output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
			}
		}
		sendMessage(config, cid, output)
	}(text, channelID)
}

func handleBlockAction(config *Config, action BlockActionPayload) {
	// Only accept from authorized user
	if action.User.ID != config.UserID {
		return
	}

	if len(action.Actions) == 0 {
		return
	}

	act := action.Actions[0]

	// Parse value: session:questionIndex:optionIndex
	parts := strings.Split(act.Value, ":")
	if len(parts) != 3 {
		return
	}

	sessionName := parts[0]
	optionIndex, _ := strconv.Atoi(parts[2])

	// Update message to show selection
	originalText := action.Message.Text
	newText := fmt.Sprintf("%s\n\n:white_check_mark: Selected: *%s*", originalText, act.Value)
	updateMessage(config, action.Channel.ID, action.Message.TS, newText)

	tmuxName := "claude-" + sessionName
	if tmuxSessionExists(tmuxName) {
		// Send arrow down keys to select option, then Enter
		for i := 0; i < optionIndex; i++ {
			exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", tmuxName, "Down").Run()
			time.Sleep(50 * time.Millisecond)
		}
		exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", tmuxName, "Enter").Run()
		fmt.Printf("[action] Selected option %d for %s\n", optionIndex, sessionName)
	}
}

func printHelp() {
	fmt.Printf(`ccc - Claude Code Companion v%s

Control Claude Code remotely via Slack and tmux.

USAGE:
    ccc                     Start/attach tmux session in current directory
    ccc -c                  Continue previous session
    ccc <message>           Send notification (if away mode is on)

COMMANDS:
    setup <bot> <app>       Complete setup (tokens, hook, service)
    doctor                  Check all dependencies and configuration
    listen                  Start the Slack bot listener manually
    install                 Install Claude hook manually
    run                     Run Claude directly (used by tmux sessions)
    hook                    Handle Claude hook (internal)

SLACK COMMANDS (in any channel):
    !ping                   Check if bot is alive
    !away                   Toggle away mode
    !new <name>             Create new session with channel
    !continue <name>        Continue existing session
    !kill <name>            Kill a session
    !list                   List active sessions
    !c <cmd>                Execute shell command

FLAGS:
    -h, --help              Show this help
    -v, --version           Show version

For more info: https://github.com/sderosiaux/claude-slack-relay-anywhere
`, version)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printHelp()
			return
		case "-v", "--version", "version":
			fmt.Printf("ccc version %s\n", version)
			return
		}
	}

	if len(os.Args) < 2 {
		if err := startSession(false); err != nil {
			os.Exit(1)
		}
		return
	}

	if os.Args[1] == "-c" {
		if err := startSession(true); err != nil {
			os.Exit(1)
		}
		return
	}

	switch os.Args[1] {
	case "run":
		continueSession := len(os.Args) > 2 && os.Args[2] == "-c"
		if err := runClaudeRaw(continueSession); err != nil {
			os.Exit(1)
		}
		return

	case "setup":
		if len(os.Args) < 4 {
			fmt.Println("Usage: ccc setup <bot_token> <app_token>")
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
		if err := listen(); err != nil {
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
			fmt.Fprintf(os.Stderr, "Error: not configured. Run: ccc setup <bot_token> <app_token>\n")
			os.Exit(1)
		}

		if !config.Away {
			fmt.Println("Away mode off, skipping notification.")
			return
		}

		// Find session channel for current directory
		cwd, _ := os.Getwd()
		home, _ := os.UserHomeDir()
		message := strings.Join(os.Args[1:], " ")

		for name, channelID := range config.Sessions {
			expectedPath := filepath.Join(home, name)
			if cwd == expectedPath || strings.HasSuffix(cwd, "/"+name) {
				if err := sendMessage(config, channelID, message); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}
		}

		fmt.Println("Not in a session directory, notification not sent.")
	}
}
