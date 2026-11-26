// Why: std.tls's calls must keep their cancel-safety classes under any cut:
// Conn.Read repeatable, Client and Server prefix, a cut WriteAll failing the
// conn.
// What: TestStdTls_SimCancel and TestStdTls_SimCancelLinux, which run the
// blocks of tls/tests_tlssim.mk at 1 and 16 carriers.
// How: under std.sim a task cancels at every 250 us of a story or every point
// of a measured handshake; the loopback benchmark's certificates are reused.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const tlsSimBlocks = `import std.crypto
import std.encoding
import std.net
import std.sim
import std.time
import std.x509

const SimAddrA = "127.0.0.1:7701"
const SimAddrB = "127.0.0.1:7702"

const SimLeaf: str = "@LEAF@"
const SimKey: str = "@KEY@"
const SimRoot: str = "@ROOT@"

// The story the server sends: StoryRecords records of StoryRecord bytes.
const StoryRecords: int = 10
const StoryRecord: int = 1000

struct SimPki {
    chain: []x509.Certificate
    key: crypto.PrivateKey
    pool: x509.Pool
}

LoadSimPki(a: &Arena): x509.Error|encoding.Error|crypto.Error|OutOfMemory!SimPki from a {
    certs := try x509.ParsePem(a, SimLeaf)
    blocks := try encoding.ParsePem(a, SimKey)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    roots := try x509.ParsePem(a, SimRoot)
    chain := certs.Done()
    rs := roots.Done()
    return SimPki(chain: chain[..], key: key, pool: x509.Pool.New(rs[..]))
}

// SimPattern is byte i of the story.
SimPattern(i: int): u8 {
    return Trunc[u8](i * 31 + (i >> 8))
}

// SimLoop is a connected loopback pair at addr: the dialled end and the
// accepted one.
SimLoop(io: Io, addr: str): net.Error!(c: net.Conn, s: net.Conn) {
    l := try net.Listen(io, addr)
    defer l.Close()
    c := try net.Dial(io, addr)
    s := l.Accept(io) else e {
        c.Close()
        fail e
    }
    return (c: c.Conn(), s: s.Conn())
}

// SimCancelAt cancels s after nap ns of simulated time.
SimCancelAt(s: &Scope, io: Io, nap: i64) {
    io.Sleep(time.Nanos(nap)) else {
        return
    }
    s.Cancel()
}

// Forward copies src to dst until src ends, in pieces of at most piece
// bytes gap ns apart (gap 0: as read), then shuts dst's write side.
Forward(io: Io, src: &net.Conn, dst: &net.Conn, piece: int, gap: i64) {
    var buf: [4096]u8 = [0; 4096]
    for {
        k := src.Read(io, mut buf[0..piece]) else {
            break
        }
        if k == 0 {
            break
        }
        dst.WriteAll(io, buf[0..k]) else {
            break
        }
        if gap > 0 {
            io.Sleep(time.Nanos(gap)) else {
                break
            }
        }
    }
    dst.Shutdown(io, net.Shutdown.Write) else {}
}

// StoryServer accepts one connection on l, sends the story and waits for
// the client's close_notify; code is 0, or what failed.
StoryServer(h: &Heap, io: Io, l: &net.Listener, cfg: &Config, code: mut int) {
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
    var rec: [1000]u8 = [0; 1000]
    for r in 0..StoryRecords {
        for i in 0..StoryRecord {
            rec[i] = SimPattern(r * StoryRecord + i)
        }
        c.WriteAll(io, rec[..]) else {
            *code = 3
            c.Abandon()
            return
        }
    }
    var b: [16]u8 = [0; 16]
    for {
        k := c.Read(io, mut b[..]) else {
            *code = 4
            c.Abandon()
            return
        }
        if k == 0 {
            break
        }
    }
    c.Close(io) else {
        c.Abandon()
    }
}

// ReadStory reads the story from c under a scope a task cancels at nap
// ns, then reads the rest after the scope (the server waits for the
// client's close_notify, so the story ends by its length): the bytes read, the interruptions, and
// whether every byte was the story's.
ReadStory(c: &Conn, io: Io, nap: i64): (got: int, cuts: int, same: bool) {
    var buf: [700]u8 = [0; 700]
    var got := 0
    var cuts := 0
    var same := true
    var done := false
    scope r := io.Scope() {
        r.go SimCancelAt(&r, io, nap)
        for {
            k := c.Read(io, mut buf[..]) else e {
                match e {
                    Canceled { cuts += 1 }
                    _ { same = false }
                }
                break
            }
            if k == 0 {
                same = false
                break
            }
            for i in 0..k {
                if buf[i] != SimPattern(got + i) {
                    same = false
                }
            }
            got += k
            if got == StoryRecords * StoryRecord {
                done = true
                r.Cancel()
                break
            }
        }
    }
    for !done && same && got < StoryRecords * StoryRecord {
        k := c.Read(io, mut buf[..]) else {
            same = false
            break
        }
        if k == 0 {
            same = false
            break
        }
        for i in 0..k {
            if buf[i] != SimPattern(got + i) {
                same = false
            }
        }
        got += k
    }
    return (got: got, cuts: cuts, same: same)
}

// RunCutRead is CutRead over an arena of its own.
RunCutRead(h: &Heap, p: &SimPki, nap: i64): Error|OutOfMemory!(got: int, cuts: int, same: bool, code: int) {
    var ar := try Arena.New(h)
    defer ar.Free()
    return try CutRead(h, &ar, p, 7, nap)
}

// RunCutHs is CutHandshake over an arena of its own.
RunCutHs(h: &Heap, p: &SimPki, side: int, nap: i64): Error|OutOfMemory!(client: int, server: int, took: i64) {
    var ar := try Arena.New(h)
    defer ar.Free()
    return try CutHandshake(h, &ar, p, 11, side, nap)
}

// CutRead is one run of the Read sweep in a fresh Sim of seed: the client
// reads the story through a forwarder in 97-byte pieces 100 us apart,
// cancelled nap ns after its handshake.
CutRead(h: &Heap, a: &Arena, p: &SimPki, seed: u64, nap: i64): Error|OutOfMemory!(got: int, cuts: int, same: bool, code: int) {
    var sm := try sim.Sim.New(a, seed)
    defer sm.Free()
    io := sm.Io()
    ids: [1]Identity = [Identity(chain: p.chain, key: &p.key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    var ccfg := DefaultConfig
    ccfg.roots = some(&p.pool)
    lb := try net.Listen(io, SimAddrB)
    defer lb.Close()
    (c: c1, s: s1) := try SimLoop(io, SimAddrA)
    up := net.Dial(io, SimAddrB) else e {
        c1.Abandon()
        s1.Abandon()
        fail e
    }
    c2 := up.Conn()
    var code := 0
    var res := (got: 0, cuts: 0, same: false)
    scope outer := io.Scope() {
        outer.go StoryServer(h, io, &lb, &scfg, mut code)
        outer.go Forward(io, &s1, &c2, 4096, 0)
        outer.go Forward(io, &c2, &s1, 97, 100_000)
        res = ClientStory(h, io, c1, &ccfg, nap)
    }
    s1.Abandon()
    c2.Abandon()
    if res.got < 0 {
        code = 100
    }
    return (got: res.got, cuts: res.cuts, same: res.same, code: code)
}

// ClientStory makes the client's handshake over conn and reads the story
// (ReadStory); got is -1 when the handshake failed.
ClientStory(h: &Heap, io: Io, conn: net.Conn, cfg: &Config, nap: i64): (got: int, cuts: int, same: bool) {
    cl := Client(h, io, conn, cfg, "localhost") else {
        conn.Abandon()
        return (got: -1, cuts: 0, same: false)
    }
    res := ReadStory(&cl, io, nap)
    cl.Close(io) else {
        cl.Abandon()
    }
    return res
}

test "tls.Conn.Read is repeatable: cancelled at every 250 us of a story that arrives in 97-byte pieces, the reads before and after the cut deliver it whole and in order" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    total := StoryRecords * StoryRecord
    var cut_runs := 0
    var nap: i64 = 0
    for nap <= 16_000_000 {
        r := try RunCutRead(sys.heap, &p, nap)
        Assert(r.code == 0)
        Assert(r.same)
        Assert(r.got == total)
        Assert(r.cuts <= 1)
        cut_runs += r.cuts
        nap += 250_000
    }
    // The story takes about 11 ms in pieces: most cuts land while a Read
    // waits.
    Assert(cut_runs >= 30)
}

// HsServer accepts one connection and makes the server's handshake under
// a scope cancelled at nap ns when cut is true; code: 0 done, 300
// Canceled, 400 another error.
HsServer(h: &Heap, io: Io, l: &net.Listener, cfg: &Config, cut: bool, nap: i64, code: mut int) {
    s := l.Accept(io) else {
        *code = 1
        return
    }
    conn := s.Conn()
    scope r := io.Scope() {
        if cut {
            r.go SimCancelAt(&r, io, nap)
        }
        c := Server(h, io, conn, cfg) else e {
            conn.Abandon()
            *code = match e {
                Canceled { 300 }
                _ { 400 }
            }
            r.Cancel()
            return
        }
        r.Cancel()
        var b: [16]u8 = [0; 16]
        _ = c.Read(io, mut b[..]) else {
            0
        }
        c.Close(io) else {
            c.Abandon()
        }
    }
}

// CutHandshake is one run of the handshake sweep in a fresh Sim of seed:
// the client's handshake (side 0) or the server's (side 1) under a scope
// cancelled at nap ns, or neither when nap < 0. The client's code, the
// server's, and the handshake's length in simulated ns.
CutHandshake(h: &Heap, a: &Arena, p: &SimPki, seed: u64, side: int, nap: i64): Error|OutOfMemory!(client: int, server: int, took: i64) {
    var sm := try sim.Sim.New(a, seed)
    defer sm.Free()
    io := sm.Io()
    ids: [1]Identity = [Identity(chain: p.chain, key: &p.key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    var ccfg := DefaultConfig
    ccfg.roots = some(&p.pool)
    l := try net.Listen(io, SimAddrA)
    defer l.Close()
    var server := 0
    var client := 0
    var took: i64 = 0
    scope outer := io.Scope() {
        outer.go HsServer(h, io, &l, &scfg, side == 1 && nap >= 0, nap, mut server)
        stream := try net.Dial(io, SimAddrA)
        r := ClientHs(h, io, stream.Conn(), &ccfg, side == 0 && nap >= 0, nap)
        client = r.code
        took = r.took
    }
    return (client: client, server: server, took: took)
}

// ClientHs makes the client's handshake under a scope cancelled at nap ns
// when cut is true: 0 and the handshake's length, or 300 for Canceled and
// 400 for another error, the connection given back and abandoned.
ClientHs(h: &Heap, io: Io, conn: net.Conn, cfg: &Config, cut: bool, nap: i64): (code: int, took: i64) {
    start := time.Now(io)
    scope r := io.Scope() {
        if cut {
            r.go SimCancelAt(&r, io, nap)
        }
        c := Client(h, io, conn, cfg, "localhost") else e {
            // Prefix: the connection is the caller's again.
            conn.Abandon()
            code := match e {
                Canceled { 300 }
                _ { 400 }
            }
            r.Cancel()
            return (code: code, took: 0)
        }
        took := time.Now(io).Since(start).Nanos()
        r.Cancel()
        c.Close(io) else {
            c.Abandon()
        }
        return (code: 0, took: took)
    }
}

test "tls.Client and tls.Server are prefix: cancelled at every point of the handshake, each returns ok or Canceled with the connection the caller's, and the other side ends" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    whole := try RunCutHs(sys.heap, &p, 0, -1)
    Assert(whole.client == 0 && whole.server == 0)
    Assert(whole.took > 0)
    step := whole.took / 40 + 1
    for side in 0..2 {
        var cancelled := 0
        var nap: i64 = 0
        for nap <= whole.took + step {
            r := try RunCutHs(sys.heap, &p, side, nap)
            if side == 0 {
                Assert(r.client == 0 || r.client == 300)
                if r.client == 300 {
                    cancelled += 1
                    // The server's handshake fails or, if it finished
                    // first, its Read ends: either way it returned.
                    Assert(r.server != 1)
                }
            } else {
                Assert(r.server == 0 || r.server == 300)
                if r.server == 300 {
                    cancelled += 1
                    Assert(r.client == 0 || r.client == 400)
                }
            }
            nap += step
        }
        Assert(cancelled >= 5)
    }
}

// SlowServer makes the handshake, then reads 100 bytes a millisecond until
// the end or a failure.
SlowServer(h: &Heap, io: Io, l: &net.Listener, cfg: &Config) {
    s := l.Accept(io) else {
        return
    }
    conn := s.Conn()
    c := Server(h, io, conn, cfg) else {
        conn.Abandon()
        return
    }
    var b: [100]u8 = [0; 100]
    for {
        k := c.Read(io, mut b[..]) else {
            break
        }
        if k == 0 {
            break
        }
        io.Sleep(time.Millis(1)) else {
            break
        }
    }
    c.Abandon()
}

// ErrCode is 300 for Canceled, 500 for time.TimedOut, 400 otherwise.
ErrCode(e: &Error): int {
    return match *e {
        Canceled { 300 }
        time.TimedOut { 500 }
        _ { 400 }
    }
}

// Slow is a transport each of whose writes takes 1 ms of simulated time
// before it starts: std.sim's network buffers whatever is written, so
// this is what makes a WriteAll wait.
struct Slow {
    s: net.Conn
}

impl net.Transport for Slow {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return try self.s.Read(io, buf)
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        try io.Sleep(time.Millis(1))
        try self.s.WriteAll(io, data)
    }

    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        try self.s.Shutdown(io, how)
    }

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io): net.Error!void {
    }

    Abandon(self) {
        Slow(s) := self
        s.Abandon()
    }
}

// CutWrite is one run of the WriteAll case: a 1 MiB WriteAll over a Slow
// transport to a server that reads 100 bytes a millisecond, cancelled at
// nap ns, then Read,
// WriteAll and Close; the four ErrCodes, 0 for success.
CutWrite(h: &Heap, a: &Arena, p: &SimPki, data: []u8, nap: i64): Error|OutOfMemory![4]int {
    var sm := try sim.Sim.New(a, 13)
    defer sm.Free()
    io := sm.Io()
    ids: [1]Identity = [Identity(chain: p.chain, key: &p.key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    var ccfg := DefaultConfig
    ccfg.roots = some(&p.pool)
    l := try net.Listen(io, SimAddrA)
    defer l.Close()
    var codes: [4]int = [0; 4]
    scope outer := io.Scope() {
        outer.go SlowServer(h, io, &l, &scfg)
        stream := try net.Dial(io, SimAddrA)
        t := Slow(s: stream.Conn())
        conn := net.NewConn(h, t) else e {
            t.Abandon()
            fail e
        }
        codes = WriteThenAll(h, io, conn, &ccfg, data, nap)
    }
    return codes
}

// WriteThenAll is CutWrite's client: 400 in every slot when the
// handshake fails.
WriteThenAll(h: &Heap, io: Io, conn: net.Conn, cfg: &Config, data: []u8, nap: i64): [4]int {
    var codes: [4]int = [0; 4]
    c := Client(h, io, conn, cfg, "localhost") else {
        conn.Abandon()
        return [400; 4]
    }
    scope w := io.Scope() {
        w.go SimCancelAt(&w, io, nap)
        c.WriteAll(io, data) else e {
            codes[0] = ErrCode(&e)
        }
        w.Cancel()
    }
    var b: [4]u8 = [0; 4]
    _ = c.Read(io, mut b[..]) else e {
        codes[1] = ErrCode(&e)
        0
    }
    c.WriteAll(io, b[..]) else e {
        codes[2] = ErrCode(&e)
    }
    c.Close(io) else e {
        codes[3] = ErrCode(&e)
        c.Abandon()
        return codes
    }
    return codes
}

test "an interrupted WriteAll fails the connection: Read, WriteAll and Close then report Canceled" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    data := try sys.arena.Slice[u8](1 << 20)
    naps: [4]i64 = [500_000, 1_000_000, 5_500_000, 20_000_000]
    for nap in naps {
        var ar := try Arena.New(sys.heap)
        codes := CutWrite(sys.heap, &ar, &p, data[..], nap) else e {
            ar.Free()
            fail e
        }
        ar.Free()
        // 1 MiB is 64 records, each written 1 ms after the last: every
        // cut lands inside the WriteAll.
        Assert(codes[0] == 300 && codes[1] == 300 && codes[2] == 300 && codes[3] == 300)
    }
}

// Stall is a transport whose 24-byte writes, a protected alert record,
// never complete: a peer that stopped reading, met at close_notify.
struct Stall {
    s: net.Conn
}

impl net.Transport for Stall {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return try self.s.Read(io, buf)
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        if data.Len() == 24 {
            try io.Sleep(time.Hours(1))
        }
        try self.s.WriteAll(io, data)
    }

    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        try self.s.Shutdown(io, how)
    }

    PeerAddr(&self): ?net.Addr {
        return none
    }

    Close(&self, io: Io): net.Error!void {
    }

    Abandon(self) {
        Stall(s) := self
        s.Abandon()
    }
}

// CloseCase is one run of the Close case: how 0 under io, 1 under
// io.NoCancel(), 2 under a scope cancelled at 50 ms; Close's ErrCode and
// how long it took in simulated ns.
CloseCase(h: &Heap, a: &Arena, p: &SimPki, how: int): Error|OutOfMemory!(code: int, took: i64) {
    var sm := try sim.Sim.New(a, 17)
    defer sm.Free()
    io := sm.Io()
    ids: [1]Identity = [Identity(chain: p.chain, key: &p.key)]
    var scfg := DefaultConfig
    scfg.identities = ids[..]
    var ccfg := DefaultConfig
    ccfg.roots = some(&p.pool)
    ccfg.close = time.Millis(300)
    l := try net.Listen(io, SimAddrA)
    defer l.Close()
    var res: (code: int, took: i64) = (code: 0, took: 0)
    scope outer := io.Scope() {
        outer.go SlowServer(h, io, &l, &scfg)
        stream := try net.Dial(io, SimAddrA)
        t := Stall(s: stream.Conn())
        conn := net.NewConn(h, t) else e {
            t.Abandon()
            fail e
        }
        res = StallClose(h, io, conn, &ccfg, how)
    }
    return res
}

// StallClose is CloseCase's client.
StallClose(h: &Heap, io: Io, conn: net.Conn, cfg: &Config, how: int): (code: int, took: i64) {
    c := Client(h, io, conn, cfg, "localhost") else {
        conn.Abandon()
        return (code: 400, took: 0)
    }
    start := time.Now(io)
    var code := 0
    var took: i64 = 0
    scope k := io.Scope() {
        if how == 2 {
            k.go SimCancelAt(&k, io, 50_000_000)
        }
        q := if how == 1 { io.NoCancel() } else { io }
        c.Close(q) else e {
            code = ErrCode(&e)
            took = time.Now(io).Since(start).Nanos()
            c.Abandon()
        }
        k.Cancel()
    }
    return (code: code, took: took)
}

test "Close over a peer that stopped reading fails with time.TimedOut at the close limit, also under NoCancel, and with Canceled under a scope cancelled first; the Conn is then the caller's to Abandon" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    for how in 0..3 {
        var ar := try Arena.New(sys.heap)
        r := CloseCase(sys.heap, &ar, &p, how) else e {
            ar.Free()
            fail e
        }
        ar.Free()
        if how == 2 {
            Assert(r.code == 300)
            Assert(r.took >= 50_000_000 && r.took < 60_000_000)
        } else {
            Assert(r.code == 500)
            Assert(r.took >= 300_000_000 && r.took < 310_000_000)
        }
    }
}
`

// tlsSimFixture is tlsSimBlocks with the bench row's certificates and key.
func tlsSimFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tests", "bench-lanes", "std-tls", "loopback", "mk", "tls-loopback.mk"))
	if err != nil {
		t.Fatal(err)
	}
	src := tlsSimBlocks
	for _, name := range []string{"Leaf", "Key", "Root"} {
		m := regexp.MustCompile(`(?m)^const ` + name + `Pem: str = (".*")$`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("tls-loopback.mk has no %sPem", name)
		}
		src = strings.Replace(src, `"@`+strings.ToUpper(name)+`@"`, string(m[1]), 1)
	}
	return src
}

// TestStdTls_SimCancel runs the cancel-safety blocks at 1 and 16 carriers.
func TestStdTls_SimCancel(t *testing.T) {
	src := tlsSimFixture(t)
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			root := t.TempDir()
			stdDir := tlsStdDir(t, root, map[string]string{"tls/tests_tlssim.mk": src})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", fmt.Sprint(c))
			out, status := mkCmd(t, root, stdDir, "test", "--std=std.tls")
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if status != 0 || strings.Count(out, "PASS std.tls") != 4+tlsDiskBlocks(t) {
				t.Fatalf("status %d\n%s", status, out)
			}
		})
	}
}

// TestStdTls_SimCancelLinux runs the cancel-safety blocks on x86_64-linux at 1 and
// 16 carriers through mk test (MK_CARRIERS reaches the VM through
// WSLENV). MK_LINUX=1, under tools/wsl-lock.ps1 with go test -p 1.
func TestStdTls_SimCancelLinux(t *testing.T) {
	linuxGate(t)
	rtsNeedWSL(t)
	src := tlsSimFixture(t)
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			root := t.TempDir()
			stdDir := tlsStdDir(t, root, map[string]string{"tls/tests_tlssim.mk": src})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", fmt.Sprint(c))
			wslenv := "MK_CARRIERS"
			if old := os.Getenv("WSLENV"); old != "" {
				wslenv = old + ":" + wslenv
			}
			t.Setenv("WSLENV", wslenv)
			out, status := mkCmd(t, root, stdDir, "test", "--std=std.tls", "--target", "x86_64-linux")
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if status != 0 || strings.Count(out, "PASS std.tls") != 4+tlsDiskBlocks(t) {
				t.Fatalf("status %d\n%s", status, out)
			}
			t.Logf("%s", out[max(0, len(out)-300):])
		})
	}
}
