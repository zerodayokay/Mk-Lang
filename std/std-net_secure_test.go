// Why: net.Secure is how std.tls plugs into net.Conn, and its erased call must
// hand over or keep the connection correctly.
// What: TestStdNet_Secure (Connect and Negotiate) and
// TestStdNet_SecureMismatchPanics.
// How: a fixture module std.netsecure in a std copy builds a connector the way
// std.tls does, and Connect runs over a loopback stream's Conn.

package std

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const net3SecureBlocks = `import std.core
import std.net

// Upper is the connector's state: whether to refuse, and why.
struct Upper {
    refuse: bool
    why: net.SecureFailure
}

// Handshake is the connector's function: it refuses, leaving conn with
// the caller, or hands conn back as the secure connection, choosing the
// last protocol of offer (-1 for an empty offer). (A connector that wraps
// conn in a transport of its own reserves its box first with
// net.ReserveConn and fills it last, so it never fails after the move;
// net/tests_conn.mk's Layer does that.)
Handshake(u: &Upper, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    if u.refuse {
        fail net.SecureFailed(why: u.why)
    }
    if host.Len() == 0 {
        fail net.SecureFailed(why: net.SecureFailure.NameMismatch)
    }
    return (conn: conn, chosen: offer.Len() - 1)
}

MakeSecure(u: &Upper): net.Secure from u {
    return net.Secure.Of[Upper](core.ErasedCall.Of[Upper, fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn](u, Handshake))
}

// Pair is a connected loopback pair, the dialled end as a Conn.
Pair(io: Io, l: &net.Listener): net.Error!(c: net.Conn, s: net.Stream) {
    var b: [32]u8 = [0; 32]
    var w := core.Capped.New(mut b[..])
    var ww := w.Writer()
    Print(mut ww, "127.0.0.1:{p}", (p: l.Port())) else {
        Panic("an address longer than 32 bytes")
    }
    d := try net.Dial(io, str.From(w.Bytes()).Must())
    s := l.Accept(io) else e {
        d.Close()
        fail e
    }
    return (c: d.Conn(), s: s)
}

// Same reports whether b holds exactly s.
Same(b: []u8, s: str): bool {
    t := str.From(b) else {
        return false
    }
    return t == s
}

test "Secure.Connect through a copied connector runs its function, and the caller uses and closes the result" (sys: &System) {
    u := Upper(refuse: false, why: net.SecureFailure.Handshake)
    sec := MakeSecure(&u)
    // A Secure is plain: a copy is the same connector.
    copy := sec
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    (c, s) := try Pair(sys.io, &l)
    defer s.Close()
    sc := copy.Connect(sys.heap, sys.io, c, "localhost") else e {
        c.Abandon()
        fail e
    }
    onerr sc.Abandon()
    try s.WriteAll(sys.io, "hello".AsBytes())
    var buf: [8]u8 = [0; 8]
    var n := 0
    for n < 5 {
        n += try sc.Read(sys.io, mut buf[n..])
    }
    Assert(Same(buf[0..5], "hello"))
    Assert(sc.PeerAddr().IsSome())
    try sc.Close(sys.io)
}

test "Secure.Negotiate hands its offer to the connector's function and returns the index it chose; Connect hands an empty one" (sys: &System) {
    u := Upper(refuse: false, why: net.SecureFailure.Handshake)
    sec := MakeSecure(&u)
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    offer: [3]str = ["h2", "http/1.1", "x"]
    for k in 0..4 {
        (c, s) := try Pair(sys.io, &l)
        defer s.Close()
        (conn: sc, chosen) := sec.Negotiate(sys.heap, sys.io, c, "localhost", offer[0..k]) else e {
            c.Abandon()
            fail e
        }
        onerr sc.Abandon()
        Assert(chosen == k - 1)
        try s.WriteAll(sys.io, "ok".AsBytes())
        var buf: [2]u8 = [0; 2]
        var n := 0
        for n < 2 {
            n += try sc.Read(sys.io, mut buf[n..])
        }
        Assert(Same(buf[..], "ok"))
        try sc.Close(sys.io)
    }
}

test "Secure.Connect that fails reports SecureFailed(why), prints it, and leaves the connection with the caller" (sys: &System) {
    u := Upper(refuse: true, why: net.SecureFailure.Expired)
    sec := MakeSecure(&u)
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    (c, s) := try Pair(sys.io, &l)
    defer s.Close()
    sc := sec.Connect(sys.heap, sys.io, c, "localhost") else e {
        var eb: [64]u8 = [0; 64]
        var ec := core.Capped.New(mut eb[..])
        var ew := ec.Writer()
        Print(mut ew, "{e}", (e)) else {
        }
        Assert(Same(ec.Bytes(), "SecureFailed(why: Expired)"))
        // c is still the caller's: it works, then closes.
        onerr c.Abandon()
        try c.WriteAll(sys.io, "x".AsBytes())
        var one: [1]u8 = [0; 1]
        Assert(try s.Read(sys.io, mut one[..]) == 1)
        try c.Close(sys.io)
        return
    }
    sc.Abandon()
    Panic("a refusing connector succeeded")
}
`

const net3SecureMismatch = `
// Other is a state type the connector was not made from.
struct Other {
    n: int
}

test "MISMATCH a Secure made from another state type panics at Connect" (sys: &System) {
    u := Upper(refuse: false, why: net.SecureFailure.Handshake)
    sec := net.Secure.Of[Other](core.ErasedCall.Of[Upper, fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn](&u, Handshake))
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    (c, s) := try Pair(sys.io, &l)
    defer s.Close()
    sc := sec.Connect(sys.heap, sys.io, c, "") else e {
        c.Abandon()
        fail e
    }
    sc.Abandon()
}
`

func net3SecureRun(t *testing.T, blocks string, carriers int) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, map[string]string{"netsecure/tests_secure.mk": blocks})
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	t.Setenv("MK_CARRIERS", fmt.Sprint(carriers))
	out, status := mkCmd(t, root, stdDir, "test", "--std=std.netsecure")
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	return out, status
}

func TestStdNet_Secure(t *testing.T) {
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			out, status := net3SecureRun(t, net3SecureBlocks, c)
			if status != 0 || strings.Count(out, "PASS std.netsecure") != 3 {
				t.Fatalf("status %d\n%s", status, out)
			}
		})
	}
}

func TestStdNet_SecureMismatchPanics(t *testing.T) {
	out, _ := net3SecureRun(t, net3SecureBlocks+net3SecureMismatch, 1)
	re := regexp.MustCompile(`FAIL std\.netsecure "MISMATCH[^"]*": panic[^\n]*\n(?:[^\n]*\n){0,3}?[^\n]*an erased value recovered as a type it was not made from`)
	if !re.MatchString(out) {
		t.Fatalf("the mismatched Secure did not panic with the erasure's message:\n%s", out)
	}
}
