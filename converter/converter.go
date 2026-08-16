// Package converter turns CommonMark Markdown into Telegram MarkdownV2.
package converter

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const reservedChars = "_*[]()~`>#+-=|{}.!\\"

// blankLineRe matches the blank lines that cannot occur inside a paragraph.
var blankLineRe = regexp.MustCompile(`\n{2,}`)

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// Image is a markdown image found while parsing input.
type Image struct {
	Alt string
	URL string
}

// Convert transforms CommonMark/GFM input into Telegram MarkdownV2-ready text.
func Convert(md string) string {
	return ConvertMermaid(md, nil)
}

// normalize prepares raw input for the parser: line endings are unified and an
// orphaned code fence is repaired, so every entry point sees the same document.
func normalize(md string) []byte {
	return []byte(repairFences(strings.ReplaceAll(md, "\r\n", "\n")))
}

// ConvertMermaid is like Convert, but replaces Mermaid diagrams that were
// successfully attached with a short placeholder naming the attached file.
// mermaidAttached is parallel to ExtractMermaid: true means that diagram was
// uploaded and should not appear as raw diagram source in the reply text.
func ConvertMermaid(md string, mermaidAttached []bool) string {
	source := normalize(md)
	doc := markdown.Parser().Parse(text.NewReader(source))
	seen := 0
	r := renderer{
		source:          source,
		mermaidAttached: mermaidAttached,
		mermaidSeen:     &seen,
	}
	return strings.TrimRight(r.renderBlocks(doc.FirstChild(), 0), "\n")
}

// MermaidAttachmentName is the filename used for the i-th successfully
// attached Mermaid diagram (0-based among attached diagrams only).
func MermaidAttachmentName(attachedIndex int) string {
	return fmt.Sprintf("mermaid-%d.jpg", attachedIndex+1)
}

// DefaultMermaidEndpoint renders a Mermaid diagram to an image from a
// base64url-encoded diagram source appended to the path.
const DefaultMermaidEndpoint = "https://mermaid.ink/img/"

// mermaidHeaderRe matches the first line of an unfenced Mermaid diagram, so
// diagrams pasted without a ```mermaid fence are still detected.
var mermaidHeaderRe = regexp.MustCompile(`^(?:(?:graph|flowchart)\s+(?:TB|TD|BT|RL|LR)\b|` +
	`(?:sequenceDiagram|classDiagram(?:-v2)?|stateDiagram(?:-v2)?|erDiagram|journey|gantt|pie|` +
	`gitGraph|mindmap|timeline|quadrantChart|requirementDiagram|C4Context|sankey-beta|xychart-beta|block-beta)\b)`)

// ExtractMermaid returns Mermaid diagram sources in document order, from
// ```mermaid fences and from paragraphs that begin with a Mermaid diagram
// declaration.
func ExtractMermaid(md string) []string {
	source := normalize(md)
	doc := markdown.Parser().Parse(text.NewReader(source))
	var diagrams []string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.FencedCodeBlock:
			if !strings.EqualFold(strings.TrimSpace(string(n.Language(source))), "mermaid") {
				return ast.WalkSkipChildren, nil
			}
			if diagram := strings.TrimSpace(segmentsText(n.Lines(), source)); diagram != "" {
				diagrams = append(diagrams, diagram)
			}
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph:
			diagram := strings.TrimSpace(segmentsText(n.Lines(), source))
			if diagram != "" && mermaidHeaderRe.MatchString(diagram) {
				diagrams = append(diagrams, diagram)
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return diagrams
}

// MermaidImageURL builds a rendering URL for a Mermaid diagram. An empty
// endpoint falls back to DefaultMermaidEndpoint.
func MermaidImageURL(endpoint, diagram string) string {
	if endpoint == "" {
		endpoint = DefaultMermaidEndpoint
	}
	if !strings.HasSuffix(endpoint, "/") {
		endpoint += "/"
	}
	return endpoint + base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(diagram)))
}

// ExtractImages returns markdown images that Telegram can fetch by URL, in
// document order. Relative or non-HTTP destinations are skipped because
// sendPhoto only accepts a public HTTP(S) URL or an uploaded file.
func ExtractImages(md string) []Image {
	source := normalize(md)
	doc := markdown.Parser().Parse(text.NewReader(source))
	var images []Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		img, ok := n.(*ast.Image)
		if !ok {
			return ast.WalkContinue, nil
		}
		destination := strings.TrimSpace(string(img.Destination))
		if !strings.HasPrefix(destination, "http://") && !strings.HasPrefix(destination, "https://") {
			return ast.WalkContinue, nil
		}
		if !IsLinkableURL(destination) {
			return ast.WalkContinue, nil
		}
		alt := plainText(img, source)
		if alt == "" {
			alt = "image"
		}
		images = append(images, Image{Alt: alt, URL: destination})
		return ast.WalkContinue, nil
	})
	return images
}

type renderer struct {
	source          []byte
	mermaidAttached []bool
	mermaidSeen     *int
}

func (r renderer) renderBlocks(node ast.Node, depth int) string {
	var out strings.Builder
	for n := node; n != nil; {
		if fence, next, ok := r.mergedCodeParagraphs(n); ok {
			out.WriteString(fence)
			out.WriteString("\n\n")
			n = next
			continue
		}
		out.WriteString(r.renderBlock(n, depth))
		n = n.NextSibling()
	}
	return out.String()
}

// mergedCodeParagraphs joins a run of paragraphs that together form one pasted
// snippet into a single code block. A blank line inside pasted source starts a
// new paragraph, so without this a snippet would come back as alternating code
// and prose. It returns the fence and the first node that was not consumed.
func (r renderer) mergedCodeParagraphs(node ast.Node) (string, ast.Node, bool) {
	first, ok := r.codeParagraphText(node)
	if !ok {
		return "", nil, false
	}

	parts := []string{first}
	qualified := looksLikeCode(first)
	next := node.NextSibling()
	for next != nil {
		text, ok := r.codeParagraphText(next)
		if !ok || !looksLikeCodeFragment(text) {
			break
		}
		parts = append(parts, text)
		qualified = qualified || looksLikeCode(text)
		next = next.NextSibling()
	}

	// At least one paragraph in the run has to be unambiguous source.
	if !qualified {
		return "", nil, false
	}
	return codeFence(strings.Join(parts, "\n\n")), next, true
}

// codeParagraphText returns the raw source of a paragraph that is a candidate
// for snippet detection, skipping Mermaid diagrams, which are handled
// separately.
func (r renderer) codeParagraphText(node ast.Node) (string, bool) {
	paragraph, ok := node.(*ast.Paragraph)
	if !ok || r.isMermaidNode(paragraph) {
		return "", false
	}
	text := strings.TrimRight(indentedSegmentsText(paragraph.Lines(), r.source), "\n")
	if !looksLikeCodeFragment(text) {
		return "", false
	}
	return text, true
}

// renderBlock renders a single block node, ignoring its siblings, so nested
// content such as a code block inside a list item can be rendered in place.
func (r renderer) renderBlock(node ast.Node, depth int) string {
	var out strings.Builder
	switch n := node.(type) {
	case *ast.Heading:
		out.WriteString("*")
		out.WriteString(r.renderInlineChildren(n))
		out.WriteString("*\n\n")
	case *ast.Paragraph, *ast.TextBlock:
		if placeholder, ok := r.mermaidPlaceholderIfAttached(n); ok {
			out.WriteString(placeholder)
			out.WriteString("\n\n")
			break
		}
		// A paragraph of unfenced source (a snippet pasted without ```) is
		// far more readable as a code block than as escaped prose.
		if raw := strings.TrimRight(indentedSegmentsText(n.Lines(), r.source), "\n"); looksLikeCode(raw) {
			out.WriteString(codeFence(raw))
			out.WriteString("\n\n")
			break
		}
		// A <br/> adds a newline of its own, which doubles up with the line
		// break already in the source and would open a gap mid-paragraph.
		content := blankLineRe.ReplaceAllString(r.renderInlineChildren(n), "\n")
		out.WriteString(strings.TrimRight(content, " \n"))
		out.WriteString("\n\n")
	case *ast.Blockquote:
		content := strings.TrimSpace(r.renderBlocks(n.FirstChild(), depth))
		for _, line := range strings.Split(content, "\n") {
			switch {
			case line == "":
				out.WriteString(">\n")
			// Telegram has no nested blockquotes, and a second literal ">"
			// would need escaping, so nested levels are flattened.
			case strings.HasPrefix(line, ">"):
				out.WriteString(line)
				out.WriteByte('\n')
			default:
				out.WriteString("> ")
				out.WriteString(line)
				out.WriteByte('\n')
			}
		}
		out.WriteByte('\n')
	case *ast.List:
		out.WriteString(r.renderList(n, depth))
		if depth == 0 {
			out.WriteByte('\n')
		}
	case *ast.FencedCodeBlock:
		if placeholder, ok := r.mermaidPlaceholderIfAttached(n); ok {
			out.WriteString(placeholder)
			out.WriteString("\n\n")
			break
		}
		out.WriteString(r.renderFencedCode(n))
		out.WriteString("\n\n")
	case *ast.CodeBlock:
		out.WriteString(r.renderCodeLines(n.Lines()))
		out.WriteString("\n\n")
	case *ast.ThematicBreak:
		out.WriteString("──────────\n\n")
	case *extensionast.Table:
		out.WriteString(r.renderTable(n))
		out.WriteString("\n\n")
	case *ast.HTMLBlock:
		raw := strings.TrimRight(r.blockLines(n.Lines()), "\n")
		// Hand-written markup spanning several lines is source, and reads
		// far better fenced than as escaped text.
		if strings.Contains(raw, "\n") && looksLikeCode(raw) {
			out.WriteString(codeFence(raw))
		} else {
			out.WriteString(translateHTMLString(raw))
		}
		out.WriteString("\n\n")
	default:
		if n.HasChildren() {
			out.WriteString(r.renderBlocks(n.FirstChild(), depth))
		}
	}
	return out.String()
}

func (r renderer) renderList(list *ast.List, depth int) string {
	var out strings.Builder
	index := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		if item.Kind() != ast.KindListItem {
			continue
		}
		prefix := "• "
		if list.IsOrdered() {
			prefix = fmt.Sprintf("%d\\. ", index)
			index++
		}
		indent := strings.Repeat("  ", depth)
		out.WriteString(indent)
		out.WriteString(prefix)

		first := true
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			if nested, ok := child.(*ast.List); ok {
				if first {
					out.WriteByte('\n')
				}
				out.WriteString(r.renderList(nested, depth+1))
				first = false
				continue
			}
			content := strings.TrimSpace(r.renderBlock(child, depth+1))
			if content == "" {
				continue
			}
			// Telegram only recognizes a code fence at the start of a line, so
			// fenced content (code blocks and tables) keeps column zero instead
			// of following the item indent.
			if strings.HasPrefix(content, "```") {
				if first {
					out.WriteByte('\n')
				}
				out.WriteString(content)
				out.WriteByte('\n')
				first = false
				continue
			}
			if !first {
				out.WriteString(indent)
				out.WriteString("  ")
			}
			out.WriteString(strings.ReplaceAll(content, "\n", "\n"+indent+"  "))
			out.WriteByte('\n')
			first = false
		}
		if first {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func (r renderer) renderInlineChildren(parent ast.Node) string {
	var out strings.Builder
	// HTML tags arrive as separate inline nodes, so the open ones are tracked
	// across the whole run of children.
	html := &htmlState{}
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if raw, ok := child.(*ast.RawHTML); ok {
			out.WriteString(html.translate(r.rawHTMLText(raw)))
			continue
		}
		if text, ok := child.(*ast.Text); ok && html.inCode() {
			out.WriteString(escapeCode(decodeEntities(r.textValue(text))))
			continue
		}
		out.WriteString(r.renderInline(child))
	}
	out.WriteString(html.closeAll())
	return out.String()
}

func (r renderer) rawHTMLText(n *ast.RawHTML) string {
	var out strings.Builder
	for i := 0; i < n.Segments.Len(); i++ {
		segment := n.Segments.At(i)
		out.Write(segment.Value(r.source))
	}
	return out.String()
}

func (r renderer) textValue(n *ast.Text) string {
	value := string(n.Segment.Value(r.source))
	if n.SoftLineBreak() || n.HardLineBreak() {
		value += "\n"
	}
	return value
}

// mermaidPlaceholderIfAttached returns a MarkdownV2 placeholder when this node
// is a Mermaid diagram that was uploaded as an attachment.
func (r renderer) mermaidPlaceholderIfAttached(n ast.Node) (string, bool) {
	if r.mermaidSeen == nil || len(r.mermaidAttached) == 0 {
		return "", false
	}
	if !r.isMermaidNode(n) {
		return "", false
	}
	index := *r.mermaidSeen
	*r.mermaidSeen++
	if index >= len(r.mermaidAttached) || !r.mermaidAttached[index] {
		return "", false
	}
	attachedIndex := 0
	for i := 0; i < index; i++ {
		if r.mermaidAttached[i] {
			attachedIndex++
		}
	}
	name := MermaidAttachmentName(attachedIndex)
	return "📎 " + escapeText(name+" (attached image)"), true
}

func (r renderer) isMermaidNode(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.FencedCodeBlock:
		return strings.EqualFold(strings.TrimSpace(string(n.Language(r.source))), "mermaid")
	case *ast.Paragraph:
		diagram := strings.TrimSpace(segmentsText(n.Lines(), r.source))
		return diagram != "" && mermaidHeaderRe.MatchString(diagram)
	default:
		return false
	}
}

func (r renderer) renderInline(n ast.Node) string {
	switch n := n.(type) {
	case *ast.Text:
		return escapeText(r.textValue(n))
	case *ast.String:
		return escapeText(string(n.Value))
	case *ast.Emphasis:
		content := r.renderInlineChildren(n)
		if n.Level == 2 {
			return "*" + content + "*"
		}
		return "_" + content + "_"
	case *ast.CodeSpan:
		return "`" + escapeCode(string(n.Text(r.source))) + "`"
	case *ast.Link:
		return renderLink(r.renderInlineChildren(n), string(n.Destination))
	case *ast.Image:
		alt := plainText(n, r.source)
		if alt == "" {
			alt = "image"
		}
		return renderLink("📷 "+escapeText(alt), string(n.Destination))
	case *ast.AutoLink:
		return renderLink(escapeText(string(n.Label(r.source))), string(n.URL(r.source)))
	case *ast.RawHTML:
		// Reached only outside renderInlineChildren, where no surrounding
		// tag state exists, so the tag stands alone.
		state := &htmlState{}
		return state.translate(r.rawHTMLText(n)) + state.closeAll()
	case *extensionast.Strikethrough:
		return "~" + r.renderInlineChildren(n) + "~"
	case *extensionast.TaskCheckBox:
		if n.IsChecked {
			return "☑ "
		}
		return "☐ "
	default:
		if n.HasChildren() {
			return r.renderInlineChildren(n)
		}
	}
	return ""
}

func (r renderer) renderFencedCode(n *ast.FencedCodeBlock) string {
	lang := strings.TrimSpace(string(n.Language(r.source)))
	content := strings.TrimSuffix(r.blockLines(n.Lines()), "\n")
	return "```" + escapeCode(lang) + "\n" + escapeCode(content) + "\n```"
}

func (r renderer) renderCodeLines(lines *text.Segments) string {
	content := strings.TrimSuffix(r.blockLines(lines), "\n")
	return "```\n" + escapeCode(content) + "\n```"
}

func (r renderer) blockLines(lines *text.Segments) string {
	return segmentsText(lines, r.source)
}

func segmentsText(lines *text.Segments, source []byte) string {
	var out strings.Builder
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		out.Write(segment.Value(source))
	}
	return out.String()
}

// indentedSegmentsText is segmentsText with the leading whitespace the parser
// strips from paragraph continuation lines put back. Indentation is meaningful
// in a pasted code snippet, and in Python it is the syntax.
func indentedSegmentsText(lines *text.Segments, source []byte) string {
	var out strings.Builder
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		start := segment.Start
		for start > 0 && (source[start-1] == ' ' || source[start-1] == '\t') {
			start--
		}
		out.Write(source[start:segment.Stop])
	}
	return out.String()
}

func (r renderer) renderTable(table *extensionast.Table) string {
	var rows [][]string
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		if row.Kind() != extensionast.KindTableHeader && row.Kind() != extensionast.KindTableRow {
			continue
		}
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, plainText(cell, r.source))
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return ""
	}

	columns := 0
	for _, row := range rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	widths := make([]int, columns)
	for _, row := range rows {
		for i, cell := range row {
			width := utf8.RuneCountInString(cell)
			if width > widths[i] {
				widths[i] = width
			}
		}
	}

	var out strings.Builder
	for _, row := range rows {
		for i := 0; i < columns; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			out.WriteString(cell)
			out.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)))
			if i < columns-1 {
				out.WriteString(" | ")
			}
		}
		out.WriteByte('\n')
	}
	return "```\n" + escapeCode(strings.TrimSuffix(out.String(), "\n")) + "\n```"
}

func plainText(n ast.Node, source []byte) string {
	var out strings.Builder
	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.Text:
			out.Write(node.Segment.Value(source))
		case *ast.String:
			out.Write(node.Value)
		case *ast.CodeSpan:
			out.Write(node.Text(source))
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(out.String())
}

func escapeText(s string) string {
	// "&amp;" and friends are markup in the source, and reach the reader as
	// the character they stand for.
	s = decodeEntities(s)
	var out strings.Builder
	out.Grow(len(s) + 8)
	for _, char := range s {
		if strings.ContainsRune(reservedChars, char) {
			out.WriteByte('\\')
		}
		out.WriteRune(char)
	}
	return out.String()
}

func escapeCode(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "`", "\\`")
}

func escapeURL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `)`, `\)`)
}

// IsLinkableURL reports whether Telegram will accept a destination as a link
// target. Relative paths such as "/image/logo.svg" are rejected by the Bot API,
// so they are rendered as plain text instead.
func IsLinkableURL(destination string) bool {
	destination = strings.TrimSpace(destination)
	parsed, err := url.Parse(destination)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "tg":
		return true
	default:
		return false
	}
}

// renderLink emits a MarkdownV2 inline link, degrading to plain text when the
// destination is not a URL Telegram can link to.
func renderLink(label, destination string) string {
	destination = strings.TrimSpace(destination)
	if IsLinkableURL(destination) {
		return "[" + label + "](" + escapeURL(destination) + ")"
	}
	if destination == "" {
		return label
	}
	return label + " \\(" + escapeText(destination) + "\\)"
}
