// Why: net.Secure is how std.tls plugs into net.Conn, and its erased call must
// hand over or keep the connection correctly.
// What: TestStdNet_Secure (Connect, and Negotiate through Of and OfAlpn),
// TestStdNet_SecureMismatchPanics and TestStdNet_SecureOfAlpnTyped.
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
// the caller, or hands conn back as the secure connection. (A connector
// that wraps conn in a transport of its own reserves its box first with
// net.ReserveConn and fills it last, so it never fails after the move;
// net/tests_conn.mk's Layer does that.)
Handshake(u: &Upper, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    if u.refuse {
        fail net.SecureFailed(why: u.why)
    }
    if host.Len() == 0 {
        fail net.SecureFailed(why: net.SecureFailure.NameMismatch)
    }
    return conn
}

MakeSecure(u: &Upper): net.Secure from u {
    return net.Secure.Of[Upper](core.ErasedCall.Of[Upper, fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn](u, Handshake))
}

// Negotiating is the negotiation function: Handshake's checks, but an
// empty host is Expired (so a test sees which function ran), and the
// last protocol of offer is chosen (-1 for an empty offer).
Negotiating(u: &Upper, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    if u.refuse {
        fail net.SecureFailed(why: u.why)
    }
    if host.Len() == 0 {
        fail net.SecureFailed(why: net.SecureFailure.Expired)
    }
    return (conn: conn, chosen: offer.Len() - 1)
}

MakeNegotiating(u: &Upper): net.Secure from u {
    return net.Secure.OfAlpn[Upper](core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn].Of[Upper](u, Handshake), core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn].Of[Upper](u, Negotiating))
}

// Why is the SecureFailure of a failed Negotiate, Handshake for any other
// failure.
Why(e: &net.Error): net.SecureFailure {
    return match *e {
        net.SecureFailed(why) { why }
        _ { net.SecureFailure.Handshake }
    }
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

// Echo negotiates offer for host through sec over a fresh loopback pair,
// checks that two bytes cross the result, and returns chosen.
Echo(sys: &System, l: &net.Listener, sec: &net.Secure, host: str, offer: []str): net.Error!int {
    (c, s) := try Pair(sys.io, l)
    defer s.Close()
    (conn: sc, chosen) := sec.Negotiate(sys.heap, sys.io, c, host, offer) else e {
        c.Abandon()
        fail e
    }
    onerr sc.Abandon()
    try s.WriteAll(sys.io, "ok".AsBytes())
    var buf: [2]u8 = [0; 2]
    var n := 0
    for n < 2 {
        n += try sc.Read(sys.io, mut buf[n..])
    }
    Assert(Same(buf[..], "ok"))
    try sc.Close(sys.io)
    return chosen
}

test "Secure.Negotiate of a connector made by Of runs its Connect: chosen is -1 whatever the offer, and the connection works" (sys: &System) {
    u := Upper(refuse: false, why: net.SecureFailure.Handshake)
    sec := MakeSecure(&u)
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    offer: [3]str = ["h2", "http/1.1", "x"]
    for k in 0..4 {
        Assert(try Echo(sys, &l, &sec, "localhost", offer[0..k]) == -1)
    }
    // An empty host reaches Handshake, the function Of was given.
    (c, s) := try Pair(sys.io, &l)
    defer s.Close()
    (conn: sc, chosen: _) := sec.Negotiate(sys.heap, sys.io, c, "", offer[..]) else e {
        c.Abandon()
        Assert(Why(&e) == net.SecureFailure.NameMismatch)
        return
    }
    sc.Abandon()
    Panic("an empty host was not refused")
}

test "Secure.Negotiate of a connector made by OfAlpn hands its offer to the negotiation function and returns the index it chose; its Connect runs the connect function" (sys: &System) {
    u := Upper(refuse: false, why: net.SecureFailure.Handshake)
    sec := MakeNegotiating(&u)
    copy := sec
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    offer: [3]str = ["h2", "http/1.1", "x"]
    for k in 0..4 {
        Assert(try Echo(sys, &l, &copy, "localhost", offer[0..k]) == k - 1)
    }
    // An empty host tells the two functions apart: Negotiating is Expired,
    // Handshake NameMismatch.
    (c, s) := try Pair(sys.io, &l)
    defer s.Close()
    (conn: nc, chosen: _) := sec.Negotiate(sys.heap, sys.io, c, "", offer[..]) else e {
        Assert(Why(&e) == net.SecureFailure.Expired)
        sc := sec.Connect(sys.heap, sys.io, c, "") else e2 {
            c.Abandon()
            Assert(Why(&e2) == net.SecureFailure.NameMismatch)
            return
        }
        sc.Abandon()
        Panic("Connect ran the negotiation function")
    }
    nc.Abandon()
    Panic("an empty host was not refused")
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
    sec := net.Secure.Of[Other](core.ErasedCall.Of[Upper, fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn](&u, Handshake))
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
			if status != 0 || strings.Count(out, "PASS std.netsecure") != 4 {
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

// net3SecureWrongType builds a Secure through OfAlpn from a negotiation
// function of Secure.Of's type, the mistake a run-time check would catch
// only at the first Negotiate.
const net3SecureWrongType = `
MakeWrong(u: &Upper): net.Secure from u {
    return net.Secure.OfAlpn[Upper](core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn].Of[Upper](u, Handshake), core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn].Of[Upper](u, Handshake))
}
`

// net3SecureWrongState builds one whose calls are of another state's type.
const net3SecureWrongState = `
struct Other {
    n: int
}

MakeWrong(u: &Upper): net.Secure from u {
    return net.Secure.OfAlpn[Other](core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn].Of[Upper](u, Handshake), core.TypedCall[fn(&Upper, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn].Of[Upper](u, Negotiating))
}
`

// TestStdNet_SecureOfAlpnTyped: OfAlpn refuses, at compile time, a call
// of another function type or of another state's type.
func TestStdNet_SecureOfAlpnTyped(t *testing.T) {
	for name, src := range map[string]string{"function": net3SecureWrongType, "state": net3SecureWrongState} {
		t.Run(name, func(t *testing.T) {
			out, status := net3SecureRun(t, net3SecureBlocks+src, 1)
			if status == 0 || strings.Contains(out, "PASS std.netsecure") || !regexp.MustCompile(`tests_secure\.mk:\d+:\d+: E0301 type-mismatch: expected .TypedCall\[fn\(&`).MatchString(out) || !strings.Contains(out, "net.Secure.OfAlpn[") {
				t.Fatalf("status %d: want E0301 at OfAlpn's argument\n%s", status, out)
			}
		})
	}
}
