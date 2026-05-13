// Package linear provides the Linear tracker adapter.
package linear

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the Linear GraphQL client.
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Linear client.
func NewClient(endpoint, apiKey string) *Client {
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GraphQLRequest represents a GraphQL request.
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a GraphQL response.
type GraphQLResponse struct {
	Data   json.RawMessage   `json:"data"`
	Errors []GraphQLError   `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error.
type GraphQLError struct {
	Message string `json:"message"`
}

// Query executes a GraphQL query.
func (c *Client) Query(query string, variables map[string]interface{}) (*GraphQLResponse, error) {
	body, err := json.Marshal(GraphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result GraphQLResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL: %s", result.Errors[0].Message)
	}

	return &result, nil
}

// Queries for Linear API.
const (
	CandidateIssuesQuery = `
query SymphonyCandidateIssues($team: String!, $states: [String!]!) {
  issues(filter: {team: {id: {eq: $team}}, state: {name: {in: $states}}}, first: 50) {
    nodes { id identifier title description priority state { name } branchName url createdAt updatedAt assignee { id } labels(first: 10) { nodes { name } } }
  }
}`
	IssueStatesQuery = `
query SymphonyIssueStates($ids: [UUID!]!) {
  issues(filter: {id: {in: $ids}}) {
    nodes { id identifier state { name } }
  }
}`
)