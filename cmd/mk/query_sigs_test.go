// Why: a tool that reasons about calls (a lint that needs every
// function taking `mut Heap`) needs every signature of a workspace member
// in one process, those a gen line writes included.
// What: tests of `mk query sig --member <name> [--json]`: the listing, its
// agreement with `sig Name`, usage errors and the documented schema.
// How: mkh176Workspace builds a member with plain functions, a method, an
// impl method, a contract, an invariant, a fn literal, a test, and the
// functions of two gen lines (std.dyn's and a generator of its own that
// writes a top-level function); the JSON is decoded generically.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const mkh176Gen = `// Step writes a top-level function that takes the heap mutably.
export Step(g: mut Gen, t: Type) {
    g.Emit(quote {
        GenStep(h: mut $t) {
            h.n += 1
        }
    })
}
`

const mkh176Heap = `import std.dyn
import src.mkgen

/// A heap.
export struct Heap {
    n: int
    invariant self.n >= 0
}

/// Takes the heap mutably.
export Collect(h: mut Heap) {
    h.n += 1
}

export Peek(h: &Heap): int
    requires h.n >= 0
{
    return h.n
}

export Heap.Grow(mut self, by: int) {
    self.n += by
}

interface Shape {
    Area(&self): f64
}

struct Square {
    side: f64
}

impl Shape for Square {
    Area(&self): f64 {
        return self.side
    }
}

gen dyn.Of(Shape)

gen mkgen.Step(Heap)

Use(h: mut Heap): f64 {
    var s := Square(side: 2.0)
    var d := ShapeDyn.Of(mut s)
    f := fn(y: int): int { return y }
    Collect(h)
    GenStep(h)
    _ = f(1)
    return d.Area()
}

test "t" {
}
`

// mkh176Want is each entry as name|sig|file:line|contracts|impl|gen, in
// the listing's order (file, line, column, name). The gen view file is not
// written by a query; its positions are the ones mk check's view has.
var mkh176Want = []string{
	"ShapeDyn.Of|ShapeDyn.Of[T: Shape](x: mut T): ShapeDyn from x|src/gen/" + mkh176Target + "/gen.mk:10||| src/heap.mk:39:1",
	"ShapeDyn.AdaptArea|ShapeDyn.AdaptArea[T: Shape](obj: &Ref): f64|src/gen/" + mkh176Target + "/gen.mk:15||| src/heap.mk:39:1",
	"ShapeDyn.Area|ShapeDyn.Area(&self): f64|src/gen/" + mkh176Target + "/gen.mk:21||Shape| src/heap.mk:39:1",
	"GenStep|GenStep(h: mut Heap)|src/gen/" + mkh176Target + "/gen.mk:28||| src/heap.mk:41:1",
	"Collect|export Collect(h: mut Heap)|src/heap.mk:11|||",
	"Peek|export Peek(h: &Heap): int|src/heap.mk:15|requires h.n >= 0||",
	"Heap.Grow|export Heap.Grow(mut self, by: int)|src/heap.mk:21|||",
	"Square.Area|Square.Area(&self): f64|src/heap.mk:34||Shape|",
	"Use|Use(h: mut Heap): f64|src/heap.mk:43|||",
	"Step|export Step(g: mut Gen, t: Type)|src/mkgen/mkgen.mk:2|||",
}

// mkh176Target is the target every query here names, so the gen view's
// path is the same on every host.
const mkh176Target = "x86_64-windows"

func mkh176Workspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for name, text := range map[string]string{
		"pkg.mk":                 "const Package = pkg.Package(\n    name: \"ws\",\n    members: [\"eng\"],\n)\n",
		"eng/pkg.mk":             "const Package = pkg.Package(\n    name: \"eng\",\n)\n",
		"eng/src/heap.mk":        mkh176Heap,
		"eng/src/mkgen/mkgen.mk": mkh176Gen,
	} {
		p := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

// sigListDoc is the --json document, decoded generically.
type sigListDoc struct {
	Version   int              `json:"version"`
	Query     string           `json:"query"`
	Member    string           `json:"member"`
	Complete  bool             `json:"complete"`
	Missing   []string         `json:"missing"`
	Functions []map[string]any `json:"functions"`
}

// TestMemberSignatures: from the workspace root, every function
// and method of the member is listed with its signature, the generated ones
// marked with their gen line; no contract predicate, invariant, fn literal
// or test is; each sig is the line `sig Name` answers first; a warm run
// prints the same bytes; the text form works from the member's directory.
func TestMemberSignatures(t *testing.T) {
	ws := mkh176Workspace(t)
	st, out, errOut := mk("query", "sig", "--member", "eng", "--root", ws, "--target", mkh176Target, "--json")
	if st != 0 {
		t.Fatalf("status %d\nstdout %s\nstderr %s", st, out, errOut)
	}
	var l sigListDoc
	if err := json.Unmarshal([]byte(out), &l); err != nil {
		t.Fatalf("JSON: %v\n%s", err, out)
	}
	if l.Version != 1 || l.Query != "sig" || l.Member != "eng" || !l.Complete || l.Missing == nil || len(l.Missing) != 0 {
		t.Errorf("header: version %d query %q member %q complete %v missing %v", l.Version, l.Query, l.Member, l.Complete, l.Missing)
	}
	fields := []string{"name", "module", "file", "line", "col", "sig", "contracts", "exported", "impl", "generated", "gen"}
	var got []string
	for _, e := range l.Functions {
		for _, f := range fields {
			if _, ok := e[f]; !ok {
				t.Errorf("entry %v lacks %q", e, f)
			}
		}
		var cs []string
		for _, c := range e["contracts"].([]any) {
			cs = append(cs, c.(string))
		}
		g := ""
		if e["generated"] == true {
			g = " " + e["gen"].(string)
		}
		got = append(got, strings.Join([]string{e["name"].(string), e["sig"].(string),
			e["file"].(string) + ":" + strconv.Itoa(int(e["line"].(float64))), strings.Join(cs, "; "), e["impl"].(string), g}, "|"))
	}
	if !slices.Equal(got, mkh176Want) {
		t.Errorf("functions:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(mkh176Want, "\n"))
	}
	// The one-name form is unchanged, and its first line is the entry's sig.
	for _, name := range []string{"Collect", "Peek", "Heap.Grow", "GenStep", "ShapeDyn.Of"} {
		st, one, _ := mk("query", "sig", name, "--root", filepath.Join(ws, "eng"), "--target", mkh176Target, "--json")
		var a struct {
			Answer []string `json:"answer"`
		}
		if err := json.Unmarshal([]byte(one), &a); st != 0 || err != nil || len(a.Answer) == 0 {
			t.Errorf("sig %s: status %d %v\n%s", name, st, err, one)
			continue
		}
		if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, name+"|"+a.Answer[0]+"|") }) {
			t.Errorf("sig %s answers %q, which the listing does not hold", name, a.Answer[0])
		}
	}
	if st, warm, _ := mk("query", "sig", "--member", "eng", "--root", ws, "--target", mkh176Target, "--json"); st != 0 || warm != out {
		t.Errorf("warm run: status %d, output differs from the cold one:\n%s", st, warm)
	}
	st, text, errOut := mk("query", "sig", "--member", "eng", "--root", filepath.Join(ws, "eng"), "--target", mkh176Target)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if st != 0 || lines[0] != "member eng: 10 function(s)" ||
		!slices.Contains(lines, "export Collect(h: mut Heap)  src/heap.mk:11:8") ||
		!slices.Contains(lines, "    requires h.n >= 0") ||
		!slices.Contains(lines, "GenStep(h: mut Heap)  src/gen/"+mkh176Target+"/gen.mk:28:1 (gen src/heap.mk:41:1)") {
		t.Errorf("text: status %d\n%s\nstderr %s", st, text, errOut)
	}
	if _, err := os.Stat(filepath.Join(ws, "eng", "src", "gen")); err == nil {
		t.Error("the query wrote the gen view")
	}
}

// TestMemberSignaturesUsage: a name with --member, an unknown member and
// two names are usage errors (exit 2).
func TestMemberSignaturesUsage(t *testing.T) {
	ws := mkh176Workspace(t)
	if st, _, errOut := mk("query", "sig", "Collect", "--member", "eng", "--root", ws); st != 2 || !strings.Contains(errOut, "--member goes with panics or sig alone") {
		t.Errorf("sig Name --member: status %d stderr %q", st, errOut)
	}
	if st, _, errOut := mk("query", "sig", "--member", "nope", "--root", ws); st != 2 || !strings.Contains(errOut, "the workspace's members are eng") {
		t.Errorf("unknown member: status %d stderr %q", st, errOut)
	}
	if st, _, _ := mk("query", "sig", "Collect", "Peek", "--root", filepath.Join(ws, "eng")); st != 2 {
		t.Errorf("sig with two names: status %d", st)
	}
}

// TestSigSchemaDocumented: the "### 5.2" part of doc/tools.md names every
// field of the document.
func TestSigSchemaDocumented(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "doc", "tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, "### 5.2")
	if i < 0 {
		t.Fatal("doc/tools.md has no section 5.2")
	}
	sec := doc[i:]
	if j := strings.Index(sec, "\n## "); j >= 0 {
		sec = sec[:j]
	}
	for _, n := range []string{"version", "query", "member", "complete", "missing", "functions",
		"name", "module", "file", "line", "col", "sig", "contracts", "exported", "impl", "generated", "gen"} {
		if !strings.Contains(sec, "`"+n+"`") {
			t.Errorf("doc/tools.md's section 5.2 does not name `%s`", n)
		}
	}
}
