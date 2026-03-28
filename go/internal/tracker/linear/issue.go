package linear

import (
	"time"

	"symphonia/internal/tracker"
)

// IssueResponse represents a Linear issue.
type IssueResponse struct {
	ID          string     `json:"id"`
	Identifier  string     `json:"identifier"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Priority    int        `json:"priority"`
	State       StateInfo  `json:"state"`
	BranchName  string     `json:"branchName"`
	URL         string     `json:"url"`
	CreatedAt   *time.Time `json:"createdAt"`
	UpdatedAt   *time.Time `json:"updatedAt"`
	Assignee    *struct {
		ID string `json:"id"`
	} `json:"assignee"`
	Labels struct {
		Nodes []struct{ Name string `json:"name"` } `json:"nodes"`
	} `json:"labels"`
}

// StateInfo holds state information.
type StateInfo struct {
	Name string `json:"name"`
}

// ToIssue converts IssueResponse to tracker.Issue.
func (i *IssueResponse) ToIssue() tracker.Issue {
	labels := make([]string, len(i.Labels.Nodes))
	for j, l := range i.Labels.Nodes {
		labels[j] = l.Name
	}
	return tracker.Issue{
		ID:          i.ID,
		Identifier:  i.Identifier,
		Title:       i.Title,
		Description: i.Description,
		Priority:    i.Priority,
		State:       i.State.Name,
		BranchName:  i.BranchName,
		URL:         i.URL,
		Labels:      labels,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   i.UpdatedAt,
	}
}

// StateResponse for minimal state queries.
type StateResponse struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	State      StateInfo `json:"state"`
}

// ToIssue converts StateResponse to tracker.Issue.
func (s *StateResponse) ToIssue() tracker.Issue {
	return tracker.Issue{
		ID:         s.ID,
		Identifier: s.Identifier,
		State:      s.State.Name,
	}
}