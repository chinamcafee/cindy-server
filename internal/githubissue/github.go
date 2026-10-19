package githubissue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	githubRepoOwner = "makecindy"
	githubRepoName  = "cindy"
	githubAPIBase   = "https://api.github.com"
)

type githubIssuePayload struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

type githubIssueResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// postGitHubIssue creates the issue with the platform bot token. The token is
// only ever held server-side and never echoed to clients or logs.
func (m *Module) postGitHubIssue(ctx context.Context, title, body, issueType string) (issueRef, error) {
	payload, err := json.Marshal(githubIssuePayload{
		Title:  title,
		Body:   body,
		Labels: []string{issueType},
	})
	if err != nil {
		return issueRef{}, fmt.Errorf("encode github payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues", githubAPIBase, githubRepoOwner, githubRepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return issueRef{}, fmt.Errorf("build github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(m.githubToken()))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cindy-server")

	resp, err := m.client.Do(req)
	if err != nil {
		return issueRef{}, fmt.Errorf("call github api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Drain a bounded amount so the connection can be reused; never log it.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return issueRef{}, fmt.Errorf("github api status %d", resp.StatusCode)
	}

	var out githubIssueResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return issueRef{}, fmt.Errorf("decode github response: %w", err)
	}
	if out.Number <= 0 || out.HTMLURL == "" {
		return issueRef{}, fmt.Errorf("github api returned an incomplete issue")
	}
	return issueRef{Number: out.Number, URL: out.HTMLURL}, nil
}
