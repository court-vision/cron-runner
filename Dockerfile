FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go ./
COPY internal/ ./internal/

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cron-runner

# Get CA certificates
FROM alpine:latest AS certs
RUN apk --no-cache add ca-certificates

# Final minimal image
FROM scratch

COPY --from=builder /app/cron-runner /cron-runner
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Expose health check port
EXPOSE 8082

# Fire-and-forget by default (FIRE_AND_FORGET=true)
# Set FIRE_AND_FORGET=false to use polling mode instead
CMD ["/cron-runner"]
