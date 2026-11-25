// Why: `mk query sig --member` must list every function a call can name,
// those a gen line writes included, and nothing a call cannot.
// What: TestSignatures, over Session.Signatures on a package without a
// workspace.
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
