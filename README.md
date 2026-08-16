# telegram-md-bot

A Telegram bot, written in Go, that converts CommonMark and
GitHub-Flavored Markdown into
Telegram's **MarkdownV2** formatting. Send it any markdown and it replies
with:

1. a live preview of how the text will render in Telegram,
2. photo previews for any `![alt](url)` images (via `sendPhoto`), and
3. the raw converted MarkdownV2 source as copyable plain text, ready to
   paste into your own bot's `sendMessage` calls.

The converter uses [goldmark](https://github.com/yuin/goldmark), a
CommonMark-compliant parser, instead of regular-expression parsing. The
Telegram API client itself uses only the Go standard library.

## What it converts

| Markdown                | Telegram MarkdownV2 result                         |
|--------------------------|-----------------------------------------------------|
| `**bold**` / `__bold__`  | `*bold*`                                             |
| `*italic*` / `_italic_`  | `_italic_`                                           |
| `~~strike~~`             | `~strike~`                                           |
| `` `code` ``             | `` `code` `` (unchanged, just escaped internally)    |
| ` ```lang ... ``` `      | fenced code block, preserved as-is                   |
| `# / ## / ...` headers   | `*bold line*` (Telegram has no header entity)        |
| `- item` / `1. item`     | `• item` / `1\. item`                                |
| `- [x] task`             | `• ☑ task`                                            |
| `> quote`                | `> quote` (Telegram's native blockquote entity)      |
| `[text](url)`            | `[text](url)`, with escaping inside                  |
| `![alt](url)`            | `[📷 alt](url)` in text, plus `sendPhoto` preview    |
| `---` / `***` / `___`    | a plain `──────────` divider line                    |
| pipe tables              | a monospaced, column-aligned block (no native tables)|

All literal punctuation that MarkdownV2 treats as special
(`_ * [ ] ( ) ~ \` > # + - = | { } . !` and `\`) is automatically
backslash-escaped in plain text, per Telegram's spec.

Nested and mixed emphasis is parsed recursively, and GFM task lists and
tables are supported.

### Reliability behavior

- Long input is split at paragraph boundaries before conversion.
- Telegram rate limits (`retry_after`) are honored and retried once.
- Polling failures use bounded exponential backoff.
- `SIGINT` and `SIGTERM` cancel in-flight HTTP requests and shut down cleanly.
- Messages, edited messages, and channel posts are accepted.
- Nested blockquotes are flattened to one level, since Telegram cannot nest them.
- Links and images with relative or non-HTTP destinations become plain text,
  because the Bot API rejects them as link targets.
- Telegram API errors are returned as typed errors with their error code and
  retry delay.

## Project layout

```
telegram-md-bot/
├── go.mod
├── main.go                 # bot entrypoint: long-polls Telegram, replies
├── main_test.go
├── converter/
│   ├── converter.go         # CommonMark AST -> MarkdownV2 renderer
│   └── converter_test.go
└── telegram/
    ├── client.go             # context-aware Telegram API client
    └── client_test.go
```

## 1. Create the bot

1. Open Telegram, message **@BotFather**.
2. `/newbot`, follow the prompts, and copy the token it gives you
   (looks like `123456789:AAExampleTokenAbcDefGhi`).

## 2. Run locally

Requires Go 1.22+.

```bash
export TELEGRAM_BOT_TOKEN="123456789:AAExampleTokenAbcDefGhi"
go run .
```

Then message your bot on Telegram — try `/start`, or paste some markdown.

## 3. Build a binary

```bash
go build -o telegram-md-bot .
TELEGRAM_BOT_TOKEN="..." ./telegram-md-bot
```

Cross-compile for a Linux server from any machine:

```bash
GOOS=linux GOARCH=amd64 go build -o telegram-md-bot-linux .
```

## 4. Run the tests

```bash
go test ./...
go test -race ./...
go vet ./...
```

## 5. Deploy (simple systemd example)

```ini
# /etc/systemd/system/telegram-md-bot.service
[Unit]
Description=telegram-md-bot
After=network.target

[Service]
Environment=TELEGRAM_BOT_TOKEN=123456789:AAExampleTokenAbcDefGhi
ExecStart=/opt/telegram-md-bot/telegram-md-bot
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now telegram-md-bot
```

## Using the converter as a library elsewhere

The conversion logic is decoupled from the bot loop, so you can import it
into any other Go program (e.g. a webhook handler instead of long-polling):

```go
import "telegram-md-bot/converter"

telegramReady := converter.Convert(rawMarkdown)
```
