package influx

import (
	"context"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
)

// Writer acopla el batching hacia InfluxDB
type Writer interface {
	WriteMetrics(ctx context.Context, metrics []MetricContext) error
	WriteAlertEvent(ctx context.Context, alert AlertEvent) error
}

type MetricContext struct {
	Target    models.TargetConfig
	Latency   float64
	StatusStr string
	Timestamp time.Time
}

type AlertEvent struct {
	TargetID  string
	TargetIP  string
	Type      string // "WARNING", "CRITICAL", "RECOVERY"
	Message   string
	LatencyMs float64
	Timestamp time.Time
}
