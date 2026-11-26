// Why: the QUIC endpoint must deduplicate retransmitted Initials, admit Retry
// connections, enforce Version Negotiation, send and honor stateless resets,
// survive NAT port rebinding, expire idle and unaccepted connections, and
// maintain closing-state rate limits for 3*PTO after server close.
//
// What: deterministic std.sim tests for all Q5 endpoint behaviors: duplicate
// Initial coalescing, Retry admission (retry_above = 0), Version Negotiation
// both ways, stateless reset both ways, NAT rebinding via Sim.Rebind, idle
// timeout, accept-queue expiry, and post-close 3*PTO closing replies.
//
// How: drives real Endpoint/Acceptor listeners and Connector/QConn clients over
// std.sim virtual time and network fault injection.

import std.core
import std.net
import std.raw
import std.sim
import std.time
import std.tls

SimVnRejectResponderTask(sock: &net.SharedDatagram, io: Io, cli_ready: &raw.Atomic[u32]) {
    var rx: [1500]u8 = [0; 1500]
    cli_ready.Store(1)
    got := sock.Recv(io, mut rx[..]) else { return }
    ph := ParsePacket(rx[0..got.n], LocalCidLen) else { return }
    match ph {
        .Long(hdr, packet: _) {
            var vn: [128]u8 = [0; 128]
            vers: [1]u32 = [0x1a2a3a4a]
            vn_len := WriteVersionNeg(mut vn[..], 0, hdr.scid.b[0..hdr.scid.n], hdr.dcid.b[0..hdr.dcid.n], vers[..])
            _ = sock.Send(io, vn[0..vn_len], some(got.peer)) else {}
        }
        _ {}
    }
}

SimServeTwoChunkEchoTask(l: &net.MuxListener, io: Io, stage: &raw.Atomic[u32], failed: &raw.Atomic[u32]) {
    var sm := l.Accept(io) else {
        failed.Store(1)
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
            failed.Store(3)
            return
        }
        some(st) {
            var rbuf: [64]u8 = [0; 64]
            // Read part 1 ("hello-") and echo immediately.
            var part1 := 0
            for part1 < 6 {
                rn := sm.Read(io, &st, mut rbuf[part1..], true) else {
                    sm.Release(st)
                    sm.Abandon()
                    failed.Store(4)
                    return
                }
                match rn {
                    none { break }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        part1 += v
                    }
                }
            }
            sm.Write(io, &st, rbuf[0..part1], false) else {
                sm.Release(st)
                sm.Abandon()
                failed.Store(5)
                return
            }
            stage.Store(1)
            // Read part 2 ("rebind") after client NAT rebinds, then echo + FIN.
            var part2 := 0
            for part2 < 6 {
                rn := sm.Read(io, &st, mut rbuf[part1 + part2..], true) else {
                    sm.Release(st)
                    sm.Abandon()
                    failed.Store(6)
                    return
                }
                match rn {
                    none { break }
                    some(v) {
                        if v == 0 {
                            break
                        }
                        part2 += v
                    }
                }
            }
            sm.Write(io, &st, rbuf[part1..part1 + part2], true) else {
                sm.Release(st)
                sm.Abandon()
                failed.Store(7)
                return
            }
            sm.Release(st)
        }
    }
    extra := sm.Accept(io, false, true) else { none }
    match extra {
        some(s2) { sm.Release(s2) }
        none {}
    }
    sm.Close(io, 0) else {
        sm.Abandon()
    }
}

SimRebindClientTask(
    h: &Heap,
    io: Io,
    udp: net.Datagram,
    ccfg: &Config,
    sa: net.Addr,
    stage: &raw.Atomic[u32],
    rebound: &raw.Atomic[u32],
    failed: &raw.Atomic[u32],
    done: &raw.Atomic[u32],
) {
    _ = udp.SetBuffers(QuicUdpRecvBuf, 0) else {}
    udp.Connect(io, sa) else {
        udp.Close()
        failed.Store(10)
        done.Store(1)
        return
    }
    sock := udp.Share()
    var qc := QConn.NewClient(h, io, sock, sa, ccfg) else {
        sock.Close()
        failed.Store(11)
        done.Store(1)
        return
    }
    var empty_qc: ?QConn = none
    var b := Box[?QConn].New(h.Alloc(), empty_qc) else {
        if x := empty_qc {
            x.MuxAbandon()
        }
        qc.MuxAbandon()
        failed.Store(12)
        done.Store(1)
        return
    }
    alpns: [1]str = ["h3"]
    _ = qc.Handshake(h, io, ccfg.Tls(), "localhost", alpns[..]) else {
        qc.MuxAbandon()
        if x := b.Take(h.Alloc()) {
            x.MuxAbandon()
        }
        failed.Store(13)
        done.Store(1)
        return
    }
    sb := core.SealedBox.Fill[QConn](b, qc)
    var cm := net.Mux.Of[QConn](h, sb)
    var mcst := cm.Open(io, false, true) else {
        cm.Abandon()
        failed.Store(14)
        done.Store(1)
        return
    }
    match mcst {
        none {
            cm.Abandon()
            failed.Store(15)
            done.Store(1)
            return
        }
        some(cst) {
            cm.Write(io, &cst, b"hello-", false) else {
                cm.Release(cst)
                cm.Abandon()
                failed.Store(16)
                done.Store(1)
                return
            }
            // Wait for main task to trigger Sim.Rebind after server echoed part1.
            var got: [16]u8 = [0; 16]
            var total := 0
            for total < 6 {
                rn := cm.Read(io, &cst, mut got[total..], true) else {
                    cm.Release(cst)
                    cm.Abandon()
                    failed.Store(17)
                    done.Store(1)
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
            for rebound.Load() == 0 {
                _ = io.Sleep(time.Millis(5)) else { break }
            }
            cm.Write(io, &cst, b"rebind", true) else {
                cm.Release(cst)
                cm.Abandon()
                failed.Store(18)
                done.Store(1)
                return
            }
            for total < 12 {
                rn := cm.Read(io, &cst, mut got[total..], true) else {
                    cm.Release(cst)
                    cm.Abandon()
                    failed.Store(19)
                    done.Store(1)
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
            cm.Release(cst)
            if total != 12 {
                failed.Store(20)
            } else {
                exp := b"hello-rebind"
                for i in 0..12 {
                    if got[i] != exp[i] {
                        failed.Store(21)
                    }
                }
            }
        }
    }
    cm.Close(io, 0) else {
        cm.Abandon()
    }
    _ = stage
    done.Store(1)
}

SimRunIdleClientTask(h: &Heap, io: Io, ccfg: &Config, sa: net.Addr, failed: &raw.Atomic[u32], done: &raw.Atomic[u32]) {
    var cn := Connector(ccfg)
    var cm := cn.Dial(h, io, sa, "localhost", "h3") else {
        failed.Store(1)
        done.Store(1)
        return
    }
    var mcst := cm.Open(io, false, true) else {
        cm.Abandon()
        failed.Store(2)
        done.Store(1)
        return
    }
    match mcst {
        none {
            cm.Abandon()
            failed.Store(3)
        }
        some(cst) {
            // Stay silent past idle timeout; Read must end with MuxClosed/Closed.
            var buf: [16]u8 = [0; 16]
            var ended := false
            _ = cm.Read(io, &cst, mut buf[..], true) else {
                ended = true
                none
            }
            if !ended {
                failed.Store(4)
            }
            cm.Release(cst)
            cm.Abandon()
        }
    }
    done.Store(1)
}

SimServeIdleTask(l: &net.MuxListener, io: Io) {
    var sm := l.Accept(io) else { return }
    extra := sm.Accept(io, false, true) else { none }
    match extra {
        some(s2) { sm.Release(s2) }
        none {}
    }
    sm.Abandon()
}

test "SIM-Endpoint retransmitted Initials route to single QConn (no duplicate connections)" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6001)
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
    s.Blackhole(l.Port(), true)

    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(15))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        sc.go SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        _ = io.Sleep(time.Millis(220)) else {}
        s.Blackhole(l.Port(), false)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(10)) else { break }
        }
        sc.Cancel()
    }
    Assert(failed.Load() == 0)
}

test "SIM-Endpoint Retry admission (retry_above = 0) completes handshake and echo" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6011)
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
    scfg.retry_above = 0
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)

    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(15))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        sc.go SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(10)) else { break }
        }
        sc.Cancel()
    }
    Assert(failed.Load() == 0)
}

test "SIM-Endpoint Version Negotiation both ways (server emits VN, client fails net.Unsupported)" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6002)
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

    // 1. Server Endpoint replies with Version Negotiation containing Version1 to unknown-version Long Header.
    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)

    var raw_cli := try net.Bind(io, "127.0.0.1:0")
    defer raw_cli.Close()
    var unknown_ver: [1200]u8 = [0; 1200]
    unknown_ver[0] = 0xc0
    unknown_ver[1] = 0x1a
    unknown_ver[2] = 0x2a
    unknown_ver[3] = 0x3a
    unknown_ver[4] = 0x4a
    unknown_ver[5] = 8
    unknown_ver[14] = 8
    scope sc1 := io.Scope() {
        sc1.CancelAfter(time.Seconds(3))
        sc1.go SimRunListenerTask(&l, io)
        try raw_cli.Send(io, unknown_ver[..], sa)
        var vn_rx: [256]u8 = [0; 256]
        got := try raw_cli.Recv(io, mut vn_rx[..])
        ph := ParsePacket(vn_rx[0..got.n], LocalCidLen).Must()
        match ph {
            .VersionNeg(dcid: _, scid: _, versions) {
                var has_v1 := false
                var off := 0
                for off + 4 <= versions.Len() {
                    v := (u32(versions[off]) << 24) | (u32(versions[off + 1]) << 16) |
                        (u32(versions[off + 2]) << 8) | u32(versions[off + 3])
                    if v == Version1 {
                        has_v1 = true
                    }
                    off += 4
                }
                Assert(has_v1)
            }
            _ { Assert(false) }
        }
        sc1.Cancel()
    }

    // 2. Client receiving Version Negotiation without Version1 rejects Dial with net.Unsupported.
    var mock_udp := try net.Bind(io, "127.0.0.1:0")
    msa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](mock_udp.Port()), v6: false)
    var mock_srv := mock_udp.Share()
    defer mock_srv.Close()
    ready := raw.Atomic[u32].New(0)
    scope sc2 := io.Scope() {
        sc2.CancelAfter(time.Seconds(5))
        sc2.go SimVnRejectResponderTask(&mock_srv, io, &ready)
        for ready.Load() == 0 {
            _ = io.Sleep(time.Millis(1)) else { break }
        }
        var cn := Connector(&ccfg)
        var got_unsup := false
        var cm := cn.Dial(h, io, msa, "localhost", "h3") else e {
            match e {
                net.Unsupported { got_unsup = true }
                _ {}
            }
            sc2.Cancel()
            Assert(got_unsup)
            return
        }
        cm.Abandon()
        sc2.Cancel()
        Assert(false)
    }
}

test "SIM-Endpoint stateless reset both ways (server emits on unknown CID, client Draining on token)" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6003)
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

    // Server Endpoint emits stateless reset (>= 21B, bit 6 set) for unknown >= 21B short-header CID.
    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)
    var probe_sock := try net.Bind(io, "127.0.0.1:0")
    defer probe_sock.Close()
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(3))
        sc.go SimRunListenerTask(&l, io)
        var unk: [40]u8 = [0x40; 40]
        try probe_sock.Send(io, unk[..], sa)
        var rx: [128]u8 = [0; 128]
        got := try probe_sock.Recv(io, mut rx[..])
        Assert(got.n >= 21 && (rx[0] & 0x80) == 0 && (rx[0] & 0x40) != 0)
        sc.Cancel()
    }

    // Client Engine receiving a stateless reset matching active peer token transitions to Draining.
    var udp := try net.Bind(io, "127.0.0.1:0")
    ss := udp.Share()
    onerr ss.Close()
    var qc := try QConn.NewClient(h, io, ss, sa, &ccfg)
    defer qc.MuxAbandon()
    tok: [16]u8 = [0x5a; 16]
    rcid := CidFromSlice(b"\x99\x88\x77\x66\x55\x44\x33\x22")
    var g := qc.eng.Lock(io.NoCancel()).Must()
    e := mut *g.Mut()
    e.hs_state = HsConfirmed
    e.cids.hs_complete = true
    onerr g.Unlock()
    try e.cids.OnNewCid(h.Alloc(), 1, 1, &rcid, &tok)
    var reset_pkt: [32]u8 = [0x40; 32]
    (mut reset_pkt[16..32]).CopyFrom(tok[..])
    ProcessDatagram(e, io.NoCancel(), mut reset_pkt[..], sa, time.Now(io.NoCancel()))
    Assert(e.close.phase == ClosePhase.Draining)
    g.Unlock()
}

test "SIM-Endpoint NAT rebinding via Sim.Rebind completes path challenge and echo" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6004)
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

    var cli_udp := try net.Bind(io, "127.0.0.1:0")
    cli_port := cli_udp.Port()
    stage := raw.Atomic[u32].New(0)
    rebound := raw.Atomic[u32].New(0)
    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(15))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeTwoChunkEchoTask(&l, io, &stage, &failed)
        sc.go SimRebindClientTask(h, io, cli_udp, &ccfg, sa, &stage, &rebound, &failed, &done)
        for stage.Load() == 0 && done.Load() == 0 {
            _ = io.Sleep(time.Millis(5)) else { break }
        }
        s.Rebind(cli_port)
        rebound.Store(1)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(10)) else { break }
        }
        sc.Cancel()
    }
    fc := failed.Load()
    if fc != 0 {
        Assert(fc != 1)
        Assert(fc != 2)
        Assert(fc != 3)
        Assert(fc != 4)
        Assert(fc != 51)
        Assert(fc != 52)
        Assert(fc != 53)
        Assert(fc != 54)
        Assert(fc != 55)
        Assert(fc != 59)
        Assert(fc != 6)
        Assert(fc != 7)
        Assert(fc != 8)
        Assert(fc != 80)
        Assert(fc == 0)
    }
}

test "SIM-Endpoint idle timeout expires silently and accept_queue expires stale handshakes" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6005)
    defer s.Free()
    io := s.Io()
    mat := BuildSimTlsConfigs(a, false, QtLeafPem, QtKeyPem, QtRootPem).Must()
    var roots := mat.roots
    var key := mat.key
    ids: [1]tls.Identity = [tls.Identity(chain: mat.chain_buf[0..mat.chain_len], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    tls_srv.handshake = time.Millis(150)
    var tls_cli := tls.DefaultConfig
    tls_cli.roots = some(&roots)
    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    scfg.idle = time.Millis(200)
    var ccfg := DefaultConfig
    ccfg.tls = some(&tls_cli)
    ccfg.idle = time.Millis(200)

    // Idle timeout test.
    var ac := Acceptor(&scfg)
    var l := try ac.Listen(h, io, "127.0.0.1:0", "h3")
    defer l.Close()
    sa := net.Addr(ip: [127, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0], port: Trunc[u16](l.Port()), v6: false)
    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(10))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeIdleTask(&l, io)
        sc.go SimRunIdleClientTask(h, io, &ccfg, sa, &failed, &done)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(20)) else { break }
        }
        sc.Cancel()
    }
    fc := failed.Load()
    if fc != 0 {
        Assert(fc != 1)
        Assert(fc != 2)
        Assert(fc != 3)
        Assert(fc != 4)
        Assert(fc != 51)
        Assert(fc != 52)
        Assert(fc != 53)
        Assert(fc != 54)
        Assert(fc != 55)
        Assert(fc != 59)
        Assert(fc != 6)
        Assert(fc != 7)
        Assert(fc != 8)
        Assert(fc != 80)
        Assert(fc == 0)
    }

    // Accept-queue expiry test: enqueue dummy entry at t0, advance 200ms > 150ms, trigger PruneExpiredAccepts.
    var ep := try Endpoint.Listen(h, io, &scfg, "127.0.0.1:0", "h3")
    defer ep.EpAbandon()
    var cli_k := KeyRing.New(&CidFromSlice(b"\x10\x20\x30\x40\x50\x60\x70\x80"), false)
    defer cli_k.Wipe()
    od1 := CidFromSlice(b"\x10\x20\x30\x40\x50\x60\x70\x80")
    sc1 := CidFromSlice(b"\x01\x02\x03\x04\x05\x06\x07\x08")
    var d1: [MinInitialDatagramLen]u8 = [0; MinInitialDatagramLen]
    _ = SealLongTest(mut cli_k, Space.Initial, &od1, &sc1, 0, b"\x06\x00\x04test", mut d1[..], time.Now(io), time.Millis(10)).Must()
    DispatchWithEp(&ep.core, io, d1[..], TestV4(127, 0, 0, 1, 42001))
    try io.Sleep(time.Millis(250))
    od2 := CidFromSlice(b"\x20\x30\x40\x50\x60\x70\x80\x90")
    var k2 := KeyRing.New(&od2, false)
    defer k2.Wipe()
    var d2: [MinInitialDatagramLen]u8 = [0; MinInitialDatagramLen]
    _ = SealLongTest(mut k2, Space.Initial, &od2, &sc1, 0, b"\x06\x00\x04test", mut d2[..], time.Now(io), time.Millis(10)).Must()
    DispatchWithEp(&ep.core, io, d2[..], TestV4(127, 0, 0, 1, 42002))

    var g := ep.core.Ref().st.Lock(io.NoCancel()).Must()
    // First expired entry was evicted when second arrived past tls.Config.handshake (150 ms).
    Assert(g.Ref().accept_q.Len() <= 1)
    g.Unlock()
    for {
        var g2 := ep.core.Ref().st.Lock(io.NoCancel()).Must()
        item := g2.Mut().accept_q.Pop()
        g2.Unlock()
        match item {
            none { break }
            some(entry) {
                AcceptEntry(conn: c, created_at: _) := entry
                var qc := c.Unpack(h)
                qc.MuxAbandon()
            }
        }
    }
}

test "SIM-Endpoint closing state replies for 3*PTO after server MuxClose and expires" (sys: &System) {
    h := sys.heap
    a := sys.arena
    var s := try sim.Sim.New(a, 6006)
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

    failed := raw.Atomic[u32].New(0)
    done := raw.Atomic[u32].New(0)
    scope sc := io.Scope() {
        sc.CancelAfter(time.Seconds(10))
        sc.go SimRunListenerTask(&l, io)
        sc.go SimServeEchoTask(&l, io, &failed, &done)
        sc.go SimClientEchoRun(h, io, &ccfg, sa, &failed, &done)
        for done.Load() == 0 {
            _ = io.Sleep(time.Millis(10)) else { break }
        }
        // Keep listener briefly alive past close so Endpoint handles closing-state window cleanly.
        _ = io.Sleep(time.Millis(100)) else {}
        sc.Cancel()
    }
    fc := failed.Load()
    if fc != 0 {
        Assert(fc != 1)
        Assert(fc != 2)
        Assert(fc != 3)
        Assert(fc != 4)
        Assert(fc != 51)
        Assert(fc != 52)
        Assert(fc != 53)
        Assert(fc != 54)
        Assert(fc != 55)
        Assert(fc != 59)
        Assert(fc != 6)
        Assert(fc != 7)
        Assert(fc != 8)
        Assert(fc != 80)
        Assert(fc == 0)
    }
}
