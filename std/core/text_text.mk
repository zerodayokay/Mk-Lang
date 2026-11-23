// Why: programs build text piece by piece; Text is the prelude's growable
// buffer that stays valid UTF-8 at every step, so its str needs no check.
// What: Text (New, Push, PushChar, PushReplaced, Str, Len, Done, Clear,
// Drop, Writer), str.Replace, Reader.Line with LineUnbuffered, and the
// helpers Room, Reserve, PushBytes and TextInto (layer 1).
// How: growth is List's: max(4, 2·cap, needed), in place when the arena's
// last block is the buffer, else a new buffer with the old one abandoned.
// The raw byte operations are mem_slots.mk's.

import std.raw

/// A growable text buffer in an arena, always valid UTF-8: build a str
/// piece by piece with Push, PushChar or a Writer, then take it with Str
/// or Done. It grows as List does. HeapText is its linear form on the heap.
export view struct Text {
    ptr: *u8
    used: int
    cap: int
    alloc: &Alloc
}

/// An empty text in a. O(1); allocates nothing until the first Push.
export Text.New(a: &Arena): Text {
    return Text(ptr: DanglingBytes(), used: 0, cap: 0, alloc: a.Alloc())
}

/// Appends s. OutOfMemory when the arena cannot grow the buffer, and then the
/// text is unchanged. Amortized O(s.Len()); growth allocates from the arena.
export Text.Push(mut self, s: str): OutOfMemory!void {
    try self.PushBytes(s.AsBytes())
}

/// Appends c's UTF-8 encoding. OutOfMemory as Push. Amortized O(1).
export Text.PushChar(mut self, c: char): OutOfMemory!void {
    var buf: [4]u8 = [0; 4]
    n := EncodeChar(c, mut buf)
    try self.PushBytes(buf[0..n])
}

/// Appends s with every occurrence of old replaced by new. Matches are found
/// from the left and never overlap; the search resumes after each replaced
/// match, and new is never searched: "aaa" with "aa" replaced by "b" gives
/// "ba". An empty old matches at every scalar boundary, so new goes before each
/// scalar and after the last: "ab" with "" replaced by "-" gives "-a-b-". A
/// valid old matches only whole scalars, so the text stays valid UTF-8.
/// OutOfMemory when the arena cannot grow the buffer, and then the text is
/// unchanged. O(s.Len() + old.Len()) search plus the copy; when new is not
/// longer than old, room for s.Len() more bytes is made once, before the first
/// copy, so appends stay amortized O(1) a byte.
export Text.PushReplaced(mut self, s: str, old: str, new: str): OutOfMemory!void {
    start := self.used
    self.Replacing(s, old, new) else {
        self.used = start
        fail OutOfMemory
    }
}

// Replacing appends s with old replaced by new, as PushReplaced, and on
// OutOfMemory may leave a prefix of the output appended. Every piece it
// appends is valid UTF-8 on its own: a run of s between matches starts and
// ends on scalar boundaries, since a non-empty valid old starts with a lead
// byte and ends a whole scalar, and an empty old matches only between
// scalars here. So the pieces go through PushBytes unchecked.
Text.Replacing(mut self, s: str, old: str, new: str): OutOfMemory!void {
    b := s.AsBytes()
    n := b.Len()
    m := old.Len()
    nb := new.AsBytes()
    if m == 0 {
        try self.PushBytes(nb)
        var i := 0
        for i < n {
            k := ScalarLen(b[i])
            try self.PushBytes(b[i..i + k])
            try self.PushBytes(nb)
            i += k
        }
        return
    }
    if nb.Len() <= m {
        try self.Room(n)
    }
    var at := 0
    for at <= n - m {
        p := FindFrom(s, old, at) else {
            break
        }
        try self.PushBytes(b[at..p])
        try self.PushBytes(nb)
        at = p + m
    }
    try self.PushBytes(b[at..n])
}

/// A copy of the text in a with every occurrence of old replaced by new,
/// matched as Text.PushReplaced matches; a text with no match is
/// still copied, since the result lives in a. OutOfMemory when the arena
/// cannot grow. O(n + old.Len()) search plus the copy; one allocation, of
/// max(4, n) bytes, when new is not longer than old.
export str.Replace(self, a: &Arena, old: str, new: str): OutOfMemory!str from a {
    var t := Text.New(a)
    try t.Replacing(self, old, new)
    return TextInto(t)
}

// Room makes room for n more bytes, growing as PushBytes does: to
// NextCap(cap, used + n), which on an empty text is max(4, n). Unlike
// Reserve's exact growth, a text that is appended to again keeps its
// amortized cost.
Text.Room(mut self, n: int): OutOfMemory!void {
    if n > self.cap - self.used {
        c := NextCap(self.cap, self.used + n)
        p := GrowBytes(self.alloc, self.ptr, self.used, self.cap, c, false)
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        self.ptr = p
        self.cap = c
    }
}

// PushBytes appends b, which the caller has validated (a str's bytes, an
// encoded char, or TextWrite's checked run).
Text.PushBytes(mut self, b: []u8): OutOfMemory!void {
    n := b.Len()
    if n == 0 {
        return
    }
    if n > self.cap - self.used {
        c := NextCap(self.cap, self.used + n)
        p := GrowBytes(self.alloc, self.ptr, self.used, self.cap, c, false)
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        self.ptr = p
        self.cap = c
    }
    CopyBytes(ByteAt(self.ptr, self.used), raw.Data(b), n) // used + n <= cap
    self.used += n
}

// Reserve makes room for n more bytes with one growth to exactly
// used + n when the room is short (CaseMapped's single allocation).
Text.Reserve(mut self, n: int): OutOfMemory!void {
    if n > self.cap - self.used {
        c := self.used + n
        p := GrowBytes(self.alloc, self.ptr, self.used, self.cap, c, false)
        if raw.IsNull(p) {
            fail OutOfMemory
        }
        self.ptr = p
        self.cap = c
    }
}

/// The text so far, valid UTF-8 always, in the arena. O(1); allocates
/// nothing.
export Text.Str(&self): str {
    return StrOf(self.ptr, self.used)
}

/// The length of the text so far in bytes, as Str().Len(). O(1).
export Text.Len(&self): int {
    return self.used
}

/// Ends the text and returns its bytes as a str in the arena, as List.Done
/// does. O(1); allocates nothing. The result borrows the arena, not the
/// Text, so a function can `return t.Done()` as a result `from a` without
/// a copy, where `return t.Str()` would return a borrow of a local (E0603).
export Text.Done(self): str {
    return StrOf(self.ptr, self.used)
}

// TextInto ends t and gives its bytes as a str that lives as long as t's
// arena: Text.Done as a function, FmtArena's last step.
TextInto(t: Text): str {
    return t.Done()
}

/// Empties the text and keeps its buffer. O(1).
export Text.Clear(mut self) {
    self.used = 0
}

/// Removes the first n bytes and shifts the rest down; n must be from 0 to
/// the length and on a scalar boundary, else a str-boundary panic (code 10).
/// O(Len() - n) for the shift; allocates nothing, and the
/// capacity is kept.
export Text.Drain(mut self, n: int) {
    if n < 0 || n > self.used {
        raw.Fail(10, "Text.Drain: count out of range")
    }
    if n < self.used && ByteView(self.ptr, self.used)[n] & 0xC0 == 0x80 {
        raw.Fail(10, "Text.Drain: not on a scalar boundary")
    }
    ShiftBytes(self.ptr, 0, n, self.used - n)
    self.used -= n
}

/// A Writer that appends to self, for code that writes to any Writer.
/// Each write must be valid UTF-8 (WriteError otherwise), and OutOfMemory
/// becomes WriteError. O(1), allocating nothing; each write is O(its
/// bytes) and grows self. It has no buffer of its own: each write lands in
/// the text at once, and Flush does nothing. It needs no Io, so formatting
/// into a Text also runs at compile time.
export Text.Writer(mut self): Writer {
    ctx := TextCtx(self)
    // Built here, not through WriterOver: the evaluator refuses a call
    // whose signature takes an Io, and this one runs at compile time.
    return Writer(io: InertIoValue(), table: &TextWriteTable, ctx: ctx, buf: ctx, used: 0, cap: 0)
}

const TextWriteTable = WriterTable(write: TextWrite, flush: TextFlush)

TextWrite(w: mut Writer, b: []u8): WriteError!void {
    if !TextRunValid(b) {
        fail WriteError
    }
    unsafe "a text writer's ctx is the Text it was made from, which its loan keeps alive and exclusive" {
        t := mut *raw.PtrCast[u8, Text](w.ctx)
        t.PushBytes(b) else {
            fail WriteError
        }
    }
}

TextFlush(w: mut Writer): WriteError!void {
}

// TextRunValid is ValidUtf8(b), TextWrite's check, with the common run
// answered inline: one of at most 32 bytes, a formatted number's or a
// format's literal, whose bytes are all below 0x80 is ASCII, so valid. Its
// bytes are or-ed together, by words from 8 bytes on: the words at 0, 8
// and 16 that start before n - 8, and the last one, at n - 8, which
// overlaps the one before it, so every byte is in one. Any other run, a
// short one with a byte at or above 0x80 included, goes to ValidUtf8
// whole. So the answer is ValidUtf8's for every run, and a Text never holds
// invalid UTF-8.
TextRunValid(b: []u8): bool {
    n := b.Len()
    if n <= 32 {
        var hi: u64 = 0
        if n >= 8 {
            hi = LoadWord(b, 0) | LoadWord(b, n - 8)
            if n > 16 {
                hi = hi | LoadWord(b, 8)
                if n > 24 {
                    hi = hi | LoadWord(b, 16)
                }
            }
        } else {
            for c in b {
                hi = hi | u64(c)
            }
        }
        if hi & 0x8080808080808080 == 0 {
            return true
        }
    }
    return ValidUtf8(b)
}

/// The next line copied into a, without its \n or \r\n, or none at the end of
/// input. ReadError when the bytes are not UTF-8, the line is longer than the
/// buffer, or the source fails; OutOfMemory when a cannot hold the copy. O(line
/// length); allocates the line in a.
///
/// The last line is returned even when it has no \n; a lone \r at its end
/// is kept. A line that fills the buffer exactly is still a line. A source
/// that reports a negative count or more bytes than it was given, or that
/// calls back into this Reader and moves its position during a fill, is a
/// ReadError, never a silent end of input or a read past the buffer. A
/// reader with no buffer of its own (Stdin(), Reader.Of) reads one line per
/// call; a line over 64 KiB is ReadError. On a buffered reader, a Line
/// interrupted by a cancel consumes nothing: call it again.
export Reader.Line(mut self, a: &Arena): ReadError|OutOfMemory!?str from a {
    if self.cap == 0 {
        return try self.LineUnbuffered(a)
    }
    read := self.table.read
    // scan: the bytes before it hold no newline, so a refill never rescans
    // them (each byte is searched once, by IndexByteIn).
    var scan := self.start
    for {
        bytes := ByteView(self.buf, self.end)
        if i := IndexByteIn(bytes, 0x0A, scan) {
            line := bytes[self.start..i]
            self.start = i + 1
            return some(try LineCopy(a, line, true))
        }
        scan = self.end
        if self.start > 0 {
            ShiftBytes(self.buf, 0, self.start, self.end - self.start)
            self.end -= self.start
            scan -= self.start
            self.start = 0
        }
        room := self.cap - self.end
        if room == 0 {
            // The buffer holds one whole line only if the input ends here
            // or its next byte is the newline; the probe byte is consumed
            // either way, and a longer line is ReadError.
            // pe: as e below, for an entry that calls back in during the
            // probe read; start is 0 here, so a moved start or end is
            // ReadError and the line below is the bytes this call buffered.
            pe := self.end
            var probe: [1]u8 = [0]
            p := try read(self, mut probe[..])
            if self.start != 0 || self.end != pe || p < 0 || p > 1 {
                fail ReadError
            }
            if p == 1 && probe[0] != 0x0A {
                fail ReadError
            }
            line := ByteView(self.buf, self.end)
            self.start = self.end
            return some(try LineCopy(a, line, p == 1))
        }
        // e: an entry that calls a method of this Reader during the fill
        // can move start or end. room was cap - e, so adding n to a moved
        // end could pass cap, and the line is built from 0, so a moved
        // start would deliver bytes the nested call already returned.
        // Start is 0 here (the compaction above), so either is ReadError,
        // and start and end stay where the nested call left them, with
        // start <= end <= cap.
        e := self.end
        n := try read(self, ByteViewMut(ByteAt(self.buf, self.end), room))
        if self.start != 0 || self.end != e || n < 0 || n > room {
            fail ReadError
        }
        if n == 0 {
            if self.end == 0 {
                return none
            }
            line := ByteView(self.buf, self.end)
            self.start = self.end
            return some(try LineCopy(a, line, false))
        }
        self.end += n
    }
}

// LineChunk is how many bytes LineUnbuffered takes per table call.
const LineChunk = 1024

// LineMax is the longest line LineUnbuffered returns: the process stdin
// buffer's size.
const LineMax = 65536

// LineUnbuffered is Line for a reader with no inline buffer. A line that
// fits one chunk is validated in the frame and copied into a once; a longer
// one grows a Text in a and is validated there (no second copy). A count
// below zero or above LineChunk is ReadError, as in Line.
Reader.LineUnbuffered(mut self, a: &Arena): ReadError|OutOfMemory!?str from a {
    var chunk: [LineChunk]u8 = [0; LineChunk]
    line := self.table.line
    n := try line(self, mut chunk[..])
    if n < 0 || n > LineChunk {
        fail ReadError
    }
    if n == 0 {
        return none
    }
    if chunk[n - 1] == 0x0A {
        return some(try LineCopy(a, chunk[0..n - 1], true))
    }
    var t := Text.New(a)
    try t.PushBytes(chunk[0..n])
    var nl := false
    for {
        m := try line(self, mut chunk[..])
        if m < 0 || m > LineChunk {
            fail ReadError
        }
        if m == 0 {
            break
        }
        var k := m
        if chunk[m - 1] == 0x0A {
            nl = true
            k = m - 1
        }
        if t.used + k > LineMax {
            fail ReadError
        }
        try t.PushBytes(chunk[0..k])
        if nl {
            break
        }
    }
    return some(try LineEnd(t, nl))
}

// LineCopy validates a line without its \n, strips one \r at its end when
// the line ended in \n (nl), and copies it into a.
LineCopy(a: &Arena, b: []u8, nl: bool): ReadError|OutOfMemory!str from a {
    var n := b.Len()
    if nl && n > 0 && b[n - 1] == 0x0D {
        n -= 1
    }
    s := str.From(b[0..n]) else {
        fail ReadError
    }
    return try a.Dup(s)
}

// LineEnd is LineCopy for a line already in t's arena: by value, so the
// result holds t's arena loan as TextInto's does; the bytes are
// validated before they become a str.
LineEnd(t: Text, nl: bool): ReadError!str {
    var n := t.used
    b := ByteView(t.ptr, n)
    if nl && n > 0 && b[n - 1] == 0x0D {
        n -= 1
    }
    if !ValidUtf8(b[0..n]) {
        fail ReadError
    }
    return StrOf(t.ptr, n)
}
