// Why: concurrent builds sharing a cache must each keep their C object until link.
// What: TestConcurrentCBuilds, over a cold cache.
// How: two mk processes build one C-source package with separate outputs.

package corpus

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

func TestConcurrentCBuilds(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the cseh package requires Windows")
	}
	needLLVM(t)
	root := repoRoot(t)
	exe := buildMk(t, root)
	pkg := t.TempDir()
	copyTree(t, filepath.Join(root, ProgramsDir, "cseh"), pkg)
	cacheDir, outputDir := t.TempDir(), t.TempDir()
	var builds sync.WaitGroup
	for i := range 2 {
		builds.Go(func() {
			out := filepath.Join(outputDir, "cseh"+strconv.Itoa(i)+".exe")
			cmd := exec.CommandContext(t.Context(), exe, "build", "-o", out)
			cmd.Dir = pkg
			cmd.Env = append(cmd.Environ(), "MK_CACHE="+cacheDir)
			if log, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("build %d: %v\n%s", i, err, log)
			}
		})
	}
	builds.Wait()
}
