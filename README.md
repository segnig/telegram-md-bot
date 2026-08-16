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
- Image downloads identify the bot with a `User-Agent`, since hosts such as
  Wikimedia answer `403` to unidentified clients.

### SVG images

The Bot API refuses SVG as a photo, so an SVG is fetched through a rasterizer
that returns PNG. This uses the public [images.weserv.nl](https://images.weserv.nl)
proxy by default, which means the image URL (not your content) is sent there.
Point it elsewhere, or turn it off and skip such images entirely:

```bash
export SVG_RENDER_ENDPOINT="https://images.internal.example.com/?url="
export SVG_RENDER_ENDPOINT=off
```

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
- Messages, edited messages, channel posts, and edited channel posts are accepted.
- In private chats every text message is converted. In groups, supergroups, and
  channels the bot responds to `/md <markdown>`, to an @mention, or to a reply
  to one of its messages. `/md` is the only form that survives Telegram's
  default privacy mode.
- Only the addressing prefix is removed from a group message; the document that
  follows reaches the converter unchanged, so a group and a private chat render
  the same input identically.
- In a group the converted answer is sent as a reply to the original message.
- In a channel the original post is edited in place, when the bot is an
  administrator with the "edit messages" right and the result is text that fits
  one message. Telegram cannot turn a text message into an album, so a post with
  images or Mermaid diagrams is answered as a reply instead, and a rejected edit
  also falls back to a reply.
- Private chats get a plain message, and command replies such as /help stay
  ordinary replies everywhere.
- When `PORT` is set (as on Render), a small HTTP health server listens so the
  process can run as a Free Web Service.
- Every inbound update is logged with its chat type, entities, and text, and
  ignored group messages say why, so delivery problems are visible in the log.
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

### Groups and channels

1. Add the bot to the group or channel (for channels it must be an
   administrator with permission to post messages).
2. In a **channel**, promote the bot and enable **Edit messages** so the post
   can be rewritten in place.
3. Send the markdown after a `/md` command, on the same message:

   ```
   /md@YourBot # Title
   **hello** and `code`
   ```

   `/convert` and `/markdown` are aliases. The markdown may span many lines.

Group and channel messages go through exactly the same pipeline as a private
chat. Only the addressing prefix is removed — the `/md` command or an @mention
on the message's first or last line — and the document after it is passed to the
converter byte for byte, so indentation, blank lines, and code blocks parse
identically. An @mention in the middle of the document is content, not
addressing, and is left in place.

Put the command on its own line when the document starts with an indented code
block. On a single line the space after `/md` cannot be told apart from that
indentation:

```
/md@YourBot
    indented code block
```

Use the command form in groups. New bots have **privacy mode enabled** by
default, and Telegram then delivers only commands, replies to the bot, and
service messages — a plain `@YourBot` mention usually never reaches the bot at
all, so it cannot answer it.

To make plain mentions and ordinary messages work:

1. Message @BotFather, `/setprivacy`, pick the bot, choose **Disable**.
2. Remove the bot from the group and add it again — the privacy setting is
   applied when the bot joins, so existing memberships keep the old behavior.

Replying to one of the bot's own messages works either way.

### Where the answer goes

In a group or channel fallback, the bot replies to the message it converted, so
the answer stays next to its source. Private chats get a plain message.

In a **channel** the bot edits the original post in place when it is an
administrator with **Edit messages**, and the result is text that fits one
message. A post with images or Mermaid is answered as a reply instead.

If a group message is ignored, the log line says so explicitly, including the
chat type and the text that arrived. If nothing is logged at all, Telegram never
delivered the update, which means privacy mode is still on.

## 3. Deploy on Render (Free Web Service)

This bot can run on Render’s **Free Web Service**. It listens on `$PORT` for
health checks while long-polling Telegram in the background.

### Limits you must accept

- Free web services **sleep after 15 minutes** with no HTTP traffic. While
  asleep, the bot stops polling and will not answer until it wakes up.
- To keep it awake, ping `https://YOUR-SERVICE.onrender.com/health` every
  5 minutes with a free monitor such as
  [UptimeRobot](https://uptimerobot.com/) or [cron-job.org](https://cron-job.org/).
- Free workspaces get **750 instance hours/month**. One always-on service uses
  about a full month of that budget.
- Render may restart free services at any time.

### Steps

1. Push this repo to GitHub (public or private).
2. Sign up at [render.com](https://render.com) with GitHub (no card needed for Free).
3. Dashboard → **New** → **Web Service** (not Background Worker).
4. Connect the repository.
5. Settings:

   | Field | Value |
   |-------|--------|
   | Language / Runtime | Go |
   | Branch | `main` (or your branch) |
   | Build Command | `go build -o telegram-md-bot .` |
   | Start Command | `./telegram-md-bot` |
   | Instance Type | **Free** |
   | Health Check Path | `/health` |

6. Environment → Add:

   | Key | Value |
   |-----|--------|
   | `TELEGRAM_BOT_TOKEN` | token from @BotFather |

7. Create Web Service and wait until the deploy is **Live**.
8. Open `https://YOUR-SERVICE.onrender.com/health` — it should return `ok`.
9. Message the bot on Telegram (`/start` or paste markdown).
10. **Required for 24/7:** create an UptimeRobot HTTP(s) monitor on
    `https://YOUR-SERVICE.onrender.com/health` every 5 minutes.

Or use the included `render.yaml`: Dashboard → **New** → **Blueprint** →
select the repo, then fill in `TELEGRAM_BOT_TOKEN`.

### Groups and channels (after deploy)

Same as local: use `/md@YourBot …` in groups. For channels, promote the bot
with **Edit messages** if you want in-place edits.

## 4. Build a binary

```bash
go build -o telegram-md-bot .
TELEGRAM_BOT_TOKEN="..." ./telegram-md-bot
```

Cross-compile for a Linux server from any machine:

```bash
GOOS=linux GOARCH=amd64 go build -o telegram-md-bot-linux .
```

## 5. Run the tests

```bash
go test ./...
go test -race ./...
go vet ./...
```

## 6. Deploy (simple systemd example)

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
