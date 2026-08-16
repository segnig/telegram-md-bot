package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"telegram-md-bot/telegram"
)

var testIdentity = botIdentity{Username: "testbot", ID: 4242}

func TestIsCommand(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"/start", true},
		{"/start@my_bot argument", true},
		{"/starter", false},
		{"please /start", false},
		{"", false},
	}
	for _, test := range tests {
		if got := isCommand(test.text, "start"); got != test.want {
			t.Errorf("isCommand(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

// pngPixel is a minimal valid PNG payload for upload tests.
var pngPixel = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
}

func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngPixel)
	}))
}

type recordedCall struct {
	path      string
	text      string
	caption   string
	mode      string
	files     int
	replyTo   int
	editingID int
}

// sentCalls drops the housekeeping calls, leaving the ones that actually
// delivered the conversion.
func sentCalls(calls []recordedCall) []recordedCall {
	sent := make([]recordedCall, 0, len(calls))
	for _, call := range calls {
		if call.path != "/deleteMessage" {
			sent = append(sent, call)
		}
	}
	return sent
}

// deletedMessage returns the message a deleteMessage call removed, or 0 when
// nothing was deleted.
func deletedMessage(calls []recordedCall) int {
	for _, call := range calls {
		if call.path == "/deleteMessage" {
			return call.editingID
		}
	}
	return 0
}

// replyTarget extracts the message a call was attached to, from the
// reply_parameters object Telegram expects.
func replyTargetOf(encoded string) int {
	var parameters struct {
		MessageID int `json:"message_id"`
	}
	if json.Unmarshal([]byte(encoded), &parameters) != nil {
		return 0
	}
	return parameters.MessageID
}

func telegramServer(t *testing.T, calls *[]recordedCall) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Real requests are addressed to /bot<token>/<method>.
		method := r.URL.Path
		if index := strings.LastIndex(method, "/"); index >= 0 {
			method = method[index:]
		}
		call := recordedCall{path: method}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decoding %s body: %v", method, err)
			}
			call.text, _ = payload["text"].(string)
			call.caption, _ = payload["caption"].(string)
			call.mode, _ = payload["parse_mode"].(string)
			if id, ok := payload["message_id"].(float64); ok {
				call.editingID = int(id)
			}
			if reply, ok := payload["reply_parameters"].(map[string]any); ok {
				if id, ok := reply["message_id"].(float64); ok {
					call.replyTo = int(id)
				}
			}
			*calls = append(*calls, call)
			if method == "/deleteMessage" {
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.MultipartForm == nil {
			_ = r.ParseForm()
		}
		call.text = r.FormValue("text")
		call.caption = r.FormValue("caption")
		call.mode = r.FormValue("parse_mode")
		call.replyTo = replyTargetOf(r.FormValue("reply_parameters"))
		if r.MultipartForm != nil {
			for _, files := range r.MultipartForm.File {
				call.files += len(files)
			}
			if media := r.FormValue("media"); media != "" {
				var items []map[string]any
				if err := json.Unmarshal([]byte(media), &items); err == nil && len(items) > 0 {
					call.caption, _ = items[0]["caption"].(string)
					call.mode, _ = items[0]["parse_mode"].(string)
				}
			}
		}
		*calls = append(*calls, call)
		if call.path == "/sendMediaGroup" {
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":1,"chat":{"id":1}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
}

func TestMermaidReplyIsSingleAttachedMessage(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	t.Setenv("MERMAID_ENDPOINT", images.URL+"/img/")
	bot := telegram.NewWithAPIBase(api.URL, "token")
	message := &telegram.Message{
		Text: "# Diagram\n\ngraph TD\nA[Start] --> B{Decision}",
		Chat: telegram.Chat{ID: 1},
	}

	handleMessage(context.Background(), bot, testIdentity, message)

	if len(calls) != 1 {
		t.Fatalf("got %d API calls, want exactly 1: %#v", len(calls), calls)
	}
	if calls[0].path != "/sendPhoto" {
		t.Errorf("path = %q, want /sendPhoto", calls[0].path)
	}
	if calls[0].files != 1 {
		t.Errorf("attached files = %d, want 1", calls[0].files)
	}
	if calls[0].mode != "MarkdownV2" || !strings.Contains(calls[0].caption, "*Diagram*") {
		t.Errorf("unexpected caption %q (mode %q)", calls[0].caption, calls[0].mode)
	}
	if strings.Contains(calls[0].caption, "graph TD") {
		t.Errorf("mermaid source should be replaced by placeholder, got %q", calls[0].caption)
	}
	if !strings.Contains(calls[0].caption, `mermaid\-1`) || !strings.Contains(calls[0].caption, "attached image") {
		t.Errorf("mermaid placeholder missing from caption %q", calls[0].caption)
	}
}

func TestMultipleImagesBecomeSingleAlbum(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	t.Setenv("MERMAID_ENDPOINT", images.URL+"/img/")
	bot := telegram.NewWithAPIBase(api.URL, "token")
	message := &telegram.Message{
		Text: "![one](" + images.URL + "/a.png)\n\n![two](" + images.URL + "/b.png)\n\ngraph TD\nA-->B",
		Chat: telegram.Chat{ID: 1},
	}

	handleMessage(context.Background(), bot, testIdentity, message)

	if len(calls) != 1 {
		t.Fatalf("got %d API calls, want exactly 1: %#v", len(calls), calls)
	}
	if calls[0].path != "/sendMediaGroup" {
		t.Errorf("path = %q, want /sendMediaGroup", calls[0].path)
	}
	if calls[0].files != 3 {
		t.Errorf("attached files = %d, want 3", calls[0].files)
	}
}

func TestTextOnlyReplyIsSingleMessage(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	message := &telegram.Message{Text: "# Title\n\nsome **bold** text", Chat: telegram.Chat{ID: 1}}

	handleMessage(context.Background(), bot, testIdentity, message)

	if len(calls) != 1 {
		t.Fatalf("got %d API calls, want exactly 1: %#v", len(calls), calls)
	}
	if calls[0].path != "/sendMessage" {
		t.Errorf("path = %q, want /sendMessage", calls[0].path)
	}
}

// remoteImageRe points the sample's external images at the local test server.
var remoteImageRe = regexp.MustCompile(`https://[^)\s]+\.svg`)

func TestEverythingSampleIsDeliveredInFull(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	raw, err := os.ReadFile("testdata/everything.md")
	if err != nil {
		t.Fatalf("reading sample: %v", err)
	}
	sample := remoteImageRe.ReplaceAllString(string(raw), images.URL+"/img.png")

	t.Setenv("MERMAID_ENDPOINT", images.URL+"/img/")
	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: sample,
		Chat: telegram.Chat{ID: 1},
	})

	if len(calls) == 0 {
		t.Fatal("no API calls were made")
	}
	if calls[0].path != "/sendMediaGroup" {
		t.Fatalf("first call = %q, want /sendMediaGroup", calls[0].path)
	}
	// Two markdown images plus two Mermaid diagrams.
	if calls[0].files != 4 {
		t.Errorf("attached files = %d, want 4", calls[0].files)
	}
	if calls[0].mode != "MarkdownV2" {
		t.Errorf("caption mode = %q, want MarkdownV2", calls[0].mode)
	}
	if count := utf8.RuneCountInString(calls[0].caption); count == 0 || count > maxTelegramCaption {
		t.Errorf("caption has %d runes, limit is %d", count, maxTelegramCaption)
	}

	delivered := calls[0].caption
	for _, call := range calls[1:] {
		if call.path != "/sendMessage" {
			t.Errorf("follow-up call = %q, want /sendMessage", call.path)
		}
		if call.mode != "MarkdownV2" {
			t.Errorf("follow-up mode = %q, want MarkdownV2", call.mode)
		}
		if count := utf8.RuneCountInString(call.text); count > maxTelegramText {
			t.Errorf("follow-up has %d runes, limit is %d", count, maxTelegramText)
		}
		delivered += call.text
	}

	// Content from the very end of the sample must survive the split.
	for _, want := range []string{"*Everything sample*", "attached image", "Emoji"} {
		if !strings.Contains(delivered, want) {
			t.Errorf("delivered reply is missing %q", want)
		}
	}
}

func TestImageDownloadIdentifiesItself(t *testing.T) {
	var agent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent = r.UserAgent()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngPixel)
	}))
	defer server.Close()

	if _, err := downloadPhoto(context.Background(), server.URL+"/a.png"); err != nil {
		t.Fatalf("downloadPhoto() failed: %v", err)
	}
	if !strings.Contains(agent, "telegram-md-bot") {
		t.Errorf("User-Agent = %q, want it to name the bot", agent)
	}
}

// svgServer serves SVG at /*.svg and PNG for anything else, standing in for
// both the origin host and the rasterizer.
func svgServer(t *testing.T, rasterized *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".svg") {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
			return
		}
		if rasterized != nil {
			*rasterized++
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngPixel)
	}))
}

func TestSVGIsRasterizedBeforeUpload(t *testing.T) {
	rasterized := 0
	origin := svgServer(t, &rasterized)
	defer origin.Close()

	t.Setenv("SVG_RENDER_ENDPOINT", origin.URL+"/render?url=")
	photo, err := fetchImage(context.Background(), origin.URL+"/logo.svg")
	if err != nil {
		t.Fatalf("fetchImage() failed: %v", err)
	}
	if rasterized != 1 {
		t.Errorf("rasterizer was called %d times, want 1", rasterized)
	}
	if !strings.HasSuffix(photo.Filename, ".png") {
		t.Errorf("filename = %q, want a .png", photo.Filename)
	}
}

func TestSVGRasterizingCanBeDisabled(t *testing.T) {
	origin := svgServer(t, nil)
	defer origin.Close()

	t.Setenv("SVG_RENDER_ENDPOINT", "off")
	_, err := fetchImage(context.Background(), origin.URL+"/logo.svg")
	if !errors.Is(err, errUnsupportedFormat) {
		t.Errorf("error = %v, want errUnsupportedFormat", err)
	}
}

func TestSVGImageReachesTheAlbum(t *testing.T) {
	origin := svgServer(t, nil)
	defer origin.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	t.Setenv("SVG_RENDER_ENDPOINT", origin.URL+"/render?url=")
	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "![logo](" + origin.URL + "/logo.svg)",
		Chat: telegram.Chat{ID: 1},
	})

	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1: %#v", len(calls), calls)
	}
	if calls[0].path != "/sendPhoto" || calls[0].files != 1 {
		t.Errorf("SVG did not become an attachment: %#v", calls[0])
	}
}

func TestSVGEndpointDefaultsAndOverrides(t *testing.T) {
	t.Setenv("SVG_RENDER_ENDPOINT", "")
	if got := svgEndpoint(); got != defaultSVGEndpoint {
		t.Errorf("svgEndpoint() = %q, want the default", got)
	}
	t.Setenv("SVG_RENDER_ENDPOINT", "https://svg.internal/?url=")
	if got := svgEndpoint(); got != "https://svg.internal/?url=" {
		t.Errorf("svgEndpoint() = %q, want the override", got)
	}
	t.Setenv("SVG_RENDER_ENDPOINT", "off")
	if got := svgEndpoint(); got != "" {
		t.Errorf("svgEndpoint() = %q, want empty when disabled", got)
	}
}

func TestUnescapeDropsBackslashes(t *testing.T) {
	if got := unescape(`a\-b \\ c\.`); got != `a-b \ c.` {
		t.Errorf("unescape() = %q", got)
	}
}

func TestPhotoExtension(t *testing.T) {
	accepted := map[string]string{
		"image/jpeg":                ".jpg",
		"image/png; charset=binary": ".png",
		"IMAGE/WEBP":                ".webp",
	}
	for contentType, want := range accepted {
		got, ok := photoExtension(contentType)
		if !ok || got != want {
			t.Errorf("photoExtension(%q) = %q, %v; want %q, true", contentType, got, ok, want)
		}
	}
	for _, contentType := range []string{"image/svg+xml", "text/html", ""} {
		if _, ok := photoExtension(contentType); ok {
			t.Errorf("photoExtension(%q) accepted, want rejected", contentType)
		}
	}
}

func TestCaptionFallbackDropsEscapes(t *testing.T) {
	got := captionFallback(`hello\. world\!`)
	if got != "hello. world!" {
		t.Errorf("captionFallback = %q", got)
	}
}

func TestCaptionFallbackRejectsOversized(t *testing.T) {
	if got := captionFallback(strings.Repeat("a", maxTelegramCaption+1)); got != "" {
		t.Errorf("expected empty caption for oversized input, got %d runes", utf8.RuneCountInString(got))
	}
}

func TestPrivateChatWorksWithoutMention(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "**bold**",
		Chat: telegram.Chat{ID: 1, Type: "private"},
	})
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
}

func TestGroupIgnoresUnmentionedMessage(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "**bold**",
		Chat: telegram.Chat{ID: -100, Type: "supergroup"},
	})
	if len(calls) != 0 {
		t.Fatalf("unmentioned group message was handled: %#v", calls)
	}
}

func TestGroupRespondsToMention(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	text := "@testbot **bold**"
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: text,
		Chat: telegram.Chat{ID: -100, Type: "supergroup"},
		Entities: []telegram.MessageEntity{{
			Type:   "mention",
			Offset: 0,
			Length: len("@testbot"),
		}},
	})
	calls = sentCalls(calls)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1: %#v", len(calls), calls)
	}
	if strings.Contains(calls[0].text, "testbot") {
		t.Errorf("mention should be stripped from reply, got %q", calls[0].text)
	}
	if !strings.Contains(calls[0].text, "*bold*") {
		t.Errorf("converted text missing: %q", calls[0].text)
	}
}

func TestChannelRespondsToMention(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "@TestBot # Title",
		Chat: telegram.Chat{ID: -200, Type: "channel"},
		Entities: []telegram.MessageEntity{{
			Type:   "mention",
			Offset: 0,
			Length: len("@TestBot"),
		}},
	})
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if !strings.Contains(calls[0].text, "*Title*") {
		t.Errorf("channel conversion missing: %q", calls[0].text)
	}
}

func TestGroupRespondsToReply(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "*italic*",
		Chat: telegram.Chat{ID: -100, Type: "group"},
		ReplyToMessage: &telegram.Message{
			From: &telegram.User{Username: "testbot", IsBot: true},
		},
	})
	if len(sentCalls(calls)) != 1 {
		t.Fatalf("reply to bot was ignored: %#v", calls)
	}
}

func TestStripBotMentionPreservesEmojiOffsets(t *testing.T) {
	text := "🚀 @testbot **hi**"
	// rocket is one rune but two UTF-16 units, followed by space (1) then @testbot (8).
	got := stripBotMention(text, []telegram.MessageEntity{{
		Type:   "mention",
		Offset: 3,
		Length: 8,
	}}, "testbot")
	if got != "🚀  **hi**" && got != "🚀 **hi**" {
		// TrimSpace may collapse the trailing space next to the emoji differently.
		if !strings.Contains(got, "🚀") || !strings.Contains(got, "**hi**") || strings.Contains(got, "testbot") {
			t.Errorf("stripBotMention() = %q", got)
		}
	}
}

func TestGroupConvertCommandWorksWithoutMention(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "/md@testbot # Title\n**bold**",
		Chat: telegram.Chat{ID: -100, Type: "supergroup"},
		Entities: []telegram.MessageEntity{{
			Type:   "bot_command",
			Offset: 0,
			Length: len("/md@testbot"),
		}},
	})
	calls = sentCalls(calls)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1: %#v", len(calls), calls)
	}
	if !strings.Contains(calls[0].text, "*Title*") || !strings.Contains(calls[0].text, "*bold*") {
		t.Errorf("converted text missing: %q", calls[0].text)
	}
	if strings.Contains(calls[0].text, "/md") {
		t.Errorf("command should be stripped, got %q", calls[0].text)
	}
}

func TestBareConvertCommandExplainsUsage(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		Text: "/md@testbot",
		Chat: telegram.Chat{ID: -100, Type: "supergroup"},
	})
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if !strings.Contains(calls[0].text, "/md@testbot") {
		t.Errorf("usage should name the bot: %q", calls[0].text)
	}
}

// A bot cannot edit a group member's message, so the rendered version is posted
// and the original deleted, which leaves the same end state.
func TestGroupOriginalIsReplacedByTheConversion(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 555,
		Text:      "/md@testbot **bold**",
		Chat:      telegram.Chat{ID: -100, Type: "supergroup"},
	})
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want a send then a delete: %#v", len(calls), calls)
	}
	if calls[0].path != "/sendMessage" || !strings.Contains(calls[0].text, "*bold*") {
		t.Errorf("first call = %q %q, want the conversion", calls[0].path, calls[0].text)
	}
	// The send must come first: a failed delete then costs nothing.
	if calls[1].path != "/deleteMessage" {
		t.Fatalf("second call = %q, want /deleteMessage", calls[1].path)
	}
	if calls[1].editingID != 555 {
		t.Errorf("deleted message = %d, want 555", calls[1].editingID)
	}
	if calls[0].replyTo != 0 {
		t.Errorf("the replacement should stand alone, got reply target %d", calls[0].replyTo)
	}
}

// If the conversion never reached the chat, deleting the original would destroy
// the only copy of it.
func TestOriginalSurvivesAFailedSend(t *testing.T) {
	var paths []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path
		if index := strings.LastIndex(method, "/"); index >= 0 {
			method = method[index:]
		}
		paths = append(paths, method)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 555,
		Text:      "/md@testbot **bold**",
		Chat:      telegram.Chat{ID: -100, Type: "supergroup"},
	})
	for _, path := range paths {
		if path == "/deleteMessage" {
			t.Fatalf("original was deleted after a failed send: %v", paths)
		}
	}
}

func TestGroupWithImagesAlsoReplacesTheOriginal(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 77,
		Text:      "/md@testbot ![logo](" + images.URL + "/img.png)",
		Chat:      telegram.Chat{ID: -100, Type: "supergroup"},
	})
	if got := sentCalls(calls); len(got) == 0 || got[0].path != "/sendPhoto" {
		t.Fatalf("expected a photo to be sent, got %#v", calls)
	}
	if deletedMessage(calls) != 77 {
		t.Errorf("deleted message = %d, want 77", deletedMessage(calls))
	}
}

func TestPrivateReplyIsNotAThreadedReply(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 7,
		Text:      "**bold**",
		Chat:      telegram.Chat{ID: 1, Type: "private"},
	})
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].replyTo != 0 {
		t.Errorf("private chat should not reply to a message, got target %d", calls[0].replyTo)
	}
}

func TestChannelPostIsEditedInPlace(t *testing.T) {
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 42,
		Text:      "/md@testbot # Title",
		Chat:      telegram.Chat{ID: -200, Type: "channel"},
	})
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1: %#v", len(calls), calls)
	}
	if calls[0].path != "/editMessageText" {
		t.Fatalf("path = %q, want /editMessageText", calls[0].path)
	}
	if calls[0].editingID != 42 {
		t.Errorf("edited message = %d, want 42", calls[0].editingID)
	}
	if calls[0].mode != "MarkdownV2" || !strings.Contains(calls[0].text, "*Title*") {
		t.Errorf("edit payload = %q (mode %q)", calls[0].text, calls[0].mode)
	}
}

// Without the "edit messages" right Telegram refuses the edit, and the answer
// must still reach the channel.
func TestChannelFallsBackToReplyWhenEditIsRejected(t *testing.T) {
	var calls []recordedCall
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			calls = append(calls, recordedCall{path: "/editMessageText"})
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message can't be edited"}`))
			return
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		call := recordedCall{path: "/sendMessage"}
		call.text, _ = payload["text"].(string)
		if reply, ok := payload["reply_parameters"].(map[string]any); ok {
			if id, ok := reply["message_id"].(float64); ok {
				call.replyTo = int(id)
			}
		}
		calls = append(calls, call)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
	}))
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 42,
		Text:      "/md@testbot # Title",
		Chat:      telegram.Chat{ID: -200, Type: "channel"},
	})
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want an edit then a reply: %#v", len(calls), calls)
	}
	if calls[1].path != "/sendMessage" {
		t.Fatalf("fallback path = %q, want /sendMessage", calls[1].path)
	}
	if calls[1].replyTo != 42 {
		t.Errorf("fallback reply target = %d, want 42", calls[1].replyTo)
	}
	if !strings.Contains(calls[1].text, "*Title*") {
		t.Errorf("fallback text = %q", calls[1].text)
	}
}

// A text message cannot become an album, so a post with attachments is answered
// rather than edited.
func TestChannelPostWithImageIsAnsweredNotEdited(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	var calls []recordedCall
	api := telegramServer(t, &calls)
	defer api.Close()

	bot := telegram.NewWithAPIBase(api.URL, "token")
	handleMessage(context.Background(), bot, testIdentity, &telegram.Message{
		MessageID: 42,
		Text:      "/md@testbot ![logo](" + images.URL + "/img.png)",
		Chat:      telegram.Chat{ID: -200, Type: "channel"},
	})
	if len(calls) == 0 {
		t.Fatal("no API calls were made")
	}
	if calls[0].path != "/sendPhoto" {
		t.Fatalf("path = %q, want /sendPhoto", calls[0].path)
	}
	if calls[0].replyTo != 42 {
		t.Errorf("reply target = %d, want 42", calls[0].replyTo)
	}
}

// A group message must reach the converter as the same document a private chat
// would deliver: only the addressing prefix may differ.
func TestGroupParsesExactlyLikePrivateChat(t *testing.T) {
	images := imageServer(t)
	defer images.Close()
	t.Setenv("MERMAID_ENDPOINT", images.URL+"/img/")

	raw, err := os.ReadFile("testdata/everything.md")
	if err != nil {
		t.Fatalf("reading sample: %v", err)
	}
	sample := remoteImageRe.ReplaceAllString(string(raw), images.URL+"/img.png")

	deliver := func(message *telegram.Message) []string {
		var calls []recordedCall
		api := telegramServer(t, &calls)
		defer api.Close()

		bot := telegram.NewWithAPIBase(api.URL, "token")
		handleMessage(context.Background(), bot, testIdentity, message)
		sent := make([]string, 0, len(calls))
		for _, call := range sentCalls(calls) {
			sent = append(sent, call.caption+call.text)
		}
		return sent
	}

	private := deliver(&telegram.Message{
		Text: sample,
		Chat: telegram.Chat{ID: 1, Type: "private"},
	})
	if len(private) == 0 {
		t.Fatal("private chat produced no output")
	}

	groups := []struct {
		name    string
		message *telegram.Message
	}{
		{"mention", &telegram.Message{
			Text: "@testbot\n" + sample,
			Chat: telegram.Chat{ID: -100, Type: "supergroup"},
			Entities: []telegram.MessageEntity{{
				Type:   "mention",
				Offset: 0,
				Length: len("@testbot"),
			}},
		}},
		{"command", &telegram.Message{
			Text: "/md@testbot\n" + sample,
			Chat: telegram.Chat{ID: -100, Type: "supergroup"},
			Entities: []telegram.MessageEntity{{
				Type:   "bot_command",
				Offset: 0,
				Length: len("/md@testbot"),
			}},
		}},
	}
	for _, group := range groups {
		got := deliver(group.message)
		if len(got) != len(private) {
			t.Fatalf("%s sent %d messages, private chat sent %d", group.name, len(got), len(private))
		}
		for i := range private {
			if got[i] != private[i] {
				t.Errorf("%s message %d differs from private chat\ngroup:   %q\nprivate: %q",
					group.name, i, got[i], private[i])
			}
		}
	}
}

// An address prefix on its own line must not disturb the body, including a
// leading indented code block or a trailing blank line, both of which change
// how the markdown parses.
func TestAddressPrefixKeepsBodyIndentation(t *testing.T) {
	body := "    indented code\n\ntext\n\n* item\n    * nested\n"
	tests := []struct {
		name string
		got  string
	}{
		{"command on its own line", mustCommandArgument(t, "/md@testbot\n"+body)},
		{"command with trailing space before the break", mustCommandArgument(t, "/md@testbot  \n"+body)},
		{"mention on its own line", stripBotMention("@testbot\n"+body, []telegram.MessageEntity{{
			Type:   "mention",
			Offset: 0,
			Length: len("@testbot"),
		}}, "testbot")},
	}
	for _, test := range tests {
		if test.got != body {
			t.Errorf("%s:\ngot  %q\nwant %q", test.name, test.got, body)
		}
	}
}

// On a single line the separator is indistinguishable from indentation, so the
// one-line form is only expected to preserve ordinary markdown.
func TestSingleLineCommandKeepsBody(t *testing.T) {
	body := "# Title with **bold** and `code`"
	if got := mustCommandArgument(t, "/md@testbot "+body); got != body {
		t.Errorf("got %q, want %q", got, body)
	}
}

func mustCommandArgument(t *testing.T, text string) string {
	t.Helper()
	argument, ok := commandArgument(text, convertCommands...)
	if !ok {
		t.Fatalf("commandArgument(%q) did not match a command", text)
	}
	return argument
}

func TestMentionInsideDocumentIsKept(t *testing.T) {
	text := "# Title\n\nPing @testbot inside the body.\n\nMore text."
	got := stripBotMention(text, []telegram.MessageEntity{{
		Type:   "mention",
		Offset: len("# Title\n\nPing "),
		Length: len("@testbot"),
	}}, "testbot")
	if got != text {
		t.Errorf("mention inside the document was altered\ngot:  %q\nwant: %q", got, text)
	}
}

func TestTrailingMentionIsStripped(t *testing.T) {
	text := "# Title\n\nbody @testbot"
	got := stripBotMention(text, []telegram.MessageEntity{{
		Type:   "mention",
		Offset: len("# Title\n\nbody "),
		Length: len("@testbot"),
	}}, "testbot")
	if got != "# Title\n\nbody" {
		t.Errorf("stripBotMention() = %q", got)
	}
}

func TestCommandArgument(t *testing.T) {
	tests := []struct {
		text string
		want string
		ok   bool
	}{
		{"/md **bold**", "**bold**", true},
		{"/md@testbot **bold**", "**bold**", true},
		{"/MD@TestBot hi", "hi", true},
		{"/md\n# Title\nbody", "# Title\nbody", true},
		{"/convert x", "x", true},
		{"/md", "", true},
		{"/mdx hi", "", false},
		{"not a command /md hi", "", false},
		{"", "", false},
	}
	for _, test := range tests {
		got, ok := commandArgument(test.text, convertCommands...)
		if ok != test.ok || got != test.want {
			t.Errorf("commandArgument(%q) = (%q, %v), want (%q, %v)", test.text, got, ok, test.want, test.ok)
		}
	}
}

func TestReplyToBotMatchedByID(t *testing.T) {
	message := &telegram.Message{
		Text: "*italic*",
		Chat: telegram.Chat{Type: "supergroup"},
		ReplyToMessage: &telegram.Message{
			From: &telegram.User{ID: testIdentity.ID, IsBot: true},
		},
	}
	if !addressedToBot(message, testIdentity) {
		t.Fatal("reply to the bot's own id should address the bot")
	}
}

func TestCommandForAnotherBotIsIgnored(t *testing.T) {
	message := &telegram.Message{
		Text: "/md@otherbot hi",
		Chat: telegram.Chat{Type: "supergroup"},
		Entities: []telegram.MessageEntity{{
			Type:   "bot_command",
			Offset: 0,
			Length: len("/md@otherbot"),
		}},
	}
	if addressedToBot(message, testIdentity) {
		t.Fatal("a command aimed at another bot should not address us")
	}
}

func TestAddressedToBotCommandInGroup(t *testing.T) {
	message := &telegram.Message{
		Text: "/help@testbot",
		Chat: telegram.Chat{Type: "supergroup"},
		Entities: []telegram.MessageEntity{{
			Type:   "bot_command",
			Offset: 0,
			Length: len("/help@testbot"),
		}},
	}
	if !addressedToBot(message, testIdentity) {
		t.Fatal("command@bot should address the bot")
	}
}
