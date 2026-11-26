// Why: a package's own C import that reads the host's SDK (sdk: true)
// binds again what a system view binds (HWND, RECT, CreateWindowExW), and
// a package that also imports the view would see two types for one C type;
// they must be one, the view's.
//
// What: ViewIdentity (the C declarations a view binds, by kind and C name,
// with each record's layout), NewViewIdentity, and ShareWithViews, which
// drops from a binding every block a view binds and makes its references
// name the view's item.
//
// How: a block's identity is read from its "// C: " comment: a struct or
// union and its tag (and its size and alignment, which must agree), an enum
// and its tag, a typedef and its name (the comment's last word); a function
// or an extern variable by its name, which is C's. A shared block's lines
// are blanked, so every other line keeps its number, and each name it
// defined is rewritten, wherever another block names it, to view.Name. The
// view's import takes the place of the head's second comment line.

package cimport

import (
	"fmt"
	"strings"

	"mk/internal/sysview"
)

// viewKey is a C declaration's identity across bindings.
type viewKey struct{ kind, name string }

// viewEntry is what a view binds for one key: its module, the names its
// block defines, in order, and a record's layout.
type viewEntry struct {
	view   string
	defs   []string
	layout string
}

// ViewIdentity is the declarations of a set of views, by identity.
type ViewIdentity struct {
	byKey map[viewKey]viewEntry
}

// NewViewIdentity indexes the views of set (every item of each, in the
// order names lists the views: an earlier view owns a declaration both
// bind).
func NewViewIdentity(set sysview.Set, names []string) (*ViewIdentity, error) {
	vi := &ViewIdentity{byKey: map[viewKey]viewEntry{}}
	for _, v := range names {
		a := set[v]
		if a == nil {
			continue
		}
		all := make([]int, a.Len())
		for i := range all {
			all[i] = i
		}
		texts, err := a.Text(all)
		if err != nil {
			return nil, err
		}
		for i, text := range texts {
			kind, defs, err := a.Head(i)
			if err != nil {
				return nil, err
			}
			k, layout, ok := identityOf(sysview.CText(text), kind, defs)
			if !ok {
				continue
			}
			if _, seen := vi.byKey[k]; !seen {
				vi.byKey[k] = viewEntry{view: v, defs: defs, layout: layout}
			}
		}
	}
	return vi, nil
}

// identityOf reads a block's identity from its C comment c, its kind and
// the names it defines.
func identityOf(c, kind string, defs []string) (viewKey, string, bool) {
	switch kind {
	case "func", "var":
		return viewKey{kind, defs[0]}, "", true
	case "method":
		return viewKey{}, "", false // an accessor goes with its record
	}
	switch {
	case strings.HasPrefix(c, "struct "), strings.HasPrefix(c, "union "):
		kw, rest, _ := strings.Cut(c, " ")
		tag, layout, _ := strings.Cut(rest, ",")
		tag, incomplete := strings.CutSuffix(tag, " (incomplete: only pointers to it)")
		if tag == "" || strings.HasPrefix(tag, "(") {
			return viewKey{}, "", false
		}
		if incomplete {
			layout = "incomplete"
		}
		layout, _, _ = strings.Cut(strings.TrimSpace(layout), ":")
		return viewKey{kw, tag}, layout, true
	case strings.HasPrefix(c, "enum "):
		tag := strings.TrimSpace(c[len("enum "):])
		if tag == "" {
			return viewKey{}, "", false
		}
		return viewKey{"enum", tag}, "", true
	case strings.HasPrefix(c, "typedef "):
		f := strings.Fields(c)
		name := f[len(f)-1]
		if name == "" || !isIdent(name) {
			return viewKey{}, "", false
		}
		return viewKey{"typedef", name}, "", true
	}
	return viewKey{}, "", false
}

// isIdent reports a C identifier.
func isIdent(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}

// ShareWithViews rewrites text, one import's binding, so that each
// declaration vi binds is the view's: the block is blanked (with the blocks
// of its accessors), and every use of a name it defined names the view's
// item. It returns the text and the views it now imports, or an error for
// a record whose layout differs from the view's.
func ShareWithViews(text string, vi *ViewIdentity) (string, []string, error) {
	lines := strings.Split(text, "\n")
	head := len(lines)
	for i, l := range lines {
		if strings.HasPrefix(l, "// C:") {
			head = i
			for head > 0 && lines[head-1] != "" {
				head--
			}
			break
		}
	}
	type blk struct{ lo, hi int }
	var blocks []blk
	for i := head; i < len(lines); {
		if lines[i] == "" {
			i++
			continue
		}
		b := blk{lo: i}
		for i < len(lines) && lines[i] != "" {
			i++
		}
		b.hi = i
		blocks = append(blocks, b)
	}
	rename := map[string]string{}
	dropped := map[string]bool{} // names of dropped blocks: their accessors go too
	var drop []blk
	var uses []string
	for _, b := range blocks {
		var defs []string
		kind, c := "", ""
		for _, l := range lines[b.lo:b.hi] {
			if n := defName(l); n != "" {
				defs = append(defs, n)
				if kind == "" {
					kind = itemKind([]string{l})
				}
			}
			if cc, ok := strings.CutPrefix(l, "// C: "); ok && c == "" {
				c = cc
			}
		}
		if len(defs) == 0 {
			continue
		}
		k, layout, ok := identityOf(c, kind, defs)
		if !ok {
			continue
		}
		e, ok := vi.byKey[k]
		if !ok {
			continue
		}
		if e.layout != layout {
			return "", nil, fmt.Errorf("%s %s is %s here and %s in the system view c.%s: the headers this package reads are not the view's", k.kind, k.name, layout, e.layout, e.view)
		}
		for i, d := range defs {
			if i < len(e.defs) {
				rename[d] = e.view + "." + e.defs[i]
			}
			dropped[d] = true
		}
		drop = append(drop, b)
		if !containsStr(uses, e.view) {
			uses = append(uses, e.view)
		}
	}
	if len(drop) == 0 {
		return text, nil, nil
	}
	for _, b := range blocks {
		if kind := itemKind(lines[b.lo:b.hi]); kind == "method" {
			if n := firstDef(lines[b.lo:b.hi]); dropped[n] {
				drop = append(drop, b)
			}
		}
	}
	for _, b := range drop {
		for i := b.lo; i < b.hi; i++ {
			lines[i] = ""
		}
	}
	for i := head; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = renameIdents(lines[i], rename)
		}
	}
	// The imports take the place of comment lines of the head, so every
	// line keeps its number.
	at := 0
	for _, v := range uses {
		for at < head && !strings.HasPrefix(lines[at], "//") {
			at++
		}
		if at == head {
			return "", nil, fmt.Errorf("the binding's head has no line for import c.%s", v)
		}
		lines[at] = "import c." + v
		at++
	}
	return strings.Join(lines, "\n"), uses, nil
}

// firstDef is the first name a block's lines define.
func firstDef(lines []string) string {
	for _, l := range lines {
		if n := defName(l); n != "" {
			return n
		}
	}
	return ""
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// renameIdents rewrites the plain identifiers of line l (outside comments
// and strings, not a member after a dot, not a qualifier) that rename
// lists.
func renameIdents(l string, rename map[string]string) string {
	if strings.HasPrefix(strings.TrimSpace(l), "//") {
		return l
	}
	var b strings.Builder
	isStart := func(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	isPart := func(c byte) bool { return isStart(c) || c >= '0' && c <= '9' }
	last := 0
	for i := 0; i < len(l); {
		c := l[i]
		switch {
		case c == '/' && i+1 < len(l) && l[i+1] == '/':
			i = len(l)
		case c == '"' || c == '\'':
			i++
			for i < len(l) && l[i] != c {
				if l[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case isStart(c):
			j := i
			for j < len(l) && isPart(l[j]) {
				j++
			}
			id := l[i:j]
			member := i > 0 && l[i-1] == '.'
			qualifier := j < len(l) && l[j] == '.'
			if to, ok := rename[id]; ok && !member && !qualifier {
				b.WriteString(l[last:i])
				b.WriteString(to)
				last = j
			}
			i = j
		case c >= '0' && c <= '9':
			for i < len(l) && isPart(l[i]) {
				i++
			}
		default:
			i++
		}
	}
	if last == 0 {
		return l
	}
	if last > len(l) {
		last = len(l)
	}
	b.WriteString(l[last:])
	return b.String()
}
