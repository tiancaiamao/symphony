// Package workspace provides per-issue workspace management.
package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Manager handles workspace lifecycle.
type Manager struct {
	root       string
	hookTimeout time.Duration
}

// New creates a new workspace manager.
func New(root string, hookTimeoutMs int) *Manager {
	return &Manager{
		root:       root,
		hookTimeout: time.Duration(hookTimeoutMs) * time.Millisecond,
	}
}

// CreateForIssue creates a workspace for the given issue.
func (m *Manager) CreateForIssue(issueID, identifier string) (string, error) {
	safeID := sanitizeIdentifier(identifier)
	workspace := filepath.Join(m.root, safeID)

	if err := os.RemoveAll(workspace); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clean workspace: %w", err)
	}

	if err := os.MkdirAll(workspace, 0755); err != nil {
		return "", fmt.Errorf("create workspace: %w", err)
	}

	return workspace, nil
}

// Remove removes a workspace.
func (m *Manager) Remove(workspace string) error {
	if !m.isPathSafe(workspace) {
		return fmt.Errorf("unsafe path: %s", workspace)
	}
	return os.RemoveAll(workspace)
}

// WorkspaceForIssue returns the workspace path for an issue.
func (m *Manager) WorkspaceForIssue(identifier string) string {
	return filepath.Join(m.root, sanitizeIdentifier(identifier))
}

func (m *Manager) isPathSafe(workspace string) bool {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(m.root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absWorkspace)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// RunHook executes a workspace hook script.
func (m *Manager) RunHook(hookScript, workspace, issueID, identifier string) error {
	if hookScript == "" {
		return nil
	}

	// Replace template variables
	replacements := map[string]string{
		"{{.Workspace}}": workspace,
		"{{.TaskID}}":   issueID,
		"{{.IssueID}}":  issueID,
		"{{.Identifier}}": identifier,
	}

	result := hookScript
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", result)
	cmd.Dir = workspace
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	env := os.Environ()
	env = append(env,
		fmt.Sprintf("SYMPHONY_WORKSPACE=%s", workspace),
		fmt.Sprintf("SYMPHONY_ISSUE_ID=%s", issueID),
		fmt.Sprintf("SYMPHONY_ISSUE_IDENTIFIER=%s", identifier),
	)
	cmd.Env = env

	return cmd.Run()
}

// EnsureRoot ensures the workspace root exists.
func (m *Manager) EnsureRoot() error {
	return os.MkdirAll(m.root, 0755)
}

func sanitizeIdentifier(identifier string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	safe := re.ReplaceAllString(identifier, "_")
	if len(safe) > 100 {
		safe = safe[:100]
	}
	return safe
}

// ReadWorkflow reads the WORKFLOW.md file from the workspace root.
func (m *Manager) ReadWorkflow(filename string) (string, error) {
	// Try to read from current directory first (if running from project root)
	content, err := os.ReadFile(filename)
	if err == nil {
		return string(content), nil
	}

	// Try to read from workspace root
	workflowPath := filepath.Join(m.root, "..", filename)
	content, err = os.ReadFile(workflowPath)
	if err == nil {
		return string(content), nil
	}

	// Try to read from GOPATH or project root
	// This is a fallback for when running in development mode
	currentDir, _ := os.Getwd()
	workflowPath = filepath.Join(currentDir, filename)
	content, err = os.ReadFile(workflowPath)
	if err == nil {
		return string(content), nil
	}

	return "", fmt.Errorf("WORKFLOW.md not found: tried %s, %s, %s", filename, filepath.Join(m.root, "..", filename), workflowPath)
}