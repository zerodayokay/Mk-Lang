// Why: defects found by writing real programs in Mk (fix loops that
// stalled, query answers that missed, false borrow errors) must stay fixed.
// What: regression tests, each named after the dogfood defect it closes,
// over mk fix (fixToClean), mk query (usesOf), check and the linker.
// How: each test writes a small program, runs the command and compares the
// fixed text, the query answer or the diagnostics.

package driver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixToClean writes src as a one-file program, runs mk fix on it, and
// fails unless the loop ends clean; it returns the fixed text.
func fixToClean(t *testing.T, src string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "main.mk")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{Root: file, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Fix(FixOptions{})
	if err != nil || res.Stopped != "clean" {
		t.Fatalf("mk fix: %v, stopped %q:\n%s", err, res.Stopped, diagLines(res.Final))
	}
	out, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestFixMovesClauseToCallers: a proven clause fix that turns
// one E0604 into one per caller is progress (movedUp no longer compares the
// total count), so mk fix writes the clause on Piece, then on First and
// Second, and ends clean; it stopped with "no progress". The other cases
// have the same cause: two callers of Grow's fix (three after it), and a
// chain of stores.
func TestFixMovesClauseToCallers(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"pack-08": `error Cut

mut view struct Whole {
    data: []u8
    pos: int
}

Whole.Fill(mut self, buf: mut []u8): Cut!int *self into self {
    n := if buf.Len() < self.data.Len() - self.pos { buf.Len() } else { self.data.Len() - self.pos }
    if n == 0 {
        fail Cut
    }
    (mut buf[..n]).CopyFrom(self.data[self.pos..self.pos + n])
    self.pos += n
    return n
}

Piece(src: mut Whole, buf: mut []u8): Cut!int {
    return try src.Fill(buf)
}

First(src: mut Whole, buf: mut []u8): Cut!int {
    return try Piece(src, buf)
}

Second(src: mut Whole, buf: mut []u8): Cut!int {
    return try Piece(src, buf)
}

export Main(sys: &System): int {
    _ = sys
    var w := Whole(data: "abc".AsBytes(), pos: 0)
    var buf: [2]u8 = [0, 0]
    n := First(mut w, mut buf[..]) else {
        return 1
    }
    m := Second(mut w, mut buf[..]) else {
        return 1
    }
    return n + m
}
`,
		"orderbook-01": `struct Inner {
    xs: HeapList[HeapList[int]]
}

Inner.Add(mut self, a: &Alloc into self): OutOfMemory!void {
    var inner := HeapList[int].New(a)
    onerr inner.Free(a)
    try inner.Push(a, 1)
    try self.xs.Push(a, inner)
}

struct Outer {
    left: Inner
    right: Inner
}

Outer.Grow(mut self, a: &Alloc, left: bool): OutOfMemory!void {
    if left {
        try self.left.Add(a)
    } else {
        try self.right.Add(a)
    }
}

Outer.One(mut self, a: &Alloc): OutOfMemory!void {
    try self.Grow(a, true)
}

Outer.Two(mut self, a: &Alloc): OutOfMemory!void {
    try self.Grow(a, false)
}

Outer.Three(mut self, a: &Alloc): OutOfMemory!void {
    try self.Grow(a, true)
}

export Main(sys: &System): OutOfMemory!void {
    a := sys.heap.Alloc()
    var o := Outer(left: Inner(xs: HeapList[HeapList[int]].New(a)), right: Inner(xs: HeapList[HeapList[int]].New(a)))
    _ = o.One(a) else { }
    _ = o.Two(a) else { }
    _ = o.Three(a) else { }
    Outer(left, right) := o
    Inner(xs: l) := left
    Inner(xs: r) := right
    l.Free(a)
    r.Free(a)
}
`,
		"lox-01": `struct Log {
    lines: List[str]
}

Log.Add(mut self, line: str): OutOfMemory!void {
    try self.lines.Push(line)
}

Log.Warn(mut self, line: str): OutOfMemory!void {
    try self.Add(line)
}

Log.Twice(mut self, line: str): OutOfMemory!void {
    try self.Warn(line)
    try self.Warn(line)
}

export Main(sys: &System): OutOfMemory!void {
    var log := Log(lines: List[str].New(sys.arena))
    try log.Twice("x")
}
`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if out := fixToClean(t, src); out == src {
				t.Errorf("mk fix wrote nothing")
			}
		})
	}
}

// TestVarDestructureFix: E0508's proven "add var" fix on a name
// bound by a destructuring binding inserts `var ` before the pattern, the
// form the language allows; it went inside the pattern (`Wrap(var n, ..)`),
// which does not parse, and mk fix made no progress.
func TestVarDestructureFix(t *testing.T) {
	t.Parallel()
	out := fixToClean(t, `struct Wrap {
    n: int
    k: int
}

Bump(x: mut int) {
    *x += 1
}

Use(w: Wrap): int {
    Wrap(n, ..) := w
    Bump(mut n)
    return n
}
`)
	if !strings.Contains(out, "    var Wrap(n, ..) := w\n") {
		t.Errorf("fixed text:\n%s", out)
	}
}

// TestUnusedElseBindingFix: E0208 on an unused `else e`
// binding carries the proven fix that deletes ` e`, so mk fix applies it
// and stops clean; it had no fix and mk fix stopped ("no proven fix").
func TestUnusedElseBindingFix(t *testing.T) {
	t.Parallel()
	out := fixToClean(t, `Parse(s: str): i64 {
    n := s.ParseInt() else e {
        return 0
    }
    return n
}
`)
	if !strings.Contains(out, "    n := s.ParseInt() else {\n") {
		t.Errorf("fixed text:\n%s", out)
	}
}

// TestQuerySigWholeDoc: mk query sig prints every `///` line of
// an item, cut only by the answer's cap, which Truncated counts; the doc was
// cut at three lines with `///...` and "truncated": 0.
func TestQuerySigWholeDoc(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "q.mk"), []byte("export Main(sys: &System) {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := queryOne(t, dir, "sig", "mem.Fixed.New")
	doc := 0
	for _, l := range a.Answer {
		if l == "///..." {
			t.Errorf("doc cut with ///...:\n%s", a.Text())
		}
		if strings.HasPrefix(l, "///") {
			doc++
		}
	}
	if doc < 4 || a.Truncated != 0 {
		t.Errorf("want Fixed.New's whole doc (at least 4 lines), truncated 0:\n%s", a.Text())
	}
	long := queryOne(t, dir, "sig", "compress.Inflater.Read")
	shown := 0
	for _, l := range long.Answer {
		if strings.HasPrefix(l, "///") {
			shown++
		}
	}
	if shown <= 3 || (len(strings.Join(long.Answer, "\n")) > QueryCapBytes) {
		t.Errorf("Inflater.Read: %d doc lines shown, truncated %d:\n%s", shown, long.Truncated, long.Text())
	}
}

// usesOf runs mk query uses on a one-file program and returns its items.
func usesOf(t *testing.T, src string) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "q.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	a := queryOne(t, dir, "uses")
	if a.Uses == nil {
		t.Fatalf("no uses listing:\n%s", a.Text())
	}
	out := map[string][]string{}
	for _, u := range *a.Uses {
		out[u.Item] = u.Sites
	}
	return out
}

// TestUsesSliceLen: xs.Len() on a []T (and on a sub-slice)
// is a site of std.core.Slice:Len; it was no site at all.
func TestUsesSliceLen(t *testing.T) {
	t.Parallel()
	u := usesOf(t, "Count(xs: []int): int {\n    return xs.Len()\n}\n\nexport Main(sys: &System) {\n    var a: [2]int = [1, 2]\n    _ = Count(a[..]) + a[..].Len()\n}\n")
	if got := u["std.core.Slice:Len"]; len(got) != 2 {
		t.Errorf("Slice:Len sites %v, want 2; all: %v", got, u)
	}
}

// TestUsesFieldRead: reading a field of a std struct uses the
// struct (info.size uses std.fs.Info), not only a site naming the type.
func TestUsesFieldRead(t *testing.T) {
	t.Parallel()
	u := usesOf(t, "import std.fs\n\nexport Main(sys: &System) {\n    info := fs.Stat(sys.io, \".\") else {\n        return\n    }\n    if info.size > 0 {\n        sys.Print(\"dir\") else {}\n    }\n}\n")
	if len(u["std.fs.Info"]) != 1 {
		t.Errorf("std.fs.Info sites %v; all: %v", u["std.fs.Info"], u)
	}
}

// TestUsesHoleFormat: a format hole prints through the value's
// Format, so `sys.Print("{addr}\n", (addr))` uses net.Addr's Format.
func TestUsesHoleFormat(t *testing.T) {
	t.Parallel()
	u := usesOf(t, "import std.net\n\nexport Main(sys: &System): net.Error|WriteError!void {\n    addr := try net.ParseAddr(\"127.0.0.1:7001\")\n    try sys.Print(\"{addr}\", (addr))\n}\n")
	if len(u["std.net#impl(std.core.Format for std.net.Addr):Format"]) != 1 {
		t.Errorf("Addr Format not used; all: %v", u)
	}
}

// TestMembersOfStr: `mk query members str` lists str's
// methods (a primitive is no item; it answered "no item named str"), and
// `members Writer` lists no private field of a std type.
func TestMembersOfStr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "q.mk"), []byte("export Main(sys: &System) {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := queryOne(t, dir, "members", "str")
	if !a.Found || !strings.Contains(a.Text(), "Len(self): int") {
		t.Errorf("members str:\n%s", a.Text())
	}
	w := queryOne(t, dir, "members", "Writer")
	for _, l := range w.Answer {
		if !strings.HasPrefix(l, "export ") && !strings.HasPrefix(l, "impl ") {
			t.Errorf("members Writer lists %q:\n%s", l, w.Text())
		}
	}
}

// TestPrintGenericField: Print, System.Print and Arena.Fmt with a
// record whose field's type is a bounded type parameter check (holes by
// name, E: Format by the bound), lower leaves the call plain, and mono
// expands it per instance; it was E0706 "the argument type is generic".
func TestPrintGenericField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := `error Bad(code: int)

Problem[E: Format + Plain](w: mut Writer, e: E): WriteError!void {
    try Print(w, "-ERR {e} {n}\n", (e, n: 3))
}

Report[E: Format + Plain](sys: &System, name: str, e: E) {
    sys.Print("{name}: {e}\n", (name, e)) else {}
    a := Arena.New(sys.heap) else {
        return
    }
    defer a.Free()
    s := a.Fmt("<{e}>", (e)) else {
        return
    }
    sys.Print("{s}\n", (s)) else {}
}

export Main(sys: &System) {
    var w := sys.Stdout()
    _ = Problem(mut w, Bad(code: 7)) else {}
    _ = Problem(mut w, 42) else {}
    _ = w.Flush() else {}
    Report(sys, "x", Bad(code: 1))
    Report(sys, "y", "str")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "-ERR Bad(code: 7) 3\n-ERR 42 3\nx: Bad(code: 1)\n<Bad(code: 1)>\ny: str\n<str>\n"
	if out := buildRun(t, dir, t.TempDir()); out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// TestClauseSpellsSizeOf: E0706 prints a callee's clause
// with its SizeOf calls whole and the call's type arguments in them, so the
// clause can be copied, and the copied clause clears it; it printed `...()`.
func TestClauseSpellsSizeOf(t *testing.T) {
	t.Parallel()
	src := `import std.simd

Luma[const N: int, const M: int](r: simd.Vec[u8, N], k: u8): simd.Vec[u16, M]
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires M * 2 == N && M >= 2
{
    return r.MulWidenLo[u16, M](simd.Vec[u8, N].Splat(k))
}
`
	const clause = "M * 2 == N && M >= 2 && SizeOf[u16]() == 2 * SizeOf[u8]()"
	r := checkText(t, src)
	if len(r.Report.Diags) != 1 || fact(r.Report.Diags[0].Diag, "clause") != clause {
		t.Fatalf("want one E0706 with clause %q:\n%s", clause, diagLines(r))
	}
	fixed := strings.Replace(src, "    requires M * 2 == N && M >= 2\n", "    requires "+clause+"\n", 1)
	if r2 := checkText(t, fixed); len(r2.Report.Diags) != 0 {
		t.Errorf("the restated clause does not clear it:\n%s", diagLines(r2))
	}
}

// TestFiniteSetGenericClause: check judges a callee's clause
// over const generics alone (Vec[u8, N].Zero's lane clause, a user
// generic's `3 <= N`) over the caller's finite set, so a caller limited to
// lane counts where it holds states nothing more; one where it fails for a
// value (N == 3) still restates it. Such a clause was always restated.
func TestFiniteSetGenericClause(t *testing.T) {
	t.Parallel()
	r := checkText(t, `import std.simd

Narrow[const N: int](): simd.Vec[u8, N]
    requires N == 16 || N == 32 || N == 64
{
    return simd.Vec[u8, N].Zero()
}

Bad[const N: int](): simd.Vec[u8, N]
    requires N == 3 || N == 16
{
    return simd.Vec[u8, N].Zero()
}

Left[const N: int](v: simd.Vec[u8, N], prev: simd.Vec[u8, N]): simd.Vec[u8, N]
    requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
    requires 3 >= 0 && 3 <= N
{
    return v.Slide(prev, 3)
}

Sobel[const N: int](v: simd.Vec[u8, N]): simd.Vec[u8, N]
    requires N == 4 || N == 8 || N == 16
{
    return Left[N](v, v)
}
`)
	var at []string
	for _, e := range r.Report.Diags {
		at = append(at, e.Diag.Code.String()+"@"+strconv.Itoa(int(r.Sources.Position(e.Diag.At.StartPos()).Line)))
	}
	if strings.Join(at, " ") != "E0706@12" {
		t.Errorf("want only Bad's E0706 at line 12, got %v:\n%s", at, diagLines(r))
	}
}

// TestBinLinksOwnRoot: each bin links its own root's export
// externs, not another bin's root's, so two roots exporting one symbol
// link; it was "duplicate symbol: Answer".
func TestBinLinksOwnRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"pkg.mk": `const Package = pkg.Package(
    name: "twobins",
    bins: [
        pkg.Bin(name: "a", root: "a", link_script: "", cpu: "", subsystem: "console"),
        pkg.Bin(name: "b", root: "b", link_script: "", cpu: "", subsystem: "console"),
    ],
    deps: [],
    c_sources: [],
    c_imports: [],
    asm_sources: [],
    targets: [],
    unsafe_modules: [],
    links: [],
)
`,
		"a/main.mk": "export extern Answer(): i32 {\n    return 1\n}\n\nexport Main(sys: &System): int {\n    _ = sys\n    return Cast[int](Answer())\n}\n",
		"b/main.mk": "export extern Answer(): i32 {\n    return 2\n}\n\nexport Main(sys: &System): int {\n    _ = sys\n    return Cast[int](Answer())\n}\n",
	}
	for p, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, paths := buildAt(t, dir, t.TempDir())
	if len(r.Report.Diags) > 0 || len(paths) != 2 {
		t.Fatalf("build: %d paths\n%s", len(paths), r.Report.Text(r.Sources))
	}
	for i, p := range paths {
		if _, _, code := runExe(t, HostTarget(), p, ""); code != i+1 {
			t.Errorf("%s: exit %d, want %d", p, code, i+1)
		}
	}
}

// TestSubSliceMutAlt: E0507 on a mut method of a sub-slice
// names the exclusive view, `(mut b[..2])`; it offered nothing.
func TestSubSliceMutAlt(t *testing.T) {
	t.Parallel()
	r := checkText(t, "Head(x: []u8): [4]u8 {\n    var b: [4]u8 = [0; 4]\n    b[..2].CopyFrom(x[..2])\n    return b\n}\n")
	if len(r.Report.Diags) != 1 {
		t.Fatalf("want one E0507:\n%s", diagLines(r))
	}
	d := r.Report.Diags[0].Diag
	if d.Code.String() != "E0507" || len(d.Alternatives) == 0 || !strings.Contains(d.Alternatives[0], "(mut b[..2])") {
		t.Errorf("got %v %q alternatives %q", d.Code, d.Message, d.Alternatives)
	}
	if r2 := checkText(t, "Head(x: []u8): [4]u8 {\n    var b: [4]u8 = [0; 4]\n    (mut b[..2]).CopyFrom(x[..2])\n    return b\n}\n"); len(r2.Report.Diags) != 0 {
		t.Errorf("the named form does not check:\n%s", diagLines(r2))
	}
	for _, tc := range []struct {
		name, src string
		hint      bool
	}{
		{"mutable slice", "Attempt(xs: mut []u64) { xs[0..1].Fill(3) }\n", true},
		{"shared slice", "Attempt(xs: []u64) { xs[0..1].Fill(3) }\n", false},
		{"immutable array", "Attempt(xs: [2]u64) { xs[0..1].Fill(3) }\n", false},
		{"shared array reference", "Attempt(xs: &[2]u64) { xs[0..1].Fill(3) }\n", false},
		{"shared result", "const Seeds: []u64 = [1, 2]\n\nValues(): []u64 { return Seeds }\n\nAttempt() { Values()[0..1].Fill(3) }\n", false},
		{"live loan", "Attempt() {\n    var xs: [2]u64 = [1, 2]\n    p := &xs\n    xs[0..1].Fill(3)\n    Assert(p[0] == 1)\n}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := checkText(t, tc.src)
			if len(r.Report.Diags) != 1 || r.Report.Diags[0].Diag.Code.String() != "E0507" {
				t.Fatalf("want one E0507:\n%s", diagLines(r))
			}
			hint := strings.Contains(strings.Join(r.Report.Diags[0].Diag.Alternatives, "\n"), "borrow the sub-slice exclusively")
			if hint != tc.hint {
				t.Errorf("exclusive sub-slice hint = %t, want %t:\n%s", hint, tc.hint, diagLines(r))
			}
			if hint {
				good := strings.Replace(tc.src, "xs[0..1].Fill", "(mut xs[0..1]).Fill", 1)
				if r := checkText(t, good); len(r.Report.Diags) != 0 {
					t.Errorf("suggested form does not check:\n%s", diagLines(r))
				}
			}
		})
	}
}

// TestLinearElseValue: a value `else` (and a statement
// `match`) on a call that returns a linear value checks: the call's
// destination temporary is moved on the success edge and never assigned on
// the error edge, which holds nothing to consume. It was a false E0402 at
// the call. Each real leak is still E0402.
func TestLinearElseValue(t *testing.T) {
	t.Parallel()
	const decls = "linear struct Res {\n    id: int\n}\n\nRes.Close(self) {\n    Res(id) := self\n    _ = id\n}\n\nMake(id: int): OutOfMemory!Res {\n    if id < 0 {\n        fail OutOfMemory\n    }\n    return Res(id: id)\n}\n"
	ok := checkText(t, decls+"\nUse(n: int) {\n    r := Make(n) else {\n        Res(id: 0)\n    }\n    r.Close()\n    s: Res\n    match Make(n) {\n        ok(v) { s = v }\n        err(_) { s = Res(id: 1) }\n    }\n    s.Close()\n}\n")
	if len(ok.Report.Diags) != 0 {
		t.Errorf("want clean:\n%s", diagLines(ok))
	}
	bad := checkText(t, decls+"\nA(n: int) {\n    r := Make(n) else {\n        Res(id: 0)\n    }\n    _ = r.id\n}\n\nB(n: int) {\n    match Make(n) {\n        ok(v) { _ = v.id }\n        err(_) { }\n    }\n}\n\nC(n: int) {\n    _ = Make(n) else { }\n}\n")
	if n := len(bad.Report.Diags); n != 3 {
		t.Errorf("want three E0402, got %d:\n%s", n, diagLines(bad))
	}
}

// TestStoreThroughSharedField: `*c into c` on a `c: &Ctx` whose
// field `hb: &Hub` reaches a channel is accepted, since c then has interior
// mutability (`*q into p` needs p `mut` or with interior mutability);
// E0604 named that clause and check refused it with E0507.
func TestStoreThroughSharedField(t *testing.T) {
	t.Parallel()
	src := `import std.sync
struct Hub {
    heap: &Heap
    ch: Chan[sync.Shared[int]]
}

Hub.Put(&self, io: Io) *self into self {
    m := sync.Shared[int].New(self.heap, 1) else {
        return
    }
    self.ch.Send(io, m) else {
        m.Free(self.heap)
    }
}

struct Ctx {
    hb: &Hub
    io: Io
}

Use(c: &Ctx) *c into c {
    c.hb.Put(c.io)
}

export Main(sys: &System) {
}
`
	if r := checkText(t, src); len(r.Report.Diags) != 0 {
		t.Errorf("want clean:\n%s", diagLines(r))
	}
	// Without the clause, E0604 names it, and the fix it names checks.
	r := checkText(t, strings.Replace(src, "Use(c: &Ctx) *c into c {", "Use(c: &Ctx) {", 1))
	if len(r.Report.Diags) != 1 || fact(r.Report.Diags[0].Diag, "clause") != "*c into c" {
		t.Errorf("want one E0604 naming *c into c:\n%s", diagLines(r))
	}
}

// TestQueryStdRaw: in a package whose manifest lists
// unsafe_modules, which may import std.raw, mk query answers std.raw's
// names; it answered "no item named".
func TestQueryStdRaw(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	man := "const Package = pkg.Package(\n    name: \"k\",\n    bins: [pkg.Bin(name: \"k\", root: \"src\", link_script: \"\", cpu: \"\", subsystem: \"console\")],\n    deps: [],\n    c_sources: [],\n    c_imports: [],\n    asm_sources: [],\n    targets: [],\n    unsafe_modules: [\"src\"],\n    links: [],\n)\n"
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.mk"), []byte("export Main(sys: &System) {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.Query("sig", []string{"raw.FromAddr"})
	if err != nil || a == nil || !a.Found || !strings.Contains(a.Text(), "FromAddr[T](a: uint): *T") {
		t.Fatalf("query sig raw.FromAddr: %v\n%v", err, a)
	}
}

// TestStoreThroughSharedFieldReject: the reject side of
// TestStoreThroughSharedField. With `*self into self` accepted on a
// `&self` that reaches a Cell through a second shared hop, a caller that
// stores a short-lived loan through it is still E0505 (the store is judged at
// the call), so accepting the clause lets no loan outlive its owner.
func TestStoreThroughSharedFieldReject(t *testing.T) {
	t.Parallel()
	r := checkText(t, `struct Slot {
    c: Cell[&i32]
}

struct Hold {
    s: &Slot
    v: &i32
}

Hold.Mix(&self) *self into self {
    self.s.c.Store(self.v)
}

Inner(slot: &Slot) {
    short: i32 = 5
    h := Hold(s: slot, v: &short)
    h.Mix()
}

export Main(sys: &System) {
    keep: i32 = 1
    slot := Slot(c: Cell.New(&keep))
    Inner(&slot)
    _ = sys
}
`)
	got := ""
	for _, e := range r.Report.Diags {
		got += e.Diag.Code.String() + " "
	}
	if !strings.Contains(got, "E0505") {
		t.Errorf("want E0505 for the short-lived loan, got %q:\n%s", got, diagLines(r))
	}
}

// TestNestedPackageNoModule: a directory under the
// package root holding its own pkg.mk is another package, and nothing under
// it is a module of this one, so a broken program there (a dogfood's
// repro/<id>/) does not break the outer package; its pkg.mk was read as
// module source (E0201 on pkg).
func TestNestedPackageNoModule(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	man := func(name, root string) string {
		return "const Package = pkg.Package(\n    name: \"" + name + "\",\n    bins: [pkg.Bin(name: \"" + name + "\", root: \"" + root + "\", link_script: \"\", cpu: \"\", subsystem: \"console\")],\n    deps: [],\n    c_sources: [],\n    c_imports: [],\n    asm_sources: [],\n    targets: [],\n    unsafe_modules: [],\n    links: [],\n)\n"
	}
	files := map[string]string{
		"pkg.mk":                     man("outer", "src"),
		"src/main.mk":                "export Main(sys: &System) {\n}\n",
		"repro/r1/pkg.mk":            man("r1", "src"),
		"repro/r1/src/main.mk":       "export Main(sys: &System) {\n    x := Nope()\n}\n",
		"repro/r1/src/deeper/two.mk": "Broken( {\n",
	}
	for p, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) != 0 {
		t.Errorf("the outer package checks clean:\n%s", diagLines(r))
	}
}
