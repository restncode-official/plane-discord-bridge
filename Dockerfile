# --- Build Stage ---
FROM golang:1.21-alpine AS builder

# Install git for fetching dependencies if needed
RUN apk add --no-cache git

WORKDIR /app

# Copy the code
COPY main.go .

# Initialize module and build
RUN go mod init plane-bridge && \
    go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bridge .

# --- Final Stage ---
FROM alpine:latest

# Add certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the binary from the builder
COPY --from=builder /app/bridge .

# Environment variables (Defaults)
ENV WEB_PORT=8080
ENV WORKSPACE_NAME="My Plane Workspace"

EXPOSE 8080

# Run the binary
CMD ["./bridge"]
