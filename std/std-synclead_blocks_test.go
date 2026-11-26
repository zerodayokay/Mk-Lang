// Why: std.sync.lead's read-token protocol and Token FIFO must pass under the
// real `mk test` in dev and release across carrier counts, and under the
// simulated PCT scheduler with concurrent cancellations.
//
// What: TestStdSyncLead_Blocks and TestStdSyncLead_PctCancel.
//
// How: collects block names from sync/lead/tests_*.mk and runs
// `mk test --std=std.sync.lead` over a copy of std at 1 and 8 carriers in
// dev and release.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func syncLeadBlockFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("sync", "lead", "tests_*.mk"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no sync/lead/tests_*.mk: %v", err)
	}
	var out []string
	for _, f := range files {
		out = append(out, filepath.ToSlash(f))
	}
	return out
}

func TestStdSyncLead_Blocks(t *testing.T) {
	names := ioBlockNames(t, syncLeadBlockFiles(t))
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
	}
	for _, c := range []cfg{{nil, 1}, {nil, 8}, {[]string{"--release"}, 1}, {[]string{"--release"}, 8}} {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		args := append([]string{"test", "--std=std.sync.lead"}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d", strings.Join(args, " "), c.carriers)
		for _, n := range names {
			if !strings.Contains(out, fmt.Sprintf("PASS std.sync.lead %q", n)) {
				t.Errorf("%s: %q did not pass (status %d)\n%s", where, n, status, out)
			}
		}
		if want := fmt.Sprintf("%d passed, 0 failed", len(names)); status != 0 || !strings.Contains(out, want) {
			t.Errorf("%s: status %d, want %q\n%s", where, status, want, out)
		}
	}
}

// TestStdSyncLead_PctCancel runs the std.sim PCT-scheduled cancellation sweep
// over Wait[D: Driven] in dev and release at 1 and 8 carriers.
// It is a cancel-safety test; tools/gate/safety.go lists it.
func TestStdSyncLead_PctCancel(t *testing.T) {
	const name = "PCT cancel-safety across 64 seeds"
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
	}
	for _, c := range []cfg{{nil, 1}, {nil, 8}, {[]string{"--release"}, 1}, {[]string{"--release"}, 8}} {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		args := append([]string{"test", "--std=std.sync.lead", "-run", name}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d", strings.Join(args, " "), c.carriers)
		if status != 0 || !strings.Contains(out, fmt.Sprintf("PASS std.sync.lead %q", name)) {
			t.Errorf("%s: %q did not pass (status %d)\n%s", where, name, status, out)
		}
	}
}
