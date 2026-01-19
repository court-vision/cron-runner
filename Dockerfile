FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod main.go ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cron-runner

FROM alpine:latest AS certs

RUN apk --no-cache add ca-certificates

FROM scratch

COPY --from=builder /app/cron-runner /cron-runner

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

CMD ["/cron-runner"]
