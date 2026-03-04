# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git for downloading dependencies
RUN apk add --no-cache git

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

# Run stage
FROM alpine:latest

WORKDIR /app

# Add CA certificates for HTTPS requests (e.g., to GitLab/OpenAI)
RUN apk --no-cache add ca-certificates tzdata

# Install dependencies for Playwright
# We install nodejs and set PLAYWRIGHT_NODEJS_PATH to /usr/bin/node
# to avoid compatibility issues with the bundled node binary on Alpine.
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

# Set environment variables for Playwright
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
ENV PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/usr/bin/chromium-browser
ENV PLAYWRIGHT_NODEJS_PATH=/usr/bin/node

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
