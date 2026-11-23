// Why: definition, references, symbols and completion need a file's declarations
// even when the file does not parse.
//
// What: decl and decls (the items of a file with their children), the item
// splitter, member readers, and localDecl and localRefs for the locals of
// one function.
//
// How: reads tokens at bracket depth 0: an item starts at a token that begins a
// line at depth 0, unless the line continues a signature. Locals are found
// by scanning the item for `name :=`, `var name`, parameters, pattern and
// `for` bindings, with each binding's scope end.

package lsp

import "strings"

// decl is one declared name of a file.
type decl struct {
	Name     string
	Owner    string // the type of a method `T.M`; "" otherwise
	Kind     int    // LSP SymbolKind
	Detail   string // the declaring word: struct, enum, fn, method, ...
	Exported bool
	// Start and End bound the item's text; NameStart and NameEnd its name.
	Start, End         int
	NameStart, NameEnd int
	Children           []decl
	// first and last are the item's token indexes in the code tokens.
	first, last int
}

// depthDelta is a bracket's effect on the depth.
func depthDelta(text string, t token) int {
	if t.Kind != tPunct {
		return 0
	}
	switch text[t.Start] {
	case '{', '(', '[':
		return 1
	case '}', ')', ']':
		return -1
	}
	return 0
}

func word(text string, ct []token, i int) string {
	if i < 0 || i >= len(ct) {
		return ""
	}
	return text[ct[i].Start:ct[i].End]
}

func isIdent(ct []token, i int) bool { return i >= 0 && i < len(ct) && ct[i].Kind == tIdent }

// matching is the index of the bracket closing the one at i, or len(ct)-1.
func matching(text string, ct []token, i int) int {
	d := 0
	for j := i; j < len(ct); j++ {
		d += depthDelta(text, ct[j])
		if d == 0 {
			return j
		}
	}
	return len(ct) - 1
}

// items splits the code tokens into items: an item starts at a token that
// begins a line at bracket depth 0.
func items(text string, ct []token) [][2]int {
	var out [][2]int
	d, start := 0, -1
	for i, t := range ct {
		if d == 0 && (t.NL || i == 0) && (i == 0 || !continuesItem(text, ct, i)) {
			if start >= 0 {
				out = append(out, [2]int{start, i - 1})
			}
			start = i
		}
		d = max(0, d+depthDelta(text, t))
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(ct) - 1})
	}
	return out
}

// continuesItem reports whether the line that ct[i] starts continues the
// item above: a signature's `from`, `ends`, `*`,
// `requires`, `ensures`, `cpu` or `section` with a string, or the body's
// `{` on a line of its own.
func continuesItem(text string, ct []token, i int) bool {
	switch word(text, ct, i) {
	case "from", "ends", "*", "requires", "ensures", "{":
		return true
	case "cpu", "section":
		return i+1 < len(ct) && ct[i+1].Kind == tString
	}
	return false
}

// decls is the file's declarations in source order.
func decls(text string, toks []token) []decl {
	ct := code(toks)
	var out []decl
	for _, r := range items(text, ct) {
		if d, ok := itemDecl(text, ct, r[0], r[1]); ok {
			out = append(out, d)
		}
	}
	return out
}

func (d *decl) setName(ct []token, i int) {
	d.NameStart, d.NameEnd = ct[i].Start, ct[i].End
}

// itemDecl reads the head of the item ct[a..b].
func itemDecl(text string, ct []token, a, b int) (decl, bool) {
	d := decl{Start: ct[a].Start, End: ct[b].End, first: a, last: b}
	j := a
	for j <= b {
		switch word(text, ct, j) {
		case "export":
			d.Exported = true
			j++
			continue
		case "linear", "mut", "view", "naked", "unsafe", "open":
			j++
			continue
		case "packed", "align":
			j++
			if word(text, ct, j) == "(" {
				j = matching(text, ct, j) + 1
			}
			continue
		}
		break
	}
	if j > b {
		return d, false
	}
	w := word(text, ct, j)
	named := func(kind int, detail string) (decl, bool) {
		if !isIdent(ct, j+1) || j+1 > b {
			return d, false
		}
		d.Kind, d.Detail, d.Name = kind, detail, word(text, ct, j+1)
		d.setName(ct, j+1)
		return d, true
	}
	switch w {
	case "import", "gen":
		return d, false
	case "struct":
		d, ok := named(symStruct, "struct")
		d.Children = members(text, ct, j+2, b, "struct")
		return d, ok
	case "enum":
		d, ok := named(symEnum, "enum")
		d.Children = members(text, ct, j+2, b, "enum")
		return d, ok
	case "interface":
		d, ok := named(symInterface, "interface")
		d.Children = members(text, ct, j+2, b, "interface")
		return d, ok
	case "error":
		return named(symStruct, "error")
	case "type":
		return named(symTypeParam, "type")
	case "const":
		return named(symConstant, "const")
	case "var":
		return named(symVariable, "var")
	case "extern":
		j++
		if word(text, ct, j) == "var" {
			return named(symVariable, "extern var")
		}
		if word(text, ct, j) == "naked" {
			j++
		}
	case "test":
		if j+1 <= b && ct[j+1].Kind == tString {
			d.Kind, d.Detail, d.Name = symFunction, "test", "test "+word(text, ct, j+1)
			d.setName(ct, j+1)
			return d, true
		}
		return d, false
	case "impl":
		k := j + 1
		var parts []string
		for k <= b && word(text, ct, k) != "{" {
			parts = append(parts, word(text, ct, k))
			k++
		}
		d.Kind, d.Detail = symNamespace, "impl"
		d.Name = "impl " + joinTokens(parts)
		d.setName(ct, j)
		d.Children = members(text, ct, j+1, b, "impl")
		return d, true
	}
	return funcDecl(text, ct, j, b, d)
}

// funcDecl reads `Name(`, `Name[..](`, `T.Name(` or `T[..].Name(` at j.
func funcDecl(text string, ct []token, j, b int, d decl) (decl, bool) {
	if !isIdent(ct, j) || keywords[word(text, ct, j)] {
		return d, false
	}
	k := j + 1
	if word(text, ct, k) == "[" {
		k = matching(text, ct, k) + 1
	}
	switch word(text, ct, k) {
	case "(":
		d.Kind, d.Detail, d.Name = symFunction, "fn", word(text, ct, j)
		d.setName(ct, j)
		return d, true
	case ".":
		if isIdent(ct, k+1) && k+1 <= b {
			n := k + 2
			if word(text, ct, n) == "[" {
				n = matching(text, ct, n) + 1
			}
			if word(text, ct, n) != "(" {
				return d, false
			}
			d.Kind, d.Detail = symMethod, "method"
			d.Owner, d.Name = word(text, ct, j), word(text, ct, k+1)
			d.setName(ct, k+1)
			return d, true
		}
	}
	return d, false
}

// joinTokens spells a token run with spaces between words, none around
// brackets and dots, and one after a comma.
func joinTokens(parts []string) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 && !strings.Contains(",[]().", p) && !strings.Contains("[(.", parts[i-1]) {
			sb.WriteByte(' ')
		}
		sb.WriteString(p)
	}
	return sb.String()
}

// members reads the body `{ ... }` of a struct, enum, interface or impl
// that starts at or after ct[from], up to ct[b]: the names that begin a
// line at depth 1.
func members(text string, ct []token, from, b int, what string) []decl {
	open := -1
	for k := from; k <= b; k++ {
		if word(text, ct, k) == "{" {
			open = k
			break
		}
		if word(text, ct, k) == "(" || word(text, ct, k) == "[" {
			k = matching(text, ct, k)
		}
	}
	if open < 0 {
		return nil
	}
	end := min(matching(text, ct, open), b)
	var out []decl
	d := 0
	for k := open + 1; k < end; k++ {
		t := ct[k]
		if d == 0 && (t.NL || k == open+1) {
			if m, ok := member(text, ct, k, end, what); ok {
				out = append(out, m)
			}
		}
		d = max(0, d+depthDelta(text, t))
	}
	return out
}

func member(text string, ct []token, k, end int, what string) (decl, bool) {
	m := decl{Start: ct[k].Start, first: k}
	if word(text, ct, k) == "export" {
		m.Exported = true
		k++
	}
	if !isIdent(ct, k) || k >= end {
		return m, false
	}
	w := word(text, ct, k)
	next := word(text, ct, k+1)
	switch what {
	case "struct":
		if w == "invariant" || next != ":" {
			return m, false
		}
		m.Kind, m.Detail = symField, "field"
	case "enum":
		m.Kind, m.Detail = symEnumMember, "variant"
	case "interface", "impl":
		if next != "(" && next != "[" {
			return m, false
		}
		m.Kind, m.Detail = symMethod, "method"
	}
	m.Name = w
	m.setName(ct, k)
	m.End = lineEndTok(text, ct, k, end)
	return m, true
}

// lineEndTok is the end offset of the member that starts at ct[k]: its
// last token before the next line at the same depth, or before end.
func lineEndTok(text string, ct []token, k, end int) int {
	d, last := 0, k
	for j := k; j < end; j++ {
		if j > k && d == 0 && ct[j].NL {
			break
		}
		d = max(0, d+depthDelta(text, ct[j]))
		last = j
	}
	return ct[last].End
}

// localDecl is the offset of the declaration of name in the item ct[a..b]
// that is visible at off: a parameter of its signature or of a `fn`
// literal, `name :=`, `var name`, or a `for` binding, declared before off
// and whose scope (localScopeEnd) reaches off. A local cannot shadow a
// local (E0206), so the nearest such declaration is the one
// meant; two sibling `for i` loops are two locals.
func localDecl(text string, ct []token, a, b, off int, name string) (int, bool) {
	best := -1
	for k := a; k <= b; k++ {
		if ct[k].Start >= off && (ct[k].Kind == tPunct && text[ct[k].Start:ct[k].End] == "{") {
			break // past off, only the signature's later parameters count
		}
		if ct[k].Kind != tIdent || word(text, ct, k) != name {
			continue
		}
		kind := localDeclKind(text, ct, a, b, k)
		if kind == declNone {
			continue
		}
		// A parameter is in scope in its whole signature (`x: mut Arena
		// into st, .., st: mut Out`); any other local after off is not.
		if ct[k].Start >= off && (kind != declParam || best >= 0) {
			if kind != declParam {
				break
			}
			continue
		}
		if end := localScopeEnd(text, ct, a, b, k, kind); end < b && ct[end].End < off {
			continue
		}
		best = ct[k].Start
	}
	return best, best >= 0
}

// The forms of a local's declaration.
const (
	declNone    = iota
	declBinding // `name :=`, `var name`
	declFor     // a `for` binding
	declParam   // a parameter of the signature or of a `fn` literal
)

// localDeclKind is the form of declaration ct[k] is in the item ct[a..b],
// or declNone.
func localDeclKind(text string, ct []token, a, b, k int) int {
	if ct[k].Kind != tIdent {
		return declNone
	}
	prev, next := word(text, ct, k-1), word(text, ct, k+1)
	switch {
	case next == ":" && k+2 <= b && word(text, ct, k+2) == "=" && ct[k+2].Start == ct[k+1].End:
		return declBinding
	case prev == "var":
		return declBinding
	case (next == "in" || next == ",") && (prev == "for" && forStmt(text, ct, k-1) || prev == "mut" && k > a && word(text, ct, k-2) == "for" && forStmt(text, ct, k-2)):
		return declFor // not `impl I for T`, not `for cond {`
	case next == ":" && (prev == "(" || prev == "," || prev == "const") && isParam(text, ct, a, k):
		return declParam
	case prev == "," && next == "in":
		return declFor
	case prev == "," && word(text, ct, k-3) == "for":
		return declFor
	case (prev == "(" || prev == "," || prev == ":" && isIdent(ct, k-2) && (word(text, ct, k-3) == "(" || word(text, ct, k-3) == ",")) && patternBinding(text, ct, a, b, k):
		return declBinding // `some(x) {` in a match, `(a, b) := e`, `P(x, y) := p`
	case prev == "else" && next == "{":
		return declBinding // `F() else e { }`: the error
	case next == ":" && ct[k].NL && word(text, ct, k+2) != "for" && enclosingOpen(text, ct, a, k) == "{":
		return declBinding // `x: T = e`, `x: T` at the start of a statement
	}
	return declNone
}

// patternBinding reports whether ct[k], a name in parentheses, is bound by
// a pattern: the parenthesis closes before `{` on a line the pattern
// starts (a match arm), or before `:=` (a destructuring).
func patternBinding(text string, ct []token, a, b, k int) bool {
	if next := word(text, ct, k+1); next != ")" && next != "," {
		return false
	}
	j := -1
	for d, x := 0, k-1; x >= a && j < 0; x-- {
		switch depthDelta(text, ct[x]) {
		case -1:
			d++
		case 1:
			if d > 0 {
				d--
			} else if word(text, ct, x) == "(" {
				j = x
			} else {
				return false
			}
		}
	}
	if j < 0 {
		return false
	}
	m := matchingIn(text, ct, j, b)
	if m >= b {
		return false
	}
	// A nested pattern closes its outer heads too: `err(cli.Unknown(arg)) {`.
	for m+1 < b && word(text, ct, m+1) == ")" {
		m++
	}
	after := word(text, ct, m+1)
	if after == ":" && word(text, ct, m+2) == "=" {
		return true
	}
	if after != "{" {
		return false
	}
	// The pattern's head starts its line: `some(x) {`, `.V(x) {`, `Status(code) {`.
	h := j - 1
	for h > a && (isIdent(ct, h) || word(text, ct, h) == "." || word(text, ct, h) == "(") && !ct[h].NL {
		if keywords[word(text, ct, h)] {
			return false // `export F(`, `fn(`: a signature, not a pattern
		}
		h--
	}
	if h == a || keywords[word(text, ct, h)] {
		return false // the item's own signature
	}
	return ct[h].NL && (isIdent(ct, h) || word(text, ct, h) == ".") && word(text, ct, h) != "if" && word(text, ct, h) != "for" && word(text, ct, h) != "match"
}

// forStmt reports whether the `for` at ct[k] starts a loop: the first
// token of its line, or after a label's `:` or a `{`.
func forStmt(text string, ct []token, k int) bool {
	if k < 0 || k >= len(ct) {
		return false
	}
	p := word(text, ct, k-1)
	return ct[k].NL || p == ":" || p == "{"
}

// enclosingOpen is the innermost bracket of ct[a..k) still open at k, or "".
func enclosingOpen(text string, ct []token, a, k int) string {
	d := 0
	for j := k - 1; j >= a; j-- {
		switch depthDelta(text, ct[j]) {
		case -1:
			d++
		case 1:
			if d > 0 {
				d--
				continue
			}
			return word(text, ct, j)
		}
	}
	return ""
}

// localScopeEnd is the index of the last token of ct[a..b] where the local
// declared at ct[k] is visible: a binding's enclosing block, a `for`
// binding's loop body, a `fn` literal parameter's body, and the whole item
// for a parameter of the signature.
func localScopeEnd(text string, ct []token, a, b, k, kind int) int {
	switch kind {
	case declFor:
		return blockAfter(text, ct, k+1, b)
	case declParam:
		// The parenthesis opening the parameter list.
		d := 0
		for j := k - 1; j >= a; j-- {
			switch depthDelta(text, ct[j]) {
			case -1:
				d++
			case 1:
				if d > 0 {
					d--
					continue
				}
				if word(text, ct, j) == "(" && word(text, ct, j-1) == "fn" {
					return blockAfter(text, ct, matchingIn(text, ct, j, b)+1, b)
				}
				return b
			}
		}
		return b
	}
	// The innermost `{` open at k.
	d := 0
	for j := k - 1; j >= a; j-- {
		switch depthDelta(text, ct[j]) {
		case -1:
			d++
		case 1:
			if d > 0 {
				d--
				continue
			}
			if word(text, ct, j) == "{" {
				return matchingIn(text, ct, j, b)
			}
		}
	}
	return b
}

// blockAfter is the index of the `}` closing the first `{` of ct[from..b]
// outside brackets (a loop's or a literal's body), or b.
func blockAfter(text string, ct []token, from, b int) int {
	d := 0
	for j := from; j <= b; j++ {
		if d == 0 && word(text, ct, j) == "{" {
			return matchingIn(text, ct, j, b)
		}
		d = max(0, d+depthDelta(text, ct[j]))
	}
	return b
}

// matchingIn is matching bounded by b: the bracket closing the one at i,
// or b.
func matchingIn(text string, ct []token, i, b int) int {
	d := 0
	for j := i; j <= b && j < len(ct); j++ {
		d += depthDelta(text, ct[j])
		if d == 0 {
			return j
		}
	}
	return min(b, len(ct)-1)
}

// notLocalUse reports whether the identifier ct[k] cannot name a local: a
// member after `.` (`p.x`; not the end of a range `0..n`), or a label
// before `:` that declares nothing (`Pt(x: 1)`).
func notLocalUse(text string, ct []token, a, b, k int) bool {
	if word(text, ct, k-1) == "." && word(text, ct, k-2) != "." {
		return true
	}
	if word(text, ct, k+1) == ":" && !(word(text, ct, k+2) == "=" && k+2 < len(ct) && ct[k+2].Start == ct[k+1].End) {
		return localDeclKind(text, ct, a, b, k) == declNone
	}
	return false
}

// localRefs are the indexes of the tokens of ct[a..b] that name the local
// declared at ct[d]: the declaration, and each use whose visible
// declaration it is (not a field `p.x`, a label `x:`, or a local
// of the same name in a sibling scope).
func localRefs(text string, ct []token, a, b, d int) []int {
	name := word(text, ct, d)
	out := []int{}
	for k := a; k <= b; k++ {
		if ct[k].Kind != tIdent || word(text, ct, k) != name {
			continue
		}
		if k == d {
			out = append(out, k)
			continue
		}
		if notLocalUse(text, ct, a, b, k) {
			continue
		}
		if at, ok := localDecl(text, ct, a, b, ct[k].End, name); ok && at == ct[d].Start {
			out = append(out, k)
		}
	}
	return out
}

// tokenIndex is the index of the token of ct starting at off, or -1.
func tokenIndex(ct []token, off int) int {
	lo, hi := 0, len(ct)
	for lo < hi {
		m := (lo + hi) / 2
		if ct[m].Start < off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(ct) && ct[lo].Start == off {
		return lo
	}
	return -1
}

// isParam reports whether the `name:` at ct[k] declares a parameter: it is
// inside the parentheses of the item's signature (before its body), or of
// a `fn(` literal; not a named argument of a call (`Pair(lo: 1)`).
func isParam(text string, ct []token, a, k int) bool {
	d := 0
	for j := k - 1; j >= a; j-- {
		switch depthDelta(text, ct[j]) {
		case -1:
			d++
		case 1:
			if d > 0 {
				d--
				continue
			}
			if word(text, ct, j) != "(" {
				return word(text, ct, j) == "[" // a generic parameter list
			}
			if word(text, ct, j-1) == "fn" {
				return true
			}
			// The signature's parentheses: no `{` at the item's depth before.
			depth := 0
			for m := a; m < j; m++ {
				if depth == 0 && word(text, ct, m) == "{" {
					return false
				}
				depth = max(0, depth+depthDelta(text, ct[m]))
			}
			return true
		}
	}
	return false
}
