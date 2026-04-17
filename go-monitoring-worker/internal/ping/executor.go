package ping

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-ping/ping"
)

// Protocol enum determines the type of network health check.
type Protocol string

const (
	ProtocolTCP  Protocol = "tcp"
	ProtocolICMP Protocol = "icmp"
)

// Result is the deterministic outcome of a health check.
type Result struct {
	Target   string
	Protocol Protocol
	Latency  time.Duration
	IsUp     bool
	ErrorMsg string
}

// Executor defines the clean architecture port for network checks.
type Executor interface {
	Execute(ctx context.Context, target string, protocol Protocol, timeout time.Duration) Result
}

// NetworkExecutor is the standard production implementation.
type NetworkExecutor struct{}

// NewNetworkExecutor instantiates a new Executor without global state.
func NewNetworkExecutor() *NetworkExecutor {
	return &NetworkExecutor{}
}

// Execute performs the network check, wrapping it safely in isolated context timeouts.
func (e *NetworkExecutor) Execute(ctx context.Context, target string, protocol Protocol, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// 1. Context Cancellation check: Ensure operation ties to the parent context.
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch protocol {
	case ProtocolTCP:
		return e.executeTCP(opCtx, target)
	case ProtocolICMP:
		return e.executeICMP(opCtx, target, timeout)
	default:
		return Result{
			Target:   target,
			Protocol: protocol,
			IsUp:     false,
			ErrorMsg: fmt.Sprintf("unsupported protocol: %s", protocol),
		}
	}
}

func (e *NetworkExecutor) executeTCP(ctx context.Context, target string) Result {
	var dialer net.Dialer
	start := time.Now()

	// 2. Explicit Network Error Handling using DialContext
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return Result{
			Target:   target,
			Protocol: ProtocolTCP,
			IsUp:     false,
			ErrorMsg: parseNetworkError(err),
		}
	}
	defer conn.Close()

	return Result{
		Target:   target,
		Protocol: ProtocolTCP,
		Latency:  time.Since(start),
		IsUp:     true,
	}
}

func (e *NetworkExecutor) executeICMP(ctx context.Context, target string, timeout time.Duration) Result {
	pinger, err := ping.NewPinger(target)
	if err != nil {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: fmt.Sprintf("failed to initialize pinger: %v", err),
		}
	}

	// Security: Run unprivileged to avoid requiring root (SYS_NET_ADMIN).
	pinger.SetPrivileged(false)
	pinger.Count = 3
	pinger.Interval = 200 * time.Millisecond
	pinger.Timeout = timeout

	done := make(chan error, 1)
	go func() {
		done <- pinger.Run()
	}()

	select {
	case <-ctx.Done():
		pinger.Stop()
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: parseNetworkError(ctx.Err()),
		}
	case err := <-done:
		if err != nil {
			return Result{
				Target:   target,
				Protocol: ProtocolICMP,
				IsUp:     false,
				ErrorMsg: parseNetworkError(err),
			}
		}

		stats := pinger.Statistics()
		if stats.PacketsRecv == 0 {
			return Result{
				Target:   target,
				Protocol: ProtocolICMP,
				IsUp:     false,
				ErrorMsg: "packet loss 100%",
			}
		}

		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			Latency:  stats.AvgRtt,
			IsUp:     true,
		}
	}
}

func parseNetworkError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "context canceled"
	}

	// Provide cleaner messages for common net errors
	errStr := err.Error()
	if strings.Contains(errStr, "connection refused") {
		return "connection refused"
	}
	if strings.Contains(errStr, "no such host") {
		return "dns resolution failed"
	}
	if strings.Contains(errStr, "i/o timeout") {
		return "network timeout"
	}

	return errStr
}
