package lower

import (
	"testing"

	"mk/internal/mir"
)

// TestResultParamErrorUnion: a generic whose result is its type parameter,
// instantiated at an error union (Get[Bad!int], as raw.Read[E!int]), cannot
// fail, so its call has no error edge: try, else, match and Must take the
// E!T it returns as a value. Before, the call's E!T type made lower give it
// an error edge, and mir.Validate's M5 failed (callee fallible=false but
// error edge=true). ?T, a struct holding T and []T results hold the E!T.
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
`
	w := newWorld(t, true)
	b := w.add("m", opt{unsafe: true}, src)
	b.wantClean(t)
	l := lowerModule(t, w, b)
	for _, k := range []string{"Else", "Try", "Match", "Must", "InOpt", "InBox", "InSlice", "FnValue", "Bound"} {
		for _, bl := range l.body(k).Blocks {
			if bl.Term.Kind == mir.TermCall && bl.Term.HasErr {
				t.Fatalf("%s: a call of a callee that cannot fail has an error edge:\n%s", k, l.print(k))
			}
		}
	}
}
