// Why: std.net's test blocks need real sockets, the counting heap and more
// than one carrier.
// What: a runner for net/tests_net.mk and net/tests_udp.mk under `mk test`,
// and TestNet_LinuxBlocks (MK_LINUX=1 under tools/wsl-lock.ps1, -p 1).
// How: each block is a child process, dev and release at 1 and 8 carriers;
// the Linux leg covers io_uring and MK_IO=epoll.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// net2Std copies std for these programs into root: from
// MK_NET2_BASE when it is set (a snapshot taken while another
// module was mid-edit), with std/net from this tree and the fixtures on
// top; otherwise copyStd of the tree.
func net2Std(t *testing.T, root string, fixtures map[string]string) string {
	t.Helper()
	base := os.Getenv("MK_NET2_BASE")
	if base == "" {
		return copyStd(t, root, fixtures)
	}
	stdDir := filepath.Join(root, "std")
	if err := os.CopyFS(stdDir, os.DirFS(base)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(stdDir, "net")); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(stdDir, "net"), os.DirFS("net")); err != nil {
		t.Fatal(err)
	}
	for p, text := range fixtures {
		if err := os.WriteFile(filepath.Join(stdDir, filepath.FromSlash(p)), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stdDir
}

func TestNet_Blocks(t *testing.T) { net2Blocks(t, hostIo) }

func TestNet_LinuxBlocks(t *testing.T) {
	stdioNeedWSL(t)
	net2Blocks(t, linuxIo)
}

func net2Blocks(t *testing.T, h ioHost) {
	// Every TCP and UDP block, including the shared datagram tests.
	names := ioBlockNames(t, []string{"net/tests_net.mk", "net/tests_udp.mk", "net/tests_udp_shared.mk"})
	root := t.TempDir()
	stdDir := net2Std(t, root, nil)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	type cfg struct {
		flags    []string
		carriers int
		io       string
	}
	cfgs := []cfg{{nil, 1, ""}, {nil, 8, ""}, {[]string{"--release"}, 1, ""}, {[]string{"--release"}, 8, ""}}
	if h.linux {
		lx := []string{"--target", "x86_64-linux"}
		cfgs = []cfg{{lx, 8, ""}, {append([]string{"--release"}, lx...), 1, ""}, {append([]string{"--release"}, lx...), 8, "epoll"}}
		// mk's child environment keeps WSLENV, so these two reach the Linux
		// children.
		t.Setenv("WSLENV", "MK_CARRIERS/u:MK_IO/u")
	}
	for _, c := range cfgs {
		t.Setenv("MK_CARRIERS", fmt.Sprint(c.carriers))
		t.Setenv("MK_IO", c.io)
		args := append([]string{"test", "--std=std.net"}, c.flags...)
		out, status := mkCmd(t, root, stdDir, args...)
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		where := fmt.Sprintf("mk %s, MK_CARRIERS=%d, MK_IO=%q", strings.Join(args, " "), c.carriers, c.io)
		for _, n := range names {
			if !strings.Contains(out, fmt.Sprintf("PASS std.net %q", n)) {
				t.Errorf("%s: %q did not pass (status %d)\n%s", where, n, status, out)
			}
		}
		if status != 0 {
			t.Errorf("%s: status %d\n%s", where, status, out)
		}
	}
}
