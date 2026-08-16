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
| unfenced code paragraph  | detected and wrapped in a code block                 |
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
backslash-escaped in plain text, per Telegram's spec. Escaping is
context-sensitive: inside code only `` ` `` and `\` are escaped, and inside a
link target only `)` and `\`, because escaping the rest there would show the
backslashes to the reader.

### Inline HTML

Markdown often carries a few HTML tags, and Telegram has an equivalent entity
for most of them, so they are translated instead of shown as literal text:

| HTML                            | Telegram MarkdownV2   |
|---------------------------------|-----------------------|
| `<b>`, `<strong>`               | `*bold*`              |
| `<i>`, `<em>`                   | `_italic_`            |
| `<u>`, `<ins>`                  | `__underline__`       |
| `<s>`, `<strike>`, `<del>`      | `~strikethrough~`     |
| `<code>`                        | `` `code` ``          |
| `<a href="…">`                  | `[text](…)`           |
| `<span class="tg-spoiler">`     | `\|\|spoiler\|\|`     |
| `<br>`, `<br/>`                 | a line break          |

A tag left unclosed is closed automatically, since Telegram rejects a message
with an unbalanced entity. Tags with no equivalent stay as escaped literal
text, and HTML character references (`&amp;`, `&lt;`, `&#39;`, `&hellip;`) are
decoded to the characters they stand for.

Nested and mixed emphasis is parsed recursively, and GFM task lists and
tables are supported.

### Indentation and nesting

Nested list structure is preserved: each level is indented by two spaces, and
mixed ordered/unordered nesting keeps its shape. Paragraphs that continue a
list item stay aligned under that item, and lists inside blockquotes keep
their indentation behind the `>` marker.

Code blocks and tables nested inside a list item are emitted at column zero,
because Telegram only recognizes a ``` fence at the start of a line.

### Code block detection

Fenced (` ``` `) and indented code blocks are converted to Telegram code blocks
as expected. In addition, a paragraph that was pasted **without** a fence is
detected as source and wrapped in one, so a snippet like

    let message = 'Hello world';
    alert(message);

arrives as a monospaced block instead of escaped prose. Detection is
deliberately conservative: it requires a majority of code-like lines plus at
least one unambiguous signal (a line ending in `;` or `{`, a lone brace, an
operator such as `:=` or `+=`, or a declaration keyword followed by code
punctuation). Sentences that merely begin with a keyword, such as "return to the
main menu" or "import duties may apply", stay as text.

When a language can be recognized from the source itself (Go, Python,
JavaScript, Java, SQL, HTML, JSON, Bash, CSS), the fence is tagged so Telegram
clients can highlight it. Adjacent paragraphs that look like fragments of the
same snippet are joined into one block, so a blank line inside pasted source
does not break it into alternating code and prose. Indentation relative to the
snippet's common baseline is preserved, which matters for Python and similar
languages.

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
  A reply that fits is always delivered as a single message.
- A single image uses `sendPhoto`, which is genuinely one message. Several
  images use `sendMediaGroup`, which Telegram delivers as one album.
- Up to 10 images are attached per reply, each at most 10 MB.
- SVG images are skipped, since the Bot API rejects them as photos.

### Long documents

Anything longer than those limits has to span several messages, so the text is
split at block boundaries rather than at an arbitrary character. Every part is
valid MarkdownV2 on its own, which matters because Telegram parses each message
independently and rejects one with an unbalanced entity:

- Cuts land between blocks, never inside a link, code span, or escape pair.
- A code block that must be divided gets its ``` fence (and language tag)
  repeated around each part.
- Emphasis still open at a cut is closed at the end of the part and reopened at
  the start of the next.
- With attachments, the first part becomes the caption and the rest follow as
  formatted messages, so nothing is dropped.
- If Telegram still refuses a part, it is resent unformatted with the escapes
  stripped instead of being replaced by an error notice.

The one case that cannot be preserved is a single link longer than the whole
limit, since it has no legal cut point.

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
│   ├── converter_test.go
│   ├── code.go              # unfenced snippet detection and language tags
│   ├── code_test.go
│   ├── html.go              # inline HTML and character references
│   ├── html_test.go
│   ├── split.go             # entity-safe splitting for Telegram's limits
│   └── split_test.go
├── telegram/
│   ├── client.go             # context-aware Telegram API client
│   └── client_test.go
└── testdata/
    └── everything.md         # sample exercising every supported feature
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
