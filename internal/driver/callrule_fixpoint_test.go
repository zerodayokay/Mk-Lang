// Why: a call's flow clauses applied in one pass, in order, missed stores
// that a later clause makes reachable, and the programs read freed memory.
// What: tests that the call rule's steps 3 and 4 run to a fixpoint, so a
// clause applied early in a call reaches a cell a later clause of the same
// call makes reachable.
// How: each reversed form (d462Fill fills a template) must give its
// control's codes (d462Codes), and forwarding a parameter into an `into`
// callee stays clean (later rounds do not walk through the loans they
// store).

package driver

import (
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
)

func d462Codes(t *testing.T, src string) []diag.Code {
	t.Helper()
	r := checkText(t, src)
	var out []diag.Code
	for _, e := range r.Report.Diags {
		if !slices.Contains(out, e.Diag.Code) {
			out = append(out, e.Diag.Code)
		}
	}
	slices.Sort(out)
	return out
}

// d462Fill replaces each "@@" of f with the next of a.
func d462Fill(f string, a ...string) string {
	for _, s := range a {
		f = strings.Replace(f, "@@", s, 1)
	}
	return f
}

const d462Order = `F(l: mut List[&Cell[str]], @@): OutOfMemory!void {
    try l.Push(c)
    l[0].Store(x)
}

Yes(): bool {
    return true
}

export Main(sys: &System) {
    var ar := Arena.New(sys.heap) else { return }
    defer ar.Free()
    cell := Cell[str].New("init")
    if Yes() {
        var ar2 := Arena.New(sys.heap) else { return }
        defer ar2.Free()
        s := ar2.Dup("secret-old") else { return }
        var l := List[&Cell[str]].New(&ar)
        F(mut l, @@) else { return }
    }
    _ = cell.Load()
}
`

// TestOrderReversed: `x into l` before `c into l` gives the codes of
// the control with the clauses in chain order.
func TestOrderReversed(t *testing.T) {
	t.Parallel()
	ctl := d462Codes(t, d462Fill(d462Order, "c: &Cell[str] into l, x: str into l", "&cell, s"))
	if !slices.Contains(ctl, diag.E0505) {
		t.Fatalf("control: want E0505, got %v", ctl)
	}
	rev := d462Codes(t, d462Fill(d462Order, "x: str into l, c: &Cell[str] into l", "s, &cell"))
	if !slices.Equal(rev, ctl) {
		t.Errorf("reversed clauses: got %v, want the control's %v", rev, ctl)
	}
}

// TestStep3BeforeStep4: `x into l` then `*q into l`: the cell q's
// list holds becomes reachable from l in step 4, and x reaches it in the
// next round.
func TestStep3BeforeStep4(t *testing.T) {
	t.Parallel()
	got := d462Codes(t, `F(l: mut List[&Cell[str]], x: str into l, q: &List[&Cell[str]]) *q into l {
    l.Push(q[0]) else { return }
    l[0].Store(x)
}

Yes(): bool {
    return true
}

export Main(sys: &System) {
    var ar := Arena.New(sys.heap) else { return }
    defer ar.Free()
    cell := Cell[str].New("init")
    var cells := List[&Cell[str]].New(&ar)
    cells.Push(&cell) else { return }
    if Yes() {
        var ar2 := Arena.New(sys.heap) else { return }
        defer ar2.Free()
        s := ar2.Dup("secret-old") else { return }
        var l := List[&Cell[str]].New(&ar)
        F(mut l, s, &cells)
    }
    _ = cell.Load()
}
`)
	if !slices.Contains(got, diag.E0505) {
		t.Errorf("step 3 before step 4: want E0505, got %v", got)
	}
}

const d462Chain = `struct W {
    b: mut &i32
}

Link(@@): OutOfMemory!void {
    try z.Push(x)
}

Store(z: mut List[W], v: &i32 into z) {
    *z[0].b = v
}

export Main(sys: &System) {
    keep: i32 = 0
    var r: &i32 = &keep
    var ar := Arena.New(sys.heap) else { return }
    defer ar.Free()
    var a2 := Arena.New(sys.heap) else { return }
    defer a2.Free()
    var z := List[W].New(&ar)
    k: i32 = 5
    Link(@@) else { return }
    n1: i32 = 2011
    pv := a2.Put(n1) else { return }
    Store(mut z, &*pv)
    a2.Reset()
    _ = *r
}
`

// TestChainReversed: `y into z, x into y` gives the codes
// of `x into z` written directly.
func TestChainReversed(t *testing.T) {
	t.Parallel()
	ctl := d462Codes(t, d462Fill(d462Chain, "x: W into z, y: &i32 into z, z: mut List[W]", "W(b: mut r), &k, mut z"))
	if len(ctl) == 0 {
		t.Fatalf("control: want a rejection")
	}
	rev := d462Codes(t, d462Fill(d462Chain, "y: &i32 into z, x: W into y, z: mut List[W]", "&k, W(b: mut r), mut z"))
	if !slices.Equal(rev, ctl) {
		t.Errorf("reversed chain: got %v, want the control's %v", rev, ctl)
	}
}

// TestForwardStaysClean: a parameter forwarded into an `into` callee
// is not stored into its own referents by a later round (self-reach).
func TestForwardStaysClean(t *testing.T) {
	t.Parallel()
	got := d462Codes(t, `Add[T](a: &Alloc, l: mut HeapList[T], x: T into l): OutOfMemory!void {
    try l.Push(a, x)
}

Rep[T](l: mut T, x: T into l): T {
    return Replace(l, x)
}

AddRef(l: mut List[&Cell[i32]], c: &Cell[i32] into l): OutOfMemory!void {
    try l.Push(c)
}

export Main(sys: &System) {
    var ar := Arena.New(sys.heap) else { return }
    defer ar.Free()
    cell := Cell[i32].New(1)
    var l := List[&Cell[i32]].New(&ar)
    AddRef(mut l, &cell) else { return }
    _ = cell.Load()
}
`)
	if len(got) != 0 {
		t.Errorf("forwarding: want clean, got %v", got)
	}
}

// TestStoreSelfTwice: two calls of a `*self into self` method on a
// value holding an &Arena parameter (tests/bench/idiomatic/gzip's
// Inflater.Read in a loop). A later round reads what self's referent holds,
// not what the arena behind the stored &Arena holds, so the second call
// stores no loan of the arena's contents into the arena.
func TestStoreSelfTwice(t *testing.T) {
	t.Parallel()
	got := d462Codes(t, `struct Z {
    a: &Arena
    n: int
}

Z.Read(mut self): int *self into self {
    self.n = self.n + 1
    return self.n
}

Round(a: &Arena): int {
    var z := Z(a: a, n: 0)
    var sum := 0
    for {
        n := z.Read()
        if n > 3 {
            break
        }
        sum = sum + n
    }
    return sum + z.Read()
}
`)
	if len(got) != 0 {
		t.Errorf("two *self into self calls: want clean, got %v", got)
	}
}
