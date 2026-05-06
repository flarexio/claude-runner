package runner

import (
	"context"
	"errors"

	"github.com/go-kit/kit/endpoint"
)

type EndpointSet struct {
	Run endpoint.Endpoint
}

type Request struct {
	Prompt   string `json:"prompt"`
	Repo     string `json:"repo,omitempty"`
	Ref      string `json:"ref,omitempty"`
	BaseRef  string `json:"base_ref,omitempty"`
	Event    string `json:"event,omitempty"`
	PRNumber int    `json:"pr_number,omitempty"`
	WorkDir  string `json:"work_dir,omitempty"`
}

type Result struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
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
