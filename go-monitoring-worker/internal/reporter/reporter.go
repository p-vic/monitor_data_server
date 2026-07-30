// Package reporter ships batched check results to the Control Plane over HTTPS.
// This replaces the direct InfluxDB write used by the legacy Docker worker.
// The Control Plane receives the results and writes them to its central InfluxDB,
// eliminating InfluxDB as a customer-side dependency.
package reporter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/engines"
)

// resultPayload mirrors the JSON schema expected by POST /api/v1/agents/{id}/telemetry.
type resultPayload struct {
	TargetID  string         `json:"target_id"`
	Ts        int64          `json:"ts"`
	Status    string         `json:"status"`
	LatencyMs float64        `json:"latency_ms"`
	Meta      map[string]any `json:"meta"`
}

// Reporter buffers CheckResults from the engine pool and flushes them to
// the Control Plane on a regular interval. Thread-safe.
type Reporter struct {
	agentID    string
	hmacSecret string
	cpURL      string
	httpClient *http.Client

	mu         sync.Mutex
	buffer     []resultPayload
	flushEvery time.Duration
}

// New creates a Reporter. flushEvery defaults to 5s if zero.
func New(agentID, hmacSecret, cpURL string, flushEvery time.Duration) *Reporter {
	if flushEvery <= 0 {
		flushEvery = 5 * time.Second
	}
	return &Reporter{
		agentID:    agentID,
		hmacSecret: hmacSecret,
		cpURL:      cpURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		flushEvery: flushEvery,
	}
}

// Add enqueues a single CheckResult. Called from worker goroutines; non-blocking.
func (r *Reporter) Add(result engines.CheckResult) {
	r.mu.Lock()
	r.buffer = append(r.buffer, resultPayload{
		TargetID:  result.TargetID,
		Ts:        result.Timestamp.Unix(),
		Status:    result.Status,
		LatencyMs: result.LatencyMs,
		Meta:      map[string]any{},
	})
	r.mu.Unlock()
}

// Run starts the flush loop. Blocks until ctx is cancelled, then performs
// one final flush to drain any buffered results before shutdown.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.flushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush — use background context so network I/O isn't cancelled.
			if err := r.flush(context.Background()); err != nil {
				log.Printf("WARN reporter: final flush failed: %v", err)
			}
			return
		case <-ticker.C:
			if err := r.flush(ctx); err != nil {
				log.Printf("WARN reporter: flush failed: %v", err)
			}
		}
	}
}

// flush drains the buffer and sends one POST to the Control Plane.
func (r *Reporter) flush(ctx context.Context) error {
	r.mu.Lock()
	if len(r.buffer) == 0 {
		r.mu.Unlock()
		return nil
	}
	batch := r.buffer
	r.buffer = nil
	r.mu.Unlock()

	payload, err := json.Marshal(map[string]any{"results": batch})
	if err != nil {
		return fmt.Errorf("reporter marshal: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/v1/agents/%s/telemetry", r.cpURL, r.agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("reporter new request: %w", err)
	}

	// HMAC-SHA256 signing — same scheme as the legacy M2M worker (timestamp.body).
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(r.hmacSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", r.agentID)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Signature", sig)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("reporter http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("reporter: CP returned %d", resp.StatusCode)
	}

	log.Printf("reporter: flushed %d results to CP", len(batch))
	return nil
}

// Heartbeat sends a lightweight ping to the Control Plane to update last_seen_at.
func (r *Reporter) Heartbeat(ctx context.Context) error {
	payload := []byte(`{}`)
	endpoint := fmt.Sprintf("%s/api/v1/agents/%s/heartbeat", r.cpURL, r.agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(r.hmacSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", r.agentID)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Signature", sig)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
