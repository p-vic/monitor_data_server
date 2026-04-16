package alerts

import (
	"bytes"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/monitoring-system/go-worker/internal/notification"
)

// Recordatorio: Este notifier debe abstraer el envío de emails con Muting Noise.
type BatchNotifier struct {
	mu           sync.Mutex
	alertQueue   map[string][]string // Key: subject/grouping, Value: list of messages
	flushTimeout time.Duration
	timer        *time.Timer
	dispatcher   *notification.Dispatcher
}

func NewBatchNotifier(flushTimeout time.Duration, d *notification.Dispatcher) Notifier {
	return &BatchNotifier{
		alertQueue:   make(map[string][]string),
		flushTimeout: flushTimeout,
		dispatcher:   d,
	}
}

func (b *BatchNotifier) SendEmail(to string, subject, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Agrupar por destinatario y asunto general
	groupKey := fmt.Sprintf("%s|%s", to, subject)
	b.alertQueue[groupKey] = append(b.alertQueue[groupKey], body)

	// Iniciar un temporizador si es el primer evento en la ventana
	if b.timer == nil {
		b.timer = time.AfterFunc(b.flushTimeout, b.flush)
	}

	return nil
}

func (b *BatchNotifier) flush() {
	b.mu.Lock()
	queueCopy := b.alertQueue
	b.alertQueue = make(map[string][]string) // Reset
	b.timer = nil
	b.mu.Unlock()

	for key, msgs := range queueCopy {
		count := len(msgs)

		var finalBody bytes.Buffer
		if count > 1 {
			// Muting Noise: Multiple incidents collapsed into one
			finalBody.WriteString(fmt.Sprintf("Muting Noise Triggered: %d incidents grouped.\n\n", count))
		}

		// En un escenario real, limitaríamos los mensajes a los primeros 10 para no saturar el email
		limit := count
		if limit > 10 {
			limit = 10
		}

		for i := 0; i < limit; i++ {
			finalBody.WriteString(msgs[i] + "\n")
		}

		if count > 10 {
			finalBody.WriteString(fmt.Sprintf("... and %d more alerts omitted.\n", count-10))
		}

		// Parse groupKey (to|subject)
		var to, subject string
		fmt.Sscanf(key, "%s|%s", &to, &subject) // Note this assumes subject has no spaces. Wait, subject might have spaces.
		// Better manual split:
		parts := bytes.SplitN([]byte(key), []byte("|"), 2)
		if len(parts) == 2 {
			to = string(parts[0])
			subject = string(parts[1])
		} else {
			to = key
		}

		isRecovery := false
		if bytes.Contains([]byte(subject), []byte("RECOVERY")) {
			isRecovery = true
		}

		// Inyectamos al Dispatcher real de SMTP
		if b.dispatcher != nil {
			b.dispatcher.Enqueue(notification.AlertPayload{
				JobID:       fmt.Sprintf("batch-%d", time.Now().UnixNano()),
				Target:      subject, // Sub-optimal but holds the Title
				IsRecovery:  isRecovery,
				TriggerTime: time.Now(),
				Details:     finalBody.String(),
				Recipient:   to,
			})
		} else {
			log.Printf("------------------ MOCK DISPATCHER -----------------\n")
			log.Printf("Routing Key: %s\n", key)
			log.Printf("Payload:\n%s\n", finalBody.String())
			log.Printf("-----------------------------------------------------------\n")
		}
	}
}
