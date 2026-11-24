// Why: the driver's report is what every command prints: its exit status,
// caps, paths and fixes must follow the catalogue and never drift.
// What: tests of exit status, the text cap, path spelling, passed-through
// facts, fix classes, and the codes the driver itself may construct.
// How: small packages checked in-process, a fix run over doc/'s blocks, and
// a scan of the driver's own Go source for diag codes.

package driver

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/source"
)

func TestExitStatus(t *testing.T) {
	sm := source.NewSourceMap()
	f := sm.Add("a.mk", []byte("x\n"))
	at := source.Span{File: f.ID, Start: 0, End: 1}
	user := diag.New(diag.E0201, at, "unknown name")
	tool := diag.Toolchain("clang", "clang -c a.ll", "boom", "clang failed")
	ice := diag.New(diag.E0000, at, "internal").Text("where", "w").Text("what", "x")
	for _, tc := range []struct {
		name string
		ds   []*diag.Diagnostic
		want int
	}{
		{"clean", nil, 0},
		{"user", []*diag.Diagnostic{user}, 1},
		{"toolchain only", []*diag.Diagnostic{tool}, 2},
		{"user and toolchain", []*diag.Diagnostic{user, tool}, 1},
		{"internal", []*diag.Diagnostic{user, ice}, 3},
		{"internal and toolchain", []*diag.Diagnostic{tool, ice}, 3},
	} {
		if got := ExitStatus(diag.Order(tc.ds, sm, diag.NoCap)); got != tc.want {
			t.Errorf("%s: exit status %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestTextCapAndCount(t *testing.T) {
	var src strings.Builder
	src.WriteString("export Main(sys: &int) {\n")
	for i := 0; i < 60; i++ {
		src.WriteString("    Missing()\n")
	}
	src.WriteString("}\n")
	file := filepath.Join(writeTree(t, map[string]string{"a.mk": src.String()}), "a.mk")
	r := check(t, Config{Root: file, MaxDiags: DefaultTextCap})
	var text bytes.Buffer
	if err := r.WriteReport(&text, false); err != nil {
		t.Fatal(err)
	}
	if len(r.Report.Diags) != DefaultTextCap || r.Report.Omitted != 10 {
		t.Errorf("kept %d omitted %d, want %d and 10", len(r.Report.Diags), r.Report.Omitted, DefaultTextCap)
	}
	if !strings.HasSuffix(text.String(), "50 errors (10 omitted)\n") {
		t.Errorf("text ends %q", text.String()[max(0, text.Len()-60):])
	}
	r = check(t, Config{Root: file})
	if len(r.Report.Diags) != 60 {
		t.Errorf("no cap: kept %d, want 60", len(r.Report.Diags))
	}
	var js bytes.Buffer
	if err := r.WriteReport(&js, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(js.Bytes(), []byte(`{"version":1,"phase":"names","diagnostics":[`)) || !bytes.HasSuffix(js.Bytes(), []byte("\"omitted\":0}\n")) {
		t.Errorf("json %q", js.String()[:min(80, js.Len())])
	}
	// A clean report prints nothing as text.
	var none bytes.Buffer
	_ = check(t, Config{Root: filepath.Join(writeTree(t, map[string]string{"b.mk": "G() {\n}\n"}), "b.mk")}).WriteReport(&none, false)
	if none.Len() != 0 {
		t.Errorf("clean text %q", none.String())
	}
}

func TestStdPathsAndGivenPaths(t *testing.T) {
	stdFiles := fakeStdFiles()
	stdFiles["fs/fs.mk"] = "export ReadText(): int {\n    return Nope()\n}\n"
	stdDir := writeTree(t, stdFiles)
	dir := writeTree(t, map[string]string{"sub/one.mk": "G() {\n}\n"})
	r := check(t, Config{Root: filepath.Join(dir, "sub", "one.mk"), StdDir: stdDir})
	got := lines(r)
	if len(got) != 1 || got[0] != "std/fs/fs.mk:2:12:E0201" {
		t.Errorf("std path: got %q", got)
	}
	// A command-line file is cited as given, with '/'.
	wd, _ := os.Getwd()
	t.Chdir(dir)
	defer t.Chdir(wd)
	r = check(t, Config{Root: "sub" + string(filepath.Separator) + "one.mk", StdDir: stdDir})
	if !slices.Contains(r.Trace.FilesParsed, "sub/one.mk") {
		t.Errorf("given path: parsed %q", r.Trace.FilesParsed)
	}
}

func TestSignatureFactPassedThrough(t *testing.T) {
	// A stub emitter's E0304 with a signature fact reaches the JSON with the
	// same text, in the emitter's key order: the driver never rebuilds facts.
	sm := source.NewSourceMap()
	f := sm.Add("a.mk", []byte("F(1)\n"))
	d := diag.New(diag.E0304, source.Span{File: f.ID, Start: 0, End: 4}, "wrong argument").
		Text("signature", "F(x: &str): int").Text("arg", "x")
	r := Result{Report: diag.Order([]*diag.Diagnostic{d}, sm, diag.NoCap), Sources: sm}
	var js bytes.Buffer
	if err := r.WriteReport(&js, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"facts":{"signature":"F(x: &str): int","arg":"x"}`) {
		t.Errorf("json %s", js.String())
	}
}

// fixRun is every diagnostic of doc/'s ok and reject blocks
// and of tests/programs, checked through resolve against the stand-in std.
func fixRun(t *testing.T) []*diag.Diagnostic {
	t.Helper()
	c, err := NewCorpusSession(Config{StdDir: fakeStd(t)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Through() != Implemented() {
		t.Fatalf("Through %v with the stand-in std", c.Through())
	}
	blocks := designBlocks(t)
	prelude := referencePrelude(t, blocks)
	var out []*diag.Diagnostic
	for _, b := range blocks {
		if b.verdict == "parse" {
			continue
		}
		o := c.CheckUnit(UnitInput{Name: b.name, Prelude: "prelude.mk",
			Files: []UnitFile{{"block.mk", b.text}, {"prelude.mk", prelude}}})
		for _, e := range o.Report.Diags {
			out = append(out, e.Diag)
		}
	}
	progs, _ := filepath.Glob("../../tests/programs/*.mk")
	for _, p := range progs {
		r := check(t, Config{Root: p, StdDir: fakeStd(t)})
		for _, e := range r.Report.Diags {
			out = append(out, e.Diag)
		}
	}
	return out
}

func TestFixClassStrings(t *testing.T) {
	n := 0
	for _, d := range fixRun(t) {
		for _, f := range d.Fixes {
			n++
			if c := f.Class.String(); c != "proven" && c != "compiles" && c != "alternatives" {
				t.Errorf("%v: fix class %q", d.Code, c)
			}
		}
	}
	t.Logf("%d fixes checked", n)
}

// knownClass are codes whose emitter attaches a proven fix where the
// catalogue does not say proven, each with why. The driver relabels
// nothing: the test logs these, and an entry no fix of the run matches any
// more is an error. Empty: E0209's catalogue row says "Proven: `_ =`".
var knownClass = map[diag.Code]string{}

func TestProvenOnlyWhereCatalogueSaysProven(t *testing.T) {
	n := 0
	seen := map[diag.Code]bool{}
	defer func() {
		for c, why := range knownClass {
			if !seen[c] {
				t.Errorf("%v no longer carries a proven fix: remove it from knownClass (%s)", c, why)
			}
		}
	}()
	for _, d := range fixRun(t) {
		e, _ := d.Code.Entry()
		for _, f := range d.Fixes {
			n++
			if e.FixClass == "None" {
				t.Errorf("%v carries a fix, but the catalogue's fix class is None", d.Code)
			}
			if f.Class == diag.Proven && !strings.Contains(strings.ToLower(e.FixClass), "proven") {
				if why, ok := knownClass[d.Code]; ok {
					seen[d.Code] = true
					t.Logf("%v: known, %s", d.Code, why)
					continue
				}
				t.Errorf("%v: a proven fix, but the catalogue says %q", d.Code, e.FixClass)
			}
		}
	}
	if n == 0 {
		t.Log("vacuous: no emitter attached a fix in this run")
	}
}

func TestDriverEmitsOnlyItsCodes(t *testing.T) {
	// E0904: the gen/ view (genview.go).
	// E0213: cross-target pkg.Select API parity (alltargets.go).
	// E0214 to E0216: dependency rules (deps_internal.go, deps_grants.go,
	// manifest_min.go).
	allowed := []string{"E0000", "E0001", "E0101", "E0202", "E0205", "E0206", "E0213", "E0214", "E0215", "E0216", "E0904", "E1102"}
	files, _ := filepath.Glob("*.go")
	// A comparison (`d.Code == diag.E0305`: a fix attached to another
	// package's diagnostic) constructs nothing, and neither does an applied
	// fix naming the code it answered (`AppliedFix{Code: diag.E0102}`: mk
	// fix lists its rounds under their trigger codes).
	re := regexp.MustCompile(`(?:[=!]= |AppliedFix\{Code: )?diag\.(E\d{4})`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if pre := m[0][:len(m[0])-len(m[1])]; strings.HasSuffix(pre, "== diag.") || strings.HasSuffix(pre, "!= diag.") || strings.HasPrefix(pre, "AppliedFix{Code: ") {
				continue
			}
			if !slices.Contains(allowed, m[1]) {
				t.Errorf("%s constructs %s, which the driver's allowed list does not hold", f, m[1])
			}
		}
	}
}

// designBlock is one fenced mk block of doc/**/*.md.
type designBlock struct {
	name, verdict, text string
}

// designBlocks extracts every fenced mk block (a test-only reader; the
// corpus extractor is internal/corpus's).
func designBlocks(t testing.TB) []designBlock {
	t.Helper()
	dir := filepath.Join("..", "..", "doc")
	paths, _ := markdownFiles(dir)
	var out []designBlock
	for _, p := range paths {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		ls := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		for i := 0; i < len(ls); i++ {
			if ls[i] != "```mk" {
				continue
			}
			start := i + 2
			var b strings.Builder
			for i++; i < len(ls) && ls[i] != "```"; i++ {
				b.WriteString(ls[i] + "\n")
			}
			text := b.String()
			first, _, _ := strings.Cut(text, "\n")
			v, _ := strings.CutPrefix(strings.TrimSpace(first), "// verdict:")
			v = strings.Fields(v + " x")[0]
			out = append(out, designBlock{"doc/" + p + "#" + itoa(start), v, text})
		}
	}
	return out
}

// referencePrelude is the reference's test prelude block.
func referencePrelude(t testing.TB, blocks []designBlock) string {
	for _, b := range blocks {
		if strings.HasPrefix(b.name, "doc/reference.md#") && strings.Contains(b.text, "linear struct Res") {
			return b.text
		}
	}
	t.Fatal("no test prelude block in doc/reference.md")
	return ""
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

// markdownFiles lists the Markdown files under dir, doc/std's pages included, as
// paths relative to dir with '/', in byte order: "reference.md",
// "std/fs.md".
func markdownFiles(dir string) ([]string, error) {
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
