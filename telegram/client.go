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
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseSize = 2 << 20

type Bot struct {
	client  *http.Client
	apiBase string
}

// DefaultAPIBase is Telegram's hosted Bot API.
const DefaultAPIBase = "https://api.telegram.org"

func New(token string) *Bot {
	return NewWithAPIBase(DefaultAPIBase, token)
}

// NewWithAPIBase targets a specific Bot API host, such as a local Bot API
// server.
func NewWithAPIBase(apiBase, token string) *Bot {
	apiBase = strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	return &Bot{
		client:  &http.Client{Timeout: 70 * time.Second},
		apiBase: apiBase + "/bot" + token,
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

// InputPhoto is image data uploaded to Telegram as a real attachment rather
// than referenced by URL.
type InputPhoto struct {
	Filename string
	Data     []byte
}

// SendPhoto uploads a single photo with an optional caption, producing one
// message.
func (b *Bot) SendPhoto(ctx context.Context, chatID int64, photo InputPhoto, caption, parseMode string) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if caption != "" {
		fields["caption"] = caption
	}
	if parseMode != "" {
		fields["parse_mode"] = parseMode
	}
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return fmt.Errorf("write sendPhoto field %q: %w", name, err)
		}
	}
	if err := writeFile(form, "photo", photo); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("finalize sendPhoto form: %w", err)
	}

	var sent Message
	return b.upload(ctx, "/sendPhoto", form.FormDataContentType(), body.Bytes(), &sent)
}

// SendMediaGroup uploads several photos as one album. Telegram attaches the
// caption to the album by placing it on the first item.
func (b *Bot) SendMediaGroup(ctx context.Context, chatID int64, photos []InputPhoto, caption, parseMode string) error {
	if len(photos) == 0 {
		return errors.New("sendMediaGroup requires at least one photo")
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	media := make([]map[string]any, 0, len(photos))
	for i, photo := range photos {
		field := "file" + strconv.Itoa(i)
		item := map[string]any{"type": "photo", "media": "attach://" + field}
		if i == 0 && caption != "" {
			item["caption"] = caption
			if parseMode != "" {
				item["parse_mode"] = parseMode
			}
		}
		media = append(media, item)
		if err := writeFile(form, field, photo); err != nil {
			return err
		}
	}

	encodedMedia, err := json.Marshal(media)
	if err != nil {
		return fmt.Errorf("encode sendMediaGroup media: %w", err)
	}
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("write sendMediaGroup chat_id: %w", err)
	}
	if err := form.WriteField("media", string(encodedMedia)); err != nil {
		return fmt.Errorf("write sendMediaGroup media: %w", err)
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("finalize sendMediaGroup form: %w", err)
	}

	var sent []Message
	return b.upload(ctx, "/sendMediaGroup", form.FormDataContentType(), body.Bytes(), &sent)
}

func writeFile(form *multipart.Writer, field string, photo InputPhoto) error {
	filename := photo.Filename
	if filename == "" {
		filename = field + ".jpg"
	}
	part, err := form.CreateFormFile(field, filename)
	if err != nil {
		return fmt.Errorf("create form file %q: %w", field, err)
	}
	if _, err := part.Write(photo.Data); err != nil {
		return fmt.Errorf("write form file %q: %w", field, err)
	}
	return nil
}

func (b *Bot) call(ctx context.Context, method, path string, payload []byte, result any) error {
	contentType := ""
	if payload != nil {
		contentType = "application/json"
	}
	return b.do(ctx, method, path, contentType, payload, result)
}

func (b *Bot) upload(ctx context.Context, path, contentType string, payload []byte, result any) error {
	return b.do(ctx, http.MethodPost, path, contentType, payload, result)
}

func (b *Bot) do(ctx context.Context, method, path, contentType string, payload []byte, result any) error {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.apiBase+path, body)
	if err != nil {
		return fmt.Errorf("create Telegram request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
