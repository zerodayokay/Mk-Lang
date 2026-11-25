// Why: diagnostics are the compiler's user interface; their codes, JSON and
// order must be stable and match the docs.
// What: tests of the catalogue against doc/errors.md, phases, ICEs, the
// JSONWriter, the JSON golden, and Order (sorting, dedup, roots, phase, cap).
// How: a two-file fixture source map; golden JSON (-update rewrites it).

package diag

import (
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mk/internal/source"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestCatalogueMatchesDocs compares every row of the embedded table with
// doc/errors.md: same codes in the same order, same names, same fix-class
// text.
func TestCatalogueMatchesDocs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "doc", "errors.md"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Entry
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "| E") {
			continue
		}
		line = strings.ReplaceAll(line, `\|`, "\x00")
		cells := strings.Split(line, "|")
		if len(cells) != 6 {
			t.Fatalf("catalogue row with %d cells: %q", len(cells), line)
		}
		for i := range cells {
			cells[i] = strings.ReplaceAll(strings.TrimSpace(cells[i]), "\x00", "|")
		}
		c, ok := parseAnyCode(cells[1])
		if !ok {
			t.Fatalf("bad code cell %q", cells[1])
		}
		rows = append(rows, Entry{Code: c, Name: cells[2], FixClass: cells[4]})
	}
	got := Catalogue()
	if len(rows) != len(got) {
		t.Errorf("design has %d rows, table has %d", len(rows), len(got))
	}
	for i := 0; i < len(rows) && i < len(got); i++ {
		if rows[i] != got[i] {
			t.Errorf("row %d: design %+v, table %+v", i, rows[i], got[i])
		}
	}
}

func parseAnyCode(s string) (Code, bool) {
	if len(s) != 5 || s[0] != 'E' {
		return 0, false
	}
	n := 0
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return Code(n), true
}

// TestE0000AndE0001AreCatalogued checks the internal-error and toolchain
// codes and their phases.
func TestE0000AndE0001AreCatalogued(t *testing.T) {
	if E0000.Name() != "internal-error" || E0001.Name() != "toolchain" {
		t.Fatalf("names %q %q", E0000.Name(), E0001.Name())
	}
	if DefaultPhase(E0000) != PhaseInternal || DefaultPhase(E0001) != PhaseNative {
		t.Fatal("phases of E0000, E0001")
	}
}

func TestCodeStringAndParse(t *testing.T) {
	for _, e := range Catalogue() {
		s := e.Code.String()
		if len(s) != 5 {
			t.Fatalf("%d renders %q", e.Code, s)
		}
		c, ok := ParseCode(s)
		if !ok || c != e.Code {
			t.Fatalf("ParseCode(%q) = %v, %v", s, c, ok)
		}
	}
	for _, bad := range []string{"E0407", "E0606", "E1201", "E501", "e0501", "E05011", ""} {
		if _, ok := ParseCode(bad); ok {
			t.Errorf("ParseCode(%q) accepted", bad)
		}
	}
}

// TestPhaseOfEveryCode: every code but E0000 maps to a user phase or native,
// and the default phases the corpus depends on hold.
func TestPhaseOfEveryCode(t *testing.T) {
	for _, e := range Catalogue() {
		p := DefaultPhase(e.Code)
		if !p.IsValid() || (p == PhaseInternal) != (e.Code == E0000) {
			t.Errorf("%v -> %v", e.Code, p)
		}
	}
	want := map[Code]Phase{
		E0101: PhaseParse, E0102: PhaseParse, E1206: PhaseParse, E1222: PhaseParse,
		E0202: PhaseImports, E0205: PhaseImports, E1102: PhaseImports,
		E0201: PhaseNames, E0207: PhaseNames, E1209: PhaseNames, E1221: PhaseNames,
		E0203: PhaseTypes, E0609: PhaseTypes, E0208: PhaseTypes, E0209: PhaseTypes, E0301: PhaseTypes, E0410: PhaseTypes,
		E0412: PhaseTypes, E0601: PhaseTypes, E0703: PhaseTypes, E0802: PhaseTypes, E0908: PhaseTypes,
		E1003: PhaseTypes, E1103: PhaseTypes, E1104: PhaseTypes, E1105: PhaseTypes, E1106: PhaseTypes,
		E1107: PhaseTypes, E1108: PhaseTypes, E1202: PhaseTypes,
		E0401: PhaseFlow, E0411: PhaseFlow, E0501: PhaseFlow, E0602: PhaseFlow, E0605: PhaseFlow,
		E0807: PhaseFlow, E1001: PhaseFlow, E1002: PhaseFlow,
	}
	for c, p := range want {
		if got := DefaultPhase(c); got != p {
			t.Errorf("DefaultPhase(%v) = %v, want %v", c, got, p)
		}
	}
	for _, p := range Phases() {
		if strings.HasPrefix(p.String(), "phase(") {
			t.Errorf("phase %d has no name", p)
		}
	}
}

func TestNewRejectsUnknownCode(t *testing.T) {
	ie := source.Guard("t", source.Span{}, func() { New(Code(407), source.Span{}, "x") })
	if ie == nil || ie.Runtime {
		t.Fatalf("want an ICE, got %+v", ie)
	}
}

func TestDuplicateFactIsICE(t *testing.T) {
	ie := source.Guard("t", source.Span{}, func() { New(E0501, source.Span{}, "x").Text("a", "1").Text("a", "2") })
	if ie == nil {
		t.Fatal("want an ICE")
	}
}

// TestICEBecomesE0000: a recovered ICE is E0000 with where and what.
func TestICEBecomesE0000(t *testing.T) {
	ie := source.Guard("mk.main.F", source.Span{}, func() { source.ICEf(source.Span{}, "bad %d", 7) })
	d := FromInternal(ie)
	if d.Code != E0000 || d.Phase != PhaseInternal {
		t.Fatalf("%+v", d)
	}
	if w, _ := d.FactValue("where"); w.Str != "mk.main.F" {
		t.Errorf("where = %q", w.Str)
	}
	if w, _ := d.FactValue("what"); w.Str != "bad 7" {
		t.Errorf("what = %q", w.Str)
	}
}

func TestToolchainTruncatesStderr(t *testing.T) {
	long := strings.Repeat("é", StderrLimit)
	d := Toolchain("clang", "clang -c x.ll", long, "clang failed")
	f, _ := d.FactValue("stderr")
	if len(f.Str) > StderrLimit || !strings.HasPrefix(long, f.Str) || len(f.Str)%2 != 0 {
		t.Fatalf("stderr cut to %d bytes", len(f.Str))
	}
}

// TestJSONEscapesRFC8259: strings are escaped as RFC 8259 requires.
func TestJSONEscapesRFC8259(t *testing.T) {
	var w JSONWriter
	w.String("a\"b\\c\b\t\n\f\r\x01\x1f/<>& é")
	got := string(w.Finish())
	want := `"a\"b\\c\b\t\n\f\r\u0001\u001f/<>&` + " é" + `"`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// TestJSONNonfiniteFloatsAreStrings: NaN and infinities are written as
// strings, finite floats in plain decimal.
func TestJSONNonfiniteFloatsAreStrings(t *testing.T) {
	var w JSONWriter
	w.BeginArray()
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1.0, -0.0 * -1, math.Copysign(0, -1), 0.1, 1e21, 2.5e-7} {
		w.Float(f)
	}
	w.EndArray()
	got := string(w.Finish())
	want := `["nan","inf","-inf",1,0,-0,0.1,1000000000000000000000,0.00000025]`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

// TestJSONUnbalancedIsDetected: malformed writer use is an internal error.
func TestJSONUnbalancedIsDetected(t *testing.T) {
	cases := map[string]func(w *JSONWriter){
		"open object":    func(w *JSONWriter) { w.BeginObject(); w.Finish() },
		"dangling key":   func(w *JSONWriter) { w.BeginObject(); w.Key("a"); w.EndObject() },
		"value no key":   func(w *JSONWriter) { w.BeginObject(); w.Int(1) },
		"wrong end":      func(w *JSONWriter) { w.BeginArray(); w.EndObject() },
		"two top values": func(w *JSONWriter) { w.Int(1); w.Int(2) },
		"empty":          func(w *JSONWriter) { w.Finish() },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			var w JSONWriter
			if source.Guard(name, source.Span{}, func() { f(&w) }) == nil {
				t.Fatal("not detected")
			}
		})
	}
}

// fixture builds a source map with two files.
func fixture() (*source.SourceMap, *source.File, *source.File) {
	sm := source.NewSourceMap()
	a := sm.Add("src/lex.mk", []byte(strings.Repeat("// pad\n", 39)+"x := 1\n    pager.Next() + a\nzzz\n"))
	b := sm.Add("src/main.mk", []byte("one\ntwo\nthree\n"))
	return sm, a, b
}

func at(f *source.File, line, col, n uint32) source.Span {
	off, ok := f.Offset(source.LineCol{Line: line, Col: col})
	if !ok {
		panic("bad position")
	}
	return source.Span{File: f.ID, Start: off, End: off + n}
}

// e0501 is the E0501 example of doc/tools.md, with the refined schema.
func e0501(f *source.File) *Diagnostic {
	return New(E0501, at(f, 41, 5, 5), "pager is borrowed by a, which is used on line 43").
		Fact("loan", LoanFact("pager", LoanExclusive, at(f, 40, 1, 1))).
		Text("holder", "a").
		Loc("kept_live_by", at(f, 42, 1, 3)).
		Text("signature", "Pager.Next(mut self): ?str").
		Fix(Fix{Class: Proven, Edits: []Edit{{Span: source.Span{File: f.ID, Start: 3, End: 3}, Text: " from self.text"}},
			Why: "Pager is a view, so its result stays borrowed"}).
		Alt("Use a before calling Next again")
}

// TestJSONGoldenE0501 snapshots the schema byte for byte.
func TestJSONGoldenE0501(t *testing.T) {
	sm, a, _ := fixture()
	got := Order([]*Diagnostic{e0501(a)}, sm, NoCap).WriteJSON(sm)
	golden := filepath.Join("testdata", "e0501.golden")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// TestOrderByFileLineColCode: Order sorts by file, line, column, code, then
// message.
func TestOrderByFileLineColCode(t *testing.T) {
	sm, a, b := fixture()
	d1 := New(E0301, at(b, 2, 1, 1), "m")
	d2 := New(E0302, at(a, 41, 1, 1), "m")
	d3 := New(E0301, at(a, 41, 1, 1), "z")
	d4 := New(E0301, at(a, 41, 1, 1), "a")
	d5 := New(E0303, at(a, 40, 3, 1), "m")
	r := Order([]*Diagnostic{d1, d2, d3, d4, d5}, sm, NoCap)
	want := []*Diagnostic{d5, d4, d3, d2, d1}
	for i, e := range r.Diags {
		if e.Diag != want[i] {
			t.Fatalf("position %d: got %v %q", i, e.Diag.Code, e.Diag.Message)
		}
	}
}

// TestOrderIgnoresInputOrder: any permutation of the input gives the same bytes.
func TestOrderIgnoresInputOrder(t *testing.T) {
	sm, a, b := fixture()
	ds := []*Diagnostic{
		New(E0301, at(a, 41, 1, 2), "m"), New(E0301, at(a, 41, 1, 3), "m"),
		New(E0301, at(b, 1, 1, 1), "m").Text("k", "1"), New(E0301, at(b, 1, 1, 1), "m").Text("k", "2"),
		New(E0302, at(b, 3, 2, 1), "n").ConsequenceOf(E0301, at(b, 1, 1, 1)),
	}
	want := string(Order(ds, sm, NoCap).WriteJSON(sm))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 50; i++ {
		p := append([]*Diagnostic(nil), ds...)
		rng.Shuffle(len(p), func(i, j int) { p[i], p[j] = p[j], p[i] })
		if got := string(Order(p, sm, NoCap).WriteJSON(sm)); got != want {
			t.Fatalf("permutation changed the output:\n%s\n%s", got, want)
		}
	}
}

// TestDedupSameCodeSpanMessage: diagnostics equal in code, span and message
// are reported once.
func TestDedupSameCodeSpanMessage(t *testing.T) {
	sm, a, _ := fixture()
	s := at(a, 41, 1, 1)
	r := Order([]*Diagnostic{New(E0301, s, "m"), New(E0301, s, "m"), New(E0301, s, "other"), New(E0302, s, "m")}, sm, NoCap)
	if len(r.Diags) != 3 {
		t.Fatalf("kept %d", len(r.Diags))
	}
}

// TestRootResolution: a consequence points at its cause's position in the
// report; an orphan whose cause is absent is its own root.
func TestRootResolution(t *testing.T) {
	sm, a, b := fixture()
	cause := New(E0201, at(a, 41, 5, 5), "unknown name")
	cons := New(E0201, at(b, 3, 1, 2), "unknown name again").ConsequenceOf(E0201, at(a, 41, 5, 5))
	orphan := New(E0206, at(b, 1, 1, 1), "dup").ConsequenceOf(E0201, at(b, 2, 1, 1))
	r := Order([]*Diagnostic{cons, orphan, cause}, sm, NoCap)
	roots := []int{r.Diags[0].Root, r.Diags[1].Root, r.Diags[2].Root}
	// Order: a:41 cause (1), b:1 orphan (2), b:3 consequence (3).
	if roots[0] != 1 || roots[1] != 2 || roots[2] != 1 {
		t.Fatalf("roots %v", roots)
	}
}

// TestEarliestPhaseOnly: only diagnostics of the earliest phase are kept.
func TestEarliestPhaseOnly(t *testing.T) {
	sm, a, _ := fixture()
	r := Order([]*Diagnostic{New(E0501, at(a, 41, 1, 1), "f"), New(E0301, at(a, 42, 1, 1), "t"),
		New(E0201, at(a, 40, 1, 1), "n").In(PhaseTypes)}, sm, NoCap)
	if r.Phase != PhaseTypes || len(r.Diags) != 2 {
		t.Fatalf("phase %v, %d kept", r.Phase, len(r.Diags))
	}
	if r := Order(nil, sm, NoCap); r.HasPhase || len(r.WriteJSON(sm)) == 0 {
		t.Fatal("empty report")
	}
}

// TestEarliestPhaseKeepsCauses: a kept diagnostic's cause of a later phase
// is kept too, as its root, and so is that cause's own cause; a later-phase
// diagnostic nothing names is still left out, and so is the report's phase.
func TestEarliestPhaseKeepsCauses(t *testing.T) {
	sm, a, b := fixture()
	root := New(E0301, at(a, 40, 1, 1), "root")
	gen := New(E0903, at(a, 41, 1, 1), "generator failed").ConsequenceOf(E0301, at(a, 40, 1, 1))
	echo := New(E0201, at(b, 1, 1, 1), "unknown name").ConsequenceOf(E0903, at(a, 41, 1, 1))
	other := New(E0302, at(a, 42, 1, 1), "unrelated")
	r := Order([]*Diagnostic{echo, other, gen, root}, sm, NoCap)
	if r.Phase != PhaseNames || len(r.Diags) != 3 {
		t.Fatalf("phase %v, %d kept:\n%s", r.Phase, len(r.Diags), r.Text(sm))
	}
	for i, want := range []*Diagnostic{root, gen, echo} {
		if r.Diags[i].Diag != want {
			t.Fatalf("position %d: %v, want %v", i, r.Diags[i].Diag.Code, want.Code)
		}
	}
	if r.Diags[1].Root != 1 || r.Diags[2].Root != 2 {
		t.Fatalf("roots %d %d, want 1 2", r.Diags[1].Root, r.Diags[2].Root)
	}
}

// TestCapKeepsLaterPhaseCause: the cap cuts only the failing phase's
// diagnostics; a cause of a later phase that sorts after its consequences
// survives it, and so do the kept consequences' links to it.
func TestCapKeepsLaterPhaseCause(t *testing.T) {
	sm, a, b := fixture()
	cause := New(E0903, at(b, 1, 1, 1), "generator failed")
	ds := []*Diagnostic{cause}
	for line := uint32(40); line <= 42; line++ {
		ds = append(ds, New(E0201, at(a, line, 1, 1), "unknown name").ConsequenceOf(E0903, at(b, 1, 1, 1)))
	}
	r := Order(ds, sm, 2)
	if r.Phase != PhaseNames || len(r.Diags) != 3 || r.Omitted != 1 || r.Diags[2].Diag != cause {
		t.Fatalf("phase %v, %d kept, %d omitted:\n%s", r.Phase, len(r.Diags), r.Omitted, r.Text(sm))
	}
	if r.Diags[0].Root != 3 || r.Diags[1].Root != 3 || r.Diags[2].Root != 3 {
		t.Fatalf("roots %d %d %d, want 3 3 3", r.Diags[0].Root, r.Diags[1].Root, r.Diags[2].Root)
	}
}

// TestInternalAlwaysEmitted: E0000 survives the phase rule and the cap.
func TestInternalAlwaysEmitted(t *testing.T) {
	sm, a, _ := fixture()
	ice := FromInternal(&source.InternalError{Where: "w", What: "x"})
	ds := []*Diagnostic{New(E0101, at(a, 41, 1, 1), "p1"), New(E0101, at(a, 42, 1, 1), "p2"), New(E0501, at(a, 41, 1, 1), "f"), ice}
	r := Order(ds, sm, 1)
	if r.Phase != PhaseParse || len(r.Diags) != 2 || r.Omitted != 1 {
		t.Fatalf("phase %v kept %d omitted %d", r.Phase, len(r.Diags), r.Omitted)
	}
	if r.Diags[0].Diag != ice {
		t.Fatal("E0000 with no location sorts first")
	}
	only := Order([]*Diagnostic{ice}, sm, NoCap)
	if !only.HasPhase || only.Phase != PhaseInternal {
		t.Fatal("internal-only report phase")
	}
}

func TestTextRendering(t *testing.T) {
	sm, a, _ := fixture()
	got := Order([]*Diagnostic{e0501(a)}, sm, NoCap).Text(sm)
	for _, want := range []string{
		"src/lex.mk:41:5: E0501 exclusive-conflict: pager is borrowed by a",
		"    |     pager.Next() + a\n    |     ^^^^^\n",
		"loan: pager (exclusive) at src/lex.mk:40:1",
		"fix (proven): Pager is a view",
		"try: Use a before calling Next again",
		"explain: mk explain E0501",
		"1 error\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

// TestOrderDedupIgnoresArrival: two diagnostics equal on (code, span, message,
// facts) but differing in cause give the same report in either arrival order.
func TestOrderDedupIgnoresArrival(t *testing.T) {
	sm, a, _ := fixture()
	cause := New(E0501, at(a, 41, 5, 5), "first")
	d1 := New(E0505, at(a, 42, 1, 3), "same")
	d2 := New(E0505, at(a, 42, 1, 3), "same").ConsequenceOf(E0501, at(a, 41, 5, 5))
	d3 := New(E0505, at(a, 42, 1, 3), "same").Alt("try this")
	x := string(Order([]*Diagnostic{cause, d1, d2, d3}, sm, NoCap).WriteJSON(sm))
	for _, p := range [][]*Diagnostic{{cause, d2, d1, d3}, {d3, d2, cause, d1}, {d1, d3, d2, cause}} {
		if y := string(Order(p, sm, NoCap).WriteJSON(sm)); x != y {
			t.Fatalf("arrival order changed the report:\n%s\n%s", x, y)
		}
	}
}

// TestJSONInvalidUTF8IsReplaced: invalid UTF-8 in a tool's stderr becomes
// U+FFFD, since RFC 8259 JSON text must be UTF-8; valid
// multi-byte text passes through unchanged.
func TestJSONInvalidUTF8IsReplaced(t *testing.T) {
	sm := source.NewSourceMap()
	d := Toolchain("clang", "clang x.ll", "erreur: fichier \xe9 \xc3\xa9 \xf0\x9f\x98", "clang failed")
	out := Order([]*Diagnostic{d}, sm, NoCap).WriteJSON(sm)
	if !utf8.Valid(out) || !json.Valid(out) {
		t.Fatalf("not valid UTF-8 JSON: %q", out)
	}
	if !strings.Contains(string(out), "fichier � é �") {
		t.Fatalf("replacement wrong: %s", out)
	}
}
