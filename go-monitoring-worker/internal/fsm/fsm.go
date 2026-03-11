package fsm

import (
	"sync"
	"time"
)

// AlertConfig defines the dynamic rules for triggering notifications.
type AlertConfig struct {
	T int  `json:"t"` // Wait time in seconds after the first alert BEFORE evaluating the rule
	N int  `json:"n"` // Minimum alerts (failures) required in the T period
	C bool `json:"c"` // True = consecutive failures only; False = cumulative failures
	R int  `json:"r"` // Consecutive successful pings below max_latency required to recover to GREEN
}

const (
	StateGreen    = "GREEN"
	StateCritical = "CRITICAL"
)

// Machine tracks the state of a single monitoring target based on its AlertConfig.
// It is fully thread-safe (Anti-Race Conditions).
type Machine struct {
	config     AlertConfig
	maxLatency time.Duration

	State   string
	isMuted bool

	// Alarm Tracking (transition to CRITICAL)
	windowStartTime time.Time
	failuresCount   int
	currentStreak   int
	maxStreakInWin  int

	// Recovery Tracking (transition to GREEN)
	recoveryStreak int

	mut sync.RWMutex
}

// NewMachine initializes the Engine with a specific configuration.
func NewMachine(cfg AlertConfig, maxLatency time.Duration) *Machine {
	// Provide sensible defaults if missing
	if cfg.N <= 0 {
		cfg.N = 1
	}
	if cfg.R <= 0 {
		cfg.R = 1
	}
	return &Machine{
		config:     cfg,
		maxLatency: maxLatency,
		State:      StateGreen,
	}
}

// TransitionResult is the DTO result of evaluating a new metric.
type TransitionResult struct {
	FiredAlert   bool
	FiredRecover bool
	NewState     string
}

// SetMute toggles mute mode, which silences output alerts without stopping the state transitions.
func (m *Machine) SetMute(muted bool) {
	m.mut.Lock()
	m.isMuted = muted
	m.mut.Unlock()
}

// Transition evaluates a new health check and mutates the state if thresholds are met.
// The `now` parameter allows simulating time in tests or passing the exact ping time.
func (m *Machine) Transition(isUp bool, latency time.Duration, now time.Time) TransitionResult {
	m.mut.Lock()
	defer m.mut.Unlock()

	res := TransitionResult{NewState: m.State}

	// 1. Evaluate if it's considered a failure
	isFailure := !isUp
	if isUp && m.maxLatency > 0 && latency > m.maxLatency {
		isFailure = true
	}

	if m.State == StateGreen {
		// --- ALARM EVALUATION SCHEME ---
		if isFailure {
			if m.windowStartTime.IsZero() {
				m.windowStartTime = now
			}
			m.failuresCount++
			m.currentStreak++
			if m.currentStreak > m.maxStreakInWin {
				m.maxStreakInWin = m.currentStreak
			}
		} else {
			m.currentStreak = 0
		}

		if !m.windowStartTime.IsZero() {
			elapsed := now.Sub(m.windowStartTime).Seconds()

			conditionMet := false
			if m.config.C {
				// Condition met if the max consecutive streak in the window >= N
				conditionMet = m.maxStreakInWin >= m.config.N
			} else {
				// Condition met if the total discrete failures >= N
				conditionMet = m.failuresCount >= m.config.N
			}

			if m.config.T == 0 {
				// Immediate evaluation mode
				if conditionMet {
					m.fireCritical(&res)
				}
			} else {
				// Delayed evaluation mode
				if elapsed >= float64(m.config.T) {
					if conditionMet {
						m.fireCritical(&res)
					} else {
						// The period ended but condition wasn't met. Reset tracking!
						m.resetFailureTracking()
						// Special case: if the CURRENT ping itself is a failure, it seeds the NEXT window
						if isFailure {
							m.windowStartTime = now
							m.failuresCount = 1
							m.currentStreak = 1
							m.maxStreakInWin = 1
						}
					}
				}
			}
		}
	} else if m.State == StateCritical {
		// --- RECOVERY EVALUATION SCHEME ---
		if !isFailure {
			m.recoveryStreak++
			if m.recoveryStreak >= m.config.R {
				m.fireRecovery(&res)
			}
		} else {
			// A single failure (or ping above max_latency) breaks the recovery chain instantly
			m.recoveryStreak = 0
		}
	}

	return res
}

func (m *Machine) fireCritical(res *TransitionResult) {
	m.State = StateCritical
	res.NewState = StateCritical
	if !m.isMuted {
		res.FiredAlert = true
	}
	m.resetFailureTracking()
}

func (m *Machine) fireRecovery(res *TransitionResult) {
	m.State = StateGreen
	res.NewState = StateGreen
	if !m.isMuted {
		res.FiredRecover = true
	}
	m.recoveryStreak = 0
	m.resetFailureTracking()
}

func (m *Machine) resetFailureTracking() {
	m.windowStartTime = time.Time{}
	m.failuresCount = 0
	m.currentStreak = 0
	m.maxStreakInWin = 0
}

// Current returns a thread-safe read of the State.
func (m *Machine) Current() string {
	m.mut.RLock()
	defer m.mut.RUnlock()
	return m.State
}

// IsMuted returns the current mute configuration safely.
func (m *Machine) IsMuted() bool {
	m.mut.RLock()
	defer m.mut.RUnlock()
	return m.isMuted
}
