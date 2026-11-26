// Why: std.http.h2's own test blocks (the frame parser, frame writers,
// settings, flow control, and abuse budgets) must pass under the real `mk
// test`, in the dev profile, where every contract and check runs, and in
// release, where the engine runs.
//
// What: TestHttpH2_Blocks.
//
// How: reads every block name of http/h2/tests_*.mk, runs
// `mk test --std=std.http.h2` over a copy of std in dev and release, on 1
// and 8 carriers, and requires a PASS line for each name and nothing failing.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestHttpH2_Blocks(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("http", "h2", "tests_*.mk"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no http/h2/tests_*.mk: %v", err)
	}
	var withBlocks []string
	for _, f := range files {
		if !strings.HasSuffix(f, "tests_helpers.mk") {
			withBlocks = append(withBlocks, filepath.ToSlash(f))
		}
	}
	names := ioBlockNames(t, withBlocks)
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
	}
	for _, c := range []cfg{{nil, 1}, {nil, 8}, {[]string{"--release"}, 1}, {[]string{"--release"}, 8}} {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		args := append([]string{"test", "--std=std.http.h2"}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d", strings.Join(args, " "), c.carriers)
		for _, n := range names {
			if !strings.Contains(out, fmt.Sprintf("PASS std.http.h2 %q", n)) {
				t.Errorf("%s: %q did not pass (status %d)\n%s", where, n, status, out)
			}
		}
		if want := fmt.Sprintf("%d passed, 0 failed", len(names)); status != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: status %d, want %q\n%s", where, status, want, out)
		}
	}
}
