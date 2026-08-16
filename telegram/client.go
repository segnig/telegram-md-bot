// Package telegram is a tiny, dependency-free client for the pieces of the
// Telegram Bot API this bot needs: long-polling getUpdates and sendMessage.
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Bot struct {
	Token   string
	client  *http.Client
	apiBase string
}

func New(token string) *Bot {
	return &Bot{
		Token:   token,
		client:  &http.Client{Timeout: 70 * time.Second},
		apiBase: "https://api.telegram.org/bot" + token,
	}
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Chat      Chat   `json:"chat"`
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

// GetUpdates long-polls for new updates starting after the given offset.
func (b *Bot) GetUpdates(offset int) ([]Update, error) {
	q := url.Values{}
	q.Set("timeout", "50")
	q.Set("offset", fmt.Sprintf("%d", offset))

	resp, err := b.client.Get(b.apiBase + "/getUpdates?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var out apiResponse[[]Update]
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w (body=%s)", err, body)
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram error %d: %s", out.ErrorCode, out.Description)
	}
	return out.Result, nil
}

// SendMessage sends text to a chat. parseMode may be "MarkdownV2", "HTML", or "".
func (b *Bot) SendMessage(chatID int64, text, parseMode string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := b.client.Post(b.apiBase+"/sendMessage", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var out apiResponse[Message]
	if err := json.Unmarshal(respBody, &out); err != nil {
		return fmt.Errorf("decode sendMessage response: %w (body=%s)", err, respBody)
	}
	if !out.OK {
		return fmt.Errorf("telegram error %d: %s", out.ErrorCode, out.Description)
	}
	return nil
}
