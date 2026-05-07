package runner

import (
	"context"
	"errors"

	"github.com/go-kit/kit/endpoint"
)

type EndpointSet struct {
	Run      endpoint.Endpoint
	RunIssue endpoint.Endpoint
}

type RunRequest struct {
	Prompt   string `json:"prompt"`
	Repo     string `json:"repo,omitempty"`
	Ref      string `json:"ref,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`
	Event    string `json:"event,omitempty"`
	PRNumber int    `json:"pr_number,omitempty"`
}

type RunIssueRequest struct {
	Repo        string `json:"repo"`
	Ref         string `json:"ref,omitempty"`
	IssueNumber int    `json:"issue_number"`
}

type Result struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func RunEndpoint(svc Service) endpoint.Endpoint {
	return func(ctx context.Context, request any) (any, error) {
		req, ok := request.(RunRequest)
		if !ok {
			return nil, errors.New("invalid request type")
		}
		return svc.Run(ctx, req)
	}
}

func RunIssueEndpoint(svc Service) endpoint.Endpoint {
	return func(ctx context.Context, request any) (any, error) {
		req, ok := request.(RunIssueRequest)
		if !ok {
			return nil, errors.New("invalid request type")
		}
		return svc.RunIssue(ctx, req)
	}
}
