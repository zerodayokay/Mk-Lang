// Why: a query answer is read by people and editors who know a dependency
// by the root's local name, not by its canonical id.
// What: tests of mk query, mk lsp's QueryChecked and the query index over
// packages with dependencies.
// How: the query index spells a dependency's module paths with the root's
// local names (modPath); the tests compare the answers' text.

package driver

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQuery_DependencyByLocalName(t *testing.T) {
	ws := workspace(t, []string{pathDep("u", "../util")}, map[string]string{
		"util/src/t.mk": "export error Bad\n\nexport F(n: int): Bad!int {\n    if n < 0 {\n        fail Bad\n    }\n    return n\n}\n",
		"app/src/a.mk":  "import u.src\n\nexport A(): src.Bad!int {\n    x := try src.F(1)\n    return x\n}\n",
	})
	s, err := NewSession(Config{Root: filepath.Join(ws, "app"), StdDir: fakeStd(t)})
	if err != nil {
		t.Fatal(err)
	}
	a, res, err := s.Query("errors", []string{"A"})
	if err != nil || a == nil {
		t.Fatalf("%v, %q", err, lines(res))
	}
	// The error set names Bad's module: u.src, as the root imports it.
	if text := a.Text(); strings.Contains(text, utilID) || !strings.Contains(text, "Bad  u.src") {
		t.Errorf("errors A:\n%s", text)
	}
}

// mk lsp's QueryChecked localizes as mk query does.
func TestQuery_CheckedDependencyByLocalName(t *testing.T) {
	ws := workspace(t, []string{pathDep("u", "../util")}, map[string]string{
		"util/src/t.mk": "export error Bad\n\nexport F(n: int): Bad!int {\n    if n < 0 {\n        fail Bad\n    }\n    return n\n}\n",
		"app/src/a.mk":  "import u.src\n\nexport A(): src.Bad!int {\n    x := try src.F(1)\n    return x\n}\n",
	})
	s, err := NewSession(Config{Root: filepath.Join(ws, "app"), StdDir: fakeStd(t)})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); len(r.Report.Diags) > 0 {
		t.Fatalf("%q", lines(r))
	}
	a, err := s.QueryChecked("errors", []string{"A"})
	if err != nil || a == nil {
		t.Fatalf("%v", err)
	}
	if text := a.Text(); strings.Contains(text, utilID) || !strings.Contains(text, "Bad  u.src") {
		t.Errorf("errors A:\n%s", text)
	}
}

// A dependency's exported items are in the query index, named
// by the root's local name.
func TestQuery_FindsDependencyItems(t *testing.T) {
	ws := workspace(t, []string{pathDep("u", "../util")}, map[string]string{
		"util/src/t.mk": "export Twelve(): int {\n    return 12\n}\n\nHidden(): int {\n    return 1\n}\n",
		"app/src/a.mk":  "import u.src\n\nexport A(): int {\n    return src.Twelve()\n}\n",
	})
	s, err := NewSession(Config{Root: filepath.Join(ws, "app"), StdDir: fakeStd(t)})
	if err != nil {
		t.Fatal(err)
	}
	a, res, err := s.Query("find", []string{"Twelve"})
	if err != nil || a == nil {
		t.Fatalf("%v, %q", err, lines(res))
	}
	if text := a.Text(); !strings.Contains(text, "u.src") || strings.Contains(text, utilID) {
		t.Errorf("find Twelve:\n%s", text)
	}
	if a, _, _ := s.Query("find", []string{"Hidden"}); a != nil && strings.Contains(a.Text(), "u.src") {
		t.Errorf("an unexported dependency item answers:\n%s", a.Text())
	}
}

// A workspace member's canonical id is its manifest name, an ordinary word:
// only module paths are rewritten to the root's local name, never a
// signature's, a contract's, a doc comment's or a test name's word that
// equals the id.
func TestQuery_LocalizesModulePathsOnly(t *testing.T) {
	ws := writeTree(t, map[string]string{
		"pkg.mk":      "const Package = pkg.Package(\n    name: \"ws\",\n    members: [\"hp\", \"eng\"],\n)\n",
		"hp/pkg.mk":   "const Package = pkg.Package(\n    name: \"hp\",\n)\n",
		"hp/src/h.mk": "export struct Heap {\n    export n: int\n}\n\n/// Takes hp.\nexport Grow(hp: mut Heap) {\n    hp.n += 1\n}\n",
		"eng/pkg.mk":  "const Package = pkg.Package(\n    name: \"eng\",\n    deps: [" + pathDep("heap", "../hp") + "],\n)\n",
		"eng/src/a.mk": "import heap.src\n\n/// Its parameter is hp, not heap.\nexport Run(hp: mut src.Heap)\n    requires hp.n >= 0\n{\n    hp.n += 1\n}\n\n" +
			"test \"hp.n grows\" {\n    var h := src.Heap(n: 0)\n    Run(mut h)\n}\n",
	})
	s, err := NewSession(Config{Root: filepath.Join(ws, "eng"), StdDir: fakeStd(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, want string }{
		{"Run", "export Run(hp: mut Heap)\n    requires hp.n >= 0\nmodule src\n/// Its parameter is hp, not heap.\n"},
		// A dependency's item: its module line is localized, its text is not.
		{"Grow", "export Grow(hp: mut Heap)\nmodule heap.src\n/// Takes hp.\n"},
	} {
		a, res, err := s.Query("sig", []string{tc.name})
		if err != nil || a == nil {
			t.Fatalf("%v, %q", err, lines(res))
		}
		if got := a.Text(); got != tc.want {
			t.Errorf("sig %s:\n%s\nwant:\n%s", tc.name, got, tc.want)
		}
		c, err := s.QueryChecked("sig", []string{tc.name})
		if err != nil || c == nil || c.Text() != tc.want {
			t.Errorf("QueryChecked sig %s: %v\n%v", tc.name, err, c)
		}
	}
	// A test is named by its key, whose quoted name is the source's.
	a, res, err := s.Query("callers", []string{"Run"})
	if err != nil || a == nil || !strings.Contains(a.Text(), `src#test("hp.n grows")`) {
		t.Errorf("callers Run: %v %v %q", err, a, lines(res))
	}
}

// No answer of any query names a dependency by its canonical id: every
// module path it prints is spelled with the root's local name, also inside
// an item key (stack names an impl method by its key).
func TestQuery_NoAnswerHoldsCanonicalID(t *testing.T) {
	ws := workspace(t, []string{pathDep("u", "../util")}, map[string]string{
		"util/src/t.mk": "export error Bad\n\nexport struct Box {\n    export n: int\n}\n\nexport Box.Get(&self): int {\n    return self.n\n}\n\nexport type Num = int\n\nexport F(n: int): Bad!int {\n    if n < 0 {\n        fail Bad\n    }\n    return n\n}\n\nexport P(xs: []int, i: int): int {\n    return xs[i]\n}\n\n" +
			"export interface Shape {\n    Area(&self): int\n}\n\nimpl Shape for Box {\n    Area(&self): int {\n        return Deep(self.n)\n    }\n}\n\nDeep(n: int): int {\n    return n + 1\n}\n",
		// The module at util's root, whose path is the canonical id alone.
		"util/r.mk": "export interface Sized {\n    Size(&self): int\n}\n\nexport struct Cell {\n    export n: int\n}\n\nimpl Sized for Cell {\n    Size(&self): int {\n        return Deep2(self.n)\n    }\n}\n\nDeep2(n: int): int {\n    return n\n}\n",
		"app/src/a.mk": "import u\nimport u.src\n\nexport A(): src.Bad!int {\n    x := try src.F(1)\n    return x\n}\n\nexport B(xs: []int, b: src.Box): int {\n    return src.P(xs, 0) + b.Get()\n}\n\nexport C(b: src.Box): src.Num {\n    return b.n\n}\n\n" +
			"export D(b: src.Box): int {\n    return b.Area()\n}\n\nexport E(c: u.Cell): int {\n    return c.Size()\n}\n",
	})
	s, err := NewSession(Config{Root: filepath.Join(ws, "app"), StdDir: fakeStd(t), KeepSyntax: true})
	if err != nil {
		t.Fatal(err)
	}
	s.fullStd = true
	if r := s.Check(); len(r.Report.Diags) > 0 {
		t.Fatalf("%q", lines(r))
	}
	qx := newQueryIndexOf(s, true)
	subjects := []string{"A", "B", "C", "F", "P", "Box", "Bad", "Num", "Box.Get", "src.F", "src.Box", "Two", "D", "E"}
	found, implKey, rootKey := 0, false, false
	for _, q := range QueryNames {
		argsets := [][]string{nil}
		switch q.Args {
		case 1:
			argsets = nil
			for _, sb := range subjects {
				argsets = append(argsets, []string{sb})
			}
		case 2:
			argsets = [][]string{{"A", "4"}, {"B", "10"}}
		}
		for _, args := range argsets {
			a := answerQuery(qx, q.Name, args, false)
			if q.Name == "perimeter" {
				a = perimeterAnswer(s)
			}
			if a == nil {
				continue
			}
			for _, l := range a.Answer {
				if strings.Contains(l, utilID) {
					t.Errorf("%s %q: %s", q.Name, args, l)
				}
				if strings.Contains(l, "u.src") {
					found++
				}
				implKey = implKey || strings.Contains(l, "D -> u.src#impl(u.src.Shape for u.src.Box):Area -> Deep")
				rootKey = rootKey || strings.Contains(l, "E -> u#impl(u.Sized for u.Cell):Size -> Deep2")
			}
		}
	}
	// find, sig, import and errors name u.src for the dependency's items.
	if found < 4 {
		t.Errorf("%d lines name u.src, want find's, sig's, import's and errors'", found)
	}
	if !implKey || !rootKey {
		t.Errorf("stack D and E name their impl methods by keys with u.src and u: %v %v", implKey, rootKey)
	}
}
