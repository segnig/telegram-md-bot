package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testBot(server *httptest.Server) *Bot {
	return &Bot{client: server.Client(), apiBase: server.URL}
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

	if err := testBot(server).SendMessage(context.Background(), 99, "*hello*", "MarkdownV2"); err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
}

func TestSendPhotoPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendPhoto" {
			t.Errorf("path = %q, want /sendPhoto", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["photo"] != "https://example.com/a.png" || payload["caption"] != "logo" {
			t.Errorf("unexpected payload: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":2,"text":"","chat":{"id":99}}}`))
	}))
	defer server.Close()

	if err := testBot(server).SendPhoto(context.Background(), 99, "https://example.com/a.png", "logo"); err != nil {
		t.Fatalf("SendPhoto returned error: %v", err)
	}
}

func TestRateLimitErrorExposesRetryDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`))
	}))
	defer server.Close()

	err := testBot(server).SendMessage(context.Background(), 99, "hello", "")
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
	err := testBot(server).SendMessage(ctx, 99, "hello", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
