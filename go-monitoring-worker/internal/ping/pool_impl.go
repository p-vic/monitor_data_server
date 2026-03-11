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

func (p *workerPool) EnqueueJob(target models.TargetConfig) {
	select {
	case p.jobQueue <- target:
		// Job accepted
	default:
		// Queue is full. We drop this interval tick rather than blocking the scheduler.
		// Dropping metrics under extreme stress implies graceful degradation.
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
			if !target.IsActive {
				continue
			}

			// Sub-Context handled gracefully by the Ping Executor avoiding resource locking
			res := p.pingExec.Execute(ctx, target.IPAddress, ProtocolICMP, target.Timeout)

			pingRes := models.PingResult{
				TargetID:  target.ID,
				LatencyMs: float64(res.Latency.Milliseconds()),
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
	}
}

func mapStatus(r models.PingResult) string {
	if r.IsDown {
		return "down"
	}
	return "up"
}
