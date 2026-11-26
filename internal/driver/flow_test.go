// Why: stage 7 runs the borrow checker on every function, std's included;
// std must stay flow-clean and a user's flow errors must be reported.
// What: TestStdFlowKnown, with its pin stdFlowKnown, and
// TestStage7ReportsUserBorrowErrors.
// How: checks the embedded std through stage 7 on both targets, and user
// modules beside std, and compares diagnostics by file and code.

package driver

import (
	"slices"
	"strings"
	"testing"

	"mk/internal/diag"
)

// stdFlowKnown are the embedded std's stage-7 diagnostics, counted by
// "path:code" (lines move as std is edited; the pin must not), each with
// the reason it is still there. Empty: the embedded std is flow-clean on
// both targets.
var stdFlowKnown = map[string]struct {
	n   int
	why string
}{}

// TestStdFlowKnown checks the embedded std through stage 7 on both targets:
// its diagnostics are exactly stdFlowKnown by (file, code) and count, all of
// the flow phase. A fixed entry, or a new one, fails, so the list follows
// std's fixes.
func TestStdFlowKnown(t *testing.T) {
	for _, target := range []string{"x86_64-windows", "x86_64-linux"} {
		tg, _, ok := TargetNamed(target)
		if !ok {
			t.Fatalf("no target %s", target)
		}
		dir := writeTree(t, map[string]string{"a.mk": "export Main(sys: &System) {\n}\n"})
		s, err := NewSession(Config{Root: dir, Target: tg})
		if err != nil {
			t.Fatal(err)
		}
		r := s.Check()
		got := map[string]int{}
		for _, e := range r.Report.Diags {
			if e.Diag.Phase != diag.PhaseFlow {
				t.Errorf("%s: a %v diagnostic: %s %s", target, e.Diag.Phase, e.Diag.Code, e.Diag.Message)
			}
			got[r.Sources.File(e.Diag.At.File).Path+":"+e.Diag.Code.String()]++
		}
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if want, ok := stdFlowKnown[k]; !ok || got[k] > want.n {
				t.Errorf("%s: new std stage-7 diagnostic %s (%d): fix it or add it to stdFlowKnown with its owner", target, k, got[k])
			}
		}
		for k, want := range stdFlowKnown {
			if got[k] < want.n {
				t.Errorf("%s: %s reported %d times, known %d (%s): lower the count", target, k, got[k], want.n, want.why)
			}
		}
		t.Logf("%s: %d std stage-7 diagnostics:\n%s", target, len(r.Report.Diags), strings.Join(lines(r), "\n"))
	}
}

// TestStage7ReportsUserBorrowErrors: a user body borrow rejects is reported
// in its module beside std's (every error of the flow phase comes in one
// run), and a clean importer of a module with a stage-7 diagnostic is still
// checked through stage 7, because a flow diagnostic leaves the module's
// types sound.
func TestStage7ReportsUserBorrowErrors(t *testing.T) {
	files := pkgFiles(map[string]string{
		"a/a.mk": "export linear struct R {\n    v: int\n}\n\nexport R.Close(self) {\n    R(v: _) := self\n}\n\nexport Leak(r: R) {\n}\n",
		"b/b.mk": "import a\n\nexport Twice(r: a.R) {\n    r.Close()\n    r.Close()\n}\n",
	})
	r := check(t, Config{Root: writeTree(t, files), StdDir: fakeStd(t)})
	got := lines(r)
	t.Logf("%q", got)
	var a, b bool
	for _, l := range got {
		a = a || strings.HasPrefix(l, "a/a.mk:")
		b = b || strings.HasPrefix(l, "b/b.mk:")
	}
	if !a || !b {
		t.Errorf("want stage-7 diagnostics in both a and b, got %q\n%s", got, r.Report.Text(r.Sources))
	}
	for _, e := range r.Report.Diags {
		if e.Diag.Phase != diag.PhaseFlow {
			t.Errorf("a %v diagnostic: %s", e.Diag.Phase, e.Diag.Message)
		}
	}
}

// TestQualifiedGlobalWritesAreE0507 checks that qualified and unqualified
// globals reach the same shared-place rule for both assignment forms.
func TestQualifiedGlobalWritesAreE0507(t *testing.T) {
	files := pkgFiles(map[string]string{
		"globals/values.mk": "export var Count: int = 0\n\nDirect() {\n    Count = 1\n}\n",
		"main.mk":           "import globals\n\nexport Main() {\n    globals.Count = 1\n    globals.Count += 1\n}\n",
	})
	r := check(t, Config{Root: writeTree(t, files), StdDir: fakeStd(t)})
	want := []string{"E0507", "E0507", "E0507"}
	if got := codes(r); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v\n%s", got, want, r.Report.Text(r.Sources))
	}
}
