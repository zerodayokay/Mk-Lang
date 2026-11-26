// Why: HTTP/2 multiplexes concurrent streams on one connection; stream state
// must stay bounded by the configured stream limit, reuse buffers on a warm
// connection with zero per-request allocation, and isolate stale StreamRefs via
// generation numbers.
//
// What: StreamRef, ResetSet, Page, PagePool, PageQueue, Stream, and StreamTable.
//
// How: StreamTable starts at min(limit, 8) slots and doubles up to limit,
// indexing active stream ids in a power-of-two open-addressing table for O(1)
// frame lookup; closed streams stay in a 256-id FIFO ResetSet (1 KiB) when
// reset by this side so late frames are minimally processed and ignored;
// client DATA uses 16 KiB Pages from a per-connection PagePool capped at 8
// idle pages.

import std.core
import std.http.fields
import std.time

// Stream states (RFC 9113 5.1).
const StIdle: u8 = 0
const StOpen: u8 = 1
const StHalfLocal: u8 = 2
const StHalfRemote: u8 = 3
const StClosed: u8 = 4

// Blocked-on flags for send flow control and write backpressure.
const BlockNone: u8 = 0
const BlockStreamWin: u8 = 1
const BlockConnWin: u8 = 2
const BlockWriteBuf: u8 = 3

// Maximum valid 31-bit stream id (RFC 9113 5.1.1).
const MaxStreamId: u32 = 0x7FFF_FFFF

// Client DATA page size and max idle free pages kept per connection.
const PageSize: int = 16384
const MaxFreePages: int = 8

/// A handle to one HTTP/2 stream: table slot index and generation.
export struct StreamRef {
    export index: int
    export gen_: u32
}

// ResetSet remembers the last 256 stream ids this side reset (RFC 9113 5.1).
struct ResetSet {
    ids: [256]u32
    head: int
    len: int
}

ResetSet.New(): ResetSet {
    return ResetSet(ids: [0; 256], head: 0, len: 0)
}

ResetSet.Has(&self, id: u32): bool {
    if id == 0 {
        return false
    }
    for i in 0..self.len {
        idx := (self.head + i) % 256
        if self.ids[idx] == id {
            return true
        }
    }
    return false
}

ResetSet.Add(mut self, id: u32) {
    if id == 0 || self.Has(id) {
        return
    }
    if self.len < 256 {
        slot := (self.head + self.len) % 256
        self.ids[slot] = id
        self.len += 1
        return
    }
    self.ids[self.head] = id
    self.head = (self.head + 1) % 256
}

// Page holds up to PageSize bytes of received client DATA.
linear view struct Page {
    buf: HeapList[u8]
    len: int
    off: int
}

Page.New(a: &Alloc): OutOfMemory!Page {
    var buf := HeapList[u8].New(a)
    onerr buf.Free(a)
    for _ in 0..PageSize {
        try buf.Push(a, 0)
    }
    return Page(buf: buf, len: 0, off: 0)
}

impl Free for Page {
    Free(self, a: &Alloc) {
        Page(buf, ..) := self
        buf.Free(a)
    }
}

// PageQueue is a per-stream FIFO of received DATA pages.
linear view struct PageQueue {
    pages: HeapList[Page]
    total: int
}

PageQueue.New(a: &Alloc): PageQueue {
    return PageQueue(pages: HeapList[Page].New(a), total: 0)
}

impl Free for PageQueue {
    Free(self, a: &Alloc) {
        PageQueue(pages, ..) := self
        pages.Free(a)
    }
}

PageQueue.Read(&self): int {
    return self.total
}

linear view struct Stream {
    id: u32
    gen_: u32
    state: u8
    refs: int
    next_free: int
    send_win: Window
    recv_win: Window
    recv_credit: Credit
    send_gate: UpdateGate
    progress: Progress
    blocked_on: u8
    dribble_since: ?time.Instant
    in_owed: bool
    owed_next: int
    owed_credit: int
    owed_reset: ?u32
    owed_100: bool
    admitted: bool
    admitted_win: int
    admit_reserve: int
    content_length: ?i64
    data_recvd: i64
    head_method: bool
    expect_100: bool
    reset: ?u32
    send_stopped: bool
    unprocessed: bool
    srv_dispatched: bool
    cli_head: ?fields.FieldBlock
    cli_head_ready: bool
    cli_head_too_large: bool
    cli_head_malformed: bool
    cli_body_malformed: bool
    cli_head_status: int
    cli_head_cl: ?i64
    cli_eof: bool
    cli_pages: PageQueue
}

Stream.New(a: &Alloc, next_free: int): Stream {
    return Stream(
        id: 0,
        gen_: 1,
        state: StIdle,
        refs: 0,
        next_free: next_free,
        send_win: Window.New(InitialWindow),
        recv_win: Window.New(InitialWindow),
        recv_credit: Credit.New(InitialWindow),
        send_gate: UpdateGate.New(),
        progress: Progress.New(),
        blocked_on: BlockNone,
        dribble_since: none,
        in_owed: false,
        owed_next: -1,
        owed_credit: 0,
        owed_reset: none,
        owed_100: false,
        admitted: false,
        admitted_win: 0,
        admit_reserve: 0,
        content_length: none,
        data_recvd: 0,
        head_method: false,
        expect_100: false,
        reset: none,
        send_stopped: false,
        unprocessed: false,
        srv_dispatched: false,
        cli_head: none,
        cli_head_ready: false,
        cli_head_too_large: false,
        cli_head_malformed: false,
        cli_body_malformed: false,
        cli_head_status: 0,
        cli_head_cl: none,
        cli_eof: false,
        cli_pages: PageQueue.New(a),
    )
}

impl Free for Stream {
    Free(self, a: &Alloc) {
        Stream(cli_head, cli_pages, ..) := self
        match cli_head {
            some(h) { h.Free(a) }
            none {}
        }
        cli_pages.Free(a)
    }
}

struct IdEntry {
    id: u32
    idx: int
}

const IdEmpty: u32 = 0
const IdTomb: u32 = 0xFFFF_FFFF

linear view struct StreamTable {
    slots: HeapList[Stream]
    map: HeapList[IdEntry]
    map_used: int
    cap_limit: int
    free_head: int
    active: int
}

HashId(id: u32, mask: int): int {
    x := (id *% 2654435761) ^ (id >> 16)
    return int(x) & mask
}

StreamTable.New(a: &Alloc, limit: int): OutOfMemory!StreamTable {
    lim := if limit < 1 { 1 } else { limit }
    init := if lim < 8 { lim } else { 8 }
    var slots := HeapList[Stream].New(a)
    onerr slots.Free(a)
    for i in 0..init {
        nf := if i + 1 < init { i + 1 } else { -1 }
        var s := Stream.New(a, nf)
        slots.Push(a, s) else e {
            s.Free(a)
            fail e
        }
    }
    var map_cap := 16
    for map_cap < init * 2 {
        map_cap *= 2
    }
    var map := HeapList[IdEntry].New(a)
    onerr map.Free(a)
    for _ in 0..map_cap {
        try map.Push(a, IdEntry(id: IdEmpty, idx: -1))
    }
    return StreamTable(slots: slots, map: map, map_used: 0, cap_limit: lim, free_head: 0, active: 0)
}

impl Free for StreamTable {
    Free(self, a: &Alloc) {
        StreamTable(slots, map, ..) := self
        slots.Free(a)
        map.Free(a)
    }
}

StreamTable.Rehash(mut self, a: &Alloc into self, new_cap: int): OutOfMemory!void {
    var nm := HeapList[IdEntry].New(a)
    onerr nm.Free(a)
    for _ in 0..new_cap {
        try nm.Push(a, IdEntry(id: IdEmpty, idx: -1))
    }
    mask := new_cap - 1
    var used := 0
    items := nm.ItemsMut()
    for e in self.map.Items() {
        if e.id != IdEmpty && e.id != IdTomb {
            var pos := HashId(e.id, mask)
            for items[pos].id != IdEmpty {
                pos = (pos + 1) & mask
            }
            items[pos] = e
            used += 1
        }
    }
    old := core.Replace(mut self.map, nm)
    old.Free(a)
    self.map_used = used
}

StreamTable.MapInsert(mut self, a: &Alloc into self, id: u32, idx: int): OutOfMemory!void {
    if (self.map_used + 1) * 2 >= self.map.Len() {
        try self.Rehash(a, self.map.Len() * 2)
    }
    mask := self.map.Len() - 1
    items := self.map.ItemsMut()
    var pos := HashId(id, mask)
    var tomb := -1
    for {
        cur := items[pos].id
        if cur == IdEmpty {
            target := if tomb >= 0 { tomb } else { pos }
            if tomb < 0 {
                self.map_used += 1
            }
            items[target] = IdEntry(id: id, idx: idx)
            return
        }
        if cur == IdTomb && tomb < 0 {
            tomb = pos
        } else if cur == id {
            items[pos].idx = idx
            return
        }
        pos = (pos + 1) & mask
    }
}

StreamTable.MapRemove(mut self, id: u32) {
    if self.map.Len() == 0 {
        return
    }
    mask := self.map.Len() - 1
    items := self.map.ItemsMut()
    var pos := HashId(id, mask)
    for {
        cur := items[pos].id
        if cur == IdEmpty {
            return
        }
        if cur == id {
            items[pos] = IdEntry(id: IdTomb, idx: -1)
            return
        }
        pos = (pos + 1) & mask
    }
}

StreamTable.Find(&self, id: u32): int {
    if id == 0 || id == IdTomb || self.map.Len() == 0 {
        return -1
    }
    mask := self.map.Len() - 1
    items := self.map.Items()
    var pos := HashId(id, mask)
    for {
        e := items[pos]
        if e.id == IdEmpty {
            return -1
        }
        if e.id == id {
            return e.idx
        }
        pos = (pos + 1) & mask
    }
}

StreamTable.Grow(mut self, a: &Alloc into self): OutOfMemory!bool {
    cur := self.slots.Len()
    if cur >= self.cap_limit {
        return false
    }
    var next := cur * 2
    if next > self.cap_limit {
        next = self.cap_limit
    }
    for i in cur..next {
        var s := Stream.New(a, self.free_head)
        self.slots.Push(a, s) else e {
            s.Free(a)
            fail e
        }
        self.free_head = i
    }
    return true
}

StreamTable.AllocSlot(mut self, a: &Alloc into self, id: u32, send_init: int, recv_init: int, credit_win: int): OutOfMemory!?int {
    if self.active >= self.cap_limit {
        return none
    }
    if self.free_head < 0 {
        grew := try self.Grow(a)
        if !grew {
            return none
        }
    }
    idx := self.free_head
    try self.MapInsert(a, id, idx)
    s := mut self.slots.ItemsMut()[idx]
    self.free_head = s.next_free
    s.next_free = -1
    s.id = id
    s.state = StOpen
    s.refs = 0
    s.send_win = Window.New(send_init)
    s.recv_win = Window.New(recv_init)
    s.recv_credit = Credit.New(credit_win)
    s.send_gate = UpdateGate.New()
    s.progress = Progress.New()
    s.blocked_on = BlockNone
    s.dribble_since = none
    s.owed_credit = 0
    s.owed_reset = none
    s.owed_100 = false
    s.admitted = false
    s.admitted_win = 0
    s.admit_reserve = 0
    s.content_length = none
    s.data_recvd = 0
    s.head_method = false
    s.expect_100 = false
    s.reset = none
    s.send_stopped = false
    s.unprocessed = false
    s.srv_dispatched = false
    s.cli_head_ready = false
    s.cli_head_too_large = false
    s.cli_head_malformed = false
    s.cli_body_malformed = false
    s.cli_head_status = 0
    s.cli_head_cl = none
    s.cli_eof = false
    self.active += 1
    return some(idx)
}

StreamTable.AppendClientData(mut self, a: &Alloc into self, idx: int, data: []u8): OutOfMemory!void {
    if data.Len() == 0 {
        return
    }
    var off := 0
    slot := mut self.slots.ItemsMut()[idx]
    plen := slot.cli_pages.pages.Len()
    if plen > 0 {
        tail := mut slot.cli_pages.pages.ItemsMut()[plen - 1]
        room := PageSize - tail.len
        if room > 0 {
            k := if data.Len() < room { data.Len() } else { room }
            (mut tail.buf.ItemsMut()[tail.len..tail.len + k]).CopyFrom(data[0..k])
            tail.len += k
            slot.cli_pages.total += k
            off += k
        }
    }
    for off < data.Len() {
        var p := try Page.New(a)
        rem := data.Len() - off
        k := if rem < PageSize { rem } else { PageSize }
        (mut p.buf.ItemsMut()[0..k]).CopyFrom(data[off..off + k])
        p.len = k
        slot.cli_pages.pages.Push(a, p) else e {
            p.Free(a)
            fail e
        }
        slot.cli_pages.total += k
        off += k
    }
}

StreamTable.ReadClientData(mut self, a: &Alloc, idx: int, dst: mut []u8): int {
    if dst.Len() == 0 {
        return 0
    }
    var copied := 0
    slot := mut self.slots.ItemsMut()[idx]
    for copied < dst.Len() && slot.cli_pages.pages.Len() > 0 {
        var drop := false
        p := mut slot.cli_pages.pages.ItemsMut()[0]
        avail := p.len - p.off
        need := dst.Len() - copied
        k := if avail < need { avail } else { need }
        if k > 0 {
            (mut dst[copied..copied + k]).CopyFrom(p.buf.Items()[p.off..p.off + k])
            p.off += k
            copied += k
            slot.cli_pages.total -= k
        }
        drop = p.off >= p.len
        if drop {
            done_p := slot.cli_pages.pages.Remove(0)
            done_p.Free(a)
        }
    }
    return copied
}

StreamTable.ReleaseSlot(mut self, idx: int, a: &Alloc) {
    s := mut self.slots.ItemsMut()[idx]
    if s.state == StIdle {
        return
    }
    old_id := s.id
    for s.cli_pages.pages.Len() > 0 {
        s.cli_pages.pages.Pop().Must().Free(a)
    }
    s.cli_pages.total = 0
    if h := mut s.cli_head {
        h.Reset()
    }
    s.id = 0
    s.gen_ +%= 1
    if s.gen_ == 0 {
        s.gen_ = 1
    }
    s.state = StIdle
    s.next_free = self.free_head
    self.free_head = idx
    self.active -= 1
    self.MapRemove(old_id)
}
