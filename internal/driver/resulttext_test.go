// Why: a function whose result is an error union held as a value (an alias
// of one, or a result type parameter bound to one) cannot fail, and one
// written E!T can; mk query sig, hover and mk apidiff print signatures, so
// the two must not print alike.
// What: TestQuerySigValueResult and TestAPIDiffValueResult.
// How: query sig on Val (an alias result), Fal (a written E!T) and Hold (a
// fn type with an alias result) in a temporary package; apidiff of a
// function whose alias result becomes a written E!T.

package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valueResultSrc = `error Bad

type R = Bad!int

export Val(x: &R): R {
    return *x
}

export Fal(x: &R): Bad!int {
    return try *x
}

export Hold(f: fn(&R): R): int {
    return 0
}
`

func TestQuerySigValueResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := "const Package = pkg.Package(\n    name: \"q\",\n    mk: \"0.6\",\n)\n"
	if err := os.WriteFile(filepath.Join(dir, "pkg.mk"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.mk"), []byte(valueResultSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(Config{Root: dir, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, want string }{
		{"Val", "export Val(x: &(Bad!int)): Bad!int (cannot fail)"},
		{"Fal", "export Fal(x: &(Bad!int)): Bad!int"},
		{"Hold", "export Hold(f: fn(&(Bad!int)): Bad!int (cannot fail)): int"},
	} {
		a, _, err := s.Query("sig", []string{c.name})
		if err != nil || a == nil || !a.Found || len(a.Answer) == 0 {
			t.Fatalf("query sig %s: %v %+v", c.name, err, a)
		}
		if a.Answer[0] != c.want {
			t.Errorf("query sig %s: %q, want %q", c.name, a.Answer[0], c.want)
		}
	}
}

func TestAPIDiffValueResult(t *testing.T) {
	v1 := "error Bad\n\ntype R = Bad!int\n\nexport Get(x: &R): R {\n    return *x\n}\n"
	v2 := strings.Replace(v1, "export Get(x: &R): R {\n    return *x", "export Get(x: &R): Bad!int {\n    return try *x", 1)
	res := apiDiffFiles(t, v1, v2)
	if !strings.Contains(res.Text(), "main.Get  signature") {
		t.Errorf("no signature change for a result that can now fail:\n%s", res.Text())
	}
}
