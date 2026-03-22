FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /claude-runner-client cmd/claude-runner-client/main.go

FROM alpine:3.23
COPY --from=builder /claude-runner-client /bin/claude-runner-client
COPY build/claude-runner-client/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["claude-runner-client"]
