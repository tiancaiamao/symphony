// Package main provides the Symphony server entry point.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"
	"symphonia/internal/config"
	httpserver "symphonia/internal/http"
	"symphonia/internal/logging"
	"symphonia/internal/scheduler"
	"symphonia/internal/store"
	"symphonia/internal/workspace"
)

var (
	configPath = flag.String("config", "", "Path to config.yaml")
	port       = flag.Int("port", 8080, "HTTP server port")
	dbPath     = flag.String("db", "", "Path to SQLite database (default: ~/.symphony/symphony.db)")
	logLevel   = flag.String("log-level", "info", "Log level: debug, info, warn, error")
	help       = flag.Bool("help", false, "Show help")
)

func main() {
	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	logging.Init(*logLevel)

	// Determine config path
	cfgPath := *configPath
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".symphony", "config.yaml")
	}

	// Load config
	cfg, err := loadOrCreateConfig(cfgPath)
	if err != nil {
		logging.ErrLog("Failed to load config", logging.Err(err))
		os.Exit(1)
	}

	logging.Info("Loaded configuration",
		logging.String("database", cfg.Workspace.Root),
		logging.Int("port", *port))

	// Determine database path
	db := *dbPath
	if db == "" {
		home, _ := os.UserHomeDir()
		db = filepath.Join(home, ".symphony", "symphony.db")
	}

	// Ensure directory exists
	dbDir := filepath.Dir(db)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		logging.ErrLog("Failed to create database directory", logging.Err(err))
		os.Exit(1)
	}

	// Initialize database
	database, err := store.InitDB(db)
	if err != nil {
		logging.ErrLog("Failed to initialize database", logging.Err(err))
		os.Exit(1)
	}
	defer database.Close()

	logging.Info("Database initialized", logging.String("path", db))

	// Create workspace manager
	wm := workspace.New(cfg.Workspace.Root, cfg.Hooks.TimeoutMs)

	// Create scheduler
	sched := scheduler.NewScheduler(database, cfg, wm)

	// Start scheduler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Run(ctx); err != nil {
		logging.ErrLog("Failed to start scheduler", logging.Err(err))
		os.Exit(1)
	}

	logging.Info("Scheduler started")

	// Start HTTP server
	server := httpserver.New(*port, database, sched)
	go func() {
		logging.Info("Starting HTTP server", logging.Int("port", *port))
		if err := server.Start(); err != nil {
			logging.ErrLog("HTTP server error", logging.Err(err))
		}
	}()

	logging.Info("Symphony started", logging.Int("port", *port))
	fmt.Printf("\n🎼 Symphony running at http://localhost:%d\n", *port)
	fmt.Println("   Kanban UI: http://localhost:8080/board")
	fmt.Println("   API:       http://localhost:8080/api/tasks")
	fmt.Println("   Health:    http://localhost:8080/health")
	fmt.Println("\n   Press Ctrl+C to stop\n")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logging.Info("Shutting down...")
	cancel()
	sched.Stop()
	logging.Info("Shutdown complete")
}

func loadOrCreateConfig(path string) (*config.Config, error) {
	// Check if config exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create default config
		if err := createDefaultConfig(path); err != nil {
			return nil, fmt.Errorf("create default config: %w", err)
		}
		logging.Info("Created default config", logging.String("path", path))
	}

	// Load config file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Parse as YAML
	cfgMap, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return config.Load(cfgMap)
}

func createDefaultConfig(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	defaultConfig := fmt.Sprintf(`# Symphony Configuration

# Workspace settings
workspace:
  root: %s/.symphony/workspaces

# Polling interval
polling:
  interval_ms: 30000

# Agent settings
agent:
  kind: ai
  command: ai
  args:
    - "--mode"
    - "rpc"
  max_concurrent_agents: 3
  max_turns: 20
  max_retry_backoff_ms: 300000

# Hooks (optional)
hooks:
  after_create: ""
  before_run: ""
  after_run: ""
  before_remove: ""
  timeout_ms: 60000

# Tracker (for Linear integration - optional)
tracker:
  kind: memory
`, home)

	return os.WriteFile(path, []byte(defaultConfig), 0644)
}

func parseYAML(data []byte) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}