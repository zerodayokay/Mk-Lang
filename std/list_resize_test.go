// Why: a generic `[T: Plain]` body cannot Push one value into a List twice
// (the second call stores it through the copies already there, E0604), so
// List.Resize is how it fills a list with copies of one value.
// What: TestListResize.
// How: one program fills lists of u32, u64, a plain struct and str through
// one generic helper, grows, shrinks and refills them, and prints them; a
// negative length panics with code 2.

package std

import (
	"strings"
	"testing"
)

// TestListResize: Resize keeps the first n elements and appends copies of
// x up to n, through a generic caller that declares `val: T into l`; a
// negative n is a bounds panic (code 2, exit 101).
func TestListResize(t *testing.T) {
	t.Parallel()
	r := runProgram(t, `struct P {
    a: u8
    b: i64
}

Refill[T: Plain](l: mut List[T], n: int, val: T into l): OutOfMemory!void {
    l.Clear()
    try l.Resize(n, val)
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var out := sys.Stdout()
    var w := List[u32].New(sys.arena)
    try Refill(mut w, 3, u32(7))
    try w.Push(1)
    try w.Resize(6, 9)
    ws := w.Items()
    try Print(mut out, "{ws}\n", (ws))
    try w.Resize(2, 5)
    w2 := w.Items()
    try Print(mut out, "{w2}\n", (w2))
    var b := List[u64].New(sys.arena)
    for k in 0..3 {
        try Refill(mut b, 100 * k, Cast[u64](k))
    }
    n := b.Len()
    first := b.Items()[0]
    last := b.Items()[199]
    try Print(mut out, "{n} {first} {last}\n", (n, first, last))
    var ps := List[P].New(sys.arena)
    try Refill(mut ps, 2, P(a: 1, b: -2))
    pn := ps.Len()
    pb := ps.Items()[1].b
    try Print(mut out, "{pn} {pb}\n", (pn, pb))
    var rs := List[str].New(sys.arena)
    try Refill(mut rs, 4, "ab")
    rn := rs.Len()
    rv := rs.Items()[3]
    try Print(mut out, "{rn} {rv}\n", (rn, rv))
    try out.Flush()
    try w.Resize(-1, 0)
}
`)
	wantRun(t, r, 101, "[7, 7, 7, 1, 9, 9]\n[7, 7]\n200 2 2\n2 -2\n4 ab\n")
	if r.code == 101 && !strings.Contains(r.stderr, "List.Resize: negative length") {
		t.Errorf("stderr does not name the panic:\n%s", r.stderr)
	}
}
