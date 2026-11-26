// Why: a fuzz target's corpus directory and saved findings must be stable,
// unshared and valid file names, and one failure must be saved once.
// What: tests of fuzzcorpus.go: directory names, failure signatures, saves
// per failure, moved corpora and the directory map's cost.
// How: unit tests on fuzzDirOf and fuzzSignature, and mk test --fuzz runs
// on small packages that check the files left behind and the report text.

package driver

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFuzzDirOfNeverSharedNorDevice(t *testing.T) {
	tests := []testCase{
		{module: "main", name: "parse bytes"},
		{module: "main", name: "NUL"},
		{module: "main", name: "com1.txt"},
		{module: "main", name: "a:b"},
		{module: "main", name: "a?b"},
		{module: "main", name: "Lex"},
		{module: "main", name: "lex"},
		{module: "main", name: "dup"},
		{module: "main.sub", name: "dup"},
	}
	seen := map[string]testCase{}
	for _, tc := range tests {
		d := fuzzDirOf(tc, tests)
		key := strings.ToLower(d)
		if o, ok := seen[key]; ok {
			t.Errorf("%v and %v share %s", o, tc, d)
		}
		seen[key] = tc
		if windowsDevice(d) {
			t.Errorf("%v: %s is a Windows device name", tc, d)
		}
		if d != fuzzDirOf(tc, tests) {
			t.Errorf("%v: not deterministic", tc)
		}
	}
	if d := fuzzDirOf(tests[0], tests); d != "parse bytes" {
		t.Errorf("a plain name keeps its directory: %s", d)
	}
	if d := fuzzDirOf(tests[1], tests); !strings.HasPrefix(d, "_NUL~") {
		t.Errorf("NUL: %s", d)
	}
}

func TestFuzzSignature(t *testing.T) {
	a := fuzzRun{reason: "panic", output: "fuzz input x\npanic: index 7 out of range [0, 3) at main.mk:4:5\n"}
	b := fuzzRun{reason: "panic", output: "panic: index 9 out of range [0, 2) at main.mk:4:5\n"}
	c := fuzzRun{reason: "panic", output: "panic: magic at main.mk:9:9\n"}
	if fuzzSignature(a) != fuzzSignature(b) || fuzzSignature(a) == fuzzSignature(c) {
		t.Errorf("signatures %q %q %q", fuzzSignature(a), fuzzSignature(b), fuzzSignature(c))
	}
	// Two messages at one site, a helper that panics for its callers, are
	// two failures; a hex address is a number.
	one := fuzzRun{reason: "panic", output: "panic: bug one: an even first byte at main.mk:4:5\n"}
	two := fuzzRun{reason: "panic", output: "panic: bug two: an odd first byte at main.mk:4:5\n"}
	if fuzzSignature(one) == fuzzSignature(two) || !strings.Contains(fuzzSignature(two), "bug two") {
		t.Errorf("one site, two messages: %q %q", fuzzSignature(one), fuzzSignature(two))
	}
	h1 := fuzzRun{reason: "panic", output: "panic: bad pointer 0x7ff6a1b2 at main.mk:9:1\n"}
	h2 := fuzzRun{reason: "panic", output: "panic: bad pointer 0x1234 at main.mk:9:1\n"}
	if fuzzSignature(h1) != fuzzSignature(h2) {
		t.Errorf("hex numbers: %q %q", fuzzSignature(h1), fuzzSignature(h2))
	}
	if fuzzSignature(fuzzRun{reason: "hang (no call finished in 1s)"}) != "hang" {
		t.Error("a hang's signature")
	}
	d := fuzzRun{reason: "panic", output: "panic: stack limit 123 reached\n"}
	e := fuzzRun{reason: "panic", output: "panic: stack limit 456 reached\n"}
	if fuzzSignature(d) != fuzzSignature(e) {
		t.Errorf("site-less panics differing in numbers: %q %q", fuzzSignature(d), fuzzSignature(e))
	}
}

const fuzzNamesSource = `import std.testing

Two(b: []u8) {
    Assert(b.Len() < 2)
}

test "NUL" {
    testing.Fuzz(Two)
}
`

// TestMkTestFuzzOneSavePerFailureAndDeviceName: a test named NUL saves
// its finding (not "cannot find the path specified"), one failure
// found by many inputs is saved once (not up to 20 files), and a
// second run that finds it again saves nothing new but still fails.
// The fuzz time is far longer than the runs need: each ends at its 20th
// failing run, however slowly the machine starts a child (and a target
// that stopped failing fails the test in minutes, not at go test's limit).
func TestMkTestFuzzOneSavePerFailureAndDeviceName(t *testing.T) {
	root := testPkg(t, map[string]string{"main.mk": fuzzNamesSource})
	rep := mkTest(t, Config{Root: root}, TestOptions{Fuzz: "NUL", FuzzTime: 2 * time.Minute})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, "not saved again") ||
		!strings.Contains(rep.Tests[0].Output, ", 20 failing runs, 1 input saved\n") {
		t.Fatalf("--fuzz NUL: %+v", rep.Tests)
	}
	ents, err := os.ReadDir(filepath.Join(root, "testdata", "fuzz"))
	if err != nil || len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "_NUL~") {
		t.Fatalf("corpus directories %v (%v)", ents, err)
	}
	dir := filepath.Join(root, "testdata", "fuzz", ents[0].Name())
	if files := corpusFiles(dir); len(files) != 1 {
		t.Errorf("saved %d inputs for one failure: %q", len(files), files)
	}
	rep = mkTest(t, Config{Root: root}, TestOptions{Fuzz: "NUL", FuzzTime: 2 * time.Minute})
	if len(rep.Tests) != 1 || rep.Tests[0].Result != "fail" || !strings.Contains(rep.Tests[0].Output, ", 20 failing runs, 0 inputs saved\n") || len(corpusFiles(dir)) != 1 {
		t.Errorf("a second run: %+v, %d files", rep.Tests, len(corpusFiles(dir)))
	}
	// Plain mk test reads the suffixed directory.
	rep = mkTest(t, Config{Root: root}, TestOptions{})
	if got := results(rep); got != "main NUL panic" {
		t.Errorf("plain mk test: %s", got)
	}
}

// TestMkTestMovedCorpusNamedInText: when a second module adds a test of
// the same name, both move to suffixed directories, and the plain text
// report names the unread plain directory under each test even when it
// passes (not a PASS with no note).
func TestMkTestMovedCorpusNamedInText(t *testing.T) {
	root := testPkg(t, map[string]string{
		"pkg.mk":                "const Package = pkg.Package(\n    name: \"fz4\",\n)\n",
		"src/main.mk":           "import std.testing\n\nTwo(b: []u8) {\n    Assert(b.Len() < 2)\n}\n\ntest \"dup\" {\n    testing.Fuzz(Two)\n}\n",
		"src/sub/sub.mk":        "import std.testing\n\nNever(b: []u8) {\n    _ = b\n}\n\ntest \"dup\" {\n    testing.Fuzz(Never)\n}\n",
		"testdata/fuzz/dup/bad": "xx",
	})
	rep := mkTest(t, Config{Root: root}, TestOptions{})
	var b strings.Builder
	if err := rep.WriteText(&b, false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), "note: testdata/fuzz/dup/ is not read") != 2 {
		t.Errorf("the text report does not name the moved directory under both tests:\n%s", b.String())
	}
}

// TestFuzzDirMapLinear: the map gives each test what one scan per test
// gave, for many tests at once.
func TestFuzzDirMapLinear(t *testing.T) {
	var tests []testCase
	for i := 0; i < 10000; i++ {
		tests = append(tests, testCase{module: "main", name: "t" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strconv.Itoa(i)})
	}
	tests = append(tests, testCase{module: "main", name: "Dup"}, testCase{module: "m.sub", name: "dup"})
	start := time.Now()
	m := fuzzDirMap(tests)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("fuzzDirMap over %d tests took %s", len(tests), d)
	}
	if m[fuzzKey{"main", "Dup"}] == "Dup" || m[fuzzKey{"m.sub", "dup"}] == "dup" || m[fuzzKey{"main", "Dup"}] == m[fuzzKey{"m.sub", "dup"}] {
		t.Errorf("case twins: Dup %q dup %q", m[fuzzKey{"main", "Dup"}], m[fuzzKey{"m.sub", "dup"}])
	}
	if d := m[fuzzKey{tests[0].module, tests[0].name}]; d != tests[0].name {
		t.Errorf("a unique plain name: %q", d)
	}
	for _, tc := range tests[:50] {
		if fuzzDirOf(tc, tests) != m[fuzzKey{tc.module, tc.name}] {
			t.Errorf("%v: fuzzDirOf and fuzzDirMap differ", tc)
		}
	}
}
