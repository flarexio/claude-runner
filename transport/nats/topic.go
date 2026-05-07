package nats

import (
	"github.com/nats-io/nats.go/micro"

	runner "github.com/flarexio/claude-runner"
)

func AddEndpoints(group micro.Group, endpoints runner.EndpointSet) {
	group.AddEndpoint("run", EndpointHandler[runner.RunRequest](endpoints.Run))
	group.AddEndpoint("run-issue", EndpointHandler[runner.RunIssueRequest](endpoints.RunIssue))
}
