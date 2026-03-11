# ==========================================
# Build Stage - Golang Worker
# ==========================================
FROM golang:1.24.0-alpine AS builder

# Git required for fetching go dependencies
RUN apk update && apk add --no-cache git tzdata

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
# Production Stage - Scratch Minimal
# ==========================================
# We use Alpine instead of Scratch strictly to have CA-Certificates and tzdata.
# The binary needs them to execute SSL requests to FastAPI and format Timezones.
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata \
    # ICMP ping operations require the SYS_NET_ADMIN capability 
    # Or allowing unprivileged pings via sysctl equivalent (done in Go Code)
    && rm -rf /var/cache/apk/*

WORKDIR /root/

# Copy the pre-built binary
COPY --from=builder /ips-worker .

# Worker Internal Health/Metrics Port
EXPOSE 8084

CMD ["./ips-worker"]
