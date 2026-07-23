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
	// primaries records, for each resolved named symbol, the open document and the
	// symbol's range, so the expansion pass can scan its body for call sites and
	// follow them to their definitions without re-reading the file.
	var primaries []lspPrimary
	opened := map[string]bool{}
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
		opened[uri] = true
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
			primaries = append(primaries, lspPrimary{name: s, uri: uri, rng: sym.Range, lines: lines})
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
	// One-hop call-edge expansion: follow static calls out of the named symbols to
	// surface a helper the task never named but the change turns on. It fills only
	// the room the named symbols leave, so it never displaces a directly-asked one.
	if len(out) < packMaxSymbols {
		for _, d := range expandCallees(client, root, primaries, found, opened) {
			out = append(out, d)
			if len(out) >= packMaxSymbols {
				break
			}
		}
	}
	return out
}

// lspPrimary is a resolved task-named symbol retained for the expansion pass: its
// open document URI, its enclosing range, and the file's lines, so the pass can
// scan the body for call sites without re-reading the file.
type lspPrimary struct {
	name  string
	uri   string
	rng   lsp.Range
	lines []string
}

// callSite matches an identifier immediately followed by an open paren, the
// textual shape of a call. It is deliberately loose: a false positive costs one
// go-to-definition probe that resolves to nothing or outside the workspace and is
// then dropped.
var callSite = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// callSkip drops call targets that are keywords, builtins, or too generic to be
// worth a probe. Following them wastes an LSP round trip on something the model
// already knows or that never resolves to a workspace definition.
var callSkip = map[string]bool{
	"if": true, "for": true, "while": true, "return": true, "print": true,
	"len": true, "str": true, "int": true, "dict": true, "list": true,
	"set": true, "tuple": true, "bool": true, "float": true, "type": true,
	"super": true, "isinstance": true, "getattr": true, "setattr": true,
	"hasattr": true, "range": true, "enumerate": true, "open": true,
	"format": true, "repr": true, "self": true, "assert": true, "raise": true,
}

// cachedDoc is a target file opened once during expansion: its symbol tree, its
// lines for slicing, and its extension for fencing.
type cachedDoc struct {
	syms  []lsp.DocumentSymbol
	lines []string
	ext   string
}

// expandCallees follows one static call hop out of each resolved task-named
// symbol. For every call site in a primary's body it asks the server for the
// callee's definition; when that definition lands in a workspace file the task
// never named, it adds the enclosing symbol to the pack, marked with the primary
// it was reached from. This surfaces the helper a named entry point dispatches
// to, which is often where the branch the task turns on actually lives. It is
// bounded on every axis (callees per symbol, total additions) and opens each
// target file at most once. Every failure is silent, so expansion never worsens
// a run whose named symbols resolved fine.
func expandCallees(client *lsp.Client, root string, primaries []lspPrimary, found map[string]*symbolDef, opened map[string]bool) []symbolDef {
	type key struct{ rel, name string }
	docs := map[string]*cachedDoc{}
	seen := map[key]bool{}
	var out []symbolDef
	for _, p := range primaries {
		if len(out) >= packMaxExpand {
			break
		}
		probes := 0
		lo, hi := p.rng.Start.Line, p.rng.End.Line
		if lo < 0 {
			lo = 0
		}
		if hi >= len(p.lines) {
			hi = len(p.lines) - 1
		}
		for ln := lo; ln <= hi && ln < len(p.lines); ln++ {
			if len(out) >= packMaxExpand || probes >= packMaxCalleesPerSymbol {
				break
			}
			for _, m := range callSite.FindAllStringSubmatchIndex(p.lines[ln], -1) {
				if len(out) >= packMaxExpand || probes >= packMaxCalleesPerSymbol {
					break
				}
				name := p.lines[ln][m[2]:m[3]]
				if len(name) < packMinCalleeLen || callSkip[name] || found[name] != nil {
					continue
				}
				probes++
				locs, err := client.Definition(p.uri, ln, m[2])
				if err != nil || len(locs) == 0 {
					continue
				}
				loc := locs[0]
				tpath := lsp.URIToPath(loc.URI)
				rel, relErr := filepath.Rel(root, tpath)
				if relErr != nil || strings.HasPrefix(rel, "..") {
					continue // definition outside the workspace: stdlib, site-packages
				}
				cd := docs[loc.URI]
				if cd == nil {
					ext := strings.ToLower(filepath.Ext(tpath))
					if _, ok := packSourceExt[ext]; !ok {
						continue
					}
					data, rerr := os.ReadFile(tpath)
					if rerr != nil {
						continue
					}
					if !opened[loc.URI] {
						if err := client.DidOpen(loc.URI, languageID(ext), string(data)); err != nil {
							continue
						}
						opened[loc.URI] = true
					}
					ds, derr := client.DocumentSymbol(loc.URI)
					if derr != nil {
						continue
					}
					cd = &cachedDoc{syms: ds, lines: strings.Split(string(data), "\n"), ext: ext}
					docs[loc.URI] = cd
				}
				sym, ok := enclosingSymbolAt(cd.syms, loc.Range.Start.Line)
				if !ok {
					continue
				}
				base := lastNameComponent(sym.Name)
				if found[base] != nil {
					continue // already carried as a named symbol
				}
				k := key{rel, base}
				if seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, symbolDef{
					name: base,
					rel:  rel,
					line: sym.Range.Start.Line + 1,
					lang: cd.ext,
					body: sliceRange(cd.lines, sym.Range),
					refs: lspRefs(client, root, loc.URI, sym.SelectionRange.Start, rel),
					via:  p.name,
				})
				if len(out) >= packMaxExpand {
					break
				}
			}
		}
	}
	return out
}

// enclosingSymbolAt returns the deepest document symbol whose range contains the
// zero-based line. The deepest match is preferred, so a method is chosen over the
// class that contains it.
func enclosingSymbolAt(syms []lsp.DocumentSymbol, line int) (lsp.DocumentSymbol, bool) {
	for _, s := range syms {
		if line < s.Range.Start.Line || line > s.Range.End.Line {
			continue
		}
		if child, ok := enclosingSymbolAt(s.Children, line); ok {
			return child, true
		}
		return s, true
	}
	return lsp.DocumentSymbol{}, false
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
