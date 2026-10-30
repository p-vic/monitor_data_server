package ping

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/alerts"
	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/storage/influx"
)

type workerPool struct {
	maxWorkers int
	pingExec   Executor
	engine     alerts.Engine
	writer     influx.Writer

	jobQueue chan models.TargetConfig
	wg       sync.WaitGroup

	// inFlight holds the IDs of targets with a ping queued or running.
	inFlight sync.Map
}

// NewWorkerPool instantiates a high-concurrency ping pool.
func NewWorkerPool(maxWorkers int, exec Executor, engine alerts.Engine, writer influx.Writer) Service {
	return &workerPool{
		maxWorkers: maxWorkers,
		pingExec:   exec,
		engine:     engine,
		writer:     writer,
		// Buffer length acts as a shock-absorber. Combined with Scheduler Jitter,
		// it effectively prevents deadlock and spikes.
		jobQueue: make(chan models.TargetConfig, maxWorkers*3),
	}
}

func (p *workerPool) ResetTarget(targetID string) {
	if p.engine != nil {
		p.engine.ResetTarget(targetID)
	}
}

func (p *workerPool) EnqueueJob(target models.TargetConfig) {
	// One ping per target at a time: a tick that arrives while the previous ping is still
	// queued or running is skipped, so a down target can never hold more than one worker.
	if _, busy := p.inFlight.LoadOrStore(target.ID, struct{}{}); busy {
		return
	}

	select {
	case p.jobQueue <- target:
		// Job accepted
	default:
		// Queue is full. We drop this interval tick rather than blocking the scheduler.
		// Dropping metrics under extreme stress implies graceful degradation.
		p.inFlight.Delete(target.ID)
		log.Printf("WARN: Worker Pool Queue full. Dropping explicit check for %s", target.IPAddress)
	}
}

func (p *workerPool) StartWorkers(ctx context.Context) {
	for i := 0; i < p.maxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}

	<-ctx.Done()
	log.Println("Stopping ping worker pool...")

	// Ensures all in-flight pings settle before exiting the Data Plane
	p.wg.Wait()
	log.Println("Ping worker pool gracefully shut down.")
}

func (p *workerPool) worker(ctx context.Context) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done(): // Global context cancellation breaks loops immediately
			return
		case target := <-p.jobQueue:
			p.process(ctx, target)
		}
	}
}

func (p *workerPool) process(ctx context.Context, target models.TargetConfig) {
	defer p.inFlight.Delete(target.ID)

	if !target.IsActive {
		return
	}

	// Sub-Context handled gracefully by the Ping Executor avoiding resource locking
	res := p.pingExec.Execute(ctx, target.IPAddress, ProtocolICMP, effectiveTimeout(target))

	pingRes := models.PingResult{
		TargetID:  target.ID,
		LatencyMs: float64(res.Latency.Nanoseconds()) / 1e6,
		IsDown:    !res.IsUp,
		Timestamp: time.Now(),
	}

	// 1. Process via Finite State Machine (Alerts)
	if p.engine != nil {
		p.engine.ProcessResult(target, pingRes)
	}

	// 2. Transmit standard Pings to Time-Series DB
	if p.writer != nil {
		metric := influx.MetricContext{
			Target:    target,
			Latency:   pingRes.LatencyMs,
			StatusStr: mapStatus(pingRes),
			Timestamp: pingRes.Timestamp,
		}
		// Writer uses internally decoupled WriteAPI (Non-Blocking background queue)
		_ = p.writer.WriteMetrics(ctx, []influx.MetricContext{metric})
	}
}

// effectiveTimeout keeps a ping from outliving its own check interval. A timeout at or
// above the interval (e.g. 10s timeout on a 1s target) is cut to 90% of the interval,
// so a down target finishes before its next tick instead of skipping it.
func effectiveTimeout(target models.TargetConfig) time.Duration {
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if target.CheckInterval > 0 && timeout >= target.CheckInterval {
		timeout = target.CheckInterval * 9 / 10
	}
	return timeout
}

func mapStatus(r models.PingResult) string {
	if r.IsDown {
		return "down"
	}
	return "up"
}
