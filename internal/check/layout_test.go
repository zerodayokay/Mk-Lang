// Why: layouts and shape terms must agree with the target architecture,
// and a layout cycle must be E0907, never a hang.
// What: TestLayoutMatchesArchitecture, TestShapeTermInstantiates,
// TestE0907_LayoutCycle, TestShape_OpaqueAlwaysFalse, TestLayoutMemoConcurrent,
// TestErrorSideInPlace.
// How: builds types by hand and compares Layout with evalTerm, a reference
// evaluation of each shape term.

package check

import (
	"fmt"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// fwd adds a struct whose declaration the test writes afterwards.
func (h *hand) fwd(name string) types.ItemID { return h.item(name, types.KindStruct, nil) }

// errorItem adds an error with payload fields.
func (h *hand) errorItem(name string, fields ...types.TypeID) types.ItemID {
	d := &types.ErrorDecl{}
	for i, f := range fields {
		d.Fields = append(d.Fields, types.FieldDecl{Name: h.in.Intern(fmt.Sprintf("f%d", i)), Type: f})
	}
	id := h.item(name, types.KindError, d)
	d.Code = types.ErrorCode(h.items.Get(id).Key)
	return id
}

func offsets(l types.Layout) []uint64 {
	var out []uint64
	for _, f := range l.Fields {
		out = append(out, f.Offset)
	}
	return out
}

func TestLayoutMatchesArchitecture(t *testing.T) {
	for _, target := range []types.Target{types.X86_64Windows, types.X86_64Linux} {
		t.Run(target.Name, func(t *testing.T) {
			h := newHand(t)
			h.c.cfg.Target = target
			tab := h.tab
			rec := tab.Record([]types.RecordField{{Name: h.in.Intern("b"), Type: types.U8}, {Name: h.in.Intern("a"), Type: types.Int}})
			mixed := h.strct("Mixed", 0, "", types.U8, types.U32, types.U16)
			packed := h.strct("Packed", 0, "", types.U8, types.U32, types.U16)
			h.items.Get(packed).Decl.(*types.StructDecl).Packed = 1
			packed2 := h.strct("Packed2", 0, "", types.U8, types.U32, types.U16)
			h.items.Get(packed2).Decl.(*types.StructDecl).Packed = 2
			color := h.enum("Color", nil, nil, nil)
			wide := h.enum("Wide", nil, nil)
			h.items.Get(wide).Decl.(*types.EnumDecl).Variants[1].Discr = 300
			shape := h.enum("Shape", []types.TypeID{types.U8}, []types.TypeID{types.Int})
			tagged := h.enum("Tagged", nil)
			h.items.Get(tagged).Decl.(*types.EnumDecl).Tag = types.U32
			bad := h.errorItem("Bad", types.U32)
			gone := h.errorItem("Gone")
			es := tab.ErrorSet([]types.ErrorMember{{Item: bad}})
			esGone := tab.ErrorSet([]types.ErrorMember{{Item: gone}})
			rows := []struct {
				name  string
				t     types.TypeID
				size  uint64
				align uint32
				offs  []uint64
			}{
				{"i8", types.I8, 1, 1, nil}, {"i16", types.I16, 2, 2, nil}, {"i32", types.I32, 4, 4, nil},
				{"i64", types.I64, 8, 8, nil}, {"u8", types.U8, 1, 1, nil}, {"u64", types.U64, 8, 8, nil},
				{"int", types.Int, 8, 8, nil}, {"uint", types.Uint, 8, 8, nil},
				{"f32", types.F32, 4, 4, nil}, {"f64", types.F64, 8, 8, nil},
				{"bool", types.Bool, 1, 1, nil}, {"char", types.Char, 4, 4, nil},
				{"void", types.Void, 0, 1, nil}, {"never", types.Never, 0, 1, nil},
				{"&int", tab.Ref(types.Int), 8, 8, nil}, {"mut int", tab.Mut(types.Int), 8, 8, nil},
				{"*int", tab.RawPtr(types.Int), 8, 8, nil},
				{"[]int", tab.Slice(types.Int), 16, 8, nil}, {"mut []u8", tab.MutSlice(types.U8), 16, 8, nil},
				{"str", types.Str, 16, 8, nil},
				{"fn", tab.Fn(types.FnType{Result: types.Void}), 8, 8, nil},
				{"[4]u16", tab.Array(h.c.intArg(4), types.U16), 8, 2, nil},
				{"?&int", tab.Optional(tab.Ref(types.Int)), 8, 8, nil},
				{"?mut int", tab.Optional(tab.Mut(types.Int)), 8, 8, nil},
				{"?fn", tab.Optional(tab.Fn(types.FnType{Result: types.Void})), 8, 8, nil},
				{"?int", tab.Optional(types.Int), 16, 8, nil},
				{"?u8", tab.Optional(types.U8), 2, 1, nil},
				{"?str", tab.Optional(types.Str), 24, 8, nil},
				{"?[]int", tab.Optional(tab.Slice(types.Int)), 24, 8, nil},
				{"?*int", tab.Optional(tab.RawPtr(types.Int)), 16, 8, nil},
				{"(b: u8, a: int)", rec, 16, 8, []uint64{0, 8}},
				{"Mixed", h.named(mixed), 12, 4, []uint64{0, 4, 8}},
				{"packed Mixed", h.named(packed), 7, 1, []uint64{0, 1, 5}},
				{"packed(2) Mixed", h.named(packed2), 8, 2, []uint64{0, 2, 6}},
				{"Color", h.named(color), 1, 1, nil},
				{"Wide", h.named(wide), 2, 2, nil},
				{"Tagged: u32", h.named(tagged), 4, 4, nil},
				{"Shape", h.named(shape), 16, 8, nil},
				{"Bad!int", tab.ErrorUnion(es, types.Int), 16, 8, nil},
				{"Gone!void", tab.ErrorUnion(esGone, types.Void), 4, 4, nil},
				{"Bad value", tab.ErrorSetType(es), 8, 4, nil},
				{"Cell[u16]", h.named(h.c.known.Cell, types.U16), 2, 2, nil},
				{"Mutex[u8]", h.named(h.c.known.Mutex, types.U8), 8, 4, []uint64{0, 4}},
			}
			for _, r := range rows {
				l := h.c.Layout(r.t)
				if l.Shape.Size != r.size || l.Shape.Align != r.align {
					t.Errorf("%s: size %d align %d, want %d %d", r.name, l.Shape.Size, l.Shape.Align, r.size, r.align)
				}
				if r.offs != nil && fmt.Sprint(offsets(l)) != fmt.Sprint(r.offs) {
					t.Errorf("%s: offsets %v, want %v", r.name, offsets(l), r.offs)
				}
			}
			// Tags and variants.
			if l := h.c.Layout(tab.Optional(tab.Ref(types.Int))); !l.HasTag || !l.Tag.Niche || l.Tag.Size != 8 {
				t.Errorf("?&int tag %+v, want niche of 8", l.Tag)
			}
			if l := h.c.Layout(tab.Optional(types.Int)); l.Tag.Niche || l.Tag.Size != 1 || l.Variants[1].Fields[0].Offset != 8 {
				t.Errorf("?int: %+v", l)
			}
			l := h.c.Layout(h.named(shape))
			if l.Tag.Size != 1 || len(l.Variants) != 2 || l.Variants[0].Fields[0].Offset != 8 || l.Variants[1].Fields[0].Offset != 8 {
				t.Errorf("Shape: %+v", l)
			}
			l = h.c.Layout(tab.ErrorUnion(es, types.Int))
			if l.Tag.Size != 4 || l.Variants[0].Discr != 0 || l.Variants[1].Discr != uint64(types.ErrorCode(h.items.Get(bad).Key)) {
				t.Errorf("Bad!int: %+v", l)
			}
			if l := h.c.Layout(h.named(wide)); l.Variants[1].Discr != 300 {
				t.Errorf("Wide: %+v", l)
			}
		})
	}
}

// evalTerm is a reference evaluation of a shape term's size and alignment
// for given argument shapes: the arithmetic the shared layout function must
// do, here only to pin check's Layout against the terms it
// stores. Optionals over a parameter are never niche here (the term does
// not say whether the argument is a pointer).
func evalTerm(h *hand, t types.ShapeTerm, args []types.Shape) (size uint64, align uint32) {
	strct := func(fs []types.ShapeTerm, packed uint32, base uint64) (uint64, uint32) {
		off, a := base, uint32(1)
		for _, f := range fs {
			s, fa := evalTerm(h, f, args)
			if packed != 0 && fa > packed {
				fa = packed
			}
			off = alignUp(off, fa) + s
			a = max(a, fa)
		}
		return alignUp(off, a), a
	}
	switch t.Kind {
	case types.TermLeaf:
		return t.Leaf.Size, t.Leaf.Align
	case types.TermParam:
		return args[t.Param].Size, args[t.Param].Align
	case types.TermStruct, types.TermRecord:
		return strct(t.Fields, t.Packed, 0)
	case types.TermArray:
		s, a := evalTerm(h, *t.Elem, args)
		return h.c.arrayLen(t.Len) * s, a
	case types.TermOptional:
		s, a := evalTerm(h, *t.Elem, args)
		a = max(a, 1)
		return alignUp(alignUp(1, a)+s, a), a
	case types.TermEnum:
		tag := uint8(types.EnumTagBytes(t.Discrs))
		var us uint64
		ua := uint32(1)
		for _, v := range t.Variants {
			s, a := strct(v, 0, 0)
			us, ua = max(us, s), max(ua, a)
		}
		al := max(uint32(tag), ua)
		return alignUp(alignUp(uint64(tag), ua)+us, al), al
	}
	h.t.Fatalf("evalTerm: kind %v", t.Kind)
	return 0, 0
}

func TestShapeTermInstantiates(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	p0, p1 := tab.Param(0), tab.Param(1)
	pair := h.strct("Pair", 2, "", p0, tab.Optional(p1), tab.Array(h.c.intArg(2), p0), tab.Ref(p1))
	wrap := h.strct("Wrap", 1, "", types.U8, h.named(pair, p0, types.U16))
	opt := h.enum("Opt", nil, []types.TypeID{types.U8})
	h.items.Get(opt).Decl.(*types.EnumDecl).Generics = []types.GenericParam{{Name: h.in.Intern("T")}}
	h.items.Get(opt).Decl.(*types.EnumDecl).Variants[1].Fields[0].Type = p0
	h.m.shapePass()
	if ds := h.m.Diags; len(ds) > 0 {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	args := [][]types.TypeID{{types.U8, types.Int}, {types.Int, types.U8}, {types.Str, types.Bool}, {h.named(h.c.known.Cell, types.U16), types.I32}}
	for _, as := range args {
		for _, gen := range []types.ItemID{pair, wrap, opt} {
			gs, _ := declGenerics(h.items.Get(gen).Decl)
			inst := h.named(gen, as[:len(gs)]...)
			var shapes []types.Shape
			for _, a := range as {
				shapes = append(shapes, h.c.Shape(a))
			}
			var term *types.ShapeTerm
			switch d := h.items.Get(gen).Decl.(type) {
			case *types.StructDecl:
				term = d.Shape
			case *types.EnumDecl:
				term = d.Shape
			}
			if term == nil {
				t.Fatalf("%s: no shape term", h.str(inst))
			}
			s, a := evalTerm(h, *term, shapes)
			l := h.c.Layout(inst)
			if s != l.Shape.Size || a != l.Shape.Align {
				t.Errorf("%s: term gives %d/%d, Layout %d/%d", h.str(inst), s, a, l.Shape.Size, l.Shape.Align)
			}
		}
	}
	// Marks of compiler-known items inlined into a term.
	h2 := newHand(t)
	box := h2.strct("Holder", 1, "", h2.named(h2.c.known.Cell, h2.tab.Param(0)), h2.named(h2.c.known.Mutex, h2.tab.Param(0)))
	h2.m.shapePass()
	term := h2.items.Get(box).Decl.(*types.StructDecl).Shape
	if term == nil || term.Fields[0].Marks&types.MarkInterior == 0 || term.Fields[1].Marks&types.MarkNoFreeze == 0 {
		t.Errorf("Holder term marks: %+v", term)
	}
}

func TestE0907_LayoutCycle(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	// struct Node { next: ?&Node } is finite.
	node := h.fwd("Node")
	h.items.Get(node).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: tab.Optional(tab.Ref(h.named(node)))}}}
	// struct A { b: B }, struct B { a: A }: one E0907 at A.
	a, b := h.fwd("A"), h.fwd("B")
	h.items.Get(a).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: h.named(b)}}}
	h.items.Get(b).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: h.named(a)}}}
	// struct L[T] { next: ?L[T] }.
	l := h.fwd("L")
	h.items.Get(l).Decl = &types.StructDecl{Generics: []types.GenericParam{{}}, Fields: []types.FieldDecl{{Type: tab.Optional(h.named(l, tab.Param(0)))}}}
	// struct W[T] { x: T } is finite; struct S { w: W[S] } is not.
	wi := h.strct("W", 1, "", tab.Param(0))
	s := h.fwd("S")
	h.items.Get(s).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Type: h.named(wi, h.named(s))}}}
	// struct G[T] { x: ?G[[1]T] } grows without repeating.
	g := h.fwd("G")
	h.items.Get(g).Decl = &types.StructDecl{Generics: []types.GenericParam{{}},
		Fields: []types.FieldDecl{{Type: tab.Optional(h.named(g, tab.Array(h.c.intArg(1), tab.Param(0))))}}}
	// enum E { A(x: E) }.
	e := h.enum("E", []types.TypeID{types.Int})
	h.items.Get(e).Decl.(*types.EnumDecl).Variants[0].Fields[0].Type = h.named(e)
	h.m.shapePass()
	var got []string
	for _, d := range h.m.Diags {
		if d.Code != diag.E0907 {
			t.Errorf("unexpected %v %s", d.Code, d.Message)
			continue
		}
		got = append(got, h.in.Str(h.items.Get(itemAt(h, d)).Name)+":"+fact(d, "cycle"))
	}
	want := "[A:[A B A] L:[L L] S:[S W S] G:" + fmt.Sprint(gChain(maxByValueDepth)) + " E:[E E]]"
	if fmt.Sprint(got) != want {
		t.Errorf("E0907s %v\nwant %s", got, want)
	}
	if h.items.Get(node).Decl.(*types.StructDecl).Shape == nil {
		t.Errorf("Node has no shape term")
	}
	if sz := h.c.Shape(h.named(node)).Size; sz != 8 {
		t.Errorf("Node size %d", sz)
	}
}

func gChain(n int) []string {
	out := make([]string, n+1)
	for i := range out {
		out[i] = "G"
	}
	return out
}

// itemAt finds the hand item whose span is the diagnostic's anchor.
func itemAt(h *hand, d *diag.Diagnostic) types.ItemID {
	for _, ii := range h.m.info.Items {
		if h.items.Get(ii.Item).Span == d.At {
			return ii.Item
		}
	}
	h.t.Fatalf("no item at %v", d.At)
	return types.NoItem
}

func TestShape_OpaqueAlwaysFalse(t *testing.T) {
	h := newHand(t)
	u := h.strct("User", 0, "", types.Str, types.Int)
	h.items.Get(u).Decl.(*types.StructDecl).Fields[1].Exported = true
	s1 := h.c.Shape(h.named(u))
	s2 := h.c.Shape(h.named(u))
	if s1.Opaque || s1 != s2 || s1.Size != 24 {
		t.Errorf("User shape %+v / %+v", s1, s2)
	}
}

func TestLayoutMemoConcurrent(t *testing.T) {
	h := newHand(t)
	pair := h.strct("Pair", 2, "", h.tab.Param(0), h.tab.Param(1))
	var ts []types.TypeID
	for _, x := range []types.TypeID{types.U8, types.Int, types.Str, h.tab.Optional(types.U16)} {
		for _, y := range []types.TypeID{types.Bool, types.F64, h.tab.Ref(types.Int)} {
			ts = append(ts, h.named(pair, x, h.tab.Optional(y)))
		}
	}
	res := make([][]types.Shape, 8)
	done := make(chan struct{})
	for g := range res {
		go func() {
			defer func() { done <- struct{}{} }()
			for _, x := range ts {
				res[g] = append(res[g], h.c.Shape(x))
			}
		}()
	}
	for range res {
		<-done
	}
	for i, x := range ts {
		want := h.c.Shape(x)
		for g := range res {
			if res[g][i] != want {
				t.Errorf("%s: goroutine %d %+v, want %+v", h.str(x), g, res[g][i], want)
			}
		}
	}
}

// TestErrorSideInPlace: an error's fields sit at the same offsets in every
// error set and every E!T that holds it, whatever the ok type and the other
// errors are, so the error side of an E!T and a widened set are the
// union's own bytes; the set is never larger or more aligned than the
// union, and no union is larger than a common payload offset would make it.
func TestErrorSideInPlace(t *testing.T) {
	h := newHand(t)
	tab := h.tab
	a16 := h.strct("A16", 0, "", types.U8)
	h.items.Get(a16).Decl.(*types.StructDecl).Align = 16
	big := h.named(a16)
	errs := []types.ItemID{
		h.errorItem("Bare"), h.errorItem("B1", types.U8), h.errorItem("B2", types.U16),
		h.errorItem("B3", types.U8, types.U8, types.U8), h.errorItem("B4", types.U32),
		h.errorItem("B8", types.I64), h.errorItem("B12", tab.Array(h.c.intArg(3), types.U32)),
		h.errorItem("B16", big), h.errorItem("Mix", types.U8, types.I64, types.U16),
	}
	all := make([]types.ErrorMember, len(errs))
	for i, e := range errs {
		all[i] = types.ErrorMember{Item: e}
	}
	sets := [][]types.ErrorMember{all}
	for i := range all {
		sets = append(sets, all[i:i+1])
	}
	oks := []types.TypeID{types.Void, types.U8, types.U16, types.I32, types.I64, big,
		tab.Array(h.c.intArg(3), types.U8), tab.Optional(types.Int)}
	fields := func(l types.Layout, code uint64) string {
		for _, v := range l.Variants {
			if v.Discr == code {
				return fmt.Sprint(v.Fields)
			}
		}
		return "absent"
	}
	for _, members := range sets {
		es := tab.ErrorSet(members)
		st := tab.ErrorSetType(es)
		ls := h.c.Layout(st)
		for _, ok := range oks {
			u := tab.ErrorUnion(es, ok)
			lu := h.c.Layout(u)
			name := h.str(u)
			if ls.Shape.Size > lu.Shape.Size || ls.Shape.Align > lu.Shape.Align {
				t.Errorf("%s: the set is %+v, larger than the union %+v", name, ls.Shape, lu.Shape)
			}
			if lu.Tag.Offset != 0 || lu.Tag.Size != 4 || ls.Tag != lu.Tag {
				t.Errorf("%s: tag %+v, the set's %+v", name, lu.Tag, ls.Tag)
			}
			// The bound a common payload offset gives: the union after the
			// code at the largest alignment, sized to the largest variant.
			us, ua := h.c.Shape(ok).Size, max(h.c.Shape(ok).Align, 4)
			for _, m := range members {
				end, va := uint64(0), uint32(1)
				for _, f := range h.items.Get(m.Item).Decl.(*types.ErrorDecl).Fields {
					fs := h.c.Shape(f.Type)
					end, va = alignUp(end, fs.Align)+fs.Size, max(va, fs.Align)
				}
				us, ua = max(us, alignUp(end, va)), max(ua, va)
			}
			if bound := alignUp(alignUp(4, ua)+us, ua); lu.Shape.Size > bound {
				t.Errorf("%s: size %d, more than %d", name, lu.Shape.Size, bound)
			}
			for _, m := range members {
				code := uint64(h.items.Get(m.Item).Decl.(*types.ErrorDecl).Code)
				alone := h.c.Layout(tab.ErrorSetType(tab.ErrorSet([]types.ErrorMember{m})))
				want := fields(alone, code)
				if got := fields(lu, code); got != want {
					t.Errorf("%s: %s at %s, alone at %s", name, h.items.Get(m.Item).Key, got, want)
				}
				if got := fields(ls, code); got != want {
					t.Errorf("%s: %s in the set at %s, alone at %s", name, h.items.Get(m.Item).Key, got, want)
				}
			}
		}
	}
}
