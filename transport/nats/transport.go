package nats

import (
	"context"
	"encoding/json"

	"github.com/go-kit/kit/endpoint"
	"github.com/nats-io/nats.go/micro"

	runner "github.com/flarexio/claude-runner"
)

func EndpointHandler(ep endpoint.Endpoint) micro.HandlerFunc {
	return func(r micro.Request) {
		var req runner.Request
		if err := json.Unmarshal(r.Data(), &req); err != nil {
			r.Error("400", err.Error(), nil)
			return
		}

		ctx := context.Background()
		resp, err := ep(ctx, req)
		if err != nil {
			r.Error("417", err.Error(), nil)
			return
		}

		r.RespondJSON(resp)
	}
}
