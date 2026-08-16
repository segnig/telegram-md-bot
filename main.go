// Command telegram-md-bot runs a Telegram bot that converts CommonMark/GFM
// input into Telegram MarkdownV2.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"telegram-md-bot/converter"
	"telegram-md-bot/telegram"
)

const (
	maxSourceChunk  = 1800
	maxTelegramText = 4096
)

const welcomeText = "Send me CommonMark or GitHub-Flavored Markdown and I'll convert it to Telegram MarkdownV2.\n\n" +
	"Supported features include nested emphasis, links, images, task lists, blockquotes, fenced code, and tables. " +
	"Long messages are split into safe-sized parts automatically."

func main() {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot := telegram.New(token)
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

	parts := splitMarkdown(message.Text, maxSourceChunk)
	for index, part := range parts {
		converted := converter.Convert(part)
		if converted == "" {
			continue
		}

		if err := sendWithRetry(ctx, bot, message.Chat.ID, converted, "MarkdownV2"); err != nil {
			log.Printf("preview part %d/%d failed: %v", index+1, len(parts), err)
			fallback := "Preview could not be rendered; the MarkdownV2 source follows."
			_ = sendWithRetry(ctx, bot, message.Chat.ID, fallback, "")
		}

		label := "MarkdownV2 source"
		if len(parts) > 1 {
			label += " (part " + itoa(index+1) + "/" + itoa(len(parts)) + ")"
		}
		raw := label + ":\n" + converted
		for _, chunk := range splitRunes(raw, maxTelegramText) {
			if err := sendWithRetry(ctx, bot, message.Chat.ID, chunk, ""); err != nil {
				log.Printf("send raw source part %d/%d failed: %v", index+1, len(parts), err)
				return
			}
		}
	}
}

func sendWithRetry(ctx context.Context, bot *telegram.Bot, chatID int64, text, parseMode string) error {
	err := bot.SendMessage(ctx, chatID, text, parseMode)
	delay, ok := telegram.RetryDelay(err)
	if !ok {
		return err
	}
	if err := sleepContext(ctx, delay); err != nil {
		return err
	}
	return bot.SendMessage(ctx, chatID, text, parseMode)
}

func isCommand(text, command string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	name := strings.SplitN(fields[0], "@", 2)[0]
	return name == "/"+command
}

func splitMarkdown(input string, limit int) []string {
	input = strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n"))
	if input == "" {
		return nil
	}

	var result []string
	var current strings.Builder
	for _, paragraph := range strings.Split(input, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if utf8.RuneCountInString(paragraph) > limit {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			result = append(result, splitRunes(paragraph, limit)...)
			continue
		}
		separator := ""
		if current.Len() > 0 {
			separator = "\n\n"
		}
		if utf8.RuneCountInString(current.String()+separator+paragraph) > limit {
			result = append(result, current.String())
			current.Reset()
			separator = ""
		}
		current.WriteString(separator)
		current.WriteString(paragraph)
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

func splitRunes(input string, limit int) []string {
	runes := []rune(input)
	if len(runes) == 0 {
		return nil
	}
	var result []string
	for len(runes) > 0 {
		end := min(limit, len(runes))
		if end < len(runes) {
			for i := end; i > end/2; i-- {
				if runes[i-1] == '\n' {
					end = i
					break
				}
			}
		}
		result = append(result, string(runes[:end]))
		runes = runes[end:]
	}
	return result
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

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
