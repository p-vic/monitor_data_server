package ping

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
)

// blockingExecutor holds every ping until release is closed, recording calls and timeouts.
type blockingExecutor struct {
	calls    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	timeouts []time.Duration
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan struct{}, 10), release: make(chan struct{})}
}

func (e *blockingExecutor) Execute(ctx context.Context, target string, protocol Protocol, timeout time.Duration) Result {
	e.calls.Add(1)
	e.mu.Lock()
	e.timeouts = append(e.timeouts, timeout)
	e.mu.Unlock()
	e.started <- struct{}{}
	<-e.release
	return Result{Target: target, Protocol: protocol, IsUp: false}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		timeout  time.Duration
		want     time.Duration
	}{
		{"timeout above interval is cut", 1 * time.Second, 10 * time.Second, 900 * time.Millisecond},
		{"timeout equal to interval is cut", 10 * time.Second, 10 * time.Second, 9 * time.Second},
		{"timeout below interval is kept", 10 * time.Second, 8 * time.Second, 8 * time.Second},
		{"long interval keeps timeout", 300 * time.Second, 10 * time.Second, 10 * time.Second},
		{"default timeout is also cut", 1 * time.Second, 0, 900 * time.Millisecond},
		{"no interval uses default", 0, 0, defaultTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := models.TargetConfig{CheckInterval: tt.interval, Timeout: tt.timeout}
			if got := effectiveTimeout(target); got != tt.want {
				t.Errorf("effectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnqueueJob_SkipsWhileInFlight(t *testing.T) {
	exec := newBlockingExecutor()
	p := NewWorkerPool(5, exec, nil, nil).(*workerPool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.StartWorkers(ctx)

	target := models.TargetConfig{ID: "t1", IPAddress: "192.0.2.1", IsActive: true,
		CheckInterval: time.Second, Timeout: 10 * time.Second}

	p.EnqueueJob(target)
	<-exec.started

	// Ticks while the first ping is still running must not start a second one.
	for i := 0; i < 3; i++ {
		p.EnqueueJob(target)
	}
	time.Sleep(50 * time.Millisecond)
	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("expected 1 ping in flight, got %d", got)
	}

	close(exec.release)
	waitUntil(t, func() bool { _, busy := p.inFlight.Load(target.ID); return !busy })

	p.EnqueueJob(target)
	waitUntil(t, func() bool { return exec.calls.Load() == 2 })

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if exec.timeouts[0] != 900*time.Millisecond {
		t.Errorf("expected executor timeout 900ms, got %v", exec.timeouts[0])
	}
}

func TestEnqueueJob_QueueFullClearsInFlight(t *testing.T) {
	// No workers and no buffer: every enqueue hits the queue-full branch.
	p := NewWorkerPool(0, newBlockingExecutor(), nil, nil).(*workerPool)
	target := models.TargetConfig{ID: "t1", IPAddress: "192.0.2.1", IsActive: true}

	p.EnqueueJob(target)

	if _, busy := p.inFlight.Load(target.ID); busy {
		t.Fatal("dropped job left the target marked in flight; it would never be pinged again")
	}
}

func TestProcess_InactiveTargetClearsInFlight(t *testing.T) {
	exec := newBlockingExecutor()
	p := NewWorkerPool(1, exec, nil, nil).(*workerPool)
	target := models.TargetConfig{ID: "t1", IPAddress: "192.0.2.1", IsActive: false}

	p.inFlight.Store(target.ID, struct{}{})
	p.process(context.Background(), target)

	if _, busy := p.inFlight.Load(target.ID); busy {
		t.Fatal("inactive target left marked in flight")
	}
	if got := exec.calls.Load(); got != 0 {
		t.Errorf("inactive target was pinged %d times", got)
	}
}
