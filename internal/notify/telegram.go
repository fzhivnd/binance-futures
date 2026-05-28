package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type TelegramClient struct {
	botToken string
	chatID   string
	client   *http.Client
}

func NewTelegramClient(botToken, chatID string, timeoutSecs int) *TelegramClient {
	timeout := time.Duration(timeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &TelegramClient{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: timeout},
	}
}

func (t *TelegramClient) SendMessage(ctx context.Context, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	payload := map[string]any{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "MarkdownV2",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned %d", resp.StatusCode)
	}
	return nil
}
