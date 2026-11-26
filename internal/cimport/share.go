// Why: a package's own C import that reads the host's SDK (sdk: true)
// binds again what a system view binds (HWND, RECT, CreateWindowExW), and
// a package that also imports the view would see two types for one C type;
// they must be one, the view's.
//
// What: ViewIdentity (the C declarations a view binds, by kind and C name,
// with each record's layout), NewViewIdentity, ShareWithViews and
// SharePackageWithViews, which drop from bindings every block a view binds
// and make references name the view's item.
//
// How: a block's identity is read from its "// C: " comment: a struct or
// union and its tag (and its size and alignment, which must agree), an enum
// and its tag, a typedef and its name (the comment's last word); a function
// or an extern variable by its name, which is C's. A shared block's lines
// are blanked, so every other line keeps its number, and each name it
// defined is rewritten, wherever another block (or another C import of the
// same package) names it, to view.Name. The view's imports take the place of
// head comment lines first, inserting any overflow above the first block.

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
	shared, uses, _, err := shareOne(text, vi)
	return shared, uses, err
}

// SharePackageWithViews runs ShareWithViews across a package's C imports and
// rewrites any cross-import qualified reference (`a.Hwnd` when `Hwnd` moved
// from `c.a` to `c.windows`) across sibling imports of the package.
func SharePackageWithViews(names, texts []string, vi *ViewIdentity) ([]string, error) {
	out := make([]string, len(texts))
	type qualTarget struct{ qualified, view string }
	moved := map[string]qualTarget{}
	for i, text := range texts {
		shared, _, droppedQual, err := shareOne(text, vi)
		if err != nil {
			return nil, err
		}
		out[i] = shared
		if i < len(names) && names[i] != "" {
			for def, qt := range droppedQual {
				moved[names[i]+"."+def] = qualTarget{qualified: qt.qualified, view: qt.view}
			}
		}
	}
	if len(texts) <= 1 || len(moved) == 0 {
		return out, nil
	}
	for i, text := range out {
		lines := strings.Split(text, "\n")
		head := bodyHead(lines)
		imported := map[string]bool{}
		for _, l := range lines[:head] {
			if mod, ok := strings.CutPrefix(strings.TrimSpace(l), "import c."); ok {
				imported[mod] = true
			}
		}
		qualRename := map[string]string{}
		qualView := map[string]string{}
		for k, qt := range moved {
			mod, _, _ := strings.Cut(k, ".")
			if imported[mod] {
				qualRename[k] = qt.qualified
				qualView[k] = qt.view
			}
		}
		if len(qualRename) == 0 {
			continue
		}
		var addViews []string
		changed := false
		for j := head; j < len(lines); j++ {
			if lines[j] == "" {
				continue
			}
			nl, usedKeys := renameQualIdents(lines[j], qualRename)
			if len(usedKeys) > 0 {
				lines[j] = nl
				changed = true
				for _, k := range usedKeys {
					v := qualView[k]
					if !containsStr(addViews, v) {
						addViews = append(addViews, v)
					}
				}
			}
		}
		if !changed {
			continue
		}
		lines = addHeadViewImports(lines, head, addViews)
		out[i] = strings.Join(lines, "\n")
	}
	return out, nil
}

type sharedTarget struct{ qualified, view string }

func bodyHead(lines []string) int {
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
	return head
}

func shareOne(text string, vi *ViewIdentity) (string, []string, map[string]sharedTarget, error) {
	lines := strings.Split(text, "\n")
	head := bodyHead(lines)
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
	droppedQual := map[string]sharedTarget{}
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
			return "", nil, nil, fmt.Errorf("%s %s is %s here and %s in the system view c.%s: the headers this package reads are not the view's", k.kind, k.name, layout, e.layout, e.view)
		}
		for i, d := range defs {
			if i < len(e.defs) {
				q := e.view + "." + e.defs[i]
				rename[d] = q
				droppedQual[d] = sharedTarget{qualified: q, view: e.view}
			}
			dropped[d] = true
		}
		drop = append(drop, b)
		if !containsStr(uses, e.view) {
			uses = append(uses, e.view)
		}
	}
	if len(drop) == 0 {
		return text, nil, nil, nil
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
	lines = addHeadViewImports(lines, head, uses)
	return strings.Join(lines, "\n"), uses, droppedQual, nil
}

// addHeadViewImports writes `import c.<v>` for each v in uses not already
// present in lines[:head], reusing head comment lines first so line numbers
// stay unchanged while there are enough comment lines, and inserting any
// remainder before head.
func addHeadViewImports(lines []string, head int, uses []string) []string {
	existing := map[string]bool{}
	for _, l := range lines[:head] {
		if mod, ok := strings.CutPrefix(strings.TrimSpace(l), "import c."); ok {
			existing[mod] = true
		}
	}
	at := 0
	var extra []string
	for _, v := range uses {
		if existing[v] {
			continue
		}
		existing[v] = true
		for at < head && !strings.HasPrefix(lines[at], "//") {
			at++
		}
		imp := "import c." + v
		if at < head {
			lines[at] = imp
			at++
		} else {
			extra = append(extra, imp)
		}
	}
	if len(extra) == 0 {
		return lines
	}
	ins := head
	var addBlank []string
	if head > 0 && lines[head-1] == "" {
		ins = head - 1
	} else {
		addBlank = append(extra, "")
		extra = addBlank
	}
	out := make([]string, 0, len(lines)+len(extra))
	out = append(out, lines[:ins]...)
	out = append(out, extra...)
	out = append(out, lines[ins:]...)
	return out
}

func renameQualIdents(l string, qualRename map[string]string) (string, []string) {
	if strings.HasPrefix(strings.TrimSpace(l), "//") {
		return l, nil
	}
	var b strings.Builder
	var hit []string
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
			if !(i > 0 && l[i-1] == '.') && j+1 < len(l) && l[j] == '.' && isStart(l[j+1]) {
				k := j + 1
				for k < len(l) && isPart(l[k]) {
					k++
				}
				key := l[i:k]
				if to, ok := qualRename[key]; ok {
					b.WriteString(l[last:i])
					b.WriteString(to)
					last = k
					hit = append(hit, key)
					i = k
					continue
				}
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
		return l, nil
	}
	b.WriteString(l[last:])
	return b.String(), hit
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
