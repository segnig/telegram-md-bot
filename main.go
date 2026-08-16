// Command telegram-md-bot runs a Telegram bot that converts standard
// Markdown text sent to it into Telegram's MarkdownV2 format, and shows
// both a rendered preview and the raw, copy-pasteable source.
package main

import (
	"log"
	"os"
	"strings"
	"time"

	"telegram-md-bot/converter"
	"telegram-md-bot/telegram"
)

const welcomeText = "Send me any Markdown text (headers, **bold**, *italic*, lists, `code`, links, tables...) " +
	"and I'll convert it into Telegram's MarkdownV2 format.\n\n" +
	"I'll reply with:\n" +
	"1) a preview of how it will render in Telegram, and\n" +
	"2) the raw converted source, in a copyable code block, so you can paste it straight into your bot's sendMessage call."

func main() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	bot := telegram.New(token)
	offset := 0

	log.Println("bot started, polling for updates...")

	for {
		updates, err := bot.GetUpdates(offset)
		if err != nil {
			log.Printf("getUpdates error: %v (retrying in 3s)", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, u := range updates {
			offset = u.UpdateID + 1

			if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
				continue
			}
			handleMessage(bot, u.Message)
		}
	}
}

func handleMessage(bot *telegram.Bot, msg *telegram.Message) {
	chatID := msg.Chat.ID
	text := msg.Text

	if strings.HasPrefix(text, "/start") || strings.HasPrefix(text, "/help") {
		if err := bot.SendMessage(chatID, welcomeText, ""); err != nil {
			log.Printf("send welcome failed: %v", err)
		}
		return
	}

	converted := converter.Convert(text)

	// 1) Try sending a live-rendered preview.
	if err := bot.SendMessage(chatID, converted, "MarkdownV2"); err != nil {
		log.Printf("preview send failed, falling back to plain text: %v", err)
		fallback := "⚠️ Couldn't render a live preview (Telegram rejected an edge case in the formatting), " +
			"but here's the converted MarkdownV2 source below — it should still work when your bot sends it " +
			"with parse_mode=MarkdownV2:\n\n" + msg.Text
		if err := bot.SendMessage(chatID, fallback, ""); err != nil {
			log.Printf("fallback send failed: %v", err)
		}
	}

	// 2) Always also send the raw converted source as copyable text,
	//    wrapped in its own code fence so whitespace/escapes are visible.
	raw := "```\n" + strings.ReplaceAll(converted, "`", "'") + "\n```"
	if err := bot.SendMessage(chatID, raw, "MarkdownV2"); err != nil {
		// If even the fenced version fails (e.g. contains an odd number
		// of backticks after substitution), just send plain.
		if err := bot.SendMessage(chatID, converted, ""); err != nil {
			log.Printf("raw source send failed: %v", err)
		}
	}
}
