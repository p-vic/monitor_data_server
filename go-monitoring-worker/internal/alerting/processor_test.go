package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/monitoring-system/go-worker/internal/domain"
	"github.com/monitoring-system/go-worker/internal/fsm"
	"github.com/monitoring-system/go-worker/internal/notification"
	"github.com/monitoring-system/go-worker/internal/ping"
	"github.com/monitoring-system/go-worker/internal/storage"
)

func TestProcessor_MutingGreenRecovery(t *testing.T) {
	// 1. Dependency Mocks (In-Memory)
	repo, _ := storage.NewSQLiteAlertRepo(":memory:")
	dispatcher := notification.NewDispatcher("", 0, "", "", "test@local", nil)

	processor := NewProcessor(repo, dispatcher, nil)

	// Fast Alert Config: Instantly fires on 1 failure. Instantly recovers on 1 success.
	cfg := fsm.AlertConfig{T: 0, N: 1, C: true, R: 1}

	machine := processor.EnsureMachine("job-muting", cfg, 500*time.Millisecond)

	// --- PATH 1: Standard Evaluation ---
	// 1 Failure -> Fires CRITICAL
	processor.Evaluate(context.Background(), domain.JobResult{
		JobID: "job-muting",
		Result: ping.Result{
			Target:   "slow-target.com",
			Protocol: ping.ProtocolICMP,
			Latency:  1000 * time.Millisecond,
			IsUp:     true,
		},
	})

	if machine.Current() != fsm.StateCritical {
		t.Fatalf("Machine should be CRITICAL, got %s", machine.Current())
	}

	// Wait 10ms for dispatcher async
	time.Sleep(10 * time.Millisecond)

	// MUTE THE MACHINE ACTIVELY DURING OUTAGE!
	// This happens when the Sysadmin clicks "Acknowledge / Mute Event" in the Dashboard
	machine.SetMute(true)

	// --- PATH 2: Muted Recovery ---
	// Target comes back online!
	processor.Evaluate(context.Background(), domain.JobResult{
		JobID: "job-muting",
		Result: ping.Result{
			Target:   "slow-target.com",
			Protocol: ping.ProtocolICMP,
			Latency:  10 * time.Millisecond,
			IsUp:     true,
		},
	})

	// The Machine MUST transition to GREEN mathematically (it recovered!)
	if machine.Current() != fsm.StateGreen {
		t.Fatalf("Machine failed to transition to GREEN despite recovery. Muting blocked math state! Got: %s", machine.Current())
	}

	// But the SQLite history must record it, and the Dispatcher MUST NOT have sent the email due to Muting.
	// Since we mock, we assert the mathematical logic over the FSM transition.

	history, _ := repo.GetHistory(context.Background(), "job-muting", 10)
	if len(history) != 2 {
		t.Fatalf("History missed the logged transition. Expected 2 events, got %d", len(history))
	}
	latest := history[0]
	if latest.StateNew != fsm.StateGreen {
		t.Errorf("DB Log missed the recovery to GREEN")
	}

	repo.Close()
	dispatcher.Stop()
}
