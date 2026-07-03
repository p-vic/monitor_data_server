package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
	"github.com/monitoring-system/go-worker/internal/storage/influx"
)

type AlertConfigParsed struct {
	T  int  `json:"t"`  // Time window in seconds
	N  int  `json:"n"`  // Number of alerts
	C  bool `json:"c"`  // Consecutive?
	R  int  `json:"r"`  // Recovery consecutive pings needed
	RI int  `json:"ri"` // Reminder interval in minutes (0 = disabled)
}

type targetState struct {
	isAlarmed          bool
	consecutiveErrors  int
	firstErrorTime     time.Time
	windowErrors       int
	consecutiveSuccess int
	currentStreak      int
	maxStreakInWin     int
	lastReminderTime   time.Time
}

type fsmEngine struct {
	states       map[string]*targetState
	mu           sync.RWMutex
	notifier     Notifier
	influxWriter influx.Writer
}

func NewEngine(n Notifier, w influx.Writer) Engine {
	return &fsmEngine{
		states:       make(map[string]*targetState),
		notifier:     n,
		influxWriter: w,
	}
}

func (e *fsmEngine) ProcessResult(target models.TargetConfig, result models.PingResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	state, exists := e.states[target.ID]
	if !exists {
		state = &targetState{}
		e.states[target.ID] = state
	}

	// Parsing config
	cfg := AlertConfigParsed{T: 120, N: 5, C: true, R: 7} // Defaults
	if target.AlertConfig != "" {
		_ = json.Unmarshal([]byte(target.AlertConfig), &cfg)
	}

	isCritical := result.IsDown || result.LatencyMs > target.MaxLatency
	isWarning := !isCritical && result.LatencyMs > target.WarningLatency

	if isCritical {
		e.handleCritical(state, target, result, cfg)
	} else {
		// If it's pure Green or Warning, Warning does not trigger Alarm, but it resets C?
		// "Alerta critical ... Si la alerta es warning no se realizará notificación"
		// Any non-critical (Green or Warning) will increment recovery if Alarmed, or reset consecutive criticals.
		if isWarning {
			// Write to Influx as Warning, but it breaks consecutive critical chain.
			e.fireEvent(target, result, "WARNING", "Latency exceeded warning threshold")
		}
		e.handleSuccessOrWarning(state, target, result, cfg, isWarning)
	}
}

func (e *fsmEngine) handleCritical(state *targetState, target models.TargetConfig, result models.PingResult, cfg AlertConfigParsed) {
	state.consecutiveSuccess = 0 // Break recovery chain

	if state.windowErrors == 0 {
		state.firstErrorTime = result.Timestamp
	}

	state.windowErrors++
	state.currentStreak++
	if state.currentStreak > state.maxStreakInWin {
		state.maxStreakInWin = state.currentStreak
	}

	// Logging Influx
	if !state.isAlarmed {
		e.fireEvent(target, result, "CRITICAL", "Critical ping detected")
	} else {
		e.fireEvent(target, result, "CRITICAL", "Critical ping ongoing")
	}

	shouldAlarm := false
	elapsed := result.Timestamp.Sub(state.firstErrorTime).Seconds()

	conditionMet := false
	if cfg.C {
		conditionMet = state.maxStreakInWin >= cfg.N
	} else {
		conditionMet = state.windowErrors >= cfg.N
	}

	if cfg.T == 0 {
		// Immediate Mode
		if conditionMet {
			shouldAlarm = true
		}
	} else {
		// Delayed Mode
		if elapsed >= float64(cfg.T) {
			if conditionMet {
				shouldAlarm = true
			} else {
				// Window expired and condition wasn't met. Reset ALL window state
				// BUT we count the current ping to start the new window!
				state.firstErrorTime = result.Timestamp
				state.windowErrors = 1
				state.currentStreak = 1
				state.maxStreakInWin = 1
			}
		}
	}

	if shouldAlarm && !state.isAlarmed {
		state.isAlarmed = true
		state.lastReminderTime = result.Timestamp

		// This generates a duplicate visual log but signals the threshold breach
		e.fireEvent(target, result, "CRITICAL", "Threshold breached")

		if e.notifier != nil {
			var latencyStr string
			if result.IsDown {
				latencyStr = "DOWN (Timeout/Unreachable)"
			} else {
				latencyStr = fmt.Sprintf("%.2f ms", result.LatencyMs)
			}

			channel := target.NotificationChannel
			if channel == "" {
				channel = "email"
			}

			if (channel == "email" || channel == "both") && len(target.AlertEmails) > 0 {
				body := fmt.Sprintf(`Critical Alert triggered for %s (%s).

=== Last Ping Status ===
- Status: %s
- Max Latency Allowed: %.2f ms
- Warning Latency: %.2f ms

=== Critical Alert Rules (Triggered) ===
- Time Window (t): %d seconds
- Fault Count (n): %d pings
- Must be Consecutive? (c): %v
- Recovery Pings Needed (r): %d

Please check the Telemetry Dashboard for visual correlation.`,
					target.Name, target.IPAddress, latencyStr, target.MaxLatency, target.WarningLatency,
					cfg.T, cfg.N, cfg.C, cfg.R)

				for _, email := range target.AlertEmails {
					_ = e.notifier.SendEmail(email, "CRITICAL ALERT: "+target.Name, body)
				}
			}

			if (channel == "telegram" || channel == "both") && target.TelegramChatID != "" {
				tgMsg := fmt.Sprintf("🚨 CRITICAL ALERT: %s\nHost: %s\nStatus: %s\nMax Latency: %.0f ms\nWindow: %ds / %d faults",
					target.Name, target.IPAddress, latencyStr, target.MaxLatency, cfg.T, cfg.N)
				_ = e.notifier.SendTelegram(target.TelegramChatID, tgMsg)
			}
		}
	}

	// Reminder notification while still in CRITICAL state
	if state.isAlarmed && cfg.RI > 0 && e.notifier != nil {
		reminderInterval := time.Duration(cfg.RI) * time.Minute
		if result.Timestamp.Sub(state.lastReminderTime) >= reminderInterval {
			state.lastReminderTime = result.Timestamp

			var latencyStr string
			if result.IsDown {
				latencyStr = "DOWN (Timeout/Unreachable)"
			} else {
				latencyStr = fmt.Sprintf("%.2f ms", result.LatencyMs)
			}

			channel := target.NotificationChannel
			if channel == "" {
				channel = "email"
			}

			if (channel == "email" || channel == "both") && len(target.AlertEmails) > 0 {
				body := fmt.Sprintf(`Reminder: %s (%s) is still DOWN.

=== Current Status ===
- Status: %s
- Max Latency Allowed: %.2f ms

This is an automated reminder. You will keep receiving these every %d minute(s) until the service recovers.`,
					target.Name, target.IPAddress, latencyStr, target.MaxLatency, cfg.RI)

				for _, email := range target.AlertEmails {
					_ = e.notifier.SendEmail(email, "REMINDER - Still DOWN: "+target.Name, body)
				}
			}

			if (channel == "telegram" || channel == "both") && target.TelegramChatID != "" {
				tgMsg := fmt.Sprintf("🔔 REMINDER - Still DOWN: %s\nHost: %s\nStatus: %s\nEvery %d min until recovery.",
					target.Name, target.IPAddress, latencyStr, cfg.RI)
				_ = e.notifier.SendTelegram(target.TelegramChatID, tgMsg)
			}
		}
	}
}

func (e *fsmEngine) handleSuccessOrWarning(state *targetState, target models.TargetConfig, result models.PingResult, cfg AlertConfigParsed, isWarning bool) {
	// A green/warning ping instantly ruptures the "Consecutive Errors Streak"
	// but it does not reset the actual count of Window Errors (if C=False)
	state.currentStreak = 0

	// If Warning, it breaks consecutive Greens. So Recovery only steps if isWarning == false (Pure Green)
	if !isWarning {
		if state.isAlarmed {
			state.consecutiveSuccess++
			if state.consecutiveSuccess >= cfg.R {
				// Recovered
				state.isAlarmed = false
				state.windowErrors = 0
				state.currentStreak = 0
				state.maxStreakInWin = 0
				state.consecutiveSuccess = 0
				state.firstErrorTime = time.Time{}

				e.fireEvent(target, result, "RECOVERY", "Service is back online")

				if e.notifier != nil {
					channel := target.NotificationChannel
					if channel == "" {
						channel = "email"
					}

					if (channel == "email" || channel == "both") && len(target.AlertEmails) > 0 {
						body := fmt.Sprintf(`Service %s (%s) has successfully recovered.

=== Ping Status ===
- Current Latency: %.2f ms
- Max Latency Bound: %.2f ms

=== Recovery Criteria Satisfied ===
- Stable Pings Count (r): %d consecutive clean pings confirmed.
- FSM State restored to: StatusGreen (Normal)

Visual dashboards will now drop the DOWN markers.`,
							target.Name, target.IPAddress, result.LatencyMs, target.MaxLatency, cfg.R)

						for _, email := range target.AlertEmails {
							_ = e.notifier.SendEmail(email, "RECOVERY: "+target.Name, body)
						}
					}

					if (channel == "telegram" || channel == "both") && target.TelegramChatID != "" {
						tgMsg := fmt.Sprintf("✅ RECOVERY: %s\nHost: %s\nLatency: %.2f ms\nService is back online.",
							target.Name, target.IPAddress, result.LatencyMs)
						_ = e.notifier.SendTelegram(target.TelegramChatID, tgMsg)
					}
				}
			} else {
				// Ongoing recovery attempt
				e.fireEvent(target, result, "RECOVERY", "Attempting recovery ping")
			}
		}
	} else {
		// If it's a Warning, it resets consecutive success because it implies it's not totally healthy yet
		state.consecutiveSuccess = 0
	}
}

func (e *fsmEngine) ResetTarget(targetID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.states, targetID)
}

func (e *fsmEngine) fireEvent(target models.TargetConfig, p models.PingResult, evType string, msg string) {
	if e.influxWriter != nil {
		alert := influx.AlertEvent{
			TargetID:  target.ID,
			TargetIP:  target.IPAddress,
			Type:      evType,
			Message:   msg,
			LatencyMs: p.LatencyMs,
			Timestamp: p.Timestamp,
		}
		// Goroutine to not block FSM
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := e.influxWriter.WriteAlertEvent(ctx, alert); err != nil {
				log.Printf("Failed to write alert to influx: %v\n", err)
			}
		}()
	}
}
