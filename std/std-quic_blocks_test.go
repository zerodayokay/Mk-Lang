// Why: std.quic's wire, protection, frame and parameter test blocks (RFC 9001
// A.1-A.5, RFC 9000 A and QT/QS rules) must pass under the real `mk test` in
// both dev and release profiles.
//
// What: TestStdQuic_Blocks.
//
// How: collects every test block name from quic/tests_*.mk, runs
// `mk test --std=std.quic` over a copy of std in dev and release at 1 and 8
// carriers, and verifies every block passes with 0 failures.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestStdQuic_Blocks(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("quic", "tests_*.mk"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no quic/tests_*.mk: %v", err)
	}
	var withBlocks []string
	for _, f := range files {
		withBlocks = append(withBlocks, filepath.ToSlash(f))
	}
	names := ioBlockNames(t, withBlocks)
	if len(names) == 0 {
		t.Fatalf("no test blocks found in %v", withBlocks)
	}
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
	}
	for _, c := range []cfg{{nil, 1}, {nil, 8}, {[]string{"--release"}, 1}, {[]string{"--release"}, 8}} {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		args := append([]string{"test", "--std=std.quic"}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d", strings.Join(args, " "), c.carriers)
		for _, n := range names {
			if !strings.Contains(out, fmt.Sprintf("PASS std.quic %q", n)) {
				t.Errorf("%s: %q did not pass (status %d)\n%s", where, n, status, out)
			}
		}
		if want := fmt.Sprintf("%d passed, 0 failed", len(names)); status != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: status %d, want %q\n%s", where, status, want, out)
		}
	}
}
