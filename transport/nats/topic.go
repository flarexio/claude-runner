package nats

import (
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/flarexio/claude-runner"
)

func AddEndpoints(group micro.Group, endpoints runner.EndpointSet, nc *nats.Conn, topicPrefix string) {
	group.AddEndpoint("run", RunHandler(endpoints.Run))
	group.AddEndpoint("async-run", AsyncRunHandler(endpoints.AsyncRun, nc, topicPrefix))
}
