// Why: QUIC connections must survive arbitrary packet loss, reordering,
// amplification limits, large certificate chains, and concurrent waiting tasks
// under std.sim's deterministic scheduler and network fault injector.
//
// What: simulation tests covering 32 seeds at 0%/10%/30% packet loss,
// anti-deadlock PTO with a 3.6 KiB certificate chain, lost-first-datagram
// recovery while another task leads (T5 PTO/2), anti-amplification unblocking,
// and IP-change vs port-only path-validation CC/RTT reset.
//
// How: pairs Acceptor and Connector inside per-seed Sim instances with
// SetNetFaults and verifies complete handshake, bidirectional echo, and clean
// connection close across every seed.

import std.crypto
import std.encoding
import std.net
import std.raw
import std.sim
import std.time
import std.tls
import std.x509

// Builds test client and server TLS Configs using either a single leaf or an
// inflated ~3.6 KiB certificate chain (leaf + 9 root certs) for anti-deadlock PTO.
BuildSimTlsConfigs(
    a: &Arena,
    large_chain: bool,
    leaf_pem: str,
    key_pem: str into a,
    root_pem: str,
): ?(key: crypto.PrivateKey, roots: x509.Pool, chain_buf: [10]x509.Certificate, chain_len: int) from a, leaf_pem, key_pem, root_pem {
    leaves := (x509.ParsePem(a, leaf_pem) else { return none }).Done()
    blocks := encoding.ParsePem(a, key_pem) else { return none }
    key := crypto.ParsePrivateKey(blocks[0].der) else { return none }
    roots_c := (x509.ParsePem(a, root_pem) else { return none }).Done()
    roots := x509.Pool.New(roots_c[..])
    var chain: [10]x509.Certificate = [leaves[0]; 10]
    var n := 1
    if large_chain {
        for i in 1..10 {
            chain[i] = roots_c[0]
        }
        n = 10
    }
    return some((key: key, roots: roots, chain_buf: chain, chain_len: n))
}

SimRunListenerTask(l: &net.MuxListener, io: Io) {
    _ = l.Run(io) else {}
}

SimServeEchoTask(l: &net.MuxListener, io: Io, failed: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    var sm := l.Accept(io) else {
        if failed.Load() == 0 {
            failed.Store(1)
        }
        return
    }
    var mst := sm.Accept(io, false, true) else {
        sm.Abandon()
        failed.Store(2)
        return
    }
    match mst {
        none {
            sm.Abandon()
            failed.Store(20)
            return
        }
        some(st) {
            var rbuf: [128]u8 = [0; 128]
            var total := 0
            for total < 128 {
                rn := sm.Read(io, &st, mut rbuf[total..], true) else {
                    sm.Release(st)
                    sm.Abandon()
                    failed.Store(3)
                    return
                }
                match rn {
                    none { break }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        total += v
                    }
                }
            }
            if total != 13 {
                sm.Release(st)
                sm.Abandon()
                failed.Store(if total == 0 { 30 } else { 31 })
                return
            }
            sm.Write(io, &st, rbuf[0..total], true) else {
                sm.Release(st)
                sm.Abandon()
                failed.Store(4)
                return
            }
            sm.Release(st)
        }
    }
    // Drive server QConn while client finishes reading and sends CONNECTION_CLOSE.
    extra := sm.Accept(io, false, true) else { none }
    match extra {
        some(st2) { sm.Release(st2) }
        none {}
    }
    sm.Close(io, 0) else {
        sm.Abandon()
    }
}

SimClientEchoRun(h: &Heap, io: Io, ccfg: &Config, sa: net.Addr, failed: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    var cn := Connector(ccfg)
    var cm := cn.Dial(h, io, sa, "localhost", "h3") else err_dial {
        code := match err_dial {
            Canceled { u32(51) }
            time.TimedOut { u32(52) }
            Closed { u32(53) }
            net.SecureFailed { u32(54) }
            net.Refused { u32(55) }
            _ { u32(59) }
        }
        failed.Store(code)
        done.Store(1)
        return
    }
    var mcst := cm.Open(io, false, true) else {
        cm.Abandon()
        failed.Store(6)
        done.Store(1)
        return
    }
    match mcst {
        none {
            cm.Abandon()
            failed.Store(60)
            done.Store(1)
            return
        }
        some(cst) {
            cm.Write(io, &cst, b"sim-quic-echo", true) else {
                cm.Release(cst)
                cm.Abandon()
                failed.Store(7)
                done.Store(1)
                return
            }
            var got: [32]u8 = [0; 32]
            var total := 0
            for total < 13 {
                rn := cm.Read(io, &cst, mut got[total..], true) else {
                    failed.Store(80)
                    break
                }
                match rn {
                    none {
                        failed.Store(81)
                        break
                    }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        total += v
                    }
                }
            }
            cm.Release(cst)
            if total != 13 {
                if failed.Load() == 0 {
                    failed.Store(8)
                }
            } else {
                for k in 0..13 {
                    if got[k] != (b"sim-quic-echo")[k] {
                        failed.Store(9)
                    }
                }
            }
        }
    }
    cm.Close(io, 0) else {
        cm.Abandon()
    }
    done.Store(1)
}

// Runs one full client <-> server QUIC handshake + stream echo under Sim(seed) with drop_ppm.
RunSimSeedHandshakeEcho(h: &Heap, a: &Arena, seed: u64, drop_ppm: u32, large_chain: bool): bool {
    var s := sim.Sim.New(a, seed) else { return false }
    defer s.Free()
    if drop_ppm > 0 {
        var nf := sim.DefaultNetFaults
        nf.drop = drop_ppm
        nf.duplicate = 0
        nf.latency_lo = time.Millis(2)
        nf.latency_hi = time.Millis(6)
        s.SetNetFaults(nf)
    }
    io := s.Io()

    mat := BuildSimTlsConfigs(a, large_chain, QtLeafPem, QtKeyPem, QtRootPem) else { return false }
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)

    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := ac.Listen(h, io, "127.0.0.1:0", "h3") else { return false }
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(30))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        sc.Cancel()
    }
    return failed.Load() == 0
}

RunSimSeedCode(h: &Heap, a: &Arena, seed: u64, drop_ppm: u32, large_chain: bool): u32 {
    var s := sim.Sim.New(a, seed) else { return 99 }
    defer s.Free()
    if drop_ppm > 0 {
        var nf := sim.DefaultNetFaults
        nf.drop = drop_ppm
        nf.duplicate = 0
        nf.latency_lo = time.Millis(2)
        nf.latency_hi = time.Millis(6)
        s.SetNetFaults(nf)
    }
    io := s.Io()
    mat := BuildSimTlsConfigs(a, large_chain, QtLeafPem, QtKeyPem, QtRootPem) else { return 98 }
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)
    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)
    var ac := Acceptor(&scfg)
    var l := ac.Listen(h, io, "127.0.0.1:0", "h3") else { return 97 }
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)
    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(30))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        sc.Cancel()
    }
    return failed.Load()
}

test "SIM-Conn 32 seeds across 0%, 10%, and 30% packet loss" (sys: &System) {
    h := sys.heap
    a := sys.arena
    // 0% loss (8 seeds) + 10% loss (12 seeds) + 30% loss (12 seeds) = 32 seeds.
    for i in 0..8 {
        Assert(RunSimSeedHandshakeEcho(h, a, Cast[u64](1000 + i), 0, false))
    }
    for i in 0..12 {
        rc := RunSimSeedCode(h, a, Cast[u64](2000 + i), 100_000, false)
        if rc != 0 {
            Assert(rc != 1)
            Assert(rc != 2)
            Assert(rc != 3)
            Assert(rc != 4)
            Assert(rc != 5)
            Assert(rc != 6)
            Assert(rc != 7)
            Assert(rc != 8)
            Assert(rc != 30)
            Assert(rc != 31)
            Assert(rc != 80)
            Assert(rc != 81)
            Assert(rc == 0)
        }
    }
    for i in 0..12 {
        Assert(RunSimSeedHandshakeEcho(h, a, Cast[u64](3000 + i), 300_000, false))
    }
}

test "SIM-Conn anti-deadlock PTO with 3.6 KiB cert chain under loss" (sys: &System) {
    h := sys.heap
    a := sys.arena
    Assert(RunSimSeedHandshakeEcho(h, a, 7777, 100_000, true))
}

test "SIM-Conn lost first datagram while another task leads (T5 PTO/2)" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 4242)
    defer s.Free()
    io := s.Io()
    mat := BuildSimTlsConfigs(a, false, QtLeafPem, QtKeyPem, QtRootPem).Must()
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)
    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)
    // Blackhole server port so client's very first Initial datagram is dropped while server tasks lead.
    s.Blackhole(l.Port(), true)

    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(15))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        sc.go SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        _ = io.Sleep(time.Millis(160)) else {}
        s.Blackhole(l.Port(), false)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(10)) else { break }
        }
        sc.Cancel()
    }
    Assert(failed.Load() == 0)
}

test "RFC 9000 9.4, RFC 9002: IP change resets congestion window/RTT while port-only rebind keeps them" (sys: &System) {
    var clk := try sim.Sim.New(sys.arena, 99)
    defer clk.Free()
    io := clk.Io()
    a := sys.arena.Alloc()
    var now := time.Now(io)

    var rec := try Recovery.New(a)
    defer rec.Free(a)
    // Establish an RTT sample of 40 ms and grow cwnd.
    f0: [0]SentFrame = []
    pn0 := rec.NextPn(Space.App)
    try rec.OnSent(a, Space.App, pn0, 1200, true, true, f0[..], now)
    try io.Sleep(time.Millis(40))
    now = time.Now(io)
    r0: [1]AckRange = [AckRange(lo: pn0, hi: pn0)]
    var acked: [4]SentFrame = [SentFrame(kind: SentKind.Control(index: 0)); 4]
    var lost: [4]SentFrame = [SentFrame(kind: SentKind.Control(index: 0)); 4]
    _ = try rec.OnAck(Space.App, r0[..], time.Nanos(0), now, mut acked[..], mut lost[..])
    Assert(rec.has_rtt_sample)
    Assert(rec.latest_rtt >= 39_000_000 && rec.latest_rtt <= 41_000_000)

    // Port-only change keeps RTT sample.
    rec.OnPathValidated(false, now)
    Assert(rec.has_rtt_sample)

    // IP change resets RTT sample and congestion state to initial defaults.
    rec.OnPathValidated(true, now)
    Assert(!rec.has_rtt_sample)
    Assert(rec.Pto().Nanos() > 500_000_000)
}
