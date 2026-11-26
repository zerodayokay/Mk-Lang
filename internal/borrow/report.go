// Why: one mistake in a body can trip many checks; the user should see each
// error once, in a stable order, in Mk's own spelling of places and
// signatures.
//
// What: errAt, the names of roots, parameters, places and fields
// (rootName, paramName, placeString, fieldName), finish (body-level reports,
// suppression, order, Facts.OnerrRuns), and the signature facts (sigString,
// flowClauses, sigFact).
//
// How: a failed boundary check reports alone; cascades are suppressed;
// diagnostics are deduplicated on (code, primary span) and sorted into one
// total order. A temporary is "this temporary", never an internal name.

package borrow

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"mk/internal/diag"
	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

// errAt builds a diagnostic anchored at span sp and collects it. Only the
// report pass calls it.
func (c *checker) errAt(code diag.Code, sp mir.SpanID, format string, args ...any) *diag.Diagnostic {
	d := diag.New(code, c.body.Span(sp), fmt.Sprintf(format, args...))
	c.diags = append(c.diags, d)
	return d
}

func (c *checker) symbol(s source.Symbol) string {
	if s == source.NoSymbol || c.tab == nil {
		return ""
	}
	return c.tab.Interner().Str(s)
}

// rootName is a root's source name; a temporary is "this temporary", never an
// internal name.
func (c *checker) rootName(r rootID) string {
	ri := &c.roots[r]
	switch ri.kind {
	case rkAbstract:
		return "*" + c.paramName(ri.param)
	case rkStatic:
		if c.tab != nil && ri.item != types.NoItem {
			return c.symbol(c.tab.Items().Get(ri.item).Name)
		}
		return "global"
	case rkSlot:
		if ri.local != mir.NoLocal && int(ri.local) < len(c.body.Locals) {
			if c.body.Locals[ri.local].Kind == mir.LocalTemp {
				return "this temporary"
			}
			return c.rootName(rootID(ri.local))
		}
		return "the reborrowed argument"
	case rkReturn:
		return "the result"
	case rkTemp:
		return "this temporary"
	case rkHolder:
		return "the loop's source"
	}
	if n := c.symbol(c.body.Locals[ri.local].Name); n != "" {
		return n
	}
	return "_" + strconv.Itoa(int(ri.local))
}

func (c *checker) paramName(p types.ParamIndex) string {
	if p == types.NoParam || int(p) >= len(c.body.Locals) {
		return "a parameter"
	}
	if n := c.symbol(c.body.Locals[p].Name); n != "" {
		return n
	}
	return "parameter " + strconv.Itoa(int(p))
}

// placeString spells a path in Mk: `p.f`, `*r`, `xs[_]`. A path rooted at a
// hidden environment parameter peels the synthetic `*env.x` (and wrapper deref)
// so diagnostics name the captured variable as the user wrote it.
func (c *checker) placeString(p pathID) string {
	pi := c.paths.get(p)
	s := c.rootName(pi.root)
	before := pi.rootType
	start := 0
	if ri := &c.roots[pi.root]; ri.kind == rkParam && c.hiddenParam(ri.param) {
		fs := c.envFields(ri.param)
		if len(pi.steps) >= 2 && pi.steps[0].kind == stDeref && pi.steps[1].kind == stField && int(pi.steps[1].a) < len(fs) {
			ft := fs[pi.steps[1].a].Type
			s = c.symbol(fs[pi.steps[1].a].Name)
			before = pi.types[1]
			start = 2
			if (c.tab.Kind(ft) == types.KRef || c.tab.Kind(ft) == types.KMut) && start < len(pi.steps) && pi.steps[start].kind == stDeref {
				before = pi.types[start]
				start++
			}
		}
	}
	for i := start; i < len(pi.steps); i++ {
		st := pi.steps[i]
		switch st.kind {
		case stField:
			s += "." + c.fieldName(before, st.a)
		case stDeref:
			if i+1 < len(pi.steps) && pi.steps[i+1].kind == stIndex {
				break // a slice index is written xs[_]
			}
			if i+1 < len(pi.steps) && pi.steps[i+1].kind == stField {
				break // r.f reads through r
			}
			s = "*" + s
		case stIndex:
			s += "[_]"
		}
		before = pi.types[i]
	}
	return s
}

// sharedPlace spells path p for an E0507. A use of a global is a temporary
// reference to it, so `Table[7] = 42` is the path (*t)[_] of a temporary t;
// when p's root is such a temporary, holding only the static loan of one var
// global, and its first step is the deref through it, p is spelled from the
// global (`Table[_]`, `P.n`, `Total`, `Table[0..2][_]` through a shared
// slice of it), and global names it when p is in the global's own memory
// (one deref). Any other path is placeString's.
func (c *checker) sharedPlace(st *state, p pathID) (place, global string) {
	name, place, ok := c.globalPlace(st, p)
	if !ok {
		return c.placeString(p), ""
	}
	if c.paths.get(p).derefs != 1 {
		return place, "" // through a reference the global holds
	}
	return place, name
}

// globalPlace is sharedPlace's spelling from a global: its name and the
// place, or ok false.
func (c *checker) globalPlace(st *state, p pathID) (name, place string, ok bool) {
	pi := c.paths.get(p)
	if st == nil || c.tab == nil || len(pi.steps) == 0 || pi.steps[0].kind != stDeref {
		return "", "", false
	}
	ri := &c.roots[pi.root]
	temp := ri.kind == rkTemp || ri.kind == rkSlot && ri.local != mir.NoLocal && int(ri.local) < len(c.body.Locals) && c.body.Locals[ri.local].Kind == mir.LocalTemp
	if !temp {
		return "", "", false
	}
	item := types.NoItem
	for _, l := range c.sets.get(c.held(st, pi.root)) {
		lo := &c.loans[l]
		if !lo.static || lo.target == noRoot || c.roots[lo.target].kind != rkStatic {
			return "", "", false
		}
		it := c.roots[lo.target].item
		if it == types.NoItem || item != types.NoItem && it != item {
			return "", "", false
		}
		item = it
	}
	if item == types.NoItem {
		return "", "", false
	}
	// The deref reaches the global itself, not memory a reference read out
	// of it or returned from its method points to. An extern var is no Mk
	// var: it is written through its address in unsafe, not its methods.
	g, global := c.tab.Items().Get(item).Decl.(*types.GlobalDecl)
	if !global || g.Extern {
		return "", "", false
	}
	name = c.symbol(c.tab.Items().Get(item).Name)
	head := name
	if pi.types[0] != g.Type {
		// A shared slice of the global (`Table[0..2]`) reaches the global's
		// own memory when the global holds no reference: then nothing its
		// static loan reaches is memory it points to. The slice is spelled
		// as written.
		if c.tab.Kind(pi.rootType) != types.KSlice || c.w.carrier(g.Type) {
			return "", "", false
		}
		head = c.tempText(pi.root)
		if head == "" {
			head = name + "[..]"
		}
	}
	if len(pi.steps) == 1 {
		return name, head, true
	}
	rest, cut := strings.CutPrefix(c.placeString(p), c.rootName(pi.root))
	if !cut {
		return "", "", false
	}
	return name, head + rest, true
}

// tempText is the one-line source text a temporary root was made from, or
// "".
func (c *checker) tempText(r rootID) string {
	ri := &c.roots[r]
	if c.env.File == nil || ri.local == mir.NoLocal || int(ri.local) >= len(c.body.Locals) {
		return ""
	}
	sp := c.body.Span(c.body.Locals[ri.local].Span)
	f := c.env.File(sp.File)
	if f == nil || sp.End > f.Len() || sp.End <= sp.Start {
		return ""
	}
	if text := f.Slice(sp); !strings.Contains(text, "\n") {
		return text
	}
	return ""
}

// globalE0507 is E0507 for a write (verb "assigned") or a mut borrow (verb
// "borrowed mut") of a var global: a global is never either, and changes only
// through its methods.
func (c *checker) globalE0507(ctx *stmtCtx, name, place, verb string) *diag.Diagnostic {
	msg := place + " is a global, which is never " + verb
	if place != name {
		msg = place + " is part of the global " + name + ", which is never " + verb
	}
	return c.errAt(diag.E0507, ctx.span, "%s", msg).Text("place", place).Text("global", name).
		Alt("a global changes only through its methods: make it, or the part that changes, an atomic or a Mutex")
}

// fieldName is the name of field i of type t, or its index.
func (c *checker) fieldName(t types.TypeID, i uint32) string {
	if c.tab == nil || t == types.NoType {
		return strconv.Itoa(int(i))
	}
	ty := c.tab.Get(t)
	for ty.Kind == types.KRef || ty.Kind == types.KMut {
		ty = c.tab.Get(ty.Elem)
	}
	switch ty.Kind {
	case types.KRecord:
		if int(i) < len(ty.Fields) {
			return c.symbol(ty.Fields[i].Name)
		}
	case types.KNamed:
		if d, ok := c.tab.Items().Get(ty.Item).Decl.(*types.StructDecl); ok && int(i) < len(d.Fields) {
			return c.symbol(d.Fields[i].Name)
		}
	}
	return strconv.Itoa(int(i))
}

// finish is the last steps of Check: body-level reports, suppression, order,
// facts.
func (c *checker) finish() {
	c.emitE0411()
	c.endsDecl()
	diags := c.diags
	// A failed boundary check reports alone.
	if len(c.boundaryFail) > 0 {
		kept := diags[:0]
		for _, d := range diags {
			drop := false
			for _, sp := range c.boundaryFail {
				fs := c.body.Span(sp)
				if d.At.File == fs.File && d.At.Start >= fs.Start && d.At.End <= fs.End &&
					!(d.At == fs && (d.Code == diag.E0602 || d.Code == diag.E0603 || d.Code == diag.E0609)) {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, d)
			}
		}
		diags = kept
	}
	slices.SortStableFunc(diags, func(a, b *diag.Diagnostic) int {
		return cmp.Or(cmp.Compare(a.At.File, b.At.File), cmp.Compare(a.At.Start, b.At.Start),
			cmp.Compare(a.Code, b.Code), strings.Compare(a.Message, b.Message), cmp.Compare(a.At.End, b.At.End))
	})
	// A static of global memory stored into a parameter's node is E0604 with
	// no clause to fix it, and the node then holds the static, so a later
	// round's walk through it stores the same value into global memory, an
	// E0605 that only repeats the E0604. At a span with such an E0604, that
	// E0605 is dropped, and the static E0604 is kept over a clause E0604
	// there, whose fix would leave it.
	var static map[source.Span]bool
	for _, d := range diags {
		if c.staticStore[d] {
			if static == nil {
				static = map[source.Span]bool{}
			}
			static[d.At] = true
		}
	}
	out := diags[:0]
	for _, d := range diags {
		if d.Code == diag.E0605 && static[d.At] {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Code == d.Code && out[n-1].At == d.At {
			if d.Code == diag.E0604 {
				if c.staticStore[d] && !c.staticStore[out[n-1]] {
					out[n-1] = d
				} else if !c.staticStore[d] && !c.staticStore[out[n-1]] {
					mergeStoreFix(out[n-1], d)
				}
			}
			continue
		}
		out = append(out, d)
	}
	if len(out) > 0 {
		c.res.Diags = out
	}
	slices.SortFunc(c.guards, func(a, b OnerrRun) int { return cmp.Compare(a.Block, b.Block) })
	c.guards = slices.CompactFunc(c.guards, func(a, b OnerrRun) bool { return a.Block == b.Block })
	c.res.Facts.OnerrRuns = c.guards
}

func sortU32(xs []uint32) { slices.Sort(xs) }

// sigString spells a signature for the `signature` fact:
// `Name(p: T, ...): R`, with the receiver as self, &self or mut self.
func (c *checker) sigString(s *types.Signature) string {
	var b strings.Builder
	b.WriteString(c.symbol(s.Name))
	b.WriteByte('(')
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if i == 0 && s.Receiver != types.NoReceiver {
			b.WriteString([...]string{"", "self", "&self", "mut self"}[s.Receiver])
		} else {
			b.WriteString(c.symbol(p.Name))
			b.WriteString(": ")
			b.WriteString(c.tab.TypeString(p.Type, s.Generics))
		}
		if p.Into != types.NoParam {
			b.WriteString(" into " + sigParam(c, s, p.Into))
		}
	}
	b.WriteByte(')')
	if rt := s.ReturnType(c.tab); rt != types.Void {
		b.WriteString(": ")
		b.WriteString(c.tab.TypeString(rt, s.Generics))
	}
	if f := c.flowClauses(s); f != "" {
		b.WriteString(" " + f)
	}
	return b.String()
}

// sigParam is parameter p of s as a clause names it: self, or its name.
func sigParam(c *checker, s *types.Signature, p types.ParamIndex) string {
	if p == types.NoParam || int(p) > len(s.Params) {
		return "?"
	}
	if int(p) == 1 && s.Receiver != types.NoReceiver {
		return "self"
	}
	return c.symbol(s.Params[p-1].Name)
}

// flowClauses spells s's declared flow clauses as the reference writes them
// (a signature in a diagnostic prints with its from and into clauses):
// `from p, q.f`, `*x into y`, `ends p`. An automatic answer is not written in source,
// so it is not printed.
func (c *checker) flowClauses(s *types.Signature) string {
	var parts []string
	if s.Flows.Declared {
		var from []string
		for _, src := range s.Flows.Sources {
			n := sigParam(c, s, src.Param)
			if src.Param != types.NoParam && int(src.Param) <= len(s.Params) {
				t := s.Params[src.Param-1].Type
				for _, st := range src.Path {
					n += "." + c.fieldName(t, st.Field)
					t = st.Type
				}
			}
			from = append(from, n)
		}
		if len(from) > 0 {
			parts = append(parts, "from "+strings.Join(from, ", "))
		}
	}
	var st []string
	for _, x := range s.Flows.Stores {
		st = append(st, "*"+sigParam(c, s, x.From)+" into "+sigParam(c, s, x.Into))
	}
	if len(st) > 0 {
		parts = append(parts, strings.Join(st, ", "))
	}
	var ends []string
	for _, p := range s.Flows.Ends {
		ends = append(ends, sigParam(c, s, p))
	}
	if len(ends) > 0 {
		parts = append(parts, "ends "+strings.Join(ends, ", "))
	}
	return strings.Join(parts, " ")
}

// sigFact adds the `signature` fact of the callee when the access is a call's
// argument.
func (c *checker) sigFact(d *diag.Diagnostic, ctx *stmtCtx) {
	if !ctx.term || c.env.Callee == nil {
		return
	}
	t := &c.body.Blocks[ctx.b].Term
	if !isCall(t) || t.Callee.Kind != mir.CallDirect {
		return
	}
	if s := c.env.Callee(t.Callee.Fn.Item); s != nil {
		d.Text("signature", c.sigString(s))
	}
}
