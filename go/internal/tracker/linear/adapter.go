package linear

import (
	"encoding/json"
	"fmt"

	"symphonia/internal/tracker"
)

// Adapter implements tracker.Tracker for Linear.
type Adapter struct {
	client       *Client
	teamID       string
	activeStates []string
}

// NewAdapter creates a new Linear tracker adapter.
func NewAdapter(endpoint, apiKey, projectSlug string, activeStates []string) (*Adapter, error) {
	client := NewClient(endpoint, apiKey)
	
	// Query team to get its ID (projectSlug is the team key like "PROJ")
	resp, err := client.Query(`query GetTeam($key: String!) { teams(filter: { key: { eq: $key } }) { nodes { id key } } }`, map[string]interface{}{
		"key": projectSlug,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch team: %w", err)
	}

	var result struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal team: %w", err)
	}

	if len(result.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("team not found: %s", projectSlug)
	}

	return &Adapter{
		client:       client,
		teamID:       result.Teams.Nodes[0].ID,
		activeStates: activeStates,
	}, nil
}

// FetchCandidateIssues returns issues in active states.
func (a *Adapter) FetchCandidateIssues() ([]tracker.Issue, error) {
	resp, err := a.client.Query(CandidateIssuesQuery, map[string]interface{}{
		"team":   a.teamID,
		"states": a.activeStates,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch candidates: %w", err)
	}

	var result struct {
		Issues struct {
			Nodes []IssueResponse `json:"nodes"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	issues := make([]tracker.Issue, len(result.Issues.Nodes))
	for i, issue := range result.Issues.Nodes {
		issues[i] = issue.ToIssue()
	}
	return issues, nil
}

// FetchIssueStatesByIDs returns current states for specific issues.
func (a *Adapter) FetchIssueStatesByIDs(ids []string) ([]tracker.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	resp, err := a.client.Query(IssueStatesQuery, map[string]interface{}{
		"ids": ids,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch states: %w", err)
	}

	var result struct {
		Issues struct {
			Nodes []StateResponse `json:"nodes"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	issues := make([]tracker.Issue, len(result.Issues.Nodes))
	for i, issue := range result.Issues.Nodes {
		issues[i] = issue.ToIssue()
	}
	return issues, nil
}