package ping

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
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
	// Resolve hostname to IPv4 address.
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: "dns resolution failed",
		}
	}
	var dst *net.IPAddr
	for _, addr := range addrs {
		if addr.IP.To4() != nil {
			dst = &addr
			break
		}
	}
	if dst == nil {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: "no ipv4 address found",
		}
	}

	// Raw ICMP socket — each call gets its own socket and a random ID,
	// so concurrent goroutines never mix up each other's echo replies.
	// Requires CAP_NET_RAW (set in docker-compose via cap_add: NET_RAW).
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: fmt.Sprintf("icmp socket: %v", err),
		}
	}
	defer conn.Close()

	id := rand.Intn(0xfffe) + 1 // 1–65534, never 0
	seq := 1

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("ips")},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: fmt.Sprintf("icmp marshal: %v", err),
		}
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	_ = conn.SetDeadline(deadline)

	start := time.Now()
	if _, err := conn.WriteTo(wb, dst); err != nil {
		return Result{
			Target:   target,
			Protocol: ProtocolICMP,
			IsUp:     false,
			ErrorMsg: parseNetworkError(err),
		}
	}

	rb := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return Result{
				Target:   target,
				Protocol: ProtocolICMP,
				IsUp:     false,
				ErrorMsg: parseNetworkError(ctx.Err()),
			}
		}

		n, _, err := conn.ReadFrom(rb)
		if err != nil {
			return Result{
				Target:   target,
				Protocol: ProtocolICMP,
				IsUp:     false,
				ErrorMsg: parseNetworkError(err),
			}
		}

		rm, err := icmp.ParseMessage(1, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		if ok && echo.ID == id && echo.Seq == seq {
			return Result{
				Target:   target,
				Protocol: ProtocolICMP,
				Latency:  time.Since(start),
				IsUp:     true,
			}
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
