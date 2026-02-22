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

# Copy the pre-built binary file from the previous stage
COPY --from=builder /app/main .

# Expose port 3000 to the outside world
EXPOSE 3000

# Command to run the executable
CMD ["./main"]
