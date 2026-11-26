// Why: a match with many arms tested one by one costs a compare per arm; a
// decision tree tests each column once, and LLVM turns its switches into jump
// tables.
//
// What: mcomp, which compiles match arms to a decision tree of dnode (leaf,
// fail, switch, str test) with build, buildStr, subColumns and complete, and
// emits it as MIR blocks with emit.
//
// How: Maranget's construction ("Compiling pattern matching to good decision
// trees", 2008) with the first-row heuristic, built abstractly first and
// emitted second, so the size guard (leaves at most 4 x the pattern nodes)
// can fall back to sequential testing before any MIR exists. Ranges, struct
// patterns and nested ok or err patterns also take the sequential path.

package lower

import (
	"mk/internal/ast"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// matchLeafFactor is the size guard's factor (a chosen value, not a
// measurement); Config.matchLeaves overrides it in tests.
const matchLeafFactor = 4

type dkind uint8

const (
	dLeaf   dkind = iota // goto the arm's entry
	dFail                // unreachable: check proved exhaustiveness (E0306)
	dSwitch              // switch on a column's discriminant or value
	dStr                 // a str literal: Eq feeding a bool switch
)

// dnode is one node of the abstract decision tree.
type dnode struct {
	kind  dkind
	arm   int
	col   int  // path index of the tested column
	disc  bool // the switch is on RvDiscriminant, else on the value
	errs  bool // an error-case switch: completeness is decided at emission
	cases []dcase
	def   *dnode // nil when the cases cover the type's whole domain
	lit   Lit    // dStr: the str constant
	yes   *dnode
	no    *dnode
	at    source.Span
}

type dcase struct {
	val   uint64
	child *dnode
}

type stepKind uint8

const (
	stepRoot    stepKind = iota
	stepSome             // the payload of a ?T
	stepPayload          // field k of a variant or error, after a downcast
)

// dpath is a column: a place reached from the scrutinee.
type dpath struct {
	parent int
	kind   stepKind
	sel    mir.VariantSel
	a      uint32
	f      *ast.PatField // a representative sub-pattern, for the field's type
	pos    int
}

// mrow is one row of the pattern matrix: nil is a wildcard.
type mrow struct {
	pats []ast.Pattern
	arm  int
}

type mcomp struct {
	bl     *builder
	paths  []dpath
	places []mir.Place
	have   []bool
	leaves int
	limit  int
	abort  bool
}

// headKind is how a column's pattern tests.
type headKind uint8

const (
	hWild headKind = iota
	hValue
	hStr
	hDiscr
	hAlt
)

// head classifies q: its kind and, for hValue and hDiscr, its case value.
func (m *mcomp) head(q ast.Pattern) (headKind, uint64) {
	if q == nil {
		return hWild, 0
	}
	pk := m.bl.in.Pattern(q.ID())
	switch pk.Kind {
	case PWild, PBind:
		return hWild, 0
	case PLit:
		l := m.bl.patLit(q)
		if l.Kind == LitConst {
			return hStr, 0
		}
		return hValue, l.Bits
	case PVariant:
		return hDiscr, m.bl.enumDecl(pk, q.Span()).Variants[pk.Variant].Discr
	case PErrorCase:
		return hDiscr, uint64(m.bl.errorCode(pk.Item, q.Span()))
	case PSome:
		return hDiscr, 1
	case PNone:
		return hDiscr, 0
	case PAlt:
		return hAlt, 0
	case PRange:
		// A range is a compare, not a case value: the arms are tested in
		// order, where LLVM forms a switch from the compares it can.
		m.abort = true
		return hWild, 0
	case POk, PErr:
		// A nested ok or err pattern (an E!T inside the scrutinee): err has
		// no one case value, any discriminant but 0, so the arms are tested
		// in order, where test and bindPat take it apart.
		m.abort = true
		return hWild, 0
	case PStruct:
		// A struct pattern has no discriminant to switch on and the tree no
		// column expansion without one: the arms are tested in order
		// (arms' fallback), where test and bindPat take its fields.
		m.abort = true
		return hWild, 0
	}
	source.ICEf(q.Span(), "lower: pattern %T (kind %d) in a match", q, pk.Kind)
	return hWild, 0
}

func (m *mcomp) leaf(n *dnode) *dnode {
	m.leaves++
	if m.leaves > m.limit {
		m.abort = true
	}
	return n
}

// without is pats with column c removed, prefixed by sub.
func without(pats []ast.Pattern, c int, sub []ast.Pattern) []ast.Pattern {
	out := make([]ast.Pattern, 0, len(pats)-1+len(sub))
	out = append(out, sub...)
	out = append(out, pats[:c]...)
	return append(out, pats[c+1:]...)
}

func withoutCol(cols []int, c int, sub []int) []int {
	out := make([]int, 0, len(cols)-1+len(sub))
	out = append(out, sub...)
	out = append(out, cols[:c]...)
	return append(out, cols[c+1:]...)
}

// expandAlts replaces each row whose column c is an alternative by one row
// per alternative, in order, leading to the same arm.
func (m *mcomp) expandAlts(rows []mrow, c int) []mrow {
	var out []mrow
	for _, r := range rows {
		if k, _ := m.head(r.pats[c]); k != hAlt {
			out = append(out, r)
			continue
		}
		var alts []mrow
		for _, a := range r.pats[c].(*ast.AltPattern).Alts {
			p := append([]ast.Pattern(nil), r.pats...)
			p[c] = a
			alts = append(alts, mrow{pats: p, arm: r.arm})
		}
		out = append(out, m.expandAlts(alts, c)...)
	}
	return out
}

// build is the column-wise construction: a leaf when the first row matches
// unconditionally, else one test on the leftmost column the first row
// tests, specializing the matrix per constructor.
func (m *mcomp) build(rows []mrow, cols []int) *dnode {
	if m.abort {
		return nil
	}
	if len(rows) == 0 {
		return m.leaf(&dnode{kind: dFail})
	}
	c := -1
	for i, q := range rows[0].pats {
		if k, _ := m.head(q); k != hWild {
			c = i
			break
		}
	}
	if c < 0 {
		return m.leaf(&dnode{kind: dLeaf, arm: rows[0].arm})
	}
	if k, _ := m.head(rows[0].pats[c]); k == hAlt {
		return m.build(m.expandAlts(rows, c), cols)
	}
	rows = m.expandAlts(rows, c)
	first := rows[0].pats[c]
	kind, _ := m.head(first)
	if kind == hStr {
		return m.buildStr(rows, cols, c)
	}
	n := &dnode{kind: dSwitch, col: cols[c], disc: kind == hDiscr, at: first.Span()}
	pk := m.bl.in.Pattern(first.ID())
	n.errs = pk.Kind == PErrorCase
	// Constructors in first-seen order (never map order: determinism).
	var keys []uint64
	reps := map[uint64]ast.Pattern{}
	var defRows []mrow
	for _, r := range rows {
		k, v := m.head(r.pats[c])
		if k == hWild {
			defRows = append(defRows, mrow{pats: without(r.pats, c, nil), arm: r.arm})
			continue
		}
		if _, ok := reps[v]; !ok {
			reps[v] = r.pats[c]
			keys = append(keys, v)
		}
	}
	for _, key := range keys {
		subCols, fields := m.subColumns(rows, c, cols[c], key)
		var spec []mrow
		for _, r := range rows {
			k, v := m.head(r.pats[c])
			switch {
			case k == hWild:
				spec = append(spec, mrow{pats: without(r.pats, c, make([]ast.Pattern, len(subCols))), arm: r.arm})
			case v == key:
				spec = append(spec, mrow{pats: without(r.pats, c, m.subPats(r.pats[c], fields)), arm: r.arm})
			}
		}
		child := m.build(spec, withoutCol(cols, c, subCols))
		if m.abort {
			return nil
		}
		n.cases = append(n.cases, dcase{val: key, child: child})
	}
	if !m.complete(pk, first, keys) {
		n.def = m.build(defRows, withoutCol(cols, c, nil))
	}
	return n
}

// buildStr is a two-way node: rows with this literal or a wildcard on the
// yes edge, rows with another literal or a wildcard on the no edge.
func (m *mcomp) buildStr(rows []mrow, cols []int, c int) *dnode {
	first := rows[0].pats[c]
	lit := m.bl.patLit(first)
	n := &dnode{kind: dStr, col: cols[c], lit: lit, at: first.Span()}
	var yes, no []mrow
	for _, r := range rows {
		k, _ := m.head(r.pats[c])
		switch {
		case k == hWild:
			yes = append(yes, mrow{pats: without(r.pats, c, nil), arm: r.arm})
			no = append(no, r)
		case m.bl.patLit(r.pats[c]).Const == lit.Const:
			yes = append(yes, mrow{pats: without(r.pats, c, nil), arm: r.arm})
		default:
			no = append(no, r)
		}
	}
	// The no edge keeps column c: its other literals test it again.
	n.yes = m.build(yes, withoutCol(cols, c, nil))
	n.no = m.build(no, cols)
	return n
}

// subColumns are the columns constructor key opens: the payload of some,
// or the fields of a variant or error that some row with key tests, in
// field order.
func (m *mcomp) subColumns(rows []mrow, c int, parent int, key uint64) ([]int, []uint32) {
	type sub struct {
		k   uint32
		f   *ast.PatField
		pos int
	}
	var subs []sub // sorted by field, one per field, first-seen representative
	var rep ast.Pattern
	for _, r := range rows {
		k, v := m.head(r.pats[c])
		if k != hDiscr || v != key {
			continue
		}
		q := r.pats[c]
		if rep == nil {
			rep = q
		}
		pk := m.bl.in.Pattern(q.ID())
		for i, f := range patFields(q) {
			if f.Pat == nil {
				continue
			}
			if hk, _ := m.head(f.Pat); hk == hWild {
				continue
			}
			fk := m.bl.fieldOf(pk, f, i, q.Span())
			at := len(subs)
			for j := range subs {
				if subs[j].k >= fk {
					at = j
					break
				}
			}
			if at < len(subs) && subs[at].k == fk {
				continue
			}
			subs = append(subs, sub{})
			copy(subs[at+1:], subs[at:])
			subs[at] = sub{k: fk, f: f, pos: i}
		}
	}
	if rep == nil {
		return nil, nil
	}
	pk := m.bl.in.Pattern(rep.ID())
	switch pk.Kind {
	case PSome:
		m.paths = append(m.paths, dpath{parent: parent, kind: stepSome})
		return []int{len(m.paths) - 1}, nil
	case PVariant, PErrorCase:
		sel, a := mir.SelEnum, pk.Variant
		if pk.Kind == PErrorCase {
			sel, a = mir.SelCase, uint32(pk.Item)
		}
		cols := make([]int, len(subs))
		fields := make([]uint32, len(subs))
		for i, sb := range subs {
			m.paths = append(m.paths, dpath{parent: parent, kind: stepPayload, sel: sel, a: a, f: sb.f, pos: sb.pos})
			cols[i], fields[i] = len(m.paths)-1, sb.k
		}
		return cols, fields
	}
	return nil, nil
}

// subPats are q's sub-patterns for the columns subColumns opened.
func (m *mcomp) subPats(q ast.Pattern, fields []uint32) []ast.Pattern {
	pk := m.bl.in.Pattern(q.ID())
	if pk.Kind == PSome {
		return []ast.Pattern{q.(*ast.SomePattern).Pat}
	}
	out := make([]ast.Pattern, len(fields))
	for i, f := range patFields(q) {
		if f.Pat == nil {
			continue
		}
		k := m.bl.fieldOf(pk, f, i, q.Span())
		for j, x := range fields {
			if x == k {
				out[j] = f.Pat
			}
		}
	}
	return out
}

// complete reports whether keys cover the whole domain: every variant,
// some and none, true and false. Error sets are decided at emission.
func (m *mcomp) complete(pk Pat, q ast.Pattern, keys []uint64) bool {
	has := func(v uint64) bool {
		for _, k := range keys {
			if k == v {
				return true
			}
		}
		return false
	}
	switch pk.Kind {
	case PVariant:
		for _, v := range m.bl.enumDecl(pk, q.Span()).Variants {
			if !has(v.Discr) {
				return false
			}
		}
		return true
	case PSome, PNone:
		return has(0) && has(1)
	case PLit:
		if l := m.bl.patLit(q); l.Type == types.Bool {
			return has(0) && has(1)
		}
	}
	return false
}

// place is the MIR place of a column.
func (m *mcomp) place(i int) mir.Place {
	for len(m.places) <= i {
		m.places = append(m.places, mir.Place{})
		m.have = append(m.have, false)
	}
	if m.have[i] {
		return m.places[i]
	}
	p := m.paths[i]
	var out mir.Place
	switch p.kind {
	case stepSome:
		out = m.bl.somePayload(m.place(p.parent))
	case stepPayload:
		out = m.bl.subField(m.bl.downcast(m.place(p.parent), p.sel, p.a), p.f, p.pos)
	default:
		source.ICEf(source.Span{}, "lower: decision tree root column has no place")
	}
	m.places[i], m.have[i] = out, true
	return out
}

// emit lowers a tree node from the current block.
func (m *mcomp) emit(n *dnode, entry func(arm int) mir.BlockID, failAt source.Span) {
	bl := m.bl
	// target is the block an edge to child takes: a leaf's arm entry
	// itself, else a fresh block the child is emitted into after.
	var pending []*dnode
	var blocks []mir.BlockID
	target := func(child *dnode) mir.BlockID {
		if child.kind == dLeaf {
			return entry(child.arm)
		}
		b := bl.newBlock()
		pending, blocks = append(pending, child), append(blocks, b)
		return b
	}
	switch n.kind {
	case dLeaf:
		bl.gotoBlock(entry(n.arm), failAt)
	case dFail:
		bl.term(mir.Terminator{Kind: mir.TermUnreachable}, failAt)
	case dStr:
		p := m.place(n.col)
		k := bl.newTemp(types.Bool, n.at)
		bl.assign(bl.b.Place(k), mir.Rvalue{Kind: mir.RvBinary, Bin: mir.Eq, A: mir.Copy(p), B: mir.Const(n.lit.Type, n.lit.Const), Type: types.Bool}, n.at)
		yes := target(n.yes)
		no := target(n.no)
		bl.boolSwitch(mir.Copy(bl.b.Place(k)), yes, no, n.at)
	case dSwitch:
		p := m.place(n.col)
		on := mir.Copy(p)
		if n.disc {
			on = bl.discr(p, n.at)
		}
		cases := make([]mir.SwitchCase, len(n.cases))
		for i, c := range n.cases {
			cases[i] = mir.SwitchCase{Value: c.val, Target: target(c.child)}
		}
		def := mir.NoBlock
		if n.def != nil && !(n.errs && n.def.kind == dFail && bl.errorsCovered(bl.b.PlaceType(p), n.cases)) {
			def = target(n.def)
		}
		bl.switchOn(on, cases, def, n.at)
	}
	for i, c := range pending {
		bl.cur = blocks[i]
		m.emit(c, entry, failAt)
	}
}

// errorsCovered reports whether the cases name every error of the error
// set t (a set with a parameter member is never covered).
func (bl *builder) errorsCovered(t types.TypeID, cases []dcase) bool {
	ty := bl.tab.Get(t)
	if ty.Kind != types.KErrorSet {
		return false
	}
	for _, mem := range bl.tab.Members(ty.Errors) {
		if mem.IsParam {
			return false
		}
		code := uint64(bl.errorCode(mem.Item, source.Span{}))
		found := false
		for _, c := range cases {
			if c.val == code {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// patternNodes counts the pattern nodes of the arms (the size guard's
// unit).
func patternNodes(arms []*ast.Arm, pat func(*ast.Arm) ast.Pattern) int {
	n := 0
	for _, a := range arms {
		ast.Inspect(pat(a), func(x ast.Node) bool {
			if _, ok := x.(ast.Pattern); ok {
				n++
			}
			return x != nil
		})
	}
	return n
}

// decisionTree compiles the arms over p into a tree from the current block
// and returns the arm entry blocks (NoBlock for an arm no leaf reaches), or
// ok false when the size guard falls back (nothing emitted then).
func (bl *builder) decisionTree(arms []*ast.Arm, p mir.Place, pat func(*ast.Arm) ast.Pattern) ([]mir.BlockID, bool) {
	factor := matchLeafFactor
	if f := bl.cfg.matchLeaves; f != 0 {
		factor = max(f, 0)
	}
	m := &mcomp{bl: bl, paths: []dpath{{parent: -1}}, limit: factor * patternNodes(arms, pat)}
	m.places, m.have = []mir.Place{p}, []bool{true}
	rows := make([]mrow, len(arms))
	for i, a := range arms {
		rows[i] = mrow{pats: []ast.Pattern{pat(a)}, arm: i}
	}
	tree := m.build(rows, []int{0})
	if m.abort || tree == nil {
		return nil, false
	}
	entries := make([]mir.BlockID, len(arms))
	for i := range entries {
		entries[i] = mir.NoBlock
	}
	entry := func(arm int) mir.BlockID {
		if entries[arm] == mir.NoBlock {
			entries[arm] = bl.newBlock()
		}
		return entries[arm]
	}
	m.emit(tree, entry, arms[len(arms)-1].Span())
	return entries, true
}
