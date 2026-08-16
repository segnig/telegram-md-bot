package converter

import (
	"encoding/base64"
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

func TestDeeplyNestedListsKeepIndentation(t *testing.T) {
	md := "- top\n  - second\n    - third\n      1. fourth\n"
	want := "• top\n  • second\n    • third\n      1\\. fourth"
	if got := Convert(md); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFencedCodeInsideListItemIsKept(t *testing.T) {
	md := "- item with fence\n\n  ```go\n  fmt.Println(1)\n  ```\n"
	got := Convert(md)
	if !strings.Contains(got, "fmt.Println(1)") {
		t.Fatalf("code content dropped, got %q", got)
	}
	// A fence indented by the list would not be parsed by Telegram.
	if !strings.Contains(got, "\n```go\n") {
		t.Errorf("fence is not at column zero, got %q", got)
	}
}

func TestTableInsideListItemIsNotIndented(t *testing.T) {
	md := "- item with table\n\n  | a | b |\n  |---|---|\n  | 1 | 2 |\n"
	got := Convert(md)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "```") && strings.HasPrefix(line, " ") {
			t.Errorf("indented fence would break parsing: %q", got)
		}
	}
	if !strings.Contains(got, "a | b") {
		t.Errorf("table content missing, got %q", got)
	}
}

func TestListItemContinuationKeepsIndent(t *testing.T) {
	md := "- item\n\n  continuation paragraph\n"
	want := "• item\n  continuation paragraph"
	if got := Convert(md); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestListInsideBlockquoteKeepsIndent(t *testing.T) {
	md := "> quote\n>\n> - alpha\n>   - beta\n"
	got := Convert(md)
	if !strings.Contains(got, "> • alpha") || !strings.Contains(got, ">   • beta") {
		t.Errorf("quoted list indentation lost, got %q", got)
	}
}

func TestNestedBlockquoteFlattened(t *testing.T) {
	got := Convert("> outer\n>\n>> inner")
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "> >") || strings.HasPrefix(line, ">>") {
			t.Fatalf("nested blockquote marker left unescaped: %q", got)
		}
	}
	if !strings.Contains(got, "> inner") {
		t.Errorf("inner quote content missing, got %q", got)
	}
}

func TestRelativeImageBecomesPlainText(t *testing.T) {
	got := Convert(`![This is an alt text.](/image/Markdown-mark.svg "sample")`)
	if strings.Contains(got, "](") {
		t.Fatalf("relative destination must not become a link, got %q", got)
	}
	if !strings.Contains(got, "📷 This is an alt text") {
		t.Errorf("alt text missing, got %q", got)
	}
}

func TestAbsoluteLinkStillRendered(t *testing.T) {
	got := Convert("[site](https://markdownlivepreview.com/)")
	want := "[site](https://markdownlivepreview.com/)"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestIsLinkableURL(t *testing.T) {
	linkable := []string{"https://example.com", "http://example.com/a?b=1", "tg://user?id=1"}
	for _, u := range linkable {
		if !IsLinkableURL(u) {
			t.Errorf("IsLinkableURL(%q) = false, want true", u)
		}
	}
	rejected := []string{"/image/logo.svg", "image.png", "", "ftp://example.com", "https://"}
	for _, u := range rejected {
		if IsLinkableURL(u) {
			t.Errorf("IsLinkableURL(%q) = true, want false", u)
		}
	}
}

func TestExtractMermaidFromFence(t *testing.T) {
	md := "text\n\n```mermaid\ngraph TD\nA-->B\n```\n\n```go\nfmt.Println(1)\n```"
	got := ExtractMermaid(md)
	if len(got) != 1 {
		t.Fatalf("got %d diagrams, want 1: %#v", len(got), got)
	}
	if got[0] != "graph TD\nA-->B" {
		t.Errorf("unexpected diagram: %q", got[0])
	}
}

func TestExtractMermaidFromUnfencedParagraph(t *testing.T) {
	md := "## Mermaid diagrams\n\ngraph TD\nA[Start] --> B{Decision}\nB -->|Yes| C[Finish]"
	got := ExtractMermaid(md)
	if len(got) != 1 {
		t.Fatalf("got %d diagrams, want 1: %#v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "graph TD") || !strings.Contains(got[0], "B -->|Yes| C[Finish]") {
		t.Errorf("unexpected diagram: %q", got[0])
	}
}

func TestExtractMermaidIgnoresProse(t *testing.T) {
	md := "This paragraph mentions graph theory and pie charts.\n\nsequenceDiagramming is a hobby."
	if got := ExtractMermaid(md); len(got) != 0 {
		t.Errorf("expected no diagrams, got %#v", got)
	}
}

func TestMermaidImageURL(t *testing.T) {
	diagram := "graph TD\nA[Start] --> B{Decision}"
	got := MermaidImageURL("", diagram)
	if !strings.HasPrefix(got, DefaultMermaidEndpoint) {
		t.Fatalf("unexpected endpoint in %q", got)
	}
	encoded := strings.TrimPrefix(got, DefaultMermaidEndpoint)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("payload is not base64url: %v", err)
	}
	if string(decoded) != diagram {
		t.Errorf("decoded = %q, want %q", decoded, diagram)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("encoded payload is not URL-safe: %q", encoded)
	}
}

func TestMermaidImageURLCustomEndpoint(t *testing.T) {
	got := MermaidImageURL("https://mermaid.example.com/img", "graph TD\nA-->B")
	if !strings.HasPrefix(got, "https://mermaid.example.com/img/") {
		t.Errorf("unexpected URL: %q", got)
	}
}

func TestExtractImagesSkipsRelativeURLs(t *testing.T) {
	got := ExtractImages("![a](/image/logo.svg)\n\n![b](https://example.com/b.png)")
	if len(got) != 1 || got[0].URL != "https://example.com/b.png" {
		t.Fatalf("unexpected images: %#v", got)
	}
}

func TestExtractImages(t *testing.T) {
	md := "Intro\n\n![logo](https://example.com/logo.png)\n\nand ![ ](https://cdn.example.com/a.jpg)"
	got := ExtractImages(md)
	if len(got) != 2 {
		t.Fatalf("got %d images, want 2: %#v", len(got), got)
	}
	if got[0].Alt != "logo" || got[0].URL != "https://example.com/logo.png" {
		t.Errorf("first image = %#v", got[0])
	}
	if got[1].Alt != "image" || got[1].URL != "https://cdn.example.com/a.jpg" {
		t.Errorf("second image = %#v", got[1])
	}
}
