// Why: std must open as real files from the editor.
// What: tests of definition into std, the diagnostic's codeDescription and the
// std hover.
// How: definitions must land in the read-only std copy under the cache.

package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mk/internal/diag"
	"mk/internal/driver"
	"mk/internal/source"
)

const stdFixture = `import std.fs

Count(s: str): int {
    return s.Len()
}

Size(xs: &List[int]): int {
    return xs.Len()
}

export Main(sys: &System) {
    _ = fs.ReadText(sys.arena, sys.io, "x")
    _ = Count("abc")
}
`

// TestLSPDefinitionIntoStd: a std method, a std type's method through a
// parameter's type, and a std module's function resolve to declarations in
// the read-only copy of std under the cache directory.
func TestLSPDefinitionIntoStd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.mk")
	must(t, writeFile(path, stdFixture))
	uri := pathURI(path)
	c := start(t)
	c.init(true)
	c.open(uri, stdFixture)
	c.diagnostics(uri)
	stdRoot := filepath.Join(testCache, "lsp")
	for _, tc := range []struct {
		at, file, decl string
		off            int
	}{
		{"s.Len()", "core/core_str.mk", "str.Len(self)", 2},
		{"xs.Len()", "core/coll_list.mk", "List[T].Len(", 3},
		{"fs.ReadText", "fs/fs.mk", "ReadText(a:", 3},
	} {
		line, col := lineOf(t, stdFixture, tc.at)
		var locs []Location
		c.result(c.call("textDocument/definition", at(uri, line, col+tc.off)), &locs)
		if len(locs) == 0 {
			t.Errorf("definition at %q: none", tc.at)
			continue
		}
		p, ok := uriPath(locs[0].URI)
		if !ok || !strings.HasPrefix(pathKey(p), pathKey(stdRoot)) || !strings.HasSuffix(filepath.ToSlash(p), tc.file) {
			t.Errorf("definition at %q: %s, want %s under %s", tc.at, locs[0].URI, tc.file, stdRoot)
			continue
		}
		raw, err := os.ReadFile(p)
		must(t, err)
		l := newLines(string(raw))
		got := l.line(locs[0].Range.Start.Line)
		if !strings.Contains(got, tc.decl) {
			t.Errorf("definition at %q: line %q, want it to declare %q", tc.at, got, tc.decl)
		}
		fi, err := os.Stat(p)
		must(t, err)
		if fi.Mode().Perm()&0o222 != 0 {
			t.Errorf("%s is writable (%v), want read-only", p, fi.Mode())
		}
	}
	c.shutdown()
}

func TestLSPDefinitionSameNamedStdMethods(t *testing.T) {
	const text = `import std.http.fields

CoreField(core_field: &Field): str {
    return core_field.Name()
}

WireField(wire_field: &fields.Field): []u8 {
    return wire_field.Name()
}
`
	std, err := filepath.Abs(filepath.Join("..", "..", "std"))
	must(t, err)
	path := filepath.Join(t.TempDir(), "main.mk")
	must(t, writeFile(path, text))
	uri := pathURI(path)
	c := startWith(t, Options{StdDir: std, CacheDir: testCache, Jobs: 2})
	c.init(true)
	c.open(uri, text)
	c.diagnostics(uri)
	for receiver, want := range map[string]string{
		"core_field": "core/core_gen.mk",
		"wire_field": "http/fields/sink.mk",
	} {
		line, col := lineOf(t, text, receiver+".Name()")
		var locs []Location
		c.result(c.call("textDocument/definition", at(uri, line, col+len(receiver)+1)), &locs)
		if len(locs) == 0 {
			t.Errorf("definition of %s.Name: none, want %s", receiver, want)
			continue
		}
		p, ok := uriPath(locs[0].URI)
		if !ok || !strings.HasSuffix(filepath.ToSlash(p), "/"+want) {
			t.Errorf("definition of %s.Name: %s, want %s", receiver, locs[0].URI, want)
		}
	}
	c.shutdown()
}

// TestLSPDiagnosticCodeDescription: a published diagnostic links its code
// to a file holding `mk explain`'s text.
func TestLSPDiagnosticCodeDescription(t *testing.T) {
	_, mainURI, _ := writeFixture(t)
	c := start(t)
	c.init(true)
	bad := strings.Replace(fixtureMain, "return self.hi - self.lo", "return self.hi - nope", 1)
	c.open(mainURI, bad)
	p := c.diagnostics(mainURI)
	if len(p.Diagnostics) != 1 || p.Diagnostics[0].CodeDescription == nil {
		t.Fatalf("diagnostics %+v, want one with a codeDescription", p.Diagnostics)
	}
	path, ok := uriPath(p.Diagnostics[0].CodeDescription.Href)
	if !ok {
		t.Fatalf("href %q is not a file URI", p.Diagnostics[0].CodeDescription.Href)
	}
	raw, err := os.ReadFile(path)
	must(t, err)
	entry, _ := driver.Explain("E0201")
	if string(raw) != entry.Text() {
		t.Errorf("%s holds %q, want mk explain E0201's text", path, raw)
	}
	c.shutdown()
}

// TestMapReportTags: an unused local or import (E0208) is tagged
// Unnecessary, so editors show it faded; other codes carry no tag.
func TestMapReportTags(t *testing.T) {
	dir := t.TempDir()
	s := newServer(nil, Options{CacheDir: testCache})
	r := s.rootFor("pkg:" + dir)
	sm := source.NewSourceMap()
	f := sm.Add("main.mk", []byte("Main() {\n    x := 1\n}\n"))
	res := driver.Result{Sources: sm, Report: diag.Report{Diags: []diag.Emitted{
		{Diag: &diag.Diagnostic{Code: diag.E0208, Phase: diag.PhaseTypes, At: source.NewSpan(f.ID, 13, 14), Message: "unused local x"}},
		{Diag: &diag.Diagnostic{Code: diag.E0201, Phase: diag.PhaseTypes, At: source.NewSpan(f.ID, 18, 19), Message: "unknown name"}},
	}}}
	got := map[string][]int{}
	s.mapReport(r, res, func(_ string, d lspDiagnostic, _ []codeAction) { got[d.Code] = d.Tags })
	if len(got["E0208"]) != 1 || got["E0208"][0] != tagUnnecessary || len(got["E0201"]) != 0 {
		t.Errorf("tags %v, want E0208 Unnecessary only", got)
	}
}

// TestLSPHoverStdSurface: a hover on a std item shows, below `mk query
// sig`, its reference library line and the section's sentences on it.
func TestLSPHoverStdSurface(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.mk")
	must(t, writeFile(path, stdFixture))
	uri := pathURI(path)
	c := start(t)
	c.init(true)
	c.open(uri, stdFixture)
	c.diagnostics(uri)
	for _, tc := range []struct{ at, want string }{
		{"s.Len()", "str.Len(self): int"},
		{"fs.ReadText", "fs.ReadText(a: &Arena, io: Io, p: str)"},
	} {
		line, col := lineOf(t, stdFixture, tc.at)
		var h hover
		c.result(c.call("textDocument/hover", at(uri, line, col+3)), &h)
		v := h.Contents.Value
		i := strings.Index(v, surfaceSection)
		if i < 0 || !strings.Contains(v[i:], tc.want) || !strings.HasPrefix(v, "```mk\n") {
			t.Errorf("hover at %q:\n%s\nwant the sig, then %q under %q", tc.at, v, tc.want, surfaceSection)
		}
	}
	c.shutdown()
}
