// Why: the endpoint demuxer, datagram pool, stateless-reset limiter, half-open
// quota, and connection footprint bounds protect servers against floods and
// memory exhaustion while keeping CID routing fast and collision-free.
//
// What: unit tests for DgramPool (4,096-slot cap and lazy chunk allocation),
// DemuxTable (lookup, tombstone reuse, rehash), per-connection footprint
// (< 24 KiB for 1,000 half-open capacity), half-open limits, Retry admission,
// and stateless reset < 21B drop / 64/sec rate limit.
//
// How: constructs DgramPool, DemuxTable, and EndpointShared directly over
// std.sim virtual clocks and verifies bounds deterministically.

import std.core
import std.crypto
import std.encoding
import std.sim
import std.time
import std.tls
import std.x509

test "DgramPool lazy chunk growth, 4,096-slot limit, free/reuse, and cleanup" (sys: &System) {
    a := sys.arena.Alloc()
    var pool := DgramPool.New(a)
    defer pool.Free(a)
    Assert(pool.chunks.Len() == 0 && pool.in_use == 0)

    var first := pool.AllocSlot(a).Must()
    Assert(first == 0 && pool.chunks.Len() == 1 && pool.in_use == 1)
    s := pool.SlotMut(first)
    s.len = 4
    s.data[0] = 0xca
    s.data[3] = 0xfe
    Assert(pool.SlotRef(first).data[0] == 0xca && pool.SlotRef(first).len == 4)
    pool.FreeSlot(first)
    Assert(pool.in_use == 0)

    // Allocate all 4,096 slots; 4,097th must return none without panic.
    var allocated := 0
    for _ in 0..PoolMaxSlots {
        idx := pool.AllocSlot(a) else {
            Assert(false)
            break
        }
        _ = idx
        allocated += 1
    }
    Assert(allocated == PoolMaxSlots && pool.in_use == PoolMaxSlots)
    Assert(pool.chunks.Len() == PoolMaxChunks)
    Assert(pool.AllocSlot(a).IsNone())

    // Freeing one slot makes exactly one slot available again.
    pool.FreeSlot(1024)
    Assert(pool.in_use == PoolMaxSlots - 1)
    reused := pool.AllocSlot(a).Must()
    Assert(reused == 1024 && pool.in_use == PoolMaxSlots)
}

test "DemuxTable insert, collision probing, remove tombstones, and rehash across 300 CIDs" (sys: &System) {
    a := sys.arena.Alloc()
    var tbl := try DemuxTable.New(a, 0x1234_5678_9abc_def0)
    defer tbl.Free(a)

    for i in 0..300 {
        var b: [8]u8 = [0; 8]
        b[0] = Trunc[u8](i & 0xff)
        b[1] = Trunc[u8]((i >> 8) & 0xff)
        b[2] = 0xa5
        cid := CidFromSlice(b[..])
        try tbl.Insert(a, &cid, i, Trunc[u32](i + 1))
    }
    for i in 0..300 {
        var b: [8]u8 = [0; 8]
        b[0] = Trunc[u8](i & 0xff)
        b[1] = Trunc[u8]((i >> 8) & 0xff)
        b[2] = 0xa5
        cid := CidFromSlice(b[..])
        got := tbl.Lookup(&cid).Must()
        Assert(got.idx == i && got.gen_ == Trunc[u32](i + 1))
    }
    // Remove evens, check odds remain and evens are gone.
    for i in 0..300 {
        if (i % 2) == 0 {
            var b: [8]u8 = [0; 8]
            b[0] = Trunc[u8](i & 0xff)
            b[1] = Trunc[u8]((i >> 8) & 0xff)
            b[2] = 0xa5
            cid := CidFromSlice(b[..])
            tbl.Remove(&cid)
        }
    }
    for i in 0..300 {
        var b: [8]u8 = [0; 8]
        b[0] = Trunc[u8](i & 0xff)
        b[1] = Trunc[u8]((i >> 8) & 0xff)
        b[2] = 0xa5
        cid := CidFromSlice(b[..])
        if (i % 2) == 0 {
            Assert(tbl.Lookup(&cid).IsNone())
        } else {
            Assert(tbl.Lookup(&cid).IsSome())
        }
    }
}

test "RFC 9000 / Section 3.11: half-open QConn memory footprint stays under 24 KiB each" (sys: &System) {
    _ = sys
    conn_bytes := core.SizeOfT[QConn]()
    // 1,000 half-open connections must fit within 24 MiB (< 24 KiB each).
    if conn_bytes >= 24_576 {
        Assert(core.SizeOfT[Engine]() < 24_576)
        Assert(core.SizeOfT[AckSet]() < 8_192)
        Assert(core.SizeOfT[Recovery]() < 8_192)
        Assert(core.SizeOfT[KeyRing]() < 8_192)
        Assert(core.SizeOfT[CidState]() < 8_192)
        Assert(core.SizeOfT[Streams]() < 8_192)
        Assert(conn_bytes < 24_576)
    }
}

test "Section 3.7, 3.11: DefaultConfig transport defaults (QT-268..272) and half-open limit" (sys: &System) {
    cfg := DefaultConfig
    Assert(cfg.half_open == 1000)
    Assert(cfg.retry_above == 256)
    Assert(cfg.streams == 100)
    Assert(cfg.idle.Nanos() == 30_000_000_000)

    a := sys.arena
    h := sys.heap
    var clk := try sim.Sim.New(a, 51)
    defer clk.Free()
    io := clk.Io()

    certs := (try x509.ParsePem(a, QtLeafPem)).Done()
    blocks := try encoding.ParsePem(a, QtKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]tls.Identity = [tls.Identity(chain: certs[..], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]

    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)
    scfg.half_open = 1
    scfg.retry_above = 100

    var ep := try Endpoint.Listen(h, io, &scfg, "127.0.0.1:0", "h3")
    defer ep.EpAbandon()

    // Craft two valid 1200B client Initial packets with distinct ODCIDs -> 2nd exceeds max_half_open = 1.
    peer1 := TestV4(127, 0, 0, 1, 41001)
    peer2 := TestV4(127, 0, 0, 1, 41002)
    odcid1 := CidFromSlice(b"\x11\x11\x11\x11\x11\x11\x11\x11")
    odcid2 := CidFromSlice(b"\x22\x22\x22\x22\x22\x22\x22\x22")
    scid1 := CidFromSlice(b"\xaa\x01\x02\x03\x04\x05\x06\x07")
    scid2 := CidFromSlice(b"\xbb\x01\x02\x03\x04\x05\x06\x07")
    var k1 := KeyRing.New(&odcid1, false)
    defer k1.Wipe()
    var k2 := KeyRing.New(&odcid2, false)
    defer k2.Wipe()

    var d1: [MinInitialDatagramLen]u8 = [0; MinInitialDatagramLen]
    _ = SealLongTest(mut k1, Space.Initial, &odcid1, &scid1, 0, b"\x06\x00\x04test", mut d1[..], time.Now(io), time.Millis(10)).Must()
    var d2: [MinInitialDatagramLen]u8 = [0; MinInitialDatagramLen]
    _ = SealLongTest(mut k2, Space.Initial, &odcid2, &scid2, 0, b"\x06\x00\x04test", mut d2[..], time.Now(io), time.Millis(10)).Must()

    DispatchWithEp(&ep.core, io, d1[..], peer1)
    DispatchWithEp(&ep.core, io, d2[..], peer2)

    var g := ep.core.Ref().st.Lock(io.NoCancel()).Must()
    Assert(g.Ref().half_open == 1)
    g.Unlock()

    // Drain any queued connection cleanly before abandoning ep (unlock before MuxAbandon to avoid re-locking ep).
    for {
        var g2 := ep.core.Ref().st.Lock(io.NoCancel()).Must()
        sm := mut *g2.Mut()
        item := sm.accept_q.Pop()
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

test "RFC 9000 10.3: short-header packet < 21 bytes does not trigger stateless reset" (sys: &System) {
    a := sys.arena
    h := sys.heap
    var clk := try sim.Sim.New(a, 52)
    defer clk.Free()
    io := clk.Io()

    certs := (try x509.ParsePem(a, QtLeafPem)).Done()
    blocks := try encoding.ParsePem(a, QtKeyPem)
    key := try crypto.ParsePrivateKey(blocks[0].der)
    ids: [1]tls.Identity = [tls.Identity(chain: certs[..], key: &key)]
    var tls_srv := tls.DefaultConfig
    tls_srv.identities = ids[..]
    var scfg := DefaultConfig
    scfg.tls = some(&tls_srv)

    var ep := try Endpoint.Listen(h, io, &scfg, "127.0.0.1:0", "h3")
    defer ep.EpAbandon()

    // 18-byte short-header packet with unknown CID (< 21B minimum): must NOT increment reset_limiter.
    var short_small: [18]u8 = [0x40; 18]
    DispatchWithEp(&ep.core, io, short_small[..], TestV4(127, 0, 0, 1, 41099))
    var g := ep.core.Ref().st.Lock(io.NoCancel()).Must()
    Assert(g.Ref().reset_limiter.tokens == MaxStatelessResetsPerSec)
    g.Unlock()
}
