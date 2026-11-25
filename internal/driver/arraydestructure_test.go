// Why: an array destructure takes each element out of the array by a
// constant element step, which codegen and the evaluator place by the
// element's size; a wrong offset passes the checker and prints the wrong
// values.
// What: TestArrayDestructureRuns.
// How: a program takes linear and plain arrays apart at run time and in a
// const evaluated at compile time, through a generic function and a nested
// array, and is judged by what it prints.

package driver

import (
	"strings"
	"testing"
)

// TestArrayDestructureRuns: `[a, b] := arr` binds element i to the i-th
// name, for an element type whose size is not a power of two of its
// fields, a generic element type and a nested array, at run time and in
// compile-time evaluation.
func TestArrayDestructureRuns(t *testing.T) {
	t.Parallel()
	out, errOut, code := buildRunText(t, `linear struct Res {
    tag: i8
    id: i64
}

Acquire(id: i64): Res {
    return Res(tag: 1, id: id)
}

Release(r: Res): i64 {
    Res(tag, id) := r
    return id + i64(tag) - 1
}

Three(): [3]Res {
    return [Acquire(10), Acquire(20), Acquire(30)]
}

Sum(): i64 {
    [a, b, c] := Three()
    return Release(a) * 100 + Release(b) * 10 + Release(c)
}

Split[T](xs: [2]T): (first: T, second: T) {
    [first, second] := xs
    return (first, second)
}

const AtCompileTime: i64 = Sum()

export Main(sys: &System): WriteError!void {
    arr := Three()
    var [p, q, r] := arr
    var s := Release(r) + Release(p) * 2
    s += Release(q) * 3
    [x, y] := [7, 8]
    (first, second) := Split([Acquire(4), Acquire(5)])
    grid := [[Acquire(1), Acquire(2)], [Acquire(3), Acquire(6)]]
    [top, bottom] := grid
    [a, b] := top
    [c, d] := bottom
    g := Release(first) * 10 + Release(second) + Release(a) + Release(b) + Release(c) + Release(d)
    (first: i, second: j) := Split([u8(7), 9])
    t := Sum()
    k := AtCompileTime
    try sys.Print("{s} {x} {y} {t} {k} {g} {i} {j}\n", (s, x, y, t, k, g, i, j))
}
`)
	if code != 0 || strings.TrimSpace(out) != "110 7 8 1230 1230 57 7 9" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 110 7 8 1230 1230 57 7 9", code, out, errOut)
	}
}
