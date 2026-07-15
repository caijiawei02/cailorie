# --- Build stage ---
FROM golang:1.24-alpine AS builder
ARG GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /cailorie ./cmd/bot

# --- Runtime stage ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata curl sqlite && \
    adduser -D -h /var/lib/cailorie cailorie
WORKDIR /var/lib/cailorie
COPY --from=builder /cailorie /usr/local/bin/cailorie
USER cailorie
EXPOSE 8080 8081
VOLUME ["/var/lib/cailorie"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -sf http://127.0.0.1:8081/health || exit 1
ENTRYPOINT ["/usr/local/bin/cailorie"]