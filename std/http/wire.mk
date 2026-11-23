// Why: the client's side of one connection, writing a request and reading
// a reply's head, is one piece Client.Exchange uses for Do and DoStreaming.
//
// What: LineWrite and WriteOn (the request), ReadReplyHead and ReadBigHead
// (the reply's head), LineRecv, LineReadInto, AbandonLine, LineUntouched
// and ResetLine.
//
// How: a head is found by HeadScan (CRLF only) in the Line's 8 KiB buffer;
// one that does not fit is read on into an arena buffer that doubles up to
// the head limit, so memory follows the head's bytes, never the limit. The
// bytes after the head stay in the buffer for the body.

import std.net
import std.raw
import std.rt

// AbandonLine releases the Line's connection, if it holds one.
AbandonLine(m: &Mutex[Line], io: Io) {
    DropConn(TakeConn(m, io, false))
}

// LineUntouched: no byte of a reply was read on the Line's connection since
// it was installed.
LineUntouched(m: &Mutex[Line], io: Io): bool {
    g := m.Lock(io.NoCancel()) else {
        Panic("http.Client: a NoCancel lock was cancelled")
    }
    f := g.Ref().fresh
    g.Unlock()
    return f
}

// LineWrite sends head and body on the Line's connection through one
// buffered Writer, so a small request is one write, then st's body when
// it has one. A cancel is Canceled, a failure of st's Reader ReadError,
// another failure WriteError.
LineWrite(m: &Mutex[Line], io: Io, head: str, body: []u8, st: mut Stream): Error!void {
    g := try m.Lock(io)
    defer g.Unlock()
    sp := g.Ref().slot.Load()
    if !raw.IsNull(sp) {
        sg := try SlotOf(sp).st.Lock(io)
        defer sg.Unlock()
        if c := &sg.Ref().conn {
            try WriteOn(c, io, head, body, st)
            return
        }
    } else if c := &g.Ref().conn {
        try WriteOn(c, io, head, body, st)
        return
    }
    fail Closed
}

// WriteOn writes head and body on c through one buffered Writer, then
// st's body when it has one.
WriteOn(c: &net.Conn, io: Io, head: str, body: []u8, st: mut Stream): Error!void {
    var wb: [4096]u8 = [0; 4096]
    var w := c.Writer(io, mut wb[..])
    WriteAllOf(mut w, head, body) else {
        try CancelCheck(io)
        fail WriteError
    }
    if st.r.IsSome() {
        try StreamOut(mut w, io, st)
    }
}

WriteAllOf(w: mut Writer, head: str, body: []u8): WriteError!void {
    try w.Str(head)
    try w.Bytes(body)
    try w.Flush()
}

// LineRecv reads into l.rb[l.e..]; 0 at the connection's end.
LineRecv(l: mut Line, io: Io): Error!int {
    return try LineConnReadFrom(&l.slot, &l.conn, mut l.fresh, io, mut l.rb[l.e..LineBuf])
}

// ReadReplyHead reads one head from the Line, up to and with its CRLF CRLF,
// skipping empty lines before it (RFC 9112 2.2): a bare CR or LF is
// BadRequest, more than limit bytes TooLarge, the connection's end before
// any byte Closed (a pooled connection the server closed) and after
// one BadRequest. base is the head's offset in the reply. The head is
// copied into a and checked as UTF-8 once; its bytes leave the buffer.
ReadReplyHead(a: &Arena, io: Io, l: mut Line, limit: int, base: int): Error!Raw from a {
    var p := HeadPos(s: l.s, scan: l.s, skipped: 0, hend: -1, bad: -1)
    for {
        p = HeadScan(l.rb[..], p, l.e)
        if p.bad >= 0 {
            fail BadRequest(offset: base + p.bad)
        }
        if p.hend >= 0 {
            break
        }
        if p.skipped + (p.scan - p.s) >= limit {
            fail TooLarge
        }
        if p.s > 0 {
            k := l.e - p.s
            for x in 0..k {
                l.rb[x] = l.rb[p.s + x]
            }
            p.scan -= p.s
            p.s = 0
            l.s = 0
            l.e = k
        }
        if l.e == LineBuf {
            return try ReadBigHead(a, io, l, p, limit, base)
        }
        n := try LineRecv(l, io)
        if n == 0 {
            if l.e == 0 && p.skipped == 0 && base == 0 {
                fail Closed
            }
            fail BadRequest(offset: base + p.skipped + l.e)
        }
        l.e += n
    }
    if p.skipped + (p.hend - p.s) > limit {
        fail TooLarge
    }
    hb := try a.Slice[u8](p.hend - p.s)
    (mut hb[..]).CopyFrom(l.rb[p.s..p.hend])
    l.s = p.hend
    text := rt.StrFrom(hb[..]) else {
        fail BadRequest(offset: base + p.skipped + Utf8Bad(hb[..]))
    }
    return Raw(text: text, skipped: p.skipped)
}

// ReadBigHead goes on with a head that filled the Line's buffer: its bytes
// move to an arena buffer that grows (GrowBytes) as at most LineBuf more
// are read at a time, until the head ends or passes limit. The bytes after
// the head, fewer than the last read, go back to the Line's buffer.
ReadBigHead(a: &Arena, io: Io, l: mut Line, p0: HeadPos, limit: int, base: int): Error!Raw from a {
    var bb := try a.Slice[u8](LineBuf * 2)
    (mut bb[0..l.e]).CopyFrom(l.rb[0..l.e])
    var be := l.e
    var q := HeadPos(s: 0, scan: p0.scan, skipped: p0.skipped, hend: -1, bad: -1)
    for {
        if be + LineBuf > bb.Len() {
            bb = try GrowBytes(a, bb[0..be], be + LineBuf)
        }
        n := try LineReadInto(l, io, mut bb[be..be + LineBuf])
        if n == 0 {
            fail BadRequest(offset: base + q.skipped + be)
        }
        be += n
        q = HeadScan(bb[..], q, be)
        if q.bad >= 0 {
            fail BadRequest(offset: base + q.bad)
        }
        if q.hend >= 0 {
            break
        }
        if q.skipped + q.scan >= limit {
            fail TooLarge
        }
    }
    if q.skipped + q.hend > limit {
        fail TooLarge
    }
    rest := be - q.hend
    (mut l.rb[0..rest]).CopyFrom(bb[q.hend..be])
    l.s = 0
    l.e = rest
    text := rt.StrFrom(bb[0..q.hend]) else {
        fail BadRequest(offset: base + q.skipped + Utf8Bad(bb[0..q.hend]))
    }
    return Raw(text: text, skipped: q.skipped)
}

// LineReadInto reads from the Line's connection into dst; 0 at its end.
LineReadInto(l: mut Line, io: Io, dst: mut []u8): Error!int {
    return try LineConnRead(l, io, dst)
}

// ResetLine empties the Line's buffer for a newly installed connection.
ResetLine(l: mut Line) {
    l.s = 0
    l.e = 0
    l.fresh = true
    l.failed = false
    l.done = true
    l.reuse = true
}
