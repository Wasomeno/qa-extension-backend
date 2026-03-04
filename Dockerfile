# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git for downloading dependencies and gcompat for running playwright driver
RUN apk add --no-cache git gcompat

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
# CGO_ENABLED=0 ensures a static build
# GOOS=linux GOARCH=amd64 (or arm64 depending on VPS)
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Install playwright driver
RUN go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5700.1 install driver

# Run stage
FROM alpine:latest

WORKDIR /app

# Add CA certificates for HTTPS requests (e.g., to GitLab/OpenAI)
RUN apk --no-cache add ca-certificates tzdata

# Install dependencies for Playwright
RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    bash \
    curl \
    gcompat

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the playwright driver from the builder stage
COPY --from=builder /root/.cache/ms-playwright-go /root/.cache/ms-playwright-go

# Set environment variables for Playwright
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium-browser

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
