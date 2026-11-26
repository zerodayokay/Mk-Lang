// Why: eval's tests need sessions, items and MIR bodies without the front
// end, since eval may not import the packages that build them.
// What: the test world: session tables, a fake Program serving hand-built MIR
// and layouts, and the bb MIR builder with call, branch and run helpers.
// How: layouts follow the compiler's layout rules, in the shape of
// internal/abi's test layouts; every body passes mir.(*Body).Validate before
// it is served.

package eval

import (
	"encoding/binary"
	"flag"
	"fmt"
	"sync"
	"testing"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

type world struct {
	t      testing.TB
	in     *source.Interner
	items  *types.Items
	consts *types.ConstTable
	tab    *types.Table
	mods   map[string]types.ModuleID

	mu         sync.Mutex
	bodies     map[types.ItemID]*Body
	inst       map[string]*Body
	bodyCalls  []types.ItemID
	lowerVals  map[types.ItemID]Const
	layouts    map[types.TypeID]*types.Layout
	forbidBody map[types.ItemID]bool
	spanN      uint32
	known      types.KnownItems
	target     types.ItemID
	// pageAlloc, pageFree, evalArena are Config's override and arena items.
	pageAlloc, pageFree, evalArena types.ItemID
}

func newWorld(t testing.TB) *world {
	in := source.NewInterner()
	items := types.NewItems()
	consts := types.NewConstTable()
	w := &world{t: t, in: in, items: items, consts: consts, tab: types.NewTable(in, items, consts),
		mods: map[string]types.ModuleID{}, bodies: map[types.ItemID]*Body{}, inst: map[string]*Body{},
		lowerVals: map[types.ItemID]Const{}, layouts: map[types.TypeID]*types.Layout{},
		forbidBody: map[types.ItemID]bool{}}
	w.module("main") // before any goroutine calls w.call
	return w
}

func (w *world) module(path string) types.ModuleID {
	if id, ok := w.mods[path]; ok {
		return id
	}
	id := w.items.AddModule(&types.Module{Path: path})
	w.mods[path] = id
	return id
}

func (w *world) item(mod, name string, kind types.ItemKind, d types.Decl) types.ItemID {
	m := w.module(mod)
	return w.items.AddItem(&types.Item{Module: m, Name: w.in.Intern(name), Kind: kind,
		Key: types.ItemKey(mod, name), Decl: d, Span: w.span()})
}

// fn declares a function with the given parameter types and result.
func (w *world) fn(mod, name string, result types.TypeID, params ...types.TypeID) types.ItemID {
	fd := &types.FuncDecl{}
	fd.Sig.Result = result
	for i, p := range params {
		fd.Sig.Params = append(fd.Sig.Params, types.Param{Name: w.in.Intern(fmt.Sprintf("p%d", i)), Type: p})
	}
	id := w.item(mod, name, types.KindFunc, fd)
	fd.Sig.Item = id
	return id
}

func (w *world) constItem(mod, name string, ty types.TypeID, fuel uint64) types.ItemID {
	return w.item(mod, name, types.KindConst, &types.ConstDecl{Type: ty, Fuel: fuel})
}

// structType declares a struct and returns its item and type.
func (w *world) structType(mod, name string, fields ...types.TypeID) (types.ItemID, types.TypeID) {
	d := &types.StructDecl{}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: w.in.Intern(fmt.Sprintf("f%d", i)), Type: f})
	}
	id := w.item(mod, name, types.KindStruct, d)
	return id, w.tab.Named(id, nil)
}

// enumType declares an enum with one variant per field list.
func (w *world) enumType(mod, name string, variants ...[]types.TypeID) (types.ItemID, types.TypeID) {
	d := &types.EnumDecl{}
	for i, fs := range variants {
		v := types.VariantDecl{Name: w.in.Intern(fmt.Sprintf("V%d", i)), Discr: uint64(i)}
		for j, f := range fs {
			v.Fields = append(v.Fields, types.FieldDecl{Name: w.in.Intern(fmt.Sprintf("f%d", j)), Type: f})
		}
		d.Variants = append(d.Variants, v)
	}
	id := w.item(mod, name, types.KindEnum, d)
	return id, w.tab.Named(id, nil)
}

// errorItem declares an error with payload fields.
func (w *world) errorItem(mod, name string, fields ...types.TypeID) types.ItemID {
	d := &types.ErrorDecl{}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: w.in.Intern(fmt.Sprintf("f%d", i)), Type: f})
	}
	id := w.item(mod, name, types.KindError, d)
	d.Code = types.ErrorCode(w.items.Get(id).Key)
	return id
}

func (w *world) intVal(t types.TypeID, x int64) types.ConstID {
	n := 8
	if tt := w.tab.Get(t); tt.Kind == types.KInt {
		n = int(tt.Bits / 8)
	}
	b := make([]byte, n)
	types.StoreUint(b, n, uint64(x))
	return w.consts.Intern(types.ConstValue{Type: t, Align: uint32(n), Bytes: b})
}

func (w *world) array(n int, elem types.TypeID) types.TypeID {
	return w.tab.Array(types.ConstArg{Kind: types.CAValue, Value: w.intVal(types.Int, int64(n))}, elem)
}

func (w *world) span() source.Span {
	w.spanN++
	return source.Span{File: 1, Start: w.spanN * 10, End: w.spanN*10 + 1}
}

// setBody validates and serves a body for item.
func (w *world) setBody(item types.ItemID, b *mir.Body) {
	w.t.Helper()
	if err := b.Validate(&mir.Env{Types: w.tab}); err != nil {
		w.t.Fatalf("invalid test MIR for %s: %v\n%s", w.items.Get(item).Key, err, (&mir.Printer{Types: w.tab, Items: w.items}).Print(b))
	}
	w.mu.Lock()
	w.bodies[item] = &Body{MIR: b, Hash: source.HashString("test.body", w.items.Get(item).Key.String()), State: BodyReady}
	w.mu.Unlock()
}

func (w *world) setState(item types.ItemID, s BodyState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bodies[item]
	if b == nil {
		b = &Body{}
		w.bodies[item] = b
	}
	b.State = s
}

func (w *world) evaluator(stack uint64) *Evaluator {
	return New(Config{Types: w.tab, Items: w.items, Consts: w.consts, Interner: w.in, Program: w,
		Known: w.known, Target: types.X86_64Windows, TargetItem: w.target, StackLimit: stack,
		PageAlloc: w.pageAlloc, PageFree: w.pageFree, EvalArena: w.evalArena})
}

// Program.

func (w *world) Body(fn types.ItemID, args []types.GenericArg) Body {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.forbidBody[fn] {
		w.t.Errorf("Program.Body(%s) must not be called", w.items.Get(fn).Key)
	}
	w.bodyCalls = append(w.bodyCalls, fn)
	if len(args) > 0 {
		if b := w.inst[instKey(fn, args)]; b != nil {
			return *b
		}
		return Body{State: BodyMissing}
	}
	if b := w.bodies[fn]; b != nil {
		return *b
	}
	return Body{State: BodyMissing}
}

func instKey(fn types.ItemID, args []types.GenericArg) string { return fmt.Sprint(fn, args) }

// setInstance serves an instance body of fn with args (as mono would).
func (w *world) setInstance(fn types.ItemID, args []types.GenericArg, b *mir.Body) {
	w.t.Helper()
	if err := b.Validate(&mir.Env{Types: w.tab}); err != nil {
		w.t.Fatalf("invalid instance MIR: %v", err)
	}
	w.mu.Lock()
	w.inst[instKey(fn, args)] = &Body{MIR: b, Hash: source.HashString("test.inst", instKey(fn, args)), State: BodyReady}
	w.mu.Unlock()
}

func (w *world) Layout(t types.TypeID) (*types.Layout, source.Hash) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := w.layoutLocked(t)
	return l, source.HashString("test.layout", w.tab.TypeString(t, nil))
}

func (w *world) LayoutName(args []types.GenericArg) string {
	if len(args) == 0 {
		return ""
	}
	return fmt.Sprint(args)
}

func (w *world) Const(item types.ItemID) Const {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.lowerVals[item]; ok {
		return c
	}
	if d, ok := w.items.Get(item).Decl.(*types.ConstDecl); ok && d.HasValue {
		return Const{Value: d.Value}
	}
	return Const{Poisoned: true}
}

func (w *world) ConstHash(c types.ConstID) source.Hash {
	v := w.consts.Get(c)
	return source.HashBytes("test.const", append(binary.LittleEndian.AppendUint32(nil, uint32(v.Type)), v.Bytes...))
}

func (w *world) TargetHash(f types.TargetField) source.Hash {
	return source.HashString("mk.target.v1", f.String())
}

// Layouts, by the compiler's layout rules.

func alignUpT(x uint64, a uint32) uint64 { return alignUp(x, uint64(a)) }

func (w *world) sa(t types.TypeID) (uint64, uint32) {
	l := w.layoutLocked(t)
	return l.Shape.Size, l.Shape.Align
}

func (w *world) structOf(ts []types.TypeID, pack uint32) *types.Layout {
	l := &types.Layout{}
	off, align := uint64(0), uint32(1)
	for _, ft := range ts {
		s, a := w.sa(ft)
		if pack != 0 {
			a = min(a, pack)
		}
		off = alignUpT(off, a)
		l.Fields = append(l.Fields, types.FieldLayout{Offset: off, Type: ft})
		off += s
		align = max(align, a)
	}
	l.Shape.Size, l.Shape.Align = alignUpT(off, align), align
	return l
}

// tagged is check's taggedUnion: the variants after the tag at the largest
// alignment, or with own (an E!T or an error set) each at its own.
func (w *world) tagged(tagSize uint8, tagAlign uint32, own bool, discrs []uint64, variants [][]types.TypeID) *types.Layout {
	var vs []*types.Layout
	usize, ualign := uint64(0), uint32(1)
	for _, fs := range variants {
		v := w.structOf(fs, 0)
		vs = append(vs, v)
		usize, ualign = max(usize, v.Shape.Size), max(ualign, v.Shape.Align)
	}
	p := alignUpT(uint64(tagSize), ualign)
	align := max(tagAlign, ualign)
	l := &types.Layout{HasTag: true, Tag: types.TagLayout{Size: tagSize}}
	l.Shape.Size, l.Shape.Align = alignUpT(p+usize, align), align
	if usize == 0 {
		l.Shape.Size = alignUpT(uint64(tagSize), align)
	}
	if own {
		end := uint64(tagSize)
		for _, v := range vs {
			end = max(end, alignUpT(uint64(tagSize), v.Shape.Align)+v.Shape.Size)
		}
		l.Shape.Size = alignUpT(end, align)
	}
	for i, v := range vs {
		vl := types.VariantLayout{Discr: discrs[i]}
		at := p
		if own {
			at = alignUpT(uint64(tagSize), v.Shape.Align)
		}
		for _, f := range v.Fields {
			vl.Fields = append(vl.Fields, types.FieldLayout{Offset: at + f.Offset, Type: f.Type})
		}
		l.Variants = append(l.Variants, vl)
	}
	return l
}

func (w *world) layoutLocked(t types.TypeID) *types.Layout {
	if l := w.layouts[t]; l != nil {
		return l
	}
	l := w.compute(t)
	w.layouts[t] = l
	return l
}

func scalar(n uint64) *types.Layout {
	return &types.Layout{Shape: types.Shape{Size: n, Align: uint32(max(n, 1))}}
}

func (w *world) compute(t types.TypeID) *types.Layout {
	ty := w.tab.Get(t)
	switch ty.Kind {
	case types.KVoid, types.KNever:
		return &types.Layout{Shape: types.Shape{Size: 0, Align: 1}}
	case types.KInt, types.KFloat:
		return scalar(uint64(ty.Bits / 8))
	case types.KIntPtr, types.KRef, types.KMut, types.KRawPtr, types.KFn:
		return scalar(8)
	case types.KBool:
		return scalar(1)
	case types.KChar:
		return scalar(4)
	case types.KStr, types.KSlice, types.KMutSlice:
		l := &types.Layout{Shape: types.Shape{Size: 16, Align: 8}}
		return l
	case types.KArray:
		s, a := w.sa(ty.Elem)
		v := w.consts.Get(ty.Len.Value)
		n := types.LoadUint(v.Bytes, len(v.Bytes))
		return &types.Layout{Shape: types.Shape{Size: s * n, Align: a}}
	case types.KRecord:
		var ts []types.TypeID
		for _, f := range ty.Fields {
			ts = append(ts, f.Type)
		}
		return w.structOf(ts, 0)
	case types.KOptional:
		if types.NicheOptional(w.tab.Kind(ty.Elem)) {
			return &types.Layout{Shape: types.Shape{Size: 8, Align: 8}, HasTag: true,
				Tag:      types.TagLayout{Size: 8, Niche: true},
				Variants: []types.VariantLayout{{Discr: 0}, {Discr: 1, Fields: []types.FieldLayout{{Offset: 0, Type: ty.Elem}}}}}
		}
		return w.tagged(1, 1, false, []uint64{0, 1}, [][]types.TypeID{nil, {ty.Elem}})
	case types.KErrorUnion, types.KErrorSet:
		var discrs []uint64
		var vs [][]types.TypeID
		if ty.Kind == types.KErrorUnion {
			discrs = append(discrs, 0)
			if ty.Elem == types.Void {
				vs = append(vs, nil)
			} else {
				vs = append(vs, []types.TypeID{ty.Elem})
			}
		}
		for _, m := range w.tab.Members(ty.Errors) {
			d := w.items.Get(m.Item).Decl.(*types.ErrorDecl)
			discrs = append(discrs, uint64(d.Code))
			var fs []types.TypeID
			for _, f := range d.Fields {
				fs = append(fs, f.Type)
			}
			vs = append(vs, fs)
		}
		return w.tagged(4, 4, true, discrs, vs)
	case types.KNamed:
		switch d := w.items.Get(ty.Item).Decl.(type) {
		case *types.StructDecl:
			var ts []types.TypeID
			for _, f := range d.Fields {
				ft := f.Type
				// A generic struct's field that is a parameter takes the
				// argument (mono's substitution, enough for these tests).
				if p := w.tab.Get(ft); p.Kind == types.KParam && int(p.Index) < len(ty.Args) {
					ft = ty.Args[p.Index].Type
				}
				ts = append(ts, ft)
			}
			return w.structOf(ts, d.Packed)
		case *types.EnumDecl:
			var discrs []uint64
			var vs [][]types.TypeID
			for _, v := range d.Variants {
				discrs = append(discrs, v.Discr)
				var fs []types.TypeID
				for _, f := range v.Fields {
					fs = append(fs, f.Type)
				}
				vs = append(vs, fs)
			}
			ts := types.EnumTagBytes(discrs)
			return w.tagged(ts, uint32(ts), false, discrs, vs)
		}
	}
	w.t.Fatalf("test layout: no layout for %s", ty.Kind)
	return nil
}

// bb builds one body.
type bb struct {
	w *world
	b *mir.Body
}

func (w *world) body(item types.ItemID, ret types.TypeID, params ...types.TypeID) *bb {
	x := &bb{w: w, b: mir.NewBody(item, w.span(), ret)}
	for i, p := range params {
		x.b.AddParam(p, w.in.Intern(fmt.Sprintf("p%d", i)), x.sp())
	}
	x.b.AddBlock(mir.OriginBody, mir.NoDefer)
	return x
}

func (x *bb) sp() mir.SpanID { return x.b.AddSpan(x.w.span()) }

func (x *bb) local(t types.TypeID) mir.LocalID {
	return x.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop, Span: x.sp()})
}

func (x *bb) block() mir.BlockID { return x.b.AddBlock(mir.OriginBody, mir.NoDefer) }

func (x *bb) assign(blk mir.BlockID, p mir.Place, rv mir.Rvalue) mir.SpanID {
	s := x.sp()
	x.b.Blocks[blk].Stmts = append(x.b.Blocks[blk].Stmts, mir.Statement{Kind: mir.StAssign, Place: p, Rvalue: rv, Span: s})
	return s
}

func (x *bb) term(blk mir.BlockID, t mir.Terminator) mir.SpanID {
	t.Span = x.sp()
	x.b.Blocks[blk].Term = t
	return t.Span
}

func (x *bb) ret(blk mir.BlockID) mir.SpanID {
	return x.term(blk, mir.Terminator{Kind: mir.TermReturn})
}

func (x *bb) gotoB(blk, to mir.BlockID) {
	x.term(blk, mir.Terminator{Kind: mir.TermGoto, Target: to})
}

// call terminates blk with a direct call of fn into dest, then goto next.
func (x *bb) call(blk mir.BlockID, fn types.ItemID, dest mir.Place, next mir.BlockID, args ...mir.Operand) mir.SpanID {
	var as []mir.Arg
	for _, a := range args {
		as = append(as, mir.Arg{Operand: a})
	}
	return x.term(blk, mir.Terminator{Kind: mir.TermCall, Callee: mir.Callee{Kind: mir.CallDirect, Fn: mir.FnRef{Item: fn}},
		Args: x.b.AddArgs(as...), Dest: dest, Target: next, Spawn: mir.NoLocal})
}

// branch terminates blk with a switch on a bool operand.
func (x *bb) branch(blk mir.BlockID, on mir.Operand, yes, no mir.BlockID) {
	x.term(blk, mir.Terminator{Kind: mir.TermSwitch, On: on, Cases: x.b.AddCases(mir.SwitchCase{Value: 1, Target: yes}), Default: no})
}

func (x *bb) done() *mir.Body {
	x.w.setBody(x.b.Item, x.b)
	return x.b
}

func use(o mir.Operand) mir.Rvalue { return mir.Rvalue{Kind: mir.RvUse, A: o} }

func bin(op mir.BinOp, a, b mir.Operand) mir.Rvalue {
	return mir.Rvalue{Kind: mir.RvBinary, Bin: op, A: a, B: b}
}

func imm(t types.TypeID, x int64) mir.Operand { return mir.Imm(t, uint64(x)) }

func lp(l mir.LocalID) mir.Place { return mir.LocalPlace(l) }

func cp(l mir.LocalID) mir.Operand { return mir.Copy(lp(l)) }

// run evaluates an EvalCall of fn in module mod at StageBodies.
func (w *world) call(e *Evaluator, fn types.ItemID, fuel uint64, args ...types.ConstID) types.EvalResult {
	return e.Eval(&types.EvalRequest{What: types.EvalCall, Item: fn, Args: args, Fuel: fuel, Stage: types.StageBodies,
		Module: w.module("main"), Site: source.Span{File: 1, Start: 1, End: 2}, Target: types.X86_64Windows})
}

// intOf reads a scalar const's value, sign-extended for signed types.
func (w *world) intOf(c types.ConstID) int64 {
	v := w.consts.Get(c)
	x := types.LoadUint(v.Bytes, len(v.Bytes))
	if t := w.tab.Get(v.Type); t.Signed && len(v.Bytes) < 8 {
		s := 64 - 8*uint(len(v.Bytes))
		return int64(x<<s) >> s
	}
	return int64(x)
}
