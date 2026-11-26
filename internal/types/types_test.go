// Why: the type table is shared by every stage, so interning, keys, error sets,
// consts and read sets must be canonical and safe under concurrency.
// What: tests of fixed types, structural interning, record field order, fn
// flows, error sets, stable keys, the items table, const forms and read sets.
// How: builds values in different orders and from several goroutines and
// requires one ID and one printed form; misuse is an ICE.

package types

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"mk/internal/source"
)

type world struct {
	in     *source.Interner
	items  *Items
	consts *ConstTable
	tab    *Table
}

func newWorld() *world {
	in := source.NewInterner()
	items := NewItems()
	consts := NewConstTable()
	return &world{in: in, items: items, consts: consts, tab: NewTable(in, items, consts)}
}

func (w *world) item(module, name string, kind ItemKind) ItemID {
	m := w.items.AddModule(&Module{Path: module})
	return w.items.AddItem(&Item{Module: m, Name: w.in.Intern(name), Kind: kind, Key: ItemKey(module, name)})
}

func isICE(f func()) bool { return source.Guard("t", source.Span{}, f) != nil }

// TestFixedTypes: the primitives sit at fixed IDs in every table and
// print as doc/reference.md names them.
func TestFixedTypes(t *testing.T) {
	w := newWorld()
	want := map[TypeID]string{Void: "void", Never: "never", Bool: "bool", Char: "char", I8: "i8", I16: "i16",
		I32: "i32", I64: "i64", U8: "u8", U16: "u16", U32: "u32", U64: "u64", Int: "int", Uint: "uint",
		F32: "f32", F64: "f64", Str: "str", Poison: "<error>"}
	for id, s := range want {
		if got := w.tab.TypeString(id, nil); got != s {
			t.Errorf("%d prints %q, want %q", id, got, s)
		}
	}
	if w.tab.IntType(32, true) != I32 || w.tab.IntType(8, false) != U8 {
		t.Error("IntType does not find the fixed IDs")
	}
	if w.tab.Len() != NumFixed {
		t.Errorf("fresh table has %d slots, want %d", w.tab.Len(), NumFixed)
	}
}

// TestTyInterningIsStructural: equal types intern to one ID, under
// concurrency.
func TestTyInterningIsStructural(t *testing.T) {
	w := newWorld()
	build := func() []TypeID {
		return []TypeID{w.tab.Ref(I32), w.tab.Mut(w.tab.Slice(U8)), w.tab.MutSlice(U8), w.tab.Optional(w.tab.Ref(Str)),
			w.tab.Array(ConstArg{Kind: CAParam, Param: 0}, F64), w.tab.RawPtr(U8), w.tab.Param(1)}
	}
	first := build()
	var wg sync.WaitGroup
	results := make([][]TypeID, 8)
	for g := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[g] = build() }()
	}
	wg.Wait()
	for _, r := range results {
		if !slices.Equal(r, first) {
			t.Fatalf("concurrent interning gave %v, want %v", r, first)
		}
	}
	if w.tab.MutSlice(U8) == w.tab.Mut(w.tab.Slice(U8)) {
		t.Error("mut []u8 and mut ([]u8) must differ")
	}
	if got := w.tab.TypeString(w.tab.Mut(w.tab.Slice(U8)), nil); got != "mut ([]u8)" {
		t.Errorf("printed %q", got)
	}
	if got := w.tab.TypeString(w.tab.MutSlice(U8), nil); got != "mut []u8" {
		t.Errorf("printed %q", got)
	}
}

// TestRecordFieldsCanonicalOrder: a record's fields intern in one
// canonical order.
func TestRecordFieldsCanonicalOrder(t *testing.T) {
	w := newWorld()
	a, b := w.in.Intern("left"), w.in.Intern("right")
	r1 := w.tab.Record([]RecordField{{a, I32}, {b, Str}})
	r2 := w.tab.Record([]RecordField{{b, Str}, {a, I32}})
	if r1 != r2 {
		t.Fatal("written order changed the record type")
	}
	if got := w.tab.TypeString(r1, nil); got != "(left: i32, right: str)" {
		t.Errorf("printed %q", got)
	}
	if !isICE(func() { w.tab.Intern(Type{Kind: KRecord, Fields: []RecordField{{b, Str}, {a, I32}}}) }) {
		t.Error("unsorted record accepted by Intern")
	}
	if !isICE(func() { w.tab.Record([]RecordField{{a, I32}, {a, Str}}) }) {
		t.Error("duplicate field accepted")
	}
}

// TestFnFlowsPositional: names never reach a fn type.
func TestFnFlowsPositional(t *testing.T) {
	w := newWorld()
	mk := func(name string) TypeID {
		s := Signature{Params: []Param{{Name: w.in.Intern(name), Type: w.tab.Ref(I32)}}, Result: w.tab.Ref(I32),
			Flows: Flows{Sources: []Source{{Param: 1}}, Declared: true}}
		return s.FnType(w.tab)
	}
	if mk("x") != mk("y") {
		t.Fatal("fn(x: &i32): &i32 from x and from y differ")
	}
	if got := w.tab.TypeString(mk("x"), nil); got != "fn(&i32): &i32" {
		t.Errorf("printed %q", got)
	}
}

func TestInternInvariantsAreICE(t *testing.T) {
	w := newWorld()
	for name, f := range map[string]func(){
		"empty error union": func() { w.tab.Intern(Type{Kind: KErrorUnion, Elem: I32}) },
		"ref of none":       func() { w.tab.Ref(NoType) },
		"bad bits":          func() { w.tab.IntType(12, true) },
		"named no item":     func() { w.tab.Named(NoItem, nil) },
		"unknown id":        func() { w.tab.Get(TypeID(9999)) },
		"fn no payload":     func() { w.tab.Intern(Type{Kind: KFn}) },
	} {
		if !isICE(f) {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestErrorSetSortedByKey: an error set is sorted by key, with
// parameter members.
func TestErrorSetSortedByKey(t *testing.T) {
	w := newWorld()
	nf := w.item("std.fs", "NotFound", KindError)
	de := w.item("app", "Denied", KindError)
	s1 := w.tab.ErrorSet([]ErrorMember{{Item: nf}, {Item: de}})
	s2 := w.tab.ErrorSet([]ErrorMember{{Item: de}, {Item: nf}, {Item: de}})
	if s1 != s2 {
		t.Fatal("A|B and B|A differ")
	}
	ms := w.tab.Members(s1)
	if len(ms) != 2 || ms[0].Item != de || ms[1].Item != nf {
		t.Fatalf("members %v not in key order (app.Denied < std.fs.NotFound)", ms)
	}
	withParam := w.tab.ErrorSet([]ErrorMember{{Item: nf}, {IsParam: true, Param: 0}})
	if !w.tab.Members(withParam)[0].IsParam {
		t.Error("params sort first")
	}
	if w.tab.ErrorSet(nil) != EmptyErrors {
		t.Error("empty set")
	}
	one := w.tab.ErrorSet([]ErrorMember{{Item: nf}})
	if w.tab.Union(one, s1) != s1 || !w.tab.Subset(one, s1) || w.tab.Subset(s1, one) {
		t.Error("union or subset")
	}
	if got := w.tab.TypeString(w.tab.ErrorUnion(s1, Void), nil); got != "Denied|NotFound!void" {
		t.Errorf("printed %q", got)
	}
}

// TestStableKeys: members live in the type's namespace; keys are
// text, never IDs; the error identity is fixed by the key alone.
func TestStableKeys(t *testing.T) {
	if MemberKey("main", "Point", "Add") == ItemKey("main", "Add") {
		t.Error("method and top-level function collide")
	}
	if got := ImplKey("main", "std.fmt.Format", "main.Point").String(); got != "main#impl(std.fmt.Format for main.Point)" {
		t.Errorf("impl key %q", got)
	}
	k := ItemKey("std.fs", "NotFound")
	c := ErrorCode(k)
	if c == 0 || c > 1<<31 {
		t.Fatalf("error code %d out of range", c)
	}
	if c != uint32(k.Hash().Uint64()&0x7fffffff)+1 {
		t.Fatal("error code formula")
	}
	// Pinned (computed independently with Python's hashlib): the code must
	// never change, since cached objects embed it.
	if c != 363192304 {
		t.Fatalf("ErrorCode changed: %d", c)
	}
	if TestKey("m", `a "b"`).String() != `m#test("a \"b\"")` {
		t.Error("test key quoting")
	}
}

func TestItemsTable(t *testing.T) {
	w := newWorld()
	id := w.item("main", "Point", KindStruct)
	if got, ok := w.items.ByKey(ItemKey("main", "Point")); !ok || got != id {
		t.Fatal("ByKey")
	}
	if !isICE(func() { w.items.AddItem(&Item{Key: ItemKey("main", "Point")}) }) {
		t.Error("duplicate key accepted")
	}
	if !isICE(func() { w.items.AddItem(&Item{}) }) {
		t.Error("zero key accepted")
	}
	if !isICE(func() { w.items.Get(NoItem) }) {
		t.Error("Get(NoItem)")
	}
}

// TestConstCanonicalForm: consts intern in one canonical form, deep
// values included.
func TestConstCanonicalForm(t *testing.T) {
	ct := NewConstTable()
	w := newWorld()
	arr := w.tab.Array(ConstArg{Kind: CAValue, Value: 1}, U8)
	pointee := ct.Intern(ConstValue{Type: arr, Align: 1, Bytes: []byte("hi")})
	v := ConstValue{Type: Str, Align: 8, Bytes: []byte{1, 2, 3, 4, 5, 6, 7, 8, 2, 0, 0, 0, 0, 0, 0, 0},
		Relocs: []Reloc{{Offset: 0, Kind: RelocData, Data: pointee}}}
	if err := v.Canonicalize(8); err != nil {
		t.Fatal(err)
	}
	if LoadUint(v.Bytes, 8) != 0 || LoadUint(v.Bytes[8:], 8) != 2 {
		t.Fatal("pointer word not zeroed or length damaged")
	}
	a, b := ct.Intern(v), ct.Intern(v)
	if a != b || ct.Get(a).Relocs[0].Data != pointee {
		t.Fatal("equal values interned twice")
	}
	other := ct.Intern(ConstValue{Type: arr, Align: 1, Bytes: []byte("ho")})
	v2 := v
	v2.Relocs = []Reloc{{Offset: 0, Kind: RelocData, Data: other}}
	if ct.Intern(v2) == a {
		t.Fatal("a different pointee must give a different value (deep)")
	}
	bad := ConstValue{Bytes: make([]byte, 12), Relocs: []Reloc{{Offset: 8, Kind: RelocData, Data: pointee}, {Offset: 0, Kind: RelocFn}}}
	if err := bad.Canonicalize(8); err == nil {
		t.Fatal("reloc past the end accepted")
	}
	over := ConstValue{Bytes: make([]byte, 16), Relocs: []Reloc{{Offset: 4, Kind: RelocFn}, {Offset: 0, Kind: RelocFn}}}
	if err := over.Canonicalize(8); err == nil {
		t.Fatal("overlap accepted")
	}
	if !isICE(func() { ct.Intern(ConstValue{Bytes: make([]byte, 8), Relocs: []Reloc{{Kind: RelocData, Data: 77}}}) }) {
		t.Fatal("reloc to unknown const accepted")
	}
	b2 := make([]byte, 8)
	StoreUint(b2, 4, 0xdeadbeef)
	if LoadUint(b2, 4) != 0xdeadbeef || b2[0] != 0xef {
		t.Fatal("little-endian helpers")
	}
}

// TestReadSetCanonical: insertion order never changes the sorted reads or the
// hash; repeated reads are recorded once.
func TestReadSetCanonical(t *testing.T) {
	reads := []Read{
		{Kind: ReadSignature, Key: ItemKey("a", "F")},
		{Kind: ReadNoItem, Key: ModuleKey("b"), Name: "G"},
		{Kind: ReadNoImpl, Key: ItemKey("std", "Eq"), Name: "main.Point"},
		{Kind: ReadTarget, Name: "os", Hash: source.HashString("v", "Windows")},
		{Kind: ReadValue, Key: ItemKey("a", "N"), Hash: source.HashString("v", "1")},
	}
	var s1, s2 ReadSet
	for _, r := range reads {
		s1.Add(r)
	}
	for i := len(reads) - 1; i >= 0; i-- {
		s2.Add(reads[i])
		s2.Add(reads[i])
	}
	if s1.Len() != len(reads) || s2.Len() != len(reads) {
		t.Fatalf("lengths %d %d", s1.Len(), s2.Len())
	}
	if s1.Hash() != s2.Hash() || !slices.Equal(s1.Sorted(), s2.Sorted()) {
		t.Fatal("order changed the read set")
	}
	if !ReadNoImpl.IsNegative() || ReadImpl.IsNegative() {
		t.Error("negative kinds")
	}
	if !isICE(func() { s1.Add(Read{}) }) {
		t.Error("zero read accepted")
	}
}

// TestReadSetPastTheScan: a set that grows past readSetScan (the size up
// to which Add scans instead of using a map) still records each read once, in
// any order, with Sorted and Hash equal to a set built in another order; a
// repeat of a read added before the switch is still ignored after it, and
// Merge of two large sets deduplicates across them.
func TestReadSetPastTheScan(t *testing.T) {
	var reads []Read
	for i := range 40 {
		reads = append(reads, Read{Kind: ReadKind(1 + i%int(numReadKinds-1)), Key: ItemKey("m", fmt.Sprintf("F%d", i)),
			Name: fmt.Sprintf("n%d", i%3), Hash: source.HashString("v", fmt.Sprint(i))})
	}
	var a, b ReadSet
	for i, r := range reads {
		a.Add(r)
		a.Add(reads[i/2]) // a repeat of an earlier read, before and after the switch
	}
	for i := len(reads) - 1; i >= 0; i-- {
		b.Add(reads[i])
		b.Add(reads[i])
	}
	if a.Len() != len(reads) || b.Len() != len(reads) {
		t.Fatalf("lengths %d %d, want %d", a.Len(), b.Len(), len(reads))
	}
	want := slices.Clone(reads)
	slices.SortFunc(want, Read.Compare)
	if !slices.Equal(a.Sorted(), want) || !slices.Equal(b.Sorted(), want) {
		t.Fatal("Sorted differs from the reads in canonical order")
	}
	if a.Hash() != b.Hash() {
		t.Fatal("insertion order changed the hash")
	}
	for _, r := range reads[:readSetScan] {
		a.Add(r)
	}
	if a.Len() != len(reads) {
		t.Fatalf("a read from before the switch was added again: %d", a.Len())
	}
	var c, d ReadSet
	for _, r := range reads[:30] {
		c.Add(r)
	}
	for _, r := range reads[10:] {
		d.Add(r)
	}
	c.Merge(&d)
	if c.Len() != len(reads) || !slices.Equal(c.Sorted(), want) {
		t.Fatalf("Merge: %d reads", c.Len())
	}
	var small ReadSet
	for _, r := range reads[:readSetScan] {
		small.Add(r)
		small.Add(r)
	}
	if small.Len() != readSetScan || small.seen != nil {
		t.Fatalf("a set of %d reads: len %d, map %v", readSetScan, small.Len(), small.seen != nil)
	}
}

// BenchmarkReadSetSmall adds eight distinct reads, each twice, to a fresh set:
// the common size of a function's read set.
func BenchmarkReadSetSmall(b *testing.B) {
	var reads []Read
	for i := range 8 {
		reads = append(reads, Read{Kind: ReadSignature, Key: ItemKey("m", fmt.Sprintf("F%d", i))})
	}
	b.ReportAllocs()
	for b.Loop() {
		var s ReadSet
		for _, r := range reads {
			s.Add(r)
			s.Add(r)
		}
	}
}

// TestPanicCodesAreFrozen pins the numbering the runtime shares.
func TestPanicCodesAreFrozen(t *testing.T) {
	want := []PanicCode{PanicOverflow, PanicBounds, PanicDivideByZero, PanicShiftAmount, PanicCast, PanicMust,
		PanicAssert, PanicExplicit, PanicStackLimit, PanicStrBoundary, PanicFreeNonEmpty, PanicWrongAllocator,
		PanicEnumTag, PanicContract, PanicUnreachable, PanicSuspendInCallback, PanicUnresolved}
	for i, c := range want {
		if int(c) != i+1 {
			t.Errorf("%v is %d, want %d", c, c, i+1)
		}
	}
	if NumPanicCodes != len(want)+1 {
		t.Errorf("%d codes", NumPanicCodes)
	}
}

func TestTargets(t *testing.T) {
	w, ok := TargetByName("x86_64-windows")
	if !ok || w.LLVMTriple != "x86_64-pc-windows-msvc" || w.OS != OSWindows || !w.Built {
		t.Fatalf("%+v", w)
	}
	for _, tg := range Targets() {
		if tg.PointerBytes != 8 {
			t.Errorf("%s pointer bytes %d", tg.Name, tg.PointerBytes)
		}
	}
	if ArchX64 != 0 || ArchArm64 != 1 || OSLinux != 0 || OSWindows != 1 || OSMacos != 2 {
		t.Error("Arch and Os must follow the prelude enums' order")
	}
	if !Dev.ContractsChecked() || Release.ContractsChecked() {
		t.Error("contracts per profile")
	}
	// Checked comes last, keeps contracts and optimizes.
	if Dev != 0 || TestProfile != 1 || Release != 2 || Checked != 3 || Checked.String() != "checked" {
		t.Error("profiles renumbered or Checked misnamed")
	}
	if !Checked.ContractsChecked() || !Checked.Optimized() || !Release.Optimized() || Dev.Optimized() || TestProfile.Optimized() {
		t.Error("Checked keeps contracts and optimizes; only Release and Checked optimize")
	}
}

func TestSignatureAccessors(t *testing.T) {
	w := newWorld()
	nf := w.item("std.fs", "NotFound", KindError)
	s := Signature{Receiver: ByShared, Params: []Param{{Name: source.SymSelf, Type: w.tab.Ref(w.tab.SelfType())},
		{Name: w.in.Intern("x"), Type: I32, Into: 1}}, Result: Bool, Errors: w.tab.ErrorSet([]ErrorMember{{Item: nf}})}
	if s.ParamByName(w.in.Intern("x")) != 2 || s.ParamByName(w.in.Intern("y")) != NoParam {
		t.Error("ParamByName")
	}
	if s.Param(2).Into != 1 {
		t.Error("Param")
	}
	if got := w.tab.TypeString(s.ReturnType(w.tab), nil); got != "NotFound!bool" {
		t.Errorf("return type %q", got)
	}
	if !isICE(func() { s.Param(NoParam) }) {
		t.Error("Param(NoParam)")
	}
}
