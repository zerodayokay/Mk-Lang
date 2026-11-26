// Why: the TLS 1.2 client must be cancel-safe too, and std.tls has no TLS 1.2
// server to test it against.
// What: TestStdTls_Tls12SimCancel and the blocks of tls/tests_tls12sim.mk,
// which hold a test-only Server12 (ECDHE-ECDSA, AES-128-GCM, X25519, EMS).
// How: under std.sim at 1 and 16 carriers the handshake is cut at every 1/40
// and a chunked read every 250 us; each ends ok or Canceled, the data whole.
// Its tests are cancel-safety tests; tools/gate/safety.go lists each.

package std

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const tls12SimBlocks = `import std.crypto
import std.net
import std.sim
import std.time

// Server12 makes conn a TLS 1.2 connection as a server, for these blocks
// only: TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 over X25519 with the
// extended master secret, signed with p's P-256 key.
Server12(h: &Heap, io: Io, conn: net.Conn, p: &SimPki): Error!Conn from h, conn {
    var b := try Box[Halves].New(h.Alloc(), Halves.New(time.Seconds(5)))
    Flights12(h, io, &conn, p, b.Ref()) else e {
        FreeHalves(b, h)
        fail e
    }
    return Conn(conn: conn, heap: h, box: b)
}

// Flights12 runs ServerFlights12 on hv's halves, both locked, on an arena
// of h.
Flights12(h: &Heap, io: Io, conn: &net.Conn, p: &SimPki, hv: &Halves): Error!void {
    var a := try Arena.New(h)
    defer a.Free()
    var gr := try hv.rd.Lock(io)
    var gw := hv.wr.Lock(io) else e {
        gr.Unlock()
        fail e
    }
    ServerFlights12(&a, io, conn, p, gr.Mut(), gw.Mut()) else e {
        gw.Unlock()
        gr.Unlock()
        fail e
    }
    gw.Unlock()
    gr.Unlock()
}

// ServerFlights12 is the server's side of RFC 5246 7.3: ClientHello;
// ServerHello (extended_master_secret, an empty renegotiation_info,
// uncompressed points), Certificate, ServerKeyExchange and
// ServerHelloDone in one record; the client's ClientKeyExchange,
// ChangeCipherSpec and Finished; its own ChangeCipherSpec and Finished.
ServerFlights12(a: &Arena, io: Io, conn: &net.Conn, p: &SimPki, rd: mut ReadHalf, wr: mut WriteHalf): Error!void {
    var hs := try Hs.New(a, true)
    rd.server = true
    kind := crypto.HashKind.Sha256
    var rw := RecordWire(conn: conn, rd: mut *rd, wr: mut *wr)
    n0 := try hs.Expect(mut rw, io, HsClientHello)
    hs.hello_due = false
    ch := try ParseClientHello(hs.buf[4..n0])
    hs.transcript.Choose(kind)
    hs.transcript.Add(hs.buf[0..n0])
    var client_random: [32]u8 = [0; 32]
    (mut client_random[..]).CopyFrom(ch.random)
    var sid: [32]u8 = [0; 32]
    sid_len := ch.legacy_id.Len()
    (mut sid[0..sid_len]).CopyFrom(ch.legacy_id)
    hs.Drop(n0)
    var server_random: [32]u8 = [0; 32]
    try crypto.Random(io, mut server_random[..])
    // Never RFC 8446 4.1.3's downgrade sentinel.
    server_random[24] = 0
    var ext: [16]u8 = [0; 16]
    var ew := Out.New(mut ext[..])
    ew.U16(ExtExtendedMasterSecret)
    ew.U16(0)
    ew.U16(ExtRenegotiationInfo)
    ew.U16(1)
    ew.U8(0)
    ew.U16(ExtPointFormats)
    ew.U16(2)
    ew.U8(1)
    ew.U8(0)
    sh := ServerHello(version: WireTls12, random: server_random[..], legacy_id: sid[0..sid_len], suite: 0xc02b, compression: 0,
        has_exts: true, exts: ew.b[0..ew.at])
    var size := 1024
    for x in p.chain {
        size += 3 + x.Der().Len()
    }
    flight := try a.Slice[u8](size)
    list := try a.Slice[u8](size)
    var lw := Out.New(list)
    for x in p.chain {
        lw.Vec24(x.Der())
    }
    var w := Out.New(flight)
    WriteServerHello(mut w, &sh)
    WriteCertificate12(mut w, lw.b[0..lw.at])
    var shares := Shares(x25519: [0; 32], x25519_pub: [0; 32], p256: none, p256_pub: [0; 65])
    defer shares.Wipe()
    try crypto.Random(io, mut shares.x25519[..])
    shares.x25519_pub = crypto.X25519(&shares.x25519, &X25519Base)
    var params: [36]u8 = [0; 36]
    params[0] = CurveNamed
    params[1] = 0
    params[2] = 0x1d
    params[3] = 32
    (mut params[4..36]).CopyFrom(shares.x25519_pub[..])
    var content: [100]u8 = [0; 100]
    (mut content[0..32]).CopyFrom(client_random[..])
    (mut content[32..64]).CopyFrom(server_random[..])
    (mut content[64..100]).CopyFrom(params[..])
    var alert: u8 = 0
    var sig: [80]u8 = [0; 80]
    sl := try Sign(mut alert, &p.key, content[..], mut sig[..])
    ske := ServerKeyExchange(curve: GroupX25519, point: shares.x25519_pub[..], params: params[..], scheme: SchemeEcdsaP256,
        signature: sig[0..sl])
    WriteServerKeyExchange(mut w, &ske)
    WriteServerHelloDone(mut w)
    if w.full || lw.full {
        fail Protocol
    }
    hs.transcript.Add(w.b[0..w.at])
    try wr.Send(conn, io, TypeHandshake, w.b[0..w.at])
    n1 := try hs.Expect12(rd, conn, io, HsClientKeyExchange)
    point := try ParseClientKeyExchange(hs.buf[4..n1])
    var keys := Keys12(pre: [0; 32], master: [0; 48], block: [0; 88])
    defer keys.Wipe()
    try SharedSecret(mut alert, &shares, GroupX25519, point, mut keys.pre[..])
    hs.transcript.Add(hs.buf[0..n1])
    hs.Drop(n1)
    var th: [48]u8 = [0; 48]
    hs.transcript.Sum(kind, mut th[..])
    Prf12(kind, keys.pre[..], "extended master secret", th[0..32], mut keys.master[..])
    var seed: [64]u8 = [0; 64]
    (mut seed[0..32]).CopyFrom(server_random[..])
    (mut seed[32..64]).CopyFrom(client_random[..])
    Prf12(kind, keys.master[..], "key expansion", seed[..], mut keys.block[0..40])
    try hs.Ccs12(rd, conn, io)
    rd.aead.Wipe()
    rd.aead = Aead.New12(Suite.Aes128Gcm, keys.block[0..16], keys.block[32..36])
    hs.transcript.Sum(kind, mut th[..])
    var want: [12]u8 = [0; 12]
    Prf12(kind, keys.master[..], "client finished", th[0..32], mut want[..])
    n2 := try hs.Expect12(rd, conn, io, HsFinished)
    got := try ParseFinished(hs.buf[4..n2], 12)
    if !crypto.Equal(got, want[..]) {
        fail Protocol
    }
    hs.transcript.Add(hs.buf[0..n2])
    hs.Drop(n2)
    try wr.Send(conn, io, TypeChangeCipherSpec, Ccs[..])
    wr.aead.Wipe()
    wr.aead = Aead.New12(Suite.Aes128Gcm, keys.block[16..32], keys.block[36..40])
    hs.transcript.Sum(kind, mut th[..])
    var fin: [12]u8 = [0; 12]
    Prf12(kind, keys.master[..], "server finished", th[0..32], mut fin[..])
    var fm: [16]u8 = [0; 16]
    var fw := Out.New(mut fm[..])
    WriteFinished(mut fw, fin[..])
    try wr.Send(conn, io, TypeHandshake, fw.b[0..fw.at])
}

// Hs12Server accepts one connection and makes Server12's handshake, then
// reads to the end; code 0, or what failed.
Hs12Server(h: &Heap, io: Io, l: &net.Listener, p: &SimPki, code: mut int) {
    s := l.Accept(io) else {
        *code = 1
        return
    }
    conn := s.Conn()
    c := Server12(h, io, conn, p) else {
        conn.Abandon()
        *code = 2
        return
    }
    var b: [16]u8 = [0; 16]
    _ = c.Read(io, mut b[..]) else {
        0
    }
    c.Close(io) else {
        c.Abandon()
    }
}

// Story12Server is StoryServer over Server12: the story, then the
// client's close_notify.
Story12Server(h: &Heap, io: Io, l: &net.Listener, p: &SimPki, code: mut int) {
    s := l.Accept(io) else {
        *code = 1
        return
    }
    conn := s.Conn()
    c := Server12(h, io, conn, p) else {
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

// Config12 is a client configuration with the TLS 1.2 client on, trusting
// p's root.
Config12(p: &SimPki): Config from p {
    var c := DefaultConfig
    c.roots = some(&p.pool)
    c.min_version = Version.Tls12
    return c
}

// CutHs12 is one run of the TLS 1.2 handshake sweep in a fresh Sim of
// seed: the client's handshake under a scope cancelled at nap ns (never
// when nap < 0); the client's code and the handshake's length.
CutHs12(h: &Heap, a: &Arena, p: &SimPki, seed: u64, nap: i64): Error|OutOfMemory!(client: int, server: int, took: i64) {
    var sm := try sim.Sim.New(a, seed)
    defer sm.Free()
    io := sm.Io()
    ccfg := Config12(p)
    l := try net.Listen(io, SimAddrA)
    defer l.Close()
    var server := 0
    var res := (code: 0, took: i64(0))
    scope outer := io.Scope() {
        outer.go Hs12Server(h, io, &l, p, mut server)
        stream := try net.Dial(io, SimAddrA)
        res = ClientHs(h, io, stream.Conn(), &ccfg, nap >= 0, nap)
    }
    return (client: res.code, server: server, took: res.took)
}

// RunCutHs12 is CutHs12 over an arena of its own.
RunCutHs12(h: &Heap, p: &SimPki, nap: i64): Error|OutOfMemory!(client: int, server: int, took: i64) {
    var ar := try Arena.New(h)
    defer ar.Free()
    return try CutHs12(h, &ar, p, 19, nap)
}

test "TLS12-SIM tls.Client over TLS 1.2 is prefix: cancelled at every 1/40 of the handshake, it returns ok or Canceled with the connection the caller's, and the server ends" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    whole := try RunCutHs12(sys.heap, &p, -1)
    Assert(whole.client == 0 && whole.server == 0)
    Assert(whole.took > 0)
    step := whole.took / 40 + 1
    var cancelled := 0
    var nap: i64 = 0
    for nap <= whole.took + step {
        r := try RunCutHs12(sys.heap, &p, nap)
        Assert(r.client == 0 || r.client == 300)
        if r.client == 300 {
            cancelled += 1
            Assert(r.server != 1)
        }
        nap += step
    }
    Assert(cancelled >= 5)
}

// CutRead12 is CutRead over Server12.
CutRead12(h: &Heap, a: &Arena, p: &SimPki, seed: u64, nap: i64): Error|OutOfMemory!(got: int, cuts: int, same: bool, code: int) {
    var sm := try sim.Sim.New(a, seed)
    defer sm.Free()
    io := sm.Io()
    ccfg := Config12(p)
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
        outer.go Story12Server(h, io, &lb, p, mut code)
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

// RunCutRead12 is CutRead12 over an arena of its own.
RunCutRead12(h: &Heap, p: &SimPki, nap: i64): Error|OutOfMemory!(got: int, cuts: int, same: bool, code: int) {
    var ar := try Arena.New(h)
    defer ar.Free()
    return try CutRead12(h, &ar, p, 7, nap)
}

test "TLS12-SIM tls.Conn.Read over TLS 1.2 is repeatable: cancelled at every 250 us of a story that arrives in 97-byte pieces, the reads before and after the cut deliver it whole and in order" (sys: &System) {
    p := try LoadSimPki(sys.arena)
    total := StoryRecords * StoryRecord
    var cut_runs := 0
    var nap: i64 = 0
    for nap <= 16_000_000 {
        r := try RunCutRead12(sys.heap, &p, nap)
        Assert(r.code == 0)
        Assert(r.same)
        Assert(r.got == total)
        Assert(r.cuts <= 1)
        cut_runs += r.cuts
        nap += 250_000
    }
    Assert(cut_runs >= 30)
}
`

// TestStdTls_Tls12SimCancel runs the TLS 1.2 cancel-safety blocks under
// std.sim at 1 and 16 carriers.
func TestStdTls_Tls12SimCancel(t *testing.T) {
	src := tlsSimFixture(t)
	for _, c := range []int{1, 16} {
		t.Run(fmt.Sprintf("carriers=%d", c), func(t *testing.T) {
			root := t.TempDir()
			stdDir := tlsStdDir(t, root, map[string]string{"tls/tests_tlssim.mk": src, "tls/tests_tls12sim.mk": tls12SimBlocks})
			t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
			t.Setenv("MK_CARRIERS", fmt.Sprint(c))
			out, status := mkCmd(t, root, stdDir, "test", "--std=std.tls", "--run", "TLS12-SIM")
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if status != 0 || strings.Count(out, "PASS std.tls") != 2 {
				t.Fatalf("status %d\n%s", status, out)
			}
		})
	}
}
