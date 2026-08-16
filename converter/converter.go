// Package converter turns standard Markdown (CommonMark-ish) into text
// formatted for Telegram's MarkdownV2 parse mode.
//
// Telegram's MarkdownV2 is NOT the same as normal Markdown:
//   - Bold is *single asterisks*, not **double**.
//   - Italic is _single underscores_.
//   - There are no headers, tables, images-as-images, or <hr>; those get
//     converted to the closest Telegram-friendly equivalent.
//   - A long list of characters must be backslash-escaped anywhere they
//     appear as literal text: _ * [ ] ( ) ~ ` > # + - = | { } . ! and \
//
// This package walks the input line by line for block-level constructs
// (headers, lists, blockquotes, code fences, tables, horizontal rules)
// and does a placeholder-based inline pass for span-level constructs
// (bold, italic, strikethrough, inline code, links, images).
package converter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// reservedChars are the characters MarkdownV2 requires to be escaped
// wherever they appear as literal (non-formatting) text.
const reservedChars = "_*[]()~`>#+-=|{}.!\\"

// Convert transforms markdown into Telegram MarkdownV2-ready text.
func Convert(md string) string {
	// Normalize line endings.
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")

	var out []string

	inCode := false
	codeLang := ""
	var codeBuf []string

	inTable := false
	var tableBuf []string

	flushTable := func() {
		if len(tableBuf) > 0 {
			if rendered := renderTable(tableBuf); rendered != "" {
				out = append(out, rendered)
			}
		}
		tableBuf = nil
		inTable = false
	}

	fenceRe := regexp.MustCompile("^```\\s*([a-zA-Z0-9_+-]*)\\s*$")
	headerRe := regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	hrRe := regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	quoteRe := regexp.MustCompile(`^>\s?(.*)$`)
	ulRe := regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	olRe := regexp.MustCompile(`^(\s*)(\d+)[.)]\s+(.*)$`)

	for _, raw := range lines {
		line := raw
		trimmed := strings.TrimSpace(line)

		// --- code fences ---
		if m := fenceRe.FindStringSubmatch(trimmed); m != nil {
			if !inCode {
				inCode = true
				codeLang = m[1]
				codeBuf = nil
			} else {
				inCode = false
				out = append(out, renderCodeBlock(codeLang, codeBuf))
			}
			continue
		}
		if inCode {
			codeBuf = append(codeBuf, line)
			continue
		}

		// --- tables (must contain a pipe) ---
		if strings.Contains(trimmed, "|") && looksLikeTableRow(trimmed) {
			inTable = true
			tableBuf = append(tableBuf, trimmed)
			continue
		} else if inTable {
			flushTable()
		}

		// --- horizontal rule ---
		if hrRe.MatchString(trimmed) {
			out = append(out, "──────────")
			continue
		}

		// --- headers -> bold line ---
		if m := headerRe.FindStringSubmatch(trimmed); m != nil {
			content := inlineConvert(m[2])
			out = append(out, "*"+content+"*")
			continue
		}

		// --- blockquote ---
		if m := quoteRe.FindStringSubmatch(trimmed); m != nil {
			out = append(out, "> "+inlineConvert(m[1]))
			continue
		}

		// --- unordered list ---
		if m := ulRe.FindStringSubmatch(line); m != nil {
			indent := m[1]
			content := inlineConvert(m[2])
			out = append(out, indent+"• "+content)
			continue
		}

		// --- ordered list ---
		if m := olRe.FindStringSubmatch(line); m != nil {
			indent, num, content := m[1], m[2], inlineConvert(m[3])
			out = append(out, indent+num+"\\. "+content)
			continue
		}

		if trimmed == "" {
			out = append(out, "")
			continue
		}

		// --- plain paragraph line ---
		out = append(out, inlineConvert(line))
	}

	if inTable {
		flushTable()
	}
	if inCode {
		// Unterminated fence: close it gracefully.
		out = append(out, renderCodeBlock(codeLang, codeBuf))
	}

	return strings.Join(out, "\n")
}

// ---------- inline (span-level) handling ----------

var (
	codeSpanRe  = regexp.MustCompile("`([^`]+)`")
	imageRe     = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	linkRe      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	boldRe      = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	strikeRe    = regexp.MustCompile(`~~([^~]+)~~`)
	italicRe    = regexp.MustCompile(`\*([^*]+)\*|_([^_]+)_`)
	placeholdRe = regexp.MustCompile("\x00(\\d+)\x00")
)

// inlineConvert applies span-level markdown -> MarkdownV2 conversion and
// escapes any remaining literal text.
func inlineConvert(s string) string {
	var placeholders []string
	store := func(rendered string) string {
		placeholders = append(placeholders, rendered)
		return fmt.Sprintf("\x00%d\x00", len(placeholders)-1)
	}

	// 1. Protect inline code spans first so nothing inside them gets
	//    reinterpreted as formatting.
	s = codeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := codeSpanRe.FindStringSubmatch(m)[1]
		return store("`" + escapeCode(inner) + "`")
	})

	// 2. Images -> "📷 alt" link.
	s = imageRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := imageRe.FindStringSubmatch(m)
		alt, url := sub[1], sub[2]
		if alt == "" {
			alt = "image"
		}
		return store("[📷 " + escapeText(alt) + "](" + escapeURL(url) + ")")
	})

	// 3. Links.
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		text, url := sub[1], sub[2]
		return store("[" + escapeText(text) + "](" + escapeURL(url) + ")")
	})

	// 4. Bold (**x** or __x__) — must run before italic.
	s = boldRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := boldRe.FindStringSubmatch(m)
		inner := sub[1]
		if inner == "" {
			inner = sub[2]
		}
		return store("*" + escapeText(inner) + "*")
	})

	// 5. Strikethrough.
	s = strikeRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := strikeRe.FindStringSubmatch(m)[1]
		return store("~" + escapeText(inner) + "~")
	})

	// 6. Italic (*x* or _x_).
	s = italicRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := italicRe.FindStringSubmatch(m)
		inner := sub[1]
		if inner == "" {
			inner = sub[2]
		}
		return store("_" + escapeText(inner) + "_")
	})

	// 7. Escape whatever plain text remains.
	s = escapeText(s)

	// 8. Restore placeholders (already-rendered, already-escaped segments).
	s = placeholdRe.ReplaceAllStringFunc(s, func(m string) string {
		idx, _ := strconv.Atoi(placeholdRe.FindStringSubmatch(m)[1])
		return placeholders[idx]
	})

	return s
}

// escapeText escapes every MarkdownV2 reserved character in plain text.
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if strings.ContainsRune(reservedChars, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeCode escapes text that will live inside a `code` span or ```block```,
// where only backslash and backtick are special.
func escapeCode(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// escapeURL escapes text inside a link's (url), where only backslash and
// close-paren are special.
func escapeURL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

func renderCodeBlock(lang string, lines []string) string {
	content := escapeCode(strings.Join(lines, "\n"))
	return "```" + lang + "\n" + content + "\n```"
}

// ---------- tables ----------

var tableSepRe = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)

func looksLikeTableRow(s string) bool {
	// A real row has at least one pipe not at the very edges being the
	// only content, and isn't a lone stray "|" in prose. Heuristic: at
	// least 2 pipe-separated cells.
	trimmed := strings.Trim(s, "|")
	return strings.Contains(trimmed, "|") || tableSepRe.MatchString(s)
}

func isTableSeparator(s string) bool {
	return tableSepRe.MatchString(s)
}

// renderTable renders buffered "| a | b |" rows as a fixed-width block
// wrapped in a MarkdownV2 pre block, since Telegram has no native tables.
func renderTable(rows []string) string {
	var parsed [][]string
	for _, r := range rows {
		if isTableSeparator(r) {
			continue
		}
		cells := strings.Split(strings.Trim(r, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		parsed = append(parsed, cells)
	}
	if len(parsed) == 0 {
		return ""
	}

	numCols := 0
	for _, row := range parsed {
		if len(row) > numCols {
			numCols = len(row)
		}
	}
	widths := make([]int, numCols)
	for _, row := range parsed {
		for i, c := range row {
			if len([]rune(c)) > widths[i] {
				widths[i] = len([]rune(c))
			}
		}
	}

	var b strings.Builder
	for _, row := range parsed {
		for i := 0; i < numCols; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteString(padRight(cell, widths[i]))
			if i < numCols-1 {
				b.WriteString(" | ")
			}
		}
		b.WriteString("\n")
	}

	content := escapeCode(strings.TrimRight(b.String(), "\n"))
	return "```\n" + content + "\n```"
}

func padRight(s string, width int) string {
	diff := width - len([]rune(s))
	if diff <= 0 {
		return s
	}
	return s + strings.Repeat(" ", diff)
}
