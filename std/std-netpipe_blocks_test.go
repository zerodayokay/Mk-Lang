// Why: std.net.pipe's bounded ConnPair and MuxPair fakes must pass under
// the real `mk test` in dev and release across carrier counts so HTTP/2 and
// HTTP/3 tests can rely on them.
//
// What: TestStdNetPipe_Blocks.
//
// How: collects block names from net/pipe/tests_*.mk and runs
// `mk test --std=std.net.pipe` over a copy of std at 1 and 8 carriers in
// dev and release.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func netPipeBlockFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("net", "pipe", "tests_*.mk"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no net/pipe/tests_*.mk: %v", err)
	}
	var out []string
	for _, f := range files {
		out = append(out, filepath.ToSlash(f))
	}
	return out
}

func TestStdNetPipe_Blocks(t *testing.T) {
	names := ioBlockNames(t, netPipeBlockFiles(t))
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
	}
	for _, c := range []cfg{{nil, 1}, {nil, 8}, {[]string{"--release"}, 1}, {[]string{"--release"}, 8}} {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		args := append([]string{"test", "--std=std.net.pipe"}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d", strings.Join(args, " "), c.carriers)
		for _, n := range names {
			if !strings.Contains(out, fmt.Sprintf("PASS std.net.pipe %q", n)) {
				t.Errorf("%s: %q did not pass (status %d)\n%s", where, n, status, out)
			}
		}
		if want := fmt.Sprintf("%d passed, 0 failed", len(names)); status != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: status %d, want %q\n%s", where, status, want, out)
		}
	}
}
