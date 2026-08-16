package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"telegram-md-bot/telegram"
)

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
	path    string
	text    string
	caption string
	mode    string
	files   int
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
			*calls = append(*calls, call)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`))
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.MultipartForm == nil {
			_ = r.ParseForm()
		}
		call.text = r.FormValue("text")
		call.caption = r.FormValue("caption")
		call.mode = r.FormValue("parse_mode")
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

	handleMessage(context.Background(), bot, message)

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

	handleMessage(context.Background(), bot, message)

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

	handleMessage(context.Background(), bot, message)

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
	handleMessage(context.Background(), bot, &telegram.Message{
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
