// Code snippet detection: markdown pasted from chat or docs often loses its
// ``` fences, so this file recognizes source that arrived as plain paragraphs
// and gives it back a fence, with a language tag when one can be inferred.
package converter

import (
	"regexp"
	"strings"
)

var (
	// Declarations that essentially never begin a line of English prose.
	codeKeywordRe = regexp.MustCompile(`^(let|const|var|function|func|def|class|struct|interface|enum|impl|trait|module|namespace|import|from|export|package|public|private|protected|static|final|return|yield|await|async|echo|print|printf|println|console\.log|System\.out|#include|#define|#!|using|SELECT|INSERT INTO|UPDATE|DELETE FROM|CREATE TABLE|ALTER TABLE|DROP TABLE)\b`)
	// Punctuation that separates a keyword used as code from the same word
	// opening an English sentence ("return null;" versus "return to the menu").
	codePunctRe = regexp.MustCompile(`[(={]|[;:{]$`)
	// Operators that only appear in source, never in prose.
	codeOperatorRe = regexp.MustCompile(`^[\w$@][\w$.\[\]]*\s*(:=|\+=|-=|\*=|/=|\?\?=|\|\|=|==|=>|->|<-)\s*\S`)
	// A plain assignment such as "message = 'hi'".
	codeAssignRe = regexp.MustCompile(`^[\w$@][\w$.\[\]"']*\s*=\s*\S`)
	// A bare call such as "alert(message);" or "obj.method(a, b)".
	codeCallRe = regexp.MustCompile(`^[\w$][\w$.:]*\(.*\)\s*[;,]?$`)
	// Closing or opening block punctuation on its own line.
	codeBraceRe = regexp.MustCompile(`^[}\])]+[;,]?$|^[{[(]$`)
	// A block opener that ends in a colon, as in Python or YAML nesting.
	codeBlockOpenRe = regexp.MustCompile(`^(if|elif|else|for|while|try|except|finally|with|switch|case|do|match)\b.*:$`)
	// A comment line in any of the common syntaxes.
	codeCommentRe = regexp.MustCompile(`^(//|/\*|\*/|#\s|--\s|;;)`)
	// A line holding nothing but a markup tag, as in hand-written HTML.
	codeTagRe = regexp.MustCompile(`^</?!?[a-zA-Z][\w:-]*(\s[^<>]*)?/?>$`)
	// A tag with content around it, which also occurs in prose, so it only
	// counts as a hint.
	codeInlineTagRe = regexp.MustCompile(`^</?!?[a-zA-Z][\w:-]*(\s[^<>]*)?/?>`)
	// A shell command line, with or without a prompt.
	codeShellRe = regexp.MustCompile(`^(\$\s+\S|>\s*\$|sudo\s|apt(-get)?\s|brew\s|npm\s|npx\s|yarn\s|pnpm\s|pip3?\s|go\s+(run|build|test|get|install|mod)\b|git\s|docker(\s|-compose)|kubectl\s|curl\s|wget\s|make\s|cd\s|ls\s|mkdir\s|rm\s|cp\s|mv\s|chmod\s|export\s+\w+=)`)
	// A "key: value" line as found in YAML, or a CSS declaration.
	codeKeyValueRe = regexp.MustCompile(`^[\w."'$-]+\s*:\s+\S|^\s*[\w-]+\s*:\s*[^ ].*;$`)
	// A field or entry line inside a JSON/dict literal.
	codeEntryRe = regexp.MustCompile(`^"[^"]+"\s*:|^'[^']+'\s*:`)
	// A line ending in an HTML character reference, whose semicolon is part
	// of the entity rather than a statement terminator.
	entitySuffixRe = regexp.MustCompile(`&[#\w]+;$`)
)

// fenceLineRe matches a ``` or ~~~ fence, capturing the marker and info string.
var fenceLineRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// markdownHeadingRe matches a heading of level two or more, which is the
// clearest sign that text is a document rather than source. A single "#" is
// left out because it is also a comment in shell and Python.
var markdownHeadingRe = regexp.MustCompile(`^ {0,3}#{2,6}\s+\S`)

// repairFences fixes input whose ``` fences do not pair up. CommonMark lets an
// unclosed fence run until a bare closer or the end of the document, so a
// missing line turns every following language-tagged fence, heading, and
// diagram into literal code. The repair closes a fence early when the next
// line is clearly document structure (a new ```lang fence or a ## heading),
// and drops a trailing orphan only when the remaining lines look like a
// document rather than source.
func repairFences(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	open := -1
	marker := ""

	flushOpen := func(before int) {
		if open < 0 {
			return
		}
		// Insert a closer immediately before the structural line, so the
		// orphaned body stays a code block and the structure is free to parse.
		out = append(out, marker)
		open = -1
		_ = before
	}

	for i, line := range lines {
		if match := fenceLineRe.FindStringSubmatch(line); match != nil {
			tag := match[1]
			info := strings.TrimSpace(match[2])
			if open < 0 {
				open, marker = len(out), tag
				out = append(out, line)
				continue
			}
			if tag[0] == marker[0] && len(tag) >= len(marker) && info == "" {
				// Valid closer for the open fence.
				out = append(out, line)
				open = -1
				continue
			}
			if info != "" {
				// A language-tagged fence while one is already open: the
				// previous fence is missing its closer.
				flushOpen(i)
				open, marker = len(out), tag
				out = append(out, line)
				continue
			}
			// Same-character fence of equal length with no info is already
			// handled above; anything else (different character) opens a new
			// fence after closing the previous one.
			flushOpen(i)
			open, marker = len(out), tag
			out = append(out, line)
			continue
		}

		if open >= 0 && markdownHeadingRe.MatchString(line) {
			flushOpen(i)
		}
		out = append(out, line)
	}

	if open >= 0 && holdsDocumentStructure(out[open+1:]) {
		// Trailing orphan with document structure after it: drop the marker
		// so the remaining lines parse as markdown instead of code.
		copy(out[open:], out[open+1:])
		out = out[:len(out)-1]
	}

	return strings.Join(out, "\n")
}

// holdsDocumentStructure reports whether lines read as markdown prose rather
// than as the body of a code block.
func holdsDocumentStructure(lines []string) bool {
	headings := 0
	for _, line := range lines {
		if markdownHeadingRe.MatchString(line) {
			headings++
			if headings >= 2 {
				return true
			}
		}
	}
	return false
}

// looksLikeCode reports whether a paragraph is really a source snippet that was
// pasted without a ``` fence. It requires a majority of code-like lines plus at
// least one unambiguous signal, so ordinary prose is never reformatted.
func looksLikeCode(paragraph string) bool {
	coded, total, strong := codeScore(paragraph)
	return strong && total > 0 && coded*10 >= total*7
}

// looksLikeCodeFragment is the laxer test used for a paragraph next to one that
// already qualified. A snippet separated by a blank line ("package main" above
// a function body) parses as its own paragraph, and judged alone it would look
// like prose.
func looksLikeCodeFragment(paragraph string) bool {
	coded, total, _ := codeScore(paragraph)
	return total > 0 && coded*10 >= total*7
}

// codeScore counts how many non-blank lines look like source, and whether any
// of them is an unambiguous signal rather than a weak hint.
func codeScore(paragraph string) (coded, total int, strong bool) {
	for _, raw := range strings.Split(paragraph, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		total++
		// An English sentence can start with a keyword or end in a
		// semicolon, so a wordy line never counts as code.
		if isProseLine(line) {
			continue
		}
		switch {
		case strings.HasSuffix(line, ";") && !entitySuffixRe.MatchString(line),
			strings.HasSuffix(line, "{"),
			strings.HasSuffix(line, "=>"), codeBraceRe.MatchString(line),
			codeOperatorRe.MatchString(line), codeTagRe.MatchString(line),
			codeShellRe.MatchString(line), codeEntryRe.MatchString(line),
			codeBlockOpenRe.MatchString(line),
			codeKeywordRe.MatchString(line) && codePunctRe.MatchString(line):
			coded++
			strong = true
		case codeAssignRe.MatchString(line), codeCallRe.MatchString(line),
			codeCommentRe.MatchString(line), codeKeyValueRe.MatchString(line),
			codeInlineTagRe.MatchString(line), codeKeywordRe.MatchString(line):
			coded++
		}
	}
	return coded, total, strong
}

// isProseLine reports whether a line reads as a sentence: several words with
// none of the punctuation that source code is built from.
func isProseLine(line string) bool {
	if strings.ContainsAny(strings.TrimSuffix(line, ";"), "(){}[]<>=|\"") {
		return false
	}
	return len(strings.Fields(line)) >= 5
}

// codeFence wraps a detected snippet in a Telegram code block, tagging it with
// a language when one is recognizable so clients can highlight it.
func codeFence(snippet string) string {
	snippet = dedent(snippet)
	return "```" + detectLanguage(snippet) + "\n" + escapeCode(snippet) + "\n```"
}

// dedent removes the indentation shared by every line, which is what a snippet
// picks up when it is pasted inside a list item or a quote. Indentation
// relative to that baseline is what carries meaning in Python and friends, and
// it is preserved.
func dedent(snippet string) string {
	lines := strings.Split(snippet, "\n")
	common := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if indent := len(line) - len(trimmed); common < 0 || indent < common {
			common = indent
		}
	}
	if common <= 0 {
		return snippet
	}
	for i, line := range lines {
		if len(line) >= common {
			lines[i] = line[common:]
		} else {
			lines[i] = strings.TrimLeft(line, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

// languageSignals maps a language tag to the patterns that identify it.
// A "signature" is decisive on its own; "hints" are shared with other
// languages and only count when two of them agree. Order matters, since the
// first language with a signature wins.
var languageSignals = []struct {
	name       string
	signatures []*regexp.Regexp
	hints      []*regexp.Regexp
}{
	{"go",
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^package\s+\w+$`),
			regexp.MustCompile(`(?m)^func\s+\w*\s*\(`),
			regexp.MustCompile(`:=`),
			regexp.MustCompile(`fmt\.[A-Z]\w*\(`),
		},
		nil,
	},
	{"python",
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*def\s+\w+\s*\(.*\)\s*:`),
			regexp.MustCompile(`(?m)^\s*(elif|except|__name__)\b`),
			regexp.MustCompile(`(?m)^\s*from\s+[\w.]+\s+import\s+`),
		},
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*import\s+[\w.]+$`),
			regexp.MustCompile(`(?m)^\s*(if|for|while|class|with|try|else)\b.*:$`),
			regexp.MustCompile(`\bprint\(`),
		},
	},
	{"js",
		[]*regexp.Regexp{
			regexp.MustCompile(`console\.log\(`),
			regexp.MustCompile(`=>`),
			regexp.MustCompile(`\b(const|let)\s+\w+\s*=`),
			regexp.MustCompile(`\bdocument\.\w+`),
		},
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*function\s+\w*\s*\(`),
			regexp.MustCompile(`(?m);$`),
		},
	},
	{"java",
		[]*regexp.Regexp{
			regexp.MustCompile(`System\.out\.print`),
			regexp.MustCompile(`public\s+(static|class)\b`),
		},
		nil,
	},
	{"sql",
		[]*regexp.Regexp{
			regexp.MustCompile(`(?is)\bselect\b.*\bfrom\b`),
			regexp.MustCompile(`(?im)^\s*(insert into|update|delete from|create table|alter table|drop table)\b`),
		},
		nil,
	},
	{"html",
		[]*regexp.Regexp{
			regexp.MustCompile(`(?i)<(!doctype|html|body|head|script|div|span|table|ul|form)\b`),
		},
		nil,
	},
	{"json",
		nil,
		[]*regexp.Regexp{
			regexp.MustCompile(`(?s)^\s*[{[]`),
			regexp.MustCompile(`(?m)^\s*"[^"]+"\s*:\s*(".*"|-?\d|true|false|null|\[|\{)`),
		},
	},
	{"bash",
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^#!/(usr/)?bin/(env )?(ba|z)?sh`),
			regexp.MustCompile(`(?m)^\s*\$\s+\S`),
			regexp.MustCompile(`(?m)^\s*(sudo|apt|apt-get|brew|npm|npx|yarn|pnpm|pip3?|docker|docker-compose|kubectl|git|curl|wget|make|export|chmod)\s`),
		},
		nil,
	},
	{"css",
		nil,
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*[.#]?[\w-]+(\s*[,:>][^;{]*)?\s*\{`),
			regexp.MustCompile(`(?m)^\s*[\w-]+\s*:\s*[^;]+;`),
		},
	},
	{"yaml",
		nil,
		[]*regexp.Regexp{
			regexp.MustCompile(`(?m)^\s*-\s+[\w-]+:\s`),
			regexp.MustCompile(`(?m)^[\w-]+:\s+\S`),
		},
	},
}

// detectLanguage guesses the language of an unfenced snippet, returning an
// empty tag rather than a possibly wrong label when nothing is convincing.
func detectLanguage(snippet string) string {
	for _, language := range languageSignals {
		for _, signature := range language.signatures {
			if signature.MatchString(snippet) {
				return language.name
			}
		}
	}
	for _, language := range languageSignals {
		matched := 0
		for _, hint := range language.hints {
			if hint.MatchString(snippet) {
				matched++
			}
		}
		if matched >= 2 {
			return language.name
		}
	}
	return ""
}
