// Why: a match must cover every value of its scrutinee's type, and the
// error must show which values it misses; the search must stay bounded on
// patterns of any size.
//
// What: exhaustive (E0306 with up to maxWitnesses witnesses, and `reason:
// too-complex` past maxExhaustSteps), the matrix pattern spat, ctors and
// ctorsOf, witnesses and ivWitnesses (the search), futureArm, show.
//
// How: usefulness over pattern matrices (Maranget), with the irrefutable
// arm as a short circuit. Or-patterns expand only at a row's head; integer
// and char columns are cut into segments no pattern splits; an open set of
// another package counts one extra member no pattern can name.

package check

import (
	"slices"
	"sort"
	"strings"

	"mk/internal/ast"
	"mk/internal/diag"
	"mk/internal/resolve"
	"mk/internal/source"
	"mk/internal/types"
)

const (
	// maxWitnesses is how many unmatched values E0306 lists (chosen, not
	// measured).
	maxWitnesses = 8
	// maxExhaustSteps caps the search (chosen, not measured).
	maxExhaustSteps = 65536
)

// spat is a pattern in the matrix: a wildcard (ctor < 0), or constructor
// ctor of its column's type with one sub-pattern per argument; lit marks a
// literal of an infinite type (never a full constructor); or, when or is
// set, an or-pattern of those alternatives. An or-pattern is expanded only
// when it reaches the head of a row (expandHeads), never multiplied out
// while lowering, so the matrix stays the size of the patterns written and
// only the search is capped.
type spat struct {
	ctor int
	lit  bool
	// iv marks a literal or range of an interval type (an integer or
	// char): it matches the keys lo to hi, inclusive (rangepat.go), and
	// ctor is -1.
	iv     bool
	lo, hi uint64
	args   []*spat
	or     []*spat
	// future marks the constructor of members an open set may gain (open.go).
	future bool
}

var wildPat = &spat{ctor: -1}

// ctorInfo is one constructor of a finite type: its name for witnesses, its
// argument names and types.
type ctorInfo struct {
	name  string
	field []string // argument labels (nil for some/ok/err, which are positional)
	args  []types.TypeID
	// item is an error member's item: two errors of one name from two
	// modules are two constructors.
	item types.ItemID
	// future is the member an open set of another package may gain, which no
	// pattern names; open is that set's declaration.
	future bool
	open   types.ItemID
}

type exhaust struct {
	u      *unit
	steps  int
	capped bool
	// closed leaves out the future constructors of open sets: the
	// search that decides whether a value of this release is unmatched.
	closed bool
	// known memoizes ctors per type for this match: the search asks for the
	// same types at every row and column. Its slices are read only.
	known map[types.TypeID][]ctorInfo
}

// ctors lists t's constructors, nil for a type with infinitely many
// (integers, char, str, anything else). The result is shared: read only.
func (x *exhaust) ctors(t types.TypeID) []ctorInfo {
	if cs, ok := x.known[t]; ok {
		return cs
	}
	cs := x.ctorsOf(t)
	for i := range cs {
		cs[i].field, cs[i].args = slices.Clip(cs[i].field), slices.Clip(cs[i].args)
	}
	if x.known == nil {
		x.known = map[types.TypeID][]ctorInfo{}
	}
	x.known[t] = cs
	return cs
}

// ctorsOf computes ctors(t).
func (x *exhaust) ctorsOf(t types.TypeID) []ctorInfo {
	u := x.u
	tab := u.tab
	in := u.c.cfg.Interner
	ty := tab.Get(t)
	switch ty.Kind {
	case types.KBool:
		return []ctorInfo{{name: "false"}, {name: "true"}}
	case types.KOptional:
		return []ctorInfo{{name: "some", args: []types.TypeID{ty.Elem}}, {name: "none"}}
	case types.KErrorUnion:
		return []ctorInfo{{name: "ok", args: []types.TypeID{ty.Elem}}, {name: "err", args: []types.TypeID{tab.ErrorSetType(ty.Errors)}}}
	case types.KErrorSet:
		var out []ctorInfo
		for _, m := range tab.Members(ty.Errors) {
			if m.IsParam {
				return nil
			}
			it := u.c.cfg.Items.Get(m.Item)
			ci := ctorInfo{name: in.Str(it.Name), item: m.Item}
			if d, ok := u.c.declOfItem(it).(*types.ErrorDecl); ok {
				for _, f := range d.Fields {
					ci.field = append(ci.field, in.Str(f.Name))
					ci.args = append(ci.args, f.Type)
				}
			}
			out = append(out, ci)
		}
		if a := u.openSetOf(ty.Errors); a != types.NoItem && !x.closed {
			out = append(out, ctorInfo{name: "_", future: true, open: a})
		}
		return out
	case types.KNamed:
		switch d := u.c.declOfItem(u.c.cfg.Items.Get(ty.Item)).(type) {
		case *types.EnumDecl:
			out := make([]ctorInfo, len(d.Variants))
			for i, v := range d.Variants {
				out[i].name = "." + in.Str(v.Name)
				for _, f := range v.Fields {
					out[i].field = append(out[i].field, in.Str(f.Name))
					out[i].args = append(out[i].args, u.c.subst(f.Type, ty.Args, types.NoType))
				}
			}
			if !x.closed && u.openEnum(ty.Item) {
				out = append(out, ctorInfo{name: "_", future: true, open: ty.Item})
			}
			return out
		case *types.StructDecl:
			ci := ctorInfo{name: in.Str(u.c.cfg.Items.Get(ty.Item).Name)}
			for _, f := range d.Fields {
				ci.field = append(ci.field, in.Str(f.Name))
				ci.args = append(ci.args, u.c.subst(f.Type, ty.Args, types.NoType))
			}
			return []ctorInfo{ci}
		}
	}
	return nil
}

// exhaustive reports E0306 at the match when some value of t is matched by
// no arm. An open set of another package counts one more member,
// which no pattern names: when every value of this release is
// matched and only that member is not, E0306 says so with `reason: open`
// and a proven fix, a `_` arm that panics, since it runs only for a member
// a later release adds.
func (u *unit) exhaustive(s *ast.MatchStmt, t types.TypeID) {
	x := &exhaust{u: u}
	var rows [][]*spat
	for _, a := range s.Arms {
		if a == nil || a.Pat == nil {
			continue
		}
		if x.irrefutable(a.Pat, t) {
			return // an irrefutable arm matches every value, no search
		}
		rows = append(rows, []*spat{x.lower(a.Pat, t)})
	}
	ws := x.witnesses(rows, []types.TypeID{t})
	at := keywordSpan(u.fx, s.Span().Start, ast.TokMatch)
	var now [][]*spat
	open := types.NoItem
	for _, w := range ws {
		if id := x.futureOf(w[0], t); id != types.NoItem {
			if open == types.NoItem {
				open = id
			}
			continue
		}
		now = append(now, w)
	}
	capped := x.capped
	if len(now) == 0 && open != types.NoItem && (len(ws) >= maxWitnesses || capped) {
		// The witnesses found were all future ones, but the search stopped
		// at its witness or step bound, so a value of this release may be
		// unmatched too: only the search without future constructors
		// decides, and the proven arm is offered only when it finds none.
		c := &exhaust{u: u, closed: true}
		for _, w := range c.witnesses(rows, []types.TypeID{t}) {
			now = append(now, w)
		}
		capped = c.capped
		if len(now) == 0 && capped {
			open = types.NoItem
		}
		x = c
	}
	switch {
	case len(now) > 0:
		missing := make([]string, len(now))
		var stub strings.Builder
		for i, w := range now {
			missing[i] = x.show(w[0], t)
			stub.WriteString(missing[i] + " {\n}\n")
		}
		end := s.Span()
		ins := source.Span{File: end.File, Start: end.End - 1, End: end.End - 1}
		d := u.err(diag.E0306, at, "the match does not cover "+missing[0]).Fact("missing", diag.TextList(missing...)).
			Fix(diag.Fix{Class: diag.Compiles, Edits: []diag.Edit{{Span: ins, Text: stub.String()}}, Why: "an arm for each missing value"})
		if open == types.NoItem {
			open = x.openIn(t)
		}
		if open != types.NoItem {
			// The arm a later release needs is a `_` one; its body is the
			// program's, so it is offered, never proven.
			d.Text("note", u.openName(open)+" may gain members: keep a `_` arm").Alt("a `_` arm: `_ { ... }`, which also covers members a later release adds")
		}
	case open != types.NoItem:
		name := u.openName(open)
		d := u.err(diag.E0306, at, "the match does not cover the members a later release may add to "+name).
			Text("reason", "open").Text("open", name).
			Text("note", "an open set may gain members: outside its package a match covers them with a `_` arm or a binding")
		if ed, ok := u.futureArm(s, name); ok {
			d.Fix(diag.Fix{Class: diag.Proven, Edits: []diag.Edit{ed}, Why: "a `_` arm for the members " + name + " may gain; every member of this release has its arm, so it never runs today"})
		}
	case capped:
		u.err(diag.E0306, at, "the match is too complex to prove exhaustive").Text("reason", "too-complex").
			Alt("add a `_` arm").Alt("split the match")
	}
}

// futureOf returns the open declaration of the first future constructor in
// witness p of type t, NoItem when it has none.
func (x *exhaust) futureOf(p *spat, t types.TypeID) types.ItemID {
	if p.ctor < 0 {
		return types.NoItem
	}
	cs := x.ctors(t)
	if p.ctor >= len(cs) {
		return types.NoItem
	}
	c := cs[p.ctor]
	if c.future {
		return c.open
	}
	for i, a := range p.args {
		if i < len(c.args) {
			if id := x.futureOf(a, c.args[i]); id != types.NoItem {
				return id
			}
		}
	}
	return types.NoItem
}

// openIn returns the open declaration t is, or holds at its top, under ?
// or E!: the set an E0306's note names. NoItem when there is none.
func (x *exhaust) openIn(t types.TypeID) types.ItemID {
	for _, c := range x.ctors(t) {
		if c.future {
			return c.open
		}
	}
	ty := x.u.tab.Get(t)
	switch ty.Kind {
	case types.KOptional:
		return x.openIn(ty.Elem)
	case types.KErrorUnion:
		if id := x.openIn(x.u.tab.ErrorSetType(ty.Errors)); id != types.NoItem {
			return id
		}
		return x.openIn(ty.Elem)
	}
	return types.NoItem
}

// futureArm is the edit that adds `_ { Panic(...) }` as the match's last
// arm, on lines of its own, one level past the indentation of the line
// the match starts on. When the closing brace shares the last arm's line
// (`.Other { n = 4 } }`, which mk fmt keeps), the brace moves to a line of
// its own after the arm: Mk takes one arm per line.
func (u *unit) futureArm(s *ast.MatchStmt, name string) (diag.Edit, bool) {
	end := s.Span()
	toks := u.fx.file.Tokens
	// The brace, past the zero-width terminator the newline rule emits
	// before a `}` that ends a statement.
	i := sort.Search(len(toks), func(i int) bool { return toks[i].Start >= end.End-1 })
	for i < len(toks) && toks[i].Kind == ast.TokTerm {
		i++
	}
	if i >= len(toks) || toks[i].Kind != ast.TokRBrace || toks[i].Start != end.End-1 {
		return diag.Edit{}, false
	}
	prev := func(j int) int { // the last token before j that has width
		for j--; j >= 0 && toks[j].Kind == ast.TokTerm; j-- {
		}
		return j
	}
	// The indentation of the match's line.
	indent := 0
	for j := sort.Search(len(toks), func(j int) bool { return toks[j].Start >= s.Span().Start }) - 1; j >= 0; j-- {
		if toks[j].Kind == ast.TokNewline {
			if toks[j+1].Kind == ast.TokWhitespace {
				indent = int(toks[j+1].Len)
			}
			break
		}
	}
	pad := strings.Repeat(" ", indent)
	call := "Panic(\"" + name + " gained a member this match does not handle\")"
	arm := pad + "    _ {\n" + pad + "        " + call + "\n" + pad + "    }\n"
	at := source.Span{File: end.File, Start: end.End - 1, End: end.End - 1}
	p := prev(i)
	switch {
	case p >= 0 && toks[p].Kind == ast.TokNewline:
		return diag.Edit{Span: at, Text: arm}, true
	case p >= 0 && toks[p].Kind == ast.TokWhitespace && prev(p) >= 0 && toks[prev(p)].Kind == ast.TokNewline:
		// The brace starts its line: the arm goes before its indentation.
		at.Start, at.End = toks[p].Start, toks[p].Start
		return diag.Edit{Span: at, Text: arm}, true
	}
	// The brace ends the last arm's line: the space before it, if any,
	// becomes the line break.
	if p >= 0 && toks[p].Kind == ast.TokWhitespace {
		at.Start = toks[p].Start
	}
	return diag.Edit{Span: at, Text: "\n" + arm + pad}, true
}

// constSpat is a const pattern's matrix pattern: the literal of its value
// (constPat), a bool as its constructor; wild when check refused it.
func (x *exhaust) constSpat(p ast.Pattern, t types.TypeID) *spat {
	u := x.u
	id := u.constPatKey(p)
	if id == types.NoConst {
		return wildPat // refused or poisoned: reported there
	}
	cv := u.c.cfg.Consts.Get(id)
	switch {
	case cv.Type == types.Bool:
		if len(cv.Bytes) == 1 && cv.Bytes[0] == 1 {
			return &spat{ctor: 1}
		}
		return &spat{ctor: 0}
	case u.tab.Kind(cv.Type) == types.KChar && len(cv.Bytes) <= 8:
		k := types.LoadUint(cv.Bytes, len(cv.Bytes))
		return &spat{ctor: -1, iv: true, lo: k, hi: k}
	case u.isInt(cv.Type) && len(cv.Bytes) <= 8:
		if _, ok := u.ivDomain(t); ok {
			k := types.LoadUint(cv.Bytes, len(cv.Bytes))
			if u.ivSigned(t) {
				v, _ := u.c.constInt(id)
				k = uint64(v) ^ 1<<63
			}
			return &spat{ctor: -1, iv: true, lo: k, hi: k}
		}
	}
	return &spat{ctor: -1, lit: true}
}

// irrefutable reports whether p matches every value of t: `_`, a binding,
// a struct pattern of irrefutable fields.
func (x *exhaust) irrefutable(p ast.Pattern, t types.TypeID) bool {
	switch p := p.(type) {
	case *ast.WildcardPattern, *ast.BadPattern:
		return true
	case *ast.NamePattern:
		r := x.u.ref(p)
		return r.Kind != resolve.RefItem && r.Kind != resolve.RefUniverse
	case *ast.PathPattern:
		if r := x.u.ref(p); r.Kind == resolve.RefItem && x.u.c.cfg.Items.Get(types.ItemID(r.A)).Kind == types.KindStruct {
			for _, f := range p.Fields {
				if f != nil && f.Pat != nil && !x.irrefutable(f.Pat, types.Poison) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// lower turns an arm's pattern into a matrix pattern; alternatives stay one
// or-pattern.
func (x *exhaust) lower(p ast.Pattern, t types.TypeID) *spat {
	u := x.u
	switch p := p.(type) {
	case *ast.WildcardPattern, *ast.BadPattern, nil:
		return wildPat
	case *ast.NamePattern:
		r := u.ref(p)
		switch r.Kind {
		case resolve.RefItem:
			if u.useOf(p.ID()).Kind == UseConst {
				return x.constSpat(p, t)
			}
			return x.byItem(t, types.ItemID(r.A), nil, nil)
		case resolve.RefUniverse:
			if source.Symbol(r.A) == source.SymTrue {
				return &spat{ctor: 1}
			}
			return &spat{ctor: 0}
		}
		return wildPat
	case *ast.LiteralPattern:
		if _, ok := u.ivDomain(t); ok {
			if k, ok := u.litKey(p, t); ok {
				return &spat{ctor: -1, iv: true, lo: k, hi: k}
			}
			return wildPat // a literal check refused: reported there
		}
		return &spat{ctor: -1, lit: true}
	case *ast.RangePattern:
		if lo, hi, ok := u.rangeKeys(p, t); ok {
			return &spat{ctor: -1, iv: true, lo: lo, hi: hi}
		}
		return wildPat // E0315 or a refused end: reported there
	case *ast.SomePattern:
		return x.ctorPat(0, []ast.Pattern{p.Pat}, x.argTypes(t, 0))
	case *ast.NonePattern:
		return &spat{ctor: 1}
	case *ast.OkPattern:
		return x.ctorPat(0, []ast.Pattern{p.Pat}, x.argTypes(t, 0))
	case *ast.ErrPattern:
		return x.ctorPat(1, []ast.Pattern{p.Pat}, x.argTypes(t, 1))
	case *ast.AltPattern:
		alts := make([]*spat, 0, len(p.Alts))
		for _, a := range p.Alts {
			alts = append(alts, x.lower(a, t))
		}
		switch len(alts) {
		case 0:
			return wildPat // after a parse error; reported there
		case 1:
			return alts[0]
		}
		return &spat{ctor: -1, or: alts}
	case *ast.VariantPattern:
		if p.Name == nil {
			return wildPat
		}
		return x.byName(t, "."+u.c.cfg.Interner.Str(p.Name.Name), p.Fields, p)
	case *ast.PathPattern:
		if p.Path == nil || len(p.Path.Names) == 0 {
			return wildPat
		}
		last := p.Path.Names[len(p.Path.Names)-1]
		name := u.c.cfg.Interner.Str(last.Name)
		r := u.ref(p)
		if r.Kind == resolve.RefDeferred {
			name = "." + name
		}
		if r.Kind == resolve.RefItem {
			if u.useOf(p.ID()).Kind == UseConst {
				return x.constSpat(p, t)
			}
			return x.byItem(t, types.ItemID(r.A), p.Fields, p)
		}
		return x.byName(t, name, p.Fields, p)
	}
	return wildPat
}

// byName finds the constructor of t named name (a variant ".V", an error, a
// struct) and lowers its fields; an unknown one (already reported) is a
// wildcard, so it adds no witness.
func (x *exhaust) byName(t types.TypeID, name string, fields []*ast.PatField, p ast.Pattern) *spat {
	cs := x.ctors(t)
	for i, c := range cs {
		if c.name != name && !(len(c.name) > 0 && c.name[0] == '.' && c.name[1:] == name) {
			continue
		}
		subs := make([]ast.Pattern, len(c.args))
		for _, f := range fields {
			if f == nil || f.Name == nil || f.Pat == nil {
				continue
			}
			for k, fn := range c.field {
				if fn == x.u.c.cfg.Interner.Str(f.Name.Name) {
					subs[k] = f.Pat
				}
			}
		}
		return x.ctorPat(i, subs, c.args)
	}
	return wildPat
}

// argTypes are constructor k's argument types of t.
func (x *exhaust) argTypes(t types.TypeID, k int) []types.TypeID {
	cs := x.ctors(t)
	if k < len(cs) {
		return cs[k].args
	}
	return nil
}

// ctorPat lowers constructor k with sub-patterns subs (nil is a wildcard).
func (x *exhaust) ctorPat(k int, subs []ast.Pattern, args []types.TypeID) *spat {
	out := &spat{ctor: k, args: make([]*spat, len(args))}
	for i := range args {
		if i < len(subs) && subs[i] != nil {
			out.args[i] = x.lower(subs[i], args[i])
		} else {
			out.args[i] = wildPat
		}
	}
	return out
}

// expandHeads replaces each row whose head is an or-pattern by one row per
// alternative (nested or-patterns flattened), in order. Rows with other
// heads are kept as they are, so the result is shared when nothing expands.
func expandHeads(rows [][]*spat) [][]*spat {
	n := -1
	for i, r := range rows {
		if len(r) > 0 && r[0].or != nil {
			n = i
			break
		}
	}
	if n < 0 {
		return rows
	}
	out := append(make([][]*spat, 0, len(rows)+1), rows[:n]...)
	var add func(h *spat, rest []*spat)
	add = func(h *spat, rest []*spat) {
		if h.or == nil {
			out = append(out, append([]*spat{h}, rest...))
			return
		}
		for _, a := range h.or {
			add(a, rest)
		}
	}
	for _, r := range rows[n:] {
		if len(r) > 0 && r[0].or != nil {
			add(r[0], r[1:])
		} else {
			out = append(out, r)
		}
	}
	return out
}

// witnesses returns up to maxWitnesses value vectors of types tys that no
// row matches, depth first, constructors in declaration order, counting
// each specialization step against maxExhaustSteps.
func (x *exhaust) witnesses(rows [][]*spat, tys []types.TypeID) [][]*spat {
	x.steps++
	if x.steps > maxExhaustSteps {
		x.capped = true
		return nil
	}
	if len(tys) == 0 {
		if len(rows) == 0 {
			return [][]*spat{{}}
		}
		return nil
	}
	rows = expandHeads(rows)
	if len(rows) == 0 {
		w := make([]*spat, len(tys))
		for i := range w {
			w[i] = wildPat
		}
		return [][]*spat{w}
	}
	cs := x.ctors(tys[0])
	if cs == nil && tys[0] != types.Poison {
		if pieces, ok := x.u.ivDomain(tys[0]); ok && slices.ContainsFunc(rows, func(r []*spat) bool { return r[0].iv }) {
			return x.ivWitnesses(rows, tys, pieces)
		}
	}
	if cs == nil || tys[0] == types.Poison {
		var def [][]*spat
		for _, r := range rows {
			if r[0].ctor < 0 && !r[0].lit && !r[0].iv {
				def = append(def, r[1:])
			}
		}
		var out [][]*spat
		for _, w := range x.witnesses(def, tys[1:]) {
			out = append(out, append([]*spat{wildPat}, w...))
		}
		return out
	}
	used := make([]bool, len(cs))
	complete := true
	for _, r := range rows {
		if h := r[0]; h.ctor >= 0 && h.ctor < len(cs) {
			used[h.ctor] = true
		}
	}
	for _, b := range used {
		complete = complete && b
	}
	var out [][]*spat
	// A constructor no row heads is matched only by the rows with a
	// wildcard head, so one search of the default matrix decides all such
	// constructors (Maranget's default case); only the constructors the
	// rows name are searched one by one.
	var def [][]*spat
	defDone := false
	for k, c := range cs {
		if !complete && !used[k] {
			if !defDone {
				var drows [][]*spat
				for _, r := range rows {
					if r[0].ctor < 0 && !r[0].lit && !r[0].iv {
						drows = append(drows, r[1:])
					}
				}
				def, defDone = x.witnesses(drows, tys[1:]), true
			}
			for _, w := range def {
				args := make([]*spat, len(c.args))
				for i := range args {
					args[i] = wildPat
				}
				out = append(out, append([]*spat{{ctor: k, args: args, future: c.future}}, w...))
				if len(out) >= maxWitnesses {
					return out
				}
			}
			if x.capped {
				return out
			}
			continue
		}
		n := len(c.args)
		var sub [][]*spat
		for _, r := range rows {
			h := r[0]
			switch {
			case h.ctor == k:
				sub = append(sub, append(append([]*spat(nil), h.args...), r[1:]...))
			case h.ctor < 0 && !h.lit && !h.iv:
				row := make([]*spat, 0, n+len(r)-1)
				for i := 0; i < n; i++ {
					row = append(row, wildPat)
				}
				sub = append(sub, append(row, r[1:]...))
			}
		}
		for _, w := range x.witnesses(sub, append(append([]types.TypeID(nil), c.args...), tys[1:]...)) {
			out = append(out, append([]*spat{{ctor: k, args: w[:n], future: c.future}}, w[n:]...))
			if len(out) >= maxWitnesses {
				return out
			}
		}
		if x.capped {
			return out
		}
	}
	return out
}

// ivWitnesses is witnesses for a column of an interval type: the
// domain's pieces are cut at every head's ends into segments no head
// splits, and each segment is searched with the rows whose head covers it
// or is a wildcard. A witness is its segment, shown as a value or a range.
func (x *exhaust) ivWitnesses(rows [][]*spat, tys []types.TypeID, pieces [][2]uint64) [][]*spat {
	// cut holds every key where a segment starts, and the key past a
	// piece's end as (end, true) so a 64-bit domain's last key needs no
	// 65th bit.
	type cut struct {
		k   uint64
		end bool
	}
	var cuts []cut
	in := func(k uint64) bool {
		for _, pc := range pieces {
			if k >= pc[0] && k <= pc[1] {
				return true
			}
		}
		return false
	}
	for _, pc := range pieces {
		cuts = append(cuts, cut{pc[0], false}, cut{pc[1], true})
	}
	for _, r := range rows {
		if h := r[0]; h.iv {
			if in(h.lo) {
				cuts = append(cuts, cut{h.lo, false})
			}
			if in(h.hi) {
				cuts = append(cuts, cut{h.hi, true})
			}
		}
	}
	// A start s begins a segment; an end e closes one at e, so e+1 starts
	// the next unless e is a piece's last key.
	starts := map[uint64]bool{}
	for _, c := range cuts {
		if !c.end {
			starts[c.k] = true
		} else if c.k != ^uint64(0) && in(c.k+1) {
			starts[c.k+1] = true
		}
	}
	keys := make([]uint64, 0, len(starts))
	for k := range starts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out [][]*spat
	for i, lo := range keys {
		hi := ^uint64(0)
		if i+1 < len(keys) {
			hi = keys[i+1] - 1
		}
		for _, pc := range pieces { // a segment never crosses a piece's end
			if lo >= pc[0] && lo <= pc[1] && hi > pc[1] {
				hi = pc[1]
			}
		}
		var sub [][]*spat
		for _, r := range rows {
			h := r[0]
			if h.iv && h.lo <= lo && hi <= h.hi || h.ctor < 0 && !h.lit && !h.iv {
				sub = append(sub, r[1:])
			}
		}
		for _, w := range x.witnesses(sub, tys[1:]) {
			out = append(out, append([]*spat{{ctor: -1, iv: true, lo: lo, hi: hi}}, w...))
			if len(out) >= maxWitnesses {
				return out
			}
		}
		if x.capped {
			return out
		}
	}
	return out
}

// show prints a witness as a pattern.
func (x *exhaust) show(p *spat, t types.TypeID) string {
	if p.iv {
		u := x.u
		if p.lo == p.hi {
			return u.ivText(p.lo, t)
		}
		lo := u.ivText(p.lo, t)
		end := p.hi + 1
		if pieces, ok := u.ivDomain(t); ok {
			if p.hi == pieces[len(pieces)-1][1] {
				return lo + ".."
			}
			// An end in a gap of the domain (char's surrogates) is no
			// value of t: the next piece's start ends the range alike.
			for i := 1; i < len(pieces); i++ {
				if end > pieces[i-1][1] && end < pieces[i][0] {
					end = pieces[i][0]
				}
			}
		}
		return lo + ".." + u.ivText(end, t)
	}
	if p.ctor < 0 {
		return "_"
	}
	cs := x.ctors(t)
	if p.ctor >= len(cs) {
		return "_"
	}
	c := cs[p.ctor]
	if c.field == nil && len(c.args) > 0 {
		return c.name + "(" + x.show(p.args[0], c.args[0]) + ")"
	}
	var parts []string
	for i, a := range p.args {
		if a.ctor >= 0 || a.iv {
			parts = append(parts, c.field[i]+": "+x.show(a, c.args[i]))
		}
	}
	if len(parts) == 0 {
		if id := x.itemOf(t); id != types.NoItem {
			if _, isStruct := x.u.c.declOfItem(x.u.c.cfg.Items.Get(id)).(*types.StructDecl); isStruct {
				return c.name + "(..)"
			}
		}
		return c.name
	}
	return c.name + "(" + strings.Join(parts, ", ") + ")"
}

func (x *exhaust) itemOf(t types.TypeID) types.ItemID {
	if ty := x.u.tab.Get(t); ty.Kind == types.KNamed {
		return ty.Item
	}
	return types.NoItem
}

// byItem is byName for a pattern that resolves to an item: an error member
// is found by its item, so a.Cut and b.Cut of one set are two constructors
// (found by name, both arms would take the first Cut, and E0306 would say
// the match does not cover Cut); any other constructor by the item's name.
func (x *exhaust) byItem(t types.TypeID, id types.ItemID, fields []*ast.PatField, p ast.Pattern) *spat {
	for i, c := range x.ctors(t) {
		if c.item == id {
			subs := make([]ast.Pattern, len(c.args))
			for _, f := range fields {
				if f == nil || f.Name == nil || f.Pat == nil {
					continue
				}
				for k, fn := range c.field {
					if fn == x.u.c.cfg.Interner.Str(f.Name.Name) {
						subs[k] = f.Pat
					}
				}
			}
			return x.ctorPat(i, subs, c.args)
		}
	}
	return x.byName(t, x.u.c.cfg.Interner.Str(x.u.c.cfg.Items.Get(id).Name), fields, p)
}
