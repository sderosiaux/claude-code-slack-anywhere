package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTP client with proper timeouts
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxConnsPerHost:     10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

// Slack API types

type SlackResponse struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Channel json.RawMessage `json:"channel,omitempty"`
	TS      string          `json:"ts,omitempty"`
	URL     string          `json:"url,omitempty"` // For Socket Mode connection
	File    *SlackFileInfo  `json:"file,omitempty"`
}

type SlackFileInfo struct {
	ID        string `json:"id"`
	Permalink string `json:"permalink"`
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
	Type     string      `json:"type"`
	Channel  string      `json:"channel"`
	User     string      `json:"user"`
	Text     string      `json:"text"`
	TS       string      `json:"ts"`
	ThreadTS string      `json:"thread_ts,omitempty"`
	BotID    string      `json:"bot_id,omitempty"`
	Files    []SlackFile `json:"files,omitempty"`
}

// SlackFile represents a file attachment in Slack messages
type SlackFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Mimetype           string `json:"mimetype"`
	Filetype           string `json:"filetype"`
	URLPrivateDownload string `json:"url_private_download"`
	URLPrivate         string `json:"url_private"`
}

// Socket Mode envelope
type SocketModeEnvelope struct {
	Type         string          `json:"type"`
	EnvelopeID   string          `json:"envelope_id"`
	Payload      json.RawMessage `json:"payload"`
	RetryAttempt int             `json:"retry_attempt,omitempty"`
	RetryReason  string          `json:"retry_reason,omitempty"`
}

// Event callback payload
type EventCallback struct {
	Type    string          `json:"type"`
	EventID string          `json:"event_id"`
	Event   json.RawMessage `json:"event"`
}

// Block action payload (button clicks)
type BlockActionPayload struct {
	Type    string    `json:"type"`
	User    SlackUser `json:"user"`
	Channel struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	Message     SlackMessage  `json:"message"`
	Actions     []BlockAction `json:"actions"`
	ResponseURL string        `json:"response_url"`
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

// Slack API helpers

func slackAPI(config *Config, method string, params url.Values) (*SlackResponse, error) {
	apiURL := fmt.Sprintf("https://slack.com/api/%s", method)

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := httpClient.Do(req)
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

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result SlackResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

// downloadSlackFileToDir downloads a file from Slack to a specified directory
// Returns the local file path or error
func downloadSlackFileToDir(config *Config, file SlackFile, targetDir string) (string, error) {
	// Use url_private_download if available, otherwise url_private
	downloadURL := file.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = file.URLPrivate
	}
	if downloadURL == "" {
		return "", fmt.Errorf("no download URL for file %s", file.Name)
	}

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download file: HTTP %d", resp.StatusCode)
	}

	// Use original filename (sanitized)
	filename := file.Name
	// Remove any path separators from filename for safety
	filename = filepath.Base(filename)
	if filename == "" || filename == "." {
		filename = file.ID
	}

	localPath := filepath.Join(targetDir, filename)

	// If file already exists, add timestamp
	if _, err := os.Stat(localPath); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		localPath = filepath.Join(targetDir, fmt.Sprintf("%s-%d%s", base, time.Now().Unix(), ext))
	}

	outFile, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		os.Remove(localPath)
		return "", err
	}

	return localPath, nil
}

// isImageFile checks if a Slack file is an image
func isImageFile(file SlackFile) bool {
	return strings.HasPrefix(file.Mimetype, "image/")
}

// isTextFile checks if a Slack file is a text/code file that Claude can read
// We use a blacklist approach: accept everything except known binary formats
func isTextFile(file SlackFile) bool {
	// Binary mimetypes to exclude
	binaryMimes := []string{
		"application/octet-stream",
		"application/zip",
		"application/gzip",
		"application/x-tar",
		"application/x-rar",
		"application/pdf",
		"application/msword",
		"application/vnd.ms-",
		"application/vnd.openxmlformats-",
		"audio/",
		"video/",
	}
	for _, prefix := range binaryMimes {
		if strings.HasPrefix(file.Mimetype, prefix) {
			return false
		}
	}

	// Binary extensions to exclude
	binaryExts := []string{
		".zip", ".tar", ".gz", ".rar", ".7z",
		".exe", ".dll", ".so", ".dylib",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".mp3", ".mp4", ".wav", ".avi", ".mov", ".mkv",
		".bin", ".dat", ".o", ".a",
	}
	ext := strings.ToLower(filepath.Ext(file.Name))
	for _, e := range binaryExts {
		if ext == e {
			return false
		}
	}

	// Everything else is considered text (snippets, code, markdown, etc.)
	return true
}

func sendMessage(config *Config, channelID string, text string) (string, error) {
	const maxLen = 3000

	messages := splitMessage(text, maxLen)
	var lastTS string

	for _, msg := range messages {
		params := url.Values{
			"channel": {channelID},
			"text":    {msg},
		}

		result, err := slackAPI(config, "chat.postMessage", params)
		if err != nil {
			return "", err
		}
		if !result.OK {
			return "", fmt.Errorf("slack error: %s", result.Error)
		}
		lastTS = result.TS

		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return lastTS, nil
}

// sendMessageToThread sends a message as a reply to an existing message (thread)
func sendMessageToThread(config *Config, channelID string, threadTS string, text string) error {
	const maxLen = 3000

	messages := splitMessage(text, maxLen)

	for _, msg := range messages {
		params := url.Values{
			"channel":   {channelID},
			"text":      {msg},
			"thread_ts": {threadTS},
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

// sendMessageToThreadGetTS sends a message to a thread and returns its timestamp
func sendMessageToThreadGetTS(config *Config, channelID string, threadTS string, text string) (string, error) {
	payload := map[string]interface{}{
		"channel":   channelID,
		"text":      text,
		"thread_ts": threadTS,
	}

	result, err := slackAPIJSON(config, "chat.postMessage", payload)
	if err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("slack error: %s", result.Error)
	}
	return result.TS, nil
}

func addReaction(config *Config, channelID string, timestamp string, emoji string) error {
	params := url.Values{
		"channel":   {channelID},
		"timestamp": {timestamp},
		"name":      {emoji},
	}
	result, err := slackAPI(config, "reactions.add", params)
	if err != nil {
		logf("Reaction error: %v", err)
		return err
	}
	if !result.OK && result.Error != "already_reacted" {
		logf("Reaction API error: %s", result.Error)
		return fmt.Errorf("reaction failed: %s", result.Error)
	}
	return nil
}

func removeReaction(config *Config, channelID string, timestamp string, emoji string) error {
	params := url.Values{
		"channel":   {channelID},
		"timestamp": {timestamp},
		"name":      {emoji},
	}
	result, err := slackAPI(config, "reactions.remove", params)
	if err != nil {
		return err
	}
	if !result.OK && result.Error != "no_reaction" {
		return fmt.Errorf("remove reaction failed: %s", result.Error)
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

func deleteMessage(config *Config, channelID string, ts string) error {
	payload := map[string]interface{}{
		"channel": channelID,
		"ts":      ts,
	}

	result, err := slackAPIJSON(config, "chat.delete", payload)
	if err != nil {
		return err
	}
	if !result.OK {
		// Ignore "message_not_found" - already deleted
		if result.Error != "message_not_found" {
			return fmt.Errorf("slack error: %s", result.Error)
		}
	}
	return nil
}

// uploadSnippet uploads content as a Slack snippet and returns the file URL
func uploadSnippet(config *Config, channelID, threadTS, filename, content, title string) (string, error) {
	// Use files.upload API (v1)
	params := url.Values{
		"channels":        {channelID},
		"thread_ts":       {threadTS},
		"content":         {content},
		"filename":        {filename},
		"title":           {title},
		"filetype":        {"text"},
		"initial_comment": {"Full output:"},
	}

	result, err := slackAPI(config, "files.upload", params)
	if err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("failed to upload snippet: %s", result.Error)
	}
	return result.File.Permalink, nil
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

	resp, err := httpClient.Do(req)
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

func getChannelName(config *Config, channelID string) (string, error) {
	params := url.Values{
		"channel": {channelID},
	}

	req, err := http.NewRequest("GET", "https://slack.com/api/conversations.info?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK      bool `json:"ok"`
		Channel struct {
			Name string `json:"name"`
		} `json:"channel"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("failed to get channel info: %s", result.Error)
	}

	return result.Channel.Name, nil
}

// archiveChannel archives a Slack channel
func archiveChannel(config *Config, channelID string) error {
	params := url.Values{
		"channel": {channelID},
	}

	result, err := slackAPI(config, "conversations.archive", params)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("failed to archive channel: %s", result.Error)
	}
	return nil
}
