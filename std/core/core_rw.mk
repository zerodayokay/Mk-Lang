// Why: every byte of output and input passes through Writer and Reader, so
// the common case (a small write, a buffered read) must cost one compare and
// one copy, while files, sockets, text and devices plug in behind one table.
//
// What: Writer and WriterTable, Reader and ReaderTable with Read, ReadAll,
// Until and Reader.Of; the ports (ReadPort, ReadPortMut, WritePort,
// WritePortMut, FilterPort, FilterPortGrow) that build a Reader or Writer
// over a module's own state; Capped, a bounded record writer; and Sink, the
// table for a device.
//
// How: each is a view over an inline buffer and a table; the fast path copies
// into the buffer and the table is called only when the buffer cannot take
// the bytes. ctx is a pointer (a text writer's Text, a process stream's
// BufState), so no address arithmetic is needed and formatting into text runs
// at compile time; NewWriter and NewReader keep a uint handle, which
// WriterCtx and ReaderCtx give back. A port's entries recover the state and
// function from the Reader's ctx and table, so the module that uses it needs
// no unsafe code.

import std.raw

/// The two functions behind a Writer: write takes one run of bytes (the
/// buffered ones, or new ones too large to buffer) and sends it on; flush
/// forces out what the destination still holds.
export struct WriterTable {
    export write: fn(mut Writer, []u8): WriteError!void
    export flush: fn(mut Writer): WriteError!void
}

/// A destination for bytes: an optional inline buffer, and a table that
/// takes what the buffer cannot hold. Print and the Format methods write
/// to one. System.Stdout, File.Writer, Text.Writer and the network streams
/// hand them out; a Writer borrows what it writes to.
///
/// A write interrupted by a cancel or a deadline fails with WriteError;
/// whether it was a timeout is read from the scope (Scope.Expired). Over a
/// stream, every Writer method is prefix under a cancel: an unknown part
/// of the bytes went out, so close the stream.
export view struct Writer {
    io: Io
    table: &WriterTable
    ctx: *u8
    buf: *u8
    used: int
    cap: int
}

/// The two functions behind a Reader.
///
/// read copies at most buf.Len() bytes into buf and returns how many, 0 at
/// end of input. line does the same but stops after the first \n it
/// copies, so a reader with no inline buffer (stdin) serves Reader.Line
/// with one call per line instead of one per byte. A source with no faster
/// way uses ReadLineBytewise as its line.
///
/// An entry should not call a method of the Reader it is given (no std
/// entry does). One that does still cannot take the Reader past its
/// buffer: Read and Until reset the buffered range after their own fills,
/// and Reader.Line fails with ReadError when an entry moved it.
export struct ReaderTable {
    export read: fn(mut Reader, mut []u8): ReadError!int
    export line: fn(mut Reader, mut []u8): ReadError!int
}

// The invariants the raw copies below rest on. A Writer keeps
// 0 <= used <= cap; a Reader keeps 0 <= start <= end; and buf holds cap
// live bytes, the inline buffer whose loan the Writer or Reader holds
// (none when cap is 0). A Reader with cap > 0 keeps end <= cap. A Reader
// with cap 0 keeps start == end == 0, except a Reader.Of, whose
// buf holds its b's end bytes under b's shared loan: b's bytes are
// buffered in place, read and never written, since every write into buf
// is inside buf[0..cap] (a fill, or Line's compaction, which runs only
// when cap > 0), and its entries move only start, by at most
// end - start. So buf always holds end live bytes, which is what the
// served copies of Read and Until read. The fields are std.core's: every
// other constructor (here, core_fmt.mk's and text_text.mk's) sets used,
// start and end to 0 and cap to buf's length, and only Writer.Bytes,
// BytesSlow and Drain, Reader.Read, Until and UntilSlow here, Reader.Of's
// entries and Reader.Line (text_text.mk) change them, each keeping both;
// Line with cap 0 goes to the line entry and changes none. A table entry
// is given the Reader itself, so it can call the Reader's own methods and
// move start and end while Read, Until or Line waits for it (no std entry
// does): Read and UntilSlow, which makes Until's fills, set start and end
// together after every fill (Until's served case makes none), so their
// copies hold even then; Line adds the fill's count to the end it saw
// before the fill, so it fails with ReadError when start or end moved
// (start is 0 at each fill and at its probe read), and otherwise end stays
// at or under cap, since the count is at most cap minus that end. So every
// later Read and Until on the Reader holds too.

/// A source of bytes: an optional inline buffer, and a table that refills
/// it. System.Stdin, File.Reader, Reader.Of and the network streams hand
/// them out; a Reader borrows what it reads from. Read, ReadAll, Until and
/// Line read from one.
///
/// A read interrupted by a cancel or a deadline fails with ReadError;
/// whether it was a timeout is read from the scope (Scope.Expired).
export view struct Reader {
    io: Io
    table: &ReaderTable
    ctx: *u8
    buf: *u8
    start: int
    end: int
    cap: int
}

/// Writes the bytes of s, as Bytes does. Fails with the table's WriteError.
/// O(s.Len()); allocates nothing.
export Writer.Str(mut self, s: str): WriteError!void {
    try self.Bytes(s.AsBytes())
}

/// Writes b. Fails with the table's WriteError. O(b.Len()); allocates
/// nothing.
///
/// When b fits in the free part of the inline buffer, it is copied there:
/// one compare and one copy, no table call. When nothing is buffered and b
/// is larger than the buffer, b goes to the table whole, uncopied; every
/// write of a writer with no buffer (a process stream, a Text) takes this
/// path, one table call. Otherwise the buffer is filled up and handed to
/// the table, then the rest is buffered or, when it still does not fit,
/// handed over whole: at most two table calls. An empty b copies nothing
/// and makes no call.
///
/// A failed table write drops the bytes it was given, and the buffer is
/// empty afterwards, so a later write starts clean instead of failing again
/// on the same bytes.
export Writer.Bytes(mut self, b: []u8): WriteError!void {
    n := b.Len()
    if n > self.cap -% self.used {
        if self.used == 0 {
            write := self.table.write
            try write(self, b)
            return
        }
        try self.BytesSlow(b)
        return
    }
    // A raw copy, not RwBytes and CopyFrom: on the fast path their bounds
    // checks and call cost about 4 ns a write (100-byte writes to stdout),
    // and n <= cap - used was just checked.
    unsafe "buf holds cap bytes and used + n <= cap" {
        raw.Copy(raw.Offset(self.buf, self.used), raw.Data(b), n)
    }
    self.used = self.used +% n
}

// BytesSlow is Bytes when b does not fit what is free, by Go bufio.Writer's
// rule: top the buffer up and hand it over full, so every table call while
// streaming carries cap bytes; once the buffer is empty, a rest that still
// exceeds it goes to the table directly, uncopied. Each pass either fills
// and drains the buffer (used > 0) or hands every remaining byte over at
// once (used = 0), so a call makes at most two table calls when cap > 0 and
// exactly one for a writer with no buffer (a process stream, a Text). A
// table entry sees whole byte runs, never the buffer itself.
Writer.BytesSlow(mut self, b: []u8): WriteError!void {
    var off := 0
    n := b.Len()
    for n - off > self.cap - self.used {
        if self.used == 0 {
            write := self.table.write
            try write(self, b[off..])
            return
        }
        k := self.cap - self.used
        dst := RwBytes(self.buf, self.cap)
        (mut dst[self.used..self.cap]).CopyFrom(b[off..off + k])
        self.used = self.cap
        off += k
        try self.Drain()
    }
    rest := n - off
    dst := RwBytes(self.buf, self.cap)
    (mut dst[self.used..self.used + rest]).CopyFrom(b[off..n])
    self.used += rest
}

// RwBytes is the n bytes at p as a slice: the raw view of a buffer for a
// table call, a drain and BytesSlow's copies (Until's and UntilSlow's
// scans read through RoBytes). The served copies of Read, Until and
// UntilSlow, Bytes's fast path and CappedWrite copy raw instead, each
// behind its own explicit compare. Every caller passes a Writer's or
// Reader's inline buffer (or a Capped's) and n <= cap, so the bytes are
// live and the holder has their loan (NewWriter and NewReader are from
// buf); for a Reader.Of, whose buf is b's data and cap 0, UntilSlow's
// refill view is zero-length, so nothing is written through it. The slice
// bounds checks of the callers then guard every copy through it.
RwBytes(p: *u8, n: int): mut []u8 {
    unsafe "p is a Writer's or Reader's inline buffer and n <= its cap" {
        return raw.SliceMut(p, n)
    }
}

// RoBytes is the n bytes at p as a shared slice: the read-only view of a
// Reader's buffered bytes for Until's and UntilSlow's scans and Reader.Of's
// entries. Every caller passes a Reader's buf and n <= end, the bytes it
// holds (Reader's invariant), whose loan the Reader holds; a Reader.Of's
// are its b, shared, so they are never viewed mut. Layer 0's own, as
// ByteView is layer 1's.
RoBytes(p: *u8, n: int): []u8 {
    unsafe "p is a Reader's buf and n <= its end, bytes it holds" {
        return raw.Slice(p, n)
    }
}

// Drain hands the buffered bytes to the table and empties the buffer. The
// buffer is emptied before the call, so a failed write drops those bytes
// and the error says so; a later write starts clean instead of failing
// forever on the same bytes (Go's bufio keeps them and the error sticks).
Writer.Drain(mut self): WriteError!void {
    if self.used == 0 {
        return
    }
    write := self.table.write
    n := self.used
    self.used = 0
    held := RwBytes(self.buf, n)
    try write(self, held[0..n])
}

/// Writes out what is buffered, then calls the table's flush. Fails with
/// the table's WriteError; the buffered bytes are dropped then too. Flush a
/// Writer with a buffer before dropping it: a dropped Writer's buffered
/// bytes are lost. O(buffered bytes) plus one table write and the flush;
/// allocates nothing.
export Writer.Flush(mut self): WriteError!void {
    try self.Drain()
    flush := self.table.flush
    try flush(self)
}

/// Reads at most buf.Len() bytes into buf and returns how many, 0 at end of
/// input. It may return fewer than asked; ReadAll loops until buf is full.
/// Fails with the table's ReadError. O(count) plus at most one table read;
/// allocates nothing.
///
/// Buffered bytes are served first. Otherwise it makes one table read:
/// straight into buf, with no copy, when buf is at least as large as the
/// inline buffer, else into the inline buffer to serve from. A table that
/// reports a negative count or more bytes than it was given breaks its
/// contract: ReadError, never a silent end of input.
///
/// On a buffered reader Read is repeatable: a call interrupted by a cancel
/// consumed nothing, and calling it again is safe.
export Reader.Read(mut self, buf: mut []u8): ReadError!int {
    want := buf.Len()
    if want == 0 {
        return 0
    }
    if self.start == self.end {
        read := self.table.read
        if want >= self.cap {
            got := try read(self, buf)
            if got < 0 || got > want {
                fail ReadError
            }
            return got
        }
        self.start = 0
        self.end = 0
        got := try read(self, RwBytes(self.buf, self.cap))
        if got < 0 || got > self.cap {
            fail ReadError
        }
        if got == 0 {
            return 0
        }
        // start again with end: an entry that called this Reader's own
        // methods may have moved it, and the raw copy below needs start <=
        // end.
        self.start = 0
        self.end = got
    }
    var n := self.end - self.start
    if n > want {
        n = want
    }
    // A raw copy, as Writer.Bytes makes: RwBytes and CopyFrom here cost
    // two slice checks, CopyFrom's length compare and an inline byte loop
    // with a runtime overlap test on every served Read, and n <=
    // end - start and n <= want were just made so.
    unsafe "self.buf holds end live bytes and 0 <= start <= end (Reader's invariant), so the n <= end - start bytes at start are the Reader's own: its inline buffer, or a Reader.Of's b; n <= want, buf's length; buf is the caller's mut slice, apart from those bytes, whose loan the Reader holds" {
        raw.Copy(raw.DataMut(buf), raw.Offset(self.buf, self.start), n)
    }
    self.start += n
    return n
}

/// A ReaderTable line entry for a source that cannot look ahead: one read
/// call per byte, up to and including the first \n. Only a reader with no
/// inline buffer reaches its line entry, and a source that cannot give
/// bytes back cannot be read past the newline any other way; a buffered
/// Reader.Line scans its buffer instead. A count other than 0 or 1 from the
/// read entry breaks its contract: ReadError. A read that fails after some
/// bytes loses them, since they have left the source, so through it an
/// unbuffered Reader's Line is prefix: an interrupted call loses what it
/// took. O(count) read calls; allocates nothing.
export ReadLineBytewise(r: mut Reader, buf: mut []u8): ReadError!int {
    read := r.table.read
    var got := 0
    for got < buf.Len() {
        n := try read(r, mut buf[got..got + 1])
        if n == 0 {
            break
        }
        if n != 1 {
            fail ReadError
        }
        got += 1
        if buf[got - 1] == 0x0A {
            break
        }
    }
    return got
}

/// Reads until buf is full or the input ends, and returns the count read:
/// less than buf.Len() only at end of input. Fails with the table's
/// ReadError; the bytes read before the failure are in buf but not
/// counted, so the call is prefix under a cancel. O(buf.Len()) plus a table
/// read per Read call; allocates nothing.
export Reader.ReadAll(mut self, buf: mut []u8): ReadError!int {
    var got := 0
    for got < buf.Len() {
        n := try self.Read(mut buf[got..])
        if n == 0 {
            break
        }
        got += n
    }
    return got
}

/// Reads into buf up to and including the first delim and returns the count.
/// Fails with the table's ReadError. O(count); allocates nothing.
///
/// It stops after delim or when buf is full, refilling the inline buffer as
/// needed, and returns 0 at end of input. At end of input after some bytes
/// it returns them, and the next call returns 0. It never takes a byte past
/// delim from the source: a buffered reader keeps what follows delim in its
/// buffer, and a reader with no inline buffer asks its table for one byte
/// at a time (for \n, the table's line entry, which stops after the
/// newline). A Reader.Of is scanned in place, as a buffered reader is.
/// Each buffered byte is searched once. A table that reports a negative
/// count or more bytes than it was given is ReadError. An interrupted call
/// is prefix: the bytes it took are lost.
export Reader.Until(mut self, delim: u8, buf: mut []u8): ReadError!int {
    // The served case first, in a small frame with no table call: bytes
    // are buffered and buf is not empty. This is the first pass of
    // UntilSlow's loop with got 0 (no refill, since start < end), so a hit
    // or a full buf returns what that pass returns; otherwise every
    // buffered byte was searched and copied, start == end, and UntilSlow
    // goes on from take, its next pass a refill, so no byte is searched
    // twice.
    s := self.start
    e := self.end
    want := buf.Len()
    if s < e && want > 0 {
        var take := e - s
        if take > want {
            take = want
        }
        // The scan's view ends at s + take <= e, inside the bytes buf
        // holds, and is read only (a Reader.Of's b is shared).
        if i := IndexByteIn(RoBytes(self.buf, s + take), delim, s) {
            k := i + 1 - s
            unsafe "self.buf holds end live bytes and 0 <= start < end (Reader's invariant and the compare above; s and e are start and end): its inline buffer, or a Reader.Of's b; take is the least of e - s and want; IndexByteIn's i is in [s, s + take) (its contract: at or after from, inside its slice), so 1 <= k <= take <= e - s and those k bytes at s are the Reader's own; k <= take <= want, buf's length; buf is the caller's mut slice, apart from those bytes, whose loan the Reader holds" {
                raw.Copy(raw.DataMut(buf), raw.Offset(self.buf, s), k)
            }
            self.start = s + k
            return k
        }
        unsafe "self.buf holds end live bytes and 0 <= start < end (Reader's invariant and the compare above; s and e are start and end): its inline buffer, or a Reader.Of's b; take is the least of e - s and want, and 1 <= take since s < e and want > 0, so the take bytes at s are the Reader's own; take <= want, buf's length; buf is the caller's mut slice, apart from those bytes, whose loan the Reader holds" {
            raw.Copy(raw.DataMut(buf), raw.Offset(self.buf, s), take)
        }
        self.start = s + take
        if take == want {
            return take
        }
        return try self.UntilSlow(delim, buf, take)
    }
    return try self.UntilSlow(delim, buf, 0)
}

// UntilSlow is Until past its served case, with got bytes of buf already
// filled: got is 0 (nothing buffered, or buf empty), or 0 < got < buf's
// length after the served case took every buffered byte (start == end), so
// the cap-0 paths, which start from an empty buf, are for got 0 alone, and
// the loop's next pass is a refill.
Reader.UntilSlow(mut self, delim: u8, buf: mut []u8, got0: int): ReadError!int {
    want := buf.Len()
    if want == 0 {
        return 0
    }
    read := self.table.read
    // With cap 0, start == end except in a Reader.Of with bytes left
    // (Reader's invariant), which takes the scan below (or Until's served
    // case); its refill there is a zero-length view, so its entry returns
    // 0 and Until returns.
    if got0 == 0 && self.cap == 0 && self.start == self.end {
        if delim == 0x0A {
            line := self.table.line
            got := try line(self, buf)
            if got < 0 || got > want {
                fail ReadError
            }
            return got
        }
        var got := 0
        for got < want {
            n := try read(self, mut buf[got..got + 1])
            if n == 0 {
                break
            }
            if n != 1 {
                fail ReadError
            }
            got += 1
            if buf[got - 1] == delim {
                break
            }
        }
        return got
    }
    var got := got0
    for got < want {
        if self.start == self.end {
            self.start = 0
            self.end = 0
            n := try read(self, RwBytes(self.buf, self.cap))
            if n < 0 || n > self.cap {
                fail ReadError
            }
            if n == 0 {
                return got
            }
            self.start = 0 // as in Read: the entry may have moved it
            self.end = n
        }
        var take := self.end - self.start
        if take > want - got {
            take = want - got
        }
        // The scan's view ends at start + take <= end, inside the bytes buf
        // holds, and is read only (a Reader.Of's b is shared); every copy
        // is a raw one, as Read's: k <= take <= want - got.
        var k := take
        var hit := false
        if i := IndexByteIn(RoBytes(self.buf, self.start + take), delim, self.start) {
            k = i + 1 - self.start
            hit = true
        }
        unsafe "self.buf holds end live bytes and 0 <= start <= end (Reader's invariant): its inline buffer, or a Reader.Of's b; IndexByteIn's i is in [start, start + take) (its contract: at or after from, inside its slice), so 1 <= k <= take <= end - start and those bytes at start are the Reader's own; got + k <= want, buf's length; buf is the caller's mut slice, apart from those bytes, whose loan the Reader holds" {
            raw.Copy(raw.Offset(raw.DataMut(buf), got), raw.Offset(self.buf, self.start), k)
        }
        self.start += k
        got += k
        if hit {
            return got
        }
    }
    return got
}

/// Returns a Writer over the table t, which must point at a global table,
/// with buf as its inline buffer and ctx as the handle the table's entries
/// read back with WriterCtx. Std-internal. O(1); allocates nothing.
export NewWriter(io: Io, t: *WriterTable, ctx: uint, buf: mut []u8): Writer from buf {
    cap := buf.Len()
    unsafe "t points at a global table; buf outlives the Writer; ctx is a handle, never dereferenced" {
        return Writer(io: io, table: &*t, ctx: raw.FromAddr[u8](ctx), buf: raw.DataMut(buf), used: 0, cap: cap)
    }
}

/// Returns a Reader over the table t, which must point at a global table,
/// with buf as its inline buffer and ctx as the handle the table's entries
/// read back with ReaderCtx. Std-internal. t's entries should not call a
/// method of the Reader they are given; ReaderTable says what happens if
/// one does. O(1); allocates nothing.
export NewReader(io: Io, t: *ReaderTable, ctx: uint, buf: mut []u8): Reader from buf {
    cap := buf.Len()
    unsafe "t points at a global table; buf outlives the Reader; ctx is a handle, never dereferenced" {
        return Reader(io: io, table: &*t, ctx: raw.FromAddr[u8](ctx), buf: raw.DataMut(buf), start: 0, end: 0, cap: cap)
    }
}

// WriterOver is a writer with no inline buffer over the table t of std.core
// and its context ctx: every write is one table call (a process stream, a
// Text). t is a const table, so the loan is static.
WriterOver(io: Io, t: &WriterTable, ctx: *u8): Writer from t {
    return Writer(io: io, table: t, ctx: ctx, buf: ctx, used: 0, cap: 0)
}

// ReaderOver is WriterOver for a Reader.
ReaderOver(io: Io, t: &ReaderTable, ctx: *u8): Reader from t {
    return Reader(io: io, table: t, ctx: ctx, buf: ctx, start: 0, end: 0, cap: 0)
}

/// Returns a Reader over the bytes of b, from the first. Read copies them
/// out with no table call and returns 0 once every byte is read, Until
/// scans b in place, and Line reads as on a reader with no buffer (one
/// table call per line, lines up to 64 KiB, as a Stdin() reader's). It
/// never writes b and holds b's loan. It takes no Io, so it runs at compile
/// time. O(1); allocates nothing.
export Reader.Of(b: []u8): Reader from b {
    // b's bytes are buf[start..end] with cap 0, so no path writes them
    // (every write into buf is a fill of buf[0..cap]; Reader's invariant
    // above). InertIoValue, as Text.Writer: the evaluator refuses a call
    // whose signature takes an Io, and this one runs at compile
    // time.
    unsafe "b's data pointer, kept as the Reader's buf under b's shared loan, which the Reader holds; with cap 0 no std path writes through it" {
        return Reader(io: InertIoValue(), table: &BytesReadTable, ctx: raw.Data(b), buf: raw.Data(b), start: 0, end: b.Len(), cap: 0)
    }
}

// The table of every Reader.Of, and only of one (BytesReadTable is private
// here and Reader.Of the one maker of a Reader over it). Read serves
// buf[start..end] itself and Until scans it, so the entries run for Read
// and Until only once every byte is read (they return 0 then), and for
// Line, through LineUnbuffered, once per line.
const BytesReadTable = ReaderTable(read: BytesRead, line: BytesLine)

// BytesRead is a Reader.Of's read entry: the next bytes, at most
// dst.Len(), 0 at the end of b. r's buf holds b's end bytes and 0 <= start
// <= end (Reader's invariant), so the safe copy's checks never fail.
BytesRead(r: mut Reader, dst: mut []u8): ReadError!int {
    var n := r.end - r.start
    if n > dst.Len() {
        n = dst.Len()
    }
    (mut dst[0..n]).CopyFrom(RoBytes(r.buf, r.end)[r.start..r.start + n])
    r.start += n
    return n
}

// BytesLine is BytesRead stopping after the first \n it copies, the line
// entry LineUnbuffered calls once per line.
BytesLine(r: mut Reader, dst: mut []u8): ReadError!int {
    var n := r.end - r.start
    if n > dst.Len() {
        n = dst.Len()
    }
    bytes := RoBytes(r.buf, r.end)
    if i := IndexByteIn(bytes[0..r.start + n], 0x0A, r.start) {
        n = i + 1 - r.start
    }
    (mut dst[0..n]).CopyFrom(bytes[r.start..r.start + n])
    r.start += n
    return n
}

/// Builds Readers over a shared state of type S from one fill function, so
/// the module that uses it needs no unsafe code: a std module keeps one as
/// a const and makes its Readers with Reader (net.Conn and tls.Conn do).
/// Plain; its fields are std.core's, so no other table can pose as one.
export struct ReadPort[S] {
    table: ReaderTable
    fill: fn(&S, Io, mut []u8): ReadError!int
}

/// Returns a port over fill, which reads at most dst.Len() bytes from the
/// state into dst and returns how many, 0 at end of input. O(1); allocates
/// nothing; runs at compile time, so a const holds it.
export ReadPort[S].New(fill: fn(&S, Io, mut []u8): ReadError!int): ReadPort[S] {
    return ReadPort[S](table: ReaderTable(read: PortRead[S], line: ReadLineBytewise), fill: fill)
}

/// Returns a Reader over s that reads through self's fill, with buf as its
/// inline buffer. An empty buf means no buffer: every Read is one fill and
/// Line is one fill per byte. Read is then still repeatable, but Line and
/// Until are prefix: an interrupted call loses the bytes it took. O(1);
/// allocates nothing.
export ReadPort[S].Reader(&self, io: Io, s: &S, buf: mut []u8): Reader from self, s, buf {
    cap := buf.Len()
    unsafe "a pointer cast of s's address, kept as the Reader's ctx under s's loan, which the Reader holds" {
        return Reader(io: io, table: &self.table, ctx: raw.PtrCast[S, u8](raw.Of(s)), buf: raw.DataMut(buf), start: 0, end: 0, cap: cap)
    }
}

// PortRead is a ReadPort[S]'s read entry. Only ReadPort[S].New puts it in a
// table, as the first field of a ReadPort[S] (fields are laid out in
// declaration order), and only ReadPort[S].Reader makes a Reader over that
// field, with the S it was given as ctx.
PortRead[S](r: mut Reader, dst: mut []u8): ReadError!int {
    unsafe "r's table is the table field, at offset 0, of a live ReadPort[S] and r's ctx a live S, both under loans r holds (ReadPort.Reader is the only maker of such a Reader); only & is taken of either" {
        p := &*raw.PtrCast[ReaderTable, ReadPort[S]](raw.Of(r.table))
        s := &*raw.PtrCast[u8, S](r.ctx)
        fill := p.fill
        return try fill(s, r.io, dst)
    }
}

/// ReadPort for a state that a Reader holds exclusively (an HTTP body, a
/// terminal): fill takes mut S. Plain.
export struct ReadPortMut[S] {
    table: ReaderTable
    fill: fn(mut S, Io, mut []u8): ReadError!int
}

/// Returns a port over fill, as ReadPort.New does. O(1); allocates nothing;
/// runs at compile time.
export ReadPortMut[S].New(fill: fn(mut S, Io, mut []u8): ReadError!int): ReadPortMut[S] {
    return ReadPortMut[S](table: ReaderTable(read: PortReadMut[S], line: ReadLineBytewise), fill: fill)
}

/// Returns a Reader over s, which it holds exclusively, as ReadPort.Reader
/// does (an empty buf makes Line and Until prefix). O(1); allocates
/// nothing.
export ReadPortMut[S].Reader(&self, io: Io, s: mut S, buf: mut []u8): Reader from self, s, buf {
    cap := buf.Len()
    unsafe "a pointer cast of s's address, kept as the Reader's ctx under s's exclusive loan, which the Reader holds" {
        return Reader(io: io, table: &self.table, ctx: raw.PtrCast[S, u8](raw.OfMut(s)), buf: raw.DataMut(buf), start: 0, end: 0, cap: cap)
    }
}

// PortReadMut is PortRead for a ReadPortMut[S].
PortReadMut[S](r: mut Reader, dst: mut []u8): ReadError!int {
    unsafe "r's table is the table field, at offset 0, of a live ReadPortMut[S] and r's ctx an S under the exclusive loan r holds (ReadPortMut.Reader is the only maker of such a Reader), so this is the one mut use of it" {
        p := &*raw.PtrCast[ReaderTable, ReadPortMut[S]](raw.Of(r.table))
        s := mut *raw.PtrCast[u8, S](r.ctx)
        fill := p.fill
        return try fill(s, r.io, dst)
    }
}

/// Builds Writers over a shared state of type S from a write and a flush
/// function: write takes every byte of a run, flush forces out what the
/// state holds (net.Conn and tls.Conn use one). Plain.
export struct WritePort[S] {
    table: WriterTable
    write: fn(&S, Io, []u8): WriteError!void
    flush: fn(&S, Io): WriteError!void
}

/// Returns a port over write and flush. O(1); allocates nothing; runs at
/// compile time.
export WritePort[S].New(write: fn(&S, Io, []u8): WriteError!void, flush: fn(&S, Io): WriteError!void): WritePort[S] {
    return WritePort[S](table: WriterTable(write: PortWrite[S], flush: PortFlush[S]), write: write, flush: flush)
}

/// Returns a Writer over s that writes through self's write and flush, with
/// buf as its inline buffer. An empty buf means no buffer: every write that
/// is not empty is one call of write. O(1); allocates nothing.
export WritePort[S].Writer(&self, io: Io, s: &S, buf: mut []u8): Writer from self, s, buf {
    cap := buf.Len()
    unsafe "a pointer cast of s's address, kept as the Writer's ctx under s's loan, which the Writer holds" {
        return Writer(io: io, table: &self.table, ctx: raw.PtrCast[S, u8](raw.Of(s)), buf: raw.DataMut(buf), used: 0, cap: cap)
    }
}

// PortWrite and PortFlush are a WritePort[S]'s entries, put in a table only
// by WritePort[S].New, as PortRead is.
PortWrite[S](w: mut Writer, b: []u8): WriteError!void {
    unsafe "w's table is the table field, at offset 0, of a live WritePort[S] and w's ctx a live S, both under loans w holds (WritePort.Writer is the only maker of such a Writer); only & is taken of either" {
        p := &*raw.PtrCast[WriterTable, WritePort[S]](raw.Of(w.table))
        s := &*raw.PtrCast[u8, S](w.ctx)
        write := p.write
        try write(s, w.io, b)
    }
}

PortFlush[S](w: mut Writer): WriteError!void {
    unsafe "as PortWrite" {
        p := &*raw.PtrCast[WriterTable, WritePort[S]](raw.Of(w.table))
        s := &*raw.PtrCast[u8, S](w.ctx)
        flush := p.flush
        try flush(s, w.io)
    }
}

/// WritePort for a state that a Writer holds exclusively (a terminal, an
/// encoder): write and flush take mut S. Plain.
export struct WritePortMut[S] {
    table: WriterTable
    write: fn(mut S, Io, []u8): WriteError!void
    flush: fn(mut S, Io): WriteError!void
}

/// Returns a port over write and flush. O(1); allocates nothing; runs at
/// compile time.
export WritePortMut[S].New(write: fn(mut S, Io, []u8): WriteError!void, flush: fn(mut S, Io): WriteError!void): WritePortMut[S] {
    return WritePortMut[S](table: WriterTable(write: PortWriteMut[S], flush: PortFlushMut[S]), write: write, flush: flush)
}

/// Returns a Writer over s, which it holds exclusively, as WritePort.Writer
/// does. O(1); allocates nothing.
export WritePortMut[S].Writer(&self, io: Io, s: mut S, buf: mut []u8): Writer from self, s, buf {
    cap := buf.Len()
    unsafe "a pointer cast of s's address, kept as the Writer's ctx under s's exclusive loan, which the Writer holds" {
        return Writer(io: io, table: &self.table, ctx: raw.PtrCast[S, u8](raw.OfMut(s)), buf: raw.DataMut(buf), used: 0, cap: cap)
    }
}

// PortWriteMut and PortFlushMut are PortWrite and PortFlush for a
// WritePortMut[S].
PortWriteMut[S](w: mut Writer, b: []u8): WriteError!void {
    unsafe "w's table is the table field, at offset 0, of a live WritePortMut[S] and w's ctx an S under the exclusive loan w holds (WritePortMut.Writer is the only maker of such a Writer)" {
        p := &*raw.PtrCast[WriterTable, WritePortMut[S]](raw.Of(w.table))
        s := mut *raw.PtrCast[u8, S](w.ctx)
        write := p.write
        try write(s, w.io, b)
    }
}

PortFlushMut[S](w: mut Writer): WriteError!void {
    unsafe "as PortWriteMut" {
        p := &*raw.PtrCast[WriterTable, WritePortMut[S]](raw.Of(w.table))
        s := mut *raw.PtrCast[u8, S](w.ctx)
        flush := p.flush
        try flush(s, w.io)
    }
}

/// Holds a filter's state D (a decoder over a compressed stream, say) and
/// the place its source Reader is kept while a Reader from
/// FilterPort.Reader reads through it. Ref and Mut reach D; Into ends the
/// holder and returns D.
export struct Filtered[D] {
    src: *Reader
    state: D
}

// src is used only by a Reader that FilterPort.Reader made, which holds
// the Filtered and the source exclusively and is neither Send nor Share;
// between such Readers it is a dead address nobody reads.
unsafe impl[D: Send] Send for Filtered[D] {}
unsafe impl[D: Share] Share for Filtered[D] {}

/// Returns a holder of d. O(1); allocates nothing.
export Filtered[D].New(d: D): Filtered[D] {
    unsafe "a null source: only a Reader from FilterPort.Reader reads src, and that call sets it first" {
        return Filtered[D](src: raw.Null[Reader](), state: d)
    }
}

/// Returns the state. O(1).
export Filtered[D].Ref(&self): &D from self {
    return &self.state
}

/// Returns the state, writable. O(1).
export Filtered[D].Mut(mut self): mut D from self {
    return mut self.state
}

/// Ends the holder and returns the state. O(1).
export Filtered[D].Into(self): D {
    Filtered(src, state) := self
    _ = src
    return state
}

/// Builds Readers that read a filter's output over a source Reader: fill
/// reads from src as it needs and writes at most dst.Len() decoded bytes
/// into dst, returning how many, 0 at the end. Plain.
export struct FilterPort[D] {
    table: ReaderTable
    fill: fn(mut D, mut Reader, mut []u8): ReadError!int
}

/// Returns a port over fill. O(1); allocates nothing; runs at compile time.
export FilterPort[D].New(fill: fn(mut D, mut Reader, mut []u8): ReadError!int): FilterPort[D] {
    return FilterPort[D](table: ReaderTable(read: FilterRead[D], line: ReadLineBytewise), fill: fill)
}

/// Returns a Reader of f's output over src, with buf as its inline buffer
/// (an empty buf makes Line and Until prefix, as for ReadPort.Reader). Its
/// Io is src's. O(1); allocates nothing.
export FilterPort[D].Reader(&self, f: mut Filtered[D], src: mut Reader, buf: mut []u8): Reader from self, f, src, buf {
    cap := buf.Len()
    io := src.io
    f.src = raw.OfMut(src)
    unsafe "a pointer cast of f's address, kept as the Reader's ctx under f's exclusive loan, which the Reader holds with src's" {
        return Reader(io: io, table: &self.table, ctx: raw.PtrCast[Filtered[D], u8](raw.OfMut(f)), buf: raw.DataMut(buf), start: 0, end: 0, cap: cap)
    }
}

// FilterRead is a FilterPort[D]'s read entry, put in a table only by
// FilterPort[D].New, as PortRead is.
FilterRead[D](r: mut Reader, dst: mut []u8): ReadError!int {
    unsafe "r's table is the table field, at offset 0, of a live FilterPort[D], r's ctx a Filtered[D] and its src the source Reader, each under the exclusive loans r holds: FilterPort.Reader, the only maker of such a Reader, set src from the source it borrowed" {
        p := &*raw.PtrCast[ReaderTable, FilterPort[D]](raw.Of(r.table))
        f := mut *raw.PtrCast[u8, Filtered[D]](r.ctx)
        src := mut *f.src
        fill := p.fill
        return try fill(mut f.state, src, dst)
    }
}

/// FilterPort for a state that grows in a fill: fill may store into d what
/// d already holds (`*d into d`), so a state that keeps an &Arena takes its
/// buffers from it as late as it learns their sizes (a zstd frame's window,
/// at the frame's header). Its Reader declares the same store, `*f into f`,
/// to its caller, so a fill can store no loan the caller does not see.
/// Plain.
export struct FilterPortGrow[D] {
    table: ReaderTable
    fill: fn(d: mut D, src: mut Reader, dst: mut []u8): ReadError!int *d into d
}

/// Returns a port over fill. O(1); allocates nothing; runs at compile time.
export FilterPortGrow[D].New(fill: fn(d: mut D, src: mut Reader, dst: mut []u8): ReadError!int *d into d): FilterPortGrow[D] {
    return FilterPortGrow[D](table: ReaderTable(read: FilterReadGrow[D], line: ReadLineBytewise), fill: fill)
}

/// Returns a Reader of f's output over src, as FilterPort.Reader does, whose
/// reads may store into f what f already holds (`*f into f`, the fill's
/// clause). O(1); allocates nothing; a read allocates what fill allocates.
export FilterPortGrow[D].Reader(&self, f: mut Filtered[D], src: mut Reader, buf: mut []u8): Reader from self, f, src, buf *f into f {
    cap := buf.Len()
    io := src.io
    f.src = raw.OfMut(src)
    unsafe "a pointer cast of f's address, kept as the Reader's ctx under f's exclusive loan, which the Reader holds with src's; the store fill may make into f is the `*f into f` this function declares" {
        return Reader(io: io, table: &self.table, ctx: raw.PtrCast[Filtered[D], u8](raw.OfMut(f)), buf: raw.DataMut(buf), start: 0, end: 0, cap: cap)
    }
}

// FilterReadGrow is a FilterPortGrow[D]'s read entry, put in a table only
// by FilterPortGrow[D].New, as FilterRead is. The fill's `*d into d` is
// the `*f into f` that FilterPortGrow.Reader, the only maker of such a
// Reader, declared to the caller that lent f.
FilterReadGrow[D](r: mut Reader, dst: mut []u8): ReadError!int {
    unsafe "r's table is the table field, at offset 0, of a live FilterPortGrow[D], r's ctx a Filtered[D] and its src the source Reader, each under the exclusive loans r holds: FilterPortGrow.Reader, the only maker of such a Reader, set src from the source it borrowed, and declared fill's store into f as `*f into f`" {
        p := &*raw.PtrCast[ReaderTable, FilterPortGrow[D]](raw.Of(r.table))
        f := mut *raw.PtrCast[u8, Filtered[D]](r.ctx)
        src := mut *f.src
        fill := p.fill
        return try fill(mut f.state, src, dst)
    }
}

/// A write target that keeps what fits in a caller's buffer and counts what
/// did not, for a record formatted on the caller's stack (a log line).
/// Writer gives the Writer that fills it; its writes never fail. std.text
/// exports it, as Capped.
export view struct Capped {
    buf: *u8
    cap: int
    kept: int
    over: int
}

/// Returns an empty target over buf. O(1); allocates nothing.
export Capped.New(buf: mut []u8): Capped from buf {
    return Capped(buf: raw.DataMut(buf), cap: buf.Len(), kept: 0, over: 0)
}

/// Returns a Writer with no inline buffer that appends to self: each write
/// keeps the bytes that fit, counts the rest, and never fails. It needs no
/// Io: it carries one that does nothing, so formatting into it runs at
/// compile time. O(1); allocates nothing; each write is O(its bytes) and
/// one table call.
export Capped.Writer(mut self): Writer from self {
    unsafe "a pointer cast of self's address, kept as the Writer's ctx under self's exclusive loan, which the Writer holds" {
        ctx := raw.PtrCast[Capped, u8](raw.OfMut(self))
        return Writer(io: InertIoValue(), table: &CappedTable, ctx: ctx, buf: ctx, used: 0, cap: 0)
    }
}

/// Returns the bytes kept, in the order written. O(1).
export Capped.Bytes(&self): []u8 from self {
    return RwBytes(self.buf, self.cap)[0..self.kept]
}

/// Returns the number of bytes written past the buffer's end and not kept,
/// saturating at the largest int. O(1).
export Capped.Dropped(&self): int {
    return self.over
}

/// Empties the target for the next record. O(1).
export Capped.Reset(mut self) {
    self.kept = 0
    self.over = 0
}

const CappedTable = WriterTable(write: CappedWrite, flush: CappedFlush)

// CappedWrite keeps the prefix of b that fits and counts the rest. A count
// over the largest int stays at it (a caller writing 2^63 bytes into a
// record is not a case to wrap on).
// The copy is raw, as Writer.Bytes's: a Capped keeps 0 <= kept <=
// cap with buf holding cap bytes (New sets kept 0 and cap to buf's length,
// Reset sets kept 0, and only this adds to it), and k <= cap - kept and k
// <= b.Len() were just made so.
CappedWrite(w: mut Writer, b: []u8): WriteError!void {
    unsafe "a Capped writer's ctx is the Capped it was made from, which its exclusive loan keeps alive; its buf holds cap bytes, 0 <= kept <= cap, k <= cap - kept and k <= b.Len(), and b is a shared slice, apart from the Capped's buffer, whose loan the Capped holds" {
        c := mut *raw.PtrCast[u8, Capped](w.ctx)
        var k := c.cap - c.kept
        if k > b.Len() {
            k = b.Len()
        }
        raw.Copy(raw.Offset(c.buf, c.kept), raw.Data(b), k)
        c.kept += k
        lost := b.Len() - k
        if c.over > MaxInt - lost {
            c.over = MaxInt
        } else {
            c.over += lost
        }
    }
}

CappedFlush(w: mut Writer): WriteError!void {
}

const MaxInt: int = 9223372036854775807

/// Builds Writers over a program's device or other byte sink of type S:
/// write takes every byte of a run and sends it on, taking its state by
/// `mut` reference as a stored callback does. Its Writers need no Io (they
/// carry one that does nothing) and none reaches write, so a freestanding
/// program, which has no Io, can make one before it has a heap and in its
/// panic hook. Neither a Sink nor its Writers allocate. Plain, with no
/// public fields. std.text exports it, as Sink.
export struct Sink[S] {
    table: WriterTable
    write: fn(mut S, []u8): WriteError!void
}

/// Returns a Sink over write. O(1); allocates nothing; runs at compile
/// time, so a const holds it, and (&C).Writer(s, buf) makes a Writer over
/// the const C.
export Sink[S].New(write: fn(mut S, []u8): WriteError!void): Sink[S] {
    return Sink[S](table: WriterTable(write: SinkWrite[S], flush: SinkFlush[S]), write: write)
}

/// Returns a Writer over s, which it holds exclusively, that writes through
/// self's write, with buf as its inline buffer. A write that would overflow
/// buf fills it up and hands it to write full; once nothing is buffered,
/// the rest of a run that still exceeds buf goes to write whole, uncopied,
/// as Writer.Bytes says. So "ab", a 10-byte run and "cd" through a 4-byte
/// buf reach write as 4, 8 and, at Flush, 2 bytes. With an empty buf every
/// write that is not empty is one call of write, and an empty write makes
/// none. Flush hands write what is buffered and does nothing more. A
/// failure of write is the Writer's WriteError. O(1); allocates nothing.
export Sink[S].Writer(&self, s: mut S, buf: mut []u8): Writer from self, s, buf {
    cap := buf.Len()
    unsafe "a pointer cast of s's address, kept as the Writer's ctx under s's exclusive loan, which the Writer holds" {
        return Writer(io: InertIoValue(), table: &self.table, ctx: raw.PtrCast[S, u8](raw.OfMut(s)), buf: raw.DataMut(buf), used: 0, cap: cap)
    }
}

// SinkWrite and SinkFlush are a Sink[S]'s entries, put in a table
// only by Sink[S].New, as PortWriteMut is. Each run the Writer hands
// over reaches write whole, so the device sees each byte once.
SinkWrite[S](w: mut Writer, b: []u8): WriteError!void {
    unsafe "w's table is the table field, at offset 0, of a live Sink[S] and w's ctx an S under the exclusive loan w holds (Sink.Writer is the only maker of such a Writer)" {
        p := &*raw.PtrCast[WriterTable, Sink[S]](raw.Of(w.table))
        s := mut *raw.PtrCast[u8, S](w.ctx)
        write := p.write
        try write(s, b)
    }
}

// SinkFlush: Writer.Flush has drained the buffer into SinkWrite before
// it calls the table's flush, so nothing is left to force out.
SinkFlush[S](w: mut Writer): WriteError!void {
}

/// Returns the Io a Writer was made with, for its table's entries. O(1).
export WriterIo(w: &Writer): Io {
    return w.io
}

/// Returns the handle a Writer was made with (NewWriter's ctx). O(1).
export WriterCtx(w: &Writer): uint {
    return raw.Addr(w.ctx)
}

/// Returns the Io a Reader was made with, for its table's entries. O(1).
export ReaderIo(r: &Reader): Io {
    return r.io
}

/// Returns the handle a Reader was made with (NewReader's ctx). O(1).
export ReaderCtx(r: &Reader): uint {
    return raw.Addr(r.ctx)
}
