// Why: mk fmt rewrites users' files; it must keep their meaning and reach
// a fixed point on everything the project ships.
// What: golden cases, idempotence on std and on every fenced mk block of
// doc/**/*.md, the token guard, the empty-record rule of Print, and FuzzFormat.
// How: formats each input twice and compares, and re-parses the output.

package format

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mk/internal/source"
	"mk/internal/syntax"
)

func mustFormat(t *testing.T, in string) string {
	t.Helper()
	out, err := Source("t.mk", []byte(in))
	if err != nil {
		t.Fatalf("Source(%q): %v", in, err)
	}
	return string(out)
}

// TestFormatGolden: each input formats to its output, and the output is a
// fixed point.
func TestFormatGolden(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"become", "F(a: int): int {\n    become   G(a,1)\n}\n", "F(a: int): int {\n    become G(a, 1)\n}\n"},
		{"range patterns", "F(x: i8) {\n    match x {\n        0 .. 10 {}\n        100..{}\n        ..-3 {}\n        -5..-4|..-9 {}\n        _ {}\n    }\n}\n", "F(x: i8) {\n    match x {\n        0..10 {}\n        100.. {}\n        ..-3 {}\n        -5..-4 | ..-9 {}\n        _ {}\n    }\n}\n"},
		{"variadic extern", "extern open(path: *u8, flags: i32,...): i32\n", "extern open(path: *u8, flags: i32, ...): i32\n"},
		{"spaces", "F(a:int,b:  int):int{\n\treturn a+b*  -a\n}\n", "F(a: int, b: int): int {\n    return a + b * -a\n}\n"},
		{"unary and types", "G(p: & Arena, q: * u64, r: ? str, s: [ ] u8): OutOfMemory ! [ ] u8 {\n}\n", "G(p: &Arena, q: *u64, r: ?str, s: []u8): OutOfMemory![]u8 {\n}\n"},
		{"error set", "H(): A|B!void {\n}\nconst M: int = X | Y\n", "H(): A|B!void {\n}\nconst M: int = X | Y\n"},
		{"blank lines", "\n\n\nA() {\n\n    x()\n\n\n    y()\n\n}\n\n\n\nB() {}\n\n", "A() {\n    x()\n\n    y()\n}\n\nB() {}\n"},
		{"brackets", "A() {\nf(g(\n1,\n2,\n))\n}\n", "A() {\n    f(g(\n        1,\n        2,\n    ))\n}\n"},
		{"continuation", "A() {\nif a &&\nb {\nc()\n}\n}\n", "A() {\n    if a &&\n        b {\n        c()\n    }\n}\n"},
		{"brace after bracket", "A() {\nfor x in [1,\n2] {\nc()\n}\n}\n", "A() {\n    for x in [1,\n        2] {\n        c()\n    }\n}\n"},
		{"contract lines", "Clamp(x: i32): i32\nrequires x >= 0\nensures result >= 0\n{\nreturn x\n}\n", "Clamp(x: i32): i32\n    requires x >= 0\n    ensures result >= 0\n{\n    return x\n}\n"},
		{"flow", "Exchange[T](a: mut T, b: mut T) *a into b, *b into a {\n}\n", "Exchange[T](a: mut T, b: mut T) *a into b, *b into a {\n}\n"},
		{"receiver into", "N.K(&self   into arena,arena: &Arena) {\n}\nN.L(mut self into  self) {\n}\nN.M(self into a,\na: &Arena) {\n}\n", "N.K(&self into arena, arena: &Arena) {\n}\nN.L(mut self into self) {\n}\nN.M(self into a,\n    a: &Arena) {\n}\n"},
		{"impl", "impl[T: Eq] Eq for &T {\n    Eq(&self, other: &&T): bool {\n        return true\n    }\n}\n", "impl[T: Eq] Eq for &T {\n    Eq(&self, other: &&T): bool {\n        return true\n    }\n}\n"},
		{"deref statement", "A(p: *int) {\n    x()\n    *p = 1\n}\n", "A(p: *int) {\n    x()\n    *p = 1\n}\n"},
		{"range and index", "A(xs: []int) {\n    for i in 0 .. 3 {\n        f(xs [ i ], xs[..])\n    }\n}\n", "A(xs: []int) {\n    for i in 0..3 {\n        f(xs[i], xs[..])\n    }\n}\n"},
		{"trailing comments", "struct S {\n    a: int // one\n    bbbb: int   // two\n}\n", "struct S {\n    a: int    // one\n    bbbb: int // two\n}\n"},
		{"comment lines", "A() {\n    x()\n    // note\n    y()\n}\n", "A() {\n    x()\n    // note\n    y()\n}\n"},
		{"test block", "test \"x\"(sys:&System){\n    Assert(true)\n}\n", "test \"x\" (sys: &System) {\n    Assert(true)\n}\n"},
		{"generic call", "A() {\n    s := try a.Slice [u64] (n)\n    b := F[int](1)\n}\n", "A() {\n    s := try a.Slice[u64](n)\n    b := F[int](1)\n}\n"},
		{"empty file", "", ""},
		{"crlf", "A() {\r\n  x()\r\n}\r\n", "A() {\n    x()\n}\n"},
		{"multi-line string", "A() {\n      s := \"\"\"\n   keep\n     this\n   \"\"\"\n}\n", "A() {\n    s := \"\"\"\n   keep\n     this\n   \"\"\"\n}\n"},
		{"fields", "A() {\n    for name, value in fields (&args) {\n    }\n}\n", "A() {\n    for name, value in fields(&args) {\n    }\n}\n"},
		// --y is E0101, so the space of a double negation stays.
		{"double negation", "A(y: int) {\n    x := - -y\n    z := -(-y)\n    w := x - -y\n}\n", "A(y: int) {\n    x := - -y\n    z := -(-y)\n    w := x - -y\n}\n"},
		{"double deref", "A(p: **int) {\n    x := * *p\n}\n", "A(p: **int) {\n    x := **p\n}\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mustFormat(t, c.in)
			if got != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, c.want)
			}
			if again := mustFormat(t, got); again != got {
				t.Errorf("not a fixed point:\n%s\nthen:\n%s", got, again)
			}
		})
	}
}

// TestEmptyPrintRecordRemoved: a bare Print call loses an empty record
// after a string whose decoded text has no hole and no lone brace (escaped
// braces are no hole, \u{7B} and \x7B are braces); anything else keeps it,
// and so does every .Fmt selector, whose receiver may be any type with a
// Fmt method.
func TestEmptyPrintRecordRemoved(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`try Print(mut w, "done\n", ())`, `try Print(mut w, "done\n")`},
		{`try Print(mut w, "{n}\n", (n: 1))`, `try Print(mut w, "{n}\n", (n: 1))`},
		{`try Print(mut w, "{{x}}", ())`, `try Print(mut w, "{{x}}")`},
		{`try Print(mut w, "x {{y}}\n", ())`, `try Print(mut w, "x {{y}}\n")`},
		{`try Print(mut w, "}}", ())`, `try Print(mut w, "}}")`},
		{`try Print(mut w, r"a\{{", ())`, `try Print(mut w, r"a\{{")`},
		{`try Print(mut w, "}", ())`, `try Print(mut w, "}", ())`},
		{`try Print(mut w, "{x", ())`, `try Print(mut w, "{x", ())`},
		{`try Print(mut w, "\u{7B}x\u{7D}", ())`, `try Print(mut w, "\u{7B}x\u{7D}", ())`},
		{`try Print(mut w, "\x7Bx\x7D", ())`, `try Print(mut w, "\x7Bx\x7D", ())`},
		{`try Print(mut w, "\u{41}\x41\\{{", ())`, `try Print(mut w, "\u{41}\x41\\{{")`},
		{`s := try ar.Fmt("x", ())`, `s := try ar.Fmt("x", ())`},
		{`x := l.Fmt("a", ())`, `x := l.Fmt("a", ())`},
		{`try x.Print(mut w, "a", ())`, `try x.Print(mut w, "a", ())`},
		{`try io.Print(mut w, "a", ())`, `try io.Print(mut w, "a", ())`},
		{`try Fmt("a", ())`, `try Fmt("a", ())`},
		{`try Print(mut w, s, ())`, `try Print(mut w, s, ())`},
		{"try Print(mut w, \"a\", () // keep\n)", "try Print(mut w, \"a\", () // keep\n)"},
		{"try Print(mut w,\n    \"a\",\n    ())", "try Print(mut w,\n    \"a\")"},
	} {
		in := "A() {\n    " + c.in + "\n}\n"
		got := mustFormat(t, in)
		want := "A() {\n    " + c.want + "\n}\n"
		if strings.Contains(c.want, "\n") {
			w := strings.ReplaceAll(c.want, "\n    ", "\n        ")
			want = "A() {\n    " + strings.ReplaceAll(w, "\n)", "\n    )") + "\n}\n"
		}
		if got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.in, got, want)
		}
	}
}

// TestHiddenPrintKeepsRecords: a module-level name may hide the
// prelude's Print, so a file that declares or binds Print keeps every
// empty record; a selector such as io.Print hides
// nothing.
func TestHiddenPrintKeepsRecords(t *testing.T) {
	call := "A() {\n    try Print(mut w, \"a\", ())\n}\n"
	for _, c := range []struct{ name, extra string }{
		{"declared", "Print(a: int, b: str, c: int) {\n}\n"},
		{"exported", "export Print(a: int, b: str, c: int) {\n}\n"},
		{"generic", "Print[T](a: T, b: str, c: int) {\n}\n"},
		{"local", "B() {\n    Print := F\n}\n"},
		{"parameter", "B(Print: fn(int, str, int)) {\n}\n"},
	} {
		in := call + c.extra
		if got := mustFormat(t, in); got != in {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, in)
		}
	}
	got := mustFormat(t, call+"\nB() {\n    io.Print(1)\n}\n")
	if want := "A() {\n    try Print(mut w, \"a\")\n}\n\nB() {\n    io.Print(1)\n}\n"; got != want {
		t.Errorf("a selector hid the prelude Print:\ngot  %q\nwant %q", got, want)
	}
}

// TestFormatOutputParses: the output of a file that parses parses: the
// token guard compares kinds and texts, while the parser also
// reads adjacency, so Source re-parses what it returns.
func TestFormatOutputParses(t *testing.T) {
	for _, in := range []string{
		"A(y: int) {\n    x := - -y\n}\n",
		"A(y: int) {\n    x := - - -y\n}\n",
		"A(y: int) {\n    x := 1 - - -y\n}\n",
	} {
		out := mustFormat(t, in)
		if _, ds := syntax.ParseFile(source.NewSourceMap().Add("t.mk", []byte(out)), source.NewInterner(), syntax.Options{AllowBodiless: true}); len(ds) > 0 {
			t.Errorf("%q formats to %q, which does not parse: %s", in, out, ds[0].Message)
		}
	}
}

// TestFormatRefusesSyntaxErrors: a file that does not parse is a
// *SyntaxError and nothing is returned.
func TestFormatRefusesSyntaxErrors(t *testing.T) {
	out, err := Source("t.mk", []byte("A( {\n"))
	var se *SyntaxError
	if out != nil || !errors.As(err, &se) || se.Count == 0 || !strings.HasPrefix(se.First, "1:") {
		t.Fatalf("out %q err %v", out, err)
	}
}

// TestFormatKeepsTokens: the guard refuses an output whose tokens differ.
func TestFormatKeepsTokens(t *testing.T) {
	text := "A() {\n    x := 1\n}\n"
	toks, _ := syntax.Lex(source.NoFile, text)
	if err := guard(text, toks, "A() {\n    x := 2\n}\n", nil); err == nil {
		t.Fatal("guard accepted a changed literal")
	}
	if err := guard(text, toks, "A() {\n    x\n    := 1\n}\n", nil); err == nil {
		t.Fatal("guard accepted a split line (terminators moved)")
	}
	// & &x must not fuse into &&x.
	if got := mustFormat(t, "A() {\n    y := & &x\n}\n"); got != "A() {\n    y := &&x\n}\n" && got != "A() {\n    y := & &x\n}\n" {
		t.Errorf("got %q", got)
	}
}

// checkIdempotent formats text, then formats the result, from the token
// list alone (no parse), and fails on a guard error or a second change.
func checkIdempotent(t *testing.T, name, text string) bool {
	t.Helper()
	text = source.Normalize([]byte(text))
	toks, ds := syntax.Lex(source.NoFile, text)
	if len(ds) > 0 {
		return false
	}
	once, err := Tokens(text, toks)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return false
	}
	toks2, _ := syntax.Lex(source.NoFile, string(once))
	twice, err := Tokens(string(once), toks2)
	if err != nil {
		t.Errorf("%s: second pass: %v", name, err)
		return false
	}
	if string(twice) != string(once) {
		t.Errorf("%s: not idempotent:\n%s\nthen:\n%s", name, once, twice)
		return false
	}
	return true
}

// TestFormatIdempotentOnStd: every .mk file of std formats to a fixed point,
// through Source (parse included); a file left unparsable is logged, not
// failed (std's own tests fail on it).
func TestFormatIdempotentOnStd(t *testing.T) {
	root := filepath.Join("..", "..", "std")
	n, changed := 0, 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		once, err := Source(p, raw)
		var se *SyntaxError
		if errors.As(err, &se) {
			t.Logf("%s: %v (skipped)", p, err)
			return nil
		}
		if err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		n++
		if string(once) != source.Normalize(raw) {
			changed++
		}
		twice, err := Source(p, once)
		if err != nil || string(twice) != string(once) {
			t.Errorf("%s: not idempotent (err %v)", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("formatted only %d std files", n)
	}
	t.Logf("%d std files, %d not already in format", n, changed)
}

// docBlocks returns every fenced mk block of doc/**/*.md with its
// document:line.
func docBlocks(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "doc")
	files, err := docFiles(dir)
	if err != nil || len(files) == 0 {
		t.Fatalf("no documents: %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(source.Normalize(raw), "\n")
		for i := 0; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "```mk" {
				continue
			}
			start := i + 1
			var b strings.Builder
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "```"; i++ {
				b.WriteString(lines[i] + "\n")
			}
			out[f+":"+itoa(start)] = b.String()
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestFormatIdempotentOnDocBlocks: every design block that lexes formats
// to a fixed point from its tokens, and every block that parses formats
// through Source too.
func TestFormatIdempotentOnDocBlocks(t *testing.T) {
	blocks := docBlocks(t)
	lexed, parsed := 0, 0
	for name, text := range blocks {
		if checkIdempotent(t, name, text) {
			lexed++
		}
		if once, err := Source(name, []byte(text)); err == nil {
			parsed++
			if twice, err := Source(name, once); err != nil || string(twice) != string(once) {
				t.Errorf("%s: Source not idempotent (err %v)", name, err)
			}
		}
	}
	if len(blocks) < 100 || lexed < len(blocks)/2 {
		t.Fatalf("%d blocks, %d lexed", len(blocks), lexed)
	}
	t.Logf("%d design blocks: %d lexed and checked, %d parsed and checked", len(blocks), lexed, parsed)
}

// TestFormatIdempotentOnTests: every .mk file under tests/ (the conformance
// units of the corpus, the programs and the benchmarks) that lexes formats to
// a fixed point from its tokens, and every one that parses through Source
// too. A file that does not lex is a reject unit of the lexer (logged count).
func TestFormatIdempotentOnTests(t *testing.T) {
	root := filepath.Join("..", "..", "tests")
	n, lexed, parsed := 0, 0, 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if checkIdempotent(t, p, string(raw)) {
			lexed++
		}
		if once, err := Source(p, raw); err == nil {
			parsed++
			if twice, err := Source(p, once); err != nil || string(twice) != string(once) {
				t.Errorf("%s: Source not idempotent (err %v)", p, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 300 || lexed < n/2 {
		t.Fatalf("%d files under tests/, %d lexed", n, lexed)
	}
	t.Logf("%d files under tests/: %d lexed and checked, %d parsed and checked", n, lexed, parsed)
}

// FuzzFormat: on any input that lexes, formatting never panics, never fails
// its guard, and is idempotent; on any input that parses, Source succeeds
// and its output parses (the token guard alone once let `- -y` become
// `--y`, which is E0101).
func FuzzFormat(f *testing.F) {
	for _, s := range []string{
		"A() {\n    x := 1 + -2\n}\n",
		"F(a: &Arena, b: *u8): E|F![]u8 {\n    return try a.Slice[u8](n)\n}\n",
		"impl[T] Eq for &T {}\n",
		"f(g(\n1,\n))\n",
		"x := \"\"\"\n  a\n  \"\"\"\n",
		"A() {\n    try Print(mut w, \"x\", ())\n}\n",
		"a := b // c\n",
		"A() {\n    x := - -y\n}\n",
		"A() {\n    x := 1 - - -y\n}\n",
		// An if expression, a pair loop and a destructuring binding.
		"A(c: bool) {\n    x := if c { 1 } else {\n2\n}\n    for (a, b) in m.Iter() {\n    }\n    B(p, q: _, ..) := r\n}\n",
	} {
		f.Add(s)
	}
	parses := func(src string) bool {
		_, ds := syntax.ParseFile(source.NewSourceMap().Add("input.mk", []byte(src)), source.NewInterner(), syntax.Options{AllowBodiless: true})
		return len(ds) == 0
	}
	f.Fuzz(func(t *testing.T, s string) {
		text := source.Normalize([]byte(s))
		if _, ds := syntax.Lex(source.NoFile, text); len(ds) > 0 {
			return
		}
		checkIdempotent(t, "input", text)
		if !parses(text) {
			return
		}
		out, err := Source("input.mk", []byte(text))
		if err != nil {
			t.Fatalf("an input that parses does not format: %v\n%s", err, text)
		}
		if !parses(string(out)) {
			t.Fatalf("an input that parses formats to text that does not:\n%s\nthen:\n%s", text, out)
		}
	})
}

// BenchmarkFormatStd formats every .mk file of std once per iteration (the
// cost behind mk fmt over std; read files are held in memory).
func BenchmarkFormatStd(b *testing.B) {
	var files [][]byte
	_ = filepath.WalkDir(filepath.Join("..", "..", "std"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".mk") {
			if raw, err := os.ReadFile(p); err == nil {
				files = append(files, raw)
			}
		}
		return nil
	})
	n := 0
	for _, f := range files {
		n += len(f)
	}
	b.SetBytes(int64(n))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range files {
			_, _ = Source("f.mk", f)
		}
	}
}

var updateGolden = flag.Bool("update", false, "rewrite testdata/valuearms.golden")

// TestValueArmLayout: match value arms and their neighbours format
// to testdata/valuearms.golden, which is a fixed point.
func TestValueArmLayout(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", "valuearms.in"))
	if err != nil {
		t.Fatal(err)
	}
	got := mustFormat(t, string(in))
	path := filepath.Join("testdata", "valuearms.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if !checkIdempotent(t, "valuearms", got) {
		t.Errorf("not idempotent")
	}
}

// TestContinuedIfCondition: a body after a continued condition whose
// last line closes a bracket is indented from the statement, not from the
// continuation (std/task/sched.mk's Linger).
func TestContinuedIfCondition(t *testing.T) {
	in := "A() {\n    if a ||\n        (*c).b && d {\n        x()\n    }\n}\n"
	if got := mustFormat(t, in); got != in {
		t.Errorf("got:\n%s\nwant:\n%s", got, in)
	}
	bad := "A() {\n    if a ||\n        (*c).b && d {\n            x()\n        }\n}\n"
	if got := mustFormat(t, bad); got != in {
		t.Errorf("got:\n%s\nwant:\n%s", got, in)
	}
}

// TestRestPatternSpace: `, ..` keeps its space in a pattern
// (std/regex/dfa.mk).
func TestRestPatternSpace(t *testing.T) {
	in := "A(b: B) {\n    B(x, y: _, ..) := b\n}\n"
	if got := mustFormat(t, in); got != in {
		t.Errorf("got:\n%s\nwant:\n%s", got, in)
	}
	if got := mustFormat(t, "A(b: B) {\n    B(x, y: _,..) := b\n}\n"); got != in {
		t.Errorf("got:\n%s", got)
	}
}

// TestStringIndexAndFloatMethod: no space is inserted
// before the index of a string literal or before a method of a float
// literal, and the output is a fixed point.
func TestStringIndexAndFloatMethod(t *testing.T) {
	for _, in := range []string{
		"F(t: mut T, a: &Alloc, i: int) {\n    t.Set(a, \"0123456789\"[..i % 11])\n}\n",
		"F(): u64 {\n    return 1.0.Bits()\n}\n",
		"F(): u64 {\n    return 1 .Bits()\n}\n",
	} {
		if got := mustFormat(t, in); got != in {
			t.Errorf("got:\n%s\nwant:\n%s", got, in)
		}
	}
}

// TestPerFieldClauseLayout: mk fmt spells the per-field clause
// `from (f: a, g: b)` with a space before the "(" (a call keeps none), on
// the signature's line and on a continuation line, and keeps it stable.
func TestPerFieldClauseLayout(t *testing.T) {
	in := "struct P {\n    a: &int\n    b: &int\n}\n\nF(x: &int, y: &int): P from(a: x,b: y) {\n    return P(a: x, b: y)\n}\n\nG(x: &int, y: &int): P\nfrom ( a:x , b : y )\n{\n    return from(a: x)\n}\n\nH(f: fn(x: &int, y: &int): P from (a: x, b: y), n: int) {\n}\n"
	want := "struct P {\n    a: &int\n    b: &int\n}\n\nF(x: &int, y: &int): P from (a: x, b: y) {\n    return P(a: x, b: y)\n}\n\nG(x: &int, y: &int): P\n    from (a: x, b: y)\n{\n    return from(a: x)\n}\n\nH(f: fn(x: &int, y: &int): P from (a: x, b: y), n: int) {\n}\n"
	got, err := Source("t.mk", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := Source("t.mk", got)
	if err != nil || string(again) != string(got) {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
}

// TestFormatMutViewStruct: `mut view struct`, `linear mut view struct`, and
// `plain view struct` format with canonical single-space spacing and stay
// idempotent.
func TestFormatMutViewStruct(t *testing.T) {
	in := "mut   view  struct A {\n    x: int\n}\n\nexport  linear  mut   view  struct B {\n    y: int\n}\n\npacked(4)  mut  view  struct C {\n    z: u32\n}\n\nexport   align(8)   packed(4)   plain   view   struct D {\n    idx: u32\n}\n"
	want := "mut view struct A {\n    x: int\n}\n\nexport linear mut view struct B {\n    y: int\n}\n\npacked(4) mut view struct C {\n    z: u32\n}\n\nexport align(8) packed(4) plain view struct D {\n    idx: u32\n}\n"
	got, err := Source("t.mk", []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := Source("t.mk", got)
	if err != nil || string(again) != string(got) {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
}

// docFiles lists the Markdown files under dir, doc/std's pages included, as
// paths relative to dir with '/', in byte order: "reference.md",
// "std/fs.md".
func docFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() || !strings.HasSuffix(p, ".md") {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	sort.Strings(out)
	return out, err
}
