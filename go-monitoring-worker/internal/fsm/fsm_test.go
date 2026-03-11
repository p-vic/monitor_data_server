package fsm

import (
	"testing"
	"time"
)

func TestFSMMachine_Example1(t *testing.T) {
	// Example 1: `{"t": 120, "n": 5, "c": true, "r": 7}`
	cfg := AlertConfig{T: 120, N: 5, C: true, R: 7}
	m := NewMachine(cfg, 500*time.Millisecond)

	now := time.Now()

	// 1. First failure triggers the window start
	m.Transition(false, 0, now)
	if m.Current() != StateGreen {
		t.Errorf("Should remain GREEN, still evaluating. Got %s", m.Current())
	}

	// 2. 4 more consecutive failures happen, arriving at 5 consecutive.
	m.Transition(false, 0, now.Add(10*time.Second))
	m.Transition(false, 0, now.Add(20*time.Second))
	m.Transition(false, 0, now.Add(30*time.Second))
	res := m.Transition(false, 0, now.Add(40*time.Second))

	if res.FiredAlert {
		t.Errorf("Should NOT fire alert yet. Window T=120 has not elapsed!")
	}

	// 3. Time passes, window ends. Check at T=120
	// We need another ping to trigger evaluation (could be healthy or not, but condition was met)
	res = m.Transition(true, 10*time.Millisecond, now.Add(120*time.Second))
	if !res.FiredAlert {
		t.Errorf("Should FIRE alert at T=120 because we had a streak of 5 inside the window.")
	}
	if m.Current() != StateCritical {
		t.Errorf("State should be CRITICAL")
	}

	// 4. Recovery requires 7 consecutive successes
	for i := 1; i <= 6; i++ {
		res = m.Transition(true, 10*time.Millisecond, now.Add(120*time.Second+time.Duration(i)*time.Second))
		if res.FiredRecover {
			t.Errorf("Should not recover at %d pings", i)
		}
	}

	// A high latency ping breaks the streak!
	res = m.Transition(true, 600*time.Millisecond, now.Add(130*time.Second))
	if res.FiredRecover {
		t.Errorf("Should not recover, latency was too high! (considered failure)")
	}

	// Now we need 7 clean ones
	for i := 1; i <= 6; i++ {
		m.Transition(true, 10*time.Millisecond, now.Add(200*time.Second+time.Duration(i)*time.Second))
	}
	res = m.Transition(true, 10*time.Millisecond, now.Add(300*time.Second))
	if !res.FiredRecover {
		t.Errorf("Should FIRE recovery after 7 clean consecutive pings")
	}
	if m.Current() != StateGreen {
		t.Errorf("State should be back to GREEN")
	}
}

func TestFSMMachine_Example1_ResetIfConditionNotMet(t *testing.T) {
	// If it doesn't meet 5 CONSECUTIVE inside 120s
	cfg := AlertConfig{T: 120, N: 5, C: true, R: 7}
	m := NewMachine(cfg, 500*time.Millisecond)

	now := time.Now()

	// 4 failures, then success
	m.Transition(false, 0, now)                     // Streak 1
	m.Transition(false, 0, now.Add(10*time.Second)) // Streak 2
	m.Transition(false, 0, now.Add(20*time.Second)) // Streak 3
	m.Transition(false, 0, now.Add(30*time.Second)) // Streak 4
	m.Transition(true, 0, now.Add(40*time.Second))  // Streak broken!

	// Then 3 failures later
	m.Transition(false, 0, now.Add(50*time.Second)) // Streak 1
	m.Transition(false, 0, now.Add(60*time.Second)) // Streak 2
	m.Transition(false, 0, now.Add(70*time.Second)) // Streak 3

	// At T=120, we had 7 total failures, but max consecutive was 4.
	// Because C=true, it should NOT fire.
	res := m.Transition(true, 0, now.Add(120*time.Second))
	if res.FiredAlert {
		t.Errorf("Should NOT fire. Max consecutive was 4. C=true requires 5 consecutive.")
	}
	if m.Current() != StateGreen {
		t.Errorf("State should remain GREEN")
	}
}

func TestFSMMachine_Example2(t *testing.T) {
	// Example 2: `{"t": 0, "n": 10, "c": true, "r": 3}`
	cfg := AlertConfig{T: 0, N: 10, C: true, R: 3}
	m := NewMachine(cfg, 500*time.Millisecond)
	now := time.Now()

	for i := 1; i <= 9; i++ {
		res := m.Transition(false, 0, now)
		if res.FiredAlert {
			t.Errorf("Should not fire yet at failure %d", i)
		}
	}

	// 10th failure
	res := m.Transition(false, 0, now)
	if !res.FiredAlert {
		t.Errorf("Should fire alert immediately on 10th consecutive failure because T=0")
	}
	if m.Current() != StateCritical {
		t.Errorf("State should be CRITICAL")
	}
}

func TestFSMMachine_Example3(t *testing.T) {
	// Example 3: `{"t": 120, "n": 5, "c": false, "r": 4}`
	cfg := AlertConfig{T: 120, N: 5, C: false, R: 4}
	m := NewMachine(cfg, 0)
	now := time.Now()

	// 5 failures interleaved with successes
	m.Transition(false, 0, now) // failure 1
	m.Transition(true, 0, now.Add(10*time.Second))
	m.Transition(false, 0, now.Add(20*time.Second)) // failure 2
	m.Transition(true, 0, now.Add(30*time.Second))
	m.Transition(false, 0, now.Add(40*time.Second)) // failure 3
	m.Transition(true, 0, now.Add(50*time.Second))
	m.Transition(false, 0, now.Add(60*time.Second)) // failure 4
	m.Transition(true, 0, now.Add(70*time.Second))
	res := m.Transition(false, 0, now.Add(80*time.Second)) // failure 5

	if res.FiredAlert {
		t.Errorf("Should NOT fire yet because T=120 elapsed is required!")
	}

	// Check at T=120. Condition Met! (5 cumulative failures)
	res = m.Transition(true, 0, now.Add(120*time.Second))

	if !res.FiredAlert {
		t.Errorf("Should FIRE alert. We had 5 cumulative failures and T=120 is met.")
	}
}

func TestFSMMachine_Example4(t *testing.T) {
	// Example 4: `{"t": 0, "n": 10, "c": false, "r": 20}`
	cfg := AlertConfig{T: 0, N: 10, C: false, R: 20}
	m := NewMachine(cfg, 0)
	now := time.Now()

	// Let's do 9 failures with a bunch of successes
	for i := 1; i <= 9; i++ {
		m.Transition(false, 0, now)
		m.Transition(true, 0, now)
	}

	if m.Current() != StateGreen {
		t.Errorf("Should be GREEN after 9 cumulative failures")
	}

	// 10th failure
	res := m.Transition(false, 0, now)
	if !res.FiredAlert {
		t.Errorf("Should FIRE instantly after 10 cumulative failures because T=0")
	}
}

func TestFSMMachine_Muting(t *testing.T) {
	cfg := AlertConfig{T: 0, N: 1, C: true, R: 1}
	m := NewMachine(cfg, 0)

	m.SetMute(true)

	// Will transition to CRITICAL because it's failure 1 and N=1.
	res := m.Transition(false, 0, time.Now())

	if m.Current() != StateCritical {
		t.Errorf("State should transition to CRITICAL regardless of Mute")
	}

	if res.FiredAlert {
		t.Errorf("FiredAlert flag MUST be false because it is Muted")
	}

	// Recover
	res = m.Transition(true, 0, time.Now())
	if m.Current() != StateGreen {
		t.Errorf("State should transition to GREEN")
	}
	if res.FiredRecover {
		t.Errorf("FiredRecover flag MUST be false because it is Muted")
	}
}
