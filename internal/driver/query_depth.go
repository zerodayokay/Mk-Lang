// Why: divide and conquer (a Karatsuba product, a recursive division, a
// parser's nesting) recurses, but no deeper than a limit the code keeps, and
// `mk query stack` should state that bound rather than give up at the first
// recursion.
//
// What: depthBound, which decides whether a recursion is bounded by a depth
// parameter, and sccs, the strongly connected components of the direct call
// graph that `stack` reaches.
//
// How: a recursion is a component of direct calls with a cycle. It is
// bounded when each of its functions has a u8 or u16 parameter, its depth,
// of one type across the component, that every call inside the component
// passes as the caller's own depth or as the caller's depth minus a positive
// constant (Sub, which traps below zero, or SubNoWrap, proved not to), and
// the calls that pass the depth unchanged form no cycle. Then every cycle
// lowers the depth, which never goes below zero, so a chain of nested calls
// inside the component is at most max+1 runs of distinct functions, max the
// type's largest value: (max+1) n frames for n functions. Each parameter of
// the component's first function that may be its depth is tried in turn;
// the other functions' depths then follow from the calls. An argument is
// followed back through copies and single assignments of locals only, to a
// parameter the body never writes; anything else is no depth, and the
// recursion stays unbounded. The check reads only MIR the session holds.

package driver

import (
	"strings"

	"mk/internal/mir"
	"mk/internal/types"
)

// recBound is a recursion bounded by a depth parameter.
type recBound struct {
	funcs  []types.ItemID
	param  string // the depth parameter's name in the first function
	ty     string // u8 or u16
	frames int    // (max+1) * len(funcs)
}

// sccs is the strongly connected components of the direct calls among
// nodes (Tarjan's algorithm, iterative), and each node's component.
func (qx *queryIndex) sccs(nodes []types.ItemID) ([][]types.ItemID, map[types.ItemID]int) {
	in := make(map[types.ItemID]bool, len(nodes))
	for _, n := range nodes {
		in[n] = true
	}
	index := map[types.ItemID]int{}
	low := map[types.ItemID]int{}
	onStack := map[types.ItemID]bool{}
	comp := map[types.ItemID]int{}
	var comps [][]types.ItemID
	var stack []types.ItemID
	type frame struct {
		id types.ItemID
		at int
	}
	next := 0
	for _, root := range nodes {
		if _, seen := index[root]; seen {
			continue
		}
		work := []frame{{id: root}}
		index[root], low[root] = next, next
		next++
		stack = append(stack, root)
		onStack[root] = true
		for len(work) > 0 {
			f := &work[len(work)-1]
			calls := qx.callsOf(f.id)
			if f.at < len(calls) {
				c := calls[f.at]
				f.at++
				if c.indirect || !in[c.callee] {
					continue
				}
				if _, seen := index[c.callee]; !seen {
					index[c.callee], low[c.callee] = next, next
					next++
					stack = append(stack, c.callee)
					onStack[c.callee] = true
					work = append(work, frame{id: c.callee})
				} else if onStack[c.callee] && index[c.callee] < low[f.id] {
					low[f.id] = index[c.callee]
				}
				continue
			}
			id := f.id
			work = work[:len(work)-1]
			if len(work) > 0 {
				if p := work[len(work)-1].id; low[id] < low[p] {
					low[p] = low[id]
				}
			}
			if low[id] == index[id] {
				var c []types.ItemID
				for {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					onStack[top] = false
					comp[top] = len(comps)
					c = append(c, top)
					if top == id {
						break
					}
				}
				comps = append(comps, c)
			}
		}
	}
	return comps, comp
}

// recursive reports whether component c has a cycle: more than one
// function, or one that calls itself.
func (qx *queryIndex) recursive(c []types.ItemID) bool {
	if len(c) > 1 {
		return true
	}
	for _, s := range qx.callsOf(c[0]) {
		if !s.indirect && s.callee == c[0] {
			return true
		}
	}
	return false
}

// depthTypes are the types a depth parameter may have, with their largest
// values.
var depthTypes = map[types.TypeID]int{types.U8: 255, types.U16: 65535}

// depthBound is the bound of recursion c, or nil when no depth parameter
// bounds it. It tries each parameter of c[0] that may be a depth: the
// depth of every other function then follows from the calls (each caller
// passes its own depth as its callee's), and the choice holds when every
// call inside c agrees with it.
func (qx *queryIndex) depthBound(c []types.ItemID) *recBound {
	inC := map[types.ItemID]bool{}
	for _, f := range c {
		inC[f] = true
	}
	wr := map[types.ItemID]map[mir.LocalID]int{}
	for _, f := range c {
		b := qx.bodyOf(f)
		if b == nil {
			return nil
		}
		wr[f] = writes(b)
	}
	// derive is the caller's parameter that call s passes as the callee's
	// parameter p, and whether lowered; ok false when it is no such value.
	derive := func(from types.ItemID, s callSite, p mir.LocalID) (mir.LocalID, bool, bool) {
		b := qx.bodyOf(from)
		j := uint32(p) - 1
		if !s.hasArgs || j >= s.args.Len {
			return 0, false, false
		}
		return depthArg(b, wr[from], b.Args[s.args.Start+j].Operand)
	}
	// in[g] are the calls into g from inside c.
	in := map[types.ItemID][]inCall{}
	for _, f := range c {
		for _, s := range qx.callsOf(f) {
			if !s.indirect && inC[s.callee] {
				in[s.callee] = append(in[s.callee], inCall{from: f, s: s})
			}
		}
	}
	b0 := qx.bodyOf(c[0])
	for p := mir.LocalID(1); int(p) <= int(b0.ParamCount) && int(p) < len(b0.Locals); p++ {
		if _, ok := depthTypes[b0.Locals[p].Type]; !ok || wr[c[0]][p] != 0 {
			continue
		}
		if rb := qx.boundBy(c, in, derive, p); rb != nil {
			return rb
		}
	}
	return nil
}

// inCall is a call from function from, inside a recursion.
type inCall struct {
	from types.ItemID
	s    callSite
}

// boundBy is the bound of recursion c when parameter p of c[0] is its
// depth, or nil when the calls inside c do not keep it one.
func (qx *queryIndex) boundBy(c []types.ItemID, in map[types.ItemID][]inCall,
	derive func(types.ItemID, callSite, mir.LocalID) (mir.LocalID, bool, bool), p mir.LocalID) *recBound {
	// Every function of c reaches c[0], so following the calls back from
	// c[0] gives each its depth: the parameter it passes as its callee's.
	depth := map[types.ItemID]mir.LocalID{c[0]: p}
	for work := []types.ItemID{c[0]}; len(work) > 0; {
		g := work[len(work)-1]
		work = work[:len(work)-1]
		for _, k := range in[g] {
			if _, ok := depth[k.from]; ok {
				continue
			}
			q, _, ok := derive(k.from, k.s, depth[g])
			if !ok {
				return nil
			}
			depth[k.from] = q
			work = append(work, k.from)
		}
	}
	if len(depth) != len(c) {
		return nil
	}
	// Check every call inside c with it: one type, each call passing the
	// caller's depth, lowered or not.
	ty := qx.bodyOf(c[0]).Locals[p].Type
	same := map[types.ItemID][]types.ItemID{} // calls that keep the depth
	for _, f := range c {
		if qx.bodyOf(f).Locals[depth[f]].Type != ty {
			return nil
		}
	}
	for g, ks := range in {
		for _, k := range ks {
			q, lowered, ok := derive(k.from, k.s, depth[g])
			if !ok || q != depth[k.from] {
				return nil
			}
			if !lowered {
				same[k.from] = append(same[k.from], g)
			}
		}
	}
	if hasCycle(c, same) {
		return nil
	}
	return &recBound{
		funcs:  c,
		param:  qx.str(qx.bodyOf(c[0]).Locals[p].Name),
		ty:     qx.w.tab.TypeString(ty, nil),
		frames: (depthTypes[ty] + 1) * len(c),
	}
}

// hasCycle reports whether the edges among nodes have a cycle.
func hasCycle(nodes []types.ItemID, edges map[types.ItemID][]types.ItemID) bool {
	color := map[types.ItemID]int{} // 0 white, 1 grey, 2 black
	var visit func(types.ItemID) bool
	visit = func(id types.ItemID) bool {
		color[id] = 1
		for _, n := range edges[id] {
			if color[n] == 1 || color[n] == 0 && visit(n) {
				return true
			}
		}
		color[id] = 2
		return false
	}
	for _, n := range nodes {
		if color[n] == 0 && visit(n) {
			return true
		}
	}
	return false
}

// writes counts the writes of each local of b: assignments, call
// results, asm outputs and mut borrows (which may write).
func writes(b *mir.Body) map[mir.LocalID]int {
	w := map[mir.LocalID]int{}
	for _, bl := range b.Blocks {
		for _, st := range bl.Stmts {
			switch st.Kind {
			case mir.StAssign:
				w[st.Place.Local]++
				r := &st.Rvalue
				if (r.Kind == mir.RvRef || r.Kind == mir.RvSlice) && r.Borrow == mir.Exclusive {
					w[r.Place.Local]++
				}
			case mir.StAsm:
				for _, op := range b.Asms[st.Asm].Operands {
					if op.Out.Local != mir.NoLocal {
						w[op.Out.Local]++
					}
				}
			}
		}
		if t := bl.Term; t.Kind == mir.TermCall {
			w[t.Dest.Local]++
		}
	}
	return w
}

// depthArg follows operand o of b back through copies and single
// assignments to a parameter b never writes (w counts b's writes): that parameter, and whether a
// positive constant was subtracted on the way (Sub or SubNoWrap).
func depthArg(b *mir.Body, w map[mir.LocalID]int, o mir.Operand) (mir.LocalID, bool, bool) {
	lowered := false
	for range 16 {
		if o.Kind != mir.OpCopy || o.Place.Proj.Len != 0 {
			return 0, false, false
		}
		l := o.Place.Local
		if int(l) >= len(b.Locals) {
			return 0, false, false
		}
		if b.Locals[l].Kind == mir.LocalParam {
			return l, lowered, w[l] == 0
		}
		if w[l] != 1 {
			return 0, false, false
		}
		r := assignOf(b, l)
		if r == nil {
			return 0, false, false
		}
		switch {
		case r.Kind == mir.RvUse:
			o = r.A
		case r.Kind == mir.RvBinary && (r.Bin == mir.Sub || r.Bin == mir.SubNoWrap) &&
			r.B.Kind == mir.OpImm && r.B.Bits > 0:
			lowered = true
			o = r.A
		default:
			return 0, false, false
		}
	}
	return 0, false, false
}

// assignOf is the rvalue of l's one plain assignment in b, nil when its one
// write is something else.
func assignOf(b *mir.Body, l mir.LocalID) *mir.Rvalue {
	for i := range b.Blocks {
		for k := range b.Blocks[i].Stmts {
			st := &b.Blocks[i].Stmts[k]
			if st.Kind == mir.StAssign && st.Place.Local == l && st.Place.Proj.Len == 0 {
				return &st.Rvalue
			}
		}
	}
	return nil
}

// cycleNames is one cycle of recursion c as "A -> B -> A".
func (qx *queryIndex) cycleNames(c []types.ItemID) string {
	inC := map[types.ItemID]bool{}
	for _, f := range c {
		inC[f] = true
	}
	// Follow calls inside c from c[0] until a function repeats.
	seen := map[types.ItemID]int{}
	var path []types.ItemID
	for id := c[0]; ; {
		if i, ok := seen[id]; ok {
			path = append(path[i:], id)
			break
		}
		seen[id] = len(path)
		path = append(path, id)
		next := types.NoItem
		for _, s := range qx.callsOf(id) {
			if !s.indirect && inC[s.callee] {
				next = s.callee
				break
			}
		}
		if next == types.NoItem {
			break // INVARIANT: c is a recursion, so each of its functions calls into it
		}
		id = next
	}
	ns := make([]string, len(path))
	for i, id := range path {
		ns[i] = qx.name(id)
	}
	return strings.Join(ns, " -> ")
}
