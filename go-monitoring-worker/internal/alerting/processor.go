package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/domain"
	"github.com/monitoring-system/go-worker/internal/fsm"
	"github.com/monitoring-system/go-worker/internal/notification"
	"github.com/monitoring-system/go-worker/internal/storage"
)

// Processor consumes ping results and passes them through the internal FSMs.
// It dictates if a Notification or a DB Persistance action is required.
type Processor struct {
	logger     *slog.Logger
	repo       storage.AlertRepository
	dispatcher *notification.Dispatcher

	// machines maps JobID to its specific State Machine instance.
	// This permits each Tenant/IP to have totally asynchronous limits.
	machines map[string]*fsm.Machine
	machMut  sync.RWMutex

	wg sync.WaitGroup
}

func NewProcessor(repo storage.AlertRepository, dispatcher *notification.Dispatcher, logger *slog.Logger) *Processor {
	return &Processor{
		logger:     logger,
		repo:       repo,
		dispatcher: dispatcher,
		machines:   make(map[string]*fsm.Machine),
	}
}

// Start spawns a background consumer for the JobResults.
func (p *Processor) Start(ctx context.Context, results <-chan domain.JobResult) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case res, ok := <-results:
				if !ok {
					// Channel explicitly closed
					return
				}
				p.Evaluate(ctx, res)
			}
		}
	}()
}

// Stop waits for the processing queue to finish.
func (p *Processor) Stop() {
	p.wg.Wait()
}

// EnsureMachine validates if a specific IP objective has a tracked Machine, creating it if absent.
func (p *Processor) EnsureMachine(jobID string, cfg fsm.AlertConfig, maxLatency time.Duration) *fsm.Machine {
	p.machMut.Lock()
	defer p.machMut.Unlock()

	machine, exists := p.machines[jobID]
	if !exists {
		machine = fsm.NewMachine(cfg, maxLatency)
		// Assuming we want to fetch the latest state from SQLite in Production to prevent Reset-On-Restart bugs
		// For now we start at Green.
		p.machines[jobID] = machine
	}
	return machine
}

// RemoveMachine wipes the state from RAM. Highly useful when Target is dynamically deleted by User.
func (p *Processor) RemoveMachine(jobID string) {
	p.machMut.Lock()
	defer p.machMut.Unlock()
	delete(p.machines, jobID)
}

// Evaluate runs the Result through the local FSM and dispatch events if constraints met.
func (p *Processor) Evaluate(ctx context.Context, res domain.JobResult) {
	p.machMut.RLock()
	machine, exists := p.machines[res.JobID]
	p.machMut.RUnlock()

	if !exists {
		// Usually a Job is configured in the Worker Pool before we receive settings,
		// but ideally Syncer should populate EnsureMachine first. We ignore metrics with no FSM.
		return
	}

	prevState := machine.Current()

	// 1. Advance the mathematical State Machine
	transition := machine.Transition(res.IsUp, res.Latency, time.Now())

	// 2. State Mutated? Persist the new status for audit logging
	if prevState != transition.NewState {
		err := p.repo.SaveEvent(ctx, res.JobID, prevState, transition.NewState)
		if err != nil && p.logger != nil {
			p.logger.Error("Failed to persist alert state history", "job_id", res.JobID, "error", err)
		}
	}

	// 3. Alarm Trigger
	if transition.FiredAlert {
		if p.logger != nil {
			p.logger.Warn("CRITICAL ALERT DISPATCHED", "target", res.Target)
		}
		p.dispatcher.Enqueue(notification.AlertPayload{
			JobID:       res.JobID,
			Target:      res.Target,
			IsRecovery:  false,
			TriggerTime: time.Now(),
			Details:     fmt.Sprintf("Target unreachable or extremely high latency observed. Error: %s", res.ErrorMsg),
			Recipient:   "admin@tenant.local", // Hardcoded for now. In real apps, sync from Control Plane.
		})
	}

	// 4. Muting Green Recovery
	if transition.FiredRecover {
		if p.logger != nil {
			p.logger.Info("RECOVERY ALERT DISPATCHED", "target", res.Target)
		}
		// FiredRecover ONLY triggers if Muted=false inside FSM and the strict `R` parameter count was satisfied.
		// Duplicate/Flapping alerts theoretically completely mitigated by `fsm.go`.
		p.dispatcher.Enqueue(notification.AlertPayload{
			JobID:       res.JobID,
			Target:      res.Target,
			IsRecovery:  true,
			TriggerTime: time.Now(),
			Details:     fmt.Sprintf("Target is stable again. Ping latency: %v", res.Latency),
			Recipient:   "admin@tenant.local",
		})
	}
}
