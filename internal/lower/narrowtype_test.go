// Why: inside `if T is X` a type written T is X, so the branch's MIR must
// name X wherever check recorded it, or mir.Validate fails on a call whose
// generic argument is T and whose argument is the narrowed copy.
// What: TestNarrowedTypeArgLowers.
// How: a std module over a std.raw stub calls raw.ReadUnaligned[T] and
// Id[T] inside `if T is u32`; the generic body lowers with Validate on, and
// both calls in the branch take u32.

package lower

import (
	"strings"
	"testing"
)

func TestNarrowedTypeArgLowers(t *testing.T) {
	w := newWorld(t, true)
	w.add("std.raw", opt{std: true}, "export ReadUnaligned[T: Plain](p: *T): T\n").wantClean(t)
	b := w.add("std.mnarrow", opt{std: true}, `import std.raw

Id[T](x: T): T {
	return x
}

F[T: Plain](p: *T, x: T): T {
	if T is u32 {
		unsafe "test" {
			return Id[T](raw.ReadUnaligned[T](p)) + x
		}
	}
	return x
}
`)
	b.wantClean(t)
	l := lowerModule(t, w, b)
	s := l.print("F")
	for _, want := range []string{"ReadUnaligned[u32]", "Id[u32]"} {
		if !strings.Contains(s, want) {
			t.Errorf("the branch does not call %s:\n%s", want, s)
		}
	}
}
