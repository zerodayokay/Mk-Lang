// Why: a ServeWith handler sometimes needs the connection itself (to take
// it over, or to flush part of a reply), but the response must not hold
// it: a store into the response would then reach the connection.
//
// What: Link, handed to a handler beside its response, and the cell
// functions ServeWith's loop and the handler share: CellLock, CellRead,
// CellWrite, CellSend, CellTake, CellTaken and CloseCell, plus the paced
// writes PacedSend and PacedFlush.
//
// How: the cell is a Mutex[?net.Conn] owned by the connection's task from
// the accept to the loop's end; only that task touches it, so each lock is
// uncontended. The comment below says why a lock here cannot fail.

// The loop reads and writes the connection through the cell between
// requests, and the handler reaches it only through its Link (Takeover,
// Flush). So each lock below is a CAS and a release, taken under a NoCancel
// Io masked at the lock, which no scope's cancel reaches, and whose
// cancellation check goes on in a dead Sim run, under a freed Recorder or
// Replayer, and under a Replayer that cannot replay it: a lock that cannot
// wait cannot fail. The handler gets the Link beside its response, never
// inside it, so a store into the response never reaches a cell the
// response held, even behind a shared reference.

import std.core
import std.http.h2
import std.net
import std.time

struct H2FlushTarget {
    conn: &h2.Conn
    stream: h2.StreamRef
}

/// A ServeWith handler's handle on the connection its request came on.
/// Response.Flush writes through it, and Takeover takes the connection
/// from it. The server makes one per request and passes it beside the
/// response, never inside it, so nothing a handler stores into its
/// response reaches the connection.
export view struct Link {
    version: HttpVersion
    cell: ?&Mutex[?net.Conn]
    h2: ?H2FlushTarget
    io: Io
    rest: []u8
    // pace: the longest a piece of a Flush may wait for the client to take
    // it (Paced).
    pace: time.Duration
}

/// The HTTP version of the connection this link's request came on. O(1);
/// allocates nothing.
export Link.Version(&self): HttpVersion {
    return self.version
}

// CellLock locks the cell; it never waits (one task), and the mask is taken
// here, so only a wait could fail it: a NoCancel lock that
// fails is a bug.
CellLock(cell: &Mutex[?net.Conn], io: Io): Guard[?net.Conn] from cell {
    return cell.Lock(io.NoCancel()) else {
        Panic("http: a connection cell's NoCancel lock was cancelled")
    }
}

// CellRead reads into buf from the cell's connection: 0 when the peer
// closed, the read failed (a deadline or cancel of io included) or the
// connection was taken.
CellRead(cell: &Mutex[?net.Conn], io: Io, buf: mut []u8): int {
    g := CellLock(cell, io)
    var n := 0
    if conn := &*g.Ref() {
        n = conn.Read(io, buf) else {
            0
        }
    }
    g.Unlock()
    return n
}

// CellWrite writes b whole on the cell's connection, paced by pace
// (Paced): false when the write fails, a piece waits past pace, or the
// connection was taken.
CellWrite(cell: &Mutex[?net.Conn], io: Io, b: []u8, pace: time.Duration): bool {
    g := CellLock(cell, io)
    var ok := false
    if conn := &*g.Ref() {
        p := Paced(conn: conn, limit: pace)
        PacedSend(&p, io, b) else {
            g.Unlock()
            return false
        }
        ok = true
    }
    g.Unlock()
    return ok
}

// CellSend writes res on the cell's connection through wb, paced by pace
// (Paced): false when the write fails, a piece waits past pace, or the
// connection was taken.
CellSend(cell: &Mutex[?net.Conn], io: Io, wb: mut [4096]u8, res: &Response, pace: time.Duration): bool {
    g := CellLock(cell, io)
    var ok := false
    if conn := &*g.Ref() {
        p := Paced(conn: conn, limit: pace)
        var w := (&PacedWrites).Writer(io, &p, mut wb[..])
        res.Send(io, mut w) else {
            g.Unlock()
            return false
        }
        ok = true
    }
    g.Unlock()
    return ok
}

// PacePiece is the most bytes one paced write hands the connection: 64
// KiB, so the limit bounds the time between pieces the client takes, not
// the whole reply (nginx's send_timeout, "between two successive write
// operations").
const PacePiece = 65536

// Paced is a server connection whose writes are paced: each piece of at
// most PacePiece bytes must be taken by the client within limit, else the
// write fails and the server ends the connection. So a client that asks
// for a reply and never reads it holds its connection, and its ServeWith
// permit, for at most limit after the socket's buffers fill, not for ever.
struct Paced {
    conn: &net.Conn
    limit: time.Duration
}

// PacedSend writes b whole on p's connection, a piece of at most PacePiece
// bytes at a time, each in a scope whose deadline is p.limit: WriteError
// when a piece fails or passes it (Canceled, from the deadline or a cancel
// of io, is WriteError too; the caller checks io itself).
PacedSend(p: &Paced, io: Io, b: []u8): WriteError!void {
    var off := 0
    for off < b.Len() {
        k := if b.Len() - off > PacePiece { PacePiece } else { b.Len() - off }
        var failed := false
        scope s := io.Scope() {
            s.CancelAfter(p.limit)
            p.conn.WriteAll(io, b[off..off + k]) else {
                failed = true
            }
        }
        if failed {
            fail WriteError
        }
        off += k
    }
}

// PacedFlush: a paced connection has no flush of its own; each PacedSend
// has sent every byte.
PacedFlush(p: &Paced, io: Io): WriteError!void {
}

const PacedWrites = core.WritePort[Paced].New(PacedSend, PacedFlush)

// CellTaken: the cell is empty, its connection taken by a handler.
CellTaken(cell: &Mutex[?net.Conn], io: Io): bool {
    g := CellLock(cell, io)
    taken := g.Ref().IsNone()
    g.Unlock()
    return taken
}

// CellTake takes the connection out of the cell, leaving it empty: none
// when it was already taken.
CellTake(cell: &Mutex[?net.Conn], io: Io): ?net.Conn {
    var g := CellLock(cell, io)
    c := Replace(mut *g.Mut(), NoConn())
    g.Unlock()
    return c
}

// NoConn is the empty cell.
NoConn(): ?net.Conn {
    return none
}

// CloseCell closes the connection a cell held at the loop's end, if a
// handler did not take it (Response.Takeover).
CloseCell(cell: ?net.Conn, io: Io) {
    match cell {
        some(conn) {
            CloseConn(conn, io)
        }
        none {}
    }
}
