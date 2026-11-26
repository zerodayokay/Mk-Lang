// Why: ok(p) and err(p) take any pattern p, and an E!T can sit inside the
// scrutinee (the ok payload of an E!(F!int), a field, a some payload), so
// lower's pattern compiler must take nested ok and err patterns apart, and
// a top-level alternative may join both sides of a match on an E!T.
// What: TestNestedUnionPatterns.
// How: a grid of matches, each kind of pattern under each kind that can
// hold an E!T, checked, lowered and validated (lowerModule fails on any
// ICE); the inexhaustive ones must be E0306, never E0000.

package lower

import (
	"strings"
	"testing"

	"mk/internal/check"
	"mk/internal/source"
)

// nestedPrelude declares the errors and the generics that make an E!T a
// value: a generic whose result is its type parameter, at an error union,
// returns the union.
const nestedPrelude = `error E
error F
error F2(code: int)

const Limit: int = 7

type R = F|F2!int

struct S[T] {
	r: T
	k: int
}

enum K[T] {
	A(r: T)
	B
}

Get[T](p: *T, bad: bool): E!T {
	if bad {
		fail E
	}
	unsafe "p points at a live T" {
		return *p
	}
}

Load[T](p: *T): T {
	unsafe "p points at a live T" {
		return *p
	}
}

Opt[T](p: *T): ?T {
	return some(Load[T](p))
}

Box[T](p: *T): S[T] {
	return S[T](r: Load[T](p), k: 5)
}

Ka[T](p: *T): K[T] {
	return K[T].A(r: Load[T](p))
}
`

func TestNestedUnionPatterns(t *testing.T) {
	cases := []struct {
		name, src string
		want      string // "" clean, else the one check code
	}{
		{"call", `Use(p: *R, bad: bool): int {
	match Get[R](p, bad) {
		ok(ok(n)) { return n }
		ok(err(_)) { return 200 }
		err(_) { return 100 }
	}
}`, ""},
		{"call/cases", `Use(p: *R): int {
	return match Get[R](p, false) {
		ok(ok(Limit)) { 7 }
		ok(ok(0..5)) { 1 }
		ok(ok(3 | 9)) { 3 }
		ok(ok(n)) { n }
		ok(err(F2(code: c))) { c }
		ok(err(F)) { 2 }
		err(E) { 100 }
	}
}`, ""},
		{"call/alt", `Use(p: *R, bad: bool): int {
	return match Get[R](p, bad) {
		ok(ok(1) | err(F)) { 11 }
		ok(ok(1) | ok(2)) | err(_) { 12 }
		ok(_) { 13 }
	}
}`, ""},
		{"call/flat-alt", `Use(p: *R): int {
	return match Load[R](p) {
		ok(1) | err(F) { 1 }
		err(_) | ok(_) { 2 }
	}
}`, ""},
		{"stored", `Use(p: *R, bad: bool): int {
	var r: E!R = Get[R](p, bad)
	return match r {
		ok(ok(n)) { n }
		ok(err(F2(code))) { code }
		ok(err(_)) | err(_) { 0 }
	}
}`, ""},
		{"stored/ref", `Use(p: *R, bad: bool): int {
	var r: E!R = Get[R](p, bad)
	match &r {
		ok(ok(n)) { return *n }
		_ { return 0 }
	}
}`, ""},
		{"stored/ref-err", `Use(p: *R, bad: bool): int {
	var r: E!R = Get[R](p, bad)
	return match &r {
		ok(ok(0)) | err(_) {
			var k := 0
			for i in [5, 6] {
				if i == 6 {
					break
				}
				k += i
			}
			k
		}
		ok(ok(n)) { *n }
		ok(err(e)) {
			match e {
				F { 1 }
				F2(code) { *code }
			}
		}
	}
}`, ""},
		{"stored/mut", `Use(p: *R, bad: bool): int {
	var r: E!R = Get[R](p, bad)
	match mut r {
		ok(ok(n)) { *n = *n + 1 }
		ok(err(_)) { }
		err(_) { }
	}
	return match r {
		ok(ok(n)) { n }
		_ { 0 }
	}
}`, ""},
		{"some", `Use(p: *R): int {
	return match Opt[R](p) {
		some(ok(0..10)) { 1 }
		some(ok(v)) { v }
		some(err(F2(code))) { code }
		some(err(_)) { 3 }
		none { 4 }
	}
}`, ""},
		{"struct", `Use(p: *R): int {
	return match Box[R](p) {
		S(r: ok(v), k) { v + k }
		S(r: err(F), k: 5) { 50 }
		S(r: err(_), k) { k }
	}
}`, ""},
		{"variant", `Use(p: *R): int {
	return match Ka[R](p) {
		.A(r: ok(v)) { v }
		.A(r: err(F2(code: c))) { c }
		.A(r: err(F)) { 77 }
		.B { 99 }
	}
}`, ""},
		{"deep", `Use(p: *(?R), bad: bool): int {
	return match Get[?R](p, bad) {
		ok(some(ok(n))) { n }
		ok(some(err(_))) | ok(none) { 1 }
		err(_) { 2 }
	}
}`, ""},
		{"inexhaustive/call", `Use(p: *R, bad: bool): int {
	match Get[R](p, bad) {
		ok(ok(n)) { return n }
		err(_) { return 100 }
	}
}`, "E0306"},
		{"inexhaustive/some", `Use(p: *R): int {
	return match Opt[R](p) {
		some(ok(v)) { v }
		some(err(F)) { 3 }
		none { 4 }
	}
}`, "E0306"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, true)
			b := w.add("m", opt{unsafe: true}, nestedPrelude, c.src)
			b.wantClean(t)
			if c.want == "" {
				lowerModule(t, w, b)
				return
			}
			var got []string
			for _, id := range b.m.Units() {
				it := w.items.Get(id)
				var body *check.Body
				if ie := source.Guard(it.Key.String(), it.Span, func() { body = w.c.Check(b.m, id) }); ie != nil {
					t.Fatalf("check ICE in %s: %v", it.Key, ie)
				}
				for _, d := range body.Diags {
					got = append(got, d.Code.String())
				}
			}
			if strings.Join(got, " ") != c.want {
				t.Fatalf("want %s, got %v", c.want, got)
			}
		})
	}
}
