// Why: shutdown must wake parked reads and writes at once, and extension and
// large-message checks must stay correct and cheap.
// What: TestStdTls_ExtCheck, TestStdTls_ShutdownReadWakes,
// TestStdTls_ShutdownBothWakesWrite and TestStdTls_SimShutdownWakes.
// How: the shutdown cases run natively against Go's crypto/tls server and under
// std.sim (tls/tests_tlsshut.mk), where "at once" is read on the simulated
// clock; ExtCheck's cost is timed only with MK_TIMING=1.

package std

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tlsCertMsg is a Certificate message (RFC 8446 4.4.2) of entries, each
// a cert_data and an extension block.
func tlsCertMsg(entries [][2][]byte) []byte {
	var list []byte
	for _, e := range entries {
		list = append(list, byte(len(e[0])>>16), byte(len(e[0])>>8), byte(len(e[0])))
		list = append(list, e[0]...)
		list = append(list, byte(len(e[1])>>8), byte(len(e[1])))
		list = append(list, e[1]...)
	}
	body := append([]byte{0, byte(len(list) >> 16), byte(len(list) >> 8), byte(len(list))}, list...)
	return tlsRawMsg(11, body)
}

// tlsExts is an extension block of the types given, each with an empty
// body, except that a negative type -n is type n with one byte of body.
func tlsExts(types ...int) []byte {
	var b []byte
	for _, ty := range types {
		if ty < 0 {
			b = tlsRawExt(b, uint16(-ty), []byte{0})
		} else {
			b = tlsRawExt(b, uint16(ty), nil)
		}
	}
	return b
}

// tlsManyEntries is a Certificate of n entries of one byte of cert_data
// and ext as each one's extensions.
func tlsManyEntries(n int, ext []byte) []byte {
	es := make([][2][]byte, n)
	for i := range es {
		es[i] = [2][]byte{{byte(i)}, ext}
	}
	return tlsCertMsg(es)
}

// tlsBigSeeds are the large-message seeds of FuzzStdTls_Messages:
// a Certificate of 43,690 empty entries (262,144 bytes of
// body, the bound), one of a single 200 KiB entry, and a 60 KiB
// ClientHello (a long padding extension).
func tlsBigSeeds() [][]byte {
	one := make([]byte, 200<<10)
	for i := range one {
		one[i] = byte(i)
	}
	var hello []byte
	hello = tlsRawU16(hello, 0x0303)
	hello = append(hello, make([]byte, 32)...)
	hello = append(hello, 0)
	hello = append(hello, 0, 2, 0x13, 0x01, 1, 0)
	var e []byte
	e = tlsRawExt(e, 43, []byte{2, 3, 4})
	e = tlsRawExt(e, 21, make([]byte, 60<<10))
	hello = tlsRawU16(hello, uint16(len(e)))
	hello = append(hello, e...)
	return [][]byte{
		tlsManyEntries(43690, nil),
		tlsCertMsg([][2][]byte{{one, nil}}),
		tlsRawMsg(1, hello),
	}
}

// TestStdTls_ExtCheck: ExtCheck refuses a type twice on both of its
// paths (a block of at most 256 bytes compared pairwise, a longer one by
// the bitmap), at the boundary, and a 256 KiB Certificate of 43,690
// entries round-trips. With MK_TIMING=1 it also times the driver on
// 43,690 entries with a 4-byte extension block each against one 262 KiB
// entry: ExtCheck's work is linear in the bytes, so the many-entry message
// may cost at most 3 times the one-entry one.
func TestStdTls_ExtCheck(t *testing.T) {
	var at64, at64dup []int
	for i := 0; i < 64; i++ {
		at64 = append(at64, 100+i)
		at64dup = append(at64dup, 100+i)
	}
	at64dup[63] = 100
	wide := append(append([]int(nil), at64[:63]...), -200) // 63 x 4 + 5 = 257 bytes
	wideDup := append(append([]int(nil), at64dup[:63]...), -100)
	wideDup[0] = 300
	wideDup[62] = 300
	type c struct {
		name string
		msg  []byte
		ok   bool
	}
	cases := []c{
		{"two distinct", tlsManyEntries(1, tlsExts(5, 18)), true},
		{"a type twice, small", tlsManyEntries(1, tlsExts(5, 18, 5)), false},
		{"64 types, 256 bytes", tlsManyEntries(1, tlsExts(at64...)), true},
		{"64 types, one twice, 256 bytes", tlsManyEntries(1, tlsExts(at64dup...)), false},
		{"257 bytes, the bitmap", tlsManyEntries(1, tlsExts(wide...)), true},
		{"257 bytes, a type twice", tlsManyEntries(1, tlsExts(wideDup...)), false},
		{"43,690 empty entries", tlsManyEntries(43690, nil), true},
		{"26,214 entries of one extension", tlsManyEntries(26214, tlsExts(5)), true},
	}
	for _, mode := range tlsModes {
		var records []string
		for _, k := range cases {
			records = append(records, "p "+tlsHex(k.msg))
		}
		got := tlsAsk(t, mode, records)
		for i, k := range cases {
			want := "err Protocol"
			if k.ok {
				want = "ok " + tlsHex(k.msg)
			}
			if got[i] != want {
				t.Errorf("%s (%s): got %.60s, want %.60s", k.name, mode, got[i], want)
			}
		}
	}
	if os.Getenv("MK_TIMING") != "1" {
		return
	}
	many := "p " + tlsHex(tlsManyEntries(26214, tlsExts(5)))
	one := "p " + tlsHex(tlsCertMsg([][2][]byte{{make([]byte, 262144-10), nil}}))
	exe := tlsExe(t, "release")
	best := func(line string) time.Duration {
		b := time.Duration(1 << 62)
		for i := 0; i < 5; i++ {
			start := time.Now()
			if r := runNative(t, exe, strings.Repeat(line+"\n", 20)); r.code != 0 {
				t.Fatalf("driver exit %d: %s", r.code, r.stderr)
			}
			b = min(b, time.Since(start))
		}
		return b
	}
	m, o := best(many), best(one)
	t.Logf("20 parses: 26,214 entries %v, one entry %v", m, o)
	if m > 3*o {
		t.Errorf("26,214 entries took %v, one entry %v: more than 3 times", m, o)
	}
}

// tlsWake runs the peer driver's wake mode against Go's server, which
// reads everything (read) or never reads (write), and returns the
// driver's line and what Go's server read.
func tlsWake(t *testing.T, mode, how string) (string, []byte, error) {
	pki := tlsPeerPKI(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{pki.leaves["p256"]}, MinVersion: tls.VersionTLS13}
	l, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	type seen struct {
		got []byte
		err error
	}
	done := make(chan seen, 1)
	stop := make(chan struct{})
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- seen{err: err}
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(30 * time.Second))
		if how == "write" {
			// The handshake, then nothing read until the test ends.
			err := c.(*tls.Conn).Handshake()
			<-stop
			done <- seen{err: err}
			return
		}
		got, err := io.ReadAll(c)
		done <- seen{got: got, err: err}
	}()
	roots := filepath.Join(t.TempDir(), "root.pem")
	os.WriteFile(roots, []byte(pki.rootPEM), 0o644)
	r := runNative(t, tlsPeerExe(t, mode), "", l.Addr().String(), "localhost", roots, "-", "-", "connector-wake"+how, "0", "0", "5")
	close(stop)
	s := <-done
	return strings.TrimSpace(r.stdout) + strings.TrimSpace(r.stderr), s.got, s.err
}

var tlsWakeLine = regexp.MustCompile(`^ok wake (read=(-?\d+) code=(\w+)|write=(\w+)) shut=(\w+) ms=(\d+)$`)

// TestStdTls_ShutdownReadWakes: natively, through tls.Connector, a Read
// parked in a task returns 0 when another task calls Shutdown(Read),
// within 2 s (the transport's wake; std.sim checks "at once" exactly),
// Shutdown reports no failure, and the write direction still works: Go's
// server reads "hello", then Close's close_notify as io.EOF.
func TestStdTls_ShutdownReadWakes(t *testing.T) {
	for _, mode := range tlsModes {
		t.Run(mode, func(t *testing.T) {
			line, got, err := tlsWake(t, mode, "read")
			m := tlsWakeLine.FindStringSubmatch(line)
			if m == nil || m[2] != "0" || m[5] != "ok" {
				t.Fatalf("driver: %q, want the parked Read to return 0 and Shutdown ok", line)
			}
			if ms, _ := strconv.Atoi(m[6]); ms > 2000 {
				t.Errorf("the parked Read returned %d ms after Shutdown", ms)
			}
			if err != nil || !bytes.Equal(got, []byte("hello")) {
				t.Errorf("Go's server read %q, %v; want hello and close_notify", got, err)
			}
		})
	}
}

// TestStdTls_ShutdownBothWakesWrite: natively, through tls.Connector,
// a 32 MiB WriteAll parked on a Go server that never reads fails with
// Closed when another task calls Shutdown(Both), well before the 5 s
// close limit (2 s here), and Shutdown itself reports no failure; Close
// then succeeds, with nothing left to send.
func TestStdTls_ShutdownBothWakesWrite(t *testing.T) {
	for _, mode := range tlsModes {
		t.Run(mode, func(t *testing.T) {
			line, _, _ := tlsWake(t, mode, "write")
			m := tlsWakeLine.FindStringSubmatch(line)
			if m == nil || m[4] != "Closed" || m[5] != "ok" {
				t.Fatalf("driver: %q, want the parked WriteAll to fail with Closed and Shutdown ok", line)
			}
			if ms, _ := strconv.Atoi(m[6]); ms > 2000 {
				t.Errorf("the parked WriteAll returned %d ms after Shutdown (the close limit is 5000)", ms)
			}
		})
	}
}

// tlsShutBlocks are the std.sim blocks of the two shutdown wake tests.
const tlsShutBlocks = `import std.crypto
import std.encoding
import std.net
import std.sim
import std.time
import std.x509

const ShAddr = "127.0.0.1:7711"

const ShLeaf: str = "@LEAF@"
const ShKey: str = "@KEY@"
const ShRoot: str = "@ROOT@"

struct ShPki {
    chain: []x509.Certificate
    key: crypto.PrivateKey
    pool: x509.Pool
}

LoadShPki(a: &Arena): x509.Error|encoding.Error|crypto.Error|OutOfMemory!ShPki from a {
    certs := try x509.ParsePem(a, ShLeaf)
    blocks := try encoding.ParsePem(a, ShKey)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots := try x509.ParsePem(a, ShRoot)
    chain := certs.Done()
    rs := roots.Done()
    return ShPki(chain: chain[..], key: key, pool: x509.Pool.New(rs[..]))
}

// ShGate is a transport whose writes of more than 1,000 bytes (application
// data; never a handshake or alert record) wait until a Shutdown of its
// write side, then fail with Reset: a peer that stopped reading, over a
// transport that meets net.Transport rule (3) within 100 us.
struct ShGate {
    s: net.Conn
    shut: Mutex[bool]
}

impl net.Transport for ShGate {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return try self.s.Read(io, buf)
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        if data.Len() > 1000 {
            for {
                var g := try self.shut.Lock(io)
                done := *g.Ref()
                g.Unlock()
                if done {
                    fail net.Reset
                }
                try io.Sleep(time.Nanos(100_000))
            }
        }
        try self.s.WriteAll(io, data)
    }

    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        write := match how {
            .Read { false }
            _ { true }
        }
        if write {
            var g := try self.shut.Lock(io)
            g.Store(true)
            g.Unlock()
        }
        try self.s.Shutdown(io, how)
    }

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io): net.Error!void {
    }

    Abandon(self) {
        ShGate(s, shut) := self
        _ = shut.Into()
        s.Abandon()
    }
}

// ShServer accepts one connection and reads it to its end: got bytes,
// code 0 when it ended with close_notify, 4 on a failure.
ShServer(h: &Heap, io: Io, l: &net.Listener, cfg: &Config, got: mut int, code: mut int) {
    s := l.Accept(io) else {
        *code = 1
        return
    }
    conn := s.Conn()
    c := Server(h, io, conn, cfg) else {
        conn.Abandon()
        *code = 2
        return
    }
    var b: [64]u8 = [0; 64]
    for {
        k := c.Read(io, mut b[..]) else e {
            *code = match e {
                Canceled { 5 }
                net.Reset { 6 }
                Alert { 7 }
                Protocol { 8 }
                _ { 9 }
            }
            c.Abandon()
            return
        }
        if k == 0 {
            break
        }
        *got += k
    }
    *code = 0
    c.Close(io) else {
        c.Abandon()
    }
}

// ShParkRead reads c once: k is what it returned (-1 for a failure),
// back when, in ns after origin.
ShParkRead(io: Io, c: &net.Conn, origin: time.Instant, k: mut int, back: mut i64) {
    var b: [64]u8 = [0; 64]
    *k = c.Read(io, mut b[..]) else {
        -1
    }
    *back = time.Now(io).Since(origin).Nanos()
}

// ShParkWrite writes data to c: closed is whether it failed with Closed,
// back when it returned, in ns after origin.
ShParkWrite(io: Io, c: &net.Conn, data: []u8, origin: time.Instant, closed: mut bool, back: mut i64) {
    c.WriteAll(io, data) else e {
        *closed = match e {
            Closed { true }
            _ { false }
        }
    }
    *back = time.Now(io).Since(origin).Nanos()
}

// ShClient connects through Connect over conn, parks a Read (read) or a
// 64 KiB WriteAll in a task, and 5 ms later calls Shutdown(Read) or
// Shutdown(Both). k is the Read's answer, or 1 when the WriteAll failed
// with Closed; lag the ns from Shutdown until the parked call returned;
// shut whether Shutdown succeeded; after a Shutdown(Read), wrote whether
// "hello" was written; closed whether Close succeeded.
ShClient(h: &Heap, io: Io, conn: net.Conn, cfg: &Config, data: []u8, read: bool): (k: int, lag: i64, shut: bool, wrote: bool, closed: bool) {
    own: [0]str = []
    (conn: sc, chosen: _) := Connect(cfg, h, io, conn, "localhost", own[..]) else {
        conn.Abandon()
        return (k: -3, lag: -1, shut: false, wrote: false, closed: false)
    }
    origin := time.Now(io)
    var k := -2
    var hit := false
    var back: i64 = -1
    var at: i64 = -1
    var shut := true
    scope s := io.Scope() {
        if read {
            s.go ShParkRead(io, &sc, origin, mut k, mut back)
        } else {
            s.go ShParkWrite(io, &sc, data, origin, mut hit, mut back)
        }
        io.Sleep(time.Millis(5)) else {}
        at = time.Now(io).Since(origin).Nanos()
        how := if read { net.Shutdown.Read } else { net.Shutdown.Both }
        sc.Shutdown(io, how) else {
            shut = false
        }
    }
    if !read {
        k = if hit { 1 } else { 0 }
    }
    var wrote := false
    if read {
        hello: [5]u8 = [104, 101, 108, 108, 111]
        wrote = true
        sc.WriteAll(io, hello[..]) else {
            wrote = false
        }
    }
    var closed := true
    sc.Close(io) else {
        closed = false
        sc.Abandon()
    }
    return (k: k, lag: back - at, shut: shut, wrote: wrote, closed: closed)
}

// ShRun is one run in a fresh Sim: the client over ShGate, the server
// reading to the end; the client's answers and the server's.
ShRun(h: &Heap, a: &Arena, p: &ShPki, read: bool): Error|OutOfMemory!(k: int, lag: i64, shut: bool, wrote: bool, closed: bool, got: int, code: int) {
    var sm := try sim.Sim.New(a, 23)
    defer sm.Free()
    io := sm.Io()
    ids: [1]Identity = [Identity(chain: p.chain, key: &p.key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    var ccfg := DefaultConfig
    ccfg.roots = some(&p.pool)
    data := try a.Slice[u8](65536)
    l := try net.Listen(io, ShAddr)
    defer l.Close()
    var got := 0
    var code := -1
    var r := (k: 0, lag: i64(0), shut: false, wrote: false, closed: false)
    scope outer := io.Scope() {
        outer.go ShServer(h, io, &l, &scfg, mut got, mut code)
        stream := try net.Dial(io, ShAddr)
        t := ShGate(s: stream.Conn(), shut: Mutex[bool].New(false))
        conn := net.NewConn(h, t) else e {
            t.Abandon()
            fail e
        }
        r = ShClient(h, io, conn, &ccfg, data[..], read)
    }
    return (k: r.k, lag: r.lag, shut: r.shut, wrote: r.wrote, closed: r.closed, got: got, code: code)
}

test "TLS-R1 Shutdown(Read) of a tls.Connector connection wakes a parked Read with 0 within 10 simulated us, stores no failure, and the write direction still takes WriteAll and Close" (sys: &System) {
    p := try LoadShPki(sys.arena)
    var ar := try Arena.New(sys.heap)
    r := ShRun(sys.heap, &ar, &p, true) else e {
        ar.Free()
        fail e
    }
    ar.Free()
    Assert(r.k == 0)
    // std.sim charges each operation a few simulated microseconds; the
    // old code never returned 0 (it failed the connection with Reset).
    Assert(r.lag >= 0 && r.lag <= 10_000)
    Assert(r.shut)
    Assert(r.wrote)
    Assert(r.closed)
    // The peer's side, as natively (TestStdTls_ShutdownReadWakes): the
    // server reads the 5 bytes, then close_notify (its Read returns 0).
    // Before a fix the run's scheduler called a deadlock when the parked
    // Read's task ended while main joined it, so the server's Read ended
    // Canceled (code 5) with nothing read.
    Assert(r.got == 5)
    Assert(r.code == 0)
}

test "TLS-R2 Shutdown(Both) of a tls.Connector connection ends a parked WriteAll with Closed within the transport's wake (100 us), not at the 5 s close limit, and Shutdown and Close succeed" (sys: &System) {
    p := try LoadShPki(sys.arena)
    var ar := try Arena.New(sys.heap)
    r := ShRun(sys.heap, &ar, &p, false) else e {
        ar.Free()
        fail e
    }
    ar.Free()
    Assert(r.k == 1)
    Assert(r.lag >= 0 && r.lag <= 200_000)
    Assert(r.shut)
    Assert(r.closed)
    // The server read nothing of the data, and no close_notify.
    Assert(r.got == 0)
}
`

// tlsPkiFixture is src with the bench row's certificates and key in place
// of "@LEAF@", "@KEY@" and "@ROOT@".
func tlsPkiFixture(t *testing.T, src string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tests", "bench-lanes", "std-tls", "loopback", "mk", "tls-loopback.mk"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Leaf", "Key", "Root"} {
		m := regexp.MustCompile(`(?m)^const ` + name + `Pem: str = (".*")$`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("tls-loopback.mk has no %sPem", name)
		}
		src = strings.Replace(src, `"@`+strings.ToUpper(name)+`@"`, string(m[1]), 1)
	}
	return src
}

// TestStdTls_SimShutdownWakes runs the tlsShutBlocks blocks under
// std.sim at 1 and 16 carriers.
func TestStdTls_SimShutdownWakes(t *testing.T) {
	src := tlsPkiFixture(t, tlsShutBlocks)
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			root := t.TempDir()
			stdDir := tlsStdDir(t, root, map[string]string{"tls/tests_tlsshut.mk": src})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", fmt.Sprint(c))
			out, status := mkCmd(t, root, stdDir, "test", "--std=std.tls")
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if status != 0 || strings.Count(out, "PASS std.tls") != 2+tlsDiskBlocks(t) {
				t.Fatalf("status %d\n%s", status, out)
			}
		})
	}
}
