# Build stage
FROM golang:1.25-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deferred-work-bot ./...

# Runtime stage — needs node for the `claude` CLI
FROM node:22-alpine
RUN apk add --no-cache ca-certificates tini && \
    npm install -g @anthropic-ai/claude-code
WORKDIR /app
COPY --from=builder /out/deferred-work-bot /usr/local/bin/deferred-work-bot
COPY projects.yaml signals.yaml ./
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/entrypoint.sh"]
