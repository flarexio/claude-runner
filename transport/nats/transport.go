package nats

import (
	"context"
	"encoding/json"

	"github.com/go-kit/kit/endpoint"
	"github.com/nats-io/nats.go/micro"
)

// EndpointHandler decodes the NATS payload into T, then dispatches to ep.
func EndpointHandler[T any](ep endpoint.Endpoint) micro.HandlerFunc {
	return func(r micro.Request) {
		var req T
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
