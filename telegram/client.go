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
	token   string
}

// DefaultAPIBase is Telegram's hosted Bot API.
const DefaultAPIBase = "https://api.telegram.org"

// longPollTimeout is how long Telegram may hold a getUpdates request open.
const longPollTimeout = 50

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
		// Long-polling waits up to longPollTimeout seconds, so the client
		// timeout has to leave room for DNS, TLS, and a slow first byte.
		client:  &http.Client{Timeout: time.Duration(longPollTimeout+40) * time.Second},
		apiBase: apiBase + "/bot" + token,
		token:   token,
	}
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// MessageEntity marks a span of text, such as a mention of the bot.
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *User  `json:"user,omitempty"`
}

type Message struct {
	MessageID       int             `json:"message_id"`
	Text            string          `json:"text"`
	Caption         string          `json:"caption,omitempty"`
	Chat            Chat            `json:"chat"`
	From            *User           `json:"from,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
}

type Update struct {
	UpdateID          int      `json:"update_id"`
	Message           *Message `json:"message,omitempty"`
	EditedMessage     *Message `json:"edited_message,omitempty"`
	ChannelPost       *Message `json:"channel_post,omitempty"`
	EditedChannelPost *Message `json:"edited_channel_post,omitempty"`
}

// IncomingMessage returns the text-bearing message represented by an update.
func (u Update) IncomingMessage() *Message {
	switch {
	case u.Message != nil:
		return u.Message
	case u.EditedMessage != nil:
		return u.EditedMessage
	case u.ChannelPost != nil:
		return u.ChannelPost
	default:
		return u.EditedChannelPost
	}
}

// Content is the text or caption the bot should convert.
func (m *Message) Content() string {
	if m == nil {
		return ""
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	return m.Caption
}

// ContentEntities are the entities that apply to Content.
func (m *Message) ContentEntities() []MessageEntity {
	if m == nil {
		return nil
	}
	if strings.TrimSpace(m.Text) != "" {
		return m.Entities
	}
	return m.CaptionEntities
}

// IsGroupOrChannel reports whether the chat is a group, supergroup, or channel.
func (c Chat) IsGroupOrChannel() bool {
	switch c.Type {
	case "group", "supergroup", "channel":
		return true
	default:
		return false
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
	query.Set("timeout", strconv.Itoa(longPollTimeout))
	query.Set("offset", strconv.Itoa(offset))
	query.Set("allowed_updates", `["message","edited_message","channel_post","edited_channel_post"]`)

	var updates []Update
	if err := b.call(ctx, http.MethodGet, "/getUpdates?"+query.Encode(), nil, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// GetMe returns the bot's own identity, including its username used for
// mentions in groups and channels.
func (b *Bot) GetMe(ctx context.Context) (User, error) {
	var me User
	if err := b.call(ctx, http.MethodGet, "/getMe", nil, &me); err != nil {
		return User{}, err
	}
	return me, nil
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
		return fmt.Errorf("perform Telegram request: %w", b.redactError(err))
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("read Telegram response: %w", b.redactError(err))
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

// redactError strips the bot token from transport errors so it never lands in
// logs. Go's net/http errors include the full URL.
func (b *Bot) redactError(err error) error {
	if err == nil || b.token == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), b.token, "***")
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}
