package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/monitoring-system/go-worker/internal/config"
	"github.com/monitoring-system/go-worker/internal/models"
)

// SyncResponse maps the JSON expected from the Control Plane.
type SyncResponse struct {
	WorkerID   string          `json:"worker_id"`
	WorkerName string          `json:"worker_name"`
	TasksCount int             `json:"tasks_count"`
	Tasks      []M2MTaskConfig `json:"tasks"`
}

// M2MTaskConfig maps exactly to the FastAPI payload
type M2MTaskConfig struct {
	ID             string      `json:"id"`
	TenantID       string      `json:"tenant_id"`
	Name           string      `json:"name"`
	IPAddress      string      `json:"ip_address"`
	Group          string      `json:"group"`
	ParentID       *string     `json:"parent_id"` // nullable UUID from control plane
	CheckInterval  int         `json:"check_interval"` // Seconds
	Timeout        int         `json:"timeout"`        // Seconds
	WarningLatency float64     `json:"warning_latency"`
	MaxLatency     float64     `json:"max_latency"`
	AlertEmails         []string    `json:"alert_emails"`
	AlertConfig         interface{} `json:"alert_config"`
	NotificationChannel string      `json:"notification_channel"`
	TelegramChatID      string      `json:"telegram_chat_id"`
	IsActive            bool        `json:"is_active"`
}

// ControlPlaneClient defines the interface for M2M communication.
type ControlPlaneClient interface {
	FetchTargets(ctx context.Context) ([]models.TargetConfig, error)
}

// M2MClient implements ControlPlaneClient securely attaching HMAC signatures.
type M2MClient struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewM2MClient instantiates the API client with custom timeouts.
func NewM2MClient(cfg *config.Config) ControlPlaneClient {
	return &M2MClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second, // Hard timeout to prevent slow-loris attacks
		},
	}
}

// FetchTargets requests the current set of jobs assigned to this worker.
func (c *M2MClient) FetchTargets(ctx context.Context) ([]models.TargetConfig, error) {
	endpoint := fmt.Sprintf("%s/api/v1/workers/sync", c.cfg.ControlPlaneURL)

	payload := []byte(`{}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 1. Generate Anti-Replay Timestamp
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// 2. Cryptographic Signature (HMAC-SHA256)
	mac := hmac.New(sha256.New, []byte(c.cfg.HMACSecret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	signature := hex.EncodeToString(mac.Sum(nil))

	// 3. Attach standard and security headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Worker-ID", c.cfg.WorkerID)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Signature", signature)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		limitReader := io.LimitReader(resp.Body, 1024)
		bodyBytes, _ := io.ReadAll(limitReader)
		return nil, fmt.Errorf("control plane returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	limitReader := io.LimitReader(resp.Body, 5*1024*1024)
	bodyBytes, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var syncResp SyncResponse
	if err := json.Unmarshal(bodyBytes, &syncResp); err != nil {
		return nil, fmt.Errorf("failed to parse sync response: %w", err)
	}

	// Transform to internal domain model
	var targets []models.TargetConfig
	for _, t := range syncResp.Tasks {

		alertConfigJSON := ""
		if t.AlertConfig != nil {
			bytes, _ := json.Marshal(t.AlertConfig)
			alertConfigJSON = string(bytes)
		}

		parentID := ""
		if t.ParentID != nil {
			parentID = *t.ParentID
		}

		targets = append(targets, models.TargetConfig{
			ID:                  t.ID,
			TenantID:            t.TenantID,
			Name:                t.Name,
			IPAddress:           t.IPAddress,
			Group:               t.Group,
			ParentID:            parentID,
			CheckInterval:       time.Duration(t.CheckInterval) * time.Second,
			Timeout:             time.Duration(t.Timeout) * time.Second,
			WarningLatency:      t.WarningLatency,
			MaxLatency:          t.MaxLatency,
			AlertEmails:         t.AlertEmails,
			AlertConfig:         alertConfigJSON,
			NotificationChannel: t.NotificationChannel,
			TelegramChatID:      t.TelegramChatID,
			IsActive:            t.IsActive,
		})
	}

	return targets, nil
}
