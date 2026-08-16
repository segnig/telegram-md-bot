package converter

import (
	"os"
	"strings"
	"testing"
)

var codeSnippets = map[string]struct {
	snippet string
	lang    string
}{
	"js":                 {"let message = 'Hello world';\nalert(message);", "js"},
	"js arrow":           {"const greet = (name) => {\n  console.log(name);\n};", "js"},
	"go with blank line": {"package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}", "go"},
	"go short":           {"count := 0\ncount += 1\nfmt.Println(count)", "go"},
	"python":             {"def main():\n    print(\"hi\")", "python"},
	"python imports":     {"import os\n\ndef main():\n    print(os.getcwd())", "python"},
	"java":               {"public static void main(String[] args) {\n  System.out.println(1);\n}", "java"},
	"sql":                {"SELECT id, name\nFROM users;", "sql"},
	"html":               {"<html>\n<body>\n<div class=\"x\">hi</div>\n</body>\n</html>", "html"},
	"bash":               {"$ go install ./...\n$ ./telegram-md-bot", "bash"},
	"bash no prompt":     {"docker compose up -d\nkubectl get pods", "bash"},
	"json":               {"{\n  \"name\": \"bot\",\n  \"version\": 2\n}", "json"},
	"css":                {".button {\n  color: red;\n  padding: 2px;\n}", "css"},
	"comments":           {"// setup the client\nclient := New()\nclient.Run()", "go"},
}

func TestUnfencedSnippetsAreDetected(t *testing.T) {
	for name, test := range codeSnippets {
		t.Run(name, func(t *testing.T) {
			got := Convert(test.snippet)
			if !strings.HasPrefix(got, "```") {
				t.Fatalf("snippet was not detected as code:\n%s", got)
			}
			if !strings.HasSuffix(got, "```") {
				t.Errorf("fence is not closed:\n%s", got)
			}
			// The snippet must survive verbatim apart from code escaping.
			for _, line := range strings.Split(test.snippet, "\n") {
				if line == "" {
					continue
				}
				if !strings.Contains(got, escapeCode(line)) {
					t.Errorf("line %q missing from:\n%s", line, got)
				}
			}
		})
	}
}

func TestDetectedSnippetsAreLabelled(t *testing.T) {
	for name, test := range codeSnippets {
		t.Run(name, func(t *testing.T) {
			if got := detectLanguage(test.snippet); got != test.lang {
				t.Errorf("detectLanguage() = %q, want %q", got, test.lang)
			}
		})
	}
}

func TestSnippetSplitByBlankLineStaysOneBlock(t *testing.T) {
	got := Convert("package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}")
	if count := strings.Count(got, "```"); count != 2 {
		t.Errorf("expected one fence, got %d fence markers:\n%s", count, got)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("declaration was dropped from:\n%s", got)
	}
}

func TestProseNextToCodeIsNotSwallowed(t *testing.T) {
	got := Convert("let x = 1;\n\nThis sentence explains what the code above does.")
	if !strings.Contains(got, "```") {
		t.Fatalf("code was not detected:\n%s", got)
	}
	if strings.Contains(got, "```\nThis sentence") {
		t.Errorf("prose was pulled into the code block:\n%s", got)
	}
	if !strings.Contains(got, `does\.`) {
		t.Errorf("prose lost its escaping:\n%s", got)
	}
}

func TestProseIsNeverTreatedAsCode(t *testing.T) {
	prose := []string{
		"Cost: $5.00 (approx!) - done.",
		"This web site is using markedjs/marked.",
		"return to the main menu when finished",
		"import duties may apply; check with customs.",
		"Markdown is a lightweight markup language, created in 2004 by John Gruber.",
		"You may be using [Markdown Live Preview](https://markdownlivepreview.com/).",
		"Let me know if you need any help with this;",
		"See <https://example.com> for the full documentation.",
		"The meeting is at 5: everyone should attend the review.",
	}
	for _, sentence := range prose {
		if got := Convert(sentence); strings.Contains(got, "```") {
			t.Errorf("prose became code: %q ->\n%s", sentence, got)
		}
	}
}

func TestDetectLanguageStaysEmptyWhenUnsure(t *testing.T) {
	if got := detectLanguage("a b c\nd e f"); got != "" {
		t.Errorf("detectLanguage() = %q, want empty", got)
	}
}

func TestDetectedSnippetKeepsIndentation(t *testing.T) {
	got := Convert("def main():\n    if True:\n        print(\"hi\")")
	for _, want := range []string{"\n    if True:", "\n        print("} {
		if !strings.Contains(got, want) {
			t.Errorf("indentation %q lost from:\n%s", want, got)
		}
	}
}

func TestDedentRemovesSharedIndentOnly(t *testing.T) {
	got := dedent("    def main():\n        return 1\n\n    x = 2")
	want := "def main():\n    return 1\n\nx = 2"
	if got != want {
		t.Errorf("dedent() = %q, want %q", got, want)
	}
}

func TestMultilineHTMLBecomesCodeBlock(t *testing.T) {
	got := Convert("<html>\n<body>\n<div class=\"x\">hi</div>\n</body>\n</html>")
	if !strings.HasPrefix(got, "```html\n") {
		t.Errorf("HTML block was not fenced:\n%s", got)
	}
}

func TestInlineHTMLStaysText(t *testing.T) {
	if got := Convert("<strong>unsafe</strong>"); strings.Contains(got, "```") {
		t.Errorf("inline HTML became a code block:\n%s", got)
	}
}

func TestRepairFencesDropsOrphanWhenDocumentFollows(t *testing.T) {
	// The sample's first language fence is left open: CommonMark would treat
	// every following heading as code, which is exactly the failure the user
	// reported. Dropping the orphan restores the rest of the document.
	raw, err := os.ReadFile("../testdata/everything.md")
	if err != nil {
		t.Fatalf("reading sample: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	var mangled []string
	dropped := false
	for i, line := range lines {
		if !dropped && line == "```" && i > 0 && strings.Contains(lines[i-1], "greet('Telegram')") {
			dropped = true
			continue
		}
		mangled = append(mangled, line)
	}
	if !dropped {
		t.Fatal("test setup failed to drop a closing fence")
	}

	got := Convert(strings.Join(mangled, "\n"))
	for _, want := range []string{
		"*Fenced without language*",
		"*Indented code block*",
		"*10\\. Horizontal rules*",
		"*11\\. Escaping and punctuation*",
		"*12\\. Mermaid diagrams*",
		"*13\\. Unicode and emoji*",
		"```js\nconst greet",
		"```python\ndef main",
		"```go\nfunc main",
		"```sql\nSELECT",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("repaired conversion is missing %q", want)
		}
	}
	if strings.Contains(got, "\\`\\`\\`python") {
		t.Errorf("python fence was swallowed into the previous block:\n%s", got)
	}
	if strings.Contains(got, "### Fenced") {
		t.Errorf("raw markdown headings leaked into the output:\n%s", got)
	}
}

func TestRepairFencesLeavesGenuineUnclosedSource(t *testing.T) {
	// A snippet that really is just unterminated source must keep its opening
	// fence; dropping it would turn code into escaped prose.
	got := Convert("```js\nvar x = 1;")
	if !strings.HasPrefix(got, "```js\n") {
		t.Errorf("genuine source lost its fence:\n%s", got)
	}
}

func TestRepairFencesIsIdempotent(t *testing.T) {
	raw, err := os.ReadFile("../testdata/everything.md")
	if err != nil {
		t.Fatalf("reading sample: %v", err)
	}
	once := repairFences(string(raw))
	twice := repairFences(once)
	if once != twice {
		t.Error("repairFences is not idempotent on a well-formed document")
	}
	if once != string(raw) {
		t.Error("repairFences mutated a well-formed document")
	}
}
