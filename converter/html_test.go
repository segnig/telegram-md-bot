package converter

import (
	"strings"
	"testing"
)

func TestHTMLTagsBecomeEntities(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want string
	}{
		{"bold", "<b>bold tag</b>", "*bold tag*"},
		{"strong", "<strong>bold</strong>", "*bold*"},
		{"italic", "<i>it</i>", "_it_"},
		{"em", "<em>it</em>", "_it_"},
		{"underline", "<u>under</u>", "__under__"},
		{"strike", "<s>gone</s>", "~gone~"},
		{"del", "<del>gone</del>", "~gone~"},
		{"code", "<code>a.b()</code>", "`a.b()`"},
		{"spoiler span", `<span class="tg-spoiler">secret</span>`, "||secret||"},
		{"spoiler tag", "<tg-spoiler>secret</tg-spoiler>", "||secret||"},
		{"link", `<a href="https://example.com/x">site</a>`, "[site](https://example.com/x)"},
		{"nested", "<b>bold <i>and italic</i></b>", "*bold _and italic_*"},
		{"plain span dropped", `<span class="x">text</span>`, "text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Convert(test.md); got != test.want {
				t.Errorf("Convert(%q) = %q, want %q", test.md, got, test.want)
			}
		})
	}
}

func TestHTMLLineBreakBecomesNewline(t *testing.T) {
	got := Convert("first<br/>second")
	if got != "first\nsecond" {
		t.Errorf("Convert() = %q, want %q", got, "first\nsecond")
	}
}

func TestHTMLCodeKeepsCodeEscaping(t *testing.T) {
	// Inside a code entity only backticks and backslashes are escaped, so the
	// dot must arrive bare.
	got := Convert(`Use <code>a.b\c</code> now`)
	if !strings.Contains(got, "`a.b\\\\c`") {
		t.Errorf("code entity escaping is wrong: %q", got)
	}
}

func TestDanglingHTMLTagIsClosed(t *testing.T) {
	got := Convert("dangling <b>never closed")
	if strings.Count(got, "*")%2 != 0 {
		t.Errorf("unbalanced entity would be rejected by Telegram: %q", got)
	}
}

func TestStrayClosingHTMLTagIsDropped(t *testing.T) {
	got := Convert("text </b> more")
	if strings.Contains(got, "*") {
		t.Errorf("stray closing tag produced a marker: %q", got)
	}
}

func TestHTMLLinkWithRelativeTargetBecomesText(t *testing.T) {
	got := Convert(`see <a href="/local">link</a> here`)
	if strings.Contains(got, "](") {
		t.Errorf("relative target should not become a link: %q", got)
	}
	if !strings.Contains(got, "link") {
		t.Errorf("link text was lost: %q", got)
	}
}

func TestHTMLEntitiesAreDecoded(t *testing.T) {
	tests := map[string]string{
		"a &amp; b":     "a & b",
		"&lt;tag&gt;":   `<tag\>`,
		"&quot;q&quot;": `"q"`,
		"&#39;s&#39;":   "'s'",
		"&#x27;s&#x27;": "'s'",
		"&hellip;":      "…",
		"&nbsp;x":       " x",
		"&unknown;":     `&unknown;`,
	}
	for input, want := range tests {
		if got := Convert(input); got != want {
			t.Errorf("Convert(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEntitiesNotDecodedInsideCodeFence(t *testing.T) {
	got := Convert("```\na &amp; b\n```")
	if !strings.Contains(got, "a &amp; b") {
		t.Errorf("code block content was altered: %q", got)
	}
}

func TestEscapingSectionOfSample(t *testing.T) {
	got := Convert("Cost: $5.00 (approx!) - done.\n" +
		"Special chars: _ * [ ] ( ) ~ > # + - = | { } . !\n" +
		"Math-ish: 2 * 3 = 6, a_b_c, 50% off.\n" +
		"Raw HTML: <b>bold tag</b> and <br/>")

	want := "Cost: $5\\.00 \\(approx\\!\\) \\- done\\.\n" +
		"Special chars: \\_ \\* \\[ \\] \\( \\) \\~ \\> \\# \\+ \\- \\= \\| \\{ \\} \\. \\!\n" +
		"Math\\-ish: 2 \\* 3 \\= 6, a\\_b\\_c, 50% off\\.\n" +
		"Raw HTML: *bold tag* and"
	if strings.TrimSpace(got) != want {
		t.Errorf("Convert() =\n%q\nwant\n%q", got, want)
	}
}
