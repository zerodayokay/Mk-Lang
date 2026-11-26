// Why: the HTTP/2 reader never waits for the write token or writes to the
// socket on the server; every frame the reader owes (PING ACK, SETTINGS ACK,
// WINDOW_UPDATE, RST_STREAM, GOAWAY) is coalesced in connection state under
// strict bounds against control-frame floods.
//
// What: OwedReset, OwedGoaway, and Owed (New, Free, Empty, QueuePing,
// QueueSettingsAck, QueueReset, QueueGoaway, AddConnCredit).
//
// How: PING payloads and SETTINGS ACKs live in fixed 32-slot rings (exceeding
// MaxOwedPings or MaxOwedSettingsAcks fails with EnhanceYourCalm); connection
// window credit coalesces in one counter; per-stream resets/credits link
// through an intrusive FIFO in the stream table; standalone resets on
// unslotted streams queue in a small HeapList bounded by ResetBudget.

struct OwedReset {
    stream: u32
    code: u32
}

struct OwedGoaway {
    last: u32
    code: u32
}

linear view struct Owed {
    pings: [32][8]u8
    ping_head: int
    ping_count: int
    ack_sizes: [32]int
    ack_head: int
    settings_acks: int
    conn_credit: int
    stream_head: int
    stream_tail: int
    resets: HeapList[OwedReset]
    goaway: ?OwedGoaway
    goaway_sent: bool
}

Owed.New(a: &Alloc): Owed {
    return Owed(
        pings: [[0; 8]; 32],
        ping_head: 0,
        ping_count: 0,
        ack_sizes: [0; 32],
        ack_head: 0,
        settings_acks: 0,
        conn_credit: 0,
        stream_head: -1,
        stream_tail: -1,
        resets: HeapList[OwedReset].New(a),
        goaway: none,
        goaway_sent: false,
    )
}

Owed.Free(self, a: &Alloc) {
    Owed(resets, ..) := self
    resets.Free(a)
}

Owed.Empty(&self): bool {
    return self.ping_count == 0 && self.settings_acks == 0 && self.conn_credit == 0 && self.stream_head < 0 && self.resets.Len() == 0 && self.goaway.IsNone()
}

Owed.QueuePing(mut self, data: [8]u8): Violation!void {
    if self.ping_count >= MaxOwedPings {
        fail Violation(code: EnhanceYourCalm, stream: 0)
    }
    slot := (self.ping_head + self.ping_count) % 32
    self.pings[slot] = data
    self.ping_count += 1
}

Owed.PopPing(mut self): ?[8]u8 {
    if self.ping_count == 0 {
        return none
    }
    d := self.pings[self.ping_head]
    self.ping_head = (self.ping_head + 1) % 32
    self.ping_count -= 1
    return some(d)
}

Owed.QueueSettingsAck(mut self, table_bytes: int): Violation!void {
    if self.settings_acks >= MaxOwedSettingsAcks {
        fail Violation(code: EnhanceYourCalm, stream: 0)
    }
    slot := (self.ack_head + self.settings_acks) % 32
    self.ack_sizes[slot] = table_bytes
    self.settings_acks += 1
}

Owed.PopSettingsAck(mut self): ?int {
    if self.settings_acks == 0 {
        return none
    }
    sz := self.ack_sizes[self.ack_head]
    self.ack_head = (self.ack_head + 1) % 32
    self.settings_acks -= 1
    return some(sz)
}

Owed.QueueReset(mut self, a: &Alloc, stream: u32, code: u32): OutOfMemory!void {
    try self.resets.Push(a, OwedReset(stream: stream, code: code))
}

Owed.QueueGoaway(mut self, last: u32, code: u32) {
    if self.goaway_sent {
        return
    }
    if cur := self.goaway {
        if last < cur.last {
            self.goaway = some(OwedGoaway(last: last, code: code))
        }
        return
    }
    self.goaway = some(OwedGoaway(last: last, code: code))
}

Owed.AddConnCredit(mut self, n: int) {
    if n > 0 {
        self.conn_credit += n
    }
}
