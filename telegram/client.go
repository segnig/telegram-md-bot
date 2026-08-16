// Package telegram provides the small, typed subset of the Telegram Bot API
// used by this application.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxResponseSize = 2 << 20

type Bot struct {
	client  *http.Client
	apiBase string
}

func New(token string) *Bot {
	return &Bot{
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
	UpdateID      int      `json:"update_id"`
	Message       *Message `json:"message,omitempty"`
	EditedMessage *Message `json:"edited_message,omitempty"`
	ChannelPost   *Message `json:"channel_post,omitempty"`
}

// IncomingMessage returns the text-bearing message represented by an update.
func (u Update) IncomingMessage() *Message {
	switch {
	case u.Message != nil:
		return u.Message
	case u.EditedMessage != nil:
		return u.EditedMessage
	default:
		return u.ChannelPost
	}
}

type responseParameters struct {
	RetryAfter int `json:"retry_after"`
}

type apiResponse[T any] struct {
	OK          bool               `json:"ok"`
	Result      T                  `json:"result"`
	Description string             `json:"description"`
	ErrorCode   int                `json:"error_code"`
	Parameters  responseParameters `json:"parameters"`
}

// APIError is returned when Telegram accepted an HTTP request but rejected the
// Bot API operation.
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram error %d: %s", e.Code, e.Description)
}

// RetryDelay returns Telegram's requested retry delay for rate-limit errors.
func RetryDelay(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter <= 0 {
		return 0, false
	}
	return apiErr.RetryAfter, true
}

// GetUpdates long-polls for new updates starting after offset.
func (b *Bot) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	query := url.Values{}
	query.Set("timeout", "50")
	query.Set("offset", strconv.Itoa(offset))
	query.Set("allowed_updates", `["message","edited_message","channel_post"]`)

	var updates []Update
	if err := b.call(ctx, http.MethodGet, "/getUpdates?"+query.Encode(), nil, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage sends text to a chat. parseMode may be "MarkdownV2", "HTML", or
// empty for plain text.
func (b *Bot) SendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode sendMessage request: %w", err)
	}

	var sent Message
	return b.call(ctx, http.MethodPost, "/sendMessage", body, &sent)
}

// SendPhoto sends a photo by public HTTP(S) URL. caption is optional plain text.
func (b *Bot) SendPhoto(ctx context.Context, chatID int64, photoURL, caption string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"photo":   photoURL,
	}
	if caption != "" {
		payload["caption"] = caption
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode sendPhoto request: %w", err)
	}

	var sent Message
	return b.call(ctx, http.MethodPost, "/sendPhoto", body, &sent)
}

func (b *Bot) call(ctx context.Context, method, path string, payload []byte, result any) error {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.apiBase+path, body)
	if err != nil {
		return fmt.Errorf("create Telegram request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("perform Telegram request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read Telegram response: %w", err)
	}

	var envelope apiResponse[json.RawMessage]
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf(
			"decode Telegram response (HTTP %d): %w (body=%q)",
			resp.StatusCode, err, responseBody,
		)
	}
	if !envelope.OK {
		return &APIError{
			Code:        envelope.ErrorCode,
			Description: envelope.Description,
			RetryAfter:  time.Duration(envelope.Parameters.RetryAfter) * time.Second,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected Telegram HTTP status %d", resp.StatusCode)
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram result: %w", err)
		}
	}
	return nil
}
