// Package engines defines the extensibility contract for monitor check types.
// All built-in engines (ICMP, HTTP, DNS, TCP) register themselves here.
// Future heavy engines (SNMP, NetFlow) use a gRPC sidecar and wrap the
// remote call behind this same interface.
package engines

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
)

// CheckResult is the normalised outcome of any engine's probe.
// All engines produce this type regardless of the underlying protocol.
type CheckResult struct {
	TargetID  string
	Timestamp time.Time
	Status    string  // "up" | "down"
	LatencyMs float64 // float to preserve sub-millisecond latency (LAN devices)
}

// Engine is the extensibility contract every monitor type must implement.
type Engine interface {
	// Type returns the engine_type string used as the registry key ("icmp", "http", …).
	Type() string
	// Check runs a single probe. ctx carries the per-check deadline from target.Timeout.
	Check(ctx context.Context, target models.TargetConfig) CheckResult
}

// registry maps engine_type strings to Engine implementations.
// All built-in engines call Register() in their package init().
var (
	mu       sync.RWMutex
	registry = map[string]Engine{}
)

// Register adds an engine to the global registry.
// Panics on duplicate registration to catch wiring errors at startup.
func Register(e Engine) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[e.Type()]; exists {
		panic(fmt.Sprintf("engines: duplicate registration for type %q", e.Type()))
	}
	registry[e.Type()] = e
}

// Get returns the engine registered for the given type, or false if unknown.
func Get(engineType string) (Engine, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := registry[engineType]
	return e, ok
}

// Default returns the ICMP engine, which is always registered.
func Default() Engine {
	mu.RLock()
	defer mu.RUnlock()
	return registry["icmp"]
}
