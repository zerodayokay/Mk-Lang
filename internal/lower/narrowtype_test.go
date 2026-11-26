// Why: inside `if T is X` a type written T is X, so the branch's MIR must
// name X wherever check recorded it, or mir.Validate fails on a call whose
// generic argument is T and whose argument is the narrowed copy.
// What: TestNarrowedTypeArgLowers, TestNarrowedChainLowers and
// TestNarrowedFnLitLowers.
// How: a std module over a std.raw stub calls raw.ReadUnaligned[T] and
// Id[T] inside `if T is u32`; the generic body lowers with Validate on, and
// both calls in the branch take u32; a chain of two tests lowers a
// parameter, a var, a mut T and the result at u32; fn literals and a fn
// parameter naming T lower at u32 in the branch.

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

// TestNarrowedChainLowers: inside `if T is U { if U is u32 { .. } }` a
// parameter, a `var` written in the branch, a `mut T` written through and
// the result are all u32 in the inner branch, so the body passes Validate.
func TestNarrowedChainLowers(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("std.mchain", opt{std: true}, `Id[T](x: T): T {
	return x
}

F[T: Plain, U: Plain](x: T, p: mut T): T {
	var v: T = x
	if T is U {
		if U is u32 {
			v = Id[T](v) + 10
			*p = *p + 100
			return v * 3
		}
	}
	return v
}
`)
	b.wantClean(t)
	l := lowerModule(t, w, b)
	if s := l.print("F"); !strings.Contains(s, "Id[u32]") {
		t.Errorf("the inner branch does not call Id[u32]:\n%s", s)
	}
}

// TestNarrowedFnLitLowers: inside `if T is u32` a fn literal whose
// signature names T, a capturing literal reading a parameter of type T and
// a parameter of fn type naming T are all u32 there, so the bodies pass
// Validate.
func TestNarrowedFnLitLowers(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("std.mfnlit", opt{std: true}, `Do(const f: fn(): int): int {
	return f()
}

Lit[T: Plain](x: T): T {
	if T is u32 {
		h := fn(y: T): T { return y }
		return h(x) + 1
	}
	return x
}

Cap[T: Plain](x: T): int {
	if T is u32 {
		return Do(fn(): int { return int(x) * 10 })
	}
	return -1
}

Param[T: Plain](x: T, f: fn(T): T): T {
	if T is u32 {
		return f(x) + 1
	}
	return x
}
`)
	b.wantClean(t)
	lowerModule(t, w, b) // every unit lowers with Validate on
}
