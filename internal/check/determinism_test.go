// Why: diagnostics and read sets must not depend on goroutine scheduling,
// or builds and caches stop being reproducible.
// What: TestDeterministicDiagnostics, TestReadSets,
// TestPhase_E0204MemberIsTypes, TestNoDuplicateEmitters.
// How: checks the same input with GOMAXPROCS 1 and 8 and compares the
// output byte for byte; reads each body's read set.

package check

import (
	"bytes"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mk/internal/diag"
	"mk/internal/types"
)

// detSrc draws diagnostics from many units: types, calls, patterns, errors,
// format strings, generics and impls.
const detSrc = `error E

struct P {
	x: int
}

enum S {
	A
	B
	C
}

interface Shape {
	Area(&self): f64
}

impl Shape for P {
	Area(&self): f64 {
		return 1.0
	}
}

F1(): int {
	x: u8 = 300
	return "s"
}

F2(s: S): int {
	match s {
		.A {
			return 1
		}
	}
	return 2
}

F3(): E!int {
	return 1
}

F4() {
	y := F3()
	p := P(y: 1)
}

F5[T](a: T, b: T): T {
	return a + b
}

F6(w: mut Writer) {
	try Print(mut w, "{x} {y}\n", (x: 1))
}

F7(p: P): f64 {
	return p.Area() + p.Volume()
}
`

// runDet checks every unit of a fresh build of detSrc, with procs workers,
// and returns the ordered JSON report.
func runDet(t *testing.T, procs int) []byte {
	t.Helper()
	old := runtime.GOMAXPROCS(procs)
	defer runtime.GOMAXPROCS(old)
	w := newWorld(t, true)
	b := w.add("m", opt{}, detSrc)
	units := b.m.Units()
	bodies := make([]*Body, len(units))
	var wg sync.WaitGroup
	sem := make(chan struct{}, procs)
	for i, id := range units {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			bodies[i] = w.c.Check(b.m, id)
		}()
	}
	wg.Wait()
	ds := append([]*diag.Diagnostic(nil), b.diags()...)
	for _, body := range bodies {
		ds = append(ds, body.Diags...)
	}
	var out bytes.Buffer
	for _, ph := range []diag.Phase{diag.PhaseNames, diag.PhaseTypes, diag.PhaseFlow} {
		var sub []*diag.Diagnostic
		for _, d := range ds {
			if d.Phase == ph {
				sub = append(sub, d)
			}
		}
		out.Write(diag.Order(sub, w.sm, diag.NoCap).WriteJSON(w.sm))
	}
	return out.Bytes()
}

func TestDeterministicDiagnostics(t *testing.T) {
	one := runDet(t, 1)
	eight := runDet(t, 8)
	if !bytes.Equal(one, eight) {
		t.Fatalf("GOMAXPROCS 1 and 8 differ:\n%s\n----\n%s", one, eight)
	}
	for _, code := range []string{"E0303", "E0301", "E0306", "E0802", "E0305", "E0703", "E0706", "E0203"} {
		if !strings.Contains(string(one), code) {
			t.Errorf("the report lacks %s:\n%s", code, one)
		}
	}
	if again := runDet(t, 8); !bytes.Equal(one, again) {
		t.Fatalf("two GOMAXPROCS 8 runs differ")
	}
}

// TestReadSets: a body's reads name signatures, shapes, fields, impls and
// negative reads, and never another body.
func TestReadSets(t *testing.T) {
	w := newWorld(t, true)
	w.add("lib", opt{}, "export struct Q {\n\texport n: int\n}\n\nexport Make(): Q {\n\treturn Q(n: 1)\n}\n\nexport interface Shape {\n\tArea(&self): f64\n}\n\nimpl Shape for Q {\n\tArea(&self): f64 {\n\t\treturn 1.0\n\t}\n}\n")
	b := w.add("m", opt{}, "import lib\n\nArea[T: lib.Shape](x: &T): f64 {\n\treturn x.Area()\n}\n\nF(): int {\n\tq := lib.Make()\n\ta := Area(&q)\n\treturn q.n + q.Missing\n}\n")
	var reads []types.Read
	for _, id := range b.m.Units() {
		if w.items.Get(id).Key.String() == "m.F" {
			reads = w.c.Check(b.m, id).Reads.Sorted()
		}
	}
	has := func(k types.ReadKind, key, name string) bool {
		for _, r := range reads {
			if r.Kind == k && r.Key.String() == key && (name == "*" || r.Name == name) {
				return true
			}
		}
		return false
	}
	var all []string
	for _, r := range reads {
		all = append(all, r.Kind.String()+" "+r.Key.String()+" "+r.Name)
		if r.Kind == types.ReadBody {
			t.Errorf("a body read another body: %s", r.Key)
		}
	}
	for _, want := range []struct {
		k         types.ReadKind
		key, name string
	}{
		{types.ReadSignature, "lib.Make", "*"},
		{types.ReadSignature, "m.Area", "*"},
		{types.ReadFields, "lib.Q", "*"},
		{types.ReadImpl, "lib.Shape", "*"},
		{types.ReadNoItem, "*", "Missing"},
	} {
		if want.key == "*" {
			found := false
			for _, r := range reads {
				if r.Kind == want.k && r.Name == want.name {
					found = true
				}
			}
			if !found {
				t.Errorf("no %v read named %s in %q", want.k, want.name, all)
			}
			continue
		}
		if !has(want.k, want.key, want.name) {
			t.Errorf("no %v read of %s in %q", want.k, want.key, all)
		}
	}
}

func TestPhase_E0204MemberIsTypes(t *testing.T) {
	w := newWorld(t, true)
	w.add("lib", opt{}, "export struct P {\n\tsecret: int\n\texport Name: int\n}\n\nexport Make(): P {\n\treturn P(secret: 1, Name: 2)\n}\n")
	b := checkBody(t, w, "E0204@4:11 E0207@8:11 E0303@12:10",
		"import lib\n\nF(p: lib.P): int {\n\treturn p.secret\n}\n\nH(p: lib.P): int {\n\treturn p.name\n}\n\nG() {\n\tx: u8 = 300\n}\n")
	for _, d := range b.bodyDiags(t) {
		if (d.Code == diag.E0204 || d.Code == diag.E0207) && d.Phase != diag.PhaseTypes {
			t.Errorf("%v has phase %v, want types", d.Code, d.Phase)
		}
	}
}

// TestNoDuplicateEmitters: each package runs on its input with no
// phase barrier; every site gets exactly one diagnostic, from its owner.
func TestNoDuplicateEmitters(t *testing.T) {
	t.Run("resolve and check", func(t *testing.T) {
		w := newWorld(t, true)
		b := w.add("m", opt{}, "str.Shout(self): str {\n\treturn self\n}\n\ntest \"x\" {\n}\n\ntest \"x\" {\n}\n\nenum S {\n\tLen\n}\n\nS.Len(&self): int {\n\treturn 1\n}\n")
		var all []*diag.Diagnostic
		all = append(all, b.rdiags...)
		all = append(all, b.m.Diags...)
		all = append(all, b.bodyDiags(t)...)
		var non []*diag.Diagnostic
		for _, d := range all {
			if d.Code != diag.E0208 {
				non = append(non, d)
			}
		}
		if got := codes(w, non); got != "E0206@15:3 E0206@8:1 E0704@1:5" {
			t.Fatalf("diagnostics %q\n%s", got, dump(w, non))
		}
		for _, d := range b.m.Diags {
			if d.Code == diag.E0206 {
				t.Errorf("check emitted E0206 at %s", w.lc(d.At))
			}
		}
	})
	t.Run("parser recovery", func(t *testing.T) {
		w := newWorld(t, true)
		b := w.add("m", opt{parseErrors: true}, "packed(3) struct P {\n\ta: u8\n}\n\nF(p: *int) {\n\tunsafe \"\" {\n\t\tx := *p\n\t}\n}\n\ninterface I {\n\tM(&self, n: int)\n\t\trequires n > 0\n}\n\nenum E: u8 {\n\tA(x: int)\n}\n")
		var ds []*diag.Diagnostic
		ds = append(ds, b.m.Diags...)
		ds = append(ds, b.bodyDiags(t)...)
		var non []*diag.Diagnostic
		for _, d := range ds {
			if d.Code != diag.E0208 {
				non = append(non, d)
			}
		}
		if len(non) != 0 {
			t.Fatalf("check added diagnostics to a recovered tree:\n%s", dump(w, non))
		}
	})
}
