package runner

import (
	"context"
	"errors"

	"github.com/go-kit/kit/endpoint"
)

type EndpointSet struct {
	Run    endpoint.Endpoint
	AsyncRun endpoint.Endpoint
}

type Request struct {
	Prompt       string   `json:"prompt"`
	Repo         string   `json:"repo,omitempty"`
	Ref          string   `json:"ref,omitempty"`
	WorkDir      string   `json:"work_dir,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
}

type Result struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

type AsyncRunRequest struct {
	Request
	Callback ResultFunc
}

type AsyncRunResponse struct {
	ID string `json:"id"`
}

func AsyncRunEndpoint(svc Service) endpoint.Endpoint {
	return func(ctx context.Context, request any) (any, error) {
		req, ok := request.(AsyncRunRequest)
		if !ok {
			return nil, errors.New("invalid request type")
		}

		id, err := svc.AsyncRun(ctx, req.Request, req.Callback)
		if err != nil {
			return nil, err
		}

		return AsyncRunResponse{ID: id}, nil
	}
}

func RunEndpoint(svc Service) endpoint.Endpoint {
	return func(ctx context.Context, request any) (any, error) {
		req, ok := request.(Request)
		if !ok {
			return nil, errors.New("invalid request type")
		}

		return svc.Run(ctx, req)
	}
}
