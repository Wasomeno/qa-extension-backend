FROM golang:1.24-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o fix-worker .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y git nodejs npm && \
    npm install -g @anthropic-ai/claude-code && \
    apt-get clean && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /app/fix-worker .

# Create non-root user for Claude Code
RUN groupadd -r fixagent && useradd -r -g fixagent -d /home/fixagent -m -s /bin/bash fixagent

EXPOSE 8080
USER fixagent
CMD ["./fix-worker"]