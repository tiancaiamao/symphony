// Package tracker provides the issue tracker interface.
package tracker

import "time"

// Issue represents a normalized issue from the tracker.
type Issue struct {
	ID          string
	Identifier  string // e.g., "SYMPHONY-123"
	Title       string
	Description string
	Priority    int
	State       string
	BranchName  string
	URL         string
	AssigneeID  string
	Labels      []string
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

// Tracker is the interface for issue tracker implementations.
type Tracker interface {
	FetchCandidateIssues() ([]Issue, error)
	FetchIssueStatesByIDs(ids []string) ([]Issue, error)
}