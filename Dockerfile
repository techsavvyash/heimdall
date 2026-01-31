# Build stage
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make openssl

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Generate RSA keys for JWT
RUN mkdir -p keys && \
    openssl genrsa -out keys/private.pem 2048 && \
    openssl rsa -in keys/private.pem -pubout -out keys/public.pem

# Build the application and migration tool
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o migrate ./cmd/migrate

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata wget bash

# Create non-root user
RUN addgroup -g 1000 heimdall && \
    adduser -D -u 1000 -G heimdall heimdall

WORKDIR /app

# Copy binaries and keys from builder
COPY --from=builder /build/server /app/
COPY --from=builder /build/migrate /app/
COPY --from=builder /build/keys /app/keys

# Copy .env.example as template
COPY .env.example /app/.env.example

# Copy entrypoint script
COPY docker-entrypoint.sh /app/
RUN chmod +x /app/docker-entrypoint.sh

# Set ownership
RUN chown -R heimdall:heimdall /app

# Switch to non-root user
USER heimdall

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the entrypoint script
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/server"]
