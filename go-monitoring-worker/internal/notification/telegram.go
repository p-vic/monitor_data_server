package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// TelegramSender sends messages to Telegram chats via the Bot API.
// All sends are fire-and-forget: they run in a background goroutine and
// log failures without blocking the caller.
type TelegramSender struct {
	botToken   string
	httpClient *http.Client
}

func NewTelegramSender(botToken string) *TelegramSender {
	return &TelegramSender{
		botToken: botToken,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send dispatches a message to chatID asynchronously.
func (t *TelegramSender) Send(chatID string, text string) {
	if t.botToken == "" || chatID == "" {
		return
	}
	go func() {
		if err := t.send(chatID, text); err != nil {
			log.Printf("[Telegram] send failed (chatID=%s): %v", chatID, err)
		}
	}()
}

func (t *TelegramSender) send(chatID string, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	payload, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned HTTP %d", resp.StatusCode)
	}
	return nil
}
