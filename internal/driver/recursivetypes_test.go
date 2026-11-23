// Why: the fixes for narrowing, nested package roots, fn literal flows and
// polymorphically recursive types each had holes that must stay closed.
// What: regression tests of `if T is X` reborrows, E0202 for a bin root in
// a nested package, a literal's expected `from` clause cold and warm, walks
// over polymorphically recursive types (TestRecursiveTypeWalkProbes over
// testdata/cd7r4), and warm canonical rounds over std.core.
// How: each test checks, builds or runs small programs, cold and on a warm
// cache, and compares verdicts; the recursive-type cases run under a
// watchdog so a walk that never ends fails fast.

package driver

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNarrowMutRefUnusedInBranch: `if T is X` reborrows a
// `mut T` parameter only when the branch names it, so a shared loan of
// `*a` held across the test (used after it, or only before it) is no
// conflict; the program builds and runs. TestNarrowMutRefParam keeps
// the `**b = **a` case, where the branch names both; a method's `mut self`
// named in the branch is still narrowed, and the write reaches the caller.
func TestNarrowMutRefUnusedInBranch(t *testing.T) {
	t.Parallel()
	const main = "\nexport Main(sys: &System) {\n    var x: i32 = 5\n    n := Size(mut x)\n    sys.Print(\"{n}\\n\", (n)) else {}\n}\n"
	after := "Size[T](a: mut T): int {\n    r: &T = &*a\n    var n := 0\n    if T is i32 {\n        n = 4\n    }\n    _ = r\n    return n\n}\n" + main
	before := "Size[T](a: mut T): int {\n    r: &T = &*a\n    _ = r\n    var n := 0\n    if T is i32 {\n        n = 4\n    }\n    return n\n}\n" + main
	for name, src := range map[string]string{"used-after": after, "used-before": before} {
		if r := checkText(t, src); len(r.Report.Diags) != 0 {
			t.Errorf("%s: want clean:\n%s", name, diagLines(r))
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, t.TempDir()); out != "4\n" {
		t.Errorf("got %q, want %q", out, "4\n")
	}
	method := "struct G[T] {\n    v: T\n}\n\nG[T].Set(mut self) {\n    if T is i32 {\n        self.v = 4\n    }\n}\n\n" +
		"export Main(sys: &System) {\n    var g := G[i32](v: 1)\n    g.Set()\n    n := g.v\n    sys.Print(\"{n}\\n\", (n)) else {}\n}\n"
	mdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mdir, "main.mk"), []byte(method), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, mdir, t.TempDir()); out != "4\n" {
		t.Errorf("method: got %q, want %q", out, "4\n")
	}
}

// TestBinRootInNestedPackageIsE0202: a bin whose root is a
// directory with its own pkg.mk, or one under such a directory, is another
// package's, so the walk forms no module of it: E0202 invalid at its root,
// as for every root the walk skips, never a clean check that builds no
// executable for it.
func TestBinRootInNestedPackageIsE0202(t *testing.T) {
	t.Parallel()
	const mainText = "export Main(sys: &System) {\n}\n"
	bin := func(name, root string) string {
		return `pkg.Bin(name: "` + name + `", root: "` + root + `", link_script: "", cpu: "", subsystem: "console")`
	}
	for _, root := range []string{"tool", "repro/r1/src"} {
		t.Run(root, func(t *testing.T) {
			t.Parallel()
			nested := strings.TrimSuffix(root, "/src")
			r := check(t, Config{StdDir: fakeStd(t), Root: writeTree(t, map[string]string{
				"pkg.mk":           manifest(map[string]string{"bins": "[" + bin("app", "app") + ", " + bin("tool", root) + "]"}),
				"app/main.mk":      mainText,
				nested + "/pkg.mk": manifest(map[string]string{"name": `"inner"`, "bins": "[" + bin("tool", "src") + "]"}),
				root + "/main.mk":  mainText,
			})})
			d := only(t, r, "E0202")
			if fact(d, "reason") != "invalid" || fact(d, "manifest") != "bins[1].root" || !strings.Contains(d.Message, "the module walk skips it") {
				t.Errorf("reason %q manifest %q message %q", fact(d, "reason"), fact(d, "manifest"), d.Message)
			}
		})
	}
}

// pickFrom is a function whose const fn parameter says `from y`, called
// with a literal returning ret.
func pickFrom(ret string) string {
	return pickSig("Pick(a: &i32, b: &i32, const f: fn(x: &i32, y: &i32): &i32 from y): i32", ret)
}

// pickSig is pickFrom with Pick's signature sig.
func pickSig(sig, ret string) string {
	return sig + " {\n    return *f(a, b)\n}\n\n" +
		"export Main(sys: &System) {\n    p: i32 = 1\n    q: i32 = 2\n    r := Pick(&p, &q, fn(x: &i32, y: &i32): &i32 {\n        _ = x\n        _ = y\n        return " + ret + "\n    })\n" +
		"    sys.Print(\"{r}\\n\", (r)) else {}\n}\n"
}

// TestLiteralTakesExpectedFrom: a literal written
// where a fn type with a `from` clause is expected takes that clause with
// the type's other flows (the language reference: "It takes that
// type's flows and writes none"): returning `y` against `from y` checks and
// runs; returning `x` is E0602 at the return. A literal with no such type
// whose result has two possible sources is E0601, whose alternatives are
// what a literal can do, never a `from` it cannot write.
func TestLiteralTakesExpectedFrom(t *testing.T) {
	t.Parallel()
	if r := checkText(t, pickFrom("y")); len(r.Report.Diags) != 0 {
		t.Errorf("want clean:\n%s", diagLines(r))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(pickFrom("y")), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, t.TempDir()); out != "2\n" {
		t.Errorf("got %q, want %q", out, "2\n")
	}
	// A generic fn type: the literal is typed against its instance.
	const generic = "Pick[T: Plain](a: &T, b: &T, const f: fn(x: &T, y: &T): &T from y): T"
	if r := checkText(t, pickSig(generic, "y")); len(r.Report.Diags) != 0 {
		t.Errorf("generic fn type: want clean:\n%s", diagLines(r))
	}
	for _, src := range []string{pickFrom("x"), pickSig(generic, "x")} {
		bad := checkText(t, src)
		if got := codes(bad); !slices.Equal(got, []string{"E0602"}) {
			t.Fatalf("returning x against `from y`: codes %q, want [E0602]:\n%s", got, diagLines(bad))
		}
	}
	bare := checkText(t, "export Two(): int {\n    g := fn(x: &i32, y: &i32): &i32 {\n        _ = x\n        return y\n    }\n    _ = g\n    return 2\n}\n")
	if got := codes(bare); !slices.Equal(got, []string{"E0601"}) {
		t.Fatalf("bare literal: codes %q, want [E0601]:\n%s", got, diagLines(bare))
	}
	d := bare.Report.Diags[0].Diag
	if len(d.Fixes) != 0 || len(d.Alternatives) == 0 {
		t.Errorf("bare literal: fixes %+v alternatives %q", d.Fixes, d.Alternatives)
	}
	for _, a := range d.Alternatives {
		if strings.HasPrefix(a, "from ") {
			t.Errorf("bare literal: alternative %q is a clause a literal cannot write", a)
		}
	}
}

// TestLiteralFlowsWarm: the flows a literal takes from its
// expected fn type give the same verdicts on a warm cache, after a body edit
// in a module the literals call: the literal storing an arena copy and the
// one returning `y` stay clean, and the store of `word` stays E0604 at the
// same place.
func TestLiteralFlowsWarm(t *testing.T) {
	t.Parallel()
	const app = "import util\n\nstruct Names {\n    arena: &Arena\n    all: List[str]\n}\n\n" +
		"Each[S](state: mut S, const step: (fn(state: mut S, word: str): OutOfMemory!void *state into state)): OutOfMemory!void *state into state {\n    try step(state, \"one\")\n}\n\n" +
		"export Good(a: &Arena): OutOfMemory!int {\n    var names := Names(arena: a, all: List[str].New(a))\n    try Each(mut names, fn(state: mut Names, word: str): OutOfMemory!void {\n        _ = util.Note(1)\n        try state.all.Push(try state.arena.Dup(word))\n    })\n    return names.all.Len()\n}\n\n" +
		"export Bad(a: &Arena): OutOfMemory!int {\n    var names := Names(arena: a, all: List[str].New(a))\n    try Each(mut names, fn(state: mut Names, word: str): OutOfMemory!void {\n        _ = util.Note(2)\n        try state.all.Push(word)\n    })\n    return names.all.Len()\n}\n\n" +
		"Pick(a: &i32, b: &i32, const f: fn(x: &i32, y: &i32): &i32 from y): i32 {\n    return *f(a, b)\n}\n\n" +
		"export Picked(): i32 {\n    p: i32 = 1\n    q: i32 = 2\n    return Pick(&p, &q, fn(x: &i32, y: &i32): &i32 {\n        _ = x\n        _ = util.Note(3)\n        return y\n    })\n}\n"
	note := func(body string) string { return "export Note(n: int): int {\n    return " + body + "\n}\n" }
	dir := writeTree(t, pkgFiles(map[string]string{"app/app.mk": app, "util/util.mk": note("n")}))
	cache := t.TempDir()
	run := func() []string {
		s, err := NewSession(Config{Root: dir, CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		return lines(s.Check())
	}
	cold := run()
	if len(cold) != 1 || !strings.HasSuffix(cold[0], ":E0604") || !strings.Contains(cold[0], "app.mk:") {
		t.Fatalf("cold: want one E0604 in app.mk, got %q", cold)
	}
	if err := os.WriteFile(filepath.Join(dir, "util", "util.mk"), []byte(note("n + 1")), 0o644); err != nil {
		t.Fatal(err)
	}
	if warm := run(); !slices.Equal(warm, cold) {
		t.Errorf("warm after the body edit: %q, cold %q", warm, cold)
	}
	if again := run(); !slices.Equal(again, cold) {
		t.Errorf("warm, unchanged: %q, cold %q", again, cold)
	}
	// The E0601 of a literal no fn type settles is reported by its
	// enclosing body, so a warm check keeps it.
	bare := "import util\n\nexport Two(): int {\n    g := fn(x: &i32, y: &i32): &i32 {\n        _ = x\n        _ = util.Note(4)\n        return y\n    }\n    _ = g\n    return 2\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "app", "app.mk"), []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}
	coldBare := run()
	if len(coldBare) != 1 || !strings.HasSuffix(coldBare[0], ":E0601") {
		t.Fatalf("bare literal: want one E0601, got %q", coldBare)
	}
	if err := os.WriteFile(filepath.Join(dir, "util", "util.mk"), []byte(note("n + 2")), 0o644); err != nil {
		t.Fatal(err)
	}
	if warm := run(); !slices.Equal(warm, coldBare) {
		t.Errorf("bare literal, warm after the body edit: %q, cold %q", warm, coldBare)
	}
	if again := run(); !slices.Equal(again, coldBare) {
		t.Errorf("bare literal, warm, unchanged: %q, cold %q", again, coldBare)
	}
}

// TestPolymorphicRecursionInstanceBuilds: a polymorphically
// recursive struct used through a parameter (`F(g: &G[int])` reading
// `g.next`) checks, builds and runs; check's facts walk and borrow's
// layer-2 walk of the parameter's referents (abstractNodes) each met a new
// type at every hop and never ended.
func TestPolymorphicRecursionInstanceBuilds(t *testing.T) {
	t.Parallel()
	const src = "struct G[T] {\n    v: T\n    next: ?&G[?T]\n}\n\nF(g: &G[int]): int {\n    n := g.next else {\n        return g.v\n    }\n    w := n.v else {\n        return 0\n    }\n    return g.v + w\n}\n\n" +
		"export Main(sys: &System) {\n    inner := G[?int](v: some(5), next: none)\n    g := G[int](v: 3, next: some(&inner))\n    r := F(&g)\n    sys.Print(\"{r}\\n\", (r)) else {}\n}\n"
	if r := checkText(t, src); len(r.Report.Diags) != 0 {
		t.Fatalf("want clean:\n%s", diagLines(r))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, t.TempDir()); out != "8\n" {
		t.Errorf("got %q, want %q", out, "8\n")
	}
}

// TestPolymorphicRecursionWriteThroughMut: the same type
// written through a `mut G[int]` parameter. Three walks follow references
// by TypeID alone and meet a new instance at every hop: borrow's
// storesFirst (firstTakes), its direct-write walk (walkExtra) and the
// driver's deep layout part of pack keys (LayoutPart). Each stops at a
// per-constructor bound; without walkExtra's bound this program hangs in
// mk check.
func TestPolymorphicRecursionWriteThroughMut(t *testing.T) {
	t.Parallel()
	const src = "struct G[T] {\n    v: T\n    next: ?&G[?T]\n}\n\nF(g: &G[int]): int {\n    n := g.next else {\n        return g.v\n    }\n    w := n.v else {\n        return 0\n    }\n    return g.v + w\n}\n\n" +
		"Bump(g: mut G[int]) {\n    g.v += 1\n}\n\n" +
		"export Main(sys: &System) {\n    inner := G[?int](v: some(5), next: none)\n    var g := G[int](v: 3, next: some(&inner))\n    Bump(mut g)\n    r := F(&g)\n    sys.Print(\"{r}\\n\", (r)) else {}\n}\n"
	if r := checkText(t, src); len(r.Report.Diags) != 0 {
		t.Fatalf("want clean:\n%s", diagLines(r))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, t.TempDir()); out != "9\n" {
		t.Errorf("got %q, want %q", out, "9\n")
	}
}

// TestPolymorphicRecursionThroughSliceAndVariant: the same
// recursion through an enum variant (the slice case is TestRecursiveTypeWalkProbes'
// "slice"). Without the bounds it hangs in mk check, as the ?& field did.
func TestPolymorphicRecursionThroughSliceAndVariant(t *testing.T) {
	t.Parallel()
	const src = "enum E[T] {\n    Leaf(v: T)\n    Node(next: &E[?T])\n}\n\nF(e: &E[int]): int {\n    return 7\n}\n\n" +
		"Set(e: mut E[int]) {\n    *e = E[int].Leaf(v: 1)\n}\n\n" +
		"export Main(sys: &System) {\n    var e := E[int].Leaf(v: 3)\n    Set(mut e)\n    n := F(&e)\n    sys.Print(\"{n}\\n\", (n)) else {}\n}\n"
	if r := checkText(t, src); len(r.Report.Diags) != 0 {
		t.Fatalf("want clean:\n%s", diagLines(r))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := buildRun(t, dir, t.TempDir()); out != "7\n" {
		t.Errorf("got %q, want %q", out, "7\n")
	}
}

// TestRecursiveTypeWalkProbes: probes of the recursive-type walks, in
// testdata/cd7r4/<case>/main.mk, each checked clean, built and run:
//   - slice: kids: []S[?T], with Main building an S[int] (LayoutPart's walk);
//   - view: a view V[T] with next: ?&V[?T] (only LayoutPart sees it);
//   - br2: G[T] growing along two fields at once (LayoutPart's 4096 bound);
//   - r1: next: ?&(a: G[?T], b: int), a record between the instances
//     (walkFrom and abstractNodes' layer-2 walk meet only the records);
//   - h1, h3: G[int] in a module whose view keeps a value (fnStoresInto and
//     holdsItem);
//   - w65, n66, t4kb: a wide type, a nest that shrinks and a large closure,
//     which a bound on the instances met turned into E0602;
//   - gr70, alt140, altrec140: long paths through unrelated types (W[S1],
//     W[?S2], ... and records of unrelated shapes), which a growth by size
//     alone turned into E0602;
//   - f3c1, f3c1s, f3c1e, f3cg1: a function returning the reference out of
//     G[int], S[int] or E[int], and a global ?&G[int], on which check's
//     flow walks and codegen's liveErrors overflowed or hung; f4c1fg: a
//     static G[?int] returned `from x`, a false E0602;
//   - f3cg1two, f4two: the same through two growing fields (?&G[?T] and
//     ?&G[[2]T]), whose instances branch, so a walk that goes on past a
//     stop meets thousands of them unless it answers at the first growth.
//
// Each case has a watchdog: a walk that does not end fails the binary in
// minutes, with every goroutine's stack, not at the package's timeout.
func TestRecursiveTypeWalkProbes(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, want string }{
		{"slice", "4\n"}, {"view", "4\n"}, {"br2", "9\n"}, {"r1", "4\n"},
		{"h1", "3\n"}, {"h3", "ok\n"}, {"w65", "none\n"}, {"n66", "none\n"}, {"t4kb", "none\n"},
		{"gr70", "none\n"}, {"alt140", "none\n"}, {"altrec140", "none\n"},
		// Check's flow walks and codegen's liveErrors, and a static of
		// such a type that took stores.
		{"f3c1", "none\n"}, {"f3c1s", "0\n"}, {"f3c1e", "none\n"}, {"f3cg1", "none\n"}, {"f4c1fg", "none\n"},
		{"f3cg1two", "none\n"}, {"f4two", "5\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join("testdata", "cd7r4", c.name, "main.mk"))
			if err != nil {
				t.Fatal(err)
			}
			watch := time.AfterFunc(15*time.Minute, func() {
				debug.SetTraceback("all")
				panic("TestRecursiveTypeWalkProbes/" + c.name + ": check and build did not end in 15 minutes")
			})
			defer watch.Stop()
			// The check and the build share one cache, so the build
			// finds std checked.
			dir, cacheDir := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "main.mk"), src, 0o644); err != nil {
				t.Fatal(err)
			}
			s, err := NewSession(Config{Root: filepath.Join(dir, "main.mk"), CacheDir: cacheDir})
			if err != nil {
				t.Fatal(err)
			}
			if r := s.Check(); len(r.Report.Diags) != 0 {
				t.Fatalf("want clean:\n%s", diagLines(r))
			}
			if out := buildRun(t, dir, cacheDir); out != c.want {
				t.Errorf("got %q, want %q", out, c.want)
			}
		})
	}
}

// TestStaticsThatTakeStoresStillDo: the closure answer at the bound
// (borrow's closureTakes) keeps E0602 for a static of a polymorphically
// recursive type that can take a store: a G[?&Mutex[?&i32]] whose argument is
// a carrier Mutex, and a G[int] whose declaration reaches an H[T] holding ?&Mutex[T]
// (testdata/cd7r4/f4mtx, f4mutex).
func TestStaticsThatTakeStoresStillDo(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"f4mtx", "f4mutex"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join("testdata", "cd7r4", name, "main.mk"))
			if err != nil {
				t.Fatal(err)
			}
			r := checkText(t, string(src))
			if len(r.Report.Diags) != 1 || r.Report.Diags[0].Diag.Code.String() != "E0602" {
				t.Fatalf("want one E0602 at `return &GI`, got:\n%s", diagLines(r))
			}
		})
	}
}

// TestCanonWarm_ImporterOfWholeStdCore (the loadsWhole guard in
// resolveClosure): a package check warms one cache with every std module,
// std.core's importers among them; mk fix --std std.core --canonical
// --dry-run on that cache then finds the same rounds as on a cold one and
// writes nothing; and the package checks again with the diagnostics it
// had, so no front-cache hit was served without its resolve Info.
func TestCanonWarm_ImporterOfWholeStdCore(t *testing.T) {
	t.Parallel()
	const coreFile = "struct CountWarm {\n    n: int\n}\n\nCountWarm.Next(mut self): ?int {\n    if self.n == 0 {\n        return none\n    }\n    self.n -= 1\n    return some(self.n)\n}\n\n" +
		"export SumWarm(k: int): int {\n    var total := 0\n    var it := CountWarm(n: k)\n    for x := it.Next() {\n        total += x\n    }\n    return total\n}\n"
	dir := copyStd(t, map[string]string{"core/canonwarm.mk": coreFile}, nil)
	pkg := writeTree(t, pkgFiles(map[string]string{"app/app.mk": "export F(xs: List[int]): int {\n    return xs.Len() + SumWarm(3)\n}\n\nexport G(): int {\n    return \"two\"\n}\n"}))
	cache := t.TempDir()
	checkPkg := func() []string {
		s, err := NewSession(Config{Root: pkg, StdDir: dir, CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		return lines(s.Check())
	}
	first := checkPkg()
	if len(first) != 1 || !strings.Contains(first[0], "app.mk:") {
		t.Fatalf("the package: want one diagnostic at G's return, got %q", first)
	}
	fix := func(cache string) *FixResult {
		s, err := NewSession(Config{StdTests: "std.core", StdDir: dir, CacheDir: cache})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.Fix(FixOptions{Canonical: true, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	warm, cold := fix(cache), fix(t.TempDir())
	applied := func(res *FixResult) int {
		n := 0
		for _, r := range res.Rounds {
			n += len(r.Applied)
		}
		return n
	}
	if applied(cold) == 0 || applied(warm) != applied(cold) || warm.Stopped != cold.Stopped {
		t.Errorf("fix rounds: warm %d applied (%q), cold %d (%q)", applied(warm), warm.Stopped, applied(cold), cold.Stopped)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "core", "canonwarm.mk")); string(got) != coreFile {
		t.Errorf("--dry-run wrote std.core:\n%s", got)
	}
	if again := checkPkg(); !slices.Equal(again, first) {
		t.Errorf("the package after the fix run: %q, first %q", again, first)
	}
}

// TestCtxStoresThroughSharedState: the repro's `Ctx`
// shape, a `&Ctx` carrying the allocator and a reference to shared state,
// stores a heap value into that state under the clause E0604 names,
// `*c into c`, and checks. What remains of the repro is its `Main`, which
// takes the state apart while `c`'s store still reaches it: E0503 at
// `Shared2(m) := st`, held by st (one loan set per struct).
func TestCtxStoresThroughSharedState(t *testing.T) {
	t.Parallel()
	const decls = "struct Shared2 {\n    m: Mutex[TextMap[HeapText]]\n}\n\nstruct Ctx {\n    st: &Shared2\n    a: &Alloc\n    io: Io\n}\n\n" +
		"PutB(c: &Ctx, key: str, value: str): Canceled|OutOfMemory!void *c into c {\n    var g := try c.st.m.Lock(c.io)\n    defer g.Unlock()\n    var v := HeapText.New(c.a)\n    onerr v.Free(c.a)\n    try v.Push(c.a, value)\n    try g.Mut().Set(c.a, key, v)\n}\n\n"
	const head = "export Main(sys: &System) {\n    a := sys.heap.Alloc()\n    st := Shared2(m: Mutex.New(TextMap[HeapText].New(a)))\n    c := Ctx(st: &st, a: a, io: sys.io)\n    PutB(&c, \"k\", \"v\") else { }\n"
	if r := checkText(t, decls+"Use(c: &Ctx): Canceled|OutOfMemory!void *c into c {\n    try PutB(c, \"k\", \"v\")\n}\n"); len(r.Report.Diags) != 0 {
		t.Errorf("PutB under `*c into c`: want clean:\n%s", diagLines(r))
	}
	r := checkText(t, decls+head+"    Shared2(m) := st\n    m.Into().Free(a)\n}\n")
	if got := codes(r); !slices.Equal(got, []string{"E0503"}) {
		t.Fatalf("the repro's Main: codes %q, want [E0503]:\n%s", got, diagLines(r))
	}
	if d := r.Report.Diags[0].Diag; fact(d, "holder") != "st" || !strings.Contains(d.Message, "st conflicts with a live shared loan") {
		t.Errorf("E0503: holder %q message %q", fact(d, "holder"), d.Message)
	}
}

// TestClosureReadsColdWarm: the closure answer at the bound reads
// the declarations the closure meets, so it records their fields and shape.
// G[T]'s next is ?&G[m.P[T]], and m's private N behind P's private field
// toggles between `a: int` (no store: clean) and `c: Mutex[?&i32]` (E0602 at
// Pick's return). On one warm cache every step gives the cold answer;
// without the reads the warm cache kept "clean" after the toggle. (An
// error's payload fields are recorded too, but a payload is plain and holds
// no reference, so no payload toggle changes the closure's answer.)
func TestClosureReadsColdWarm(t *testing.T) {
	t.Parallel()
	main := "import m\n\nstruct G[T] {\n    v: T\n    next: ?&G[m.P[T]]\n}\n\nvar GI: G[int] = G[int](v: 1, next: none)\n\n" +
		"Pick(x: &i32): &G[int] from x {\n    return &GI\n}\n\nexport Main(sys: &System) {\n    n: i32 = 7\n    g := Pick(&n)\n    w := g.v\n    sys.Print(\"{w}\\n\", (w)) else {}\n}\n"
	for _, c := range []struct{ name, tmpl, without, with string }{
		{"struct", "struct N {\n    FIELD\n}\n\nexport struct P[T] {\n    export t: T\n    export r: ?mut i32\n    q: ?&N\n}\n", "a: int", "c: Mutex[?&i32]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{
				"pkg.mk":  manifest(map[string]string{"name": `"cw"`}),
				"main.mk": main,
				"m/m.mk":  strings.Replace(c.tmpl, "FIELD", c.without, 1),
			}
			dir := writeTree(t, files)
			warm := t.TempDir()
			for i, with := range []bool{false, true, false, true} {
				field := c.without
				if with {
					field = c.with
				}
				if err := os.WriteFile(filepath.Join(dir, "m", "m.mk"), []byte(strings.Replace(c.tmpl, "FIELD", field, 1)), 0o644); err != nil {
					t.Fatal(err)
				}
				var got [2]string
				for j, cache := range []string{warm, t.TempDir()} {
					got[j] = strings.Join(baseLines(check(t, Config{Root: dir, CacheDir: cache})), " ")
				}
				if got[0] != got[1] {
					t.Errorf("step %d (with %v): warm %q, cold %q", i+1, with, got[0], got[1])
				}
				if e := strings.Contains(got[1], "E0602"); e != with {
					t.Errorf("step %d (with %v): cold %q, want E0602 %v", i+1, with, got[1], with)
				}
			}
		})
	}
}
