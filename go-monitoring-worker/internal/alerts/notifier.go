package alerts

// Notifier abstracts delivery of alert messages across channels.
type Notifier interface {
	SendEmail(to string, subject, body string) error
	SendTelegram(chatID string, message string) error
}
