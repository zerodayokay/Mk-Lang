// Why: codegen needs a closed body per instance: every type, const
// argument and generic-argument range substituted, and every call it can
// make direct made direct.
//
// What: Instantiate and the builder that makes an instance body: type and
// const substitution, sizeOf, ifaceCall (interface calls made direct),
// constParamCall (calls through a const value parameter made direct), and
// the ordered list of per-instance rewrites in the other files.
//
// How: the body is rebuilt only when something needs rewriting; otherwise
// the item's own body is returned. The rewrites run in a fixed order
// (builder.build), and with Options.Validate set the result goes through
// mir.Validate.

package mono

import (
	"encoding/binary"
	"slices"

	"mk/internal/mir"
	"mk/internal/types"
)

// Instantiate builds the instance body of item at args: args are the item's
// combined generic arguments, then one CAValue per const value parameter in
// parameter order. It returns nil for a bodiless item, and the item's own body
// when nothing needs rewriting: a non-generic body with no const value
// parameter, no onerr guard or type test, no release-dropped call, no call of
// a Slice[T] namespace function (which slice.go adapts), no SizeOf[T]()
// spelled anywhere, every const value parameter's argument already a constant,
// and, inside Collect, no reference to a literal other than the kept one
// (canon.go). Otherwise the result is a new body and never aliases the generic
// one's slices. args must be closed; anything else is an ICE.
//
// The driver's Program.Body for the evaluator may call it.
//
// Instantiate does not canonicalize fn literal references: that needs
// the literals kept by one collection, so only Collect's bodies do it.
func Instantiate(env Env, item types.ItemID, args []types.GenericArg, opt Options) *mir.Body {
	b, _ := instantiate(env, item, args, opt, nil)
	return b
}

// instantiate is Instantiate with the collection's kept literals (nil: no
// literal canonicalization), and the forwarded formats that do not match
// (format.go), which Collect reports.
func instantiate(env Env, item types.ItemID, args []types.GenericArg, opt Options, lits *literals) (*mir.Body, []fmtProblem) {
	gb := env.Body(item)
	if gb == nil {
		return nil, nil
	}
	b := newBuilder(env, item, gb, args, opt)
	b.lits = lits
	return b.build(), b.fmtProbs
}

// builder builds one instance body.
type builder struct {
	env  Env
	tab  *types.Table
	opt  Options
	item types.ItemID
	gb   *mir.Body
	args []types.GenericArg
	sig  *types.Signature // the item's signature, nil for a test or const
	s    substituter
	// constParam maps a const value parameter's local to its argument.
	constParam map[mir.LocalID]types.ConstID
	// hidden counts the body's hidden fields-loop generics.
	hidden int
	b      *mir.Body
	// lits are the collection's kept fn literals (canon.go); nil outside
	// Collect.
	lits *literals
	// fmtProbs are the forwarded formats that do not match (format.go).
	fmtProbs []fmtProblem
	// tasksFolded: a UsesTasks call was folded into a branch (tasks.go).
	tasksFolded bool
	// curBlock is the block whose terminator term is handling; errSets
	// the blocks whose call is Format on an error set, rewritten into a
	// switch once the walk is done (errfmt.go).
	curBlock mir.BlockID
	errSets  []mir.BlockID
	// fi are the Release and HasFree items (release.go); releases: the body calls
	// std.raw.Release, resolved after the prune; freeFolded: a HasFree
	// call was folded into a branch.
	fi         freeItems
	releases   bool
	freeFolded bool
}

func newBuilder(env Env, item types.ItemID, gb *mir.Body, args []types.GenericArg, opt Options) *builder {
	bl := &builder{env: env, tab: env.Table(), opt: opt, item: item, gb: gb, args: args}
	bl.hidden = hiddenGenerics(gb)
	visible := int(gb.GenericCount) - bl.hidden
	if visible > len(args) {
		ice("instance of %s: %d generic arguments for %d generics", env.Items().Get(item).Key, len(args), visible)
	}
	gen := args[:visible:visible]
	// A hidden generic (a fields loop's current field, after the visible
	// ones) stays a parameter until the loop unrolls.
	for h := visible; h < int(gb.GenericCount); h++ {
		gen = append(gen, types.TypeArg(env.Table().Param(uint32(h))))
	}
	for _, a := range args {
		if a.IsConst && !closedConst(bl.tab, a.Const) || !a.IsConst && !closed(bl.tab, a.Type) {
			ice("instance of %s: open argument %s", env.Items().Get(item).Key, bl.tab.GenericArgString(a, nil))
		}
	}
	bl.s = substituter{tab: bl.tab, args: gen, fold: bl.sizeOf, sliceSelf: true}
	it := env.Items().Get(item)
	if fd, ok := it.Decl.(*types.FuncDecl); ok {
		bl.sig = &fd.Sig
	}
	bl.s.self = bl.selfType(it, gen)
	// Const value parameters: args past the generics, in order.
	rest := args[visible:]
	if len(rest) > 0 {
		if bl.sig == nil {
			ice("instance of %s: const arguments for an item with no signature", it.Key)
		}
		bl.constParam = map[mir.LocalID]types.ConstID{}
		n := 0
		for i, p := range bl.sig.Params {
			if !p.Const {
				continue
			}
			if n >= len(rest) || !rest[n].IsConst || rest[n].Const.Kind != types.CAValue {
				ice("instance of %s: const parameter %d has no value argument", it.Key, i+1)
			}
			bl.constParam[mir.LocalID(i+1)] = rest[n].Const.Value
			n++
		}
		if n != len(rest) {
			ice("instance of %s: %d const arguments for %d const parameters", it.Key, len(rest), n)
		}
	}
	return bl
}

// hiddenGenerics counts the distinct hidden generics of a body's generic
// fields loops; they are the last of Body.GenericCount (lower's input
// GenericCount).
func hiddenGenerics(b *mir.Body) int {
	if b.GenericCount == 0 || len(b.Loops) == 0 {
		return 0
	}
	var seen map[uint32]bool
	for _, lp := range b.Loops {
		if lp.FieldsOf != types.NoType {
			if seen == nil {
				seen = map[uint32]bool{}
			}
			seen[lp.FieldsParam] = true
		}
	}
	return len(seen)
}

// VisibleGenerics is how many of a body's GenericCount are the item's own
// combined generics, which callers pass: the rest are hidden fields-loop
// generics that mono unrolls.
func VisibleGenerics(b *mir.Body) int { return int(b.GenericCount) - hiddenGenerics(b) }

// selfType is KSelf's substitution: the impl's For type for an impl method,
// NoType elsewhere.
func (bl *builder) selfType(it *types.Item, gen []types.GenericArg) types.TypeID {
	if it.Owner == types.NoItem {
		return types.NoType
	}
	own := bl.env.Items().Get(it.Owner)
	if id, ok := own.Decl.(*types.ImplDecl); ok {
		return Subst(bl.tab, id.For, gen)
	}
	return types.NoType
}

// sizeOf folds SizeOf[T]() of a closed T into an int const.
func (bl *builder) sizeOf(t types.TypeID) types.ConstArg {
	l := bl.env.Layout(t)
	if l == nil {
		ice("SizeOf[%s]: no layout", bl.tab.TypeString(t, nil))
	}
	return types.ConstArg{Kind: types.CAValue, Value: intConst(bl.env.Consts(), int64(l.Shape.Size))}
}

// intConst interns an int const value.
func intConst(ct *types.ConstTable, n int64) types.ConstID {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(n))
	return ct.Intern(types.ConstValue{Type: types.Int, Align: 8, Bytes: b[:]})
}

func (bl *builder) build() *mir.Body {
	gb := bl.gb
	if gb.GenericCount == 0 && bl.constParam == nil && !bl.needsRewrite() && !bl.callsSliceFn() &&
		!bl.mentionsSizeOf(gb) && !bl.constArgs(gb, false) && !bl.canonLiterals(gb, false) {
		return gb
	}
	bl.s.memo = map[types.TypeID]types.TypeID{}
	b := cloneBody(gb)
	bl.b = b
	b.GenericCount = 0
	// One hidden parameter per const parameter bound to a capturing literal,
	// before any other rewrite; the plan of which arguments forward such a
	// parameter is read from gb, whose operands are not substituted yet.
	params, ts := bl.hiddenParams()
	pl := planEnvs(gb, params)
	if pl != nil {
		for i, l := range insertParams(b, ts) {
			pl.envOf[params[i]] = l
		}
	}
	for i := range b.Locals {
		b.Locals[i].Type = bl.s.typ(b.Locals[i].Type)
	}
	for i := range b.Projections {
		b.Projections[i].Type = bl.s.typ(b.Projections[i].Type)
	}
	for i := range b.GenericArgs {
		b.GenericArgs[i] = bl.s.genericArg(b.GenericArgs[i])
	}
	for i := range b.Operands {
		bl.operand(&b.Operands[i])
	}
	for i := range b.Args {
		bl.operand(&b.Args[i].Operand)
	}
	for i := range b.Loops {
		b.Loops[i].FieldsOf = bl.s.typ(b.Loops[i].FieldsOf)
	}
	for i := range b.Blocks {
		blk := &b.Blocks[i]
		for j := range blk.Stmts {
			bl.rvalue(&blk.Stmts[j].Rvalue)
		}
		bl.curBlock = mir.BlockID(i)
		bl.term(&blk.Term)
	}
	// Asm operands, and a const parameter as a const operand.
	bl.asmOperands(b)
	// A const value parameter's argument is a constant operand.
	bl.constArgs(b, true)
	// Environments threaded to the calls that need them.
	bl.threadEnvs(b, pl)
	folded := bl.resolve()
	if bl.hidden > 0 {
		// A generic fields loop's hidden generic is unrolled per field.
		bl.unrollFields()
		folded = true
	}
	// Slice[T] selves and receivers become slices (slice.go).
	if bl.adaptSlices() {
		folded = true
	}
	// A forwarded format is a constant now; judge and expand it.
	// So is a literal format whose record argument had a generic field,
	// which lower left plain in a generic body.
	if bl.constParam != nil || gb.GenericCount > 0 {
		bl.fmtProbs = bl.expandFormats()
	}
	if bl.errSetFormats() { // Format on an error set (errfmt.go)
		folded = true
	}
	if folded {
		prune(b)
	}
	for (bl.tasksFolded || bl.freeFolded) && foldConstBools(b) {
		prune(b) // `rt.UsesTasks() && x` joins (tasks.go), and HasFree
	}
	if bl.releases {
		// Release (release.go): on live blocks only, so a Release that a
		// folded HasFree guards is gone before it is judged.
		prune(b)
		bl.resolveReleases()
	}
	// Every literal reference names the literal kept for its content.
	bl.canonLiterals(b, true)
	if bl.opt.Validate {
		bl.validate()
	}
	return b
}

func cloneBody(gb *mir.Body) *mir.Body {
	b := *gb
	b.Locals = append([]mir.Local(nil), gb.Locals...)
	b.Blocks = make([]mir.Block, len(gb.Blocks))
	for i := range gb.Blocks {
		b.Blocks[i] = gb.Blocks[i]
		b.Blocks[i].Stmts = append([]mir.Statement(nil), gb.Blocks[i].Stmts...)
	}
	b.Projections = append([]mir.Projection(nil), gb.Projections...)
	b.GenericArgs = append([]types.GenericArg(nil), gb.GenericArgs...)
	b.Operands = append([]mir.Operand(nil), gb.Operands...)
	b.Args = append([]mir.Arg(nil), gb.Args...)
	b.Cases = append([]mir.SwitchCase(nil), gb.Cases...)
	b.Loops = append([]mir.LoopInfo(nil), gb.Loops...)
	b.Defers = append([]mir.DeferInfo(nil), gb.Defers...)
	b.Spans = slices.Clone(gb.Spans)
	return &b
}

func (bl *builder) operand(o *mir.Operand) {
	if (o.Kind == mir.OpCopy || o.Kind == mir.OpMove) && o.Place.Proj.Len == 0 {
		// A const value parameter is its argument in the instance:
		// forwarding it to another const parameter keys that callee too.
		if c, ok := bl.constParam[o.Place.Local]; ok {
			*o = mir.Const(bl.b.Locals[o.Place.Local].Type, c)
			return
		}
	}
	if (o.Kind == mir.OpCopy || o.Kind == mir.OpMove) && o.Place.Proj.Len != 0 && bl.constParam != nil {
		// A field of a const value parameter is a constant too, so it may
		// be passed on to a const parameter (std.atomic's `o.v`).
		if c, ok := bl.constParam[o.Place.Local]; ok {
			if v, ok := bl.constField(bl.s.typ(bl.b.Locals[o.Place.Local].Type), c, o.Place.Proj); ok {
				*o = v
				return
			}
		}
	}
	switch o.Kind {
	case mir.OpConst:
		o.Type = bl.s.typ(o.Type)
		o.Const = bl.constValue(o.Const)
	case mir.OpImm:
		o.Type = bl.s.typ(o.Type)
	}
}

// constField is the constant operand at the field path ps of the const c of
// type t: struct and record fields only, and only bytes no relocation
// touches (a fn value's bytes are its relocation); else false and the
// operand stays a read of the place.
func (bl *builder) constField(t types.TypeID, c types.ConstID, ps mir.Range) (mir.Operand, bool) {
	v := bl.env.Consts().Get(c)
	var off uint64
	for i := ps.Start; i < ps.Start+ps.Len; i++ {
		p := bl.b.Projections[i]
		if p.Kind != mir.ProjField || p.FieldLoop {
			return mir.Operand{}, false
		}
		switch bl.tab.Kind(t) {
		case types.KNamed, types.KRecord:
		default:
			return mir.Operand{}, false
		}
		l := bl.env.Layout(t)
		if l == nil || l.HasTag || int(p.A) >= len(l.Fields) {
			return mir.Operand{}, false
		}
		off += l.Fields[p.A].Offset
		t = bl.s.typ(p.Type)
	}
	fl := bl.env.Layout(t)
	if fl == nil || off+fl.Shape.Size > uint64(len(v.Bytes)) {
		return mir.Operand{}, false
	}
	for _, r := range v.Relocs {
		if uint64(r.Offset) < off+fl.Shape.Size && uint64(r.Offset)+8 > off {
			return mir.Operand{}, false
		}
	}
	fv := types.ConstValue{Type: t, Align: fl.Shape.Align, Bytes: append([]byte(nil), v.Bytes[off:off+fl.Shape.Size]...)}
	return bl.constOperand(t, bl.env.Consts().Intern(fv)), true
}

// constValue substitutes an interned const whose type or fn relocations
// mention the generics. Only a fn value can be open (lower's const fn
// argument Keep[T] in a generic body): its bytes do not depend
// on the arguments. Any other open const is an ICE, because its bytes were
// laid out before the arguments were known.
func (bl *builder) constValue(c types.ConstID) types.ConstID {
	if c == types.NoConst {
		return c
	}
	ct := bl.env.Consts()
	v := ct.Get(c)
	open := !closed(bl.tab, v.Type)
	for _, r := range v.Relocs {
		if r.Kind == types.RelocFn && !closedArgs(bl.tab, r.Fn.Args) {
			open = true
		}
	}
	if !open {
		return c
	}
	if bl.tab.Kind(v.Type) != types.KFn {
		ice("open const of type %s in a generic body", bl.tab.TypeString(v.Type, nil))
	}
	nv := types.ConstValue{Type: bl.s.typ(v.Type), Align: v.Align, Bytes: append([]byte(nil), v.Bytes...)}
	for _, r := range v.Relocs {
		r.Fn.Args = bl.s.genericArgs(r.Fn.Args)
		nv.Relocs = append(nv.Relocs, r)
	}
	return ct.Intern(nv)
}

func (bl *builder) rvalue(rv *mir.Rvalue) {
	rv.Type = bl.s.typ(rv.Type)
	bl.operand(&rv.A)
	bl.operand(&rv.B)
	if rv.Len.Kind != types.CANone {
		rv.Len = bl.s.constArg(rv.Len)
	}
	if rv.Kind == mir.RvConstArg {
		// A const parameter used as a value becomes an immediate.
		if rv.Len.Kind != types.CAValue {
			ice("RvConstArg of a %v const argument after substitution", rv.Len.Kind)
		}
		*rv = mir.Rvalue{Kind: mir.RvUse, A: bl.constOperand(rv.Type, rv.Len.Value)}
	}
}

// constOperand is an operand for const value c of type t: an immediate for
// an integer, bool or char of at most 8 bytes, else the const itself.
func (bl *builder) constOperand(t types.TypeID, c types.ConstID) mir.Operand {
	v := bl.env.Consts().Get(c)
	switch bl.tab.Kind(t) {
	case types.KInt, types.KIntPtr, types.KBool, types.KChar:
		if len(v.Bytes) <= 8 && len(v.Relocs) == 0 {
			return mir.Imm(t, types.LoadUint(v.Bytes, len(v.Bytes)))
		}
	}
	return mir.Const(t, c)
}

func (bl *builder) term(t *mir.Terminator) {
	t.Type = bl.s.typ(t.Type)
	bl.operand(&t.On)
	bl.operand(&t.Msg)
	bl.operand(&t.Callee.Value)
	t.Callee.SelfType = bl.s.typ(t.Callee.SelfType)
	if t.Kind == mir.TermTailCall {
		// A tail call's callee is direct or a fn value (check's E0314
		// refuses a method through a bound); a constant fn value becomes
		// direct as a call's does.
		switch t.Callee.Kind {
		case mir.CallInterface:
			noTail(t, "a method through a bound")
		case mir.CallValue:
			bl.constParamCall(t)
		}
		return
	}
	if t.Kind != mir.TermCall {
		return
	}
	switch t.Callee.Kind {
	case mir.CallInterface:
		// A hidden generic's waits for its unrolled copy; Items() on a
		// Slice[T] self is adaptSlices'.
		if closed(bl.tab, t.Callee.SelfType) && !ifaceNamed(bl.tab, t.Callee.SelfType) {
			bl.ifaceCall(t)
		}
	case mir.CallValue:
		bl.constParamCall(t)
	}
}

// ifaceCall makes an interface call direct: check proved
// the bound, so a missing impl is an ICE.
func (bl *builder) ifaceCall(t *mir.Terminator) {
	if bl.isErrSetFormat(t) {
		bl.errSets = append(bl.errSets, bl.curBlock) // rewritten after the walk (errfmt.go)
		return
	}
	c := &t.Callee
	fn, fargs, ok := bl.env.ImplMethod(c.Iface, c.Method, c.SelfType)
	if !ok {
		ice("no impl of %s method %d for %s", bl.env.Items().Get(c.Iface).Key, c.Method, bl.tab.TypeString(c.SelfType, nil))
	}
	// The impl method's generics are the impl's, then its own; the
	// own arguments are the call's, already substituted (lower keeps them in
	// Fn.Args of the interface call).
	if own := c.Fn.Args; own.Len > 0 {
		fargs = append(slices.Clip(fargs), bl.b.GenericArgs[own.Start:own.End()]...)
	}
	t.Callee = mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: fn, Args: bl.b.AddGenericArgs(fargs...)}}
}

// constParamCall makes a call through a constant fn value direct: a const
// value parameter's operand is already its argument (see operand), a const
// whose one relocation, at offset 0, names the function.
func (bl *builder) constParamCall(t *mir.Terminator) {
	v := t.Callee.Value
	if v.Kind != mir.OpConst {
		return
	}
	cv := bl.env.Consts().Get(v.Const)
	if bl.tab.Kind(cv.Type) != types.KFn || len(cv.Relocs) != 1 || cv.Relocs[0].Kind != types.RelocFn || cv.Relocs[0].Offset != 0 {
		ice("call through a constant fn value that is not one function")
	}
	f := cv.Relocs[0].Fn
	t.Callee = mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: f.Item, Args: bl.b.AddGenericArgs(f.Args...)}}
}

// instSig is the item's signature with the instance's arguments substituted,
// for mir.Validate's check of local 0 and the parameters.
func (bl *builder) instSig() *types.Signature {
	if bl.sig == nil {
		return nil
	}
	sig := *bl.sig
	sig.Generics, sig.OwnGenerics = nil, 0
	sig.Params = append([]types.Param(nil), bl.sig.Params...)
	for i := range sig.Params {
		sig.Params[i].Type = bl.s.typ(sig.Params[i].Type)
	}
	sig.Result = bl.s.typ(sig.Result)
	sig.Errors = bl.s.errors(sig.Errors)
	return &sig
}

func (bl *builder) validate() {
	env := &mir.Env{Types: bl.tab, Sig: bl.instSig(), Known: bl.opt.Known,
		Callee: func(id types.ItemID) *types.Signature {
			if fd, ok := bl.env.Items().Get(id).Decl.(*types.FuncDecl); ok {
				return &fd.Sig
			}
			return nil
		}}
	if err := bl.b.Validate(env); err != nil {
		ice("instance %s: %v", nameOf(bl.env, bl.item, bl.args), err)
	}
}
