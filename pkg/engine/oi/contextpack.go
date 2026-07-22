package oi

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The symbol-anchored context pack.
//
// A code-as-action model reads the workspace in slices it chooses, so it can
// edit a function the task named without ever opening the branch of that function
// the task actually turns on. That is not a persistence failure and not a
// prompting failure, it is a retrieval failure: the model never put the deciding
// lines in front of itself. The observed case that motivated this: a task listed
// "settings_loader must load multiple environments", the model read the file,
// changed the function to thread a new parameter, and left the one branch that
// composes the environment-named companion file untouched, because nothing made
// it look there.
//
// The pack is deterministic retrieval that runs once before the loop. It lifts
// the identifiers the task names, resolves each to its definition in the
// workspace, and hands the model the whole definition and where it is used, so
// the first edit is made against the full contract rather than a chosen slice. It
// is not a prompt: the same task text and the same tree always produce the same
// pack, and it adds no model round trip. It degrades to nothing when the
// workspace is absent or unreadable, so a run that cannot use it pays nothing.

const (
	// packMaxSymbols caps how many resolved definitions the pack carries, newest
	// intent first, so a task that names a dozen things does not bury the model.
	packMaxSymbols = 12
	// packMaxDefLines caps a single definition slice; a longer body is truncated
	// with a marker so one giant function cannot crowd out the rest.
	packMaxDefLines = 200
	// packMaxRefs caps how many use sites are listed per symbol.
	packMaxRefs = 10
	// packMaxBytes is the whole pack's ceiling, so the preamble stays a preamble.
	packMaxBytes = 28_000
	// packMinSymbolLen drops short prose words; a real symbol the task leans on is
	// almost always this long, and shorter ones survive only when backticked.
	packMinSymbolLen = 4
	// packMaxFileBytes skips a file too large to be worth scanning for a def.
	packMaxFileBytes = 1 << 20
	// packMaxFiles bounds the walk so a huge tree cannot stall the preamble.
	packMaxFiles = 20_000
)

// packSourceExt is the set of extensions the pack scans for definitions. The map
// value is the block-extent strategy: "py" for indentation, "brace" for a
// brace-delimited body, "" for a fixed window fallback.
var packSourceExt = map[string]string{
	".py": "py", ".pyi": "py",
	".go": "brace", ".js": "brace", ".jsx": "brace", ".ts": "brace", ".tsx": "brace",
	".java": "brace", ".c": "brace", ".h": "brace", ".cc": "brace", ".cpp": "brace",
	".rs": "brace", ".rb": "py", ".php": "brace", ".cs": "brace", ".swift": "brace",
	".kt": "brace", ".scala": "brace",
}

// packSkipDir names directories the walk never descends into.
var packSkipDir = map[string]bool{
	".git": true, ".tomodata": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true, ".mypy_cache": true,
	"dist": true, "build": true, ".tox": true, ".idea": true, "target": true,
}

var (
	backtickSpan = regexp.MustCompile("`([^`]+)`")
	identToken   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	hasInnerCaps = regexp.MustCompile(`[a-z][A-Z]`)
)

// defRegex matches a definition line for a symbol: def/class/func/function NAME,
// allowing an optional Go receiver group before the name. Shared by the regex and
// LSP resolvers so both prefilter files the same way.
func defRegex(symbol string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*(?:def|class|func|function)\s+(?:\([^)]*\)\s*)?` + regexp.QuoteMeta(symbol) + `\b`)
}

// contextPack builds the retrieval preamble for a turn, or "" when there is
// nothing to add. taskText is the user's message; the workspace is e.Workspace.
func (e *Engine) contextPack(taskText string) string {
	return ContextPackWith(e.Workspace, taskText, e.LSP)
}

// ContextPack is the symbol-anchored retrieval as a pure function of a workspace
// root and a task text, returning the preamble the engine injects once before the
// loop, or "" when there is nothing to add. It uses the dependency-free regex
// resolver. It is exported so a preview command and the labs A/B can drive the
// exact mechanism the engine uses rather than a reimplementation of it.
func ContextPack(workspace, taskText string) string {
	return ContextPackWith(workspace, taskText, nil)
}

// ContextPackWith is ContextPack with an optional language-server command. When
// lspArgv is non-empty and the server resolves the named symbols, their exact
// enclosing definitions and true references feed the pack; on any LSP failure, or
// when lspArgv is empty, it falls back to the regex resolver, so the caller is
// never worse off for asking.
func ContextPackWith(workspace, taskText string, lspArgv []string) string {
	if workspace == "" {
		return ""
	}
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return ""
	}
	symbols := candidateSymbols(taskText)
	if len(symbols) == 0 {
		return ""
	}
	var defs []symbolDef
	if len(lspArgv) > 0 {
		defs = resolveSymbolsLSP(workspace, symbols, lspArgv)
	}
	if len(defs) == 0 {
		defs = resolveSymbols(workspace, symbols)
	}
	if len(defs) == 0 {
		return ""
	}
	return renderPack(defs)
}

// symbolDef is one resolved definition and where it is used.
type symbolDef struct {
	name string
	rel  string // workspace-relative file path
	line int    // 1-indexed line of the def
	lang string // extension key, drives fencing
	body string // the sliced definition text
	refs []string
}

// candidateSymbols extracts the identifiers a task leans on, in first-appearance
// order. A token qualifies when it is long enough and looks like code rather than
// prose: it is backticked, carries an underscore, or has an interior capital.
// This keeps "settings_loader", "populate_obj", and "buildEnvList" and drops
// "must", "multiple", and "environments".
func candidateSymbols(taskText string) []string {
	backticked := map[string]bool{}
	for _, m := range backtickSpan.FindAllStringSubmatch(taskText, -1) {
		for _, tok := range identToken.FindAllString(m[1], -1) {
			backticked[tok] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range identToken.FindAllString(taskText, -1) {
		if seen[tok] || len(tok) < packMinSymbolLen {
			continue
		}
		codey := backticked[tok] || strings.Contains(tok, "_") || hasInnerCaps.MatchString(tok)
		if !codey {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// resolveSymbols walks the workspace once and, for each symbol, records the first
// definition it finds and the files that reference the symbol. It returns the
// definitions in the symbols' task order, capped at packMaxSymbols.
func resolveSymbols(root string, symbols []string) []symbolDef {
	defRe := make(map[string]*regexp.Regexp, len(symbols))
	refRe := make(map[string]*regexp.Regexp, len(symbols))
	for _, s := range symbols {
		defRe[s] = defRegex(s)
		refRe[s] = regexp.MustCompile(`\b` + regexp.QuoteMeta(s) + `\b`)
	}
	found := map[string]*symbolDef{}
	refs := map[string][]string{}
	files := 0

	filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // skip unreadable entries, never abort the whole pack
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
		strategy, ok := packSourceExt[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil
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
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		isTest := looksLikeTestPath(rel)
		lines := strings.Split(text, "\n")
		for _, s := range symbols {
			if refRe[s].MatchString(text) {
				refs[s] = appendRef(refs[s], rel, isTest)
			}
			if found[s] != nil {
				continue
			}
			loc := defRe[s].FindStringIndex(text)
			if loc == nil {
				continue
			}
			lineNo := 1 + strings.Count(text[:loc[0]], "\n")
			body := sliceDefinition(lines, lineNo-1, strategy)
			found[s] = &symbolDef{name: s, rel: rel, line: lineNo, lang: strings.ToLower(filepath.Ext(path)), body: body}
		}
		return nil
	})

	var out []symbolDef
	for _, s := range symbols {
		d := found[s]
		if d == nil {
			continue
		}
		d.refs = trimRefs(refs[s], d.rel)
		out = append(out, *d)
		if len(out) >= packMaxSymbols {
			break
		}
	}
	return out
}

// sliceDefinition returns the definition block starting at lines[start]. For an
// indentation language the block runs until the indentation returns to the def's
// level or shallower; for a brace language it runs until the braces opened on or
// after the def line close; otherwise a fixed window. Every path is capped at
// packMaxDefLines.
func sliceDefinition(lines []string, start int, strategy string) string {
	if start < 0 || start >= len(lines) {
		return ""
	}
	end := start + 1
	switch strategy {
	case "py":
		base := indentWidth(lines[start])
		for end < len(lines) {
			ln := lines[end]
			if strings.TrimSpace(ln) != "" && indentWidth(ln) <= base && !isDecorator(ln) {
				break
			}
			end++
		}
	case "brace":
		depth, opened := 0, false
		for end = start; end < len(lines); end++ {
			depth += strings.Count(lines[end], "{") - strings.Count(lines[end], "}")
			if strings.Contains(lines[end], "{") {
				opened = true
			}
			if opened && depth <= 0 {
				end++
				break
			}
		}
	default:
		end = start + 40
	}
	if end > start+packMaxDefLines {
		end = start + packMaxDefLines
		if end > len(lines) {
			end = len(lines)
		}
		return strings.Join(lines[start:end], "\n") + "\n... (definition truncated)"
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

// renderPack formats the resolved definitions into the preamble.
func renderPack(defs []symbolDef) string {
	var b strings.Builder
	b.WriteString("Relevant definitions from this workspace, gathered from the identifiers the task names. " +
		"Read these before editing: the change you need may turn on a branch inside one of these that you would not otherwise open. " +
		"These are the current definitions, not the target; change them to satisfy the task.\n")
	for _, d := range defs {
		fence := fenceFor(d.lang)
		fmt.Fprintf(&b, "\n## %s  —  %s:%d\n%s\n%s\n%s\n", d.name, d.rel, d.line, fence, d.body, "```")
		if len(d.refs) > 0 {
			fmt.Fprintf(&b, "used in: %s\n", strings.Join(d.refs, ", "))
		}
		if b.Len() > packMaxBytes {
			b.WriteString("\n... (context pack truncated at size limit)\n")
			break
		}
	}
	return b.String()
}

func indentWidth(line string) int {
	n := 0
	for _, r := range line {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

func isDecorator(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "@")
}

func looksLikeTestPath(rel string) bool {
	l := strings.ToLower(rel)
	return strings.Contains(l, "test") || strings.Contains(l, "spec")
}

func appendRef(refs []string, rel string, isTest bool) []string {
	label := rel
	if isTest {
		label += " (test)"
	}
	for _, r := range refs {
		if r == label {
			return refs
		}
	}
	return append(refs, label)
}

// trimRefs drops the defining file from the use list, prefers test files (they
// encode the contract), and caps the count.
func trimRefs(refs []string, defRel string) []string {
	var tests, other []string
	for _, r := range refs {
		if r == defRel || r == defRel+" (test)" {
			continue
		}
		if strings.HasSuffix(r, "(test)") {
			tests = append(tests, r)
		} else {
			other = append(other, r)
		}
	}
	sort.Strings(tests)
	sort.Strings(other)
	out := append(tests, other...)
	if len(out) > packMaxRefs {
		out = out[:packMaxRefs]
	}
	return out
}

func fenceFor(ext string) string {
	switch ext {
	case ".py", ".pyi":
		return "```python"
	case ".go":
		return "```go"
	case ".js", ".jsx":
		return "```javascript"
	case ".ts", ".tsx":
		return "```typescript"
	case ".rb":
		return "```ruby"
	case ".rs":
		return "```rust"
	default:
		return "```"
	}
}
