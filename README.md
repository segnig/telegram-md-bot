# telegram-md-bot

A Telegram bot, written in Go, that converts standard Markdown into
Telegram's **MarkdownV2** formatting. Send it any markdown and it replies
with:

1. a live preview of how the text will render in Telegram, and
2. the raw converted MarkdownV2 source in a copyable code block, ready to
   paste into your own bot's `sendMessage` calls.

No external Go dependencies — the Telegram API client and the converter
are both built on the standard library only, so `go build` works offline
once the module is on disk.

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
| `> quote`                | `> quote` (Telegram's native blockquote entity)      |
| `[text](url)`            | `[text](url)`, with escaping inside                  |
| `![alt](url)`            | `[📷 alt](url)` (Telegram has no inline images)      |
| `---` / `***` / `___`    | a plain `──────────` divider line                    |
| pipe tables              | a monospaced, column-aligned block (no native tables)|

All literal punctuation that MarkdownV2 treats as special
(`_ * [ ] ( ) ~ \` > # + - = | { } . !` and `\`) is automatically
backslash-escaped in plain text, per Telegram's spec.

### Known limitations

- Nested/mixed emphasis (e.g. `**bold *and italic* together**`) isn't
  parsed recursively — treat that as a stretch case.
- Link/image URLs containing an unescaped `)` inside the URL itself (e.g.
  `https://example.com/path_(x)`) are ambiguous in standard Markdown too;
  escape it as `\)` in your source or wrap the URL in `<angle brackets>`
  if you hit this.
- Table-row detection is heuristic (any line with `|` in it while inside a
  block of such lines); a stray `|` in prose could be misread as a table
  row.

## Project layout

```
telegram-md-bot/
├── go.mod
├── main.go                 # bot entrypoint: long-polls Telegram, replies
├── converter/
│   ├── converter.go         # the markdown -> MarkdownV2 conversion logic
│   └── converter_test.go
└── telegram/
    └── client.go             # minimal getUpdates / sendMessage client
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
