package oi

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tamnd/tomo/pkg/lsp"
)

// LSP-backed symbol resolution.
//
// The regex resolver (contextpack.go) finds a definition by matching a
// def/class/func line and guesses the extent by indentation or brace counting.
// That is good enough most of the time and needs no tools, but it makes two
// mistakes a language server does not: it can resolve the wrong same-named symbol
// (a method and a free function that share a name, a shadowing local), and its
// extent is a heuristic that a decorator, a multiline signature, or an unusual
// layout can throw off. It also lists textual matches as "references", which
// includes comments and unrelated identifiers that happen to share the spelling.
//
// When a language server is configured for the workspace, this path resolves the
// same symbols through it instead: documentSymbol gives the exact enclosing range
// of the real definition, and textDocument/references gives true use sites. The
// result feeds the identical renderer, so the pack the model sees has the same
// shape either way. Everything here fails soft: a missing server binary, a slow
// index, or any protocol error returns no definitions and the caller falls back
// to the regex resolver, so a run that cannot use LSP is never worse off.

// lspStartTimeout bounds server startup and the whole resolution pass, so a
// wedged server cannot stall the turn before the loop even begins.
const lspStartTimeout = 25 * time.Second

// resolveSymbolsLSP resolves the symbols through a language server started with
// argv, rooted at the workspace. It returns nil on any failure so the caller
// falls back to the regex resolver. It is a drop-in for resolveSymbols.
func resolveSymbolsLSP(root string, symbols []string, argv []string) []symbolDef {
	if len(argv) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), lspStartTimeout)
	defer cancel()
	client, err := lsp.Start(ctx, argv, root)
	if err != nil {
		return nil
	}
	defer client.Close()

	// Prefilter with the same regexes as the regex resolver: only files that
	// textually define a wanted symbol are opened, so the server is not asked to
	// index the whole tree when a handful of files hold the definitions.
	defRe := make(map[string]*regexp.Regexp, len(symbols))
	for _, s := range symbols {
		defRe[s] = defRegex(s)
	}

	found := map[string]*symbolDef{}
	files := 0
	filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && packSkipDir[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if files > packMaxFiles {
			return filepath.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := packSourceExt[ext]; !ok {
			return nil
		}
		if allResolved(found, symbols) {
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil || info.Size() > packMaxFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		// Which wanted, still-unresolved symbols does this file define textually?
		var here []string
		for _, s := range symbols {
			if found[s] == nil && defRe[s].MatchString(text) {
				here = append(here, s)
			}
		}
		if len(here) == 0 {
			return nil
		}
		uri := lsp.PathToURI(path)
		if err := client.DidOpen(uri, languageID(ext), text); err != nil {
			return nil
		}
		syms, err := client.DocumentSymbol(uri)
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		lines := strings.Split(text, "\n")
		for _, s := range here {
			sym, ok := findSymbol(syms, s)
			if !ok {
				continue
			}
			body := sliceRange(lines, sym.Range)
			d := &symbolDef{
				name: s,
				rel:  rel,
				line: sym.Range.Start.Line + 1,
				lang: ext,
				body: body,
			}
			d.refs = lspRefs(client, root, uri, sym.SelectionRange.Start, rel)
			found[s] = d
		}
		return nil
	})

	var out []symbolDef
	for _, s := range symbols {
		if d := found[s]; d != nil {
			out = append(out, *d)
			if len(out) >= packMaxSymbols {
				break
			}
		}
	}
	return out
}

// lspRefs collects the distinct files that reference the symbol at pos, marking
// tests and dropping the defining file, mirroring the regex resolver's trimRefs.
func lspRefs(client *lsp.Client, root, defURI string, pos lsp.Position, defRel string) []string {
	locs, err := client.References(defURI, pos.Line, pos.Character, false)
	if err != nil {
		return nil
	}
	var refs []string
	for _, loc := range locs {
		p := lsp.URIToPath(loc.URI)
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		refs = appendRef(refs, rel, looksLikeTestPath(rel))
	}
	return trimRefs(refs, defRel)
}

// findSymbol walks a document-symbol tree for the first symbol with the given
// name, at any depth, so a method or nested function resolves too.
func findSymbol(syms []lsp.DocumentSymbol, name string) (lsp.DocumentSymbol, bool) {
	for _, s := range syms {
		if s.Name == name {
			return s, true
		}
		// A server may report a qualified name like "(*T).Method" or "Class.method";
		// match on the final dotted component as well.
		if base := lastNameComponent(s.Name); base == name {
			return s, true
		}
		if child, ok := findSymbol(s.Children, name); ok {
			return child, true
		}
	}
	return lsp.DocumentSymbol{}, false
}

func lastNameComponent(name string) string {
	name = strings.TrimSuffix(name, "()")
	if i := strings.LastIndexAny(name, ".)"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// sliceRange returns the text of lines spanned by r, inclusive of the end line,
// capped at packMaxDefLines like the regex slicer.
func sliceRange(lines []string, r lsp.Range) string {
	start := r.Start.Line
	end := r.End.Line
	if start < 0 || start >= len(lines) {
		return ""
	}
	if end < start {
		end = start
	}
	last := end + 1
	if last > len(lines) {
		last = len(lines)
	}
	if last > start+packMaxDefLines {
		last = start + packMaxDefLines
		if last > len(lines) {
			last = len(lines)
		}
		return strings.Join(lines[start:last], "\n") + "\n... (definition truncated)"
	}
	return strings.Join(lines[start:last], "\n")
}

func allResolved(found map[string]*symbolDef, symbols []string) bool {
	n := 0
	for _, s := range symbols {
		if found[s] != nil {
			n++
		}
	}
	return n >= len(symbols) || n >= packMaxSymbols
}

// languageID maps a file extension to the LSP language identifier a server
// expects in a didOpen. An unknown extension gets a bare guess from the ext.
func languageID(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".jsx":
		return "javascript"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp":
		return "cpp"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}
