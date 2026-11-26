// Why: some names have no item of their own: a payload field, a loop label, a
// generic parameter, a member of a type only the checker knows.
//
// What: labelDefs, recordFieldDefs, receiverTypes, afterDot, isMemberAt and
// isImportLine.
//
// How: each case is read from the tokens around the name; a receiver whose type
// only the checker knows (`xs.Len()` on a slice) is typed by the last check.

package lsp

import (
	"path/filepath"
	"sort"
	"strings"
)

// afterDot reports whether the cursor's name follows a `.`: a member, even
// when its receiver is no plain name (`(e).M`, `xs[i].M`).
func afterDot(c cursor) bool {
	return isMemberAt(c.f.text.s, c.ct, c.i)
}

// isMemberAt reports whether ct[i] follows a member `.`, not the second
// dot of a range `a..b`.
func isMemberAt(text string, ct []token, i int) bool {
	return i > 0 && word(text, ct, i-1) == "." && (i < 2 || word(text, ct, i-2) != "." || ct[i-2].End != ct[i-1].Start)
}

// isImportLine reports whether offset off is on an `import` line.
func isImportLine(text string, off int) bool {
	start := strings.LastIndexByte(text[:off], '\n') + 1
	return strings.HasPrefix(strings.TrimSpace(text[start:off]), "import")
}

// labelDefs answers a loop label's use, a generic parameter, and a field
// label; nil for any other name. ct[a..b] is the item.
func (s *Server) labelDefs(c cursor, a, b int) []Location {
	text, ct, i := c.f.text.s, c.ct, c.i
	u8 := s.utf8.Load()
	at := func(f srcFile, k int) Location {
		return Location{URI: s.uriOf(f.path), Range: f.text.span(code(f.toks)[k].Start, code(f.toks)[k].End, u8)}
	}
	here := func(k int) []Location {
		return []Location{{URI: s.uriOf(c.f.path), Range: c.f.text.span(ct[k].Start, ct[k].End, u8)}}
	}
	prev, next := word(text, ct, i-1), word(text, ct, i+1)
	if prev == "break" || prev == "continue" {
		for k := a; k < i; k++ {
			if word(text, ct, k) == c.name && word(text, ct, k+1) == ":" && word(text, ct, k+2) == "for" {
				return here(k)
			}
		}
	}
	if k, ok := genericParam(text, ct, a, b, c.name); ok {
		return here(k)
	}
	// A method of an impl or interface: the impl's own generics too
	// (`impl[T] Slice[T] for List[T]`).
	for _, r := range items(text, ct) {
		if r[0] <= i && i <= r[1] && r[0] != a {
			if k, ok := genericParam(text, ct, r[0], r[1], c.name); ok {
				return here(k)
			}
		}
	}
	if next != ":" || word(text, ct, i+2) == "=" {
		return nil
	}
	if word(text, ct, i+2) == "for" {
		return here(i) // a loop label is its own declaration
	}
	// The call or construction whose parenthesis holds the label.
	j, d := -1, 0
	for k := i - 1; k >= a && j < 0; k-- {
		switch depthDelta(text, ct[k]) {
		case -1:
			d++
		case 1:
			if d > 0 {
				d--
			} else if word(text, ct, k) == "(" {
				j = k
			} else {
				k = a // a `{` or `[`: no call
			}
		}
	}
	if j < 0 {
		return nil
	}
	callee := j - 1
	if word(text, ct, callee) == "]" { // Route[C](...)
		for d, k := 0, callee; k > a; k-- {
			switch word(text, ct, k) {
			case "]":
				d++
			case "[":
				d--
			}
			if d == 0 {
				callee = k - 1
				break
			}
		}
	}
	if !isIdent(ct, callee) || keywords[word(text, ct, callee)] {
		return here(i) // a record `(acc: x, n: y)`, `return (a: x)`: the label is the field
	}
	name := word(text, ct, callee)
	r := s.rootOfPath(c.f.path)
	dir := pathKey(filepath.Dir(c.f.path))
	var near, far []Location
	named := func(d decl) bool { return d.Name == name }
	cands := append(append(s.search(r, named), s.searchPaths(r.depFiles, named)...), s.searchStd("", named)...)
	// A type (a struct, an error, a variant) before a function of the name.
	sort.SliceStable(cands, func(x, y int) bool { return cands[x].decl.Kind != symFunction && cands[y].decl.Kind == symFunction })
	for _, h := range cands {
		hct := code(h.f.toks)
		for k := tokenIndex(hct, h.decl.Start); k >= 0 && k < len(hct) && hct[k].Start < h.decl.End; k++ {
			if (h.decl.Kind == symFunction || h.decl.Kind == symMethod) && h.f.text.s[hct[k].Start:hct[k].End] == "{" {
				break // a function's parameters, not its body's labels
			}
			if hct[k].Kind == tIdent && h.f.text.s[hct[k].Start:hct[k].End] == c.name && k+1 < len(hct) && h.f.text.s[hct[k+1].Start:hct[k+1].End] == ":" && (k+2 >= len(hct) || h.f.text.s[hct[k+2].Start:hct[k+2].End] != "=" || hct[k+2].Start != hct[k+1].End) {
				if pathKey(filepath.Dir(h.f.path)) == dir {
					near = append(near, at(h.f, k))
				} else {
					far = append(far, at(h.f, k))
				}
				break
			}
		}
	}
	if len(near) > 0 {
		return near[:1]
	}
	if len(far) > 0 {
		return far[:1]
	}
	return nil
}

// genericParam is the index of name as a generic parameter of the item
// ct[a..b]'s head (`F[T: Ord](`, `List[T].Len(`), before its body.
func genericParam(text string, ct []token, a, b int, name string) (int, bool) {
	d := 0
	for k := a; k <= b; k++ {
		w := word(text, ct, k)
		if w == "{" && d == 0 {
			return 0, false
		}
		switch w {
		case "[":
			d++
		case "]":
			d--
		case "(":
			d += 100
		case ")":
			d -= 100
		}
		if d == 1 && isIdent(ct, k) && w == name {
			if p := word(text, ct, k-1); p == "[" || p == "," || p == "const" {
				return k, true
			}
		}
	}
	return 0, false
}

// receiverTypes are the type names a member's receiver has by the
// checker (TypeAt of the expression before the `.`): its base name, and
// Slice for a slice or an array, whose methods std.core declares on
// Slice[T].
func (s *Server) receiverTypes(c cursor) []string {
	if !afterDot(c) || c.i < 2 {
		return nil
	}
	text := c.f.text.s
	if word(text, c.ct, c.i-2) == "]" {
		// A type with arguments, `simd.Vec[u8, 16].Splat`: the type's name.
		for d, k := 0, c.i-2; k > 0; k-- {
			switch word(text, c.ct, k) {
			case "]":
				d++
			case "[":
				d--
			}
			if d == 0 {
				// A type is PascalCase; `xs[i].M` indexes a value.
				if w := word(text, c.ct, k-1); isIdent(c.ct, k-1) && w[0] >= 'A' && w[0] <= 'Z' {
					return []string{w}
				}
				break
			}
		}
	}
	t, ok := s.typeAt(c.f, c.ct[c.i-2].End)
	if !ok {
		return nil
	}
	for {
		u := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(t, "&"), "mut "), "?")
		if u == t {
			break
		}
		t = strings.TrimSpace(u)
	}
	if strings.HasPrefix(t, "[") {
		return []string{"Slice"}
	}
	if i := strings.IndexByte(t, '['); i >= 0 {
		t = t[:i]
	}
	out := []string{lastSegment(t)}
	if b := lastSegment(t); b == "List" || b == "HeapList" {
		// Slice[T]'s methods work on List and HeapList.
		out = append(out, "Slice")
	}
	// A generic parameter: its bounds' interfaces (`K: Ord` gives
	// `c.Less(k)` Ord's Less).
	a, b := itemOf(text, c.ct, c.i)
	if k, ok := genericParam(text, c.ct, a, b, t); ok && word(text, c.ct, k+1) == ":" {
		d := 0
		for j := k + 2; j <= b; j++ {
			w := word(text, c.ct, j)
			if d == 0 && (w == "," || w == "]") {
				break
			}
			switch w {
			case "[", "(":
				d++
			case "]", ")":
				d--
			}
			if d == 0 && isIdent(c.ct, j) && word(text, c.ct, j+1) != "." {
				out = append(out, w)
			}
		}
	}
	if len(out) == 1 {
		out = append(out, s.ownerBounds(c, t)...)
	}
	return out
}

// ownerBounds are the bounds a method's owner type declares for its
// generic parameter t (`struct Heap[T: Ord]` for `Heap[T].Push`'s T).
func (s *Server) ownerBounds(c cursor, t string) []string {
	fn, ok := s.enclosingFunc(c)
	if !ok || fn.Owner == "" {
		return nil
	}
	for _, h := range s.typeDecls(c.f.path, func(d decl) bool { return d.Name == fn.Owner && d.Owner == "" && d.Kind != symMethod }) {
		head := declHead(h.f, h.decl)
		i := strings.Index(head, fn.Owner+"[")
		if i < 0 {
			continue
		}
		rest := head[i+len(fn.Owner)+1:]
		if j := strings.IndexByte(rest, ']'); j >= 0 {
			rest = rest[:j]
		}
		for _, p := range strings.Split(rest, ",") {
			name, bounds, ok := strings.Cut(p, ":")
			if !ok || strings.TrimSpace(name) != t {
				continue
			}
			var out []string
			for _, b := range strings.Split(bounds, "+") {
				b = strings.TrimSpace(b)
				if k := strings.IndexByte(b, '['); k >= 0 {
					b = b[:k]
				}
				if b != "" {
					out = append(out, lastSegment(b))
				}
			}
			return out
		}
	}
	return nil
}

// nameIsLocal reports whether the cursor's bare name is a local in scope
// (a local may hide a module name).
func nameIsLocal(c cursor) bool {
	a, b := itemOf(c.f.text.s, c.ct, c.i)
	_, ok := localDecl(c.f.text.s, c.ct, a, b, c.ct[c.i].Start, c.name)
	return ok
}

// qualIsLocal reports whether the receiver before the cursor's `.` is a
// local in scope, not the import binding it spells.
func qualIsLocal(c cursor) bool {
	if c.qual == "" || c.i < 2 {
		return false
	}
	a, b := itemOf(c.f.text.s, c.ct, c.i)
	_, ok := localDecl(c.f.text.s, c.ct, a, b, c.ct[c.i-2].Start, c.qual)
	return ok
}

// recordFieldDefs is a record field's label in the result type of the
// function whose call bound the receiver (`raw := try ReadHead(..)`, then
// `raw.text` to `(text: str, skipped: int)` in ReadHead's signature).
func (s *Server) recordFieldDefs(c cursor) []Location {
	if c.qual == "" || c.i < 2 {
		return nil
	}
	text, ct := c.f.text.s, c.ct
	a, b := itemOf(text, ct, c.i)
	at, ok := localDecl(text, ct, a, b, ct[c.i-2].Start, c.qual)
	if !ok {
		return nil
	}
	k := tokenIndex(ct, at)
	if k < 0 || word(text, ct, k+1) != ":" || word(text, ct, k+2) != "=" {
		return nil
	}
	j := k + 3
	for word(text, ct, j) == "try" {
		j++
	}
	for isIdent(ct, j) && word(text, ct, j+1) == "." {
		j += 2
	}
	if !isIdent(ct, j) || word(text, ct, j+1) != "(" {
		return nil
	}
	callee := word(text, ct, j)
	r := s.rootOfPath(c.f.path)
	u8 := s.utf8.Load()
	pred := func(d decl) bool { return d.Name == callee && (d.Kind == symFunction || d.Kind == symMethod) }
	for _, h := range append(s.search(r, pred), s.searchStd("", pred)...) {
		hct := code(h.f.toks)
		ht := h.f.text.s
		// Past the parameter list: the result type's labels.
		k := tokenIndex(hct, h.decl.NameEnd)
		for k >= 0 && k < len(hct) && word(ht, hct, k) != "(" {
			k++
		}
		if k < 0 || k >= len(hct) {
			continue
		}
		for k = matching(ht, hct, k) + 1; k+1 < len(hct) && hct[k].Start < h.decl.End && word(ht, hct, k) != "{"; k++ {
			if hct[k].Kind == tIdent && word(ht, hct, k) == c.name && word(ht, hct, k+1) == ":" {
				return []Location{{URI: s.uriOf(h.f.path), Range: h.f.text.span(hct[k].Start, hct[k].End, u8)}}
			}
		}
	}
	return nil
}

// isFieldLabel reports whether ct[i] is a field label `x:` in a call's or a
// record's parentheses, not a parameter being declared: then it names the
// field, even where a local of that name is in scope (`BtHit(i: i)`).
func isFieldLabel(text string, ct []token, a, b, i int) bool {
	return word(text, ct, i+1) == ":" && word(text, ct, i+2) != "=" &&
		enclosingOpen(text, ct, a, i) == "(" && localDeclKind(text, ct, a, b, i) == declNone
}
