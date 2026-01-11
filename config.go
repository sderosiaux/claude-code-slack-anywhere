package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config stores bot configuration and session mappings
type Config struct {
	BotToken    string            `json:"bot_token"`              // Slack Bot Token (xoxb-...)
	AppToken    string            `json:"app_token"`              // Slack App Token (xapp-...) for Socket Mode
	UserID      string            `json:"user_id,omitempty"`      // Authorized Slack user ID (deprecated, use user_ids)
	UserIDs     []string          `json:"user_ids,omitempty"`     // Authorized Slack user IDs
	Sessions    map[string]string `json:"sessions"`               // session name -> channel ID
	ProjectsDir string            `json:"projects_dir,omitempty"` // Base directory for projects
}

// IsAuthorizedUser checks if a user ID is in the authorized list
func (c *Config) IsAuthorizedUser(userID string) bool {
	// Check new UserIDs list
	for _, id := range c.UserIDs {
		if id == userID {
			return true
		}
	}
	// Fallback to old single UserID for backward compat
	return c.UserID != "" && c.UserID == userID
}

// ConfigManager provides thread-safe access to Config
type ConfigManager struct {
	mu     sync.RWMutex
	config *Config
	path   string
}

func NewConfigManager(configPath string) *ConfigManager {
	path := configPath
	if path == "" {
		path = getConfigPath()
	}
	return &ConfigManager{
		path: path,
	}
}

func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := os.ReadFile(cm.path)
	if err != nil {
		return err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if config.Sessions == nil {
		config.Sessions = make(map[string]string)
	}
	cm.config = &config
	return nil
}

func (cm *ConfigManager) Get() *Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

func (cm *ConfigManager) GetSession(name string) (string, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return "", false
	}
	val, ok := cm.config.Sessions[name]
	return val, ok
}

func (cm *ConfigManager) SetSession(name, channelID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.config == nil {
		return fmt.Errorf("config not loaded")
	}
	cm.config.Sessions[name] = channelID
	return cm.saveLocked()
}

func (cm *ConfigManager) DeleteSession(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.config == nil {
		return fmt.Errorf("config not loaded")
	}
	delete(cm.config.Sessions, name)
	return cm.saveLocked()
}

func (cm *ConfigManager) GetSessionByChannel(channelID string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return ""
	}
	for name, cid := range cm.config.Sessions {
		if cid == channelID {
			return name
		}
	}
	return ""
}

func (cm *ConfigManager) GetAllSessions() map[string]string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	// Return a copy to prevent external mutation
	sessions := make(map[string]string, len(cm.config.Sessions))
	for k, v := range cm.config.Sessions {
		sessions[k] = v
	}
	return sessions
}

func (cm *ConfigManager) saveLocked() error {
	data, err := json.MarshalIndent(cm.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cm.path, data, 0600)
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccsa.json")
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

// getProjectsDir returns the base directory for projects from config
// Returns empty string if not configured (mandatory field)
func getProjectsDir(config *Config) string {
	if config == nil || config.ProjectsDir == "" {
		return ""
	}
	// Expand ~ if present
	if len(config.ProjectsDir) > 2 && config.ProjectsDir[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, config.ProjectsDir[2:])
	}
	return config.ProjectsDir
}

// getSessionByChannel returns session name for a channel (used in tests)
func getSessionByChannel(config *Config, channelID string) string {
	if config == nil || config.Sessions == nil {
		return ""
	}
	for name, cid := range config.Sessions {
		if cid == channelID {
			return name
		}
	}
	return ""
}
