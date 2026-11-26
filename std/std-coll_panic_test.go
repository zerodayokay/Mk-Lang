// Why: std.coll's panics end the process, so Mk test blocks cannot observe
// them; each must exit 101 with its message and print nothing after.
// What: TestSTDCOLL_Panics (codes 2, 8, 11, 12) and TestSTDCOLL_BTreeSetLinearIsE0701
// (B-tree Sets of linear values that are not Free, refused with E0701).
// How: one program with a mode per call, run at dev and release; the calls
// next to each panic that must not panic print "after".

package std

import (
	"strings"
	"testing"
)

const c2PanicProgram = `import std.coll

linear struct Tok {
    n: int
}

impl Ord for Tok {
    Less(&self, other: &Tok): bool {
        return self.n < other.n
    }
}

Burn(t: Tok) {
    Tok(n: _) := t
}

// Res is linear and Free: HeapBTree.Set takes a V that is plain or Free
// (E0701 otherwise), so the heap tree's linear cases hold Res.
linear struct Res {
    n: int
}

impl Free for Res {
    Free(self, a: &Alloc) {
        Res(n: _) := self
    }
}

Bits(sys: &System, which: str): OutOfMemory!void {
    var b := try coll.Bitset.New(sys.arena, 65)
    if which == "Bitset.Set.neg" {
        b.Set(-1)
    } else if which == "Bitset.Set.len" {
        b.Set(65)
    } else if which == "Bitset.Remove.len" {
        b.Remove(65)
    } else if which == "Bitset.Has.len" {
        _ = b.Has(65)
    } else if which == "Bitset.New.neg" {
        _ = try coll.Bitset.New(sys.arena, -1)
    } else if which == "Bitset.edges" {
        b.Set(0)
        b.Set(64)
        b.Remove(0)
        if !b.Has(64) || b.Has(0) || b.Count() != 1 {
            b.Set(1000)
        }
        _ = try coll.Bitset.New(sys.arena, 0)
    }
}

Linear(sys: &System, which: str) {
    h := sys.heap.Alloc()
    if which == "BTree.GetMut.linear" {
        // COLL2-2: GetMut updates a linear value in place (Replace fills
        // a tree of values that are not Free, whose Set takes none; an
        // arena tree holds no linear value); Has sees every key.
        var t := coll.HeapBTree[int, Tok].New(h)
        var u := coll.HeapBTree[int, Res].New(h)
        for i in 0..200 {
            if o := t.Replace(h, i, Tok(n: i)).Must() {
                Burn(o)
            }
            y := Res(n: i)
            u.Set(h, i, y) else {
                y.Free(h)
            }
        }
        for i in 0..200 {
            if v := t.GetMut(&i) {
                v.n += 1000
            }
            if v := u.GetMut(&i) {
                v.n += 2000
            }
        }
        var ok := true
        for i in 0..201 {
            if t.Has(&i) != (i < 200) || u.Has(&i) != (i < 200) {
                ok = false
            }
        }
        for i in 0..200 {
            if v := t.Remove(&i) {
                if v.n != i + 1000 {
                    ok = false
                }
                Burn(v)
            }
            if v := u.Remove(&i) {
                if v.n != i + 2000 {
                    ok = false
                }
                v.Free(h)
            }
        }
        if !ok || t.Len() != 0 || u.Len() != 0 {
            Assert(false)
        }
        t.FreeEmpty(h)
        u.FreeEmpty(h)
    } else if which == "HeapBTree.FreeEmpty.linear" {
        var u := coll.HeapBTree[int, Res].New(h)
        x := Res(n: 1)
        u.Set(h, 1, x) else {
            x.Free(h)
        }
        u.FreeEmpty(h)
    } else if which == "HeapBTree.FreeEmpty.emptied" {
        var u := coll.HeapBTree[int, Res].New(h)
        for i in 0..100 {
            x := Res(n: i)
            u.Set(h, i, x) else {
                x.Free(h)
            }
        }
        for i in 0..100 {
            if v := u.Remove(&i) {
                v.Free(h)
            }
        }
        u.FreeEmpty(h)
    } else if which == "HeapDeque.FreeEmpty.linear" {
        var q := coll.HeapDeque[Tok].New(h)
        x := Tok(n: 1)
        q.Push(h, x) else {
            Burn(x)
        }
        q.FreeEmpty(h)
    } else if which == "HeapDeque.FreeEmpty.emptied" {
        var q := coll.HeapDeque[Tok].New(h)
        x := Tok(n: 1)
        q.PushFront(h, x) else {
            Burn(x)
        }
        for y := q.Pop() {
            Burn(y)
        }
        q.FreeEmpty(h)
    }
}

Owner(sys: &System, which: str) {
    h := sys.heap.Alloc()
    w := sys.arena.Alloc()
    var q := coll.HeapDeque[int].New(h)
    var u := coll.HeapBTree[int, int].New(h)
    if which == "HeapDeque.Push" {
        q.Push(w, 1) else {
        }
    } else if which == "HeapDeque.PushFront" {
        q.PushFront(w, 1) else {
        }
    } else if which == "HeapDeque.Push.grow" {
        for i in 0..4 {
            q.Push(h, i) else {
            }
        }
        q.Push(w, 4) else {
        }
    } else if which == "HeapDeque.spare" {
        q.Push(h, 1) else {
        }
        q.Push(w, 2) else {
        }
        q.PushFront(w, 3) else {
        }
    } else if which == "HeapBTree.Set" {
        u.Set(w, 1, 1) else {
        }
    } else if which == "HeapBTree.Set.split" {
        // 31 keys fill the root leaf of an int tree (BtB 16).
        for i in 0..31 {
            u.Set(h, i, i) else {
            }
        }
        u.Set(w, 31, 31) else {
        }
    } else if which == "HeapBTree.spare" {
        u.Set(h, 1, 1) else {
        }
        u.Set(w, 2, 2) else {
        }
        u.Set(w, 1, 3) else {
        }
    } else if which == "HeapDeque.Free" {
        var r := coll.HeapDeque[int].New(h)
        r.Free(w)
    } else if which == "HeapBTree.Free" {
        var r := coll.HeapBTree[int, int].New(h)
        r.Free(w)
    }
    q.Free(h)
    u.Free(h)
}

export Main(sys: &System) {
    var out := sys.Stdout()
    which := sys.args[1]
    Print(mut out, "before\n") else {
        return
    }
    out.Flush() else {
        return
    }
    if which.StartsWith("Bitset") {
        Bits(sys, which) else {
            return
        }
    } else if which.StartsWith("Heap") && which.Find(".linear").IsNone() && which.Find(".emptied").IsNone() {
        Owner(sys, which)
    } else {
        Linear(sys, which)
    }
    Print(mut out, "after\n") else {
        return
    }
}
`

// TestSTDCOLL_Panics runs each mode of the program at dev and release and
// checks the exit code, the message and whether "after" was printed. Codes:
// 2 (Bitset bounds, negative length), 8 (a linear value would be dropped),
// 11 (FreeEmpty of a non-empty container), 12 (wrong allocator on a call
// that allocates or frees).
func TestSTDCOLL_Panics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode string
		msg  string // "" means no panic: exit 0 and "after"
	}{
		{"Bitset.Set.neg", "Bitset.Set: index out of range"},
		{"Bitset.Set.len", "Bitset.Set: index out of range"},
		{"Bitset.Remove.len", "Bitset.Remove: index out of range"},
		{"Bitset.Has.len", "Bitset.Has: index out of range"},
		{"Bitset.New.neg", "Bitset.New: negative length"},
		{"Bitset.edges", ""},
		{"BTree.GetMut.linear", ""},
		{"HeapBTree.FreeEmpty.linear", "HeapBTree.FreeEmpty of a non-empty tree"},
		{"HeapBTree.FreeEmpty.emptied", ""},
		{"HeapDeque.FreeEmpty.linear", "HeapDeque.FreeEmpty of a non-empty deque"},
		{"HeapDeque.FreeEmpty.emptied", ""},
		{"HeapDeque.Push", "wrong allocator: HeapDeque.Push"},
		{"HeapDeque.PushFront", "wrong allocator: HeapDeque.PushFront"},
		{"HeapDeque.Push.grow", "wrong allocator: HeapDeque.Push"},
		{"HeapDeque.spare", ""},
		{"HeapBTree.Set", "wrong allocator: HeapBTree.Set"},
		{"HeapBTree.Set.split", "wrong allocator: HeapBTree.Set"},
		{"HeapBTree.spare", ""},
		{"HeapDeque.Free", "wrong allocator: HeapDeque.Free"},
		{"HeapBTree.Free", "wrong allocator: HeapBTree.Free"},
	}
	for _, flags := range c2Flags {
		t.Run(c2FlagName(flags), func(t *testing.T) {
			t.Parallel()
			exe := collBuild(t, c2PanicProgram, flags...)
			for _, c := range cases {
				code, stdout, stderr := collOwnerRun(t, exe, c.mode)
				if !strings.HasPrefix(stdout, "before\n") {
					t.Fatalf("%s: stdout %q, want it to start with before", c.mode, stdout)
				}
				if c.msg == "" {
					if code != 0 || !strings.Contains(stdout, "after") {
						t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 0 and after", c.mode, code, stdout, stderr)
					}
					continue
				}
				if code != 101 || !strings.Contains(stderr, "panic: ") || !strings.Contains(stderr, c.msg) || strings.Contains(stdout, "after") {
					t.Errorf("%s: exit %d, stdout %q, stderr %q; want 101, no after, and %q", c.mode, code, stdout, stderr, c.msg)
				}
			}
		})
	}
}

// c2SetLinearProgram is the two former '.linear' Set rows of
// TestSTDCOLL_Panics, which once panicked with code 8: a B-tree's
// Set of a linear value that is not Free. Each Set marked "// E0701 X" must be
// refused at check, naming X: Replace for the heap tree, HeapBTree for the
// arena tree, whose Replace refuses a linear value too.
const c2SetLinearProgram = `import std.coll

linear struct Tok {
    n: int
}

Burn(t: Tok) {
    Tok(n: _) := t
}

ArenaSetLinear(sys: &System) {
    var t := coll.BTree[int, Tok].New(sys.arena)
    for i in 0..2 {
        x := Tok(n: i)
        t.Set(1, x) else { // E0701 HeapBTree
            Burn(x)
        }
    }
}

HeapSetLinear(sys: &System) {
    h := sys.heap.Alloc()
    var u := coll.HeapBTree[int, Tok].New(h)
    for i in 0..2 {
        x := Tok(n: i)
        u.Set(h, 1, x) else { // E0701 Replace
            Burn(x)
        }
    }
    one := 1
    if v := u.Remove(&one) {
        Burn(v)
    }
    u.FreeEmpty(h)
}

export Main(sys: &System) {
    ArenaSetLinear(sys)
    HeapSetLinear(sys)
}
`

// TestSTDCOLL_BTreeSetLinearIsE0701: the rows BTree.Set.linear and HeapBTree.Set.linear
// expect E0701 at their Sets, naming the alternative marked, and nothing else. They are a
// check, not a mode of the panic program, which must build. Free of a
// container of linear values that are not Free, Remove by a linear key, and a
// B-tree's Set of a linear key or value (not Free, for HeapBTree) are E0701 at
// check; the other such cases are std/rw-coll_release_test.go's.
func TestSTDCOLL_BTreeSetLinearIsE0701(t *testing.T) {
	t.Parallel()
	out := checkUncapped(t, c2SetLinearProgram)
	want := map[int]string{}
	for i, l := range strings.Split(c2SetLinearProgram, "\n") {
		if _, alt, ok := strings.Cut(l, "// E0701 "); ok {
			want[i+1] = "E0701 try: " + strings.TrimSpace(alt)
		}
	}
	if len(want) != 2 {
		t.Fatalf("fixture: %d marked Sets, want 2", len(want))
	}
	got := rwCollDiags(out)
	for n, w := range want {
		if len(got[n]) != 1 || got[n][0] != w {
			t.Errorf("main.mk:%d: got %q, want %q\n%s", n, got[n], w, out)
		}
	}
	for n, codes := range got {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected %v at main.mk:%d\n%s", codes, n, out)
		}
	}
}
