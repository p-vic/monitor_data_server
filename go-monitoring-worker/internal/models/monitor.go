package models

import "time"

// Status unificado en todo el sistema
type Status string

const (
	StatusGreen  Status = "green"
	StatusYellow Status = "warning"
	StatusRed    Status = "critical"
	StatusGray   Status = "down"
)

type TargetConfig struct {
	ID             string
	TenantID       string
	Name           string
	IPAddress      string
	Group          string
	CheckInterval  time.Duration
	Timeout        time.Duration
	WarningLatency float64
	MaxLatency     float64
	AlertEmails    []string
	AlertType      string // "every_n_alerts", etc.
	AlertConfig    string // JSON `{"t": 60, "n": 5, "c": true, "r": 3}`
	IsActive       bool
}

type PingResult struct {
	TargetID  string
	LatencyMs float64
	IsDown    bool
	Timestamp time.Time
}
