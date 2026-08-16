package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"telegram-md-bot/converter"
	"telegram-md-bot/telegram"
)

// validateMarkdownV2 mimics Telegram's entity parser closely enough to catch
// the failures it reports in practice: an unescaped reserved character in plain
// text, and an entity that is never closed.
func validateMarkdownV2(s string) error {
	const reserved = "_*[]()~`>#+-=|{}.!"
	runes := []rune(s)
	var open []string
	atLineStart := true

	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\\':
			i++
			atLineStart = false
		case c == '\n':
			atLineStart = true
		case strings.HasPrefix(string(runes[i:]), "```"):
			j := i + 3
			for ; j < len(runes); j++ {
				if runes[j] == '\\' {
					j++
					continue
				}
				if strings.HasPrefix(string(runes[j:]), "```") {
					break
				}
			}
			if j >= len(runes) {
				return fmt.Errorf("unclosed ``` block at rune %d", i)
			}
			i = j + 2
			atLineStart = false
		case c == '`':
			j := i + 1
			for ; j < len(runes); j++ {
				if runes[j] == '\\' {
					j++
					continue
				}
				if runes[j] == '`' {
					break
				}
			}
			if j >= len(runes) {
				return fmt.Errorf("unclosed code span at rune %d", i)
			}
			i = j
			atLineStart = false
		case c == '*' || c == '_' || c == '~':
			marker := string(c)
			if len(open) > 0 && open[len(open)-1] == marker {
				open = open[:len(open)-1]
			} else {
				open = append(open, marker)
			}
			atLineStart = false
		case c == ']':
			if i+1 >= len(runes) || runes[i+1] != '(' {
				return fmt.Errorf("stray ] at rune %d", i)
			}
			j := i + 2
			for ; j < len(runes); j++ {
				if runes[j] == '\\' {
					j++
					continue
				}
				if runes[j] == ')' {
					break
				}
			}
			if j >= len(runes) {
				return fmt.Errorf("unclosed link target at rune %d", i)
			}
			i = j
			atLineStart = false
		case c == '>' && atLineStart:
		case c == '[':
			atLineStart = false
		case strings.ContainsRune(reserved, c):
			return fmt.Errorf("unescaped %q at rune %d in line %q", string(c), i, lineAround(s, i))
		default:
			atLineStart = false
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("unclosed entities %v", open)
	}
	return nil
}

func lineAround(s string, runeIndex int) string {
	runes := []rune(s)
	start, end := runeIndex, runeIndex
	for start > 0 && runes[start-1] != '\n' {
		start--
	}
	for end < len(runes) && runes[end] != '\n' {
		end++
	}
	return string(runes[start:end])
}

func TestCodeBlockPipeline(t *testing.T) {
	tests := []struct {
		name string
		md   string
		lang string // language tag expected in the sent message
	}{
		{"js template literal", "```js\nconst greet = (name) => {\n  console.log(`Hello, ${name}`);\n};\n```", "js"},
		{"python indented", "```python\ndef main():\n    if True:\n        print(\"hi\")\n```", "python"},
		{"go tabs", "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```", "go"},
		{"json", "```json\n{\n  \"a\": [1, 2],\n  \"b\": {\"c\": true}\n}\n```", "json"},
		{"html", "```html\n<div class=\"x\">a &amp; b</div>\n```", "html"},
		{"diff", "```diff\n- old line\n+ new line\n```", "diff"},
		{"sql", "```sql\nSELECT * FROM t WHERE a = 'b';\n```", "sql"},
		{"lang with plus", "```c++\nint main() { return 0; }\n```", "c++"},
		{"lang with hash", "```C#\nvar x = 1;\n```", "C#"},
		{"lang uppercase", "```JavaScript\nvar x = 1;\n```", "JavaScript"},
		{"shell", "```sh\n$ echo \"a|b\" > file.txt\n```", "sh"},
		{"blank line inside", "```py\na = 1\n\nb = 2\n```", "py"},
		{"nested fence", "````md\n```js\nx\n```\n````", "md"},
		{"tilde fence", "~~~js\nvar x = 1;\n~~~", "js"},
		{"unterminated fence", "```js\nvar x = 1;", "js"},
		{"info trailing spaces", "```js   \nvar x = 1;\n```", "js"},
		{"info with attributes", "```js {1,3}\nvar x = 1;\n```", "js"},
		{"fence after paragraph", "text right above\n```js\nvar x = 1;\n```", "js"},
		{"fence in list", "* Install:\n\n  ```bash\n  go install ./...\n  ```\n\n* Done", "bash"},
		{"fence in nested list", "1. one\n   * inner\n\n     ```go\n     x := 1\n     ```", "go"},
		{"fence in blockquote", "> quoted:\n>\n> ```go\n> x := 1\n> ```", "go"},
		{"backticks inside code", "```md\nuse `code` here\n```", "md"},
		{"backslash inside code", "```go\nre := \"a\\\\b\\n\"\n```", "go"},
		{"heading then fence", "## Blocks of code\n\n```js\nlet a = 1;\n```", "js"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []recordedCall
			api := telegramServer(t, &calls)
			defer api.Close()

			bot := telegram.NewWithAPIBase(api.URL, "token")
			handleMessage(context.Background(), bot, &telegram.Message{
				Text: test.md,
				Chat: telegram.Chat{ID: 1},
			})

			if len(calls) != 1 {
				t.Fatalf("got %d messages, want 1: %#v", len(calls), calls)
			}
			sent := calls[0].text
			if calls[0].mode != "MarkdownV2" {
				t.Errorf("parse_mode = %q, want MarkdownV2", calls[0].mode)
			}
			if err := validateMarkdownV2(sent); err != nil {
				t.Errorf("invalid MarkdownV2: %v\nsent:\n%s", err, sent)
			}
			if strings.Count(sent, "```")%2 != 0 {
				t.Errorf("unbalanced fences:\n%s", sent)
			}
			if !strings.Contains(sent, "```"+test.lang+"\n") {
				t.Errorf("language tag %q missing from:\n%s", test.lang, sent)
			}
			_ = converter.Convert(test.md)
		})
	}
}
