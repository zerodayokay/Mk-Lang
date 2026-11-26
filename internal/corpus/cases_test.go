// Why: .cases files drive the program tests, so their parser must be exact.
// What: tests of cases.go: ParseCases on synthetic text (every key, its
// defaults and errors) and LoadCases over the real tests/programs.
// How: LoadCases must parse every .cases file, and every file it names must
// exist.

package corpus

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseCases(t *testing.T) {
	const p = "tests/programs/panics/copy.cases"
	text := "# comment\n\ncase ok\nargs copy.in \"out file.txt\" -v\nfiles copy.in sub/x.in\nstdout copy.in\n" +
		"case bad\nprofile release\ntarget x86_64-linux\nstdin ../wc.in\nstderr notfound.err\nexit 101\ntimeout 3\n"
	cs, err := ParseCases(p, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("%d cases, want 2", len(cs))
	}
	ok, bad := cs[0], cs[1]
	if ok.Program != "tests/programs/panics/copy.mk" || ok.Name != "ok" || ok.At != p+":3" {
		t.Errorf("ok: program %q name %q at %q", ok.Program, ok.Name, ok.At)
	}
	if !slices.Equal(ok.Args, []string{"copy.in", "out file.txt", "-v"}) {
		t.Errorf("args %q", ok.Args)
	}
	if !slices.Equal(ok.Files, []string{"tests/programs/panics/copy.in", "tests/programs/panics/sub/x.in"}) {
		t.Errorf("files %q", ok.Files)
	}
	if ok.Stdout != "tests/programs/panics/copy.in" || ok.Stdin != "" || ok.Stderr != "" || ok.Exit != 0 {
		t.Errorf("ok streams: %q %q %q exit %d", ok.Stdin, ok.Stdout, ok.Stderr, ok.Exit)
	}
	if DefaultCaseTimeout != 30*time.Second {
		t.Errorf("DefaultCaseTimeout = %v, want 30s", DefaultCaseTimeout)
	}
	if !slices.Equal(ok.Profiles, []string{"dev", "release"}) || !slices.Equal(ok.Targets, []string{"x86_64-windows", "x86_64-linux"}) || ok.Timeout != DefaultCaseTimeout {
		t.Errorf("ok defaults: %q %q %v", ok.Profiles, ok.Targets, ok.Timeout)
	}
	if !slices.Equal(bad.Profiles, []string{"release"}) || !slices.Equal(bad.Targets, []string{"x86_64-linux"}) ||
		bad.Stdin != "tests/programs/wc.in" || bad.Stderr != "tests/programs/panics/notfound.err" || bad.Exit != 101 || bad.Timeout != 3*time.Second {
		t.Errorf("bad: %+v", bad)
	}
	// Both orders of a two-value choice give the default order.
	cs, err = ParseCases(p, "case a\nprofile release dev\n")
	if err != nil || !slices.Equal(cs[0].Profiles, []string{"dev", "release"}) {
		t.Errorf("profile release dev: %v %v", cs, err)
	}
	for _, c := range []struct{ text, want string }{
		{"", "no case"},
		{"# only a comment\n", "no case"},
		{"args x\n", ":1: args before the first case line"},
		{"case\n", ":1: case \"\""},
		{"case a b\n", "want one name"},
		{"case a\ncase a\n", ":2: case a twice"},
		{"case a\nexit 1\nexit 2\n", ":3: exit twice"},
		{"case a\nprofile debug\n", "want one or more of dev release"},
		{"case a\nprofile dev dev\n", "dev twice"},
		{"case a\nprofile\n", "empty value"},
		{"case a\ntarget aarch64-linux\n", "want one or more of x86_64-windows x86_64-linux"},
		{"case a\nexit x\n", "invalid syntax"},
		{"case a\nexit 300\n", "want 0 to 255"},
		{"case a\ntimeout 0\n", "positive"},
		{"case a\nargs \"open\n", "unterminated quote"},
		{"case a\nargs \"a\"b\n", "text after a closing quote"},
		{"case a\nargs a\"b\n", "a quote inside"},
		{"case a\nstdout ../../../x\n", "leaves tests/programs"},
		{"case a\nstdout /abs\n", "relative path"},
		{"case a\nstdout\n", "relative path"},
		{"case a\nstdotu x\n", "unknown key \"stdotu\""},
	} {
		if _, err := ParseCases(p, c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want one containing %q", c.text, err, c.want)
		}
	}
}

func TestLoadCases(t *testing.T) {
	cs, err := LoadCases(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var fizz *ProgramCase
	files := map[string]bool{}
	for i, c := range cs {
		files[c.Program] = true
		if c.Program == "tests/programs/fizz.mk" && c.Name == "ok" {
			fizz = &cs[i]
		}
	}
	if fizz == nil {
		t.Fatal("no fizz.mk case ok")
	}
	if fizz.Stdout != "tests/programs/fizz.out" || fizz.Exit != 0 || len(fizz.Profiles) != 2 || len(fizz.Targets) != 2 {
		t.Errorf("fizz ok: %+v", *fizz)
	}
	t.Logf("%d cases over %d programs", len(cs), len(files))
}
