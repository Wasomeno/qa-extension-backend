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

# Copy the compiled Go binary
COPY --from=builder /app/main .

# Copy the static files
COPY --from=builder /app/static ./static

# Copy the driver JS files that were extracted during build
COPY --from=builder /root/.cache/ms-playwright-go /root/.cache/ms-playwright-go

# Crucial step: The official image stores browsers in /ms-playwright, not /root/.cache
# We must tell playwright-go to look there for the browsers.
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright

EXPOSE 3000
CMD ["./main"]
