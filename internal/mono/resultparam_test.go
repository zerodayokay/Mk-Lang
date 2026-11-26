// Why: an impl of Source[E!int] whose method writes its result as an alias of
// the E!int cannot fail, so a call of it through a bound, made direct per
// instance, must carry no error edge (mir.Validate's M5).
// What: TestResultParamAliasImpl.
// How: collects a program that calls the method through `S: Source[R]` and
// through `S: Source[T]` at T = R, with Validate, and finds its instances.

package mono

import (
	"strings"
	"testing"

	"mk/internal/types"
)

func TestResultParamAliasImpl(t *testing.T) {
	src := `error Bad

type R = Bad!int

Make(n: int): Bad!int {
    if n < 0 {
        fail Bad
    }
    return n
}

interface Source[T] {
    Get(&self): T
}

struct Cell {
    n: int
}

impl Source[Bad!int] for Cell {
    Get(&self): R {
        var r: R = Make(self.n)
        return r
    }
}

Via[S: Source[R]](s: &S): int {
    return s.Get() else { 9 }
}

Pass[T, S: Source[T]](s: &S): T from s {
    return s.Get()
}

Run(): int {
    c := Cell(n: -4)
    a := Via(&c)
    b := Pass[R, Cell](&c) else { 7 }
    return a + b
}
`
	sw := newSrcWorld(t, src)
	source, cell := sw.item(t, "Source"), sw.item(t, "Cell")
	get := types.NoItem
	for _, id := range sw.units {
		if k := sw.items.Get(id).Key.String(); strings.Contains(k, "#impl(") && strings.HasSuffix(k, ":Get") {
			get = id
		}
	}
	if get == types.NoItem {
		t.Fatal("no impl method Get")
	}
	sw.impls[implKey{source, 0, sw.tab.Named(cell, nil)}] = get
	in, ds := sw.collect(t, Options{Validate: true}, sw.limitRoots()...)
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if len(instancesOf(in, get)) == 0 {
		t.Fatalf("no instance of Cell's Get:\n%s", summary(in))
	}
}
