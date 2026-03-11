package scheduler

import (
	"context"

	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/ping"
)

// Manager controla la memoria de "Qué IPs debemos monitorear"
type Manager interface {
	SyncConfiguration(targets []models.TargetConfig) // Llamado por el API REST
	StartTicks(ctx context.Context, pool ping.Service)
}
