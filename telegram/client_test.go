package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testBot(server *httptest.Server) *Bot {
	return &Bot{client: server.Client(), apiBase: server.URL}
}

func TestTransportErrorsRedactToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijacking unsupported")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer server.Close()

	token := "123456:SECRET-TOKEN-VALUE"
	bot := NewWithAPIBase(server.URL, token)
	bot.client = server.Client()
	err := bot.SendMessage(context.Background(), Target{ChatID: 1}, "hi", "")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("token leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Errorf("expected redacted token marker, got: %v", err)
	}
}

func TestGetUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("offset"); got != "42" {
			t.Errorf("offset = %q, want 42", got)
		}
		if got := r.URL.Query().Get("timeout"); got != "50" {
			t.Errorf("timeout = %q, want 50", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":42,"message":{"message_id":7,"text":"hello","chat":{"id":99}}}]}`))
	}))
	defer server.Close()

	updates, err := testBot(server).GetUpdates(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetUpdates returned error: %v", err)
	}
	if len(updates) != 1 || updates[0].IncomingMessage().Text != "hello" {
		t.Fatalf("unexpected updates: %#v", updates)
	}
}

func TestSendMessagePayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["text"] != "*hello*" || payload["parse_mode"] != "MarkdownV2" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"text":"hello","chat":{"id":99}}}`))
	}))
	defer server.Close()

	if err := testBot(server).SendMessage(context.Background(), Target{ChatID: 99}, "*hello*", "MarkdownV2"); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
}

func TestSendPhotoAttachment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendPhoto" {
			t.Errorf("path = %q, want /sendPhoto", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("content type = %q, want multipart/form-data", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.FormValue("chat_id"); got != "99" {
			t.Errorf("chat_id = %q", got)
		}
		if got := r.FormValue("caption"); got != "*logo*" {
			t.Errorf("caption = %q", got)
		}
		if got := r.FormValue("parse_mode"); got != "MarkdownV2" {
			t.Errorf("parse_mode = %q", got)
		}
		file, header, err := r.FormFile("photo")
		if err != nil {
			t.Fatalf("missing photo file: %v", err)
		}
		defer file.Close()
		if header.Filename != "logo.png" {
			t.Errorf("filename = %q", header.Filename)
		}
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read photo: %v", err)
		}
		if string(data) != "PNGDATA" {
			t.Errorf("photo bytes = %q", data)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2,"text":"","chat":{"id":99}}}`))
	}))
	defer server.Close()

	photo := InputPhoto{Filename: "logo.png", Data: []byte("PNGDATA")}
	if err := testBot(server).SendPhoto(context.Background(), Target{ChatID: 99}, photo, "*logo*", "MarkdownV2"); err != nil {
		t.Fatalf("SendPhoto returned error: %v", err)
	}
}

func TestSendMediaGroupAttachment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMediaGroup" {
			t.Errorf("path = %q, want /sendMediaGroup", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		var media []map[string]any
		if err := json.Unmarshal([]byte(r.FormValue("media")), &media); err != nil {
			t.Fatalf("decode media: %v", err)
		}
		if len(media) != 2 {
			t.Fatalf("media items = %d, want 2", len(media))
		}
		if media[0]["caption"] != "album caption" || media[0]["media"] != "attach://file0" {
			t.Errorf("first media item = %#v", media[0])
		}
		if media[1]["media"] != "attach://file1" {
			t.Errorf("second media item = %#v", media[1])
		}
		for _, field := range []string{"file0", "file1"} {
			if _, _, err := r.FormFile(field); err != nil {
				t.Errorf("missing %s: %v", field, err)
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":2,"chat":{"id":99}},{"message_id":3,"chat":{"id":99}}]}`))
	}))
	defer server.Close()

	photos := []InputPhoto{
		{Filename: "a.jpg", Data: []byte("A")},
		{Filename: "b.jpg", Data: []byte("B")},
	}
	if err := testBot(server).SendMediaGroup(context.Background(), Target{ChatID: 99}, photos, "album caption", ""); err != nil {
		t.Fatalf("SendMediaGroup returned error: %v", err)
	}
}

func TestRateLimitErrorExposesRetryDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`))
	}))
	defer server.Close()

	err := testBot(server).SendMessage(context.Background(), Target{ChatID: 99}, "hello", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 {
		t.Fatalf("expected APIError 429, got %v", err)
	}
	delay, ok := RetryDelay(err)
	if !ok || delay != 3*time.Second {
		t.Errorf("RetryDelay = %v, %v; want 3s, true", delay, ok)
	}
}

func TestRequestCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := testBot(server).SendMessage(ctx, Target{ChatID: 99}, "hello", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestGetMe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Md","username":"MdBot"}}`))
	}))
	defer server.Close()

	me, err := testBot(server).GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.Username != "MdBot" || !me.IsBot {
		t.Errorf("unexpected identity: %#v", me)
	}
}

func TestIncomingMessagePrefersEditedChannelPost(t *testing.T) {
	update := Update{EditedChannelPost: &Message{Text: "edited"}}
	if got := update.IncomingMessage(); got == nil || got.Text != "edited" {
		t.Errorf("IncomingMessage() = %#v", got)
	}
}

func TestChatIsGroupOrChannel(t *testing.T) {
	for _, test := range []struct {
		kind string
		want bool
	}{
		{"private", false},
		{"group", true},
		{"supergroup", true},
		{"channel", true},
	} {
		if got := (Chat{Type: test.kind}).IsGroupOrChannel(); got != test.want {
			t.Errorf("type %q: got %v, want %v", test.kind, got, test.want)
		}
	}
	if !(Chat{Type: "channel"}).IsChannel() || (Chat{Type: "group"}).IsChannel() {
		t.Error("only channel should report IsChannel")
	}
}
