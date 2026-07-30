package engines

import (
	"context"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/ping"
)

// ICMPEngine implements Engine using raw ICMP echo (requires CAP_NET_RAW).
// It wraps the existing ping.Executor so the proven ICMP implementation is
// reused without duplication.
type ICMPEngine struct {
	exec ping.Executor
}

// NewICMPEngine creates an ICMPEngine and registers it in the global registry.
func NewICMPEngine(exec ping.Executor) *ICMPEngine {
	e := &ICMPEngine{exec: exec}
	Register(e)
	return e
}

func (e *ICMPEngine) Type() string { return "icmp" }

func (e *ICMPEngine) Check(ctx context.Context, target models.TargetConfig) CheckResult {
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	res := e.exec.Execute(ctx, target.IPAddress, ping.ProtocolICMP, timeout)

	status := "up"
	if !res.IsUp {
		status = "down"
	}

	return CheckResult{
		TargetID:  target.ID,
		Timestamp: time.Now(),
		Status:    status,
		// Nanoseconds()/1e6 preserves sub-millisecond latency.
		// Milliseconds() would truncate 0.4ms → 0, triggering false "down" detection.
		LatencyMs: float64(res.Latency.Nanoseconds()) / 1e6,
	}
}
