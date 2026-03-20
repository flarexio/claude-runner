package nats

import (
	"github.com/nats-io/nats.go/micro"

	runner "github.com/flarexio/claude-runner"
)

func AddEndpoints(group micro.Group, endpoints runner.EndpointSet) {
	group.AddEndpoint("run", RunHandler(endpoints.Run))
}
