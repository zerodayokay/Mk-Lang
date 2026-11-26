// Why: an endpoint must read HTTP/2 frames as soon as they arrive (RFC 9113
// 5.2.2), but a pooled connection with no request waiting on it is read by
// nobody: it answers no PING or SETTINGS and sees no GOAWAY until the next
// request. Client.Drive gives every pooled connection a reader of its own.
//
// What: Client.Drive, Client.Stop and DriveMuxConn, the task that reads one
// connection.
//
// How: Drive scans the mux table, claims each Live slot not yet driven
// (ClaimDrive sets the slot's driven bit and takes a reference, as a request
// does) and starts DriveMuxConn on it in Drive's own scope, then waits on
// drive_wake, which the pool notifies when a slot goes Live and Stop notifies
// to end Drive. DriveMuxConn is a party of the connection's read-token
// protocol whose condition is never true, so it reads whenever no request
// does. When the connection fails, finishes draining after a GOAWAY, or sits
// idle past ClientLimits.idle with no stream, DriveMuxConn retires the slot;
// on Stop or a cancel it leaves the slot Live for requests, which then read
// for themselves. Either way it clears driven and drops its reference, so
// the last reference closes a retired connection.

import std.core
import std.raw

// DriveMuxConn reads the connection of slot sl, claimed by ClaimDrive at
// generation gen_, until it ends or io is cancelled.
DriveMuxConn(client: &Client, sl: &MuxSlot, io: Io, gen_: u64) {
    nc := io.NoCancel()
    a := client.heap.Alloc()
    // The reference ClaimDrive took keeps the connection alive (mem_mux.mk).
    p := sl.conn.Load()
    var retire := false
    if !raw.IsNull(p) {
        retire = true
        MuxConnOf(p).Drive(io, client.limits.idle) else {
            retire = false
        }
    }
    if retire {
        _ = sl.MarkDead(gen_)
    }
    if box := sl.ReleaseRef(a, true) {
        CloseMuxBox(a, box, nc, client.limits.close)
    }
}

/// Reads every pooled HTTP/2 connection of the client in the background, one
/// task per connection, until Stop or a cancel of io: each answers PING and
/// SETTINGS while idle and sees a GOAWAY at once, and one idle past
/// ClientLimits.idle with no stream is closed with GOAWAY(NO_ERROR). Run it
/// as a task of a scope that outlives the requests; then Stop, the scope's
/// end, and Close. After Stop it returns at once, so a later Drive of the
/// same client returns too. Errors: Canceled when io is cancelled; it
/// returns normally after Stop. O(16) per wake; allocates one task per
/// connection it drives.
export Client.Drive(&self, io: Io): Canceled!void {
    scope d := io.Scope() {
        for {
            if self.drive_stop.Load() != 0 {
                d.Cancel()
                return
            }
            core.SyncCancel(io) else {
                d.Cancel()
                fail Canceled
            }
            for k in 0..MaxMuxSlots {
                sl := MuxSlotAt(&self.mux, k)
                if gen_ := sl.ClaimDrive() {
                    d.go DriveMuxConn(self, sl, io, gen_)
                }
            }
            self.drive_wake.Wait(io) else {
                fail Canceled
            }
        }
    }
}

/// Makes Drive return: its connection tasks end and leave their connections
/// pooled, read from then on by the requests that wait on them. Call it
/// before the end of the scope that runs Drive; it is safe to call more than
/// once or with no Drive running. Never blocks or fails. O(1); allocates
/// nothing.
export Client.Stop(&self) {
    self.drive_stop.Store(1)
    self.drive_wake.NotifyOne()
}
