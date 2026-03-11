package alerts

// Notifier abstrae el envío de emails o webhooks, y gestiona el Muting Noise
type Notifier interface {
	SendEmail(to string, subject, body string) error
}
