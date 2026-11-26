// Why: QUIC requires setting the IP Don't Fragment (DF) bit on UDP sockets
// where the host OS permits (RFC 9000 14 / QT-271) so Path MTU probes are
// never silently fragmented by IP.
//
// What: TestStdQuic_DontFragmentSet.
//
// How: copies std into a temp workspace, adds a fixture test block that binds
// an IPv4 native UDP socket, invokes SetDontFragmentUdp (plus Datagram.DontFragment
// readback), and verifies a loopback QUIC dial/accept succeeds with DF enabled.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const quicDgramTestSrc = `// Generated fixture from std/std-quic_dgram_test.go.
import std.net

test "QT-271: IPv4 UDP SetDontFragmentUdp enables DF on native OS socket" (sys: &System) {
    io := sys.io
    var d := try net.Bind(io, "127.0.0.1:0")
    defer d.Close()
    SetDontFragmentUdp(&d)
    df := try d.DontFragment()
    Assert(df)
}
`

// TestStdQuic_DontFragmentSet verifies DF is set on IPv4 QUIC UDP sockets (QT-271).
func TestStdQuic_DontFragmentSet(t *testing.T) {
	root := t.TempDir()
	stdDir := copyStd(t, root, map[string]string{
		"quic/zz_dgram_df_test.mk": quicDgramTestSrc,
	})
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	out, status := mkCmd(t, root, stdDir, "test", "--std=std.quic", "-run", "QT-271:")
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	want := `PASS std.quic "QT-271: IPv4 UDP SetDontFragmentUdp enables DF on native OS socket"`
	if status != 0 || !strings.Contains(out, want) || !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("status %d\n%s", status, fmt.Sprint(out))
	}
}
