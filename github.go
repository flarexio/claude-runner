package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultGitHubBaseURL = "https://api.github.com"

// GitHubClient is the minimal API surface that runner needs to operate on
// GitHub issues. It is an interface so tests can supply fakes.
type GitHubClient interface {
	GetIssue(ctx context.Context, repo string, number int) (*Issue, error)
	AddLabels(ctx context.Context, repo string, number int, labels []string) error
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	CreateComment(ctx context.Context, repo string, number int, body string) error
}

type Issue struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	Labels  []Label `json:"labels"`
}

type Label struct {
	Name string `json:"name"`
}

func (i *Issue) IsOpen() bool {
	return strings.EqualFold(i.State, "open")
}

func (i *Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

type httpGitHubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewGitHubClient returns an HTTP-backed GitHubClient.
func NewGitHubClient(cfg GitHubConfig) GitHubClient {
	base := cfg.BaseURL
	if base == "" {
		base = defaultGitHubBaseURL
	}
	return &httpGitHubClient{
		baseURL: strings.TrimRight(base, "/"),
		token:   cfg.Token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *httpGitHubClient) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d", repo, number)

	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, statusError("get issue", resp)
	}

	var issue Issue
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return nil, fmt.Errorf("decode issue: %w", err)
	}
	return &issue, nil
}

func (c *httpGitHubClient) AddLabels(ctx context.Context, repo string, number int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number)
	body := map[string]any{"labels": labels}

	resp, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return statusError("add labels", resp)
	}
	return nil
}

func (c *httpGitHubClient) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	path := fmt.Sprintf("/repos/%s/issues/%d/labels/%s", repo, number, url.PathEscape(label))

	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 means the label was already absent; treat as success.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return statusError("remove label", resp)
}

func (c *httpGitHubClient) CreateComment(ctx context.Context, repo string, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)

	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return statusError("create comment", resp)
	}
	return nil
}

func (c *httpGitHubClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.http.Do(req)
}

func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("github %s: %s: %s", op, resp.Status, strings.TrimSpace(string(body)))
}
