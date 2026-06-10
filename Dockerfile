# Stage 1: Build the Go application
FROM golang:1.25-bookworm AS builder
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

# Create a non-root user to run the app
RUN groupadd -r appuser && useradd -r -g appuser -d /home/appuser -m -s /bin/bash appuser

# Install Pi CLI binary
# See https://pi.ai/docs for installation instructions
# COPY --from=... or RUN curl ... to install the pi binary into /usr/local/bin/pi

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