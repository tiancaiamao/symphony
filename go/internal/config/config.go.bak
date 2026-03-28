// Package config provides typed configuration access.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration.
type Config struct {
	Tracker   TrackerConfig
	Polling   PollingConfig
	Workspace WorkspaceConfig
	Hooks     HooksConfig
	Agent     AgentConfig
	Server    ServerConfig
}

// TrackerConfig holds issue tracker settings.
type TrackerConfig struct {
	Kind           string
	Endpoint       string
	APIKey         string
	ProjectSlug    string
	Assignee       string
	ActiveStates   []string
	TerminalStates []string
}

// PollingConfig holds polling interval settings.
type PollingConfig struct {
	IntervalMs int
}

// WorkspaceConfig holds workspace settings.
type WorkspaceConfig struct {
	Root string
}

// HooksConfig holds workspace hook settings.
type HooksConfig struct {
	AfterCreate  string
	BeforeRun    string
	AfterRun     string
	BeforeRemove string
	TimeoutMs    int
}

// AgentConfig holds agent settings.
type AgentConfig struct {
	Kind                string
	Command             string
	Args                []string
	Env                 map[string]string
	MaxConcurrentAgents  int
	MaxTurns            int
	MaxRetryBackoffMs   int
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port int
}

// Load creates a Config from a workflow config map.
func Load(workflowCfg map[string]interface{}) (*Config, error) {
	cfg := &Config{
		Tracker: TrackerConfig{
			Kind:           getString(workflowCfg, "tracker", "kind", "linear"),
			Endpoint:       getString(workflowCfg, "tracker", "endpoint", "https://api.linear.app/graphql"),
			ActiveStates:   []string{"Todo", "In Progress"},
			TerminalStates: []string{"Closed", "Done", "Cancelled", "Canceled", "Duplicate"},
		},
		Polling: PollingConfig{
			IntervalMs: getInt(workflowCfg, "polling", "interval_ms", 30000),
		},
		Workspace: WorkspaceConfig{
			Root: getPath(workflowCfg, "workspace", "root", filepath.Join(os.TempDir(), "symphony_workspaces")),
		},
		Hooks: HooksConfig{
			AfterCreate:  getString(workflowCfg, "hooks", "after_create", ""),
			BeforeRun:    getString(workflowCfg, "hooks", "before_run", ""),
			AfterRun:     getString(workflowCfg, "hooks", "after_run", ""),
			BeforeRemove: getString(workflowCfg, "hooks", "before_remove", ""),
			TimeoutMs:    getInt(workflowCfg, "hooks", "timeout_ms", 60000),
		},
		Agent: AgentConfig{
			Kind:               getString(workflowCfg, "agent", "kind", "ai"),
			Command:            getString(workflowCfg, "agent", "command", "ai"),
			Args:               getStringSlice(workflowCfg, "agent", "args", []string{"--mode", "rpc"}),
			MaxConcurrentAgents: getInt(workflowCfg, "agent", "max_concurrent_agents", 10),
			MaxTurns:           getInt(workflowCfg, "agent", "max_turns", 20),
			MaxRetryBackoffMs:  getInt(workflowCfg, "agent", "max_retry_backoff_ms", 300000),
		},
		Server: ServerConfig{
			Port: getInt(workflowCfg, "server", "port", 8080),
		},
	}

	// Apply nested config maps
	if tracker, ok := workflowCfg["tracker"].(map[string]interface{}); ok {
		cfg.Tracker.APIKey = resolveEnv(getStringDirect(tracker, "api_key", ""))
		cfg.Tracker.ProjectSlug = resolveEnv(getStringDirect(tracker, "project_slug", ""))
		cfg.Tracker.Assignee = resolveEnv(getStringDirect(tracker, "assignee", ""))
		if states, ok := tracker["active_states"].([]interface{}); ok {
			cfg.Tracker.ActiveStates = toStringSlice(states)
		}
		if states, ok := tracker["terminal_states"].([]interface{}); ok {
			cfg.Tracker.TerminalStates = toStringSlice(states)
		}
	}

	// Get agent env vars
	if agent, ok := workflowCfg["agent"].(map[string]interface{}); ok {
		if env, ok := agent["env"].(map[string]interface{}); ok {
			cfg.Agent.Env = make(map[string]string)
			for k, v := range env {
				if str, ok := v.(string); ok {
					cfg.Agent.Env[k] = resolveEnv(str)
				}
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks configuration values.
func (c *Config) Validate() error {
	if c.Tracker.Kind != "linear" && c.Tracker.Kind != "memory" {
		return fmt.Errorf("invalid tracker kind: %s", c.Tracker.Kind)
	}

	if c.Tracker.Kind == "linear" {
		if c.Tracker.APIKey == "" {
			return fmt.Errorf("missing required config: tracker.api_key")
		}
		if c.Tracker.ProjectSlug == "" {
			return fmt.Errorf("missing required config: tracker.project_slug")
		}
	}

	if c.Polling.IntervalMs <= 0 {
		c.Polling.IntervalMs = 30000 // sensible default
	}

	if c.Hooks.TimeoutMs <= 0 {
		c.Hooks.TimeoutMs = 60000 // sensible default
	}

	if c.Agent.MaxConcurrentAgents <= 0 {
		c.Agent.MaxConcurrentAgents = 1 // sensible default
	}

	return nil
}

// GetRetryDelay calculates exponential backoff delay.
func (c *Config) GetRetryDelay(attempt int) time.Duration {
	baseMs := 10000
	delayMs := baseMs
	for i := 0; i < attempt; i++ {
		delayMs *= 2
	}
	if delayMs > c.Agent.MaxRetryBackoffMs {
		delayMs = c.Agent.MaxRetryBackoffMs
	}
	return time.Duration(delayMs) * time.Millisecond
}

// --- Helper functions ---

func getString(cfg map[string]interface{}, section, key, defaultVal string) string {
	if sec, ok := cfg[section].(map[string]interface{}); ok {
		return resolveEnv(getStringDirect(sec, key, defaultVal))
	}
	return defaultVal
}

func getStringDirect(cfg map[string]interface{}, key, defaultVal string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return defaultVal
}

func getInt(cfg map[string]interface{}, section, key string, defaultVal int) int {
	if sec, ok := cfg[section].(map[string]interface{}); ok {
		if v, ok := sec[key].(int); ok {
			return v
		}
		if v, ok := sec[key].(float64); ok {
			return int(v)
		}
		if v, ok := sec[key].(string); ok {
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
	}
	return defaultVal
}

func getPath(cfg map[string]interface{}, section, key, defaultVal string) string {
	val := getString(cfg, section, key, defaultVal)
	if val == "" {
		return defaultVal
	}
	// Expand ~ to home directory
	if strings.HasPrefix(val, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return val
		}
		return filepath.Join(home, strings.TrimPrefix(val, "~/"))
	}
	return val
}

func getStringSlice(cfg map[string]interface{}, section, key string, defaultVal []string) []string {
	if sec, ok := cfg[section].(map[string]interface{}); ok {
		if v, ok := sec[key].([]interface{}); ok {
			return toStringSlice(v)
		}
	}
	return defaultVal
}

func toStringSlice(v []interface{}) []string {
	result := make([]string, 0, len(v))
	for _, item := range v {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func resolveEnv(value string) string {
	if strings.HasPrefix(value, "$") {
		name := strings.TrimPrefix(value, "$")
		name = strings.Trim(name, "{}")
		if envVal := os.Getenv(name); envVal != "" {
			return envVal
		}
	}
	return value
}