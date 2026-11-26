// Why: codegen's tests need types, layouts and MIR without importing check or
// lower.
// What: testEnv, a small Env standing in for check's shape pass, and the fnb
// helpers that build MIR bodies by hand, plus unitOf, emit and fnText.
// How: testEnv computes layouts by the compiler's rules (field order, tags,
// align(n)); bodies are built block by block and emitted per target.

package codegen

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"mk/internal/mir"
	"mk/internal/source"
	"mk/internal/types"
)

type testEnv struct {
	in      *source.Interner
	tt      *types.Table
	items   *types.Items
	consts  *types.ConstTable
	mod     types.ModuleID
	mu      sync.Mutex
	layouts map[types.TypeID]*types.Layout
	// noFreeze lists the types that are not Freeze (Cell-like test types).
	noFreeze map[types.TypeID]bool
	mods     map[string]types.ModuleID
}

func newTestEnv() *testEnv {
	in := source.NewInterner()
	items := types.NewItems()
	consts := types.NewConstTable()
	e := &testEnv{in: in, items: items, consts: consts, tt: types.NewTable(in, items, consts),
		layouts: map[types.TypeID]*types.Layout{}, noFreeze: map[types.TypeID]bool{}, mods: map[string]types.ModuleID{}}
	e.mod = items.AddModule(&types.Module{Path: "main"})
	e.mods["main"] = e.mod
	return e
}

func (e *testEnv) Table() *types.Table       { return e.tt }
func (e *testEnv) Items() *types.Items       { return e.items }
func (e *testEnv) Consts() *types.ConstTable { return e.consts }
func (e *testEnv) Path(f source.FileID) string {
	return "main/t.mk"
}

// LineCol: line = Off/100 + 1, col = Off%100 + 1, so a test picks a position
// by its offset.
func (e *testEnv) LineCol(p source.Pos) source.LineCol {
	return source.LineCol{Line: p.Off/100 + 1, Col: p.Off%100 + 1}
}

func (e *testEnv) Facts(t types.TypeID) types.Facts {
	return types.Facts{Freeze: !e.noFreeze[t], Interior: e.noFreeze[t]}
}

func alignUp(n uint64, a uint32) uint64 {
	if a <= 1 {
		return n
	}
	return (n + uint64(a) - 1) / uint64(a) * uint64(a)
}

// sa is the size and alignment of a concrete type.
func (e *testEnv) sa(t types.TypeID) (uint64, uint32) {
	ty := e.tt.Get(t)
	switch ty.Kind {
	case types.KVoid, types.KNever:
		return 0, 1
	case types.KBool:
		return 1, 1
	case types.KChar:
		return 4, 4
	case types.KInt, types.KFloat:
		return uint64(ty.Bits) / 8, uint32(ty.Bits) / 8
	case types.KIntPtr, types.KRef, types.KMut, types.KRawPtr, types.KFn:
		return 8, 8
	case types.KStr, types.KSlice, types.KMutSlice:
		return 16, 8
	}
	l := e.Layout(t)
	return l.Shape.Size, l.Shape.Align
}

func (e *testEnv) Layout(t types.TypeID) *types.Layout {
	e.mu.Lock()
	l, ok := e.layouts[t]
	e.mu.Unlock()
	if ok {
		return l
	}
	l = e.compute(t)
	e.mu.Lock()
	e.layouts[t] = l
	e.mu.Unlock()
	return l
}

func (e *testEnv) structOf(ts []types.TypeID, pack uint32) *types.Layout {
	l := &types.Layout{}
	off, align := uint64(0), uint32(1)
	for _, ft := range ts {
		s, a := e.sa(ft)
		if pack != 0 {
			a = min(a, pack)
		}
		off = alignUp(off, a)
		l.Fields = append(l.Fields, types.FieldLayout{Offset: off, Type: ft})
		off += s
		align = max(align, a)
	}
	l.Shape = types.Shape{Size: alignUp(off, align), Align: align}
	return l
}

// tagged is check's taggedUnion: a tag of tagSize bytes at 0, then each
// variant a struct of its fields at absolute offsets, after the tag at the
// largest alignment, or with own (an E!T or an error set) at its own.
func (e *testEnv) tagged(tagSize uint8, own bool, discrs []uint64, variants [][]types.TypeID) *types.Layout {
	pa := uint32(1)
	for _, v := range variants {
		for _, ft := range v {
			_, a := e.sa(ft)
			pa = max(pa, a)
		}
	}
	start := alignUp(uint64(tagSize), pa)
	l := &types.Layout{HasTag: true, Tag: types.TagLayout{Offset: 0, Size: tagSize}}
	end := uint64(tagSize)
	for i, v := range variants {
		vl := types.VariantLayout{Discr: discrs[i]}
		off := start
		if own {
			va := uint32(1)
			for _, ft := range v {
				_, a := e.sa(ft)
				va = max(va, a)
			}
			off = alignUp(uint64(tagSize), va)
		}
		for _, ft := range v {
			s, a := e.sa(ft)
			off = alignUp(off, a)
			vl.Fields = append(vl.Fields, types.FieldLayout{Offset: off, Type: ft})
			off += s
		}
		end = max(end, off)
		l.Variants = append(l.Variants, vl)
	}
	align := max(pa, uint32(tagSize))
	l.Shape = types.Shape{Size: alignUp(end, align), Align: align}
	return l
}

// substArgs is a generic struct's field type t at args: a type parameter,
// and an array whose length is a const parameter (std.simd's Vec[T, N]
// holds [N]T); other types are taken as they are.
func (e *testEnv) substArgs(t types.TypeID, args []types.GenericArg) types.TypeID {
	if len(args) == 0 {
		return t
	}
	ty := e.tt.Get(t)
	switch ty.Kind {
	case types.KParam:
		return args[ty.Index].Type
	case types.KArray:
		l := ty.Len
		if l.Kind == types.CAParam {
			l = args[l.Param].Const
		}
		return e.tt.Array(l, e.substArgs(ty.Elem, args))
	}
	return t
}

func (e *testEnv) compute(t types.TypeID) *types.Layout {
	ty := e.tt.Get(t)
	switch ty.Kind {
	case types.KNamed:
		it := e.items.Get(ty.Item)
		switch d := it.Decl.(type) {
		case *types.StructDecl:
			if it.Key.String() == "std.core.Scope" && len(ty.Args) == 1 {
				// Scope[E] is {hdr: Scope, err: E!void}.
				es := e.tt.Get(ty.Args[0].Type).Errors
				return e.structOf([]types.TypeID{e.tt.Named(ty.Item, nil), e.tt.ErrorUnion(es, types.Void)}, 0)
			}
			var ts []types.TypeID
			for _, f := range d.Fields {
				ts = append(ts, e.substArgs(f.Type, ty.Args))
			}
			l := e.structOf(ts, d.Packed)
			if d.Align > l.Shape.Align {
				// align(n): the alignment, and the size rounded up to it.
				l.Shape.Align = d.Align
				l.Shape.Size = alignUp(l.Shape.Size, d.Align)
			}
			return l
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
			size := types.EnumTagBytes(discrs)
			if d.Tag != types.NoType {
				s, _ := e.sa(d.Tag)
				size = uint8(s)
			}
			return e.tagged(size, false, discrs, vs)
		}
	case types.KRecord:
		var ts []types.TypeID
		for _, f := range ty.Fields {
			ts = append(ts, f.Type)
		}
		return e.structOf(ts, 0)
	case types.KArray:
		s, a := e.sa(ty.Elem)
		cv := e.consts.Get(ty.Len.Value)
		n := types.LoadUint(cv.Bytes, len(cv.Bytes))
		return &types.Layout{Shape: types.Shape{Size: s * n, Align: a}}
	case types.KOptional:
		if types.NicheOptional(e.tt.Kind(ty.Elem)) {
			return &types.Layout{Shape: types.Shape{Size: 8, Align: 8}, HasTag: true,
				Tag:      types.TagLayout{Offset: 0, Size: 8, Niche: true},
				Variants: []types.VariantLayout{{Discr: 0}, {Discr: 1, Fields: []types.FieldLayout{{Offset: 0, Type: ty.Elem}}}}}
		}
		return e.tagged(1, false, []uint64{0, 1}, [][]types.TypeID{nil, {ty.Elem}})
	case types.KErrorUnion, types.KErrorSet:
		discrs := []uint64{}
		vs := [][]types.TypeID{}
		if ty.Kind == types.KErrorUnion {
			discrs = append(discrs, 0)
			if ty.Elem == types.Void {
				vs = append(vs, nil)
			} else {
				vs = append(vs, []types.TypeID{ty.Elem})
			}
		}
		for _, mem := range e.tt.Members(ty.Errors) {
			ed := e.items.Get(mem.Item).Decl.(*types.ErrorDecl)
			discrs = append(discrs, uint64(ed.Code))
			var fs []types.TypeID
			for _, f := range ed.Fields {
				fs = append(fs, f.Type)
			}
			vs = append(vs, fs)
		}
		return e.tagged(4, true, discrs, vs)
	}
	panic("testEnv: no layout for " + e.tt.TypeString(t, nil))
}

// item adds an item of module main.
func (e *testEnv) item(name string, kind types.ItemKind, d types.Decl) types.ItemID {
	return e.items.AddItem(&types.Item{Module: e.mod, Name: e.in.Intern(name), Kind: kind,
		Key: types.ItemKey("main", name), Decl: d})
}

// itemIn adds an item of module mod under key (a method's key is
// mod.Owner:Name).
func (e *testEnv) itemIn(mod string, key types.Key, name string, kind types.ItemKind, d types.Decl) types.ItemID {
	id, ok := e.mods[mod]
	if !ok {
		id = e.items.AddModule(&types.Module{Path: mod})
		e.mods[mod] = id
	}
	return e.items.AddItem(&types.Item{Module: id, Name: e.in.Intern(name), Kind: kind, Key: key, Decl: d})
}

// fnIn adds a bodiless function of module mod (owner "" for a free
// function), an intrinsic when intrinsic is set.
func (e *testEnv) fnIn(mod, owner, name string, params []types.TypeID, result types.TypeID, intrinsic bool) types.ItemID {
	sig := types.Signature{Name: e.in.Intern(name), Result: result}
	for i, p := range params {
		sig.Params = append(sig.Params, types.Param{Name: e.in.Intern("p" + string(rune('a'+i))), Type: p})
	}
	key := types.ItemKey(mod, name)
	if owner != "" {
		key = types.MemberKey(mod, owner, name)
	}
	id := e.itemIn(mod, key, name, types.KindFunc, &types.FuncDecl{Sig: sig, Bodiless: true, Intrinsic: intrinsic})
	e.items.Get(id).Decl.(*types.FuncDecl).Sig.Item = id
	return id
}

// structIn adds a struct of module mod and returns its type.
func (e *testEnv) structIn(mod, name string, fields ...types.TypeID) types.TypeID {
	d := &types.StructDecl{}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: e.in.Intern("f" + string(rune('a'+i))), Type: f})
	}
	return e.tt.Named(e.itemIn(mod, types.ItemKey(mod, name), name, types.KindStruct, d), nil)
}

// fn adds a function item with a signature over params and result.
func (e *testEnv) fn(name string, params []types.TypeID, result types.TypeID, errs types.ErrorSetID) types.ItemID {
	sig := types.Signature{Name: e.in.Intern(name), Result: result, Errors: errs}
	for i, p := range params {
		sig.Params = append(sig.Params, types.Param{Name: e.in.Intern("p" + string(rune('a'+i))), Type: p})
	}
	id := e.item(name, types.KindFunc, &types.FuncDecl{Sig: sig})
	e.items.Get(id).Decl.(*types.FuncDecl).Sig.Item = id
	return id
}

// errItem adds an error item with payload fields.
func (e *testEnv) errItem(name string, fields ...types.TypeID) types.ItemID {
	d := &types.ErrorDecl{Code: types.ErrorCode(types.ItemKey("main", name))}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: e.in.Intern("f" + string(rune('a'+i))), Type: f})
	}
	return e.item(name, types.KindError, d)
}

// structT adds a struct and returns its type.
func (e *testEnv) structT(name string, fields ...types.TypeID) types.TypeID {
	d := &types.StructDecl{}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: e.in.Intern("f" + string(rune('a'+i))), Type: f})
	}
	return e.tt.Named(e.item(name, types.KindStruct, d), nil)
}

// enumT adds an enum whose variants have the given payloads and discriminants.
func (e *testEnv) enumT(name string, tag types.TypeID, discrs []uint64, payloads ...[]types.TypeID) types.TypeID {
	d := &types.EnumDecl{Tag: tag}
	for i, ds := range discrs {
		v := types.VariantDecl{Name: e.in.Intern("V" + string(rune('A'+i))), Discr: ds}
		if i < len(payloads) {
			for _, f := range payloads[i] {
				v.Fields = append(v.Fields, types.FieldDecl{Type: f})
			}
		}
		d.Variants = append(d.Variants, v)
	}
	return e.tt.Named(e.item(name, types.KindEnum, d), nil)
}

// intConst interns an int const (an array length).
func (e *testEnv) intConst(t types.TypeID, n uint64) types.ConstID {
	s, a := e.sa(t)
	b := make([]byte, s)
	types.StoreUint(b, int(s), n)
	return e.consts.Intern(types.ConstValue{Type: t, Align: a, Bytes: b})
}

// fnb builds one function body.
type fnb struct {
	e    *testEnv
	item types.ItemID
	b    *mir.Body
	cur  mir.BlockID
}

// body starts the body of item: the return local, then the parameters of
// its signature; spans are at offset 100*line, so line n of LineCol.
func (e *testEnv) body(item types.ItemID) *fnb {
	sig := &e.items.Get(item).Decl.(*types.FuncDecl).Sig
	b := mir.NewBody(item, source.Span{File: 1, Start: 0, End: 1}, sig.ReturnType(e.tt))
	for _, p := range sig.Params {
		b.AddParam(p.Type, p.Name, 0)
	}
	f := &fnb{e: e, item: item, b: b}
	f.cur = b.AddBlock(mir.OriginBody, mir.NoDefer)
	return f
}

// at is a span on line ln, column col.
func (f *fnb) at(ln, col uint32) mir.SpanID {
	return f.b.AddSpan(source.Span{File: 1, Start: (ln-1)*100 + col - 1, End: (ln-1)*100 + col})
}

func (f *fnb) local(t types.TypeID) mir.LocalID {
	return f.b.AddLocal(mir.Local{Type: t, Kind: mir.LocalTemp, Loop: mir.NoLoop})
}

func (f *fnb) assign(p mir.Place, rv mir.Rvalue, sp mir.SpanID) {
	blk := &f.b.Blocks[f.cur]
	blk.Stmts = append(blk.Stmts, mir.Statement{Kind: mir.StAssign, Place: p, Rvalue: rv, Span: sp})
}

func (f *fnb) newBlock() mir.BlockID { return f.b.AddBlock(mir.OriginBody, mir.NoDefer) }

func (f *fnb) term(t mir.Terminator) { f.b.Blocks[f.cur].Term = t }

func (f *fnb) ret(sp mir.SpanID) { f.term(mir.Terminator{Kind: mir.TermReturn, Span: sp}) }

func place(l mir.LocalID) mir.Place { return mir.LocalPlace(l) }

// unit is a one-pack unit of the given bodies (symbols mk.<key>) plus
// bodiless functions (externs and hooks in no pack).
func unitOf(e *testEnv, bodies []*mir.Body, bodiless ...types.ItemID) *Unit {
	u := &Unit{Packs: [][]int{nil}}
	for _, b := range bodies {
		u.Packs[0] = append(u.Packs[0], len(u.Funcs))
		it := e.items.Get(b.Item)
		u.Funcs = append(u.Funcs, Func{Symbol: "mk." + it.Key.String(), Key: it.Key.String(), Item: b.Item, Body: b})
	}
	for _, id := range bodiless {
		it := e.items.Get(id)
		sym := "mk." + it.Key.String()
		if it.Decl.(*types.FuncDecl).Sig.Extern {
			sym = e.in.Str(it.Name)
		}
		u.Funcs = append(u.Funcs, Func{Symbol: sym, Key: it.Key.String(), Item: id, Pack: -1})
	}
	return u
}

// emit is pack 0's IR for target.
func emit(t *testing.T, e *testEnv, u *Unit, target types.Target, profile types.Profile) string {
	t.Helper()
	var b bytes.Buffer
	for _, f := range u.Funcs {
		if f.Body != nil {
			if err := f.Body.Validate(&mir.Env{Types: e.tt}); err != nil {
				t.Fatalf("%s: %v", f.Symbol, err)
			}
		}
	}
	if _, err := EmitPack(u, 0, e, Config{Target: target, Profile: profile}, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// fnText is the text of one function definition in a module.
func fnText(ll, symbol string) string {
	i := strings.Index(ll, "@"+symbol+"(")
	if i < 0 {
		i = strings.Index(ll, "@\""+symbol+"\"(")
	}
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(ll[:i], "\ndefine ")
	end := strings.Index(ll[i:], "\n}\n")
	if start < 0 || end < 0 {
		return ""
	}
	return ll[start+1 : i+end+2]
}
