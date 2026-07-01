# --- Build stage ---
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /cailorie ./cmd/bot

# --- Runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -h /var/lib/cailorie cailorie
WORKDIR /var/lib/cailorie
COPY --from=builder /cailorie /usr/local/bin/cailorie
USER cailorie
VOLUME ["/var/lib/cailorie"]
ENTRYPOINT ["/usr/local/bin/cailorie"]