// Why: HTTP/2 stream table scaling, generation isolation, 256-entry ResetSet
// FIFO tracking, 16 KiB client DATA paging, and bounded Owed rings must hold
// their exact invariants under churn and wraparound.
//
// What: unit tests for StreamTable, ResetSet, PageQueue, and Owed.
//
// How: exercises StreamTable lookup/grow/release/stale-generation, ResetSet
// 256-entry wraparound, AppendClientData/ReadClientData multi-page splitting,
// and Owed 32-entry PING/SETTINGS caps.

import std.core

test "StreamTable grows to limit, keeps O(1) lookup, and bumps generation on ReleaseSlot" (sys: &System) {
    a := sys.heap.Alloc()
    var tbl := StreamTable.New(a, 24).Must()
    defer tbl.Free(a)
    var idxs: [24]int = [0; 24]
    var gens: [24]u32 = [0; 24]
    for i in 0..24 {
        sid := Trunc[u32](1 + i * 2)
        idx := tbl.AllocSlot(a, sid, 65535, 65535, 65535).Must().Must()
        idxs[i] = idx
        gens[i] = tbl.slots.Items()[idx].gen_
        Assert(tbl.Find(sid) == idx)
    }
    Assert(tbl.active == 24)
    Assert(tbl.AllocSlot(a, 99, 65535, 65535, 65535).Must().IsNone())
    // Release even slots and verify Find returns -1 and generation advances.
    for i in 0..24 {
        if (i & 1) == 0 {
            sid := Trunc[u32](1 + i * 2)
            tbl.ReleaseSlot(idxs[i], a)
            Assert(tbl.Find(sid) == -1)
            Assert(tbl.slots.Items()[idxs[i]].gen_ != gens[i])
            Assert(tbl.slots.Items()[idxs[i]].gen_ != 0)
        }
    }
    Assert(tbl.active == 12)
    // Reallocate into freed slots with new stream ids.
    for i in 0..12 {
        sid := Trunc[u32](101 + i * 2)
        idx := tbl.AllocSlot(a, sid, 65535, 65535, 65535).Must().Must()
        Assert(tbl.Find(sid) == idx)
    }
    Assert(tbl.active == 24)
}

test "StreamTable keeps its map capacity during stream churn" (sys: &System) {
    a := sys.heap.Alloc()
    var tbl := StreamTable.New(a, 8).Must()
    defer tbl.Free(a)
    anchor := tbl.AllocSlot(a, 1, 65535, 65535, 65535).Must().Must()
    cap := tbl.map.Len()
    for i in 1..513 {
        sid := Trunc[u32](1 + i * 2)
        idx := tbl.AllocSlot(a, sid, 65535, 65535, 65535).Must().Must()
        Assert(tbl.Find(sid) == idx)
        tbl.ReleaseSlot(idx, a)
        Assert(tbl.Find(1) == anchor && tbl.Find(sid) == -1)
    }
    Assert(tbl.map.Len() == cap && tbl.map_used == 1)
}

test "ResetSet tracks 256 ids as FIFO without duplicates" {
    var rs := ResetSet.New()
    rs.Add(0)
    Assert(!rs.Has(0))
    for i in 1..257 {
        rs.Add(Trunc[u32](i * 2 - 1))
        rs.Add(Trunc[u32](i * 2 - 1)) // duplicate ignored
    }
    Assert(rs.len == 256)
    for i in 1..257 {
        Assert(rs.Has(Trunc[u32](i * 2 - 1)))
    }
    // Adding 10 more evicts the oldest 10.
    for i in 257..267 {
        rs.Add(Trunc[u32](i * 2 - 1))
    }
    Assert(rs.len == 256)
    for i in 1..11 {
        Assert(!rs.Has(Trunc[u32](i * 2 - 1)))
    }
    for i in 11..267 {
        Assert(rs.Has(Trunc[u32](i * 2 - 1)))
    }
}

test "StreamTable client DATA appends and drains across multiple 16 KiB pages" (sys: &System) {
    a := sys.heap.Alloc()
    var tbl := StreamTable.New(a, 4).Must()
    defer tbl.Free(a)
    idx := tbl.AllocSlot(a, 1, 65535, 65535, 65535).Must().Must()
    var chunk: [20000]u8 = [0; 20000]
    for i in 0..chunk.Len() {
        chunk[i] = Trunc[u8]((i * 7 + 3) & 0xFF)
    }
    tbl.AppendClientData(sys.heap, idx, chunk[0..10000]).Must()
    tbl.AppendClientData(sys.heap, idx, chunk[10000..20000]).Must()
    Assert(tbl.slots.Items()[idx].cli_pages.Read() == 20000)
    var out: [20000]u8 = [0; 20000]
    var got := 0
    for got < 20000 {
        n := tbl.ReadClientData(a, idx, mut out[got..got + if 20000 - got < 3333 { 20000 - got } else { 3333 }])
        Assert(n > 0)
        got += n
    }
    Assert(tbl.slots.Items()[idx].cli_pages.Read() == 0)
    Assert(core.BytesEqual(out[..], 0, chunk[..]))
    Assert(tbl.free_pages.Len() == 2)
    tbl.AppendClientData(sys.heap, idx, chunk[..]).Must()
    Assert(tbl.free_pages.Len() == 0)
    tbl.ReleaseSlot(idx, a)
    Assert(tbl.free_pages.Len() == 2)
}

test "Owed bounds PING and SETTINGS rings to 32 entries and drains FIFO" (sys: &System) {
    a := sys.heap.Alloc()
    var ow := Owed.New(a)
    defer ow.Free(a)
    Assert(ow.Empty())
    for i in 0..32 {
        var p: [8]u8 = [0; 8]
        p[0] = Trunc[u8](i)
        ow.QueuePing(p).Must()
        ow.QueueSettingsAck(4096 + i).Must()
    }
    var extra: [8]u8 = [99; 8]
    match ow.QueuePing(extra) {
        err(v) { Assert(v.code == EnhanceYourCalm && v.stream == 0) }
        ok(_) { Assert(false) }
    }
    match ow.QueueSettingsAck(4096) {
        err(v) { Assert(v.code == EnhanceYourCalm && v.stream == 0) }
        ok(_) { Assert(false) }
    }
    for i in 0..32 {
        p := ow.PopPing().Must()
        Assert(p[0] == Trunc[u8](i))
        sz := ow.PopSettingsAck().Must()
        Assert(sz == 4096 + i)
    }
    Assert(ow.PopPing().IsNone())
    Assert(ow.PopSettingsAck().IsNone())
    Assert(ow.Empty())
}
