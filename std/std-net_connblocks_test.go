// Why: std.net's connection test blocks need the real `mk test` with the
// counting heap, at several carrier counts and on both OSes.
// What: TestStdNet_Blocks, TestStdNet_LinuxBlocks and TestStdNet_LinuxSim.
// How: each block in a child process at 1 and 16 carriers; on Linux
// (MK_LINUX=1, under tools/wsl-lock.ps1) under io_uring and MK_IO=epoll.
// Its tests are cancel-safety tests; tools/gate/safety.go lists each.

package std

import (
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// net3BlockFiles are the files whose blocks these tests judge.
// net/tests_async.mk holds concurrency regression blocks; on Windows two
// of them assert only with MK_NET3_BLOCKED set, which mk inherits from
// this process.
var net3BlockFiles = []string{"net/tests_conn.mk", "net/tests_stream.mk", "net/tests_unix.mk", "net/tests_async.mk"}

// net3Judge fails t unless every block of net3BlockFiles passed in out.
func net3Judge(t *testing.T, where, out string, status int) {
	t.Helper()
	for _, n := range ioBlockNames(t, net3BlockFiles) {
		if !strings.Contains(out, fmt.Sprintf("PASS std.net %q", n)) {
			t.Errorf("%s: %q did not pass (status %d)", where, n, status)
		}
	}
	if t.Failed() {
		t.Logf("%s:\n%s", where, out)
	}
}

// TestStdNet_Blocks runs every std.net block once per configuration and
// judges net3BlockFiles' blocks; another file's failing block is logged.
// On Windows one configuration has a TEMP past sun_path (unixLongTemp),
// which the Unix blocks' relative socket names must not depend on.
func TestStdNet_Blocks(t *testing.T) {
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type config struct {
		carriers int
		flags    []string
		longTemp bool
	}
	configs := []config{{1, nil, false}, {16, nil, false}, {16, []string{"--release"}, false}}
	if runtime.GOOS == "windows" {
		configs = append(configs, config{16, nil, true})
	}
	for _, c := range configs {
		where := fmt.Sprintf("carriers=%d%s", c.carriers, strings.Join(c.flags, ""))
		if c.longTemp {
			where += " long TEMP"
		}
		t.Run(where, func(t *testing.T) {
			t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
			if c.longTemp {
				// mk is built first, in the package's temporary directory,
				// which outlives this one.
				mkExe(t)
				long := unixLongTemp(t)
				t.Setenv("TEMP", long)
				t.Setenv("TMP", long)
			}
			args := append([]string{"test", "--std=std.net"}, c.flags...)
			out, status := mkCmd(t, root, stdDir, args...)
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			net3Judge(t, where, out, status)
		})
	}
}

// TestStdNet_LinuxBlocks runs the same blocks on x86_64-linux, one
// wsl.exe per block, at 16 carriers under io_uring (the default) and
// epoll. MK_LINUX=1 only, under the WSL lock with go test -p 1.
func TestStdNet_LinuxBlocks(t *testing.T) {
	linuxGate(t)
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	t.Setenv("MK_CARRIERS", "16")
	net3LinuxScratch(t, root)
	for _, backend := range []string{"", "epoll"} {
		t.Run("io="+backend, func(t *testing.T) {
			t.Setenv("MK_IO", backend)
			var out string
			status := 0
			for _, n := range ioBlockNames(t, net3BlockFiles) {
				o, st := mkCmd(t, root, stdDir, "test", "--std=std.net", "--target", "x86_64-linux", "-run", n)
				out += o
				if st != 0 {
					status = st
				}
			}
			net3Judge(t, "x86_64-linux io="+backend, out, status)
		})
	}
	// The blocks bind in MK_LINUX_TMP, not in UxDir's /tmp fallback: with
	// it naming a directory that does not exist, a Unix block must fail.
	t.Run("scratch-is-used", func(t *testing.T) {
		t.Setenv("MK_IO", "")
		t.Setenv("MK_LINUX_TMP", fmt.Sprintf("/tmp/mk-std-net3-missing-%d", time.Now().UnixNano()))
		n := "ListenUnix refuses a path in use, an empty path and one too long; DialUnix to nothing is Refused"
		out, st := mkCmd(t, root, stdDir, "test", "--std=std.net", "--target", "x86_64-linux", "-run", n)
		if st == 0 || !strings.Contains(out, fmt.Sprintf("FAIL std.net %q", n)) {
			t.Errorf("the Unix block passed with MK_LINUX_TMP missing (status %d), so it does not bind there\n%s", st, out)
		}
	})
}

// net3LinuxScratch makes a scratch directory under the distribution's
// /tmp through the runtime tests' runner (linuxRunScript: one wsl.exe client,
// under linuxGate and the lock), sets MK_LINUX_TMP to it and forwards it
// with MK_CARRIERS and MK_IO through WSLENV, and removes it after the
// test. mk test --target x86_64-linux starts a wsl.exe per block, so the
// directory outlives one client, unlike linuxScratchSh's; the Unix blocks
// put their socket files in it (net/tests_unix.mk's UxDir).
func net3LinuxScratch(t *testing.T, root string) {
	t.Helper()
	out := linuxRunScript(t, root, "net3-mktemp.sh", "mktemp -d /tmp/mk-std-XXXXXX\n", time.Minute)
	dir := strings.TrimSpace(out)
	if !strings.HasPrefix(dir, "/tmp/mk-std-") || strings.ContainsAny(dir, "'\n ") {
		t.Fatalf("mktemp gave %q", out)
	}
	t.Cleanup(func() {
		linuxRunScript(t, root, "net3-rm.sh", "rm -rf -- '"+dir+"'\n", time.Minute)
	})
	t.Setenv("MK_LINUX_TMP", dir)
	t.Setenv("WSLENV", "MK_CARRIERS/u:MK_IO/u:MK_LINUX_TMP/u")
}

// TestStdNet_LinuxSim runs the std.sim fixtures (TestStdNet_SimCancel's
// cancel blocks and TestStdNet_SimSweep's sweep and Recorder blocks) on
// x86_64-linux at 16 carriers, one wsl.exe per block. MK_LINUX=1 only,
// under the WSL lock with go test -p 1.
func TestStdNet_LinuxSim(t *testing.T) {
	linuxGate(t)
	for _, f := range []struct{ module, file, blocks string }{
		{"std.netsim", "netsim/tests_conn.mk", net3SimBlocks},
		{"std.netsweep", "netsweep/tests_sweep.mk", net3SweepBlocks},
	} {
		t.Run(f.module, func(t *testing.T) {
			root := t.TempDir()
			stdDir := copyStd(t, root, map[string]string{f.file: f.blocks})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", "16")
			t.Setenv("WSLENV", "MK_CARRIERS/u")
			names := regexp.MustCompile(`(?m)^test "([^"]+)"`).FindAllStringSubmatch(f.blocks, -1)
			if len(names) == 0 {
				t.Fatal("the fixture has no blocks")
			}
			for _, n := range names {
				out, st := mkCmd(t, root, stdDir, "test", "--std="+f.module, "--target", "x86_64-linux", "-run", n[1])
				if strings.Contains(out, "no usable LLVM") {
					t.Skipf("needs the LLVM toolchain: %s", out)
				}
				if st != 0 || !strings.Contains(out, fmt.Sprintf("PASS %s %q", f.module, n[1])) {
					t.Errorf("x86_64-linux %q: status %d\n%s", n[1], st, out)
				}
			}
		})
	}
}
