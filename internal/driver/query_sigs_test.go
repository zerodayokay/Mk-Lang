// Why: `mk query sig --member` must list every function a call can name,
// those a gen line writes included, and nothing a call cannot.
// What: TestSignatures, over Session.Signatures on a package without a
// workspace, and TestSignatures_SpellsAliases, over a package whose
// signatures name aliases.
// How: checks a one-module package and compares the entries' names, sigs
// and gen marks (an error's provided Format among them), and the text
// form's header.

package driver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mk/internal/source"
)

func TestSignatures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"pkg.mk": "const Package = pkg.Package(\n    name: \"p\",\n    unsafe_modules: [\"src\"],\n)\n",
		"src/a.mk": "import std.dyn\n\nextern labs(x: i64): i64\n\ninterface Shape {\n    Area(&self): f64\n}\n\nstruct Square {\n    side: f64\n    invariant self.side >= 0.0\n}\n\n" +
			"impl Shape for Square {\n    Area(&self): f64 {\n        return self.side\n    }\n}\n\ngen dyn.Of(Shape)\n\n" +
			"export Use(x: int): f64\n    requires x > 0\n{\n    var s := Square(side: 2.0)\n    var d := ShapeDyn.Of(mut s)\n" +
			"    f := fn(y: int): int { return y }\n    _ = f(x)\n    return d.Area()\n}\n\ntest \"t\" {\n}\n\nerror Oops\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewSession(Config{Root: dir, CacheDir: filepath.Join(dir, ".cache")})
	if err != nil {
		t.Fatal(err)
	}
	l, res := s.Signatures("p")
	if l == nil || len(res.Report.Diags) != 0 {
		t.Fatalf("no listing:\n%s", diagLines(res))
	}
	var got []string
	for _, e := range l.Functions {
		got = append(got, e.Name+"|"+e.Sig+"|"+strings.Join(e.Contracts, ";")+"|"+e.Impl+"|"+e.Gen)
	}
	want := []string{ // src/a.mk sorts before the gen view, src/gen/...
		"labs|extern labs(x: i64): i64|||",
		"Square.Area|Square.Area(&self): f64||Shape|",
		"Use|export Use(x: int): f64|requires x > 0||",
		"Oops.Format|Oops.Format(&self, w: mut Writer): WriteError!void||Format|", // the compiler's
		"ShapeDyn.Of|ShapeDyn.Of[T: Shape](x: mut T): ShapeDyn from x|||src/a.mk:20:1",
		"ShapeDyn.AdaptArea|ShapeDyn.AdaptArea[T: Shape](obj: &Ref): f64|||src/a.mk:20:1",
		"ShapeDyn.Area|ShapeDyn.Area(&self): f64||Shape|src/a.mk:20:1",
	}
	if l.Version != 1 || l.Query != "sig" || l.Member != "p" || !l.Complete || !slices.Equal(got, want) {
		t.Errorf("listing %d %q %q %v:\n%s\nwant:\n%s", l.Version, l.Query, l.Member, l.Complete, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if txt := l.Text(); !strings.HasPrefix(txt, "member p: 7 function(s)\n") || !strings.Contains(txt, "export Use(x: int): f64  src/a.mk:22:8\n    requires x > 0\n") {
		t.Errorf("text:\n%s", txt)
	}
}

// sigAliasFiles is a package whose signatures name aliases: of a primitive
// (Value), of another module's type (w.Word), a generic one (Opt[T]), one of
// an error set (AB), nested under prefix operators and a generic struct's
// argument, and in a function a gen line writes.
var sigAliasFiles = map[string]string{
	"pkg.mk": manifest(nil),
	"w/w.mk": "export type Word = u64\n",
	"g/g.mk": "import w\n\nexport Fns(g: mut Gen) {\n    g.Emit(quote {\n        export Raw(): w.Word {\n            return 0\n        }\n    })\n}\n",
	"a/a.mk": "import w\nimport g\n\nexport type Value = u64\n\nexport type Opt[T] = ?T\n\nexport error A\n\nexport error B\n\nexport type AB = A | B\n\n" +
		"export struct Box[T] {\n    export v: T\n}\n\n" +
		"export Load(i: u64): Value {\n    return i\n}\n\n" +
		"export Count(i: u64): u64 {\n    return i\n}\n\n" +
		"export Nest(a: []Value, b: mut Value, c: ?w.Word, d: &[4]Value, e: Box[Value]): Opt[(Value)] {\n    return none\n}\n\n" +
		"export Fail(i: int): AB!Value {\n    if i < 0 {\n        fail A\n    }\n    return 1\n}\n\n" +
		"gen g.Fns()\n",
}

// TestSignatures_SpellsAliases: sig and sig --member spell each alias a
// signature names as the declaration writes it, the resolved field spells
// it as its type, an alias-free signature is unchanged, and a warm session,
// which reads the signatures from the cache, answers the same.
func TestSignatures_SpellsAliases(t *testing.T) {
	t.Parallel()
	dir := writeTree(t, sigAliasFiles)
	cache := t.TempDir()
	want := []string{
		"A.Format|A.Format(&self, w: mut Writer): WriteError!void|A.Format(&self, w: mut Writer): WriteError!void",
		"B.Format|B.Format(&self, w: mut Writer): WriteError!void|B.Format(&self, w: mut Writer): WriteError!void",
		"Load|export Load(i: u64): Value|export Load(i: u64): u64",
		"Count|export Count(i: u64): u64|export Count(i: u64): u64",
		"Nest|export Nest(a: []Value, b: mut Value, c: ?Word, d: &[4]Value, e: Box[Value]): Opt[Value]|" +
			"export Nest(a: []u64, b: mut u64, c: ?u64, d: &[4]u64, e: Box[u64]): ?u64",
		"Fail|export Fail(i: int): AB!Value|export Fail(i: int): A|B!u64",
		"Raw|export Raw(): Word|export Raw(): u64", // a's gen view, a/gen/..., sorts before g/g.mk
		"Fns|export Fns(g: mut Gen)|export Fns(g: mut Gen)",
	}
	for _, run := range []string{"cold", "warm"} {
		s, err := NewSession(Config{Root: dir, CacheDir: cache, BuildID: source.HashString("test", "mk")})
		if err != nil {
			t.Fatal(err)
		}
		l, res := s.Signatures("p")
		if l == nil || len(res.Report.Diags) != 0 {
			t.Fatalf("%s: no listing:\n%s", run, diagLines(res))
		}
		var got []string
		for _, e := range l.Functions {
			got = append(got, e.Name+"|"+e.Sig+"|"+e.Resolved)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s listing:\n%s\nwant:\n%s", run, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		a, res, err := s.Query("sig", []string{"Nest"})
		if err != nil || a == nil || len(a.Answer) == 0 || a.Answer[0] != strings.Split(want[4], "|")[1] {
			t.Errorf("%s: sig Nest: %v %v %q", run, err, a, lines(res))
		}
	}
}
