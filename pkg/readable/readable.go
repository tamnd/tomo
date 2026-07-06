// Package readable turns an HTML page into clean Markdown. A raw page is mostly
// chrome (scripts, nav, styling) that wastes the model's context and buries the
// text that matters. This lifts out the main content and renders it as Markdown,
// which the model reads far better than a wall of tags. It is pure Go, so tomo
// stays a single static binary.
package readable

import (
	"io"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Page is the readable view of a document.
type Page struct {
	Title    string
	Markdown string
}

// skip lists elements that never carry readable content, so the walker drops
// them and everything under them.
var skip = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true,
	atom.Form: true, atom.Button: true, atom.Input: true, atom.Select: true,
	atom.Textarea: true, atom.Svg: true, atom.Iframe: true, atom.Head: true,
}

// FromHTML parses r and returns its title and the main content as Markdown.
func FromHTML(r io.Reader) (Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return Page{}, err
	}
	w := &writer{}
	w.block(pickRoot(doc), 0)
	return Page{Title: strings.TrimSpace(findTitle(doc)), Markdown: strings.TrimSpace(w.out.String())}, nil
}

// pickRoot chooses the node whose subtree holds the real content: the first
// <article>, else the first <main>, else <body>, else the document itself.
func pickRoot(doc *html.Node) *html.Node {
	if a := firstElement(doc, atom.Article); a != nil {
		return a
	}
	if m := firstElement(doc, atom.Main); m != nil {
		return m
	}
	if b := firstElement(doc, atom.Body); b != nil {
		return b
	}
	return doc
}

// writer accumulates Markdown, keeping each block separated by a blank line.
type writer struct {
	out strings.Builder
}

// emit writes one block, ensuring a blank line before it when text already
// exists. Empty blocks are dropped so boilerplate does not leave holes.
func (w *writer) emit(s string) {
	s = strings.TrimRight(s, " \t")
	if strings.TrimSpace(s) == "" {
		return
	}
	if w.out.Len() > 0 {
		w.out.WriteString("\n\n")
	}
	w.out.WriteString(s)
}

// block walks n as block-level flow, emitting a Markdown block per element it
// recognizes and recursing into containers. indent is the list nesting depth.
func (w *writer) block(n *html.Node, indent int) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if skip[c.DataAtom] {
			continue
		}
		switch c.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			level := int(c.Data[1] - '0')
			w.emit(strings.Repeat("#", level) + " " + inline(c))
		case atom.P:
			w.emit(inline(c))
		case atom.Pre:
			w.emit("```\n" + strings.Trim(textOf(c), "\n") + "\n```")
		case atom.Blockquote:
			w.emit(quote(inline(c)))
		case atom.Hr:
			w.emit("---")
		case atom.Ul, atom.Ol:
			w.list(c, indent)
		case atom.Table:
			w.emit(table(c))
		case atom.Br:
			// A stray block-level break carries nothing on its own.
		default:
			// A container (div, section, figure, ...): descend for its blocks.
			w.block(c, indent)
		}
	}
}

// list renders a ul or ol, numbering ordered lists and indenting nested ones.
func (w *writer) list(n *html.Node, indent int) {
	ordered := n.DataAtom == atom.Ol
	pad := strings.Repeat("  ", indent)
	item := 0
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		item++
		marker := "- "
		if ordered {
			marker = strconv.Itoa(item) + ". "
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(pad + marker + inline(c))
		// Nested lists hang under the item at the next indent level.
		for gc := c.FirstChild; gc != nil; gc = gc.NextSibling {
			if gc.Type == html.ElementNode && (gc.DataAtom == atom.Ul || gc.DataAtom == atom.Ol) {
				var nested writer
				nested.list(gc, indent+1)
				if s := nested.out.String(); s != "" {
					b.WriteString("\n" + s)
				}
			}
		}
	}
	w.emit(b.String())
}

// inline renders a node's subtree as one line of inline Markdown: text with
// links, emphasis, and inline code, whitespace collapsed. Nested block lists
// are left to the list renderer and skipped here.
func inline(n *html.Node) string {
	var b strings.Builder
	writeInline(&b, n)
	return strings.TrimSpace(collapseWS(b.String()))
}

func writeInline(b *strings.Builder, n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.TextNode:
			b.WriteString(c.Data)
		case c.Type != html.ElementNode:
			continue
		case skip[c.DataAtom]:
			continue
		}
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.A:
			text := inline(c)
			href := attr(c, "href")
			switch {
			case text == "":
			case href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:"):
				b.WriteString(text)
			default:
				b.WriteString("[" + text + "](" + href + ")")
			}
		case atom.Img:
			if src := attr(c, "src"); src != "" {
				b.WriteString("![" + attr(c, "alt") + "](" + src + ")")
			}
		case atom.Strong, atom.B:
			if s := inline(c); s != "" {
				b.WriteString("**" + s + "**")
			}
		case atom.Em, atom.I:
			if s := inline(c); s != "" {
				b.WriteString("*" + s + "*")
			}
		case atom.Code:
			if s := strings.TrimSpace(collapseWS(textOf(c))); s != "" {
				b.WriteString("`" + s + "`")
			}
		case atom.Br:
			b.WriteString("\n")
		case atom.Ul, atom.Ol:
			// Handled as a nested block under the list item, not inline.
		default:
			writeInline(b, c)
		}
	}
}

// table renders a simple pipe table. The first row becomes the header; a table
// with no rows renders as nothing.
func table(n *html.Node) string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if c.DataAtom == atom.Tr {
				var cells []string
				for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type == html.ElementNode && (cell.DataAtom == atom.Td || cell.DataAtom == atom.Th) {
						cells = append(cells, strings.ReplaceAll(inline(cell), "|", "\\|"))
					}
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
				continue
			}
			walk(c)
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| " + strings.Join(rows[0], " | ") + " |\n")
	b.WriteString("|" + strings.Repeat(" --- |", len(rows[0])) + "\n")
	for _, r := range rows[1:] {
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// quote prefixes every line of s with a blockquote marker.
func quote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// textOf returns the raw concatenated text of a subtree, preserving spacing so
// code and pre blocks keep their shape.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			} else if c.Type == html.ElementNode && !skip[c.DataAtom] {
				walk(c)
			}
		}
	}
	walk(n)
	return b.String()
}

// collapseWS replaces every run of whitespace with a single space.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func firstElement(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := firstElement(c, a); got != nil {
			return got
		}
	}
	return nil
}

func findTitle(doc *html.Node) string {
	if t := firstElement(doc, atom.Title); t != nil {
		return textOf(t)
	}
	return ""
}
