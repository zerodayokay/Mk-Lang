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
}
