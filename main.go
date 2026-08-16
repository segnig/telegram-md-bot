// Command telegram-md-bot runs a Telegram bot that converts CommonMark/GFM
// input into Telegram MarkdownV2.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"telegram-md-bot/converter"
	"telegram-md-bot/telegram"
)

const (
	maxTelegramText    = 4096
	maxTelegramCaption = 1024
	maxAlbumPhotos     = 10
	maxPhotoBytes      = 10 << 20
	photoFetchTimeout  = 30 * time.Second
)

const welcomeText = "Send me CommonMark or GitHub-Flavored Markdown and I'll convert it to Telegram MarkdownV2.\n\n" +
	"Supported features include nested emphasis, links, images, task lists, blockquotes, fenced code, and tables. " +
	"Markdown images and Mermaid diagrams are uploaded as attachments in the same reply."

func main() {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot := telegram.NewWithAPIBase(os.Getenv("TELEGRAM_API_BASE"), token)
	if err := run(ctx, bot); err != nil {
		log.Printf("bot stopped: %v", err)
	}
}

func run(ctx context.Context, bot *telegram.Bot) error {
	offset := 0
	backoff := time.Second
	log.Println("bot started, polling for updates")

	for {
		updates, err := bot.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			delay := backoff
			if retryAfter, ok := telegram.RetryDelay(err); ok {
				delay = retryAfter
			} else if backoff < 30*time.Second {
				backoff *= 2
			}
			log.Printf("getUpdates failed: %v; retrying in %s", err, delay)
			if err := sleepContext(ctx, delay); err != nil {
				return nil
			}
			continue
		}
		backoff = time.Second

		for _, update := range updates {
			offset = update.UpdateID + 1
			message := update.IncomingMessage()
			if message == nil || strings.TrimSpace(message.Text) == "" {
				continue
			}
			handleMessage(ctx, bot, message)
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}

func handleMessage(ctx context.Context, bot *telegram.Bot, message *telegram.Message) {
	if isCommand(message.Text, "start") || isCommand(message.Text, "help") {
		if err := sendWithRetry(ctx, bot, message.Chat.ID, welcomeText, ""); err != nil {
			log.Printf("send welcome failed: %v", err)
		}
		return
	}

	media := collectPhotos(ctx, message.Text)
	converted := converter.ConvertMermaid(message.Text, media.MermaidAttached)
	if converted == "" && len(media.Photos) == 0 {
		return
	}

	if len(media.Photos) > 0 {
		sendAlbumReply(ctx, bot, message.Chat.ID, converted, media.Photos)
		return
	}
	sendTextReply(ctx, bot, message.Chat.ID, converted)
}

// sendAlbumReply delivers the response as the attachments plus their caption.
// Telegram caps a caption far below a message, so text that does not fit
// continues in follow-up messages rather than being dropped.
func sendAlbumReply(ctx context.Context, bot *telegram.Bot, chatID int64, converted string, photos []telegram.InputPhoto) {
	caption, rest := "", ""
	if chunks := converter.Split(converted, maxTelegramCaption); len(chunks) > 0 {
		caption = chunks[0]
		rest = strings.Join(chunks[1:], "\n\n")
	}

	err := sendPhotos(ctx, bot, chatID, photos, caption, "MarkdownV2")
	if err != nil && caption != "" {
		log.Printf("album with MarkdownV2 caption failed: %v", err)
		err = sendPhotos(ctx, bot, chatID, photos, captionFallback(caption), "")
	}
	if err != nil {
		log.Printf("sending attachments failed: %v", err)
		sendTextReply(ctx, bot, chatID, converted)
		return
	}
	sendTextReply(ctx, bot, chatID, rest)
}

func sendPhotos(ctx context.Context, bot *telegram.Bot, chatID int64, photos []telegram.InputPhoto, caption, parseMode string) error {
	if len(photos) == 1 {
		return retry(ctx, func() error {
			return bot.SendPhoto(ctx, chatID, photos[0], caption, parseMode)
		})
	}
	return retry(ctx, func() error {
		return bot.SendMediaGroup(ctx, chatID, photos, caption, parseMode)
	})
}

// sendTextReply sends the rendered MarkdownV2 preview, splitting at block
// boundaries so each part is still valid MarkdownV2 on its own.
func sendTextReply(ctx context.Context, bot *telegram.Bot, chatID int64, converted string) {
	for _, chunk := range converter.Split(converted, maxTelegramText) {
		err := sendWithRetry(ctx, bot, chatID, chunk, "MarkdownV2")
		if err == nil {
			continue
		}
		// Rather than losing the part Telegram refused to parse, resend it
		// unformatted with the escapes stripped.
		log.Printf("preview reply failed: %v%s", err, offsetContext(chunk, err))
		if err := sendWithRetry(ctx, bot, chatID, unescape(chunk), ""); err != nil {
			log.Printf("send fallback failed: %v", err)
			return
		}
	}
}

func captionFallback(converted string) string {
	caption := unescape(converted)
	if utf8.RuneCountInString(caption) > maxTelegramCaption {
		return ""
	}
	return caption
}

// unescape drops the MarkdownV2 backslashes so text can be resent verbatim
// when Telegram rejects the formatted version.
func unescape(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
		}
		out.WriteRune(runes[i])
	}
	return out.String()
}

// collectedMedia is the set of uploaded photos for one reply, plus which
// Mermaid diagrams from ExtractMermaid were successfully attached.
type collectedMedia struct {
	Photos          []telegram.InputPhoto
	MermaidAttached []bool
}

// collectPhotos downloads markdown images and rendered Mermaid diagrams so
// they can be uploaded as attachments instead of referenced by URL.
func collectPhotos(ctx context.Context, markdown string) collectedMedia {
	var photos []telegram.InputPhoto

	for _, image := range converter.ExtractImages(markdown) {
		if len(photos) >= maxAlbumPhotos {
			break
		}
		photo, err := downloadPhoto(ctx, image.URL)
		if err != nil {
			log.Printf("skipping image %q: %v", image.URL, err)
			continue
		}
		photos = append(photos, photo)
	}

	diagrams := converter.ExtractMermaid(markdown)
	attached := make([]bool, len(diagrams))
	mermaidIndex := 0
	for i, diagram := range diagrams {
		if len(photos) >= maxAlbumPhotos {
			break
		}
		url := converter.MermaidImageURL(mermaidEndpoint(), diagram)
		photo, err := downloadPhoto(ctx, url)
		if err != nil {
			log.Printf("skipping Mermaid diagram: %v", err)
			continue
		}
		photo.Filename = converter.MermaidAttachmentName(mermaidIndex)
		mermaidIndex++
		photos = append(photos, photo)
		attached[i] = true
	}

	return collectedMedia{Photos: photos, MermaidAttached: attached}
}

func downloadPhoto(ctx context.Context, url string) (telegram.InputPhoto, error) {
	ctx, cancel := context.WithTimeout(ctx, photoFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return telegram.InputPhoto{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return telegram.InputPhoto{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return telegram.InputPhoto{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	extension, ok := photoExtension(resp.Header.Get("Content-Type"))
	if !ok {
		return telegram.InputPhoto{}, fmt.Errorf("unsupported content type %q", resp.Header.Get("Content-Type"))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return telegram.InputPhoto{}, err
	}
	if len(data) > maxPhotoBytes {
		return telegram.InputPhoto{}, fmt.Errorf("image exceeds %d bytes", maxPhotoBytes)
	}
	return telegram.InputPhoto{Filename: "image" + extension, Data: data}, nil
}

// photoExtension reports whether Telegram accepts the media type as a photo.
// SVG in particular is rejected, so it is filtered out here.
func photoExtension(contentType string) (string, bool) {
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	switch strings.ToLower(mediaType) {
	case "image/jpeg", "image/jpg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	case "image/gif":
		return ".gif", true
	case "image/bmp":
		return ".bmp", true
	default:
		return "", false
	}
}

// mermaidEndpoint allows pointing at a self-hosted renderer instead of the
// public service.
func mermaidEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("MERMAID_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	return converter.DefaultMermaidEndpoint
}

// byteOffsetRe pulls the position out of Telegram's "can't parse entities:
// ... at byte offset 123" rejection.
var byteOffsetRe = regexp.MustCompile(`byte offset (\d+)`)

// offsetContext quotes the text around the byte offset Telegram complained
// about, which is the piece of information needed to fix an escaping bug.
func offsetContext(text string, err error) string {
	var apiErr *telegram.APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	match := byteOffsetRe.FindStringSubmatch(apiErr.Description)
	if match == nil {
		return ""
	}
	offset, convErr := strconv.Atoi(match[1])
	if convErr != nil || offset > len(text) {
		return ""
	}
	start := max(offset-40, 0)
	end := min(offset+40, len(text))
	return fmt.Sprintf(" (around offset %d: %q, message is %d bytes)",
		offset, text[start:end], len(text))
}

func previewFallbackText(err error) string {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.Description != "" {
		return "Preview could not be rendered (Telegram said: " + apiErr.Description + ")."
	}
	return "Preview could not be rendered."
}

func sendWithRetry(ctx context.Context, bot *telegram.Bot, chatID int64, text, parseMode string) error {
	return retry(ctx, func() error {
		return bot.SendMessage(ctx, chatID, text, parseMode)
	})
}

// retry repeats a call once when Telegram answered with a rate-limit delay.
func retry(ctx context.Context, send func() error) error {
	err := send()
	delay, ok := telegram.RetryDelay(err)
	if !ok {
		return err
	}
	if err := sleepContext(ctx, delay); err != nil {
		return err
	}
	return send()
}

func isCommand(text, command string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	name := strings.SplitN(fields[0], "@", 2)[0]
	return name == "/"+command
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
