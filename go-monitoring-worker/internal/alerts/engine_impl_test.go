package alerts

import (
	"testing"
	"time"

	"github.com/monitoring-system/go-worker/internal/models"
)

// mockNotifier intercepts emails to verify logic
type mockNotifier struct {
	sentEmails int
	lastSubj   string
}

func (m *mockNotifier) SendEmail(to string, subject string, body string) error {
	m.sentEmails++
	m.lastSubj = subject
	return nil
}

func (m *mockNotifier) SendTelegram(chatID string, message string) error {
	return nil
}

// Helper to simulate a ping result
func makePing(isDown bool, latency float64, t time.Time) models.PingResult {
	return models.PingResult{
		IsDown:    isDown,
		LatencyMs: latency,
		Timestamp: t,
	}
}

// Ejemplo 1: T=120, N=5, C=true, R=7
// Emite notif cuando expira T (120s) si hay 5 fallas consecutivas.
// Recupera después de 7 pings OK.
func TestFSM_Ejemplo1(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	target := models.TargetConfig{
		ID:             "tgt-1",
		Name:           "Test1",
		AlertConfig:    `{"t": 120, "n": 5, "c": true, "r": 7}`,
		MaxLatency:     100,
		WarningLatency: 50,
		AlertEmails:    []string{"test@test.com"},
	}

	baseTime := time.Now()

	// 1er Fallo - Inicia la ventana de 120s
	engine.ProcessResult(target, makePing(false, 200, baseTime))
	if notifier.sentEmails != 0 {
		t.Fatalf("Should not email yet")
	}

	// 4 fallos subsiguientes, PERO dentro de los 120s
	for i := 1; i <= 4; i++ {
		engine.ProcessResult(target, makePing(false, 200, baseTime.Add(time.Duration(i*10)*time.Second)))
		if notifier.sentEmails != 0 {
			t.Fatalf("Should not email yet, still inside T window")
		}
	}

	// 6to fallo que OCURRE después de los 120s (ej. a los 125s)
	// Aquí cierra la ventana y evalúa: ¿Tuvimos 5 consecutivas? Sí (de hecho 6). Dispara.
	engine.ProcessResult(target, makePing(false, 200, baseTime.Add(125*time.Second)))
	if notifier.sentEmails != 1 {
		t.Fatalf("Expected 1 email to be sent after 120s window elapsed with >= 5 consecutive errors")
	}

	// Recuperación (R=7)
	for i := 1; i <= 6; i++ {
		// Ping exitoso
		engine.ProcessResult(target, makePing(false, 30, baseTime.Add(time.Duration(130+i*10)*time.Second)))
		if notifier.sentEmails != 1 {
			t.Fatalf("Should not send recovery yet (needs 7)")
		}
	}
	// 7mo Ping
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(300*time.Second)))
	if notifier.sentEmails != 2 || notifier.lastSubj != "RECOVERY: Test1" {
		t.Fatalf("Expected recovery email after 7 continuous green pings")
	}
}

// Ejemplo 2: T=0, N=10, C=true, R=3
func TestFSM_Ejemplo2(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	target := models.TargetConfig{
		ID:             "tgt-2",
		Name:           "Test2",
		AlertConfig:    `{"t": 0, "n": 10, "c": true, "r": 3}`,
		MaxLatency:     100,
		WarningLatency: 50,
		AlertEmails:    []string{"test@test.com"},
	}

	baseTime := time.Now()

	// 9 Fallos
	for i := 0; i < 9; i++ {
		engine.ProcessResult(target, makePing(false, 200, baseTime.Add(time.Duration(i)*time.Second)))
	}
	if notifier.sentEmails != 0 {
		t.Fatalf("Expected 0 emails at 9 faults")
	}

	// Falla aleatoria OK que rompa el streak (C=true)
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(10*time.Second)))

	// Ahora necesitamos 10 nuevamentes consecutivas
	for i := 0; i < 9; i++ {
		engine.ProcessResult(target, makePing(false, 200, baseTime.Add(time.Duration(20+i)*time.Second)))
	}
	// El décimo del streak
	engine.ProcessResult(target, makePing(false, 200, baseTime.Add(30*time.Second)))

	if notifier.sentEmails != 1 {
		t.Fatalf("Expected 1 critical email upon EXACTLY 10 consecutive faults with T=0, got %d", notifier.sentEmails)
	}

	// Recuperación (R=3)
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(40*time.Second)))
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(41*time.Second)))
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(42*time.Second)))

	if notifier.sentEmails != 2 {
		t.Fatalf("Expected recovery email")
	}
}

// Ejemplo 3: T=120, N=5, C=false, R=4
// 5 alertas ACUMULADAS (no consecutivas) en una ventana de 120s
func TestFSM_Ejemplo3(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	target := models.TargetConfig{
		ID:          "tgt-3",
		AlertConfig: `{"t": 120, "n": 5, "c": false, "r": 4}`,
		MaxLatency:  100,
		AlertEmails: []string{"test@test.com"},
	}

	baseTime := time.Now()

	engine.ProcessResult(target, makePing(true, 0, baseTime))                       // Fallo 1 (inicia reloj)
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(10*time.Second))) // OK
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(20*time.Second)))   // Fallo 2
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(30*time.Second))) // OK
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(40*time.Second)))   // Fallo 3
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(50*time.Second))) // OK
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(60*time.Second)))   // Fallo 4
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(70*time.Second))) // OK
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(80*time.Second)))   // Fallo 5

	if notifier.sentEmails != 0 {
		t.Fatalf("Should not alert yet: T=120s has not expired")
	}

	// Rompemos la ventana de 120s enviando un ping a los 125s (ya sea verde o rojo, el motor DEBE evaluar el pasado)
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(125*time.Second)))

	if notifier.sentEmails != 1 {
		t.Fatalf("Expected 1 email since window closed and we had >=5 cumulative errors (even with Greens in between).")
	}
}

// Ejemplo 4: T=0, N=10, C=false, R=20
func TestFSM_Ejemplo4(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	target := models.TargetConfig{
		ID:          "tgt-4",
		AlertConfig: `{"t": 0, "n": 10, "c": false, "r": 20}`,
		MaxLatency:  100,
		AlertEmails: []string{"test@test.com"},
	}

	baseTime := time.Now()

	// Meteremos 9 errores alternando con 9 verdes.
	// Como C=false, los verdes no borran la cuenta de windowErrors (asumiendo T=0 evalua infinito o la ventana no importa)
	// Para T=0, C=false significa que al llegar a 10 históricos, truena.
	timePtr := baseTime
	for i := 0; i < 9; i++ {
		engine.ProcessResult(target, makePing(true, 0, timePtr))
		timePtr = timePtr.Add(1 * time.Second)
		engine.ProcessResult(target, makePing(false, 10, timePtr))
		timePtr = timePtr.Add(1 * time.Second)
	}

	if notifier.sentEmails != 0 {
		t.Fatalf("Should be exactly 0 emails")
	}

	// Décimo fallo acumulativo
	engine.ProcessResult(target, makePing(true, 0, timePtr))

	if notifier.sentEmails != 1 {
		t.Fatalf("Expected 1 critical email upon exactly 10 cumulative faults (interleaved) with T=0")
	}
}

// Alarm suppression: child alert is suppressed when parent is already CRITICAL.
// Parent fires normally; child enters alarmed state but sends no notification.
// When parent recovers, a subsequent child failure sends its own alert.
func TestFSM_AlarmSuppression(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	parent := models.TargetConfig{
		ID:             "parent-1",
		Name:           "Tower",
		AlertConfig:    `{"t": 0, "n": 1, "c": true, "r": 1}`,
		MaxLatency:     100,
		WarningLatency: 50,
		AlertEmails:    []string{"ops@test.com"},
	}
	child := models.TargetConfig{
		ID:             "child-1",
		Name:           "Router under Tower",
		ParentID:       "parent-1",
		AlertConfig:    `{"t": 0, "n": 1, "c": true, "r": 1}`,
		MaxLatency:     100,
		WarningLatency: 50,
		AlertEmails:    []string{"ops@test.com"},
	}

	engine.UpdateTargets([]models.TargetConfig{parent, child})

	baseTime := time.Now()

	// 1. Parent goes CRITICAL → must fire notification
	engine.ProcessResult(parent, makePing(true, 0, baseTime))
	if notifier.sentEmails != 1 {
		t.Fatalf("Expected 1 email for parent CRITICAL, got %d", notifier.sentEmails)
	}

	// 2. Child goes CRITICAL while parent is still CRITICAL → must be suppressed
	engine.ProcessResult(child, makePing(true, 0, baseTime.Add(5*time.Second)))
	if notifier.sentEmails != 1 {
		t.Fatalf("Expected child CRITICAL to be suppressed (still 1 email), got %d", notifier.sentEmails)
	}

	// 3. Parent recovers
	engine.ProcessResult(parent, makePing(false, 10, baseTime.Add(10*time.Second)))
	if notifier.sentEmails != 2 {
		t.Fatalf("Expected recovery email for parent, got %d", notifier.sentEmails)
	}

	// 4. Child is still down — reset child state and trigger again (simulates next sync cycle)
	engine.ResetTarget("child-1")
	engine.ProcessResult(child, makePing(true, 0, baseTime.Add(15*time.Second)))
	if notifier.sentEmails != 3 {
		t.Fatalf("Expected child CRITICAL alert now that parent recovered, got %d", notifier.sentEmails)
	}
}

// Validación explícita de Caída (IsDown) y Recuperación por Latencia
func TestFSM_IsDownToRecovery(t *testing.T) {
	notifier := &mockNotifier{}
	engine := NewEngine(notifier, nil)

	target := models.TargetConfig{
		ID:             "tgt-down",
		Name:           "Server En Llamas",
		AlertConfig:    `{"t": 0, "n": 3, "c": true, "r": 2}`, // 3 fallos para CRITICAL, 2 para RECOVERY
		MaxLatency:     100,
		WarningLatency: 50,
		AlertEmails:    []string{"test@test.com"},
	}

	baseTime := time.Now()

	// 1. Simular la caída absoluta (IsDown = true) 3 veces
	engine.ProcessResult(target, makePing(true, 0, baseTime))
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(10*time.Second)))
	engine.ProcessResult(target, makePing(true, 0, baseTime.Add(20*time.Second)))

	if notifier.sentEmails != 1 {
		t.Fatalf("Expected EXACTLY 1 email after 3 IsDown=true pings, got %d", notifier.sentEmails)
	}

	if notifier.lastSubj != "CRITICAL ALERT: Server En Llamas" {
		t.Fatalf("Expected CRITICAL ALERT email, got %s", notifier.lastSubj)
	}

	// 2. Simular ping fallido con alta latencia (aún no se recupera)
	engine.ProcessResult(target, makePing(false, 300, baseTime.Add(30*time.Second)))
	if notifier.sentEmails != 1 {
		t.Fatalf("Should not send unexpected recovery emails yet")
	}

	// 3. Empieza a recuperarse: 1er Ping OK (R requiere 2)
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(40*time.Second)))
	if notifier.sentEmails != 1 {
		t.Fatalf("Should not send recovery yet: needs 2 consecutive clean pings")
	}

	// 4. Se cierra la recuperación: 2do Ping OK (R=2 alcanzado)
	engine.ProcessResult(target, makePing(false, 30, baseTime.Add(50*time.Second)))
	if notifier.sentEmails != 2 {
		t.Fatalf("Expected RECOVERY email after exactly 2 valid pings (R=2)")
	}

	if notifier.lastSubj != "RECOVERY: Server En Llamas" {
		t.Fatalf("Expected RECOVERY email, got %s", notifier.lastSubj)
	}
}
