package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if err := r.ParseMultipartForm(1 << 20); err == nil && r.MultipartForm != nil {
			call.caption = r.FormValue("caption")
			call.mode = r.FormValue("parse_mode")
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

func TestSplitRunesPrefersNewline(t *testing.T) {
	parts := splitRunes("12345\n67890", 8)
	if len(parts) != 2 || parts[0] != "12345\n" {
		t.Errorf("unexpected split: %#v", parts)
	}
}

func TestSplitRunesLimitsUnicodeByRunes(t *testing.T) {
	input := strings.Repeat("東京", 20)
	parts := splitRunes(input, 11)
	for _, part := range parts {
		if count := utf8.RuneCountInString(part); count > 11 {
			t.Errorf("part has %d runes, limit is 11: %q", count, part)
		}
	}
	if strings.Join(parts, "") != input {
		t.Errorf("split content did not round-trip")
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
