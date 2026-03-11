package notification

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"sync"
	"time"
)

// Dispatcher handles the asynchronous sending of alerts to prevent blocking the FSM Eval loop.
type Dispatcher struct {
	host     string
	port     int
	username string
	password string
	fromMsg  string
	logger   *slog.Logger

	// Buffered channel acting as an asynchronous queue
	queue chan AlertPayload
	wg    sync.WaitGroup
}

// AlertPayload defines the data needed to send an email notification.
type AlertPayload struct {
	JobID       string
	Target      string
	IsRecovery  bool
	TriggerTime time.Time
	Details     string
	Recipient   string // e.g. Tenant's mapped email
}

// NewDispatcher initializes the SMTP worker queue.
func NewDispatcher(host string, port int, username, password, from string, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		host:     host,
		port:     port,
		username: username,
		password: password,
		fromMsg:  from,
		logger:   logger,
		// Buffer size 500 tolerates sudden mass-outages without blocking the caller FSM thread
		queue: make(chan AlertPayload, 500),
	}
}

// Start boots up the background workers that will drain the notification queue.
func (d *Dispatcher) Start(ctx context.Context, numWorkers int) {
	for i := 0; i < numWorkers; i++ {
		d.wg.Add(1)
		go d.workerLoop(ctx)
	}
}

// Stop gracefully waits for all pending emails to be sent before shutting down.
func (d *Dispatcher) Stop() {
	close(d.queue) // Signal workers that no new emails will arrive
	d.wg.Wait()
}

// Enqueue securely pushes an alert to the background queue.
// Implementing non-blocking logic to prevent Deadlocks if SMTP is unreachable.
func (d *Dispatcher) Enqueue(payload AlertPayload) {
	select {
	case d.queue <- payload:
		// Enqueued successfully
	default:
		// Queue is completely full (500+ pending emails).
		// We drop the email to protect the core Monitoring Loop (Fail Operational pattern).
		if d.logger != nil {
			d.logger.Error("Notification queue is FULL. Dropping alert to preserve memory", "job_id", payload.JobID)
		}
	}
}

func (d *Dispatcher) workerLoop(ctx context.Context) {
	defer d.wg.Done()

	for {
		select {
		case <-ctx.Done():
			// Forceful shutdown signal (SIGKILL)
			return

		case payload, ok := <-d.queue:
			if !ok {
				// Queue channel was closed gracefully
				return
			}

			d.sendEmailWithRetries(ctx, payload)
		}
	}
}

func (d *Dispatcher) sendEmailWithRetries(ctx context.Context, p AlertPayload) {
	// Simple Retry Pattern for transient network failures toward SendGrid/AWS SES
	maxRetries := 3

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := d.sendEmail(p)
		if err == nil {
			if d.logger != nil {
				d.logger.Info("Alert email dispatched cleanly", "job_id", p.JobID, "is_recovery", p.IsRecovery)
			}
			return
		}

		if d.logger != nil {
			d.logger.Warn("Failed to send email alert", "attempt", attempt, "error", err)
		}

		// Exponential backoff
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}

	if d.logger != nil {
		d.logger.Error("Exhausted retries. Email alert lost definitively.", "job_id", p.JobID)
	}
}

func (d *Dispatcher) sendEmail(p AlertPayload) error {
	// Skip entirely if SMTP is not configured
	if d.host == "" || d.port == 0 {
		return fmt.Errorf("SMTP configuration is missing or disabled")
	}

	auth := smtp.PlainAuth("", d.username, d.password, d.host)

	// Determine Subject Template
	subject := "CRITICAL: Monitoring Target Down"
	if p.IsRecovery {
		subject = "RESOLVED: Monitoring Target Restored"
	}

	// Construct purely text-based secure MIME
	msg := []byte(fmt.Sprintf("To: %s\r\n"+
		"From: %s\r\n"+
		"Subject: %s | %s\r\n"+
		"\r\n"+
		"Target: %s\r\n"+
		"Event Time: %s\r\n"+
		"Details: %s\r\n",
		p.Recipient, d.fromMsg, subject, p.Target, p.Target, p.TriggerTime.Format(time.RFC1123Z), p.Details))

	addr := fmt.Sprintf("%s:%d", d.host, d.port)

	return smtp.SendMail(addr, auth, d.fromMsg, []string{p.Recipient}, msg)
}
