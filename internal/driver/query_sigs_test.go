// Why: `mk query sig --member` must list every function a call can name,
// those a gen line writes included, and nothing a call cannot.
// Every entry, a type's too, must be what `sig <subject>` answers.
// What: TestSignatures and TestSignaturesAnswerAsSig, over
// Session.Signatures on a package without a workspace.
// How: checks a one-module package and compares the entries' names, sigs
// and gen marks (an error's provided Format among them), and the text
// form's header; then, over a three-module package with every item kind,
// asks a second session `sig <subject>` for each entry and compares the
// answer line for line.

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

// sigsEverySource is a package with an item of every kind the listing
// holds, documented, over three modules: src/more's Point and Thing take
// the bare spellings from src's (src/more's module comes first), and two
// modules end in more, so src/x/more's Solo has only its full path.
var sigsEverySource = map[string]string{
	"pkg.mk": "const Package = pkg.Package(\n    name: \"p\",\n    unsafe_modules: [\"src\"],\n)\n",
	"src/a.mk": `import std.dyn

/// Labs is C's labs.
extern labs(x: i64): i64

/// Shape has an area.
export interface Shape {
    Area(&self): f64
}

/// Point is a point.
export struct Point {
    export x: int
}

/// Point.Sum adds.
export Point.Sum(&self): int {
    return self.x
}

impl Shape for Point {
    /// Area of a point.
    Area(&self): f64 {
        return 0.0
    }
}

/// Tok is linear.
export linear struct Tok {
    n: int
}

/// Color is open.
export open enum Color {
    Red
    Green
}

/// Oops fails.
export error Oops

/// Word is a word.
export type Word = u64

/// Max is the most.
export const Max: int = 10

/// Count counts.
var Count: int = 0

/// Box holds one.
export struct Box[T] {
    v: T
}

/// Box.Get reads it.
export Box.Get[T](&self): &T from self {
    return &self.v
}

/// Thing is the root's.
export Thing(): int {
    return 1
}

` + strings.Repeat("/// One line of a long doc that passes the answer's cap, which is 800 bytes.\n", 14) + `export Long(x: int): int
    requires x > 0
{
    return x
}

/// Guarded's contracts alone pass the cap.
export Guarded(x: int): int
` + strings.Repeat("    requires x != 1000000000000000000\n", 25) + `{
    return x
}

gen dyn.Of(Shape)
`,
	"src/more/b.mk":   "/// Point is the other point.\nexport struct Point {\n    export y: int\n}\n\n/// Thing is the other Thing.\nexport Thing(): int {\n    return 2\n}\n\n/// Solo is alone.\nexport Solo(): int {\n    return 3\n}\n",
	"src/x/more/c.mk": "/// Solo is the other module more's.\nexport Solo(): int {\n    return 4\n}\n",
}

// TestSignaturesAnswerAsSig: every entry of the listing, functions and the
// other items, is `sig Subject`'s answer: its first line, its contracts,
// its module line, its doc lines and its cut, item by item, over every item
// kind, a generic owner's method, generated functions, an extern and
// spellings that another module's item takes; only an impl method (an
// error's built-in Format among them) has no subject. An impl method has
// its own doc lines and the built-in Format none, though it is located at
// its error's name; an entry is generated exactly when it is in the gen
// view, a type too.
func TestSignaturesAnswerAsSig(t *testing.T) {
	t.Parallel()
	dir := writeTree(t, sigsEverySource)
	cfg := Config{Root: dir, CacheDir: filepath.Join(dir, ".cache")}
	s, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, res := s.Signatures("p")
	if l == nil || len(res.Report.Diags) != 0 || !l.Complete {
		t.Fatalf("no listing:\n%s", diagLines(res))
	}
	q, err := NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	subjects := map[string]string{}
	implDocs := map[string][]string{}
	cut, headCut := false, false
	for i, e := range append(slices.Clone(l.Functions), l.Items...) {
		if want := i >= len(l.Functions); want != (e.Kind != "func") {
			t.Errorf("%s: kind %q in the wrong array", e.Name, e.Kind)
		}
		kinds[e.Kind] = true
		if e.Doc == nil || e.Contracts == nil {
			t.Errorf("%s: doc %v contracts %v, not []", e.Name, e.Doc, e.Contracts)
		}
		if gv := strings.HasPrefix(e.File, "src/gen/"); e.Generated != gv || e.Generated != (e.Gen != "") {
			t.Errorf("%s (%s %s): generated %v gen %q", e.Name, e.Kind, e.File, e.Generated, e.Gen)
		}
		if e.Subject == "" {
			if e.Impl == "" {
				t.Errorf("%s (module %s): no subject", e.Name, e.Module)
			}
			implDocs[e.Module+" "+e.Name] = e.Doc
			continue
		}
		subjects[e.Module+" "+e.Name] = e.Subject
		cut = cut || e.Truncated > 0
		a, _, err := q.Query("sig", []string{e.Subject})
		if err != nil || a == nil || !a.Found {
			t.Errorf("sig %s: %v %v", e.Subject, a, err)
			continue
		}
		want := []string{e.Sig}
		if e.Kind == "func" {
			for _, c := range e.Contracts {
				want = append(want, "    "+c)
			}
			want = append(want, "module "+e.Module)
		}
		want = append(want, e.Doc...)
		if len(e.Doc) == 0 && e.Truncated > 0 && len(a.Answer) < len(want) {
			// The cut falls inside the head: the answer is its first lines.
			want, headCut = want[:len(a.Answer)], true
		}
		if !slices.Equal(a.Answer, want) || a.Truncated != e.Truncated {
			t.Errorf("sig %s answers (%d cut)\n%s\nthe entry (%d cut)\n%s", e.Subject, a.Truncated,
				strings.Join(a.Answer, "\n"), e.Truncated, strings.Join(want, "\n"))
		}
	}
	for _, k := range []string{"func", "struct", "enum", "error", "interface", "alias", "const", "global"} {
		if !kinds[k] {
			t.Errorf("no entry of kind %s", k)
		}
	}
	if !cut {
		t.Error("no entry's doc is cut: Long's doc is meant to pass the cap")
	}
	if !headCut {
		t.Error("no entry's head is cut: Guarded's contracts are meant to pass the cap")
	}
	for k, want := range map[string][]string{"src Point.Area": {"/// Area of a point."}, "src Oops.Format": {}} {
		if got, ok := implDocs[k]; !ok || !slices.Equal(got, want) {
			t.Errorf("%s: doc %q (listed %v), want %q", k, got, ok, want)
		}
	}
	if !slices.ContainsFunc(l.Items, func(e SigEntry) bool { return e.Name == "ShapeDyn" && e.Generated }) {
		t.Error("no generated item: gen dyn.Of(Shape) writes the struct ShapeDyn")
	}
	for k, want := range map[string]string{"src.more Point": "Point", "src Point": "src.Point", "src.more Thing": "Thing",
		"src Thing": "src.Thing", "src.more Solo": "Solo", "src.x.more Solo": "src.x.more.Solo", "src Box.Get": "Box.Get"} {
		if subjects[k] != want {
			t.Errorf("%s: subject %q, want %q", k, subjects[k], want)
		}
	}
}
