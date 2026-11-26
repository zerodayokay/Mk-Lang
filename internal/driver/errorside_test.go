// Why: a borrow or a write of the error side of an E!T must reach the
// union's own bytes: a copy would leave a returned reference dangling and
// a write lost.
// What: TestErrorSideBorrowsInPlace.
// How: builds errSideSrc for the host in dev and release, runs it, and
// reads each borrowing function's IR for a slot of the error set.

package driver

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"mk/internal/types"
)

// errSideSrc takes the error side of F|F2!int (F2's u8 beside an int ok
// payload) and of G|W!u8 apart by every form: a payload field and the
// whole error, shared and returned through from r or a struct, under
// ok(..) and some(..), on a call, written through mut (a field, a nested
// field, the whole error replaced by another), behind a mut parameter and
// at compile time. Smash reuses the stack between taking a reference and
// reading it.
const errSideSrc = `
error E
error F
error F2(code: u8)
error G(a: u8, b: u16)
error W(w: u64)

Get[T: Plain](x: &T, bad: bool): E!T {
    if bad {
        fail E
    }
    return *x
}

Mk(n: int): F|F2!int {
    if n == -1 {
        fail F
    }
    if n < 0 {
        fail F2(code: Cast[u8](-n))
    }
    return n
}

MkG(n: int): G|W!u8 {
    if n == -1 {
        fail W(w: 77)
    }
    if n < 0 {
        fail G(a: Cast[u8](-n), b: 300)
    }
    return Cast[u8](n)
}

Wrap[T: Plain](x: &T): ?T {
    return some(*x)
}

struct View {
    c: &u8
}

// A field of a payload error, shared, through from r.
Pick(r: &(F|F2!int), d: &u8): &u8 from r, d {
    return match r {
        err(F2(code: c)) { c }
        _ { d }
    }
}

// The whole error side, shared.
Whole(r: &(F|F2!int)): ?&(F|F2) from r {
    return match r {
        err(e) { some(e) }
        _ { none }
    }
}

// Through a returned struct holding the view.
Viewed(r: &(F|F2!int), d: &u8): View from r, d {
    return match r {
        err(F2(code: c)) { View(c: c) }
        _ { View(c: d) }
    }
}

// Nested under ok(...).
PickOk(r: &(E!(F|F2!int)), d: &u8): &u8 from r, d {
    return match r {
        ok(err(F2(code: c))) { c }
        _ { d }
    }
}

// Nested under ?T.
PickSome(r: &?(F|F2!int), d: &u8): &u8 from r, d {
    return match r {
        some(err(F2(code: c))) { c }
        _ { d }
    }
}

// Two fields of a payload beside a u8 ok side and a u64 error.
PickG(r: &(G|W!u8), d: &u16): &u16 from r, d {
    return match r {
        err(G(a: _, b: b)) { b }
        _ { d }
    }
}

PickW(r: &(G|W!u8), d: &u64): &u64 from r, d {
    return match r {
        err(W(w: w)) { w }
        _ { d }
    }
}

Smash(n: int): int {
    var a: [16]int = [n, n, n, n, n, n, n, n, n, n, n, n, n, n, n, n]
    var t := 0
    for x in a {
        t += x
    }
    return t
}

// A field written through mut, stored and nested.
MutField(n: int): int {
    var s: F|F2!int = Mk(n)
    match mut s {
        err(F2(code: c)) { *c = 9 }
        _ {}
    }
    return match s {
        err(F2(code: c)) { int(c) }
        _ { 0 }
    }
}

MutNested(n: int): int {
    var a: F|F2!int = Mk(n)
    var r: E!(F|F2!int) = Get(&a, false)
    match mut r {
        ok(err(F2(code: c))) { *c = 9 }
        _ {}
    }
    return match r {
        ok(err(F2(code: c))) { int(c) }
        _ { 0 }
    }
}

MutSome(n: int): int {
    var a: F|F2!int = Mk(n)
    var o: ?(F|F2!int) = Wrap(&a)
    match mut o {
        some(err(F2(code: c))) { *c = 11 }
        _ {}
    }
    return match o {
        some(err(F2(code: c))) { int(c) }
        _ { 0 }
    }
}

// The whole error side through mut: another error written in its place.
MutWhole(n: int): int {
    var s: G|W!u8 = MkG(n)
    match mut s {
        err(e) { *e = W(w: 123456789012) }
        _ {}
    }
    return match s {
        err(W(w: w)) { Cast[int](w) }
        err(G(a: a, b: b)) { int(a) + int(b) }
        ok(v) { int(v) }
    }
}

// Behind a mut parameter.
Bump(r: mut (G|W!u8)) {
    match r {
        err(G(a: a, b: b)) {
            *a = *a + 1
            *b = *b + 1
        }
        _ {}
    }
}

const Folded: int = MutField(-2) + MutNested(-2) * 100

export Main(sys: &System): WriteError!void {
    var s: F|F2!int = Mk(-7)
    var d: u8 = 1
    p := Pick(&s, &d)
    _ = Smash(255)
    try sys.Print("pick {x}\n", (x: *p))

    w := Whole(&s)
    _ = Smash(254)
    wn := match w {
        some(e) { match e {
            F2(code: c) { int(*c) }
            F { -1 }
        } }
        none { 0 }
    }
    try sys.Print("whole {x}\n", (x: wn))

    v := Viewed(&s, &d)
    _ = Smash(253)
    try sys.Print("view {x}\n", (x: *v.c))

    var r: E!(F|F2!int) = Get(&s, false)
    q := PickOk(&r, &d)
    _ = Smash(252)
    try sys.Print("ok {x}\n", (x: *q))

    var s6: F|F2!int = Mk(-6)
    var o: ?(F|F2!int) = Wrap(&s6)
    so := PickSome(&o, &d)
    _ = Smash(251)
    try sys.Print("some {x}\n", (x: *so))

    var g: G|W!u8 = MkG(-5)
    var d16: u16 = 1
    pg := PickG(&g, &d16)
    _ = Smash(250)
    try sys.Print("g {x}\n", (x: *pg))

    var gw: G|W!u8 = MkG(-1)
    var d64: u64 = 1
    pw := PickW(&gw, &d64)
    _ = Smash(249)
    try sys.Print("w {x}\n", (x: *pw))

    try sys.Print("call {x}\n", (x: match Mk(-3) {
        err(F2(code: c)) { int(c) }
        _ { 0 }
    }))

    try sys.Print("mut {a} {b} {c} {d} {e}\n", (a: MutField(-2), b: MutNested(-2), c: MutSome(-2), d: MutWhole(-4), e: MutWhole(3)))
    Bump(mut g)
    try sys.Print("bump {x}\n", (x: match g {
        err(G(a: a, b: b)) { int(a) * 1000 + int(b) }
        _ { 0 }
    }))
    try sys.Print("const {x}\n", (x: Folded))
}
`

// TestErrorSideBorrowsInPlace: the program prints the values it stored in
// both profiles, and no function that returns a borrow of an error side
// has a slot of the error set F|F2 (8 bytes, align 4) to copy it into.
func TestErrorSideBorrowsInPlace(t *testing.T) {
	t.Parallel()
	tools := needTools(t)
	target := HostTarget()
	const want = "pick 7\nwhole 7\nview 7\nok 7\nsome 6\ng 300\nw 77\ncall 3\n" +
		"mut 9 9 11 123456789012 3\nbump 6301\nconst 909\n"
	setSlot := regexp.MustCompile(`alloca \[8 x i8\], align 4\b`)
	for _, prof := range []types.Profile{types.Dev, types.Release} {
		t.Run(prof.String(), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			file := filepath.Join(dir, "main.mk")
			if err := os.WriteFile(file, []byte(errSideSrc), 0o644); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var ll strings.Builder
			cfg := Config{Root: file, Target: target, Profile: prof, CacheDir: t.TempDir(), Tools: tools}
			cfg.TestHooks.OnLL = func(_, text string) { mu.Lock(); ll.WriteString(text); mu.Unlock() }
			s, err := NewSession(cfg)
			if err != nil {
				t.Fatal(err)
			}
			r, paths, err := s.Build(filepath.Join(dir, "main"+exeSuffix(target)))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Report.Diags) > 0 || len(paths) != 1 {
				t.Fatalf("build:\n%s", r.Report.Text(r.Sources))
			}
			out, errOut, code := runExe(t, target, paths[0], "")
			if code != 0 || out != want {
				t.Errorf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, want)
			}
			text := ll.String()
			for _, fn := range []string{"Pick", "Whole", "Viewed", "PickOk", "PickSome"} {
				body := llFunc(text, "@mk.main."+fn+"(")
				if body == "" {
					t.Fatalf("no %s in the IR", fn)
				}
				// Viewed copies the View it returns, never the set.
				if setSlot.MatchString(body) || fn != "Viewed" && strings.Contains(body, "memcpy") {
					t.Errorf("%s copies the error side:\n%s", fn, body)
				}
			}
		})
	}
}

// llFunc is the text of the definition whose first line holds sig, up to
// its closing brace, or "".
func llFunc(ll, sig string) string {
	for _, part := range strings.Split(ll, "\ndefine ")[1:] {
		head, _, _ := strings.Cut(part, "\n")
		if strings.Contains(head, sig) {
			body, _, _ := strings.Cut(part, "\n}\n")
			return body
		}
	}
	return ""
}
