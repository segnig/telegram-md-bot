package main

import (
	"strings"
	"testing"
	"unicode/utf8"
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

func TestSplitMarkdownKeepsParagraphs(t *testing.T) {
	input := "first paragraph\n\nsecond paragraph\n\nthird"
	parts := splitMarkdown(input, 34)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2: %#v", len(parts), parts)
	}
	if parts[0] != "first paragraph\n\nsecond paragraph" || parts[1] != "third" {
		t.Errorf("unexpected parts: %#v", parts)
	}
}

func TestSplitMarkdownLimitsUnicodeByRunes(t *testing.T) {
	input := strings.Repeat("東京", 20)
	parts := splitMarkdown(input, 11)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %#v", parts)
	}
	for _, part := range parts {
		if count := utf8.RuneCountInString(part); count > 11 {
			t.Errorf("part has %d runes, limit is 11: %q", count, part)
		}
	}
	if strings.Join(parts, "") != input {
		t.Errorf("split content did not round-trip")
	}
}

func TestSplitRunesPrefersNewline(t *testing.T) {
	parts := splitRunes("12345\n67890", 8)
	if len(parts) != 2 || parts[0] != "12345\n" {
		t.Errorf("unexpected split: %#v", parts)
	}
}
