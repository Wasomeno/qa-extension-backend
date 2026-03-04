# Build stage
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Install playwright driver and browsers
# This downloads everything to /root/.cache/ms-playwright-go
RUN go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5700.1 install --with-deps chromium

# Run stage
# We switch to debian-slim for the final image to ensure 100% compatibility 
# with Playwright's glibc and GUI library requirements.
FROM debian:bookworm-slim

WORKDIR /app

# Install CA certificates, tzdata, and Node.js
RUN apt-get update && apt-get install -y \
    ca-certificates \
    tzdata \
    curl \
    gnupg \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the playwright driver and browser cache from the builder stage
COPY --from=builder /root/.cache/ms-playwright-go /root/.cache/ms-playwright-go

# Install the browser executable AND the system dependencies required by Chromium
RUN npx playwright install --with-deps chromium

# Expose port 3000
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
