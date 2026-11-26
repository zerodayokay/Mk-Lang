// Why: QUIC carries TLS handshake messages in independent CRYPTO streams at
// each encryption level starting at offset 0, bounds out-of-order buffering at
// 64 KiB per level, retransmits unacknowledged CRYPTO data, and discards Initial
// and Handshake CRYPTO buffers when their keys are dropped.
//
// What: CryptoLevel and CryptoStreams (New, Free, OnCrypto, Read, Queue, Next,
// OnAcked, OnLost, Discard, MaxOffset, UnreadBytes).
//
// How: each level keeps a receive ByteRing plus OooBuf (at most 32 segments)
// bounded by read_offset + 65,536, and a send ByteRing with acknowledged and
// lost ByteRanges.

// Receive and send state for one encryption level's CRYPTO stream.
linear struct CryptoLevel {
    rx_contig: ByteRing
    rx_ooo: OooBuf
    read_offset: u64
    contig_offset: u64
    max_offset: u64
    tx_ring: ByteRing
    tx_base: u64
    tx_send: u64
    tx_write: u64
    tx_acked: HeapList[ByteRange]
    tx_lost: HeapList[ByteRange]
    discarded: bool
}

CryptoLevel.New(a: &Alloc): CryptoLevel {
    return CryptoLevel(
        rx_contig: ByteRing.New(a),
        rx_ooo: OooBuf.New(a),
        read_offset: 0,
        contig_offset: 0,
        max_offset: 0,
        tx_ring: ByteRing.New(a),
        tx_base: 0,
        tx_send: 0,
        tx_write: 0,
        tx_acked: HeapList[ByteRange].New(a),
        tx_lost: HeapList[ByteRange].New(a),
        discarded: false,
    )
}

CryptoLevel.Free(self, a: &Alloc) {
    CryptoLevel(
        rx_contig,
        rx_ooo,
        read_offset: _,
        contig_offset: _,
        max_offset: _,
        tx_ring,
        tx_base: _,
        tx_send: _,
        tx_write: _,
        tx_acked,
        tx_lost,
        discarded: _,
    ) := self
    rx_contig.Free(a)
    rx_ooo.Free(a)
    tx_ring.Free(a)
    tx_acked.Free(a)
    tx_lost.Free(a)
}

// Returns the byte length of a CRYPTO frame header (RFC 9000 19.6). O(1).
CryptoHeaderLen(offset: u64, data_len: int): int {
    return 1 + VarintLen(offset) + VarintLen(Cast[u64](data_len))
}

// Per-level CRYPTO stream table (Initial, Handshake, App) per RFC 9000 7.5, 19.6.
// Linear: owns three CryptoLevel buffers.
linear struct CryptoStreams {
    levels: [3]CryptoLevel
}

// Initializes all three encryption levels' CRYPTO streams starting at offset 0. O(1).
CryptoStreams.New(a: &Alloc): CryptoStreams {
    return CryptoStreams(levels: [CryptoLevel.New(a), CryptoLevel.New(a), CryptoLevel.New(a)])
}

// Releases all CRYPTO buffers back to a. O(1).
CryptoStreams.Free(self, a: &Alloc) {
    CryptoStreams(levels) := self
    [l0, l1, l2] := levels
    l0.Free(a)
    l1.Free(a)
    l2.Free(a)
}

// Processes a received CRYPTO frame at level (RFC 9000 7.5, 19.6).
// Fails with TransportError(ErrFrameEncoding, FrameCrypto) if offset + len > 2^62 - 1,
// TransportError(ErrCryptoBufferExceeded, FrameCrypto) if buffered out-of-order span exceeds
// 64 KiB past read_offset, or GapLimit if out-of-order segments exceed 32.
CryptoStreams.OnCrypto(mut self, a: &Alloc, level: Space, offset: u64, data: []u8): Error!void {
    dlen := Cast[u64](data.Len())
    if dlen > MaxOffset - offset {
        fail TransportError(code: ErrFrameEncoding, frame: FrameCrypto)
    }
    li := SpaceIndex(level)
    if self.levels[li].discarded {
        return
    }
    end := offset + dlen
    if end > self.levels[li].read_offset + Cast[u64](CryptoBufferLimit) {
        fail TransportError(code: ErrCryptoBufferExceeded, frame: FrameCrypto)
    }
    if end > self.levels[li].max_offset {
        self.levels[li].max_offset = end
    }
    ins_lo := if offset > self.levels[li].contig_offset { offset } else { self.levels[li].contig_offset }
    if ins_lo >= end {
        return
    }
    if ins_lo > self.levels[li].contig_offset {
        if self.levels[li].rx_ooo.ProjectedGaps(ins_lo, end) > MaxRecvGaps {
            fail GapLimit
        }
        slice := data[Cast[int](ins_lo - offset)..]
        try self.levels[li].rx_ooo.Insert(a, ins_lo, slice)
        return
    }
    slice := data[Cast[int](ins_lo - offset)..]
    try self.levels[li].rx_contig.Push(a, slice)
    self.levels[li].contig_offset = end
    self.levels[li].contig_offset = try self.levels[li].rx_ooo.DrainContig(a, mut self.levels[li].rx_contig, self.levels[li].contig_offset)
}

// Reads contiguous CRYPTO bytes at level into buf and advances read_offset. O(n).
CryptoStreams.Read(mut self, level: Space, buf: mut []u8): int {
    li := SpaceIndex(level)
    n := self.levels[li].rx_contig.PopInto(buf)
    self.levels[li].read_offset += Cast[u64](n)
    return n
}

// Queues outgoing TLS handshake bytes at level for transmission in CRYPTO
// frames. Amortized O(data.Len()).
CryptoStreams.Queue(mut self, a: &Alloc, level: Space, data: []u8): OutOfMemory!void {
    li := SpaceIndex(level)
    if self.levels[li].discarded || data.Len() == 0 {
        return
    }
    try self.levels[li].tx_ring.Push(a, data)
    self.levels[li].tx_write += Cast[u64](data.Len())
}

// Emits CRYPTO frames (lost retransmissions first, then unsent bytes) at level
// into out[0..max] and records SentKind.Crypto frames.
CryptoStreams.Next(mut self, level: Space, out: mut []u8, max: int, frames: mut []SentFrame): (n: int, frames: int) {
    li := SpaceIndex(level)
    if self.levels[li].discarded {
        return (n: 0, frames: 0)
    }
    limit := if max < out.Len() { max } else { out.Len() }
    var off := 0
    var fn_cnt := 0

    for self.levels[li].tx_lost.Len() > 0 && fn_cnt < frames.Len() && off < limit {
        lr := self.levels[li].tx_lost[0]
        avail := Cast[int](lr.hi - lr.lo)
        hdr := CryptoHeaderLen(lr.lo, avail)
        if off + hdr >= limit {
            break
        }
        room := limit - off - hdr
        var take := if avail < room { avail } else { room }
        if take > 1500 {
            take = 1500
        }
        if take <= 0 {
            break
        }
        rel := Cast[int](lr.lo - self.levels[li].tx_base)
        var scratch: [1500]u8 = [0; 1500]
        self.levels[li].tx_ring.CopyAt(rel, mut scratch[0..take])
        w := WriteCrypto(mut out[off..limit], lr.lo, scratch[0..take]) else { 0 }
        if w == 0 {
            break
        }
        off += w
        frames[fn_cnt] = SentFrame(kind: SentKind.Crypto(level: level, offset: lr.lo, len: take))
        fn_cnt += 1
        if lr.lo + Cast[u64](take) == lr.hi {
            _ = self.levels[li].tx_lost.Remove(0)
        } else {
            items := self.levels[li].tx_lost.ItemsMut()
            items[0].lo += Cast[u64](take)
        }
    }

    if fn_cnt < frames.Len() && off < limit && self.levels[li].tx_send < self.levels[li].tx_write {
        unsent := Cast[int](self.levels[li].tx_write - self.levels[li].tx_send)
        hdr := CryptoHeaderLen(self.levels[li].tx_send, unsent)
        if off + hdr < limit {
            room := limit - off - hdr
            var take := if unsent < room { unsent } else { room }
            if take > 1500 {
                take = 1500
            }
            if take > 0 {
                rel := Cast[int](self.levels[li].tx_send - self.levels[li].tx_base)
                var scratch: [1500]u8 = [0; 1500]
                self.levels[li].tx_ring.CopyAt(rel, mut scratch[0..take])
                w := WriteCrypto(mut out[off..limit], self.levels[li].tx_send, scratch[0..take]) else { 0 }
                if w > 0 {
                    send_off := self.levels[li].tx_send
                    self.levels[li].tx_send += Cast[u64](take)
                    off += w
                    frames[fn_cnt] = SentFrame(kind: SentKind.Crypto(level: level, offset: send_off, len: take))
                    fn_cnt += 1
                }
            }
        }
    }
    return (n: off, frames: fn_cnt)
}

// Processes acknowledgment of a CRYPTO SentFrame.
CryptoStreams.OnAcked(mut self, a: &Alloc, f: &SentFrame) {
    match f.kind {
        .Crypto(level, offset, len) {
            li := SpaceIndex(level)
            if self.levels[li].discarded {
                return
            }
            end := offset + Cast[u64](len)
            if end <= self.levels[li].tx_base {
                return
            }
            lo := if offset > self.levels[li].tx_base { offset } else { self.levels[li].tx_base }
            SubtractRange(a, mut self.levels[li].tx_lost, lo, end) else {
                Assert(false)
            }
            MergeRange(a, mut self.levels[li].tx_acked, lo, end) else {
                Assert(false)
            }
            if self.levels[li].tx_acked.Len() > 0 && self.levels[li].tx_acked[0].lo == self.levels[li].tx_base {
                front := self.levels[li].tx_acked.Remove(0)
                adv := Cast[int](front.hi - self.levels[li].tx_base)
                self.levels[li].tx_ring.DropFront(adv)
                self.levels[li].tx_base = front.hi
            }
        }
        _ {}
    }
}

// Processes loss of a CRYPTO SentFrame (re-queues unacknowledged bytes).
CryptoStreams.OnLost(mut self, a: &Alloc, f: &SentFrame) {
    match f.kind {
        .Crypto(level, offset, len) {
            li := SpaceIndex(level)
            if self.levels[li].discarded {
                return
            }
            end := offset + Cast[u64](len)
            if end <= self.levels[li].tx_base {
                return
            }
            lo := if offset > self.levels[li].tx_base { offset } else { self.levels[li].tx_base }
            MergeRange(a, mut self.levels[li].tx_lost, lo, end) else {
                Assert(false)
            }
            for i in 0..self.levels[li].tx_acked.Len() {
                ar := self.levels[li].tx_acked[i]
                SubtractRange(a, mut self.levels[li].tx_lost, ar.lo, ar.hi) else {
                    Assert(false)
                }
            }
        }
        _ {}
    }
}

// Drops Initial or Handshake CRYPTO send and receive buffers when that level's
// keys are discarded (RFC 9000 13.3). O(1).
CryptoStreams.Discard(mut self, level: Space) {
    if level == Space.App {
        return
    }
    li := SpaceIndex(level)
    self.levels[li].discarded = true
    self.levels[li].rx_contig.Clear()
    self.levels[li].rx_ooo.Clear()
    self.levels[li].tx_ring.Clear()
    self.levels[li].tx_acked.Clear()
    self.levels[li].tx_lost.Clear()
}

// Returns the highest CRYPTO byte offset received at level (for RFC 9001 4.1.3).
CryptoStreams.MaxOffset(&self, level: Space): u64 {
    return self.levels[SpaceIndex(level)].max_offset
}

// Returns unread contiguous bytes buffered at level (for RFC 9001 4.1.3).
CryptoStreams.UnreadBytes(&self, level: Space): int {
    return self.levels[SpaceIndex(level)].rx_contig.len
}

// Reports whether CRYPTO data or lost CRYPTO ranges are queued to send at level. O(1).
CryptoStreams.HasPending(&self, level: Space): bool {
    li := SpaceIndex(level)
    if self.levels[li].discarded {
        return false
    }
    return self.levels[li].tx_lost.Len() > 0 || self.levels[li].tx_send < self.levels[li].tx_write
}

// Resets send cursors at level so the identical CRYPTO bytes are resent from
// offset 0 after a Retry packet (RFC 9000 17.2.5.3). O(1).
CryptoStreams.ResetTx(mut self, level: Space) {
    li := SpaceIndex(level)
    self.levels[li].tx_send = 0
    self.levels[li].tx_base = 0
    self.levels[li].tx_acked.Clear()
    self.levels[li].tx_lost.Clear()
}
