// Why: using the shipped POSIX view must select the glibc ABI on each Linux target.
// What: exercises the embedded view in a Mk package with no explicit links.
// How: verifies the ELF dependency and symbol version, then runs on the matching host.

package driver

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"mk/internal/types"
)

func TestSysView_EmbeddedPOSIX(t *testing.T) {
	const main = `import c.posix

export Main(sys: &System): WriteError!void {
    var parent: i32 = 0
    unsafe "getppid takes no arguments and returns the parent process ID" {
        parent = posix.getppid()
    }
    try sys.Print("{parent}\n", (parent))
}
`
	dir := writeTree(t, sysPkg(main, nil))
	for _, target := range []types.Target{types.X86_64Linux, types.Aarch64Linux} {
		t.Run(target.Name, func(t *testing.T) {
			for _, profile := range []types.Profile{types.Dev, types.Release} {
				t.Run(fmt.Sprint(profile), func(t *testing.T) {
					exe := mustBuild(t, dir, target, profile)
					spec := libcSpecs[target.Name]
					interp, needed := elfDyn(t, exe)
					if interp != spec.interp || !slices.Equal(needed, []string{"libc.so.6"}) {
						t.Fatalf("interpreter %q, dynamic libraries %v", interp, needed)
					}
					if version, lib := importVersion(t, exe, "getppid"); version != spec.base || lib != "libc.so.6" {
						t.Fatalf("getppid requires %q of %q, want %s of libc.so.6", version, lib, spec.base)
					}
					if HostTarget() == target {
						out, stderr, status := runExe(t, target, exe, "")
						if want := fmt.Sprintf("%d\n", os.Getpid()); status != 0 || out != want {
							t.Fatalf("exit %d, stdout %q, want %q, stderr %q", status, out, want, stderr)
						}
						t.Log("ran against host glibc")
					}
				})
			}
		})
	}
}
