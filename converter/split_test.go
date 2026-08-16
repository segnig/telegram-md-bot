package converter

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitKeepsShortTextWhole(t *testing.T) {
	got := Split("*hello*", 100)
	if len(got) != 1 || got[0] != "*hello*" {
		t.Errorf("Split() = %#v", got)
	}
}

func TestSplitDropsEmptyInput(t *testing.T) {
	if got := Split("   \n\n  ", 100); got != nil {
		t.Errorf("Split() = %#v, want nil", got)
	}
}

func TestSplitRespectsLimit(t *testing.T) {
	text := Convert(strings.Repeat("A paragraph with **bold** text.\n\n", 40))
	for _, chunk := range Split(text, 200) {
		if count := utf8.RuneCountInString(chunk); count > 200 {
			t.Errorf("chunk has %d runes, limit is 200: %q", count, chunk)
		}
	}
}

func TestSplitNeverBreaksInsideCodeFence(t *testing.T) {
	var input strings.Builder
	input.WriteString("intro\n\n```go\n")
	for i := 0; i < 60; i++ {
		input.WriteString("fmt.Println(\"line\")\n")
	}
	input.WriteString("```\n")

	for _, chunk := range Split(Convert(input.String()), 400) {
		if strings.Count(chunk, "```")%2 != 0 {
			t.Errorf("chunk has an unbalanced fence:\n%s", chunk)
		}
		if count := utf8.RuneCountInString(chunk); count > 400 {
			t.Errorf("chunk has %d runes, limit is 400", count)
		}
	}
}

func TestSplitReopensFenceWithLanguage(t *testing.T) {
	input := "```go\n" + strings.Repeat("x := 1\n", 40) + "```"
	chunks := Split(Convert(input), 120)
	if len(chunks) < 2 {
		t.Fatalf("expected the fence to be split, got %d chunks", len(chunks))
	}
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk, "```go\n") || !strings.HasSuffix(chunk, "\n```") {
			t.Errorf("chunk is not a self-contained fence:\n%s", chunk)
		}
	}
}

func TestSplitClosesEmphasisAcrossPieces(t *testing.T) {
	long := "*" + strings.Repeat("bold word ", 30) + "*"
	for _, chunk := range Split(long, 80) {
		if strings.Count(chunk, "*")%2 != 0 {
			t.Errorf("chunk leaves emphasis open: %q", chunk)
		}
	}
}

func TestSplitDoesNotCutInsideLink(t *testing.T) {
	text := strings.Repeat("see [label](https://example.com/page) and more words here. ", 6)
	for _, chunk := range Split(text, 100) {
		if strings.Count(chunk, "[") != strings.Count(chunk, "](") {
			t.Errorf("chunk splits a link: %q", chunk)
		}
	}
}

func TestSplitDoesNotCutAfterEscape(t *testing.T) {
	text := strings.Repeat(`word\. `, 40)
	for _, chunk := range Split(text, 50) {
		if strings.HasSuffix(chunk, `\`) {
			t.Errorf("chunk ends with a dangling escape: %q", chunk)
		}
	}
}

func TestSplitKeepsAllContent(t *testing.T) {
	text := Convert("# Title\n\n" + strings.Repeat("Some sentence here.\n\n", 30))
	joined := strings.Join(Split(text, 150), "")
	for _, word := range []string{"Title", "sentence"} {
		if !strings.Contains(joined, word) {
			t.Errorf("split lost %q", word)
		}
	}
	if got, want := strings.Count(joined, "sentence"), strings.Count(text, "sentence"); got != want {
		t.Errorf("split kept %d of %d sentences", got, want)
	}
}
