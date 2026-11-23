// Why: a loan through which nothing can be written carries no store, and the
// shallow predicate that says so must accept exactly the safe types.
// What: tests of shallow.go's predicate: 18 types with their
// verdicts, the whole-type additions, every exclusion, recursive types and
// types whose declaration borrow cannot read.
// How: tables of shallowCase run through checkShallow.

package borrow

import (
	"testing"

	"mk/internal/types"
)

type shallowCase struct {
	name string
	t    types.TypeID
	want bool
	why  string
}

func checkShallow(t *testing.T, k *kit, cases []shallowCase) {
	t.Helper()
	w := newWalker(k.env(nil), nil)
	for _, c := range cases {
		if got := w.shallow(c.t); got != c.want {
			t.Errorf("shallow(%s) = %v, want %v (%s)", c.name, got, c.want, c.why)
		}
	}
}

// 18 types, with their verdicts under the structural predicate.
func TestShallowBundle(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	fn := k.tab.Fn(types.FnType{Result: types.Void})
	k.structItem(k.main, "SF", 0, false, false, field("s", types.Str), field("f", fn))
	k.structItem(k.main, "SS", 0, false, false, field("s", types.Str), field("r", refI32(k)))
	k.structItem(k.main, "SP", 1, false, false, field("s", types.Str), field("x", p0))
	checkShallow(t, k, []shallowCase{
		{"str", types.Str, true, "(shared, u8)"},
		{"&i32", refI32(k), true, "(shared, i32)"},
		{"[]u8", k.tab.Slice(types.U8), true, "(shared, u8)"},
		{"SS{str,&i32}", k.named("SS"), true, "a struct of shallow fields"},
		{"fn", fn, false, "a closure's captures are not in refSteps"},
		{"SF{str,fn}", k.named("SF"), false, "a fn inline"},
		{"&T", k.tab.Ref(p0), false, "an opaque referent"},
		{"[]T", k.tab.Slice(p0), false, "an opaque referent"},
		{"T", p0, false, "ANY"},
		{"SP[T]", k.named("SP", p0), false, "a type parameter inline"},
		{"&Alloc", k.tab.Ref(k.Alloc), false, "an allocator referent"},
		{"&Heap", k.tab.Ref(k.Heap), false, "an allocator referent"},
		{"&Arena", k.tab.Ref(k.Arena), false, "an allocator referent, writable by type"},
		{"&str", k.tab.Ref(types.Str), false, "a carrier referent"},
		{"mut i32", k.tab.Mut(types.I32), false, "an exclusive step"},
		{"&Cell[i32]", k.tab.Ref(k.named("Cell", types.I32)), true, "non-carrier Cell holds no loans"},
		{"&Cell[&i32]", k.tab.Ref(k.named("Cell", refI32(k))), false, "a referent writable by type"},
		{"&List[i32]", k.tab.Ref(k.named("List", types.I32)), false, "a carrier referent (a view)"},
		{"List[str]", k.named("List", types.Str), false, "a view by value"},
	})
}

// A value is shallow by its whole type, not only when it is a reference, so
// options, arrays, error unions, records, structs of shallow fields and types
// with no reference at all are shallow.
func TestShallowAdditions(t *testing.T) {
	k := newKit(t)
	ri := refI32(k)
	k.structItem(k.main, "SS", 0, false, false, field("s", types.Str), field("r", ri))
	k.structItem(k.main, "Nm", 0, false, false, field("s", types.Str), field("k", types.I32), field("r", ri))
	k.structItem(k.main, "Outer", 0, false, false, field("in", k.named("SS")), field("n", types.Int))
	k.structItem(k.main, "Lin", 0, false, true, field("s", types.Str))
	k.structItem(k.main, "Raw", 0, false, false, field("s", types.Str), field("p", k.tab.RawPtr(types.U8)))
	k.structItem(k.main, "HC", 0, false, false, field("s", types.Str), field("c", k.named("Cell", ri)))
	k.enumItem("Tok", 0, []fieldSpec{field("text", types.Str)}, []fieldSpec{field("n", types.I64)}, nil)
	es := k.errs("Bad")
	rec := k.tab.Record([]types.RecordField{{Name: k.in.Intern("r"), Type: ri}, {Name: k.in.Intern("s"), Type: types.Str}})
	recPlain := k.tab.Record([]types.RecordField{{Name: k.in.Intern("n"), Type: types.I32}})
	checkShallow(t, k, []shallowCase{
		{"?str", k.tab.Optional(types.Str), true, "?str"},
		{"[2]str", k.tab.Array(k.lenArg(2), types.Str), true, "[N]str"},
		{"Bad!str", k.tab.ErrorUnion(es, types.Str), true, "E!T is inline like ?T"},
		{"?&i32", k.tab.Optional(ri), true, "(shared, i32) under ?"},
		{"[]?u8", k.tab.Slice(k.tab.Optional(types.U8)), true, "a referent with no reference"},
		{"(r: &i32, s: str)", rec, true, "a record of shallow fields"},
		{"Nm{s,k,r}", k.named("Nm"), true, "probe c07's record element"},
		{"[2]Nm", k.tab.Array(k.lenArg(2), k.named("Nm")), true, "an array of a shallow struct"},
		{"Outer{SS,int}", k.named("Outer"), true, "a non-view struct of shallow fields, nested"},
		{"Lin{str}", k.named("Lin"), true, "linear is a kind, not a reference"},
		{"Tok{str | i64 | }", k.named("Tok"), true, "every variant's payload is shallow"},
		{"i32", types.I32, true, "a type with no reference"},
		{"Point", k.Point, true, "a type with no reference"},
		{"(n: i32)", recPlain, true, "a record with no reference"},
		{"void", types.Void, true, "no reference"},
		{"Raw{str,*u8}", k.named("Raw"), true, "a raw pointer is no reference to the checker (refSteps); writing through it is unsafe code's"},
		{"Cell[&i32]", k.named("Cell", ri), false, "a Cell by value is writable by type inline, so a shared path to its holder may store a new reference into it"},
		{"HC{str, Cell[&i32]}", k.named("HC"), false, "a struct with a Cell inline"},
		{"?Cell[&i32]", k.tab.Optional(k.named("Cell", ri)), false, "a Cell inline under ?"},
	})
}

// Every exclusion: a mut reference, ANY (a view, a type parameter, Self), a
// carrier referent, a referent writable by type, an allocator, an opaque type
// inline or as a referent.
func TestShallowExclusions(t *testing.T) {
	k := newKit(t)
	p0 := k.tab.Param(0)
	self := k.tab.SelfType()
	fn := k.tab.Fn(types.FnType{Result: types.Void})
	ri := refI32(k)
	cellStr := k.named("Cell", types.Str)
	// H is a holder: no interior inline, but it reaches a Cell
	// through a shared reference, so it is a carrier and not writable by
	// type. Only the carrier test keeps &H deep.
	k.structItem(k.main, "H", 0, false, false, field("name", types.Str), field("slot", k.tab.Ref(cellStr)))
	k.structItem(k.main, "WC", 0, false, false, field("n", types.I32), field("c", k.named("Cell", types.I32)))
	k.structItem(k.main, "WM", 0, false, false, field("m", k.named("Mutex", types.I32)))
	k.structItem(k.main, "MS", 0, false, false, field("s", types.Str), field("m", k.tab.Mut(types.I32)))
	k.structItem(k.main, "AS", 0, false, false, field("s", types.Str), field("a", k.tab.Ref(k.Arena)))
	k.structItem(k.main, "VS", 0, true, false, field("s", types.Str))
	k.structItem(k.main, "FS", 0, false, false, field("n", types.I32), field("f", fn))
	k.enumItem("Ev", 0, []fieldSpec{field("s", types.Str)}, []fieldSpec{field("c", k.tab.Ref(cellStr))})
	recMut := k.tab.Record([]types.RecordField{{Name: k.in.Intern("m"), Type: k.tab.Mut(types.I32)}, {Name: k.in.Intern("s"), Type: types.Str}})
	recFn := k.tab.Record([]types.RecordField{{Name: k.in.Intern("f"), Type: fn}})
	recT := k.tab.Record([]types.RecordField{{Name: k.in.Intern("x"), Type: p0}})
	checkShallow(t, k, []shallowCase{
		// A mut reference.
		{"mut i32", k.tab.Mut(types.I32), false, "exclusive"},
		{"mut str", k.tab.Mut(types.Str), false, "exclusive (probe b04's middle)"},
		{"mut []u8", k.tab.MutSlice(types.U8), false, "exclusive slice"},
		{"?mut i32", k.tab.Optional(k.tab.Mut(types.I32)), false, "exclusive under ?"},
		{"MS{str, mut i32}", k.named("MS"), false, "an exclusive field"},
		{"(m: mut i32, s: str)", recMut, false, "an exclusive record field"},
		// ANY: a view, a type parameter, Self.
		{"Text", k.named("Text"), false, "a view by value"},
		{"VS{str} (view)", k.named("VS"), false, "a view by value, even of shallow fields (a view's loans may sit outside its fields)"},
		{"?Text", k.tab.Optional(k.named("Text")), false, "a view inline"},
		{"T", p0, false, "a type parameter"},
		{"?T", k.tab.Optional(p0), false, "a type parameter inline"},
		{"[2]T", k.tab.Array(k.lenArg(2), p0), false, "a type parameter inline"},
		{"(x: T)", recT, false, "a type parameter in a record"},
		{"Self", self, false, "Self"},
		{"&Self", k.tab.Ref(self), false, "Self as a referent"},
		// A carrier referent.
		{"&str", k.tab.Ref(types.Str), false, "str holds a loan"},
		{"&[]u8", k.tab.Ref(k.tab.Slice(types.U8)), false, "a slice holds a loan"},
		{"[]str", k.tab.Slice(types.Str), false, "its elements hold loans"},
		{"&&i32", k.tab.Ref(ri), false, "a reference referent"},
		{"&H{str, &Cell[str]}", k.tab.Ref(k.named("H")), false, "a carrier that reaches a Cell (twin-carrier-ref.mk)"},
		{"&Text", k.tab.Ref(k.named("Text")), false, "a view referent"},
		{"&List[str]", k.tab.Ref(k.named("List", types.Str)), false, "a view referent (probe a03's *q)"},
		{"&HeapList[str]", k.tab.Ref(k.named("HeapList", types.Str)), false, "a view referent"},
		{"Ev{str | &Cell[str]}", k.named("Ev"), false, "one variant reaches a Cell"},
		// A referent writable by type (carrier Cell/Mutex/Chan, mut view) vs
		// non-carrier Cell/Mutex (which hold no loans and stay shallow).
		{"&Cell[str]", k.tab.Ref(cellStr), false, "writable by type and a carrier (probe a02)"},
		{"&Cell[i32]", k.tab.Ref(k.named("Cell", types.I32)), true, "non-carrier Cell holds no loans"},
		{"&Mutex[i32]", k.tab.Ref(k.named("Mutex", types.I32)), true, "non-carrier Mutex holds no loans"},
		{"&Mutex[&i32]", k.tab.Ref(k.named("Mutex", ri)), false, "carrier Mutex is writable by type"},
		{"&WC{i32, Cell[i32]}", k.tab.Ref(k.named("WC")), true, "non-carrier Cell inside the referent"},
		{"&WM{Mutex[i32]}", k.tab.Ref(k.named("WM")), true, "non-carrier Mutex inside the referent"},
		{"Cell[&Cell[str]]", k.named("Cell", k.tab.Ref(cellStr)), false, "the referent is a Cell"},
		// An allocator.
		{"&Arena", k.tab.Ref(k.Arena), false, "an allocator"},
		{"&Alloc", k.tab.Ref(k.Alloc), false, "an allocator (no carrier in the kit: only the allocator test excludes it)"},
		{"&Heap", k.tab.Ref(k.Heap), false, "an allocator"},
		{"?&Alloc", k.tab.Optional(k.tab.Ref(k.Alloc)), false, "an allocator under ?"},
		{"AS{str, &Arena}", k.named("AS"), false, "an allocator reference inline"},
		// Opaque: fn, type parameter, Self, inline or as a referent.
		{"fn", fn, false, "a fn"},
		{"?fn", k.tab.Optional(fn), false, "a fn inline"},
		{"&fn", k.tab.Ref(fn), false, "a fn referent (no carrier in the kit: only the opaque test excludes it)"},
		{"FS{i32, fn}", k.named("FS"), false, "a fn field (probe b09's S)"},
		{"(f: fn)", recFn, false, "a fn in a record"},
		{"&T", k.tab.Ref(p0), false, "a type parameter referent"},
		{"[]T", k.tab.Slice(p0), false, "a type parameter referent"},
	})
}

// A recursive type terminates, whatever its verdict.
func TestShallowRecursive(t *testing.T) {
	k := newKit(t)
	node := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern("Node"), Kind: types.KindStruct,
		Key: types.ItemKey("main", "Node")})
	nodeT := k.tab.Named(node, nil)
	k.items.Get(node).Decl = &types.StructDecl{Fields: []types.FieldDecl{{Name: k.in.Intern("v"), Type: types.I32},
		{Name: k.in.Intern("next"), Type: k.tab.Optional(k.tab.Ref(nodeT))}}}
	k.byNm["Node"] = node
	checkShallow(t, k, []shallowCase{
		{"Node{i32, ?&Node}", nodeT, false, "the referent Node holds a reference: a carrier"},
		{"&Node", k.tab.Ref(nodeT), false, "a carrier referent"},
	})
}

// A named type whose declaration borrow cannot read shows no field to a
// walk, so it is never judged shallow, by value, inline or as a referent
// (a conservative choice).
func TestShallowUnreadDecl(t *testing.T) {
	k := newKit(t)
	u := k.items.AddItem(&types.Item{Module: k.main.ID, Name: k.in.Intern("U"), Kind: types.KindStruct,
		Key: types.ItemKey("main", "U")})
	ut := k.tab.Named(u, nil)
	checkShallow(t, k, []shallowCase{
		{"U (no Decl)", ut, false, "no field to read"},
		{"?U", k.tab.Optional(ut), false, "inline"},
		{"&U", k.tab.Ref(ut), false, "as a referent"},
		{"Scope", k.Scope, false, "read through a reference, and a view"},
	})
}
