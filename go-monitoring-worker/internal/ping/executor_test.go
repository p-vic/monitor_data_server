package ping

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestExecuteTCP_Success(t *testing.T) {
	// Start a dummy local TCP server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on local port: %v", err)
	}
	defer l.Close()

	go func() {
		// Accept and instantly close to simulate healthy ping
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	addr := l.Addr().String()

	executor := NewNetworkExecutor()
	ctx := context.Background()

	res := executor.Execute(ctx, addr, ProtocolTCP, 2*time.Second)

	if !res.IsUp {
		t.Errorf("expected TCP ping to be up, but was down. Error: %s", res.ErrorMsg)
	}
	if res.Latency <= 0 {
		t.Errorf("expected Latency to be > 0, got %v", res.Latency)
	}
}

func TestExecuteTCP_ConnectionRefused(t *testing.T) {
	executor := NewNetworkExecutor()
	ctx := context.Background()

	// Port 54321 is assumed to be closed
	res := executor.Execute(ctx, "127.0.0.1:54321", ProtocolTCP, 1*time.Second)

	if res.IsUp {
		t.Error("expected TCP ping to be down, but was up")
	}
	if res.ErrorMsg != "connection refused" {
		t.Errorf("expected 'connection refused', got '%s'", res.ErrorMsg)
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	executor := NewNetworkExecutor()
	ctx, cancel := context.WithCancel(context.Background())

	cancel() // Cancel instantly before execution

	res := executor.Execute(ctx, "192.0.2.1:80", ProtocolTCP, 5*time.Second)

	if res.IsUp {
		t.Error("expected ping to fail due to context cancellation")
	}
	if res.ErrorMsg != "context canceled" {
		t.Errorf("expected 'context canceled', got '%s'", res.ErrorMsg)
	}
}

func TestExecute_InvalidProtocol(t *testing.T) {
	executor := NewNetworkExecutor()
	ctx := context.Background()

	res := executor.Execute(ctx, "127.0.0.1", "udp", 1*time.Second)

	if res.IsUp {
		t.Error("expected ping to fail for unsupported protocol")
	}
	if !strings.Contains(res.ErrorMsg, "unsupported protocol") {
		t.Errorf("expected unsupported protocol error, got %s", res.ErrorMsg)
	}
}

func TestParseNetworkError(t *testing.T) {
	tests := []struct {
		err      error
		expected string
	}{
		{context.DeadlineExceeded, "timeout exceeded"},
		{context.Canceled, "context canceled"},
		{&net.AddrError{Err: "connection refused"}, "connection refused"}, // Mock string match
		{&net.DNSError{Err: "no such host"}, "dns resolution failed"},
	}

	for _, tt := range tests {
		actual := parseNetworkError(tt.err)
		if actual != tt.expected {
			t.Errorf("parseNetworkError(%v) = %s, expected %s", tt.err, actual, tt.expected)
		}
	}
}
