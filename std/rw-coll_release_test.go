// Why: B-tree Set and Replace release keys and values, so a K or V that cannot
// be released must be refused at the call.
// What: TestRwColl_BTreeSetAndReplaceBounds over HeapBTree and the arena
// BTree.
// How: HeapBTree.Set needs K and V not linear or Free, Replace needs K so; the
// arena BTree's Set and Replace need K and V not linear. E0701 only at marked
// calls.

package std

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rwCollSetProgram marks each call check must refuse with a trailing
// "// E0701", and "// E0701 X" where the diagnostic names the alternative X.
const rwCollSetProgram = `import std.coll

linear struct Tok {
    n: int
}

impl Ord for Tok {
    Less(&self, other: &Tok): bool {
        return self.n < other.n
    }
}

linear struct Res {
    n: int
}

impl Free for Res {
    Free(self, a: &Alloc) {
        Res(n: _) := self
    }
}

impl Ord for Res {
    Less(&self, other: &Res): bool {
        return self.n < other.n
    }
}

Burn(t: Tok) {
    Tok(n: _) := t
}

BurnAll(o: ?Tok) {
    if t := o {
        Burn(t)
    }
}

HeapSet(a: &Alloc) {
    var u := coll.HeapBTree[int, Tok].New(a)
    x := Tok(n: 1)
    u.Set(a, 1, x) else { // E0701 Replace
        Burn(x)
    }
    u.FreeEmpty(a)
    var w := coll.HeapBTree[Tok, int].New(a)
    k := Tok(n: 1)
    w.Set(a, k, 1) else { // E0701 Replace
        Burn(k)
    }
    w.FreeEmpty(a)
    var f := coll.HeapBTree[int, Res].New(a)
    r := Res(n: 1)
    f.Set(a, 1, r) else {
        r.Free(a)
    }
    f.Free(a)
    var p := coll.HeapBTree[int, int].New(a)
    p.Set(a, 1, 1) else {
    }
    p.Free(a)
}

HeapReplace(a: &Alloc) {
    var u := coll.HeapBTree[int, Tok].New(a)
    BurnAll(u.Replace(a, 1, Tok(n: 1)).Must())
    u.FreeEmpty(a)
    var w := coll.HeapBTree[Tok, int].New(a)
    _ = w.Replace(a, Tok(n: 1), 1).Must() // E0701
    w.FreeEmpty(a)
    var f := coll.HeapBTree[Res, int].New(a)
    _ = f.Replace(a, Res(n: 1), 1).Must()
    f.Free(a)
}

ArenaSet(ar: &Arena) {
    var t := coll.BTree[int, Tok].New(ar)
    x := Tok(n: 1)
    t.Set(1, x) else { // E0701 HeapBTree
        Burn(x)
    }
    var s := coll.BTree[Tok, int].New(ar)
    k := Tok(n: 1)
    s.Set(k, 1) else { // E0701 HeapBTree
        Burn(k)
    }
    var f := coll.BTree[int, Res].New(ar)
    r := Res(n: 1)
    f.Set(1, r) else { // E0701 HeapBTree
        r.Free(ar.Alloc())
    }
    var p := coll.BTree[int, int].New(ar)
    p.Set(1, 1) else {
    }
}

ArenaReplace(ar: &Arena) {
    var t := coll.BTree[int, Tok].New(ar)
    BurnAll(t.Replace(1, Tok(n: 1)).Must()) // E0701 HeapBTree
    var s := coll.BTree[Tok, int].New(ar)
    _ = s.Replace(Tok(n: 1), 1).Must() // E0701 HeapBTree
    var g := coll.BTree[Res, int].New(ar)
    _ = g.Replace(Res(n: 1), 1).Must() // E0701 HeapBTree
}

GenericHeapSet[V](a: &Alloc, u: mut coll.HeapBTree[int, V], v: V) {
    u.Set(a, 1, v) else { // E0701 Replace
        return
    }
}

GenericArenaSet[V](t: mut coll.BTree[int, V], v: V) {
    t.Set(1, v) else { // E0701 HeapBTree
        return
    }
}

export Main(sys: &System) {
}
`

var rwCollDiagHead = regexp.MustCompile(`^main\.mk:(\d+):\d+: (E\d{4}) `)

// rwCollDiags maps a line of main.mk to its diagnostics' codes, each with
// " try: X" appended for every alternative the text output names.
func rwCollDiags(out string) map[int][]string {
	got := map[int][]string{}
	line, idx := 0, -1
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if m := rwCollDiagHead.FindStringSubmatch(l); m != nil {
			line, _ = strconv.Atoi(m[1])
			got[line] = append(got[line], m[2])
			idx = len(got[line]) - 1
			continue
		}
		if t, ok := strings.CutPrefix(strings.TrimSpace(l), "try: "); ok && idx >= 0 {
			got[line][idx] += " try: " + t
		}
	}
	return got
}

// TestRwColl_BTreeSetAndReplaceBounds: E0701 at exactly the marked
// calls, naming the marked alternative, and nothing elsewhere: the heap
// tree's Set (K and V not linear or Free, naming Replace), the heap tree's
// Replace (K), the arena tree's Set and Replace (K and V not linear, a Free
// type included, naming HeapBTree, since the arena tree holds no linear
// value), and a generic Set whose V may be linear.
func TestRwColl_BTreeSetAndReplaceBounds(t *testing.T) {
	t.Parallel()
	out := checkUncapped(t, rwCollSetProgram)
	want := map[int]string{}
	for i, l := range strings.Split(rwCollSetProgram, "\n") {
		if _, m, ok := strings.Cut(l, "// E0701"); ok {
			want[i+1] = "E0701"
			if alt := strings.TrimSpace(m); alt != "" {
				want[i+1] = "E0701 try: " + alt
			}
		}
	}
	if len(want) != 11 {
		t.Fatalf("fixture: %d marked calls, want 11", len(want))
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
