// Why: real engine passes combine `plain view struct` handles, store-disjoint
// split `mut self` methods, and bundled `CallCx` structs across source-level Mk
// modules.
//
// What: end-to-end driver tests verifying zero false borrow conflicts for the
// combined patterns alongside negative soundness diagnostics.
//
// How: checks full Mk programs via driver and asserts expected clean passes or
// exact error codes.

package driver

import "testing"

func TestBundledCtxAndDisjointSplit(t *testing.T) {
	t.Parallel()
	t.Run("plain view handles with bundled CallCx and disjoint split self", func(t *testing.T) {
		t.Parallel()
		src := `plain view struct LocalRef {
    idx: u32
}

struct GcHeap {
    bytes: int
}

struct Roots {
    count: int
}

struct Realm {
    id: int
}

struct CallCx {
    heap: mut GcHeap
    roots: &Roots
    realm: &Realm
}

Alloc(h: mut GcHeap, r: &Roots): LocalRef from r {
    h.bytes = h.bytes +% 8
    return LocalRef(idx: Trunc[u32](r.count))
}

UseLocal(v: LocalRef): int {
    return int(v.idx)
}

Step(cx: mut CallCx): int {
    a := Alloc(mut *cx.heap, cx.roots)
    b := Alloc(mut *cx.heap, cx.roots)
    return UseLocal(a) +% UseLocal(b)
}

struct Arena {
    top: int
}

Arena.Reset(mut self) {
    self.top = 0
}

struct Graph {
    edges: int
}

struct Pass {
    graph: &Graph
    scratch: &Arena
    steps: int
}

Pass.Advance(mut self) {
    self.steps = self.steps +% 1
}

Run(g: &Graph): int {
    var h := GcHeap(bytes: 0)
    var r := Roots(count: 1)
    var rlm := Realm(id: 2)
    var cx := CallCx(heap: mut h, roots: &r, realm: &rlm)
    s := Step(mut cx)
    var a := Arena(top: 4)
    var p := Pass(graph: g, scratch: &a, steps: s)
    p.Advance()
    gr := p.graph
    a.Reset()
    return p.steps +% gr.edges +% a.top
}

export Main(sys: &System): int {
    _ = sys
    var g := Graph(edges: 1)
    return Run(&g)
}
`
		if r := checkText(t, src); len(r.Report.Diags) != 0 {
			t.Fatalf("want clean, got:\n%s", diagLines(r))
		}
	})

	t.Run("store into cx.heap does not poison cx.realm return", func(t *testing.T) {
		t.Parallel()
		src := `struct Extra {
    tag: int
}

struct Slot {
    p: &Extra
}

struct GcHeap {
    slot: Slot
}

struct Roots {
    n: int
}

struct Realm {
    id: int
}

struct CallCx {
    heap: mut GcHeap
    roots: &Roots
    realm: &Realm
}

SaveExtra(h: mut GcHeap, ex: &Extra into h) {
    h.slot.p = ex
}

StepAndRealm(cx: mut CallCx, ex: &Extra into cx): &Realm from cx.realm {
    SaveExtra(mut *cx.heap, ex)
    return cx.realm
}

export Main(sys: &System) {
    _ = sys
}
`
		if r := checkText(t, src); len(r.Report.Diags) != 0 {
			t.Fatalf("want clean, got:\n%s", diagLines(r))
		}
	})

	t.Run("negative double mut heap and realm under roots", func(t *testing.T) {
		t.Parallel()
		src := `struct GcHeap {
    n: int
}

struct Roots {
    n: int
}

struct Realm {
    id: int
}

struct CallCx {
    heap: mut GcHeap
    roots: &Roots
    realm: &Realm
}

BadRealm(cx: &CallCx): &Realm from cx.roots {
    return cx.realm
}

Pair(a: mut GcHeap, b: mut GcHeap) {
    a.n = b.n
}

BadDouble(cx: mut CallCx) {
    Pair(mut *cx.heap, mut *cx.heap)
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		got := baseLines(r)
		want := []string{"main.mk:18:5:E0602", "main.mk:26:5:E0501"}
		_ = got
		if len(r.Report.Diags) != 2 {
			t.Fatalf("want E0602 and E0501, got:\n%s", diagLines(r))
		}
		c0 := r.Report.Diags[0].Diag.Code.String()
		c1 := r.Report.Diags[1].Diag.Code.String()
		if !((c0 == "E0602" && c1 == "E0501") || (c0 == "E0501" && c1 == "E0602")) {
			t.Fatalf("want E0602 and E0501, got %v (%v)", got, want)
		}
	})

	t.Run("soundness depth-3 Cell in mut field rejects stack store", func(t *testing.T) {
		t.Parallel()
		src := `struct Heap {
    c: &Cell[&i32]
}

struct CallCx {
    h1: mut Heap
}

struct Wrap {
    c: &Cell[&i32]
}

PutWrap(w: &Wrap, x: &i32 into w) {
    w.c.Store(x)
}

Bad(cx: mut CallCx) {
    w := Wrap(c: cx.h1.c)
    y: i32 = 42
    PutWrap(&w, &y)
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0505") {
			t.Fatalf("expected E0505 for depth-3 Cell store, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness sibling mut fields sharing depth-3 Cell conflict on E0501", func(t *testing.T) {
		t.Parallel()
		src := `struct Heap1 {
    c: &Cell[&i32]
}

struct Heap2 {
    c: &Cell[&i32]
}

struct CallCx {
    h1: mut Heap1
    h2: mut Heap2
}

BadAlias(cx: mut CallCx, list: mut List[i32] into cx): i32 {
    r := &list[0]
    cx.h1.c.Store(r)
    v := cx.h2.c.Load()
    list.Push(999) else { return *v }
    return *v
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0501") {
			t.Fatalf("expected E0501 for sibling mut fields aliasing depth-3 Cell, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness mut field behind shared &CallCx is non-exclusive", func(t *testing.T) {
		t.Parallel()
		src := `struct GcHeap {
    n: int
}

struct CallCx {
    h1: mut GcHeap
}

TakeMut(h: mut GcHeap) {
    h.n = 1
}

BadShared(cx: &CallCx) {
    TakeMut(mut *cx.h1)
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0507") {
			t.Fatalf("expected E0507 borrowing mut field through shared &CallCx, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness non-param root field path does not index unrelated n2F field", func(t *testing.T) {
		t.Parallel()
		src := `struct Other {
    n: i32
}

struct GcHeap {
    saved: ?&i32
}

struct Sub {
    heap: mut GcHeap
}

struct CallCx {
    info: &Other
    sub: Sub
}

Store(h: mut GcHeap, x: &i32 into h) {
    h.saved = some(x)
}

Bad(cx: mut CallCx) {
    s := mut cx.sub
    y: i32 = 42
    Store(mut *s.heap, &y)
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0505") {
			t.Fatalf("expected E0505 storing stack local through sub.heap, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness Sneaky caller opt rejects stale field return via E0505", func(t *testing.T) {
		t.Parallel()
		src := `struct Tag {
    val: &i32
}

struct Box {
    tag: &Tag
    next: ?&Box
}

GetNext(b: &Box): &Box from b {
    if nx := b.next {
        return nx
    }
    return b
}

GetTag(b: &Box): &Tag from b.tag {
    return b.tag
}

Sneaky(b: &Box): &Tag from b.tag {
    nx := GetNext(b)
    return GetTag(nx)
}

BadCaller(): i32 {
    long_val: i32 = 1
    long_tag := Tag(val: &long_val)
    var opt: ?&Tag = none
    if true {
        short_val: i32 = 2
        short_tag := Tag(val: &short_val)
        short_box := Box(tag: &short_tag, next: none)
        bx := Box(tag: &long_tag, next: some(&short_box))
        opt = some(Sneaky(&bx))
    }
    if r := opt {
        return *r.val
    }
    return 0
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0505") {
			t.Fatalf("expected E0505 in Sneaky caller opt, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness SneakyRoots bundled context rejects aliasing push via E0501", func(t *testing.T) {
		t.Parallel()
		src := `struct Roots {
    a: &i32
    b: ?&i32
}

struct CallCx {
    roots: mut Roots
}

SameRoots(r: &Roots): &Roots from r {
    return r
}

GetA(r: &Roots): &i32 from r.a {
    return r.a
}

SneakyRoots(r: &Roots): &i32 from r.a {
    same := SameRoots(r)
    return GetA(same)
}

SetB(r: mut Roots, x: &i32 into r) {
    r.b = some(x)
}

BadAlias(cx: mut CallCx, list: mut List[i32] into cx): i32 {
    r := &list[0]
    SetB(mut *cx.roots, r)
    v := SneakyRoots(&*cx.roots)
    list.Push(999) else { return *v }
    return *v
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if !hasDiagCode(r, "E0501") {
			t.Fatalf("expected E0501 for SneakyRoots bundled context alias, got:\n%s", diagLines(r))
		}
	})

	t.Run("soundness MutVarSub mutation through mut ref rejects E0602", func(t *testing.T) {
		t.Parallel()
		src := `struct Tag {
    val: &i32
}

struct PairBox {
    a: &Tag
    b: &Tag
}

TakeSub(t: &Tag): &i32 from t.val {
    return t.val
}

MutVarSub(p: &PairBox): &i32 from p.a {
    var r := p.a
    pr := mut r
    *pr = p.b
    return TakeSub(r)
}

export Main(sys: &System) {
    _ = sys
}
`
		r := checkText(t, src)
		if len(r.Report.Diags) != 1 || !hasDiagCode(r, "E0602") {
			t.Fatalf("expected single E0602 diagnostic, got:\n%s", diagLines(r))
		}
	})
}

func hasDiagCode(r Result, code string) bool {
	for _, d := range r.Report.Diags {
		if d.Diag.Code.String() == code {
			return true
		}
	}
	return false
}
