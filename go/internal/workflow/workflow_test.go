package workflow

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	content := `# Symphony Workflow

## Config

` + "```yaml\n" + `
tracker:
  kind: memory
  active_states:
    - Backlog
  terminal_states:
    - Done

agent:
  kind: ai
  command: echo
  args:
    - hello
  max_concurrent_agents: 2
  max_turns: 10

workspace:
  root: /tmp/test-ws

hooks:
  timeout_ms: 5000

polling:
  interval_ms: 1000
` + "```\n"

	wf, err := ReadAndParse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ReadAndParse failed: %v", err)
	}

	tracker := wf.Config["tracker"].(map[string]interface{})
	if tracker["kind"] != "memory" {
		t.Errorf("expected tracker kind 'memory', got '%v'", tracker["kind"])
	}

	agent := wf.Config["agent"].(map[string]interface{})
	if agent["kind"] != "ai" {
		t.Errorf("expected agent kind 'ai', got '%v'", agent["kind"])
	}

	polling := wf.Config["polling"].(map[string]interface{})
	interval := polling["interval_ms"]
	switch v := interval.(type) {
	case int:
		if v != 1000 {
			t.Errorf("expected polling interval 1000, got %d", v)
		}
	case int64:
		if v != 1000 {
			t.Errorf("expected polling interval 1000, got %d", v)
		}
	default:
		t.Errorf("unexpected type for interval_ms: %T", interval)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load("nonexistent.md")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestFrontMatterFallback(t *testing.T) {
	content := `---
tracker:
  kind: linear
---
# Prompt below`
	wf, err := ReadAndParse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ReadAndParse failed: %v", err)
	}
	tracker := wf.Config["tracker"].(map[string]interface{})
	if tracker["kind"] != "linear" {
		t.Errorf("expected tracker kind 'linear', got '%v'", tracker["kind"])
	}
}