package scheduler

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/ping"
)

type dynamicManager struct {
	mu            sync.RWMutex
	activeTargets map[string]models.TargetConfig
	cancelFuncs   map[string]context.CancelFunc
	pool          ping.Service
	rootCtx       context.Context
}

func NewManager() Manager {
	// Initialize random seed for Jitter
	rand.Seed(time.Now().UnixNano())

	return &dynamicManager{
		activeTargets: make(map[string]models.TargetConfig),
		cancelFuncs:   make(map[string]context.CancelFunc),
	}
}

func (m *dynamicManager) StartTicks(ctx context.Context, pool ping.Service) {
	m.mu.Lock()
	m.rootCtx = ctx
	m.pool = pool
	m.mu.Unlock()

	log.Println("Dynamic Scheduler started. Waiting for REST Sync from Control Plane...")
	<-ctx.Done()
	log.Println("Dynamic Scheduler shutting down...")
}

func (m *dynamicManager) SyncConfiguration(targets []models.TargetConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.rootCtx == nil || m.rootCtx.Err() != nil {
		log.Println("SyncConfiguration called but scheduler is not running yet.")
		return
	}

	incomingMap := make(map[string]models.TargetConfig)
	for _, t := range targets {
		if t.IsActive {
			incomingMap[t.ID] = t
		}
	}

	// 1. Detect targets to Remove or Update
	for id, activeConfig := range m.activeTargets {
		incomingConfig, exists := incomingMap[id]
		if !exists || incomingConfig != activeConfig {
			// Stop current tick loop
			if cancel, hasCancel := m.cancelFuncs[id]; hasCancel {
				cancel()
				delete(m.cancelFuncs, id)
			}
			delete(m.activeTargets, id)
		}
	}

	// 2. Add New or Updated targets
	addedCount := 0
	for id, incomingConfig := range incomingMap {
		if _, exists := m.activeTargets[id]; !exists {
			// Start new loop
			targetCtx, cancel := context.WithCancel(m.rootCtx)
			m.cancelFuncs[id] = cancel
			m.activeTargets[id] = incomingConfig

			go m.runTargetLoop(targetCtx, incomingConfig)
			addedCount++
		}
	}

	log.Printf("Scheduler Sync complete. Active Targets: %d (Started %d this cycle)\n", len(m.activeTargets), addedCount)
}

func (m *dynamicManager) runTargetLoop(ctx context.Context, target models.TargetConfig) {
	interval := target.CheckInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Jitter: Prevent thunderous herd by delaying the initial hit.
	// We wait up to 30% of the interval before starting the actual periodic ticker.
	maxJitterMs := int(float64(interval.Milliseconds()) * 0.3)
	if maxJitterMs > 0 {
		jitter := rand.Intn(maxJitterMs)

		// Wait Jitter time before entering the loop
		select {
		case <-time.After(time.Duration(jitter) * time.Millisecond):
		case <-ctx.Done():
			return // Cancelled during jitter
		}
	}

	// Ticker starts now
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial immediate ping after jitter
	m.pool.EnqueueJob(target)

	for {
		select {
		case <-ctx.Done():
			// The Context was cancelled (Target removed or updated, or system shutdown)
			return
		case <-ticker.C:
			// Time to ping again
			m.pool.EnqueueJob(target)
		}
	}
}
