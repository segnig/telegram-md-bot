// Inline HTML handling: markdown often carries a few HTML tags, and Telegram
// has an equivalent entity for most of them. Translating is far friendlier
// than showing "<b\>bold tag</b\>" to the reader.
package converter

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	htmlTagRe     = regexp.MustCompile(`^<\s*(/?)\s*([a-zA-Z][\w:-]*)((?:\s[^<>]*)?)\s*/?>$`)
	htmlAnyTagRe  = regexp.MustCompile(`<[^<>]*>`)
	htmlHrefRe    = regexp.MustCompile(`(?i)href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	htmlSpoilerRe = regexp.MustCompile(`(?i)tg-spoiler`)
)

// htmlMarkers are the tags that map directly onto a MarkdownV2 delimiter.
var htmlMarkers = map[string]string{
	"b":          "*",
	"strong":     "*",
	"i":          "_",
	"em":         "_",
	"u":          "__",
	"ins":        "__",
	"s":          "~",
	"strike":     "~",
	"del":        "~",
	"tg-spoiler": "||",
}

// htmlEntity is one open tag awaiting its closing counterpart.
type htmlEntity struct {
	name   string
	closer string
}

// htmlState tracks the tags opened so far, so that they can be closed in the
// right order and any tag left dangling can still be closed at the end.
// Telegram rejects a message with an unbalanced entity outright.
type htmlState struct {
	open []htmlEntity
}

// inCode reports whether text now belongs to a code entity, where the escaping
// rules are different.
func (h *htmlState) inCode() bool {
	for _, entity := range h.open {
		if entity.name == "code" {
			return true
		}
	}
	return false
}

// translate converts a single tag, returning the text to emit. Tags without a
// Telegram equivalent are kept as literal, escaped text.
func (h *htmlState) translate(tag string) string {
	match := htmlTagRe.FindStringSubmatch(tag)
	if match == nil {
		return escapeText(tag)
	}
	closing := match[1] == "/"
	name := strings.ToLower(match[2])
	attributes := match[3]

	if name == "span" {
		// A span is only meaningful as Telegram's spoiler.
		if !closing && !htmlSpoilerRe.MatchString(attributes) {
			return ""
		}
		name = "tg-spoiler"
	}

	if closing {
		return h.close(name)
	}

	switch {
	case name == "br":
		return "\n"
	case name == "p" || name == "div":
		return ""
	case name == "code":
		h.push("code", "`")
		return "`"
	case name == "a":
		return h.openLink(attributes)
	}
	if marker, ok := htmlMarkers[name]; ok {
		// Nesting the same entity twice is not allowed, and reopening it
		// would only unbalance the message.
		if h.isOpen(name) {
			return ""
		}
		h.push(name, marker)
		return marker
	}
	return escapeText(tag)
}

func (h *htmlState) openLink(attributes string) string {
	match := htmlHrefRe.FindStringSubmatch(attributes)
	if match == nil {
		return ""
	}
	href := match[1] + match[2] + match[3]
	if !IsLinkableURL(href) {
		// Telegram rejects relative targets, so the text is kept plain.
		h.push("a", "")
		return ""
	}
	h.push("a", "]("+escapeURL(href)+")")
	return "["
}

// close emits the closers for the named tag, including any tags still open
// inside it, so entities never overlap.
func (h *htmlState) close(name string) string {
	index := -1
	for i := len(h.open) - 1; i >= 0; i-- {
		if h.open[i].name == name {
			index = i
			break
		}
	}
	if index < 0 {
		return ""
	}
	var out strings.Builder
	for i := len(h.open) - 1; i >= index; i-- {
		out.WriteString(h.open[i].closer)
	}
	h.open = h.open[:index]
	return out.String()
}

// closeAll closes everything still open, which is what keeps a message with a
// stray "<b>" from being rejected.
func (h *htmlState) closeAll() string {
	var out strings.Builder
	for i := len(h.open) - 1; i >= 0; i-- {
		out.WriteString(h.open[i].closer)
	}
	h.open = nil
	return out.String()
}

func (h *htmlState) push(name, closer string) {
	h.open = append(h.open, htmlEntity{name: name, closer: closer})
}

func (h *htmlState) isOpen(name string) bool {
	for _, entity := range h.open {
		if entity.name == name {
			return true
		}
	}
	return false
}

// translateHTMLString converts a run of HTML that arrived as one block, rather
// than as inline nodes inside a paragraph.
func translateHTMLString(raw string) string {
	state := &htmlState{}
	var out strings.Builder
	last := 0
	for _, position := range htmlAnyTagRe.FindAllStringIndex(raw, -1) {
		out.WriteString(escapeHTMLText(raw[last:position[0]], state))
		out.WriteString(state.translate(raw[position[0]:position[1]]))
		last = position[1]
	}
	out.WriteString(escapeHTMLText(raw[last:], state))
	out.WriteString(state.closeAll())
	return out.String()
}

func escapeHTMLText(text string, state *htmlState) string {
	if state.inCode() {
		return escapeCode(decodeEntities(text))
	}
	return escapeText(text)
}

// htmlEntities are the named references common enough to be worth decoding;
// anything else is left as written.
var htmlEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'",
	"nbsp": " ", "hellip": "…", "mdash": "—", "ndash": "–",
	"laquo": "«", "raquo": "»", "copy": "©", "reg": "®", "trade": "™",
	"times": "×", "divide": "÷", "deg": "°", "plusmn": "±", "middot": "·",
}

var entityRe = regexp.MustCompile(`&(#[0-9]+|#[xX][0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);`)

// decodeEntities turns HTML character references into the characters they
// stand for, so "&amp;" reaches Telegram as "&" instead of as markup noise.
func decodeEntities(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	return entityRe.ReplaceAllStringFunc(text, func(entity string) string {
		body := entity[1 : len(entity)-1]
		if strings.HasPrefix(body, "#") {
			digits, base := body[1:], 10
			if len(digits) > 0 && (digits[0] == 'x' || digits[0] == 'X') {
				digits, base = digits[1:], 16
			}
			code, err := strconv.ParseInt(digits, base, 32)
			if err != nil || code <= 0 || code > 0x10FFFF {
				return entity
			}
			return string(rune(code))
		}
		if decoded, ok := htmlEntities[strings.ToLower(body)]; ok {
			return decoded
		}
		return entity
	})
}
