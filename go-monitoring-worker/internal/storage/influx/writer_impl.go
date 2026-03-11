package influx

import (
	"context"
	"log"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

type influxWriterImpl struct {
	client   influxdb2.Client
	writeAPI api.WriteAPI
}

// Config wraps TSDB variables
type Config struct {
	URL       string
	Token     string
	Org       string
	Bucket    string
	BatchSize uint
}

func NewInfluxWriter(cfg Config) Writer {
	options := influxdb2.DefaultOptions()

	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100 // Batch thresholds for optimization
	}
	options.SetBatchSize(cfg.BatchSize)
	options.SetFlushInterval(5000) // 5s absolute fallback flush
	options.SetPrecision(time.Millisecond)

	// Exponential backoff retries mitigate network partitions
	options.SetMaxRetries(3)
	options.SetRetryInterval(2000)

	client := influxdb2.NewClientWithOptions(cfg.URL, cfg.Token, options)
	writeAPI := client.WriteAPI(cfg.Org, cfg.Bucket)

	// A consuming routine is strictly required to avert channels memory leaks in WriteAPI
	go func() {
		for err := range writeAPI.Errors() {
			log.Printf("WARN: InfluxDB Async Write Error: %v", err)
		}
	}()

	return &influxWriterImpl{
		client:   client,
		writeAPI: writeAPI,
	}
}

func (w *influxWriterImpl) WriteMetrics(ctx context.Context, metrics []MetricContext) error {
	for _, m := range metrics {
		p := write.NewPoint(
			"pings",
			map[string]string{
				"target_id": m.Target.ID,
				"tenant_id": m.Target.TenantID,
				"ip":        m.Target.IPAddress,
				"group":     m.Target.Group,
				"status":    m.StatusStr,
			},
			map[string]interface{}{
				"latency_ms": m.Latency,
			},
			m.Timestamp,
		)
		// Internal queue handles async non-blocking operations natively
		w.writeAPI.WritePoint(p)
	}
	return nil
}

func (w *influxWriterImpl) WriteAlertEvent(ctx context.Context, alert AlertEvent) error {
	p := write.NewPoint(
		"alerts",
		map[string]string{
			"target_id": alert.TargetID,
			"ip":        alert.TargetIP,
			"type":      alert.Type,
		},
		map[string]interface{}{
			"message":    alert.Message,
			"latency_ms": alert.LatencyMs,
		},
		alert.Timestamp,
	)
	w.writeAPI.WritePoint(p)
	return nil
}

// Close ensures safe teardown and synchronization of pending writes
func (w *influxWriterImpl) Close() {
	w.writeAPI.Flush()
	w.client.Close()
}
