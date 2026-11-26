// Why: a by-value match that takes a linear struct of plain fields apart
// copies its bindings, so lower adds one move out of it for the loan
// checker to see the struct consumed; that move should copy a scalar, not
// an array, and each alternative must make it on its own edge, whatever
// the others' shape. A struct with no fields has nothing to move, so
// StConsume ends it.
// What: TestTakeWholeScalarField, TestTakeAltsSameShape, TestTakeAltsMixedVariant,
// TestTakeWholeNoFields, TestDestructureNoFields and scrutinee.
// How: the printed MIR of small matches, read for the move.

package lower

import (
	"strings"
	"testing"
)

func TestTakeWholeScalarField(t *testing.T) {
	l := lowerSrc(t, `linear struct Big {
	buf: [8]int
	n: int
}
Use(b: Big): int {
	return match b {
		Big(buf: _, n) { n }
	}
}
`)
	got := l.print("Use")
	x := scrutinee(t, got)
	if !strings.Contains(got, "= move "+x+".1") {
		t.Errorf("the take does not move the scalar field n:\n%s", got)
	}
	if strings.Contains(got, "= move "+x+".0") {
		t.Errorf("the take moves the array field buf:\n%s", got)
	}
}

func TestTakeAltsSameShape(t *testing.T) {
	l := lowerSrc(t, `linear struct T {
	id: int
}
Use(o: ?T): int {
	return match o {
		some(T(id: 1)) | some(T(id: 2)) { 0 }
		some(T(id)) { id }
		none { 1 }
	}
}
`)
	got := l.print("Use")
	x := scrutinee(t, got)
	if n := strings.Count(got, "= move ("+x+" as some).0.0"); n != 3 {
		t.Errorf("want a take on each alternative's edge and in some(T(id)), got %d:\n%s", n, got)
	}
}

func TestTakeAltsMixedVariant(t *testing.T) {
	l := lowerSrc(t, `linear struct T {
	id: int
}
Use(o: ?T): int {
	return match o {
		some(T(id: 1)) | none { 0 }
		some(T(id)) { id }
	}
}
`)
	got := l.print("Use")
	x := scrutinee(t, got)
	if n := strings.Count(got, "= move ("+x+" as some).0"); n != 2 {
		t.Errorf("want a take on the some alternative's edge and in some(T(id)), got %d:\n%s", n, got)
	}
}

func TestTakeWholeNoFields(t *testing.T) {
	l := lowerSrc(t, `linear struct E {}
Use(o: ?E): int {
	return match o {
		some(E()) { 1 }
		none { 0 }
	}
}
Top(e: E): int {
	return match e {
		E() { 2 }
	}
}
Skip(o: ?E): int {
	return match &o {
		some(E()) { 1 }
		none { 0 }
	}
}
`)
	got := l.print("Use")
	if x := scrutinee(t, got); !strings.Contains(got, "consume ("+x+" as some).0") {
		t.Errorf("some(E()) does not consume the payload:\n%s", got)
	}
	got = l.print("Top")
	if x := scrutinee(t, got); !strings.Contains(got, "consume "+x+"\n") {
		t.Errorf("E() does not consume the temporary:\n%s", got)
	}
	if got = l.print("Skip"); strings.Contains(got, "consume") {
		t.Errorf("a shared match consumes:\n%s", got)
	}
}

func TestDestructureNoFields(t *testing.T) {
	l := lowerSrc(t, `linear struct E {}
linear struct S {
	e: E
	n: int
}
view struct View {}
struct WithView { v: View }
Top(e: E) {
	E() := e
}
Nested(s: S): int {
	S(e: E(), n) := s
	return n
}
NestedView(v: WithView) {
	WithView(v: View()) := v
}
`)
	got := l.print("Top")
	if x := scrutinee(t, got); !strings.Contains(got, "consume "+x+"\n") {
		t.Errorf("E() := e does not consume the temporary:\n%s", got)
	}
	got = l.print("Nested")
	if x := scrutinee(t, got); !strings.Contains(got, "consume "+x+".0") {
		t.Errorf("S(e: E(), n) := s does not consume field e:\n%s", got)
	}
	got = l.print("NestedView")
	if x := scrutinee(t, got); !strings.Contains(got, "consume "+x+".0") {
		t.Errorf("WithView(v: View()) := v does not consume field v:\n%s", got)
	}
}

// scrutinee is the temporary a by-value match on parameter _1 moves it
// into.
func scrutinee(t *testing.T, mirText string) string {
	t.Helper()
	for _, ln := range strings.Split(mirText, "\n") {
		if x, ok := strings.CutSuffix(strings.TrimSpace(ln), " = move _1"); ok {
			return x
		}
	}
	t.Fatalf("no move of _1:\n%s", mirText)
	return ""
}
