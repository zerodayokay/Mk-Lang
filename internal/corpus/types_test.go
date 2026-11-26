// Why: types-group reject blocks should be judged without a full std, so a
// std gap cannot hide a checker bug.
// What: the stand-in run over the doc corpus, knownTypesFindings, and the
// Sink and Dangling block regressions.
// How: the stand-in std of names_test.go is checked through types; blocks
// whose need lies within the stages up to types are judged strictly.
//
// Every reject block whose need lies within the stages up to types is PASS
// or PENDING std:. With the stand-in std of names_test.go (every prelude
// name; of members, only the Print surface and std.mmio), the driver judges
// units through types. A block whose need is within those stages is judged
// strictly; a block that needs flow, and every ok block, draws E0203 for
// the members the stand-in lacks, so those are logged, not judged. An
// E0000 is never stand-in noise: each one is a listed finding
// (knownTypesFindings), and a new one fails.

package corpus

import (
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/driver"
)

// knownTypesFindings are doc blocks that the stand-in run judges FAIL for
// a reason outside this package. A block is found
// as knownDesignConflicts finds it, by its document and a source line only
// it holds; has is a substring of its FAIL reason; why says what fails. A
// listed block that stops failing that way is an error, so the list cannot
// go stale.
var knownTypesFindings = []struct {
	doc, line string
	has       string // a substring of its FAIL reason
	why       string
}{
	// The Sink block's lower ICE went with the check fix, and the Dangling
	// block's E0301 (on &n of n := 1) when the block typed its local. Each
	// has a regression test below.
	{"doc/reference.md", "const SmallPrimes: []int = Primes(EvalArena(), 30).Must()", "EvalArena() with no Config.EvalArena function",
		"a const evaluated in EvalArena(), which the evaluator serves through std.core's MakeEvalArena; the stand-in std has none, so evaluation stops with an E0000; mk corpus judges it with the real std"},
	{"doc/examples.md", "        become raw.Compare(a, b, n) // <- E0314", "module std.raw is internal",
		"a block that becomes the intrinsic raw.Compare; the stand-in std has no std.raw, so it gives E0202; mk corpus judges it with the real std"},
	// The blocks below need members or modules the stand-in std does not
	// declare; mk corpus judges each with the real std.
	{"doc/reference.md", "    return Keep(b) // <- E0701", "emitted nothing",
		"the stand-in List is an empty struct, so a Bag holding one is Plain"},
	{"doc/reference.md", "    xs := try a.Slice[str](4) // <- E0701", "has no member `Slice`",
		"Arena.Slice is std's; the stand-in Arena has no members"},
	{"doc/reference.md", "    return p // <- E0301", "has no member `Put`",
		"Arena.Put is std's; the stand-in Arena has no members"},
	{"doc/reference.md", "gen derive.Eq(P) // <- E0704", "module std.derive",
		"std.derive is not in the stand-in std"},
	{"doc/reference.md", "    s := io.Scope() // <- E1003", "has no member `Scope`",
		"Io.Scope is std's; the stand-in Io has no members"},
	{"doc/reference.md", "        s.go Double(3) // <- E0301", "has no member `Scope`",
		"Io.Scope is std's; the stand-in Io has no members"},
	{"doc/reference.md", "            s.go Work() // <- E1003", "has no member `Scope`",
		"Io.Scope is std's; the stand-in Io has no members"},
	{"doc/reference.md", "    scope s := io.ScopeOf[Bad]() { // <- E0802", "has no member `ScopeOf`",
		"Io.ScopeOf is std's; the stand-in Io has no members"},
	{"doc/reference.md", "        s.go Odd() // <- E0801", "has no member `ScopeOf`",
		"Io.ScopeOf is std's; the stand-in Io has no members"},
	{"doc/reference.md", "    try scope s := io.Scope() { // <- E0804", "has no member `Scope`",
		"Io.Scope is std's; the stand-in Io has no members"},
	{"doc/reference.md", "    return str.FromC(p) // <- E1101", "has no member `FromC`",
		"str.FromC is std's; the stand-in std declares no str members"},
	{"doc/reference.md", "        return p + 1 // <- E0301", "module std.raw is internal",
		"the stand-in std has no std.raw, so the import gives E0202"},
	{"doc/std/build.md", "    return build.File(\"data.txt\") // <- E0705", "module std.build",
		"std.build is not in the stand-in std"},
	{"doc/std/core.md", "    _ = m.Remove(k)                          // <- E0701", "`Eq` has no method `Eq`",
		"a block that implements std's Eq and Hash and calls HeapMap.Remove, Replace and Ensure, which the stand-in std lacks"},
	{"doc/std/core.md", "    l.Push(r) else { // <- E0701", "has no member `Push`",
		"a block that calls List.Push and HeapList.Push, which the stand-in std lacks"},
	{"doc/std/raw.md", "    return raw.Read(p) // <- E1101", "module std.raw is internal",
		"the stand-in std has no std.raw, so the import gives E0202"},
	{"doc/std/raw.md", "    return a.LoadOrd(2) // <- E0706", "module std.raw is internal",
		"the stand-in std has no std.raw, so the import gives E0202"},
	{"doc/std/raw.md", "var Level: raw.Atomic[f64] = raw.Atomic[f64].New(0.0) // <- E0701", "module std.raw is internal",
		"the stand-in std has no std.raw, so the import gives E0202"},
}

func knownTypesFinding(u *Unit) int {
	for i, k := range knownTypesFindings {
		if u.Block != nil && u.Block.Document == k.doc && strings.Contains("\n"+u.Block.Source, "\n"+k.line+"\n") {
			return i
		}
	}
	return -1
}

func TestCorpusThroughTypes(t *testing.T) {
	root := repoRoot(t)
	r, err := Run(Options{Driver: driver.Config{StdDir: standInStd(t, root)}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Std.Through != driver.Implemented() || len(r.Std.State.Report.Diags) != 0 {
		var b strings.Builder
		_ = WriteText(&b, r)
		t.Fatalf("stand-in std: through %s, %d std diagnostics, want %s and none\n%s", r.Std.Through, len(r.Std.State.Report.Diags), driver.Implemented(), b.String())
	}
	if r.Std.Through < diag.PhaseTypes {
		t.Logf("through %s: the types stages are not implemented; nothing to judge", r.Std.Through)
		return
	}
	seen := make([]int, len(knownTypesFindings))
	var within, pass, flowPass, logged int
	for _, u := range r.Units {
		// `within` counts the blocks whose need is in the stage set up to
		// types (stagesTypes), not r.Avail: Available() holds StageFlow, and a
		// flow block past the stand-in, which lacks most members, draws
		// E0203 for every member it uses, which says nothing about the
		// block (logged below).
		need, group := unitNeed(u.Unit)
		in := u.Unit.Verdict == VerdictReject && need&stagesTypes == need && group <= min(r.Std.Through, diag.PhaseTypes)
		if i := knownTypesFinding(u.Unit); i >= 0 {
			seen[i]++
			k := knownTypesFindings[i]
			if u.Status != StatusFail || !strings.Contains(u.Reason, k.has) {
				t.Errorf("known finding %s (%s) no longer fails with %q (%s %s): remove it", u.Unit.Key, k.why, k.has, u.Status, u.Reason)
			} else {
				t.Logf("known finding %s: %s FAIL (%s): %s", u.Unit.Key, verdictText(u.Unit), k.why, u.Reason)
			}
			continue
		}
		if in {
			within++
		}
		// The types-stage expectation holds past the stand-in too: a PASS or
		// PENDING unit must be one stage6Problem accepts (a FAIL is judged
		// below).
		checkStage6(t, u, r)
		switch {
		case u.Status == StatusFail && strings.Contains(u.Reason, "internal error"):
			t.Errorf("%s %s: internal error: %s", u.Unit.Key, verdictText(u.Unit), u.Reason)
		case in && u.Status == StatusPass:
			pass++
			t.Logf("through types: %s %s PASS", u.Unit.Key, verdictText(u.Unit))
		case in && u.Status == StatusPending && strings.HasPrefix(u.Reason, "std: "):
			t.Logf("through types: %s %s PENDING %s", u.Unit.Key, verdictText(u.Unit), u.Reason)
		case in:
			t.Errorf("%s %s through types is %s %s; want PASS or PENDING std:", u.Unit.Key, verdictText(u.Unit), u.Status, u.Reason)
		case u.Status == StatusPass && u.Unit.Verdict == VerdictReject && need.Has(driver.StageFlow):
			flowPass++
			t.Logf("past types, through %s: %s %s PASS", r.Std.Through, u.Unit.Key, verdictText(u.Unit))
		case u.Status == StatusFail:
			logged++
			t.Logf("not judged (stand-in lacks most members): %s %s FAIL: %s", u.Unit.Key, verdictText(u.Unit), u.Reason)
		}
	}
	for i, n := range seen {
		if n != 1 {
			t.Errorf("known finding %q matched %d blocks, want 1", knownTypesFindings[i].line, n)
		}
	}
	s := r.Summary
	t.Logf("through %s: %d reject blocks through types, %d PASS; %d flow reject blocks PASS; %d other blocks FAIL on the stand-in (logged); pass %d, fail %d, pending %d",
		r.Std.Through, within, pass, flowPass, logged, s.Pass, s.Fail, s.Pending)
}

// Regression for a finding on doc/memory.md's E0507
// block (struct Sink { out: mut []u8 }, Bad(s: &Sink) { s.out[0] = 1 }):
// lower raised E0000 "*ast.FieldExpr has no type (check gap)" on it, so it
// was listed in knownTypesFindings. Check now types the field write through
// the shared step, so the block has no E0000 and, while flow is not wired,
// waits for flow (E0507's write-through-shared form is flow's); once
// flow lands it must PASS.
func TestCorpusThroughTypesSinkBlockHasNoICE(t *testing.T) {
	flowBlockPastTypes(t, "    s.out[0] = 1 // <- E0507", diag.E0000)
}

// Regression for a finding on the Dangling block (doc/memory.md, reject
// E0603): it wrote n := 1 and returned &n as &i32, which is E0301 (literals
// bind as int), so it was listed in knownTypesFindings until the block
// typed the local, n: i32 = 1. It must draw no E0301 (and no E0000) and,
// like the Sink block, wait for flow while flow is not wired and PASS once
// it is.
func TestCorpusThroughTypesDanglingBlockIsNotE0301(t *testing.T) {
	flowBlockPastTypes(t, "    return &n // <- E0603", diag.E0000, diag.E0301)
}

// flowBlockPastTypes runs the doc corpus with the near member-less stand-in
// std, finds the one doc/memory.md block holding line, and
// requires that it draws none of banned and is PENDING needs StageFlow
// while flow is not wired, PASS once it is.
func flowBlockPastTypes(t *testing.T, line string, banned ...diag.Code) {
	t.Helper()
	root := repoRoot(t)
	r, err := Run(Options{Driver: driver.Config{StdDir: standInStd(t, root)}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Std.Through < diag.PhaseTypes {
		t.Fatalf("stand-in std judged through %s, want types", r.Std.Through)
	}
	var found []UnitResult
	for _, u := range r.Units {
		if u.Unit.Block != nil && u.Unit.Block.Document == "doc/memory.md" && strings.Contains("\n"+u.Unit.Block.Source, "\n"+line+"\n") {
			found = append(found, u)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the block holding %q matched %d blocks, want 1", line, len(found))
	}
	u := found[0]
	for _, d := range u.Emitted {
		for _, c := range banned {
			if d.Code == c {
				t.Errorf("%s: %s again: %s", u.Unit.Key, d.Code, d.Message)
			}
		}
	}
	want := "PENDING needs StageFlow"
	if r.Avail.Has(driver.StageFlow) {
		want = "PASS"
	}
	if got := u.Status.String() + " " + u.Reason; strings.TrimSpace(got) != want {
		t.Errorf("%s %s: %s, want %s", u.Unit.Key, verdictText(u.Unit), got, want)
	}
}
