// Why: QPACK separates dynamic-table updates and acknowledgments onto
// unidirectional encoder and decoder streams (RFC 9204 4.2-4.4) that can be
// split across transport reads; both decoders and our static-only encoder
// must parse and emit these instructions cleanly without buffering cuts.
//
// What: QDecoder.Instructions, QDecoder.Owed, and QEncoder.PeerInstructions.
//
// How: Instructions walks complete encoder-stream instructions in b and stops
// before any instruction cut at the end, returning the bytes consumed; Owed
// serializes Section Acknowledgment, Stream Cancellation, and coalesced
// Insert Count Increment instructions into the caller's slice;
// PeerInstructions validates decoder-stream bytes against an encoder that
// never inserted (accepting Stream Cancellation, rejecting Section
// Acknowledgment and Insert Count Increment as BadInstruction).

// QStringCheck fails early with CapacityExceeded when an incomplete string's
// minimum decoded length already cannot fit in the table capacity alongside
// other_len + EntryOverhead, avoiding unbounded stalling on a truncated stream.
QCheckStringFit(cap: int, other_len: int, raw_len: int, huff: bool): CapacityExceeded!void {
    room := cap - EntryOverhead - other_len
    if room < 0 {
        fail CapacityExceeded(size: EntryOverhead + other_len)
    }
    min_len := if huff { (raw_len * 8) / 30 } else { raw_len }
    if min_len > room {
        fail CapacityExceeded(size: EntryOverhead + other_len + min_len)
    }
}

// QReadStrLen reads the Huffman bit and (n)-bit prefix string length at pos,
// checking that the string could fit in cap and returning none if b ends
// before the full string bytes arrive.
QReadStrHead(b: []u8, pos: int, n: int, cap: int, other_len: int): QpackError!?(huff: bool, start: int, end: int) {
    if pos >= b.Len() {
        return none
    }
    huff := (int(b[pos]) >> n) & 1 == 1
    r := QGetInt(b, pos, n) else e {
        return match e {
            Truncated(offset: _) { none }
            BadInteger(offset) { fail BadInteger(offset: offset) }
        }
    }
    try QCheckStringFit(cap, other_len, r.value, huff)
    if r.value > b.Len() - r.next {
        return none
    }
    return some((huff: huff, start: r.next, end: r.next + r.value))
}

// QDecodeVal validates and decodes a string from b[start..end] into out,
// returning the decoded length.
QDecodeStr(out: mut []u8, b: []u8, huff: bool, start: int, end: int, err_at: int): BadHuffman!int {
    src := b[start..end]
    if !huff {
        (mut out[0..src.Len()]).CopyFrom(src)
        return src.Len()
    }
    got := HuffDecode(out, src) else {
        fail BadHuffman(offset: err_at)
    }
    return got
}

// QDecodedLen validates a string's Huffman encoding (if huff) and returns its
// decoded byte length.
QDecodedLen(b: []u8, huff: bool, start: int, end: int, err_at: int): BadHuffman!int {
    if !huff {
        return end - start
    }
    got := HuffValid(b[start..end]) else {
        fail BadHuffman(offset: err_at)
    }
    return got
}

/// Feeds encoder-stream bytes from b into self's dynamic table and returns how
/// many bytes were consumed (an incomplete instruction at the end of b stays
/// for the caller's next call). Errors (RFC 9204 4.3, 6): BadInteger,
/// BadIndex, BadHuffman, CapacityExceeded (all QPACK_ENCODER_STREAM_ERROR),
/// or OutOfMemory. O(bytes); allocates nothing once scratch covers table max.
export QDecoder.Instructions(mut self, a: &Alloc, b: []u8): QpackError|OutOfMemory!int {
    var at := 0
    n := b.Len()
    for at < n {
        c := b[at]
        if c & 0x80 != 0 {
            // 4.3.2: Insert with Name Reference ('1' T + 6-bit index, 8-bit value).
            stat := (c & 0x40) != 0
            r := QGetInt(b, at, 6) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            ni := r.value
            var abs := 0
            var name_len := 0
            var nidx := 0
            if stat {
                if ni < 0 || ni >= QStaticLen {
                    fail BadIndex(offset: at, index: ni)
                }
                name_len = QStaticTable[ni].name.Len()
                nidx = int(QFirstIndex[ni])
            } else {
                abs = self.table.inserted - 1 - ni
                if ni < 0 || !self.table.Has(abs) {
                    fail BadIndex(offset: at, index: ni)
                }
                name_len = self.table.NameLen(abs)
                nidx = self.table.NameIndex(abs)
            }
            vh := try QReadStrHead(b, r.next, 7, self.table.cap, name_len) else {
                return at
            }
            val_len := try QDecodedLen(b, vh.huff, vh.start, vh.end, r.next)
            need := EntrySize(name_len, val_len)
            if need > self.table.cap {
                fail CapacityExceeded(size: need)
            }
            try Grow(mut self.scratch, a, name_len + val_len, self.table.max)
            sc := self.scratch.ItemsMut()
            // Copy name into scratch before Fit evicts, in case abs is evicted.
            if stat {
                (mut sc[0..name_len]).CopyFrom(QStaticTable[ni].name.AsBytes())
            } else {
                self.table.CopyName(abs, mut sc[0..name_len])
            }
            _ = try QDecodeStr(mut sc[name_len..name_len + val_len], b, vh.huff, vh.start, vh.end, r.next)
            _ = self.table.Fit(need)
            sb := self.scratch.Items()
            self.table.Add(sb[0..name_len], sb[name_len..name_len + val_len], nidx)
            at = vh.end
        } else if c & 0xC0 == 0x40 {
            // 4.3.3: Insert with Literal Name ('01' H + 5-bit name, 8-bit value).
            nh := try QReadStrHead(b, at, 5, self.table.cap, 0) else {
                return at
            }
            name_len := try QDecodedLen(b, nh.huff, nh.start, nh.end, at)
            if EntrySize(name_len, 0) > self.table.cap {
                fail CapacityExceeded(size: EntrySize(name_len, 0))
            }
            vh := try QReadStrHead(b, nh.end, 7, self.table.cap, name_len) else {
                return at
            }
            val_len := try QDecodedLen(b, vh.huff, vh.start, vh.end, nh.end)
            need := EntrySize(name_len, val_len)
            if need > self.table.cap {
                fail CapacityExceeded(size: need)
            }
            try Grow(mut self.scratch, a, name_len + val_len, self.table.max)
            sc := self.scratch.ItemsMut()
            _ = try QDecodeStr(mut sc[0..name_len], b, nh.huff, nh.start, nh.end, at)
            _ = try QDecodeStr(mut sc[name_len..name_len + val_len], b, vh.huff, vh.start, vh.end, nh.end)
            _ = self.table.Fit(need)
            sb := self.scratch.Items()
            nb := sb[0..name_len]
            self.table.Add(nb, sb[name_len..name_len + val_len], QStaticNameIndex(nb))
            at = vh.end
        } else if c & 0xE0 == 0x20 {
            // 4.3.1: Set Dynamic Table Capacity ('001' + 5-bit capacity).
            r := QGetInt(b, at, 5) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            if r.value > self.table.max {
                fail CapacityExceeded(size: r.value)
            }
            self.table.SetCap(r.value)
            at = r.next
        } else {
            // 4.3.4: Duplicate ('000' + 5-bit relative index).
            r := QGetInt(b, at, 5) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            abs := self.table.inserted - 1 - r.value
            if r.value < 0 || !self.table.Has(abs) {
                fail BadIndex(offset: at, index: r.value)
            }
            nl := self.table.NameLen(abs)
            vl := self.table.ValueLen(abs)
            need := EntrySize(nl, vl)
            if need > self.table.cap {
                fail CapacityExceeded(size: need)
            }
            nidx := self.table.NameIndex(abs)
            try Grow(mut self.scratch, a, nl + vl, self.table.max)
            sc := self.scratch.ItemsMut()
            self.table.CopyName(abs, mut sc[0..nl])
            self.table.CopyValue(abs, mut sc[nl..nl + vl])
            _ = self.table.Fit(need)
            sb := self.scratch.Items()
            self.table.Add(sb[0..nl], sb[nl..nl + vl], nidx)
            at = r.next
        }
    }
    return at
}

/// Writes owed decoder-stream instructions (Section Acknowledgment, Stream
/// Cancellation, and coalesced Insert Count Increment) into out from out[0]
/// up to out.Len() and returns the byte count written; instructions that do
/// not fit remain owed for the next call. O(owed); allocates nothing.
export QDecoder.Owed(mut self, out: mut []u8): int {
    var o := 0
    for self.acks.Len() > 0 {
        ack := self.acks[0]
        need := IntLen(int(ack.stream), 7)
        if o + need > out.Len() {
            return o
        }
        o += PutInt(mut out[o..], int(ack.stream), 7, 0x80)
        if ack.ric > self.known_received {
            self.known_received = ack.ric
        }
        _ = self.acks.Remove(0)
    }
    for self.cancel_count > 0 {
        sid := self.cancels[self.cancel_head]
        need := IntLen(int(sid), 6)
        if o + need > out.Len() {
            return o
        }
        o += PutInt(mut out[o..], int(sid), 6, 0x40)
        self.cancel_head = (self.cancel_head + 1) % self.cancels.Len()
        self.cancel_count -= 1
    }
    if self.table.inserted > self.known_received {
        inc := self.table.inserted - self.known_received
        need := IntLen(inc, 6)
        if o + need > out.Len() {
            return o
        }
        o += PutInt(mut out[o..], inc, 6, 0x00)
        self.known_received = self.table.inserted
    }
    return o
}

/// Validates decoder-stream bytes b received from a peer when our encoder
/// never inserts into the dynamic table (RFC 9204 4.4): returns bytes
/// consumed (any incomplete instruction at the end of b stays for the next
/// call). Stream Cancellation is accepted; Section Acknowledgment and Insert
/// Count Increment fail with BadInstruction (QPACK_DECODER_STREAM_ERROR).
/// O(bytes); allocates nothing.
export QEncoder.PeerInstructions(&self, b: []u8): QpackError!int {
    _ = self
    var at := 0
    n := b.Len()
    for at < n {
        c := b[at]
        if c & 0x80 != 0 {
            // 4.4.1: Section Acknowledgment ('1' + 7-bit stream id).
            _ = QGetInt(b, at, 7) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            fail BadInstruction(offset: at)
        } else if c & 0xC0 == 0x40 {
            // 4.4.2: Stream Cancellation ('01' + 6-bit stream id).
            r := QGetInt(b, at, 6) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            at = r.next
        } else {
            // 4.4.3: Insert Count Increment ('00' + 6-bit increment).
            _ = QGetInt(b, at, 6) else e {
                match e {
                    Truncated(offset: _) { return at }
                    BadInteger(offset) { fail BadInteger(offset: offset) }
                }
            }
            fail BadInstruction(offset: at)
        }
    }
    return at
}
