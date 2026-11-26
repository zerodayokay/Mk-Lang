// Why: a by-value destructure of a non-plain value whose bound leaves are
// all plain or `_` must still take from its temporary, or borrow reports
// E0402 at the temporary's scope end (std's HeapText.Free, Arena, Guard,
// File.Close hit this).
// What: TestDestructure_ConsumesNonPlainTemporary,
// TestDestructure_SecondTemporaryTakes.
// How: lowers each shape and checks the take in the MIR text.

package lower

import (
	"regexp"
	"testing"
)

const takeSrc = `linear struct Tok {
	n: int
	m: int
}
struct Res {
	t: Tok
	k: int
}
struct Pair {
	left: Tok
	right: Tok
}
linear struct H {
	r: Tok
	id: int
}
struct P {
	x: int
	y: int
}
Leaf(t: Tok): int {
	Tok(n, m: _) := t
	return n
}
AllWild(t: Tok) {
	Tok(n: _, m: _) := t
}
AllBound(t: Tok): int {
	Tok(n, m) := t
	return n + m
}
Nested(r: Res): int {
	Res(t: Tok(n, m: _), k) := r
	return n + k
}
NestedWild(r: Res) {
	Res(t: Tok(n: _, m: _), k: _) := r
}
Siblings(p: Pair): int {
	Pair(left: Tok(n, m: _), right: Tok(n: _, m)) := p
	return n + m
}
Linear(h: H): int {
	H(r, id) := h
	Tok(n, m) := r
	return id + n + m
}
PlainP(p: P): int {
	P(x, y: _) := p
	return x
}
`

var takeRE = regexp.MustCompile(`= move _2\.[0-9.]+`)

// TestDestructure_ConsumesNonPlainTemporary pins the takes: one Move per
// linear struct with no other take, of the first `_` leaf (else the first
// plain binding), and none added for a plain value.
func TestDestructure_ConsumesNonPlainTemporary(t *testing.T) {
	l := lowerSrc(t, takeSrc)
	cases := []struct {
		fn    string
		takes []string
	}{
		{"Leaf", []string{"= move _2.1"}},
		{"AllWild", []string{"= move _2.0"}},
		{"AllBound", []string{"= move _2.0"}},
		{"Nested", []string{"= move _2.0.1"}},
		{"NestedWild", []string{"= move _2.0.0"}},
		{"Siblings", []string{"= move _2.0.1", "= move _2.1.0"}},
		{"Linear", []string{"= move _2.0"}},
		{"PlainP", nil},
	}
	for _, c := range cases {
		d := l.print(c.fn)
		got := takeRE.FindAllString(d, -1)
		if len(got) != len(c.takes) {
			t.Fatalf("%s: takes %q, want %q:\n%s", c.fn, got, c.takes, d)
		}
		for i := range got {
			if got[i] != c.takes[i] {
				t.Fatalf("%s: takes %q, want %q:\n%s", c.fn, got, c.takes, d)
			}
		}
	}
	// The user's plain bindings stay copies.
	wantIn(t, l.print("Leaf"), "= copy _2.0", "= move _2.1")
	wantIn(t, l.print("Nested"), "= copy _2.0.0", "= move _2.0.1", "= copy _2.1")
}

// TestDestructure_SecondTemporaryTakes: the second destructure of Linear
// (Tok(n, m) := r, both leaves plain) takes from its own temporary too.
func TestDestructure_SecondTemporaryTakes(t *testing.T) {
	l := lowerSrc(t, takeSrc)
	d := l.print("Linear")
	if n := len(regexp.MustCompile(`= move _[0-9]+\.[0-9]`).FindAllString(d, -1)); n != 2 {
		t.Fatalf("Linear: %d takes, want 2 (r, then n of the second destructure):\n%s", n, d)
	}
}
