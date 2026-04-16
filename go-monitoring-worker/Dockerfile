# ==========================================
# Build Stage - Golang Worker
# ==========================================
FROM golang:1.24.0-alpine AS builder

# git not needed: go mod download uses GOPROXY (HTTPS). tzdata required for timezone handling.
RUN apk add --no-cache tzdata

WORKDIR /app

# Dependency resolution cached layer
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire Go Source code
COPY . .

# Static Compliation for scratch image compatibility
# CGO_ENABLED=0 disables C dependencies since go-ping handles Raw Sockets natively
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /ips-worker ./cmd/monitor

# ==========================================
# Production Stage - Alpine Minimal
# ==========================================
FROM alpine:3.19

# Copy ca-certificates and tzdata directly from builder — avoids a second apk call
# which is slow/unreliable depending on the server's CDN connectivity.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

WORKDIR /root/

# Copy the pre-built binary
COPY --from=builder /ips-worker .

# Worker Internal Health/Metrics Port
EXPOSE 8084

CMD ["./ips-worker"]
