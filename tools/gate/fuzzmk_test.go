// Why: the Mk fuzz scan and the apidiff step decide what the gate runs and
// fails on, from text and JSON.
// What: tests of mkFuzzTargets (on files and on the tree), fuzzArgv,
// apidiffBreaking, namesItem, apidiffVerdict and extractStd.
// How: writes small trees and a scratch git repository under t.TempDir().

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeGateFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, text := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGateMkFuzzTargets(t *testing.T) {
	root := t.TempDir()
	writeGateFiles(t, root, map[string]string{
		"pkgs/parser/pkg.mk":                   "pkg.Package()\n",
		"pkgs/parser/src/lex.mk":               "import std.testing\n\ntest \"lex { }\" {\n    // }\n    testing.Fuzz(Lex)\n}\n\ntest \"plain\" {\n    Assert(true)\n}\n\ntest \"bench\" {\n    testing.Bench(B)\n}\n",
		"tools/one/main.mk":                    "test \"one\" { testing.Fuzz(F) }\n",
		"tools/one/testdata/x.mk":              "test \"hidden\" { testing.Fuzz(F) }\n",
		"tools/none/main.mk":                   "test \"none\" { Assert(true) }\n",
		"tools/cmt/main.mk":                    "test \"mention\" {\n    // testing.Fuzz(F) is only named here\n    x := \"testing.Fuzz(F)\"\n}\n// test \"ghost\" { testing.Fuzz(F) }\n",
		"tests/conformance/rules/D1_accept.mk": "test \"unit\" { testing.Fuzz(F) }\n",
		".hidden/main.mk":                      "test \"dot\" { testing.Fuzz(F) }\n",
	})
	got, err := mkFuzzTargets(root, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	want := []fuzzTarget{{"./pkgs/parser", "lex { }", true}, {"./tools/one", "one", true}}
	if !slices.Equal(got, want) {
		t.Errorf("targets %v, want %v", got, want)
	}
	g := &gate{root: root, goExe: "go", fuzzTime: 5 * time.Minute}
	if argv := strings.Join(g.mkFuzzArgv(want[0]), " "); argv != "go run ./cmd/mk test --fuzz lex { } --fuzztime 5m0s ./pkgs/parser" {
		t.Errorf("argv %s", argv)
	}
	aff := mkAffected([]string{"./pkgs/parser", "./tools/one"}, []string{"pkgs/parser/src/lex.mk", "internal/x.go"}, false)
	if !aff["./pkgs/parser"] || aff["./tools/one"] {
		t.Errorf("affected %v", aff)
	}
	if aff = mkAffected([]string{"./tools/one"}, nil, true); !aff["./tools/one"] {
		t.Error("a ./cmd/mk change affects every Mk target")
	}
	d1, err := mkDirDigest(filepath.Join(root, "pkgs", "parser"))
	if err != nil {
		t.Fatal(err)
	}
	writeGateFiles(t, root, map[string]string{"pkgs/parser/testdata/fuzz/lex { }/seed": "x"})
	if d2, _ := mkDirDigest(filepath.Join(root, "pkgs", "parser")); d2 == d1 {
		t.Error("a new corpus file leaves the digest unchanged")
	}
}

// TestGateFuzzArgv: a go fuzz target's command, with -parallel when the
// pool sets it and -fuzzminimizetime for the targets of fuzzMinimizeTime,
// each of which is a listed untrusted-input target.
func TestGateFuzzArgv(t *testing.T) {
	g := &gate{goExe: "go", fuzzTime: 5 * time.Minute}
	plain := strings.Join(g.fuzzArgv(fuzzTarget{"./std", "FuzzStdUrl_Parse", false}, 0), " ")
	if plain != "go test -run=^$ -fuzz=^FuzzStdUrl_Parse$ -fuzztime=5m0s -timeout=35m0s ./std" {
		t.Errorf("argv %s", plain)
	}
	capped := strings.Join(g.fuzzArgv(fuzzTarget{"./std", "FuzzStdHttpFields_Huffman", false}, 4), " ")
	if capped != "go test -run=^$ -fuzz=^FuzzStdHttpFields_Huffman$ -parallel=4 -fuzzminimizetime=100x -fuzztime=5m0s -timeout=35m0s ./std" {
		t.Errorf("argv %s", capped)
	}
	listed := map[string]bool{}
	for _, e := range untrusted {
		for _, n := range e.targets {
			listed[untrustedPkgOf(n)+" "+n] = true
		}
	}
	for k := range fuzzMinimizeTime {
		if !listed[k] {
			t.Errorf("fuzzMinimizeTime names %q, which is no listed untrusted-input target", k)
		}
	}
}

func TestGateApidiffBreakingList(t *testing.T) {
	// The first release has no earlier std, so nothing is announced as
	// breaking.
	if apidiffBase == "" && len(apidiffBreaking) != 0 {
		t.Errorf("no earlier release, yet the breaking list names %q", apidiffBreaking)
	}
	if !namesItem([]string{"Method.Flows"}, "std.core.Method.Flows") || namesItem([]string{"Method.Flows"}, "std.fs.Open") {
		t.Error("namesItem")
	}
	// Qualified names only: a trailing name must not list every item that
	// ends in it.
	if namesItem([]string{"Bytes"}, "std.mem.Buf.Bytes") || !namesItem([]string{"StackSize.Bytes"}, "std.core.StackSize.Bytes") ||
		!namesItem([]string{"std.net.ConnSlot.Fill"}, "std.net.ConnSlot.Fill") || namesItem([]string{"std.net.ConnSlot"}, "std.net.ConnSlot.Fill") {
		t.Error("namesItem matches by more than the qualified name")
	}
}

func TestGateApidiffVerdict(t *testing.T) {
	listed := []string{"Method.Flows"}
	doc := []byte(`{"changes":[{"severity":"breaking","key":"std.core.Method.Flows","what":"signature","old":"a","new":"b"},` +
		`{"severity":"breaking","key":"std.fs.Open","what":"removed","old":"x","new":""},` +
		`{"severity":"compatible","key":"std.fs.New","what":"added","old":"","new":"y"}]}`)
	un, notes, err := apidiffVerdict(doc, 1, listed)
	if err != nil || len(un) != 1 || !strings.Contains(un[0], "std.fs.Open removed") || len(notes) != 3 {
		t.Errorf("verdict %q %q %v", un, notes, err)
	}
	if _, _, err := apidiffVerdict([]byte(`{"changes":[],"incomplete":true}`), 1, listed); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("incomplete: %v", err)
	}
	if _, _, err := apidiffVerdict(nil, 2, listed); err == nil {
		t.Error("exit 2 without a document passes")
	}
	if un, _, err := apidiffVerdict([]byte(`{"changes":[]}`), 0, listed); err != nil || len(un) != 0 {
		t.Errorf("no change: %q %v", un, err)
	}
}

// TestGateApidiffExtract: extractStd writes a revision's std/ from git
// archive, in a scratch repository.
func TestGateApidiffExtract(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	writeGateFiles(t, root, map[string]string{"std/core/a.mk": "export A() {}\n", "std/pkg.mk": "x\n", "other.txt": "o"})
	for _, a := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}, {"tag", "v0"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	g := &gate{root: root}
	dir := filepath.Join(t.TempDir(), "old")
	if err := g.extractStd("v0", dir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "std", "core", "a.mk")); err != nil || string(b) != "export A() {}\n" {
		t.Errorf("std/core/a.mk: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err == nil {
		t.Error("a file outside std/ was extracted")
	}
}

// TestGateMkFuzzTargetsOfTheTree pins the repository's Mk fuzz targets: none
// (the conformance units that call testing.Fuzz are no targets).
func TestGateMkFuzzTargetsOfTheTree(t *testing.T) {
	got, err := mkFuzzTargets(filepath.Join("..", ".."), func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the tree's Mk fuzz targets: %v", got)
	}
}
