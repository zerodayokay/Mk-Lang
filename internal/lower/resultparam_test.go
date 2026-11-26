// Why: a call whose type is E!T only because a type parameter or an alias
// stands for the E!T has a callee that cannot fail, so lower must give it
// no error edge (mir.Validate's M5) and keep the E!T it returns.
// What: TestResultParamErrorUnion.
// How: lowers try, else, match and Must over such calls (a generic, a fn
// value, an interface method through a bound, an alias result) and reads
// each call's terminator: no error edge, and an E!T destination.

package lower

import (
	"testing"

	"mk/internal/mir"
	"mk/internal/types"
)

// TestResultParamErrorUnion: a generic whose result is its type parameter,
// instantiated at an error union (Get[Bad!int], as raw.Read[E!int]), and a
// function whose result is an alias of one (Named), cannot fail, so the
// call has no error edge: try, else, match and Must take the E!T it returns
// as a value. ?T, a struct holding T and []T results hold the E!T.
func TestResultParamErrorUnion(t *testing.T) {
	src := `error Bad

type R = Bad!int

struct Box[T] {
	v: T
}

Get[T](p: *T): T {
	unsafe "p points at a live T" {
		return *p
	}
}

Opt[T](p: *T): ?T {
	return some(Get[T](p))
}

Wrap[T](p: *T): Box[T] {
	return Box[T](v: Get[T](p))
}

Else(p: *R): int {
	v := Get[R](p) else e {
		_ = e
		return 100
	}
	return v
}

Try(p: *R): Bad!int {
	return try Get[R](p) + 1
}

Match(p: *R): int {
	match Get[R](p) {
		ok(v) {
			return v
		}
		err(_) {
			return 200
		}
	}
}

Must(p: *R): int {
	return Get[R](p).Must()
}

InOpt(p: *R): int {
	return (Opt[R](p) else { return 300 }) else { 301 }
}

InBox(p: *R): int {
	return Wrap[R](p).v else { 400 }
}

Sl[T](s: []T): []T {
	return s
}

InSlice(s: []R): int {
	return Sl[R](s)[0] else { 500 }
}

FnValue(p: *R): int {
	g := Get[R]
	return g(p) else { 600 }
}

interface Src[T] {
	Get(&self): T
}

struct Holder[T] {
	p: *T
}

impl[T] Src[T] for Holder[T] {
	Get(&self): T {
		unsafe "p points at a live T" {
			return *self.p
		}
	}
}

Bound[S: Src[R]](s: &S): int {
	return s.Get() else { 700 }
}

Named(p: *R): R {
	unsafe "p points at a live R" {
		return *p
	}
}

Alias(p: *R): int {
	return Named(p) else { 800 }
}
`
	w := newWorld(t, true)
	b := w.add("m", opt{unsafe: true}, src)
	b.wantClean(t)
	l := lowerModule(t, w, b)
	for _, k := range []string{"Else", "Try", "Match", "Must", "InOpt", "InBox", "InSlice", "FnValue", "Bound", "Alias"} {
		body := l.body(k)
		for _, bl := range body.Blocks {
			if bl.Term.Kind == mir.TermCall && bl.Term.HasErr {
				t.Fatalf("%s: a call of a callee that cannot fail has an error edge:\n%s", k, l.print(k))
			}
		}
	}
	// The call's destination is the whole E!T the callee returns, not its
	// T: mir.Validate does not compare it with a generic callee's result.
	for _, k := range []string{"Else", "Try", "Match", "Must", "FnValue", "Bound", "Alias"} {
		body := l.body(k)
		for _, bl := range body.Blocks {
			if bl.Term.Kind == mir.TermCall && w.tab.Kind(body.PlaceType(bl.Term.Dest)) != types.KErrorUnion {
				t.Fatalf("%s: the call's destination is not the E!T:\n%s", k, l.print(k))
			}
		}
	}
}
