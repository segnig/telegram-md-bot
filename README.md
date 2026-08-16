# telegram-md-bot

A Telegram bot, written in Go, that converts CommonMark and
GitHub-Flavored Markdown into
Telegram's **MarkdownV2** formatting.

The bot answers with a **single message** per request. Markdown images and
Mermaid diagrams are downloaded and uploaded to Telegram as real
attachments, and the converted MarkdownV2 becomes the caption of that same
message, so everything shares one message.

When a request has no images, the reply is one text message with only the
rendered preview.

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
| `![alt](url)`            | `[📷 alt](url)` in text, plus an uploaded attachment  |
| `---` / `***` / `___`    | a plain `──────────` divider line                    |
| pipe tables              | a monospaced, column-aligned block (no native tables)|
| Mermaid diagrams         | rendered to an image and uploaded as an attachment    |

All literal punctuation that MarkdownV2 treats as special
(`_ * [ ] ( ) ~ \` > # + - = | { } . !` and `\`) is automatically
backslash-escaped in plain text, per Telegram's spec.

Nested and mixed emphasis is parsed recursively, and GFM task lists and
tables are supported.

### Indentation and nesting

Nested list structure is preserved: each level is indented by two spaces, and
mixed ordered/unordered nesting keeps its shape. Paragraphs that continue a
list item stay aligned under that item, and lists inside blockquotes keep
their indentation behind the `>` marker.

Code blocks and tables nested inside a list item are emitted at column zero,
because Telegram only recognizes a ``` fence at the start of a line.

### Mermaid diagrams

Mermaid diagrams are detected in two forms: fenced blocks tagged
` ```mermaid `, and unfenced paragraphs that start with a diagram
declaration such as `graph TD`, `sequenceDiagram`, or `gantt`.

Each successfully rendered Mermaid diagram is uploaded as an attachment named
`mermaid-N.jpg`, and the diagram source in the reply text is replaced with a
placeholder such as `📎 mermaid-1.jpg (attached image)`.

Rendering uses the public [mermaid.ink](https://mermaid.ink) service by
default, which means diagram source leaves your machine. Point
`MERMAID_ENDPOINT` at a self-hosted renderer to avoid that:

```bash
export MERMAID_ENDPOINT="https://mermaid.internal.example.com/img/"
```

A diagram that fails to render is skipped and logged; the rest of the reply
is still delivered.

### One message per reply

Telegram imposes limits that shape this behavior:

- A caption may hold 1024 characters, while a text message may hold 4096.
  If the converted markdown exceeds the caption limit, it is sent as a
  follow-up message, because it cannot fit alongside the attachments.
- A single image uses `sendPhoto`, which is genuinely one message. Several
  images use `sendMediaGroup`, which Telegram delivers as one album.
- Up to 10 images are attached per reply, each at most 10 MB.
- SVG images are skipped, since the Bot API rejects them as photos.

### Reliability behavior

- Telegram API and image downloads honor request cancellation.
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
