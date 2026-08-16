// Package converter turns CommonMark Markdown into Telegram MarkdownV2.
package converter

import (
	"strings"
	"unicode/utf8"
)

// Split breaks already-converted MarkdownV2 into pieces of at most limit
// runes, cutting only at block boundaries so no piece ends inside a code
// fence, a link, or an emphasis entity. Telegram parses every message
// independently, so a naive cut would leave unbalanced entities and be
// rejected with "can't parse entities".
func Split(text string, limit int) []string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}

	var chunks []string
	var current []string
	currentLen := 0

	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, strings.Join(current, "\n\n"))
			current = nil
			currentLen = 0
		}
	}

	for _, block := range splitBlocks(text) {
		for _, piece := range fitBlock(block, limit) {
			size := utf8.RuneCountInString(piece)
			separator := 0
			if len(current) > 0 {
				separator = 2
			}
			if currentLen+separator+size > limit {
				flush()
				separator = 0
			}
			current = append(current, piece)
			currentLen += separator + size
		}
	}
	flush()

	return chunks
}

// splitBlocks divides text into top-level blocks: runs of consecutive lines,
// with each fenced code block kept whole as a single block.
func splitBlocks(text string) []string {
	var blocks []string
	var current []string

	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
	}

	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "```"):
			flush()
			fence := []string{line}
			for i++; i < len(lines); i++ {
				fence = append(fence, lines[i])
				if strings.HasPrefix(lines[i], "```") {
					break
				}
			}
			blocks = append(blocks, strings.Join(fence, "\n"))
		case strings.TrimSpace(line) == "":
			flush()
		default:
			current = append(current, line)
		}
	}
	flush()

	return blocks
}

// fitBlock breaks a single oversized block into limit-sized pieces, repeating
// the ``` fence around each piece of a code block so every piece stays valid.
func fitBlock(block string, limit int) []string {
	if utf8.RuneCountInString(block) <= limit {
		return []string{block}
	}

	lines := strings.Split(block, "\n")
	if strings.HasPrefix(block, "```") && len(lines) > 2 {
		opening := lines[0]
		body := lines[1 : len(lines)-1]
		// Each piece pays for the opening fence, the closing fence, and the
		// two newlines that attach them to the body.
		room := limit - utf8.RuneCountInString(opening) - len("\n```") - 1
		var pieces []string
		for _, group := range groupLines(body, room) {
			pieces = append(pieces, opening+"\n"+group+"\n```")
		}
		return pieces
	}

	return groupLines(lines, limit)
}

// groupLines packs whole lines into limit-sized groups, falling back to a hard
// rune cut only for a single line that cannot fit on its own.
func groupLines(lines []string, limit int) []string {
	var groups []string
	var current []string
	currentLen := 0

	flush := func() {
		if len(current) > 0 {
			groups = append(groups, strings.Join(current, "\n"))
			current = nil
			currentLen = 0
		}
	}

	for _, line := range lines {
		size := utf8.RuneCountInString(line)
		if size > limit {
			flush()
			groups = append(groups, splitLine(line, limit)...)
			continue
		}
		separator := 0
		if len(current) > 0 {
			separator = 1
		}
		if currentLen+separator+size > limit {
			flush()
			separator = 0
		}
		current = append(current, line)
		currentLen += separator + size
	}
	flush()

	return groups
}

// splitLine is the last resort for a single line longer than the limit. It cuts
// only where MarkdownV2 allows: never inside a link, a code span, or a
// backslash escape. Emphasis that is still open at the cut is closed at the end
// of the piece and reopened at the start of the next one.
func splitLine(line string, limit int) []string {
	runes := []rune(line)
	var parts []string
	reopen := ""

	for len(runes) > 0 {
		budget := limit - utf8.RuneCountInString(reopen)
		if budget < 1 {
			budget = 1
		}
		if len(runes) <= budget {
			parts = append(parts, reopen+string(runes))
			break
		}

		end := safeCut(runes, budget)
		closing := closeMarkers(openMarkers(reopen + string(runes[:end])))
		// The closing markers have to fit inside the limit too.
		if shrunk := budget - utf8.RuneCountInString(closing); shrunk > 0 && shrunk < end {
			end = safeCut(runes, shrunk)
			closing = closeMarkers(openMarkers(reopen + string(runes[:end])))
		}
		if end == 0 {
			end = budget
			closing = ""
		}

		parts = append(parts, reopen+string(runes[:end])+closing)
		reopen = reverse(closing)
		runes = runes[end:]
	}

	return parts
}

// safeCut returns the largest index at or below budget where the line may be
// cut, preferring a space boundary.
func safeCut(runes []rune, budget int) int {
	mask := cutMask(runes)
	if budget >= len(runes) {
		budget = len(runes)
	}
	best := 0
	for i := budget; i > 0; i-- {
		if !mask[i] {
			continue
		}
		if best == 0 {
			best = i
		}
		if runes[i-1] == ' ' {
			return i
		}
	}
	return best
}

// cutMask marks every index where a cut would land inside a construct that
// must stay intact: an escape pair, a code span, or a link.
func cutMask(runes []rune) []bool {
	mask := make([]bool, len(runes)+1)
	for i := range mask {
		mask[i] = true
	}
	block := func(start, end int) {
		for i := start + 1; i <= end && i < len(mask); i++ {
			mask[i] = false
		}
	}

	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			block(i, i+1)
			i++
		case '`':
			end := scanUntil(runes, i+1, '`')
			block(i, end)
			i = end
		case '[':
			end := scanUntil(runes, i+1, ')')
			block(i, end)
			i = end
		}
	}
	return mask
}

// scanUntil returns the index of the next unescaped stop rune, or the last
// index when the construct is never closed.
func scanUntil(runes []rune, from int, stop rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == '\\' {
			i++
			continue
		}
		if runes[i] == stop {
			return i
		}
	}
	return len(runes) - 1
}

// openMarkers returns the emphasis markers still open at the end of s, in the
// order they were opened.
func openMarkers(s string) []rune {
	runes := []rune(s)
	var open []rune
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\\':
			i++
		case '`':
			i = scanUntil(runes, i+1, '`')
		case '*', '_', '~':
			if len(open) > 0 && open[len(open)-1] == runes[i] {
				open = open[:len(open)-1]
			} else {
				open = append(open, runes[i])
			}
		}
	}
	return open
}

// closeMarkers renders the sequence that closes the open markers, innermost
// first.
func closeMarkers(open []rune) string {
	var out strings.Builder
	for i := len(open) - 1; i >= 0; i-- {
		out.WriteRune(open[i])
	}
	return out.String()
}

func reverse(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
