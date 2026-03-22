FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /claude-runner cmd/claude-runner/main.go

FROM node:24-alpine3.23
RUN apk add --no-cache git
RUN npm install -g @anthropic-ai/claude-code
COPY --from=builder /claude-runner /bin/claude-runner
EXPOSE 8080
ENTRYPOINT ["claude-runner"]
