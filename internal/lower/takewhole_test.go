// Why: a by-value match that takes a linear struct of plain fields apart
// copies its bindings, so lower adds one move out of it for the loan
// checker to see the struct consumed; that move should copy a scalar, not
// an array, and alternatives of the same shape must make it too.
// What: TestTakeWholeScalarField, TestTakeAltsSameShape and scrutinee.
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
	if n := strings.Count(got, "= move ("+x+" as some).0.0"); n != 2 {
		t.Errorf("want a take in the alternative's arm and in some(T(id)), got %d:\n%s", n, got)
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
