package nats

import (
	"context"
	"encoding/json"

	"github.com/go-kit/kit/endpoint"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/flarexio/claude-runner"
)

func RunHandler(endpoint endpoint.Endpoint) micro.HandlerFunc {
	return func(r micro.Request) {
		var req runner.Request
		if err := json.Unmarshal(r.Data(), &req); err != nil {
			r.Error("400", err.Error(), nil)
			return
		}

		ctx := context.Background()
		resp, err := endpoint(ctx, req)
		if err != nil {
			r.Error("417", err.Error(), nil)
			return
		}

		r.RespondJSON(resp)
	}
}

func AsyncRunHandler(ep endpoint.Endpoint, nc *nats.Conn, topicPrefix string) micro.HandlerFunc {
	return func(r micro.Request) {
		var req runner.Request
		if err := json.Unmarshal(r.Data(), &req); err != nil {
			r.Error("400", err.Error(), nil)
			return
		}

		callback := func(result *runner.Result) {
			data, _ := json.Marshal(result)
			nc.Publish(topicPrefix+".results."+result.ID, data)
		}

		asyncReq := runner.AsyncRunRequest{
			Request:  req,
			Callback: callback,
		}

		ctx := context.Background()
		resp, err := ep(ctx, asyncReq)
		if err != nil {
			r.Error("417", err.Error(), nil)
			return
		}

		r.RespondJSON(resp)
	}
}
