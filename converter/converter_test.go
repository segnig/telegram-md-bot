package converter

import (
	"strings"
	"testing"
)

func TestBoldItalic(t *testing.T) {
	got := Convert("This is **bold** and this is *italic* and this is __also bold__.")
	want := `This is *bold* and this is _italic_ and this is *also bold*\.`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestStrikethrough(t *testing.T) {
	got := Convert("~~gone~~")
	want := "~gone~"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestInlineCode(t *testing.T) {
	got := Convert("Run `go build .` now")
	want := "Run `go build .` now"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestHeader(t *testing.T) {
	got := Convert("## Title Here")
	want := "*Title Here*"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestLink(t *testing.T) {
	got := Convert("See [my site](https://example.com/a_b?x=1)")
	want := "See [my site](https://example.com/a_b?x=1)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEscapesPlainPunctuation(t *testing.T) {
	got := Convert("Cost: $5.00 (approx!) - done.")
	if !strings.Contains(got, `\.`) || !strings.Contains(got, `\(`) || !strings.Contains(got, `\!`) {
		t.Errorf("expected escaped punctuation, got %q", got)
	}
}

func TestUnorderedList(t *testing.T) {
	got := Convert("- first\n- second")
	want := "• first\n• second"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestOrderedList(t *testing.T) {
	got := Convert("1. first\n2. second")
	want := "1\\. first\n2\\. second"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCodeBlock(t *testing.T) {
	got := Convert("```go\nfmt.Println(\"hi\")\n```")
	want := "```go\nfmt.Println(\"hi\")\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestBlockquote(t *testing.T) {
	got := Convert("> a wise quote")
	want := "> a wise quote"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestHorizontalRule(t *testing.T) {
	got := Convert("---")
	want := "──────────"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestTable(t *testing.T) {
	md := "| Name | Age |\n|------|-----|\n| Abel | 30  |"
	got := Convert(md)
	if !strings.HasPrefix(got, "```\n") || !strings.HasSuffix(got, "\n```") {
		t.Errorf("expected table wrapped in code block, got %q", got)
	}
	if !strings.Contains(got, "Name") || !strings.Contains(got, "Abel") {
		t.Errorf("table content missing, got %q", got)
	}
}

func TestNestedEmphasis(t *testing.T) {
	got := Convert("**bold and *italic* together**")
	want := "*bold and _italic_ together*"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestTaskList(t *testing.T) {
	got := Convert("- [x] shipped\n- [ ] pending")
	want := "• ☑ shipped\n• ☐ pending"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNestedList(t *testing.T) {
	got := Convert("- parent\n  1. child")
	want := "• parent\n  1\\. child"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnicodeTableAlignment(t *testing.T) {
	got := Convert("| Name | City |\n|---|---|\n| José | 東京 |")
	if !strings.Contains(got, "José") || !strings.Contains(got, "東京") {
		t.Errorf("Unicode table content missing, got %q", got)
	}
}

func TestRawHTMLRenderedAsText(t *testing.T) {
	got := Convert("<strong>unsafe</strong>")
	if got != `<strong\>unsafe</strong\>` {
		t.Errorf("expected raw HTML to remain literal text, got %q", got)
	}
}

func TestWindowsLineEndings(t *testing.T) {
	got := Convert("first\r\n\r\nsecond")
	want := "first\n\nsecond"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
