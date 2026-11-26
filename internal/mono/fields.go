// Why: a generic fields loop (Fields[B], fields(&x)) visits the fields of a
// type it does not know, so each instance must unroll it over the concrete
// type's fields.
//
// What: the unrolling of fields loops per instance, with loopFields (every
// field of a record, the exported fields of a struct) and strConst.
//
// How: lower gives a loop with FieldsOf set the blocks exactly [Header,
// Exit): the header is goto body, every continue and the body's end go to
// one latch whose terminator is goto Exit, and break goes to Exit.
// unrollLoop copies that range once per field and chains each copy's latch
// to the next copy's header.

package mono

import (
	"encoding/binary"

	"mk/internal/mir"
	"mk/internal/types"
)

// loopField is one field a fields loop visits: its index in the struct or
// record, its type in the instance and its name.
type loopField struct {
	index uint32
	typ   types.TypeID
	name  string
}

// loopFields lists the fields a generic fields loop over t visits, in
// declaration order: every field of a record, the exported fields of a
// struct (Fields[B]).
func (bl *builder) loopFields(t types.TypeID) []loopField {
	ty := bl.tab.Get(t)
	in := bl.tab.Interner()
	var out []loopField
	switch ty.Kind {
	case types.KRecord:
		for i, f := range ty.Fields {
			out = append(out, loopField{index: uint32(i), typ: f.Type, name: in.Str(f.Name)})
		}
	case types.KNamed:
		if k := bl.opt.Known; k != nil && ty.Item == k.Scope {
			// Scope[E] is {hdr, err} whatever its declaration says;
			// neither field is exported, so no Fields[B] loop reaches it.
			ice("fields loop over %s", bl.tab.TypeString(t, nil))
		}
		sd, ok := bl.env.Items().Get(ty.Item).Decl.(*types.StructDecl)
		if !ok {
			ice("fields loop over %s, not a struct", bl.tab.TypeString(t, nil))
		}
		for i, f := range sd.Fields {
			if f.Exported {
				out = append(out, loopField{index: uint32(i), typ: Subst(bl.tab, f.Type, ty.Args), name: in.Str(f.Name)})
			}
		}
	default:
		ice("fields loop over %s", bl.tab.TypeString(t, nil))
	}
	return out
}

// unrollFields unrolls every generic fields loop of the substituted body.
// Nested generic fields loops are not unrolled (an ICE).
func (bl *builder) unrollFields() {
	b := bl.b
	for l := range b.Loops {
		lp := b.Loops[l]
		if lp.FieldsOf == types.NoType {
			continue
		}
		for j, o := range b.Loops {
			if j != l && o.FieldsOf != types.NoType && o.Header >= lp.Header && o.Header < lp.Exit {
				ice("nested generic fields loops are not unrolled yet")
			}
		}
		bl.unrollLoop(mir.LoopID(l))
	}
}

// unrollLoop copies loop l's range once per field. Copies 1..n-1 are
// appended, built from the original blocks; then the original range becomes
// copy 0 in place. Each copy's latch goes to the next copy's header, the
// last one's to Exit; break still goes to Exit. With no field, the edges
// into the header go to Exit and the range is dead.
func (bl *builder) unrollLoop(l mir.LoopID) {
	b := bl.b
	lp := b.Loops[l]
	h, e := lp.Header, lp.Exit
	latch := h + 2 // header, body, latch: lower's allocation order
	if latch >= e || b.Blocks[latch].Term.Kind != mir.TermGoto || b.Blocks[latch].Term.Target != e {
		ice("fields loop %d: bb%d is not a latch to its exit bb%d", l, latch, e)
	}
	fs := bl.loopFields(lp.FieldsOf)
	n := len(fs)
	size := int(e - h)
	heads := make([]mir.BlockID, n+1)
	heads[n] = e
	if n > 0 {
		heads[0] = h
	}
	base := len(b.Blocks)
	for c := 1; c < n; c++ {
		heads[c] = mir.BlockID(base + (c-1)*size)
	}
	for c := 1; c < n; c++ {
		bl.copyRange(l, latch, heads[c], heads[c+1], fs[c], false, false)
	}
	if n > 0 {
		bl.copyRange(l, latch, h, heads[1], fs[0], true, false)
	} else {
		// The range is dead: give its hidden types a closed stand-in so
		// no local keeps a parameter; prune drops the blocks.
		bl.copyRange(l, latch, h, e, loopField{typ: types.Void}, true, true)
		for x := range b.Blocks {
			if mir.BlockID(x) >= h && mir.BlockID(x) < e {
				continue
			}
			t := &b.Blocks[x].Term
			if t.Kind == mir.TermGoto && t.Target == h {
				t.Target = e
			}
		}
	}
	b.Loops[l].FieldsOf, b.Loops[l].FieldsParam = types.NoType, 0
}

// rangeCopy is one copy of a loop's range.
type rangeCopy struct {
	bl      *builder
	b       *mir.Body
	l       mir.LoopID
	h, e    mir.BlockID
	latch   mir.BlockID
	dst     mir.BlockID
	next    mir.BlockID
	f       loopField
	inPlace bool
	s       substituter
	locals  map[mir.LocalID]mir.LocalID
	loops   map[mir.LoopID]mir.LoopID
	// In place, each shared range is rewritten once: projections,
	// operands and call arguments are separate slices.
	doneProj, doneOps, doneArgs map[mir.Range]bool
}

// copyRange writes the copy of loop l's range for field f at dst (in place
// when dst is the header itself), with its latch going to next. A dead
// range only gets closed types; its interface calls stay unresolved.
func (bl *builder) copyRange(l mir.LoopID, latch, dst, next mir.BlockID, f loopField, inPlace, dead bool) {
	b := bl.b
	lp := b.Loops[l]
	args := make([]types.GenericArg, bl.gb.GenericCount)
	for i := range args {
		args[i] = types.TypeArg(bl.tab.Param(uint32(i)))
	}
	args[lp.FieldsParam] = types.TypeArg(f.typ)
	rc := &rangeCopy{bl: bl, b: b, l: l, h: lp.Header, e: lp.Exit, latch: latch, dst: dst, next: next, f: f,
		inPlace: inPlace, s: substituter{tab: bl.tab, args: args, fold: bl.sizeOf, memo: map[types.TypeID]types.TypeID{}},
		locals: map[mir.LocalID]mir.LocalID{}, loops: map[mir.LoopID]mir.LoopID{},
		doneProj: map[mir.Range]bool{}, doneOps: map[mir.Range]bool{}, doneArgs: map[mir.Range]bool{}}
	// Other loops inside the range get a LoopInfo per copy.
	if !inPlace {
		for j := range b.Loops {
			o := b.Loops[j]
			if mir.LoopID(j) != l && o.Header >= rc.h && o.Header < rc.e {
				o.Header, o.Exit = rc.block(o.Header), rc.block(o.Exit)
				b.Loops = append(b.Loops, o)
				rc.loops[mir.LoopID(j)] = mir.LoopID(len(b.Loops) - 1)
			}
		}
		// Each copied loop's holder is the copy's own holder local (in
		// loop order: locals are numbered as they are made).
		for j := range b.Loops {
			n, ok := rc.loops[mir.LoopID(j)]
			if h := b.Loops[j].Holder; ok && h != mir.NoLocal {
				b.Loops[n].Holder = rc.local(h)
			}
		}
	}
	for x := rc.h; x < rc.e; x++ {
		var blk mir.Block
		if inPlace {
			blk = b.Blocks[x]
		} else {
			blk = b.Blocks[x]
			blk.Stmts = append([]mir.Statement(nil), blk.Stmts...)
		}
		for si := range blk.Stmts {
			rc.stmt(&blk.Stmts[si])
		}
		rc.term(&blk.Term, x)
		if inPlace {
			b.Blocks[x] = blk
		} else {
			b.Blocks = append(b.Blocks, blk)
		}
		t := &b.Blocks[rc.block(x)].Term
		if !dead && isCall(t) && t.Callee.Kind == mir.CallInterface && closed(bl.tab, t.Callee.SelfType) {
			noTail(t, "a method through a bound")
			bl.curBlock = rc.block(x)
			bl.ifaceCall(t)
		}
	}
}

// block maps a block of the range to its copy; others are unchanged.
func (rc *rangeCopy) block(x mir.BlockID) mir.BlockID {
	if x >= rc.h && x < rc.e && x != mir.NoBlock {
		return rc.dst + (x - rc.h)
	}
	return x
}

func (rc *rangeCopy) loop(id mir.LoopID) mir.LoopID {
	if n, ok := rc.loops[id]; ok {
		return n
	}
	return id
}

// local maps a local whose type mentions the hidden generic to its copy's
// own local (retyped in place for copy 0); a loop holder of a loop inside
// the range gets its own local per copy too.
func (rc *rangeCopy) local(x mir.LocalID) mir.LocalID {
	if x == mir.NoLocal {
		return x
	}
	if n, ok := rc.locals[x]; ok {
		return n
	}
	b := rc.b
	lo := b.Locals[x]
	nt := rc.s.typ(lo.Type)
	_, inner := rc.loops[lo.Loop]
	if nt == lo.Type && !(lo.Kind == mir.LocalLoopHolder && inner) {
		rc.locals[x] = x
		return x
	}
	if rc.inPlace {
		b.Locals[x].Type = nt
		rc.locals[x] = x
		return x
	}
	lo.Type = nt
	if lo.Kind == mir.LocalLoopHolder {
		lo.Loop = rc.loop(lo.Loop)
	}
	n := b.AddLocal(lo)
	rc.locals[x] = n
	return n
}

func (rc *rangeCopy) step(st mir.Projection) mir.Projection {
	st.Type = rc.s.typ(st.Type)
	switch {
	case st.Kind == mir.ProjField && st.FieldLoop && st.A == uint32(rc.l):
		st.FieldLoop, st.A = false, rc.f.index
	case st.Kind == mir.ProjIndex:
		st.A = uint32(rc.local(mir.LocalID(st.A)))
	}
	return st
}

func (rc *rangeCopy) place(p mir.Place) mir.Place {
	np := mir.Place{Local: rc.local(p.Local), Proj: p.Proj}
	if p.Proj.Len == 0 {
		return np
	}
	b := rc.b
	if rc.inPlace {
		if !rc.doneProj[p.Proj] {
			rc.doneProj[p.Proj] = true
			for i := p.Proj.Start; i < p.Proj.End(); i++ {
				b.Projections[i] = rc.step(b.Projections[i])
			}
		}
		return np
	}
	steps := make([]mir.Projection, p.Proj.Len)
	for i := range steps {
		steps[i] = rc.step(b.Projections[p.Proj.Start+uint32(i)])
	}
	return b.Place(np.Local, steps...)
}

func (rc *rangeCopy) operand(o mir.Operand) mir.Operand {
	switch o.Kind {
	case mir.OpCopy, mir.OpMove:
		o.Place = rc.place(o.Place)
	default:
		o.Type = rc.s.typ(o.Type)
	}
	return o
}

// operands copies (or rewrites in place) a range of Body.Operands.
func (rc *rangeCopy) operands(r mir.Range) mir.Range {
	b := rc.b
	if r.Len == 0 {
		return r
	}
	if rc.inPlace {
		if !rc.doneOps[r] {
			rc.doneOps[r] = true
			for i := r.Start; i < r.End(); i++ {
				b.Operands[i] = rc.operand(b.Operands[i])
			}
		}
		return r
	}
	ops := make([]mir.Operand, r.Len)
	for i := range ops {
		ops[i] = rc.operand(b.Operands[r.Start+uint32(i)])
	}
	return b.AddOperands(ops...)
}

func (rc *rangeCopy) genericArgs(r mir.Range) mir.Range {
	b := rc.b
	if r.Len == 0 {
		return r
	}
	as := make([]types.GenericArg, r.Len)
	for i := range as {
		as[i] = rc.s.genericArg(b.GenericArgs[r.Start+uint32(i)])
	}
	if rc.inPlace {
		copy(b.GenericArgs[r.Start:r.End()], as)
		return r
	}
	return b.AddGenericArgs(as...)
}

func (rc *rangeCopy) stmt(st *mir.Statement) {
	switch st.Kind {
	case mir.StAssign:
		st.Place = rc.place(st.Place)
		rv := &st.Rvalue
		rv.Place = rc.place(rv.Place)
		rv.A, rv.B = rc.operand(rv.A), rc.operand(rv.B)
		rv.Type = rc.s.typ(rv.Type)
		if (rv.Kind == mir.RvRef || rv.Kind == mir.RvSlice) && rv.Origin != mir.OriginPlain {
			rv.Loop = rc.loop(rv.Loop)
		}
		if rv.Len.Kind != types.CANone {
			rv.Len = rc.s.constArg(rv.Len)
		}
		switch rv.Kind {
		case mir.RvAggregate:
			rv.Ops = rc.operands(rv.Ops)
		case mir.RvFnRef:
			rv.Fn.Args = rc.genericArgs(rv.Fn.Args)
		case mir.RvFieldName:
			if rv.Field.Loop && rv.Field.Index == uint32(rc.l) {
				*rv = mir.Rvalue{Kind: mir.RvUse, A: mir.Const(types.Str, rc.bl.strConst(rc.f.name))}
			}
		}
	case mir.StConsume:
		st.Place = rc.place(st.Place)
	case mir.StScopeEnd, mir.StJoin:
		st.Local = rc.local(st.Local)
	}
}

func (rc *rangeCopy) term(t *mir.Terminator, x mir.BlockID) {
	b := rc.b
	t.On, t.Msg = rc.operand(t.On), rc.operand(t.Msg)
	t.Type = rc.s.typ(t.Type)
	switch t.Kind {
	case mir.TermGoto:
		if x == rc.latch {
			t.Target = rc.next
		} else {
			t.Target = rc.block(t.Target)
		}
	case mir.TermSwitch:
		if !rc.inPlace {
			cs := append([]mir.SwitchCase(nil), b.Cases[t.Cases.Start:t.Cases.End()]...)
			t.Cases = b.AddCases(cs...)
		}
		for i := t.Cases.Start; i < t.Cases.End(); i++ {
			b.Cases[i].Target = rc.block(b.Cases[i].Target)
		}
		t.Default = rc.block(t.Default)
	case mir.TermCall:
		c := &t.Callee
		c.Value = rc.operand(c.Value)
		c.SelfType = rc.s.typ(c.SelfType)
		if c.Kind == mir.CallDirect || c.Kind == mir.CallInterface {
			// An interface call's Fn.Args are the method's own generic
			// arguments (lower's CallIface).
			c.Fn.Args = rc.genericArgs(c.Fn.Args)
		}
		if !rc.inPlace {
			as := append([]mir.Arg(nil), b.Args[t.Args.Start:t.Args.End()]...)
			for i := range as {
				as[i].Operand = rc.operand(as[i].Operand)
			}
			t.Args = b.AddArgs(as...)
		} else if !rc.doneArgs[t.Args] {
			rc.doneArgs[t.Args] = true
			for i := t.Args.Start; i < t.Args.End(); i++ {
				b.Args[i].Operand = rc.operand(b.Args[i].Operand)
			}
		}
		t.Dest = rc.place(t.Dest)
		t.Target = rc.block(t.Target)
		if t.HasErr {
			t.Err.Block = rc.block(t.Err.Block)
			t.Err.Error = rc.local(t.Err.Error)
		}
		t.Spawn = rc.local(t.Spawn)
	case mir.TermTypeTest, mir.TermOnerrGuard:
		t.Yes, t.No = rc.block(t.Yes), rc.block(t.No)
		t.Value = rc.local(t.Value)
	case mir.TermTailCall:
		// As a call's callee and arguments; no Dest (_0), Target, error
		// edge or spawn to rename.
		c := &t.Callee
		c.Value = rc.operand(c.Value)
		c.SelfType = rc.s.typ(c.SelfType)
		if c.Kind == mir.CallDirect {
			c.Fn.Args = rc.genericArgs(c.Fn.Args)
		}
		if !rc.inPlace {
			as := append([]mir.Arg(nil), b.Args[t.Args.Start:t.Args.End()]...)
			for i := range as {
				as[i].Operand = rc.operand(as[i].Operand)
			}
			t.Args = b.AddArgs(as...)
		} else if !rc.doneArgs[t.Args] {
			rc.doneArgs[t.Args] = true
			for i := t.Args.Start; i < t.Args.End(); i++ {
				b.Args[i].Operand = rc.operand(b.Args[i].Operand)
			}
		}
	}
}

// strConst interns a str const value: a pointer to an [N]u8 const of the
// bytes, and the length, in canonical form.
func (bl *builder) strConst(s string) types.ConstID {
	ct := bl.env.Consts()
	n := types.ConstArg{Kind: types.CAValue, Value: intConst(ct, int64(len(s)))}
	data := ct.Intern(types.ConstValue{Type: bl.tab.Array(n, types.U8), Align: 1, Bytes: []byte(s)})
	var by [16]byte
	binary.LittleEndian.PutUint64(by[8:], uint64(len(s)))
	return ct.Intern(types.ConstValue{Type: types.Str, Align: 8, Bytes: by[:],
		Relocs: []types.Reloc{{Offset: 0, Kind: types.RelocData, Data: data}}})
}
