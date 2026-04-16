package ping

import (
	"context"

	"github.com/monitoring-system/go-worker/internal/models"
)

// Service es la fachada del worker pool
type Service interface {
	StartWorkers(ctx context.Context)
	EnqueueJob(target models.TargetConfig)
	ResetTarget(targetID string)
}
