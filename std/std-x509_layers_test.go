// Why: a Stable std module must not depend on the Experimental std.x509,
// std.tls or std.quic.
// What: TestX509_LayerNoStableImportsExperimental and
// TestX509_LayerCheckCatches.
// How: reads the import lines of every std/**/*.mk file and the class table of
// internal/driver/classes.go as text, so package std imports nothing of the
// compiler.

package std

import (
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// x509Guarded are the modules no Stable module may import.
var x509Guarded = []string{"std.x509", "std.tls", "std.quic"}

// x509ClassRow matches one Experimental row of internal/driver's
// moduleClasses table.
var x509ClassRow = regexp.MustCompile(`\{"(std(?:\.\w+)+)",\s*ClassExperimental\b`)

// x509ImportLine matches an import item (04b-grammar: "import" [ Name "=" ]
// Path), group 1 the path.
var x509ImportLine = regexp.MustCompile(`^import\s+(?:\w+\s*=\s*)?([\w.]+)\s*$`)

// x509Experimental reads the Experimental modules from internal/driver's
// classes table.
func x509Experimental(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../internal/driver/classes.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range x509ClassRow.FindAllStringSubmatch(string(b), -1) {
		out = append(out, m[1])
	}
	return out
}

// x509ModuleOf is the module a std file belongs to: "std." and its
// directory, the per-target directories pkg.mk maps to std.sys; "" for a
// file that belongs to no module (corpus/, testdata, std's root).
func x509ModuleOf(p string) string {
	dir := path.Dir(p)
	if dir == "." || strings.HasPrefix(dir+"/", "corpus/") || slices.Contains(strings.Split(dir, "/"), "testdata") {
		return ""
	}
	switch dir {
	case "sys/windows", "sys/linux", "sys/nutshell", "free/sys":
		return "std.sys"
	}
	return "std." + strings.ReplaceAll(dir, "/", ".")
}

// x509Under reports whether module m is mod or a module under it.
func x509Under(m, mod string) bool {
	return m == mod || strings.HasPrefix(m, mod+".")
}

// x509LayerViolations is every import of a guarded module by a module that
// is not Experimental, as "file:line: importer imports module".
func x509LayerViolations(files []mkFile, experimental []string) []string {
	isExp := func(m string) bool {
		return slices.ContainsFunc(experimental, func(e string) bool { return x509Under(m, e) })
	}
	var bad []string
	for _, f := range files {
		mod := x509ModuleOf(f.path)
		if mod == "" || isExp(mod) {
			continue
		}
		for i, line := range strings.Split(f.text, "\n") {
			m := x509ImportLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			for _, g := range x509Guarded {
				if x509Under(m[1], g) {
					bad = append(bad, f.path+":"+strconv.Itoa(i+1)+": "+mod+" imports "+m[1])
				}
			}
		}
	}
	return bad
}

// x509DiskFiles reads every .mk file under std/ on disk (deeper than the
// embedded FS's patterns too), CRLF normalized.
func x509DiskFiles(t *testing.T) []mkFile {
	t.Helper()
	var out []mkFile
	err := fs.WalkDir(os.DirFS("."), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mk") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, mkFile{p, strings.ReplaceAll(string(b), "\r\n", "\n")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestX509_LayerNoStableImportsExperimental is the layer check over
// today's std.
func TestX509_LayerNoStableImportsExperimental(t *testing.T) {
	exp := x509Experimental(t)
	for _, g := range x509Guarded {
		if !slices.Contains(exp, g) {
			t.Errorf("internal/driver/classes.go does not mark %s Experimental (found %v)", g, exp)
		}
	}
	files := x509DiskFiles(t)
	if len(files) < 100 {
		t.Fatalf("read %d std files; the walk is broken", len(files))
	}
	for _, v := range x509LayerViolations(files, exp) {
		t.Errorf("a Stable module imports an Experimental one: %s", v)
	}
}

// TestX509_LayerCheckCatches proves the check can fail: fabricated files of
// Stable modules that import the guarded modules (in each import form) are
// reported, and the same imports from Experimental modules, from a module
// under one, from corpus/ and from testdata are not.
func TestX509_LayerCheckCatches(t *testing.T) {
	exp := []string{"std.x509", "std.tls"}
	files := []mkFile{
		{"http/client.mk", "import std.core\nimport std.tls\n"},
		{"net/x.mk", "// import std.x509\nimport  std.x509  \n"},
		{"sys/windows/a.mk", "import t = std.tls.record\n"},
		{"fs/b.mk", "import std.x509x\nimport std.tlsx\n"},
		{"tls/conn.mk", "import std.x509\n"},
		{"tls/hs/msg.mk", "import std.x509\n"},
		{"x509/verify.mk", "import std.tls\n"},
		{"corpus/prelude.mk", "import std.tls\n"},
		{"net/testdata/case.mk", "import std.tls\n"},
	}
	got := x509LayerViolations(files, exp)
	want := []string{
		"http/client.mk:2: std.http imports std.tls",
		"net/x.mk:2: std.net imports std.x509",
		"sys/windows/a.mk:1: std.sys imports std.tls.record",
	}
	if !slices.Equal(got, want) {
		t.Errorf("violations\n got  %q\n want %q", got, want)
	}
}
