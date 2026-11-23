// Why: a capturing fn literal may stand only as a const argument, and its
// environment and captures must be typed and ordered exactly.
// What: tests of positions, captures, environments, the literal unit's
// wait, E0609 uses and writes) and TestE0705_ConstFnUses.
// How: checks programs over the fake prelude (capDiags) and reads
// Body.Envs, Captures and EnvRef; captureSeeds feed FuzzCheck.

package check

import (
	"strings"
	"sync"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
	"mk/internal/types"
)

// capDecls declares a sort with a const comparator, a generic one, a method
// with a const parameter and a plain fn-taking function; F starts on line 22.
const capDecls = "Sort(xs: mut []i32, const less: fn(&i32, &i32): bool) {\n}\n\n" +
	"GSort[T](xs: mut []T, const less: fn(&T, &T): bool) {\n}\n\n" +
	"struct Box {\n\tn: int\n}\n\nBox.Each(&self, const f: fn(int): bool) {\n}\n\n" +
	"Keep(xs: []i32, keep: fn(&i32): bool): int {\n\treturn 0\n}\n\n" +
	"Use(f: fn(): int): int {\n\treturn f()\n}\n\n"

// capDiags checks src over the fake prelude: every diagnostic of every unit,
// deduplicated as diag.Order does (a capturing literal's unit carries its
// enclosing unit's E0609 too), E0208 left out.
func capDiags(t *testing.T, src string) (*world, []*diag.Diagnostic) {
	t.Helper()
	w := newWorld(t, true)
	b := w.add("m", opt{}, src)
	ds := append([]*diag.Diagnostic(nil), b.diags()...)
	ds = append(ds, b.bodyDiags(t)...)
	var out []*diag.Diagnostic
	seen := map[*diag.Diagnostic]bool{}
	for _, d := range ds {
		if d.Code == diag.E0208 || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return w, out
}

func runCapCases(t *testing.T, cases []bodyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, ds := capDiags(t, tc.src)
			if got := codes(w, ds); got != tc.want {
				t.Fatalf("diagnostics %q, want %q\n%s", got, tc.want, dump(w, ds))
			}
		})
	}
}

// TestConstArgumentCaptures: a literal written as a const argument may
// read the enclosing locals and parameters.
func TestConstArgumentCaptures(t *testing.T) {
	runCapCases(t, []bodyCase{
		{"function", capDecls + "F(xs: mut []i32, origin: i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool { return *a - origin < *b - origin })\n}\n", ""},
		{"generic callee", capDecls + "F(xs: mut []i32, origin: i32) {\n\tGSort(xs, fn(a: &i32, b: &i32): bool { return *a < origin })\n}\n", ""},
		{"method", capDecls + "F(b: &Box, limit: int) {\n\tb.Each(fn(x: int): bool { return x < limit })\n}\n", ""},
		{"interface method", capDecls + "interface Walk {\n\tVisit(&self, const f: fn(int): bool)\n}\n\nF[W: Walk](w: &W, limit: int) {\n\tw.Visit(fn(x: int): bool { return x < limit })\n}\n", ""},
		{"namespace form", capDecls + "F(b: &Box, limit: int) {\n\tBox.Each(b, fn(x: int): bool { return x < limit })\n}\n", ""},
		{"parenthesized", capDecls + "F(xs: mut []i32, origin: i32) {\n\tSort(xs, (fn(a: &i32, b: &i32): bool { return *a < origin }))\n}\n", ""},
		{"defer", capDecls + "F(xs: mut []i32, origin: i32) {\n\tdefer Sort(xs, fn(a: &i32, b: &i32): bool { return *a < origin })\n}\n", ""},
		{"local and var local", capDecls + "F(xs: mut []i32) {\n\tk := 1\n\tvar v := 2\n\tv += 1\n\tSort(xs, fn(a: &i32, b: &i32): bool { return *a < 3 && k + v > 0 })\n}\n", ""},
		{"self", capDecls + "Box.Sorted(&self, xs: mut []i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool { return self.n > 0 })\n}\n", ""},
		{"nested", capDecls + "F(xs: mut []i32, origin: i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\tvar ys: [2]i32 = [1, 2]\n\t\tSort(mut ys[..], fn(c: &i32, d: &i32): bool { return *c < origin })\n\t\treturn *a < origin\n\t})\n}\n", ""},
		{"capture of a capture only", capDecls + "F(xs: mut []i32, origin: i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\tvar ys: [2]i32 = [1, 2]\n\t\tSort(mut ys[..], fn(c: &i32, d: &i32): bool { return *c < origin })\n\t\treturn true\n\t})\n}\n", ""},
		{"a field and a read through a reference", capDecls + "F(xs: mut []i32, b: &Box) {\n\tSort(xs, fn(a: &i32, c: &i32): bool { return b.n > 0 && *a < *c })\n}\n", ""},
		{"no capture stays a value", capDecls + "F(xs: []i32): int {\n\tf := fn(x: &i32): bool { return *x > 0 }\n\treturn Keep(xs, f)\n}\n", ""},
	})
}

// TestE0609_Positions: a capturing literal anywhere but a const
// argument is E0609 in the types group, at the first captured use.
//
// A fn literal in a bracket argument (H[fn(..) { .. }]) is a parse error
// (E0101: a bracket holds a type or a const expression, never a block), so
// litContext's bracket reason has no source that reaches it.
func TestE0609_Positions(t *testing.T) {
	runCapCases(t, []bodyCase{
		{"bound", capDecls + "F(limit: i32): int {\n\tf := fn(x: &i32): bool { return *x > limit }\n\t_ = f\n\treturn 0\n}\n", "E0609@23:39"},
		{"var", capDecls + "F(limit: i32) {\n\tvar f: fn(&i32): bool = fn(x: &i32): bool { return *x > limit }\n\t_ = f\n}\n", "E0609@23:58"},
		{"returned", capDecls + "F(limit: i32): fn(&i32): bool {\n\treturn fn(x: &i32): bool { return *x > limit }\n}\n", "E0609@23:41"},
		{"not const argument", capDecls + "F(xs: []i32, limit: i32): int {\n\treturn Keep(xs, fn(x: &i32): bool { return *x > limit })\n}\n", "E0609@23:50"},
		{"array element", capDecls + "F(limit: i32) {\n\tfs := [fn(x: &i32): bool { return *x > limit }]\n\t_ = fs\n}\n", "E0609@23:41"},
		{"record field", capDecls + "F(limit: i32) {\n\tr := (f: fn(x: &i32): bool { return *x > limit })\n\t_ = r\n}\n", "E0609@23:43"},
		{"some", capDecls + "F(limit: int) {\n\to := some(fn(): int { return limit })\n\t_ = o\n}\n", "E0609@23:31"},
		{"call through a fn value", capDecls + "F(g: fn(fn(&i32): bool): bool, limit: i32): bool {\n\treturn g(fn(x: &i32): bool { return *x > limit })\n}\n", "E0609@23:43"},
		{"s.go with a const parameter", capDecls + "Spawned(const f: fn(&i32): bool) {\n}\n\nF(s: &Scope, limit: i32) {\n\ts.go Spawned(fn(x: &i32): bool { return *x > limit })\n}\n", "E0609@26:47"},
	})
	w, ds := capDiags(t, capDecls+"F(xs: []i32, limit: i32): int {\n\treturn Keep(xs, fn(x: &i32): bool { return *x > limit })\n}\n")
	if len(ds) != 1 || ds[0].Phase != diag.PhaseTypes || fact(ds[0], "reason") != "argument" || fact(ds[0], "captured") != "[limit]" {
		t.Fatalf("facts:\n%s", dump(w, ds))
	}
}

// TestE0609_Above is the reference's Above block: one E0609 from
// check, on the marked line, captured = [limit].
func TestE0609_Above(t *testing.T) {
	keys, texts := languageDocBlocks(t)
	for _, k := range keys {
		src := texts[k]
		if !containsAll(src, "// verdict: reject E0609\n", "Above(xs: []i32, limit: i32): int {") {
			continue
		}
		w, ds := capDiags(t, src)
		if len(ds) != 1 || ds[0].Code != diag.E0609 || fact(ds[0], "captured") != "[limit]" || ds[0].Phase != diag.PhaseTypes {
			t.Fatalf("%s:\n%s", k, dump(w, ds))
		}
		return
	}
	t.Fatal("no Above block")
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// TestCaptureWrites: inside a const fn literal, assigning a `var` capture
// is allowed (capturing it `mut`), while assigning a non-`var` capture is
// E0508 in the flow group (with a proven `var ` fix for a local `:=`).
func TestE0609_Writes(t *testing.T) {
	pre := capDecls + "F(xs: mut []i32, b: Box) {\n\tvar n := 0\n\tm := 0\n"
	runCapCases(t, []bodyCase{
		{"assign var", pre + "\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tn = 1\n\t\treturn true\n\t})\n\t_ = m\n}\n", ""},
		{"compound var", pre + "\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tn += 1\n\t\treturn true\n\t})\n\t_ = m\n}\n", ""},
		{"assign non-var local", pre + "\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tm = 1\n\t\treturn true\n\t})\n\t_ = n\n}\n", "E0508@26:3"},
		{"field non-var param", pre + "\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tb.n = 1\n\t\treturn true\n\t})\n\t_ = n + m\n}\n", "E0508@26:3"},
		{"read", pre + "\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tk := n + b.n + m\n\t\treturn k > 0\n\t})\n\tn = 2\n\t_ = n\n}\n", ""},
	})
	w, ds := capDiags(t, pre+"\tSort(xs, fn(a: &i32, c: &i32): bool {\n\t\tm += 1\n\t\treturn true\n\t})\n\t_ = n\n}\n")
	if len(ds) != 1 || ds[0].Code != diag.E0508 || ds[0].Phase != diag.PhaseFlow || fact(ds[0], "captured") != "[m]" || fact(ds[0], "reason") != "write" {
		t.Fatalf("E0508 facts:\n%s", dump(w, ds))
	}
	if len(ds[0].Fixes) != 1 || ds[0].Fixes[0].Class != diag.Proven || len(ds[0].Fixes[0].Edits) != 1 || ds[0].Fixes[0].Edits[0].Text != "var " {
		t.Fatalf("E0508 fix: %+v", ds[0].Fixes)
	}
}

// TestEnvTypes: the enclosing unit's Body.Envs and the literal unit's
// Captures and EnvRef: fields in byte order of the names, &T each.
func TestEnvTypes(t *testing.T) {
	w := newWorld(t, true)
	b := w.add("m", opt{}, capDecls+"F(xs: mut []i32, zeta: i32, b: &Box) {\n\talpha := \"s\"\n\tSort(xs, fn(x: &i32, y: &i32): bool { return *x < zeta && b.n > 0 && alpha.Len() > 0 })\n}\n")
	var outer, lit *Body
	for _, id := range b.m.Units() {
		body := w.c.Check(b.m, id)
		if len(body.Diags) > 0 {
			t.Fatalf("%s:\n%s", w.items.Get(id).Key, dump(w, body.Diags))
		}
		switch w.items.Get(id).Key.String() {
		case "m.F":
			outer = body
		case "m.F#fn0":
			lit = body
		}
	}
	if outer == nil || lit == nil || len(outer.Envs) != 1 {
		t.Fatalf("units: outer %v literal %v", outer != nil, lit != nil)
	}
	want := w.tab.Ref(w.tab.Record([]types.RecordField{
		{Name: w.in.Intern("alpha"), Type: w.tab.Ref(types.Str)},
		{Name: w.in.Intern("b"), Type: w.tab.Ref(w.tab.Ref(w.tab.Named(b.item("Box"), nil)))},
		{Name: w.in.Intern("zeta"), Type: w.tab.Ref(types.I32)},
	}))
	if outer.Envs[0].Ref != want || lit.EnvRef != want {
		t.Fatalf("env %s, literal %s, want %s", w.tab.TypeString(outer.Envs[0].Ref, nil), w.tab.TypeString(lit.EnvRef, nil), w.tab.TypeString(want, nil))
	}
	if len(lit.Captures) != 3 || w.in.Str(lit.Captures[0].Name) != "alpha" || lit.Captures[2].Type != types.I32 {
		t.Fatalf("captures %v", lit.Captures)
	}
	// The literal's read set covers the enclosing unit's whole one.
	if lit.Reads.Len() < outer.Reads.Len() {
		t.Fatalf("literal reads %d, enclosing %d", lit.Reads.Len(), outer.Reads.Len())
	}
	for i, s := range outer.Envs[0].Sources {
		if s.Kind != UseLocal || outer.Locals[s.Local].Name != lit.Captures[i].Name {
			t.Fatalf("source %d: %+v", i, s)
		}
	}
}

// TestLiteralUnitWaits: the literal unit waits for its enclosing
// unit: started together on 8 goroutines in Units() order, the bodies equal
// a sequential run's; a parent with an error closes the record, so the
// literal carries that error and never hangs.
func TestLiteralUnitWaits(t *testing.T) {
	src := capDecls + "F(xs: mut []i32, origin: i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool { return *a - origin < *b })\n}\n\n" +
		"G(xs: mut []i32, limit: i32) {\n\tf := fn(a: &i32, b: &i32): bool { return *a < limit }\n\t_ = f\n}\n"
	seq := func() map[string]string {
		w := newWorld(t, true)
		b := w.add("m", opt{}, src)
		out := map[string]string{}
		for _, id := range b.m.Units() {
			body := w.c.Check(b.m, id)
			out[w.items.Get(id).Key.String()] = codes(w, body.Diags) + " " + w.tab.TypeString(body.EnvRef, nil)
		}
		return out
	}()
	w := newWorld(t, true)
	b := w.add("m", opt{}, src)
	units := b.m.Units()
	res := make([]string, len(units))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, id := range units {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ie := source.Guard("t", source.Span{}, func() {
				body := w.c.Check(b.m, id)
				res[i] = codes(w, body.Diags) + " " + w.tab.TypeString(body.EnvRef, nil)
			}); ie != nil {
				res[i] = "ICE " + ie.What
			}
		}()
	}
	wg.Wait()
	for i, id := range units {
		k := w.items.Get(id).Key.String()
		if res[i] != seq[k] {
			t.Errorf("%s: parallel %q, sequential %q", k, res[i], seq[k])
		}
	}
	if got := seq["m.G#fn0"]; got[:5] != "E0609" {
		t.Errorf("G's literal carries %q, want its parent's E0609", got)
	}
}

// useDecls adds a type with &self, mut self and self methods, and a linear
// type with a by-value method, to capDecls; G starts on line 43.
const useDecls = capDecls + "struct Bag {\n\tn: int\n}\n\nBag.Peek(&self): int {\n\treturn self.n\n}\n\n" +
	"Bag.Push(mut self, x: int) {\n\tself.n = x\n}\n\nTake(xs: mut []i32) {\n}\n\n" +
	"linear struct Res {\n\tid: int\n}\n\nRes.Close(self) {\n}\n\n"

// TestE0609_Uses: inside a const fn literal, a shared borrow, a plain copy
// or field read, a &self method, Cell.Store, a plain element read, a raw-pointer
// write in unsafe, and mutating a `var` local or reborrowing a `mut T`/`mut []T`
// are accepted; mutating a non-`var` capture is E0508; moving any non-plain
// capture (even a `var` linear local or a `mut []T` passed to `into`/generic)
// and mutating a capture inside a contract are E0609 in the types group.
func TestE0609_Uses(t *testing.T) {
	pre := useDecls + "TakeInto(xs: mut []i32 into dst, dst: mut ?mut []i32) {\n}\n\n" +
		"TakeGen[T](x: T) {\n}\n\n" +
		"G(xs: mut []i32, zs: mut []i32, b: Bag, c: Cell[int], p: *i32, r: Res) {\n\tvar n := 0\n\tvar arr: [3]i32 = [1, 2, 3]\n\tvar vb := Bag(n: 0)\n\tvar vr := Res(id: 1)\n\tvar opt: ?mut []i32 = none\n"
	lit := func(body string) string {
		return pre + "\tSort(xs, fn(a: &i32, d: &i32): bool {\n" + body + "\t\treturn true\n\t})\n\tn = 1\n\t_ = n\n\tarr[0] = 1\n\tvb.Push(2)\n\tr.Close()\n\tvr.Close()\n\t_ = opt\n}\n"
	}
	// The literal's first line is 57.
	runCapCases(t, []bodyCase{
		{"shared borrow", lit("\t\tq := &n\n\t\t_ = q\n"), ""},
		{"plain copy and field", lit("\t\tm := n + b.n\n\t\t_ = m\n"), ""},
		{"&self method", lit("\t\t_ = b.Peek()\n"), ""},
		{"Cell.Store", lit("\t\tc.Store(1)\n"), ""},
		{"element read", lit("\t\t_ = arr[1] + zs[0]\n"), ""},
		{"raw pointer write", lit("\t\tunsafe \"p points at a live i32\" {\n\t\t\t*p = 1\n\t\t}\n"), ""},
		{"mut borrow var", lit("\t\tq := mut n\n\t\t*q = 1\n"), ""},
		{"mut slice var", lit("\t\tq := mut arr[0..2]\n\t\t_ = q\n"), ""},
		{"for in mut var", lit("\t\tfor e in mut arr {\n\t\t\t*e = 1\n\t\t}\n"), ""},
		{"mut self var", lit("\t\tvb.Push(1)\n"), ""},
		{"mut self non-var", lit("\t\tb.Push(1)\n"), "E0508@57:3"},
		{"mut parameter reborrow", lit("\t\tTake(zs)\n"), ""},
		{"mut slice write reborrow", lit("\t\tzs[0] = 1\n"), ""},
		{"mut parameter into is move", lit("\t\tTakeInto(zs, mut opt)\n"), "E0609@57:12"},
		{"mut parameter generic is move", lit("\t\tTakeGen(zs)\n"), "E0609@57:11"},
		{"move", lit("\t\tm := zs\n\t\t_ = m\n"), "E0609@57:8"},
		{"by-value self", lit("\t\tr.Close()\n"), "E0609@57:3"},
		{"by-value self var linear", lit("\t\tvr.Close()\n"), "E0609@57:3"},
	})
	// Move stays E0609 in types; non-var mut-self is E0508 in flow.
	w, ds := capDiags(t, lit("\t\tm := zs\n\t\t_ = m\n"))
	if len(ds) != 1 || ds[0].Code != diag.E0609 || fact(ds[0], "reason") != "move" || ds[0].Phase != diag.PhaseTypes {
		t.Errorf("move: %s", dump(w, ds))
	}
	w, ds = capDiags(t, lit("\t\tb.Push(1)\n"))
	if len(ds) != 1 || ds[0].Code != diag.E0508 || fact(ds[0], "reason") != "mut-self" || ds[0].Phase != diag.PhaseFlow {
		t.Errorf("mut-self non-var: %s", dump(w, ds))
	}
	// Mutating a capture inside a contract predicate is E0609 in types.
	contractMut := "Each(const f: fn(): bool): bool {\n\treturn f()\n}\n\n" +
		"H(var x: int): int\n\trequires Each(fn(): bool {\n\t\tx += 1\n\t\treturn x > 0\n\t})\n{\n\treturn x\n}\n"
	w, ds = capDiags(t, contractMut)
	if got := codes(w, ds); got != "E0609@7:3" {
		t.Errorf("contract mut: %q\n%s", got, dump(w, ds))
	}
}

// TestMutCaptureEnvs checks Body.Envs, Modes, EnvRef and Captures for shared,
// mut, reborrow, mixed, nested, and write-through-& captures.
func TestMutCaptureEnvs(t *testing.T) {
	checkEnv := func(t *testing.T, src, fnKey, litKey, wantEnv string, wantModes []CaptureMode) {
		t.Helper()
		w := newWorld(t, true)
		b := w.add("m", opt{}, src)
		var outer, lit *Body
		for _, id := range b.m.Units() {
			body := w.c.Check(b.m, id)
			if len(body.Diags) > 0 {
				t.Fatalf("%s:\n%s", w.items.Get(id).Key, dump(w, body.Diags))
			}
			switch w.items.Get(id).Key.String() {
			case fnKey:
				outer = body
			case litKey:
				lit = body
			}
		}
		if outer == nil || lit == nil || len(outer.Envs) == 0 {
			t.Fatalf("missing bodies: outer=%v lit=%v", outer != nil, lit != nil)
		}
		var env LitEnv
		for _, e := range outer.Envs {
			if w.items.Get(e.Item).Key.String() == litKey {
				env = e
				break
			}
		}
		if got := w.tab.TypeString(env.Ref, nil); got != wantEnv {
			t.Errorf("outer env %s, want %s", got, wantEnv)
		}
		if got := w.tab.TypeString(lit.EnvRef, nil); got != wantEnv {
			t.Errorf("literal env %s, want %s", got, wantEnv)
		}
		if len(env.Modes) != len(wantModes) || len(lit.Captures) != len(wantModes) {
			t.Fatalf("modes %v / captures %d, want %v", env.Modes, len(lit.Captures), wantModes)
		}
		for i, m := range wantModes {
			if env.Modes[i] != m || lit.Captures[i].Mode != m {
				t.Errorf("capture %d: env mode %v, lit mode %v, want %v", i, env.Modes[i], lit.Captures[i].Mode, m)
			}
		}
	}
	t.Run("mixed shared and mut", func(t *testing.T) {
		src := capDecls + "F(xs: mut []i32, limit: i32) {\n\tvar hits := 0\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\tif *a > limit {\n\t\t\thits += 1\n\t\t}\n\t\treturn *a < *b\n\t})\n\t_ = hits\n}\n"
		checkEnv(t, src, "m.F", "m.F#fn0", "mut (hits: mut int, limit: &i32)", []CaptureMode{CapMut, CapShared})
	})
	t.Run("reborrow mut ref and mut slice", func(t *testing.T) {
		src := capDecls + "F(xs: mut []i32, r: mut i32, zs: mut []i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\t*r = *a\n\t\tzs[0] = *b\n\t\treturn *a < *b\n\t})\n}\n"
		checkEnv(t, src, "m.F", "m.F#fn0", "mut (r: mut i32, zs: mut []i32)", []CaptureMode{CapReborrow, CapReborrow})
	})
	t.Run("nested mut propagates to outer literal", func(t *testing.T) {
		src := capDecls + "F(xs: mut []i32) {\n\tvar count := 0\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\tvar ys: [2]i32 = [1, 2]\n\t\tSort(mut ys[..], fn(c: &i32, d: &i32): bool {\n\t\t\tcount += 1\n\t\t\treturn *c < *d\n\t\t})\n\t\treturn *a < *b\n\t})\n\t_ = count\n}\n"
		checkEnv(t, src, "m.F", "m.F#fn0", "mut (count: mut int)", []CaptureMode{CapMut})
		checkEnv(t, src, "m.F#fn0", "m.F#fn0#fn0", "mut (count: mut int)", []CaptureMode{CapMut})
	})
	t.Run("write through captured shared ref stays CapShared for borrow E0507", func(t *testing.T) {
		src := capDecls + "F(xs: mut []i32, p: &i32) {\n\tSort(xs, fn(a: &i32, b: &i32): bool {\n\t\t*p = 1\n\t\treturn *a < *b\n\t})\n}\n"
		checkEnv(t, src, "m.F", "m.F#fn0", "&(p: &&i32)", []CaptureMode{CapShared})
	})
}

// constFnDecls has functions with a const fn parameter; G starts on line 15.
const constFnDecls = "Fwd(x: &int, const f: fn(&int): bool): bool {\n\treturn f(x)\n}\n\n" +
	"Loose(f: fn(&int): bool): bool {\n\treturn true\n}\n\n" +
	"Try(x: &int, const f: fn(&int): E!bool): E!bool {\n\treturn try f(x)\n}\n\nerror E\n\n"

// TestE0705_ConstFnUses: a const fn parameter is only called or passed
// on as the whole argument of a const parameter; bound, returned, stored,
// passed to a non-const parameter, compared, spawned or spawned with,
// captured by a literal, or named in a contract, it is E0705 with the use's
// kind.
func TestE0705_ConstFnUses(t *testing.T) {
	head := constFnDecls + "G(x: &int, s: &Scope, const less: fn(&int): bool): bool {\n"
	runCapCases(t, []bodyCase{
		{"call", head + "\treturn less(x)\n}\n", ""},
		{"forward", head + "\treturn Fwd(x, less)\n}\n", ""},
		{"defer", head + "\tdefer Run(x, less)\n\treturn true\n}\n\nRun(x: &int, const f: fn(&int): bool) {\n\t_ = f(x)\n}\n", ""},
		{"try", constFnDecls + "H(x: &int, const f: fn(&int): E!bool): E!bool {\n\tv := try f(x)\n\treturn try Try(x, f)\n}\n", ""},
		{"bound", head + "\tg := less\n\treturn g(x)\n}\n", "E0705@16:7"},
		{"returned", constFnDecls + "H(const less: fn(&int): bool): fn(&int): bool {\n\treturn less\n}\n", "E0705@16:9"},
		{"stored", head + "\tfs := [less]\n\treturn fs[0](x)\n}\n", "E0705@16:9"},
		{"non-const argument", head + "\treturn Loose(less)\n}\n", "E0705@16:15"},
		{"compared", head + "\treturn less == less\n}\n", "E0308@16:9 E0705@16:17 E0705@16:9"},
		{"captured", head + "\treturn Fwd(x, fn(y: &int): bool { return less(y) })\n}\n", "E0705@16:43"},
		{"s.go call", head + "\ts.go less(x)\n\treturn true\n}\n", "E0301@16:7 E0705@16:7"},
		{"s.go forward", head + "\ts.go Run(x, less)\n\treturn true\n}\n\nRun(x: &int, const f: fn(&int): bool) {\n\t_ = f(x)\n}\n", "E0705@16:14"},
		{"requires", constFnDecls + "H(x: &int, const less: fn(&int): bool): bool requires less(x) {\n\treturn less(x)\n}\n", "E0705@15:55"},
	})
	// The use facts.
	for _, tc := range []struct{ src, use string }{
		{head + "\tg := less\n\treturn g(x)\n}\n", "bound"},
		{head + "\treturn Loose(less)\n}\n", "argument"},
		{head + "\treturn Fwd(x, fn(y: &int): bool { return less(y) })\n}\n", "captured"},
	} {
		w, ds := capDiags(t, tc.src)
		if len(ds) != 1 || ds[0].Code != diag.E0705 || fact(ds[0], "use") != tc.use || fact(ds[0], "param") != "less" {
			t.Errorf("%q: %s", tc.use, dump(w, ds))
		}
	}
}

// captureSeeds are the accept and reject sources of TestE0609_Uses and
// TestE0705_ConstFnUses, for FuzzCheck's seed corpus.
func captureSeeds() []string {
	pre := useDecls + "G(xs: mut []i32, zs: mut []i32, b: Bag, c: Cell[int], p: *i32, r: Res) {\n\tvar n := 0\n\tvar arr: [3]i32 = [1, 2, 3]\n"
	var out []string
	for _, body := range []string{"\t\tq := &n\n\t\t_ = q\n", "\t\t_ = b.Peek()\n", "\t\tc.Store(1)\n", "\t\tq := mut n\n\t\t*q = 1\n",
		"\t\tfor e in mut arr {\n\t\t\t*e = 1\n\t\t}\n", "\t\tb.Push(1)\n", "\t\tTake(zs)\n", "\t\tm := zs\n\t\t_ = m\n", "\t\tr.Close()\n"} {
		out = append(out, pre+"\tSort(xs, fn(a: &i32, d: &i32): bool {\n"+body+"\t\treturn true\n\t})\n\tn = 1\n\t_ = n\n\tarr[0] = 1\n\tr.Close()\n}\n")
	}
	head := constFnDecls + "G(x: &int, s: &Scope, const less: fn(&int): bool): bool {\n"
	for _, body := range []string{"\treturn less(x)\n}\n", "\treturn Fwd(x, less)\n}\n", "\tg := less\n\treturn g(x)\n}\n",
		"\treturn Loose(less)\n}\n", "\ts.go less(x)\n\treturn true\n}\n", "\treturn Fwd(x, fn(y: &int): bool { return less(y) })\n}\n"} {
		out = append(out, head+body)
	}
	// The programs of constcapture_test.go: a const F, an alias of a fn
	// type, captures in contracts and an invariant, a fields loop.
	out = append(out,
		constNotFnDecls+"Mk(k: i32): Box2[fn(i32): i32] {\n\treturn Keep(fn(x: i32): i32 { return x + k })\n}\n",
		constNotFnDecls+"Fw(const less: fn(i32): i32): Box2[fn(i32): i32] {\n\treturn Keep(less)\n}\n",
		"type Less = fn(&int, &int): bool\n\nAp(const f: fn(): int): int {\n\treturn f()\n}\n\nOuter(const less: Less): int {\n\ta := 1\n\treturn Ap(fn(): int {\n\t\tif less(&a, &a) {\n\t\t\treturn 1\n\t\t}\n\t\treturn 0\n\t})\n}\n",
		contractCapDecls+"Has(xs: []i32, limit: i32): i32\n\trequires Any(xs, fn(x: &i32): bool { return *x > limit })\n\tensures !Any(xs, fn(x: &i32): bool { return *x == result + limit })\n{\n\treturn 0\n}\n",
		invariantCapSrc,
		"Ap(const f: fn(): i32): i32 {\n\treturn f()\n}\n\nCount[A: Fields[Format] + Plain](args: A): i32 {\n\tvar n: i32 = 0\n\tfor _, _ in fields(&args) {\n\t\tn += Ap(fn(): i32 {\n\t\t\tz: i32 = 3\n\t\t\treturn Ap(fn(): i32 { return z })\n\t\t})\n\t}\n\treturn n\n}\n")
	return out
}

// TestLiteralUnderParseError: a capturing literal whose enclosing unit
// never typed it and reported nothing (a parse error recovered around it)
// gets no environment and no ICE (FuzzCheck 63976af49801fb2b).
func TestLiteralUnderParseError(t *testing.T) {
	checkNoICE(t, "0000000000000000000000000000000000\nA(000000000){B00\nr:0000000\nfn(A:r")
	checkNoICE(t, "A(r: int) {\n\tB(fn(): int { return r }\n}\n")
}
