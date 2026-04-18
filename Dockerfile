# Stage 1: Build the Go application
FROM golang:1.24-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# We use CGO_ENABLED=0 to ensure the Go binary is statically linked
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# Run the Playwright installer ONLY to get the driver JS files
# (We skip browsers because the Microsoft image already has them)
RUN PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5700.1 install

# Stage 2: The Official Playwright Environment
# We use the exact version of Playwright that playwright-go v0.5700.1 wraps (v1.57.0)
FROM mcr.microsoft.com/playwright:v1.57.0-jammy

WORKDIR /app

# Install Node.js (required for Claude Code CLI)
RUN apt-get update && apt-get install -y curl && \
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y nodejs && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create a non-root user for Claude Code (it refuses --dangerously-skip-permissions as root)
RUN groupadd -r appuser && useradd -r -g appuser -d /home/appuser -m -s /bin/bash appuser

# Create .ssh directory for appuser with proper permissions
# Add Server B host key so SSH doesn't prompt for verification
ARG SSH_REMOTE_HOST=136.115.249.188
RUN mkdir -p /home/appuser/.ssh && \
    chmod 700 /home/appuser/.ssh && \
    ssh-keyscan -H "$SSH_REMOTE_HOST" > /home/appuser/.ssh/known_hosts && \
    chown -R appuser:appuser /home/appuser/.ssh

# Copy the compiled Go binary
COPY --from=builder /app/main .

# Copy the static files
COPY --from=builder /app/static ./static

# Copy the driver JS files that were extracted during build
COPY --from=builder /root/.cache/ms-playwright-go /home/appuser/.cache/ms-playwright-go

# Ensure appuser owns the app directory and cache
RUN chown -R appuser:appuser /app /home/appuser/.cache

# Crucial step: The official image stores browsers in /ms-playwright, not /root/.cache
# We must tell playwright-go to look there for the browsers.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

# Set Anthropic API key (pass at runtime via docker-compose or -e flag)
# ENV ANTHROPIC_API_KEY=your-api-key-here
# ENV ANTHROPIC_BASE_URL=https://api.opencode.ai/v1

# Switch to non-root user
USER appuser

EXPOSE 3000
CMD ["./main"]