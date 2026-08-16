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
	"net/url"
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
	// Several image hosts, Wikimedia among them, refuse requests that do not
	// identify the client.
	userAgent = "telegram-md-bot/1.0 (Markdown to Telegram MarkdownV2 bot)"
	// Telegram will not accept SVG as a photo, so such images are fetched
	// through a rasterizer that returns PNG.
	defaultSVGEndpoint = "https://images.weserv.nl/?output=png&w=1024&url="
)

const welcomeText = "Send me CommonMark or GitHub-Flavored Markdown and I'll convert it to Telegram MarkdownV2.\n\n" +
	"In a private chat, just paste markdown.\n\n" +
	"In a group or channel, start the message with /md@the_bot followed by your markdown:\n\n" +
	"/md@the_bot **bold** and `code`\n\n" +
	"In a group the conversion is sent as a reply to the original message. " +
	"In a channel the post is edited in place " +
	"(needs \"edit messages\"). Mentions and replies also work if privacy mode is disabled for me " +
	"in @BotFather (/setprivacy, then re-add me to the group).\n\n" +
	"Supported features include nested emphasis, links, images, task lists, blockquotes, fenced code, and tables. " +
	"Markdown images and Mermaid diagrams are uploaded as attachments in the same reply."

const commandUsageText = "Send the markdown after the command, on the same message:\n\n" +
	"/md@the_bot # Title\n**bold** and `code`\n\n" +
	"The markdown may span as many lines as you like."

func main() {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Render Free Web Services require a process that listens on $PORT.
	// The health server is optional locally (no PORT) and mandatory on Render.
	startHealthServer(ctx)

	bot := telegram.NewWithAPIBase(os.Getenv("TELEGRAM_API_BASE"), token)
	if err := run(ctx, bot); err != nil {
		log.Printf("bot stopped: %v", err)
	}
}

// startHealthServer binds the HTTP health endpoints Render probes. Without
// this, a Free Web Service is marked unhealthy and restarted in a loop.
func startHealthServer(ctx context.Context) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "telegram-md-bot is running\n")
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("health server listening on :%s (Render Free Web Service)", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("health server stopped: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
}

func run(ctx context.Context, bot *telegram.Bot) error {
	me, err := bot.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	username := strings.ToLower(me.Username)
	if username == "" {
		return errors.New("bot has no username; set one with @BotFather")
	}
	identity := botIdentity{Username: username, ID: me.ID}

	offset := 0
	backoff := time.Second
	log.Printf("bot started as @%s (id %d), polling for updates", username, me.ID)
	log.Printf("in groups/channels use: /md@%s <your markdown>", username)
	log.Printf("(a plain @%s mention only arrives if privacy mode is disabled in @BotFather)", username)

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
			logUpdate(update.UpdateID, message)
			if message == nil || strings.TrimSpace(message.Content()) == "" {
				continue
			}
			handleMessage(ctx, bot, identity, message)
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}

// botIdentity is the bot's own Telegram account, used to recognize mentions and
// replies in groups and channels.
type botIdentity struct {
	Username string
	ID       int64
}

func handleMessage(ctx context.Context, bot *telegram.Bot, identity botIdentity, message *telegram.Message) {
	content := message.Content()

	// An explicit /md command is always delivered, even in a group with
	// privacy mode on, so it is honored before any addressing rules.
	if argument, ok := commandArgument(content, convertCommands...); ok {
		if strings.TrimSpace(argument) == "" {
			usage := strings.ReplaceAll(commandUsageText, "@the_bot", "@"+identity.Username)
			if err := sendWithRetry(ctx, bot, replyTarget(message), usage, ""); err != nil {
				log.Printf("send usage failed (chat %d): %v", message.Chat.ID, err)
			}
			return
		}
		content = argument
	} else if message.Chat.IsGroupOrChannel() {
		if !addressedToBot(message, identity) {
			log.Printf("ignoring %s chat %d: not addressed to @%s (text=%q)",
				message.Chat.Type, message.Chat.ID, identity.Username, truncateForLog(content, 80))
			return
		}
		content = stripBotMention(content, message.ContentEntities(), identity.Username)
		log.Printf("handling %s chat %d from mention/reply (text=%q)",
			message.Chat.Type, message.Chat.ID, truncateForLog(content, 80))
	}
	if strings.TrimSpace(content) == "" {
		return
	}

	if isCommand(content, "start") || isCommand(content, "help") {
		welcome := strings.ReplaceAll(welcomeText, "@the_bot", "@"+identity.Username)
		if err := sendWithRetry(ctx, bot, replyTarget(message), welcome, ""); err != nil {
			log.Printf("send welcome failed (chat %d): %v", message.Chat.ID, err)
		}
		return
	}

	media := collectPhotos(ctx, content)
	converted := converter.ConvertMermaid(content, media.MermaidAttached)
	if converted == "" && len(media.Photos) == 0 {
		return
	}

	// In a channel the bot can replace the post it was given, which reads as
	// the markdown simply rendering itself. Attachments rule that out, because
	// Telegram cannot turn a text message into an album.
	if message.Chat.IsChannel() && len(media.Photos) == 0 &&
		editOriginalPost(ctx, bot, message, converted) {
		return
	}

	target := replyTarget(message)
	if len(media.Photos) > 0 {
		sendAlbumReply(ctx, bot, target, converted, media.Photos)
	} else {
		sendTextReply(ctx, bot, target, converted)
	}
}

// replyTarget attaches the answer to the message that asked for it, so it stays
// next to its source. A private chat needs no such anchor.
func replyTarget(message *telegram.Message) telegram.Target {
	target := telegram.Target{ChatID: message.Chat.ID}
	if message.Chat.IsGroupOrChannel() {
		target.ReplyTo = message.MessageID
	}
	return target
}

// editOriginalPost rewrites a channel post as its converted form and reports
// whether that worked. It fails when the bot is not an administrator with the
// "edit messages" right, or when the result is too long for one message, and
// the caller then falls back to an ordinary reply.
func editOriginalPost(ctx context.Context, bot *telegram.Bot, message *telegram.Message, converted string) bool {
	chunks := converter.Split(converted, maxTelegramText)
	if len(chunks) != 1 {
		return false
	}
	err := retry(ctx, func() error {
		return bot.EditMessageText(ctx, message.Chat.ID, message.MessageID, chunks[0], "MarkdownV2")
	})
	if err != nil {
		log.Printf("editing post %d in chat %d failed, replying instead: %v",
			message.MessageID, message.Chat.ID, err)
		return false
	}
	return true
}

// logUpdate records every inbound chat so group/channel delivery problems are
// visible without guessing whether Telegram forwarded the update.
func logUpdate(updateID int, message *telegram.Message) {
	if message == nil {
		log.Printf("update %d: no message payload", updateID)
		return
	}
	entities := make([]string, 0, len(message.ContentEntities()))
	for _, entity := range message.ContentEntities() {
		entities = append(entities, entity.Type)
	}
	log.Printf("update %d: chat=%d type=%s entities=%v text=%q",
		updateID, message.Chat.ID, message.Chat.Type, entities, truncateForLog(message.Content(), 100))
}

func truncateForLog(text string, limit int) string {
	runes := []rune(strings.ReplaceAll(text, "\n", "\\n"))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// addressedToBot reports whether a group or channel message is meant for this
// bot: an @mention, a /command@bot, or a reply to one of its messages.
func addressedToBot(message *telegram.Message, identity botIdentity) bool {
	if replied := message.ReplyToMessage; replied != nil && replied.From != nil {
		if replied.From.ID == identity.ID ||
			strings.EqualFold(replied.From.Username, identity.Username) {
			return true
		}
	}
	for _, entity := range message.ContentEntities() {
		switch entity.Type {
		case "mention":
			mention := utf16Slice(message.Content(), entity.Offset, entity.Length)
			if strings.EqualFold(mention, "@"+identity.Username) {
				return true
			}
		case "text_mention":
			// A mention without a username carries the account itself.
			if entity.User != nil && (entity.User.ID == identity.ID ||
				strings.EqualFold(entity.User.Username, identity.Username)) {
				return true
			}
		case "bot_command":
			command := utf16Slice(message.Content(), entity.Offset, entity.Length)
			if at := strings.Index(command, "@"); at >= 0 {
				if strings.EqualFold(command[at+1:], identity.Username) {
					return true
				}
				continue
			}
			// A bare /command in a group is delivered to every bot, so it
			// counts as addressed only when no other bot was named.
			return true
		}
	}
	// Fallback for hosts that omit entities: look for @username in the text.
	return strings.Contains(strings.ToLower(message.Content()), "@"+identity.Username)
}

// convertCommands are the commands that carry markdown to convert. Telegram's
// privacy mode always delivers commands to a bot, while a plain @mention in a
// group may never arrive, so this is the reliable way to reach the bot there.
var convertCommands = []string{"md", "convert", "markdown"}

// commandArgument returns the text following a leading /command, and whether
// one of the given commands was used. "/md@thebot **hi**" yields "**hi**".
func commandArgument(text string, commands ...string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}
	first, rest := trimmed, ""
	if index := strings.IndexAny(trimmed, " \t\n"); index >= 0 {
		first, rest = trimmed[:index], trimmed[index:]
	}
	name := strings.TrimPrefix(strings.SplitN(first, "@", 2)[0], "/")
	for _, command := range commands {
		if strings.EqualFold(name, command) {
			return consumeSeparator(rest), true
		}
	}
	return "", false
}

// consumeSeparator drops only the whitespace that separated an addressing
// prefix from the document: spaces and tabs on the prefix's own line plus at
// most one line break. Indentation on the first body line survives, so an
// indented code block still parses the way it would in a private chat.
func consumeSeparator(text string) string {
	text = strings.TrimLeft(text, " \t")
	text = strings.TrimPrefix(text, "\r")
	return strings.TrimPrefix(text, "\n")
}

// stripBotMention removes the @mention that addresses the bot, leaving the rest
// of the message untouched so the converter sees the same document a private
// chat would deliver.
func stripBotMention(text string, entities []telegram.MessageEntity, username string) string {
	runes := []rune(text)
	units := utf16Units(text)
	needle := "@" + username
	for _, entity := range entities {
		if entity.Type != "mention" {
			continue
		}
		if !strings.EqualFold(utf16Slice(text, entity.Offset, entity.Length), needle) {
			continue
		}
		start := utf16IndexToRune(units, entity.Offset)
		end := utf16IndexToRune(units, entity.Offset+entity.Length)
		if start < 0 || end < 0 || start >= end || end > len(runes) {
			continue
		}
		if stripped, ok := removeAddress(runes, start, end); ok {
			return stripped
		}
	}
	// Some clients omit entities; fall back to a textual match.
	index := strings.Index(strings.ToLower(text), strings.ToLower(needle))
	if index < 0 {
		return text
	}
	start := utf8.RuneCountInString(text[:index])
	if stripped, ok := removeAddress(runes, start, start+utf8.RuneCountInString(needle)); ok {
		return stripped
	}
	return text
}

// removeAddress deletes a mention that merely addresses the bot, meaning one on
// the message's first or last line. A mention in the middle of the document is
// part of the content and is kept, along with every other byte around it.
func removeAddress(runes []rune, start, end int) (string, bool) {
	before, after := string(runes[:start]), string(runes[end:])
	if strings.Contains(before, "\n") && strings.Contains(after, "\n") {
		return "", false
	}
	switch {
	case before == "" || strings.HasSuffix(before, "\n"):
		return before + consumeSeparator(after), true
	case after == "" || strings.HasPrefix(after, "\n") || strings.HasPrefix(after, "\r"):
		return strings.TrimRight(before, " \t") + after, true
	default:
		// Mention sat between words: close the gap it leaves behind.
		return before + strings.TrimLeft(after, " \t"), true
	}
}

// utf16Units returns the UTF-16 code unit count of each rune, matching the
// offsets Telegram uses in MessageEntity.
func utf16Units(s string) []int {
	units := make([]int, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			units = append(units, 2)
		} else {
			units = append(units, 1)
		}
	}
	return units
}

func utf16IndexToRune(units []int, offset int) int {
	at := 0
	for i, size := range units {
		if at == offset {
			return i
		}
		at += size
	}
	if at == offset {
		return len(units)
	}
	return -1
}

func utf16Slice(s string, offset, length int) string {
	runes := []rune(s)
	units := utf16Units(s)
	start := utf16IndexToRune(units, offset)
	end := utf16IndexToRune(units, offset+length)
	if start < 0 || end < 0 || start > end || end > len(runes) {
		return ""
	}
	return string(runes[start:end])
}

// sendAlbumReply delivers the response as the attachments plus their caption.
// Telegram caps a caption far below a message, so text that does not fit
// continues in follow-up messages rather than being dropped. It reports whether
// the whole response reached the chat.
func sendAlbumReply(ctx context.Context, bot *telegram.Bot, target telegram.Target, converted string, photos []telegram.InputPhoto) bool {
	caption, rest := "", ""
	if chunks := converter.Split(converted, maxTelegramCaption); len(chunks) > 0 {
		caption = chunks[0]
		rest = strings.Join(chunks[1:], "\n\n")
	}

	err := sendPhotos(ctx, bot, target, photos, caption, "MarkdownV2")
	if err != nil && caption != "" {
		log.Printf("album with MarkdownV2 caption failed: %v", err)
		err = sendPhotos(ctx, bot, target, photos, captionFallback(caption), "")
	}
	if err != nil {
		log.Printf("sending attachments failed: %v", err)
		return sendTextReply(ctx, bot, target, converted)
	}
	return sendTextReply(ctx, bot, target, rest)
}

func sendPhotos(ctx context.Context, bot *telegram.Bot, target telegram.Target, photos []telegram.InputPhoto, caption, parseMode string) error {
	if len(photos) == 1 {
		return retry(ctx, func() error {
			return bot.SendPhoto(ctx, target, photos[0], caption, parseMode)
		})
	}
	return retry(ctx, func() error {
		return bot.SendMediaGroup(ctx, target, photos, caption, parseMode)
	})
}

// sendTextReply sends the rendered MarkdownV2 preview, splitting at block
// boundaries so each part is still valid MarkdownV2 on its own. It reports
// whether every part reached the chat.
func sendTextReply(ctx context.Context, bot *telegram.Bot, target telegram.Target, converted string) bool {
	for _, chunk := range converter.Split(converted, maxTelegramText) {
		err := sendWithRetry(ctx, bot, target, chunk, "MarkdownV2")
		if err == nil {
			continue
		}
		// Rather than losing the part Telegram refused to parse, resend it
		// unformatted with the escapes stripped.
		log.Printf("preview reply failed: %v%s", err, offsetContext(chunk, err))
		if err := sendWithRetry(ctx, bot, target, unescape(chunk), ""); err != nil {
			log.Printf("send fallback failed: %v", err)
			return false
		}
	}
	return true
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
		photo, err := fetchImage(ctx, image.URL)
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

// errUnsupportedFormat marks an image Telegram will not accept as a photo.
var errUnsupportedFormat = errors.New("unsupported image format")

// fetchImage downloads an image, rasterizing formats Telegram rejects as
// photos. SVG is the common case: the Bot API refuses it outright, so without
// this an SVG in the markdown would simply go missing from the reply.
func fetchImage(ctx context.Context, imageURL string) (telegram.InputPhoto, error) {
	photo, err := downloadPhoto(ctx, imageURL)
	if !errors.Is(err, errUnsupportedFormat) {
		return photo, err
	}
	endpoint := svgEndpoint()
	if endpoint == "" {
		return telegram.InputPhoto{}, err
	}
	rasterized, rasterErr := downloadPhoto(ctx, endpoint+url.QueryEscape(imageURL))
	if rasterErr != nil {
		return telegram.InputPhoto{}, fmt.Errorf("%w (rasterizing also failed: %v)", err, rasterErr)
	}
	return rasterized, nil
}

func downloadPhoto(ctx context.Context, url string) (telegram.InputPhoto, error) {
	ctx, cancel := context.WithTimeout(ctx, photoFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return telegram.InputPhoto{}, err
	}
	// Wikimedia and others answer 403 to the default Go user agent, so the
	// bot identifies itself.
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/*")
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
		return telegram.InputPhoto{}, fmt.Errorf("%w: content type %q",
			errUnsupportedFormat, resp.Header.Get("Content-Type"))
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

// svgEndpoint returns the rasterizer that converts formats Telegram rejects
// into PNG. Set SVG_RENDER_ENDPOINT to a self-hosted service, or to "off" to
// skip such images instead of sending their URL to a third party.
func svgEndpoint() string {
	endpoint := strings.TrimSpace(os.Getenv("SVG_RENDER_ENDPOINT"))
	switch endpoint {
	case "":
		return defaultSVGEndpoint
	case "off":
		return ""
	default:
		return endpoint
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

func sendWithRetry(ctx context.Context, bot *telegram.Bot, target telegram.Target, text, parseMode string) error {
	return retry(ctx, func() error {
		return bot.SendMessage(ctx, target, text, parseMode)
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
