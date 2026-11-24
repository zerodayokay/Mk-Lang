// Why: these are the requests a user meets first: hover, definition, references,
// symbols, formatting and quick fixes.
//
// What: hover (with codeHover for error codes), definition and defs, references,
// document symbols, formatting, codeActions and verifyActions, and the
// helpers cursorAt, imports, search and searchStd.
//
// How: the checker's answers come first (`mk query sig`, `kind`, the last check's
// resolved uses); the declaration scan of the root's files and std on disk
// answers where the check cannot. A compiles fix is recompiled before it is
// offered.

package lsp

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"mk/internal/driver"
	"mk/internal/format"
)

// fileForURI is a request's document: the open buffer, else the disk file.
func (s *Server) fileForURI(uri string) (srcFile, *rpcError) {
	path, ok := uriPath(uri)
	if !ok {
		return srcFile{}, invalid(errors.New("not a file URI: " + uri))
	}
	f, ok := s.fileOf(path)
	if !ok {
		return srcFile{}, invalid(errors.New("no such file: " + uri))
	}
	return f, nil
}

// cursor is the identifier under a position.
type cursor struct {
	f    srcFile
	ct   []token // code tokens
	i    int     // the identifier's index in ct
	name string
	qual string // the identifier before an adjacent `.`, if any
}

func (s *Server) cursorAt(uri string, p Position) (cursor, bool, *rpcError) {
	f, e := s.fileForURI(uri)
	if e != nil {
		return cursor{}, false, e
	}
	ct := code(f.toks)
	off := f.text.off(p, s.utf8.Load())
	i := tokenAt(ct, off)
	if i < 0 {
		return cursor{f: f, ct: ct}, false, nil
	}
	c := cursor{f: f, ct: ct, i: i, name: word(f.text.s, ct, i)}
	if i >= 2 && word(f.text.s, ct, i-1) == "." && isIdent(ct, i-2) && ct[i-2].End == ct[i-1].Start {
		c.qual = word(f.text.s, ct, i-2)
	}
	return c, true, nil
}

// item is the index range of the item of ct holding token i.
func itemOf(text string, ct []token, i int) (int, int) {
	for _, r := range items(text, ct) {
		if r[0] <= i && i <= r[1] {
			// A method of an impl or an interface is its own item: its
			// parameters and locals are its.
			if d, ok := itemDecl(text, ct, r[0], r[1]); ok && (d.Detail == "impl" || d.Detail == "interface") {
				for _, ch := range d.Children {
					if ch.Kind == symMethod && ch.Start <= ct[i].Start && ct[i].Start < ch.End {
						a := tokenIndex(ct, ch.Start)
						b := sort.Search(len(ct), func(k int) bool { return ct[k].Start >= ch.End }) - 1
						if a >= 0 && b >= a {
							return a, b
						}
					}
				}
			}
			return r[0], r[1]
		}
	}
	return 0, len(ct) - 1
}

// imports maps the names a file's imports bind to their module paths.
func imports(text string, ct []token) map[string]string {
	out := map[string]string{}
	for _, r := range items(text, ct) {
		if word(text, ct, r[0]) != "import" {
			continue
		}
		var parts []string
		alias := ""
		for k := r[0] + 1; k <= r[1]; k++ {
			w := word(text, ct, k)
			if w == "=" && len(parts) == 1 {
				alias, parts = parts[0], nil
				continue
			}
			if w != "." {
				parts = append(parts, w)
			}
		}
		if len(parts) == 0 {
			continue
		}
		if alias == "" {
			alias = parts[len(parts)-1]
		}
		out[alias] = strings.Join(parts, ".")
	}
	// A manifest reaches std.pkg through its implicit `pkg` binding
	// (std.pkg).
	if _, ok := out["pkg"]; !ok && strings.Contains(text, "pkg.Package(") {
		out["pkg"] = "std.pkg"
	}
	return out
}

// hover answers `mk query sig` for the name, and `kind` for a type.
func (s *Server) hover(p textDocumentPositionParams) (any, *rpcError) {
	if h, ok := s.codeHover(p); ok {
		return h, nil
	}
	c, ok, e := s.cursorAt(p.TextDocument.URI, p.Position)
	if e != nil {
		return nil, e
	}
	if !ok {
		return s.operatorHover(c.f, p.Position), nil
	}
	patternHead := (c.name == "ok" || c.name == "err") && word(c.f.text.s, c.ct, c.i+1) == "(" && c.ct[c.i].NL
	patternHead = patternHead || asmWords[c.name] && insideAsm(c.f.text.s, c.ct, c.i)
	patternHead = patternHead || contextualWord(c)
	// After a dot a rule word is a member's name (`ch.Send()`, `v.Cast()`).
	rule := ruleWords[c.name] && !afterDot(c)
	flow := flowWords[c.name] && !afterDot(c) // `r.from` is a field
	if keywords[c.name] || flow || rule || patternHead {
		if v, ok := keywordHover(c.name); ok {
			v += s.keywordContext(c)
			t := c.ct[c.i]
			rng := c.f.text.span(t.Start, t.End, s.utf8.Load())
			return hover{Contents: markupContent{Kind: "markdown", Value: v}, Range: &rng}, nil
		}
		if keywords[c.name] {
			// A declaration's keyword (`error`, `enum`, `interface`): the
			// hover of the name it declares.
			for k := c.i + 1; k < len(c.ct) && k <= c.i+4; k++ {
				if isIdent(c.ct, k) && !keywords[word(c.f.text.s, c.ct, k)] {
					dc := cursor{f: c.f, ct: c.ct, i: k, name: word(c.f.text.s, c.ct, k)}
					if h, ok := s.nameHover(dc).(hover); ok {
						t := c.ct[c.i]
						rng := c.f.text.span(t.Start, t.End, s.utf8.Load())
						h.Range = &rng
						return h, nil
					}
					break
				}
			}
			return nil, nil
		}
	}
	text := c.f.text.s
	if c.qual == "" && !afterDot(c) {
		a, b := itemOf(text, c.ct, c.i)
		if at, ok := localDecl(text, c.ct, a, b, c.f.text.off(p.Position, s.utf8.Load())+1, c.name); ok && !isFieldLabel(text, c.ct, a, b, c.i) {
			return s.localHoverMD(c, a, b, at), nil
		}
	}
	return s.nameHover(c), nil
}

// codeHover answers a hover on an error code, in code or in a comment
// (`// <- E0501`), with `mk explain`'s text.
func (s *Server) codeHover(p textDocumentPositionParams) (hover, bool) {
	f, e := s.fileForURI(p.TextDocument.URI)
	if e != nil {
		return hover{}, false
	}
	u8 := s.utf8.Load()
	text := f.text.s
	off := f.text.off(p.Position, u8)
	a, b := off, off
	for a > 0 && isIdentByte(text[a-1]) {
		a--
	}
	for b < len(text) && isIdentByte(text[b]) {
		b++
	}
	w := text[a:b]
	if len(w) != 5 || w[0] != 'E' || strings.Trim(w[1:], "0123456789") != "" {
		return hover{}, false
	}
	entry, ok := driver.Explain(w)
	if !ok {
		return hover{}, false
	}
	rng := f.text.span(a, b, u8)
	return hover{Contents: markupContent{Kind: "markdown", Value: "```text\n" + strings.TrimRight(entry.Text(), "\n") + "\n```"}, Range: &rng}, true
}

func hoverOf(c cursor, lines []string, u8 bool) hover {
	t := c.ct[c.i]
	rng := c.f.text.span(t.Start, t.End, u8)
	return hover{Contents: markupContent{Kind: "markdown", Value: "```mk\n" + strings.Join(lines, "\n") + "\n```"}, Range: &rng}
}

// isTypeHead reports whether a sig answer's first line declares a type.
func isTypeHead(l string) bool {
	for _, w := range strings.Fields(l) {
		switch w {
		case "export", "mut", "plain", "view", "linear":
			continue
		case "struct", "enum", "interface", "error", "type":
			return true
		}
		return false
	}
	return false
}

// subjects are the query subjects the cursor's name may mean, best first.
func (s *Server) subjects(r *root, c cursor) []string {
	if c.qual == "" {
		return []string{c.name}
	}
	text := c.f.text.s
	if _, ok := imports(text, c.ct)[c.qual]; ok && !qualIsLocal(c) {
		return []string{c.qual + "." + c.name}
	}
	out := []string{c.qual + "." + c.name}
	// A value receiver: its declared type, else the one type declaring a
	// method of that name.
	a, b := itemOf(text, c.ct, c.i)
	if at, ok := localDecl(text, c.ct, a, b, c.ct[c.i].Start, c.qual); ok {
		if t := s.localType(c, at, a, b); t != "" {
			out = append(out, t+"."+c.name)
		}
	}
	owners := map[string]bool{}
	for _, h := range s.search(r, func(d decl) bool { return d.Owner != "" && d.Name == c.name }) {
		owners[h.decl.Owner] = true
	}
	if len(owners) == 1 {
		for o := range owners {
			out = append(out, o+"."+c.name)
		}
	}
	return out
}

// localType is the type a local was declared with, as the text spells it
// (`x: T`, `x: &mut T`, `x := T.New(..)`, `x := m.T(..)`, `x := T(..)`), or
// "" when the text does not say. For `x :=` the checker's type comes first
// (localTypeAt, exact.go's typeAt).
func (s *Server) localType(c cursor, at, a, b int) string {
	text := c.f.text.s
	k := -1
	for j := a; j <= b; j++ {
		if c.ct[j].Start == at {
			k = j
			break
		}
	}
	if k < 0 {
		return ""
	}
	ct := c.ct
	j := k + 1
	if word(text, ct, j) == ":" && word(text, ct, j+1) != "=" {
		j++
		for word(text, ct, j) == "&" || word(text, ct, j) == "mut" || word(text, ct, j) == "?" {
			j++
		}
		if word(text, ct, j) == "[" {
			return "Slice" // []T and [N]T have Slice[T]'s methods
		}
		return qualified(text, ct, j)
	}
	if word(text, ct, j) == ":" && word(text, ct, j+1) == "=" {
		lt := s.newInfer(c.f, ct).localTypeAt(a, b, k, 0)
		if t := baseType(lt); t != "" {
			return t
		}
		if i := topLevelBang(lt); i >= 0 {
			lt = lt[i+1:]
		}
		for strings.HasPrefix(lt, "?") || strings.HasPrefix(lt, "&") || strings.HasPrefix(lt, "mut ") {
			lt = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(lt, "?"), "&"), "mut ")
		}
		if strings.HasPrefix(lt, "[") {
			return "Slice" // an inferred []T or [N]T
		}
		j += 2
		var segs []string
		for isIdent(ct, j) {
			segs = append(segs, word(text, ct, j))
			if word(text, ct, j+1) != "." {
				break
			}
			j += 2
		}
		imps := imports(text, ct)
		switch {
		case len(segs) == 1:
			return segs[0]
		case len(segs) >= 2 && imps[segs[0]] != "":
			return segs[0] + "." + segs[1]
		case len(segs) >= 2:
			return segs[0]
		}
	}
	return ""
}

// qualified spells `T` or `m.T` at ct[j], or "".
func qualified(text string, ct []token, j int) string {
	if !isIdent(ct, j) {
		return ""
	}
	t := word(text, ct, j)
	if word(text, ct, j+1) == "." && isIdent(ct, j+2) {
		t += "." + word(text, ct, j+2)
	}
	return t
}

// hit is one declaration found in a root's files.
type hit struct {
	f    srcFile
	decl decl
}

// search walks the root's files' declarations, children included.
func (s *Server) search(r *root, pred func(decl) bool) []hit {
	return s.searchPaths(append(s.rootFiles(r), r.genFiles...), pred)
}

// searchStd walks std's files on disk (stdfiles.go), or those of one std
// module directory when dir is not "".
func (s *Server) searchStd(dir string, pred func(decl) bool) []hit {
	var paths []string
	for _, p := range s.stdFiles() {
		if dir == "" || pathKey(filepath.Dir(p)) == pathKey(dir) {
			paths = append(paths, p)
		}
	}
	return s.searchPaths(paths, pred)
}

func (s *Server) searchPaths(paths []string, pred func(decl) bool) []hit {
	var out []hit
	for _, p := range paths {
		f, ok := s.fileOf(p)
		if !ok {
			continue
		}
		var walk func(ds []decl)
		walk = func(ds []decl) {
			for _, d := range ds {
				if pred(d) {
					out = append(out, hit{f: f, decl: d})
				}
				walk(d.Children)
			}
		}
		walk(f.decls)
	}
	return out
}

func (s *Server) location(h hit) Location {
	return Location{URI: s.uriOf(h.f.path), Range: h.f.text.span(h.decl.NameStart, h.decl.NameEnd, s.utf8.Load())}
}

// definition finds the declaration of the name under the cursor.
func (s *Server) definition(p textDocumentPositionParams) (any, *rpcError) {
	c, ok, e := s.cursorAt(p.TextDocument.URI, p.Position)
	if e != nil || !ok || keywords[c.name] {
		return nil, e
	}
	locs := s.defs(c)
	if len(locs) == 0 {
		return nil, nil
	}
	// A generated declaration: then the `gen` lines that made it.
	r := s.rootOfPath(c.f.path)
	seen := map[string]bool{}
	for _, l := range locs {
		if g, ok := s.inGen(r, l); ok && !seen[pathKey(g)] {
			seen[pathKey(g)] = true
			locs = append(locs, s.genLines(r, g, l)...)
		}
	}
	return locs, nil
}

func (s *Server) defs(c cursor) []Location {
	text := c.f.text.s
	u8 := s.utf8.Load()
	r := s.rootOfPath(c.f.path)
	// On a declaration's own name (an item, a method, a field, a variant):
	// that declaration.
	self := Location{URI: s.uriOf(c.f.path), Range: c.f.text.span(c.ct[c.i].Start, c.ct[c.i].End, u8)}
	if h, ok := s.declAt(self); ok && h.decl.Name == c.name {
		return []Location{self}
	}
	// An import binding or a segment of an import's path: the module's
	// main file (hoverdeep.go).
	if mod, ok := s.bindingAt(c); ok && (c.qual == "" || isImportLine(text, c.ct[c.i].Start)) {
		if l, ok := s.moduleLoc(r, mod); ok {
			return []Location{l}
		}
	}
	if c.qual == "" && !afterDot(c) {
		a, b := itemOf(text, c.ct, c.i)
		if at, ok := localDecl(text, c.ct, a, b, c.ct[c.i].End, c.name); ok && !isFieldLabel(text, c.ct, a, b, c.i) {
			return []Location{{URI: s.uriOf(c.f.path), Range: c.f.text.span(at, at+len(c.name), u8)}}
		}
		if l := s.labelDefs(c, a, b); len(l) > 0 {
			return l
		}
		// The same module (directory) first, then the whole root.
		dir := pathKey(filepath.Dir(c.f.path))
		top := func(d decl) bool {
			return d.Owner == "" && d.Name == c.name && d.Kind != symField && d.Kind != symEnumMember && d.Kind != symMethod
		}
		var near, far []Location
		for _, h := range s.search(r, top) {
			if pathKey(filepath.Dir(h.f.path)) == dir {
				near = append(near, s.location(h))
			} else {
				far = append(far, s.location(h))
			}
		}
		if len(near) > 0 {
			return near
		}
		if len(far) > 0 {
			return far
		}
		// A std item: the prelude, or a name the root does not declare;
		// where the last check resolves it, else by the text.
		if l, ok := s.lookupDef(r, c.name, ""); ok {
			return []Location{l}
		}
		return s.stdLocations(s.searchStd("", top))
	}
	if mod, ok := imports(text, c.ct)[c.qual]; ok && !qualIsLocal(c) {
		if sd := s.stdModuleDir(mod); sd != "" {
			if l, ok := s.lookupDef(r, mod+"."+c.name, mod); ok {
				return []Location{l}
			}
			return s.stdLocations(s.searchStd(sd, func(d decl) bool {
				return d.Owner == "" && d.Name == c.name && d.Kind != symField && d.Kind != symEnumMember && d.Kind != symMethod
			}))
		}
		var out []Location
		for _, h := range s.searchModule(r, mod, func(d decl) bool { return d.Owner == "" && d.Name == c.name }) {
			out = append(out, s.location(h))
		}
		return out
	}
	// A type's method, variant or field; else any member of that name.
	var owned, all []Location
	types := map[string]bool{c.qual: true}
	for _, t := range s.receiverTypes(c) {
		types[t] = true
	}
	a, b := itemOf(text, c.ct, c.i)
	if at, ok := localDecl(text, c.ct, a, b, c.ct[c.i].Start, c.qual); ok {
		if t := s.localType(c, at, a, b); t != "" {
			types[lastSegment(t)] = true
		}
	}
	for _, h := range s.search(r, func(d decl) bool {
		return d.Name == c.name && d.Owner != "" || d.Name == c.name && (d.Kind == symField || d.Kind == symEnumMember || d.Kind == symMethod)
	}) {
		all = append(all, s.location(h))
		if types[h.decl.Owner] {
			owned = append(owned, s.location(h))
		}
	}
	// The type's own members, and the methods of its impls (`impl Eq for
	// User`, as a generator emits them).
	ownedBy := func(d decl) bool {
		if d.Detail == "impl" {
			_, typ := implParts(d.Name)
			return types[typ]
		}
		return types[d.Name] && d.Owner == ""
	}
	// The root's, then std's (List[T]'s Items is in std.core's impl of
	// Slice[T]).
	implHits := s.search(r, ownedBy)
	if len(implHits) == 0 || !hasChild(implHits, c.name) {
		implHits = append(implHits, s.searchStd("", ownedBy)...)
	}
	for _, h := range implHits {
		for _, ch := range h.decl.Children {
			if ch.Name == c.name {
				owned = append(owned, s.location(hit{f: h.f, decl: ch}))
			}
		}
	}
	// Two std modules may declare a type of one name (net.Conn, tls.Conn):
	// the module the receiver's type is spelled from picks.
	mdir := s.receiverModuleDir(c)
	owned = inModuleDir(owned, mdir)
	if len(owned) > 0 {
		return owned
	}
	// A member the last check resolves (a std type's, an alias's).
	for _, sub := range s.subjects(r, c) {
		if l, ok := s.lookupDef(r, sub, ""); ok {
			return []Location{l}
		}
	}
	// A std type's member: the method `T.M`, or a field or variant of T.
	var inStd []hit
	for _, h := range s.searchStd("", func(d decl) bool {
		return d.Name == c.name && types[d.Owner] || types[d.Name] && d.Owner == ""
	}) {
		if h.decl.Name == c.name {
			inStd = append(inStd, h)
			continue
		}
		for _, ch := range h.decl.Children {
			if ch.Name == c.name {
				inStd = append(inStd, hit{f: h.f, decl: ch})
			}
		}
	}
	if l := inModuleDir(s.stdLocations(inStd), mdir); len(l) > 0 {
		return l
	}
	if l := s.recordFieldDefs(c); len(l) > 0 {
		return l
	}
	return all
}

// receiverModuleDir is the std directory of the module a member's
// receiver type comes from, when the text spells it: `m.T.name` with m an
// import binding, or a receiver whose type is `m.T`; "" otherwise.
func (s *Server) receiverModuleDir(c cursor) string {
	if !afterDot(c) || c.i < 2 {
		return ""
	}
	text := c.f.text.s
	imps := imports(text, c.ct)
	modOf := func(t string) string {
		for {
			u := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(t, "&"), "mut "), "?"))
			if u == t {
				break
			}
			t = u
		}
		if i := strings.IndexByte(t, '['); i >= 0 {
			t = t[:i]
		}
		j := strings.LastIndexByte(t, '.')
		if j <= 0 {
			return ""
		}
		mod := t[:j]
		if m, ok := imps[mod]; ok {
			mod = m
		}
		return s.stdModuleDir(mod)
	}
	if c.i >= 4 && word(text, c.ct, c.i-3) == "." && isIdent(c.ct, c.i-4) {
		if m, ok := imps[word(text, c.ct, c.i-4)]; ok {
			return s.stdModuleDir(m)
		}
	}
	a, b := itemOf(text, c.ct, c.i)
	if at, ok := localDecl(text, c.ct, a, b, c.ct[c.i].Start, c.qual); ok {
		if t := s.localType(c, at, a, b); t != "" {
			return modOf(t)
		}
	}
	if t, ok := s.typeAt(c.f, c.ct[c.i-2].End); ok {
		return modOf(t)
	}
	return ""
}

// inModuleDir keeps the locations below dir, unless none is or dir is "".
func inModuleDir(locs []Location, dir string) []Location {
	if dir == "" {
		return locs
	}
	var in []Location
	k := pathKey(dir)
	for _, l := range locs {
		if p, ok := uriPath(l.URI); ok && pathKey(filepath.Dir(p)) == k {
			in = append(in, l)
		}
	}
	if len(in) == 0 {
		return locs
	}
	return in
}

// stdLocations are std hits as locations, those of test files only when
// no other file declares the name (stdFiles lists test files last).
func (s *Server) stdLocations(hs []hit) []Location {
	var out, tests []Location
	for _, h := range hs {
		base := filepath.Base(h.f.path)
		if strings.HasPrefix(base, "tests_") || strings.HasSuffix(base, "_test.mk") {
			tests = append(tests, s.location(h))
			continue
		}
		out = append(out, s.location(h))
	}
	if len(out) == 0 {
		return tests
	}
	return out
}

func lastSegment(t string) string {
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t[i+1:]
	}
	return t
}

// references lists a local's tokens in its scope; an item's resolved uses
// from a full check of the current texts (exact.go); else, for a root that
// does not check, every identifier token spelling the name in the root.
func (s *Server) references(p referenceParams) (any, *rpcError) {
	c, ok, e := s.cursorAt(p.TextDocument.URI, p.Position)
	if e != nil || !ok || keywords[c.name] {
		return []Location{}, e
	}
	u8 := s.utf8.Load()
	text := c.f.text.s
	defs := map[string]bool{}
	for _, l := range s.defs(c) {
		defs[l.URI+"#"+posKey(l.Range.Start)] = true
	}
	out := []Location{}
	addFile := func(f srcFile, from, to int) {
		uri := s.uriOf(f.path)
		ct := code(f.toks)
		for k := from; k <= to && k < len(ct); k++ {
			t := ct[k]
			if t.Kind != tIdent || t.End-t.Start != len(c.name) || f.text.s[t.Start:t.End] != c.name {
				continue
			}
			rng := f.text.span(t.Start, t.End, u8)
			if !p.Context.IncludeDeclaration && defs[uri+"#"+posKey(rng.Start)] {
				continue
			}
			out = append(out, Location{URI: uri, Range: rng})
		}
	}
	if c.qual == "" {
		a, b := itemOf(text, c.ct, c.i)
		if at, ok := localDecl(text, c.ct, a, b, c.ct[c.i].End, c.name); ok && !notLocalUse(text, c.ct, a, b, c.i) {
			// The local's own tokens, by scope.
			uri := s.uriOf(c.f.path)
			d := tokenIndex(c.ct, at)
			for _, k := range localRefs(text, c.ct, a, b, d) {
				if k == d && !p.Context.IncludeDeclaration {
					continue
				}
				t := c.ct[k]
				out = append(out, Location{URI: uri, Range: c.f.text.span(t.Start, t.End, u8)})
			}
			return out, nil
		}
	}
	// An item: its resolved uses (exact.go), else every token spelling it.
	if locs, ok := s.exactRefs(c, p.Context.IncludeDeclaration); ok {
		return locs, nil
	}
	r := s.rootOfPath(c.f.path)
	for _, path := range s.rootFiles(r) {
		if f, ok := s.fileOf(path); ok {
			addFile(f, 0, len(f.toks))
		}
	}
	return out, nil
}

func posKey(p Position) string {
	return strings.Join([]string{itoa(p.Line), itoa(p.Character)}, ":")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// symbols is the file's items, methods under their type when it is
// declared in the same file.
func (s *Server) symbols(p documentParams) (any, *rpcError) {
	f, e := s.fileForURI(p.TextDocument.URI)
	if e != nil {
		return nil, e
	}
	u8 := s.utf8.Load()
	var conv func(d decl) documentSymbol
	conv = func(d decl) documentSymbol {
		end := max(d.End, d.NameEnd)
		ds := documentSymbol{Name: d.Name, Detail: d.Detail, Kind: d.Kind,
			Range: f.text.span(d.Start, end, u8), SelectionRange: f.text.span(d.NameStart, d.NameEnd, u8)}
		for _, ch := range d.Children {
			ds.Children = append(ds.Children, conv(ch))
		}
		return ds
	}
	out := []documentSymbol{}
	index := map[string]int{}
	for _, d := range f.decls {
		if d.Owner == "" {
			index[d.Name] = len(out)
			out = append(out, conv(d))
		}
	}
	for _, d := range f.decls {
		if d.Owner == "" {
			continue
		}
		if i, ok := index[d.Owner]; ok {
			out[i].Children = append(out[i].Children, conv(d))
			continue
		}
		ds := conv(d)
		ds.Name = d.Owner + "." + d.Name
		out = append(out, ds)
	}
	return out, nil
}

// formatting runs mk fmt's formatter on the buffer: one edit replacing the
// whole text, or none when it is formatted or does not parse.
func (s *Server) formatting(p documentParams) (any, *rpcError) {
	path, ok := uriPath(p.TextDocument.URI)
	if !ok {
		return nil, invalid(errors.New("not a file URI: " + p.TextDocument.URI))
	}
	raw, err := s.bufferText(path)
	if err != nil {
		return nil, invalid(err)
	}
	out, err := format.Source(path, raw)
	if err != nil {
		return []textEdit{}, nil
	}
	if string(out) == string(raw) {
		return []textEdit{}, nil
	}
	whole := newLines(string(raw))
	return []textEdit{{Range: Range{End: whole.endPos(s.utf8.Load())}, NewText: string(out)}}, nil
}

// codeActions are the last check's fixes of the diagnostics in the range.
func (s *Server) codeActions(p codeActionParams) (any, *rpcError) {
	path, ok := uriPath(p.TextDocument.URI)
	if !ok {
		return nil, invalid(errors.New("not a file URI: " + p.TextDocument.URI))
	}
	r := s.rootOfPath(path)
	out := []codeAction{}
	fds := r.fixes[pathKey(path)]
	var pending []int
	for i, fd := range fds {
		if before(fd.diag.Range.End, p.Range.Start) || before(p.Range.End, fd.diag.Range.Start) {
			continue
		}
		if !fd.verified && (hasCompilesAction(fd.fixes) || hasClauseAction(fd)) {
			pending = append(pending, i)
		}
	}
	if len(pending) > 0 {
		s.verifyActions(r, path, fds, pending)
	}
	for _, fd := range fds {
		if before(fd.diag.Range.End, p.Range.Start) || before(p.Range.End, fd.diag.Range.Start) {
			continue
		}
		out = append(out, fd.fixes...)
	}
	if f, ok := s.fileOf(path); ok {
		out = append(out, s.moreActions(r, path, f, p.Range)...)
	}
	return out, nil
}

// compilesTitle starts the title of a compiles fix's action (mapReport).
const compilesTitle = "compiles: "

func hasCompilesAction(as []codeAction) bool {
	for _, a := range as {
		if strings.HasPrefix(a.Title, compilesTitle) {
			return true
		}
	}
	return false
}

// provenTitle starts the title of a proven fix's action (mapReport).
const provenTitle = "proven: "

// hasClauseAction reports whether fd offers a proven `from` or `into`
// clause (E0601, E0602, E0604), which the driver recompiles too: a clause
// that clears the function may leave a caller with an error no clause
// clears.
func hasClauseAction(fd fixedDiag) bool {
	switch fd.diag.Code {
	case "E0601", "E0602", "E0604":
	default:
		return false
	}
	for _, a := range fd.fixes {
		if strings.HasPrefix(a.Title, provenTitle) {
			return true
		}
	}
	return false
}

// verifyActions checks fixes for the editor: a compiles fix, and a proven
// clause fix, is recompiled before it is offered. It runs when code actions
// are asked for a diagnostic that offers one, not after every check: the
// package is checked again through the driver's overlay (a cache hit) and
// each compiles fix tried alone (driver CheckVerified), and the actions of
// the diagnostics fds[pending] are replaced by the verified ones, where the
// verified check reports the same diagnostic. Each is marked verified, so
// the next request does not recheck; the next check replaces fds.
func (s *Server) verifyActions(r *root, path string, fds []fixedDiag, pending []int) {
	for _, i := range pending {
		fds[i].verified = true
	}
	sess, err := s.session(r)
	if err != nil {
		return
	}
	res, err := sess.CheckVerified()
	if err != nil {
		return
	}
	type key struct {
		code, msg string
		rng       Range
	}
	got := map[key][]codeAction{}
	s.mapReport(r, res, func(p string, d lspDiagnostic, as []codeAction) {
		if pathKey(p) == pathKey(path) {
			got[key{d.Code, d.Message, d.Range}] = as
		}
	})
	for _, i := range pending {
		d := fds[i].diag
		if as, ok := got[key{d.Code, d.Message, d.Range}]; ok {
			fds[i].fixes = as
		}
	}
}

func before(a, b Position) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
}
