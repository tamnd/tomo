package readable

import (
	"strings"
	"testing"
)

func fromString(t *testing.T, doc string) Page {
	t.Helper()
	p, err := FromHTML(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTitleAndHeadings(t *testing.T) {
	p := fromString(t, `<html><head><title>Hello World</title></head>
		<body><article><h1>Big</h1><h2>Small</h2><p>A paragraph.</p></article></body></html>`)
	if p.Title != "Hello World" {
		t.Errorf("title = %q", p.Title)
	}
	if !strings.Contains(p.Markdown, "# Big") || !strings.Contains(p.Markdown, "## Small") {
		t.Errorf("headings missing: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "A paragraph.") {
		t.Errorf("paragraph missing: %q", p.Markdown)
	}
}

func TestDropsChrome(t *testing.T) {
	p := fromString(t, `<body>
		<nav>menu links</nav>
		<header>site header</header>
		<main><p>Keep this.</p></main>
		<footer>site footer</footer>
		<script>var x = 1;</script>
		<style>.a{color:red}</style>
	</body>`)
	for _, junk := range []string{"menu links", "site header", "site footer", "var x", "color:red"} {
		if strings.Contains(p.Markdown, junk) {
			t.Errorf("chrome leaked (%q): %q", junk, p.Markdown)
		}
	}
	if !strings.Contains(p.Markdown, "Keep this.") {
		t.Errorf("content dropped: %q", p.Markdown)
	}
}

func TestLinksAndEmphasis(t *testing.T) {
	p := fromString(t, `<article><p>See <a href="https://x.test/a">the docs</a> and
		<strong>note</strong> the <em>detail</em>.</p></article>`)
	if !strings.Contains(p.Markdown, "[the docs](https://x.test/a)") {
		t.Errorf("link markdown missing: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "**note**") || !strings.Contains(p.Markdown, "*detail*") {
		t.Errorf("emphasis missing: %q", p.Markdown)
	}
}

func TestBareAndAnchorLinksStayPlain(t *testing.T) {
	p := fromString(t, `<article><p><a href="#top">jump</a> and <a href="javascript:void(0)">nope</a></p></article>`)
	if strings.Contains(p.Markdown, "](") {
		t.Errorf("anchor and script links should not become markdown links: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "jump") || !strings.Contains(p.Markdown, "nope") {
		t.Errorf("link text should survive: %q", p.Markdown)
	}
}

func TestLists(t *testing.T) {
	p := fromString(t, `<article><ul><li>one</li><li>two</li></ul>
		<ol><li>first</li><li>second</li></ol></article>`)
	if !strings.Contains(p.Markdown, "- one") || !strings.Contains(p.Markdown, "- two") {
		t.Errorf("unordered list missing: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "1. first") || !strings.Contains(p.Markdown, "2. second") {
		t.Errorf("ordered list missing: %q", p.Markdown)
	}
}

func TestNestedList(t *testing.T) {
	p := fromString(t, `<article><ul><li>top<ul><li>child</li></ul></li></ul></article>`)
	if !strings.Contains(p.Markdown, "- top") || !strings.Contains(p.Markdown, "  - child") {
		t.Errorf("nested list not indented: %q", p.Markdown)
	}
}

func TestPreservesCodeBlock(t *testing.T) {
	p := fromString(t, "<article><pre>func main() {\n\tprintln(1)\n}</pre></article>")
	if !strings.Contains(p.Markdown, "```") || !strings.Contains(p.Markdown, "func main()") {
		t.Errorf("code fence missing: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "\tprintln(1)") {
		t.Errorf("pre whitespace not preserved: %q", p.Markdown)
	}
}

func TestInlineCode(t *testing.T) {
	p := fromString(t, `<article><p>Run <code>go test</code> often.</p></article>`)
	if !strings.Contains(p.Markdown, "`go test`") {
		t.Errorf("inline code missing: %q", p.Markdown)
	}
}

func TestTable(t *testing.T) {
	p := fromString(t, `<article><table>
		<tr><th>Name</th><th>Age</th></tr>
		<tr><td>Ada</td><td>36</td></tr></table></article>`)
	if !strings.Contains(p.Markdown, "| Name | Age |") || !strings.Contains(p.Markdown, "| Ada | 36 |") {
		t.Errorf("table not rendered: %q", p.Markdown)
	}
	if !strings.Contains(p.Markdown, "| --- |") {
		t.Errorf("table header separator missing: %q", p.Markdown)
	}
}

func TestImage(t *testing.T) {
	p := fromString(t, `<article><p><img src="/pic.png" alt="a cat"></p></article>`)
	if !strings.Contains(p.Markdown, "![a cat](/pic.png)") {
		t.Errorf("image markdown missing: %q", p.Markdown)
	}
}

func TestCollapsesWhitespace(t *testing.T) {
	p := fromString(t, "<article><p>lots\n\n   of     space</p></article>")
	if !strings.Contains(p.Markdown, "lots of space") {
		t.Errorf("whitespace not collapsed: %q", p.Markdown)
	}
}

func TestFallsBackToBodyWithoutArticle(t *testing.T) {
	p := fromString(t, `<body><div><p>Loose content.</p></div></body>`)
	if !strings.Contains(p.Markdown, "Loose content.") {
		t.Errorf("body fallback failed: %q", p.Markdown)
	}
}
