# Build stage
# We use Debian (bookworm) as builder because it has glibc, 
# which is required to run the playwright-go driver installation tool.
FROM golang:1.24-bookworm AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
# CGO_ENABLED=0 ensures a static build
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Install playwright driver
# This downloads the driver (including JS files and a bundled node binary) to /root/.cache/ms-playwright-go
RUN go run github.com/playwright-community/playwright-go/cmd/playwright@v0.5700.1 install driver

# Run stage
# We use Alpine for the final image to keep it lightweight.
FROM alpine:latest

WORKDIR /app

# Add CA certificates for HTTPS requests and tzdata
RUN apk --no-cache add ca-certificates tzdata

# Install dependencies for Playwright
# We install Alpine's native nodejs and tell playwright-go to use it 
# via PLAYWRIGHT_NODEJS_PATH to avoid glibc compatibility issues with 
# the bundled node binary.
RUN apk add --no-cache \
    chromium \
    nss \
    freetype \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    bash \
    curl \
    nodejs

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Copy the playwright driver cache from the builder stage
# This contains the JS files required by the driver.
COPY --from=builder /root/.cache/ms-playwright-go /root/.cache/ms-playwright-go

# Set environment variables for Playwright
# 1. Skip downloading browsers (use Alpine's chromium)
# 2. Point to Alpine's chromium executable
# 3. Point to Alpine's native nodejs to execute the driver JS scripts
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium-browser
ENV PLAYWRIGHT_NODEJS_PATH=/usr/bin/node

# Expose port 3000
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
