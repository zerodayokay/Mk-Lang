// Why: std.image's test blocks must pass at dev and release and under both
// carrier counts.
// What: TestImage_Blocks.
// How: runs `mk test --std=std.image` over a copy of std in each mode.

package std

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var imageSummary = regexp.MustCompile(`(?m)^(\d+) passed, (\d+) failed`)

func imageBlocks(t *testing.T, carriers string, flags ...string) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CARRIERS", carriers)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	args := append([]string{"test", "--std=std.image"}, flags...)
	out, status := mkCmd(t, root, stdDir, args...)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	m := imageSummary.FindStringSubmatch(out)
	if status != 0 || m == nil || m[2] != "0" {
		t.Fatalf("mk %s (MK_CARRIERS=%s): exit %d\n%s", strings.Join(args, " "), carriers, status, out)
	}
	if m[1] == "0" {
		t.Fatalf("mk %s: no block ran\n%s", strings.Join(args, " "), out)
	}
}

// TestImage_Blocks runs every std.image block on this host.
func TestImage_Blocks(t *testing.T) {
	for _, prof := range []string{"dev", "release"} {
		for _, carriers := range []string{"1", "16"} {
			t.Run(prof+"/"+carriers, func(t *testing.T) {
				var flags []string
				if prof == "release" {
					flags = append(flags, "--release")
				}
				imageBlocks(t, carriers, flags...)
			})
		}
	}
}
