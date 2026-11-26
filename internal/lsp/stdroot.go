// Why: a user browsing std's own files expects answers there too, but std is no
// user package: checked as one it reports errors.
//
// What: the std root: rootKeyOf, the browse file, stdCacheDir, docAbove, moduleOf
// and declHover for items the query index does not hold.
//
// How: every file of a std tree belongs to one root, keyed "std:<tree>", whose
// check is std checked as std: a synthetic browse.mk under the cache imports
// the open files' modules. The check reads std from source (NoCache) so its
// syntax is kept for TypeAt.

package lsp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// pkgName is a manifest's first `name:` field, the package's own.
var pkgName = regexp.MustCompile(`name:\s*"([^"]*)"`)

// stdCopyDir matches the read-only std copy's directory, <cache>/lsp/std-<16 hex>.
var stdCopyDir = regexp.MustCompile(`^std-[0-9a-f]{16}$`)

// stdDirs are the --std directories of the servers of this process, by
// path key; stdManifests memoizes whether a directory's pkg.mk names std.
var stdDirs, stdManifests sync.Map

// registerStdDir marks a --std directory as a std tree.
func registerStdDir(dir string) {
	if dir == "" {
		return
	}
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	stdDirs.Store(pathKey(filepath.Clean(dir)), true)
}

// stdTreeLevel reports whether dir is the top of a std tree: a registered
// --std directory, a std copy, or a directory whose pkg.mk names std.
// manifest says whether dir holds a pkg.mk (or is a tree), which ends the
// walk upwards.
func stdTreeLevel(dir string) (std, manifest bool) {
	if _, ok := stdDirs.Load(pathKey(dir)); ok {
		return true, true
	}
	if stdCopyDir.MatchString(filepath.Base(dir)) && filepath.Base(filepath.Dir(dir)) == "lsp" {
		return true, true
	}
	fi, err := os.Stat(filepath.Join(dir, "pkg.mk"))
	if err != nil || fi.IsDir() {
		return false, false
	}
	k := pathKey(dir) + "\x00" + fi.ModTime().String()
	if v, ok := stdManifests.Load(k); ok {
		return v.(bool), true
	}
	isStd := false
	if raw, err := os.ReadFile(filepath.Join(dir, "pkg.mk")); err == nil {
		m := pkgName.FindSubmatch(raw)
		isStd = m != nil && string(m[1]) == "std"
	}
	stdManifests.Store(k, isStd)
	return isStd, true
}

// isStdRoot reports whether a root is a std tree's.
func (s *Server) isStdRoot(r *root) bool { return r.std > 0 }

// stdCheckDir is the std directory of a std root's check: the tree, or ""
// (the std embedded in mk) for the read-only copy, which holds only the
// files some check loaded.
func stdCheckDir(r *root) string {
	if stdCopyDir.MatchString(filepath.Base(r.dir)) {
		return ""
	}
	return r.dir
}

// stdCacheDir is the cache directory of std roots' checks and files:
// --cache, else MK_CACHE, else the user's cache directory; never a
// directory under std (a .mk-cache there breaks std's own tests).
func (s *Server) stdCacheDir() string {
	if s.opt.CacheDir != "" {
		return s.opt.CacheDir
	}
	if d := os.Getenv("MK_CACHE"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "mk")
	}
	return filepath.Join(os.TempDir(), "mk-cache")
}

// stdInternal are the std modules user code cannot import (E0202), and directories that are no importable module; a
// browse file reaches the internal ones through stdPulls.
var stdInternal = setOf("core raw rt sys target task free corpus pkg")

// stdPulls are imported when an open std file's module is internal: they
// load std.core, std.raw, std.rt, std.sys, std.target and std.task.
var stdPulls = []string{"std.compress", "std.fs", "std.net", "std.time"}

// stdNestedInternal are the std-internal modules under a public module's
// directory, with the public modules that load them: std.simd.kernel, the
// vector kernels, which std.hash and std.crypto import (std.text and
// std.encoding import it too, but std.hash already loads the whole module),
// and std.http.fields, the HPACK and QPACK codecs, which std.http imports.
var stdNestedInternal = map[string][]string{"simd/kernel": {"std.crypto", "std.hash"}, "http/fields": {"std.http"}}

// stdModuleOf is the import path of the std module holding path, or ""
// for a std-internal module.
func stdModuleOf(tree, path string) string {
	rel, err := filepath.Rel(tree, filepath.Dir(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	rel = filepath.ToSlash(rel)
	segs := strings.Split(rel, "/")
	if stdInternal[segs[0]] || stdNestedInternal[rel] != nil {
		return ""
	}
	return "std." + strings.Join(segs, ".")
}

// stdPullsOf are the imports that load the std-internal module holding
// path: its importers for a nested one, else stdPulls.
func stdPullsOf(tree, path string) []string {
	if rel, err := filepath.Rel(tree, filepath.Dir(path)); err == nil {
		if p := stdNestedInternal[filepath.ToSlash(rel)]; p != nil {
			return p
		}
	}
	return stdPulls
}

// browseFile writes a std root's browse.mk under the std cache directory:
// an import of each open std file's module (stdPulls for an internal
// one), rewritten only when the set changes. It returns its path.
func (s *Server) browseFile(r *root) (string, error) {
	mods := map[string]bool{}
	for _, d := range s.docs.all() {
		if foldKey(rootKeyOf(d.path)) != foldKey(r.key) {
			continue
		}
		if m := stdModuleOf(r.dir, d.path); m != "" {
			mods[m] = true
		} else {
			for _, p := range stdPullsOf(r.dir, d.path) {
				mods[p] = true
			}
		}
	}
	if len(mods) == 0 {
		for _, p := range stdPulls {
			mods[p] = true
		}
	}
	var lines []string
	for m := range mods {
		lines = append(lines, "import "+m)
	}
	sort.Strings(lines)
	text := "// Written by mk lsp: std checked as std for the editor.\n" + strings.Join(lines, "\n") + "\n\nexport Main(sys: &System) {\n}\n"
	// Named for the tree and the text, so it is written once and never
	// rewritten under another server's check of the same cache.
	sum := sha256.Sum256([]byte(pathKey(r.dir) + "\x00" + text))
	dir := filepath.Join(s.stdCacheDir(), "lsp", "browse-"+hex.EncodeToString(sum[:8]))
	path := filepath.Join(dir, "browse.mk")
	if old, err := os.ReadFile(path); err == nil && string(old) == text {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "browse-*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		if old, rerr := os.ReadFile(path); rerr == nil && string(old) == text {
			return path, nil // another server wrote the same file first
		}
		return "", err
	}
	return path, nil
}

// stdRootFiles are a std root's files: those of the open std files'
// module directories (not their subdirectories, which are other modules).
func (s *Server) stdRootFiles(r *root) []string {
	dirs := map[string]string{}
	for _, d := range s.docs.all() {
		if foldKey(rootKeyOf(d.path)) == foldKey(r.key) {
			dirs[pathKey(filepath.Dir(d.path))] = filepath.Dir(d.path)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".mk") && e.Name() != "pkg.mk" {
				p := filepath.Join(dir, e.Name())
				seen[pathKey(p)] = true
				out = append(out, p)
			}
		}
	}
	for _, d := range s.docs.all() {
		if !seen[pathKey(d.path)] && foldKey(rootKeyOf(d.path)) == foldKey(r.key) {
			out = append(out, d.path)
		}
	}
	sort.Strings(out)
	return out
}

// declHover is the hover of the declaration the cursor's name resolves to
// lexically (defs): its head up to the body, and the comment above it.
func (s *Server) declHover(c cursor) (hover, bool) {
	for _, l := range s.defs(c) {
		h, ok := s.declAt(l)
		if !ok {
			// A field of an error's payload, a label, a pattern binding:
			// the line that declares it.
			if v, ok := s.labelHover(c, l); ok {
				return s.hoverAtCursor(c, v), true
			}
			if v, ok := s.lineHover(l); ok {
				return s.hoverAtCursor(c, v), true
			}
			continue
		}
		if v, ok := s.declMarkdown(c, h); ok {
			return s.hoverAtCursor(c, v), true
		}
	}
	return hover{}, false
}

// hoverAtCursor is markdown v as the hover of the cursor's token.
func (s *Server) hoverAtCursor(c cursor, v string) hover {
	t := c.ct[c.i]
	rng := c.f.text.span(t.Start, t.End, s.utf8.Load())
	return hover{Contents: markupContent{Kind: "markdown", Value: v}, Range: &rng}
}

// lineHover is the trimmed line holding location l, with its module and
// the checker's type there, as hover markdown.
func (s *Server) lineHover(l Location) (string, bool) {
	p, ok := uriPath(l.URI)
	if !ok {
		return "", false
	}
	f, ok := s.fileOf(p)
	if !ok {
		return "", false
	}
	line := strings.TrimSpace(f.text.line(l.Range.Start.Line))
	if line == "" {
		return "", false
	}
	v := "```mk\n" + line + "\n"
	if m := s.moduleOf(p); m != "" {
		v += "module " + m + "\n"
	}
	v += "```"
	if t, ok := s.typeAt(f, f.text.off(l.Range.End, s.utf8.Load())); ok {
		v += "\n\nchecked type: `" + t + "`"
	}
	return v, true
}

// maxShownItem is the most lines of a struct, enum, error, interface or
// type a hover shows whole; a longer one shows its head.
const maxShownItem = 20

// declHead is a declaration's text from its start to its body's `{` (or its
// end), each line right-trimmed and dedented by the first line's indent. A
// short type declaration is shown whole, with its fields or variants.
func declHead(f srcFile, d decl) string {
	text := f.text.s
	if d.End > len(text) || d.Start > d.End {
		return ""
	}
	end := d.End
	whole := false
	switch d.Detail {
	case "struct", "enum", "error", "interface", "type":
		whole = strings.Count(text[d.Start:d.End], "\n") < maxShownItem
	}
	if !whole {
		depth := 0
	scan:
		for _, t := range code(f.toks) {
			if t.Start < d.NameEnd {
				continue
			}
			if t.Start >= d.End {
				break
			}
			switch text[t.Start:t.End] {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
			case "{":
				if depth == 0 {
					end = t.Start
					break scan
				}
			}
		}
		if end == d.End {
			end = sigEnd(text, end)
		}
	}
	start := strings.LastIndexByte(text[:d.Start], '\n') + 1
	lines := strings.Split(text[start:end], "\n")
	indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " \t"))
	var out []string
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		if len(l) >= indent && strings.TrimSpace(l[:indent]) == "" {
			l = l[indent:]
		}
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// sigEnd extends a bodiless declaration, whose item ends with its first
// line, over the lines that continue a signature (a
// line beginning with from, ends, *, requires or ensures, or with cpu or
// section and a string).
func sigEnd(text string, end int) int {
	for {
		nl := strings.IndexByte(text[end:], '\n')
		if nl < 0 {
			return end
		}
		if rest := strings.TrimSpace(text[end : end+nl]); rest != "" && !strings.HasPrefix(rest, "//") {
			return end
		}
		from := end + nl + 1
		to := strings.IndexByte(text[from:], '\n')
		if to < 0 {
			to = len(text) - from
		}
		if !continuesSig(strings.TrimSpace(text[from : from+to])) {
			return end
		}
		end = from + to
	}
}

// continuesSig reports whether a trimmed line continues the signature above.
func continuesSig(l string) bool {
	for _, w := range []string{"from", "ends", "requires", "ensures"} {
		if rest, ok := strings.CutPrefix(l, w); ok && (rest == "" || rest[0] == ' ' || rest[0] == '	') {
			return true
		}
	}
	if strings.HasPrefix(l, "*") {
		return true
	}
	for _, w := range []string{"cpu", "section"} {
		if rest, ok := strings.CutPrefix(l, w); ok && strings.HasPrefix(strings.TrimSpace(rest), "\"") {
			return true
		}
	}
	return false
}

// docAbove is the run of `//` comment lines right above offset at's line,
// with the slashes and one space removed; "" when there is none.
func docAbove(text string, at int) string {
	start := strings.LastIndexByte(text[:at], '\n') + 1
	var lines []string
	for start > 0 {
		prev := strings.LastIndexByte(text[:start-1], '\n') + 1
		l := strings.TrimSpace(text[prev : start-1])
		if !strings.HasPrefix(l, "//") {
			break
		}
		l = strings.TrimPrefix(strings.TrimPrefix(l, "///"), "//")
		lines = append([]string{strings.TrimPrefix(l, " ")}, lines...)
		start = prev
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// moduleOf names the module of a file in a std tree (`std.atomic`), else "".
func (s *Server) moduleOf(path string) string {
	tree, ok := strings.CutPrefix(rootKeyOf(path), "std:")
	if !ok {
		return ""
	}
	rel, err := filepath.Rel(tree, filepath.Dir(path))
	if err != nil {
		return ""
	}
	if rel == "." {
		return "std"
	}
	return "std." + strings.ReplaceAll(filepath.ToSlash(rel), "/", ".")
}
