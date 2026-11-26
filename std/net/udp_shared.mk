// Why: QUIC connections and servers share one UDP socket across concurrent
// reader and writer tasks; Datagram is !Share, so SharedDatagram puts
// independent read and write locks around the handle, as Conn does for streams.
//
// What: SharedDatagram with Send, SendBatch, Recv, TryRecv, Port, LocalAddr
// and Close.
//
// How: one Mutex[u8] serializes receives and another serializes sends, so one
// Recv/TryRecv and one Send/SendBatch run concurrently without interleaving
// or allocating.

import std.core
import std.rt
import std.task

/// A UDP socket for many tasks: linear, Send and Share. One Recv (or TryRecv)
/// and one Send (or SendBatch) run at once, each under its own lock, as
/// Conn's Read and WriteAll. Release it with Close.
export linear struct SharedDatagram {
    h: uint
    bound: int
    v6: bool
    zone: u32
    peer: ?Addr
    reads: Mutex[u8]
    writes: Mutex[u8]
}

// SendOne sends one datagram on self under the caller's held write lock.
SharedDatagram.SendOne(&self, io: Io, data: []u8, to: ?Addr): Error!void {
    match to {
        some(dst) {
            try UdpSend(self.h, self.v6, self.zone, io, data, dst)
        }
        none {
            match self.peer {
                some(_) {
                    try UdpSendConnected(self.h, io, data)
                }
                none {
                    try CancelPoint(io)
                    fail InvalidAddress
                }
            }
        }
    }
}

/// Sends data as one datagram to `to`, or to the connected peer when `to` is
/// none. Fails with InvalidAddress when `to` is none on an unconnected
/// socket, or with Datagram.Send's errors. One runtime call under the write
/// lock; allocates nothing.
export SharedDatagram.Send(&self, io: Io, data: []u8, to: ?Addr): Error!void {
    g := try self.writes.Lock(io)
    defer g.Unlock()
    try self.SendOne(io, data, to)
}

/// Sends `buf` as datagrams of `seg` bytes each (the last may be shorter), in
/// order, to `to` (or the connected peer when `to` is none). Fails with
/// Os(code: 0) when `seg <= 0`, or with Send's errors; an earlier datagram may
/// have been sent on failure. One syscall per datagram in this release;
/// allocates nothing.
export SharedDatagram.SendBatch(&self, io: Io, buf: []u8, seg: int, to: ?Addr): Error!void {
    if seg <= 0 {
        try CancelPoint(io)
        fail Os(code: 0)
    }
    g := try self.writes.Lock(io)
    defer g.Unlock()
    var off := 0
    for off < buf.Len() {
        rem := buf.Len() - off
        n := if rem > seg { seg } else { rem }
        try self.SendOne(io, buf[off..off + n], to)
        off += n
    }
}

/// Waits for one datagram into `buf`, returning its byte count and sender.
/// Canceled consumes nothing. Errors as Datagram.Recv's. One runtime call
/// under the read lock; allocates nothing.
export SharedDatagram.Recv(&self, io: Io, buf: mut []u8): Error!(n: int, peer: Addr) {
    g := try self.reads.Lock(io)
    defer g.Unlock()
    return try UdpRecv(self.h, self.zone, io, buf)
}

/// Receives one queued datagram into `buf` without waiting, or returns none
/// immediately when nothing is queued. Errors as Datagram.Recv's. One runtime
/// call under the read lock; allocates nothing.
export SharedDatagram.TryRecv(&self, io: Io, buf: mut []u8): Error!?(n: int, peer: Addr) {
    g := try self.reads.Lock(io)
    defer g.Unlock()
    var from := core.IoAddr(ip: [0; 16], port: 0, v6: false, zone: 0)
    r := task.NativeTryRecvFrom(io, self.h, buf, mut from)
    if r.err == core.SysErr.None && r.value == -1 {
        return none
    }
    n := try CheckCount(r, buf.Len())
    if !PeerZoneOk(&from, self.zone) {
        fail InvalidAddress
    }
    return some((n: n, peer: Unmapped(Addr(ip: from.ip, port: from.port, v6: from.v6))))
}

/// The port the socket is bound to. O(1); allocates nothing.
export SharedDatagram.Port(&self): int {
    return self.bound
}

/// The bound address of this socket (zero Addr for another Io's socket).
/// O(1): one system call; allocates nothing.
export SharedDatagram.LocalAddr(&self): Addr {
    return UdpLocalAddr(self.h)
}

/// Closes the socket. Infallible and not cancellable. O(1); allocates nothing.
export SharedDatagram.Close(self) {
    SharedDatagram(h, bound: _, v6: _, zone: _, peer: _, reads, writes) := self
    _ = reads.Into()
    _ = writes.Into()
    rt.Abandon(h)
}
