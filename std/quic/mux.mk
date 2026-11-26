// Why: HTTP/3 and any other multiplexed protocol talk to std.quic solely
// through std.net's Mux, MuxListener, MuxDialer and MuxServer interfaces.
//
// What: impl net.MuxEngine for QConn, impl net.MuxListening for Endpoint,
// Connector, and Acceptor.
//
// How: Connector and Acceptor erase Config via core.ErasedCall.Of and wrap it
// in net.MuxDialer.Of / net.MuxServer.Of; QConn and Endpoint implement the
// engine and listener interfaces directly.

import std.core
import std.net

impl net.MuxEngine for QConn {
    Open(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
        return try self.MuxOpen(io, uni, wait)
    }

    Accept(&self, io: Io, uni: bool, wait: bool): net.Error!?(id: i64, gen_: u32) {
        return try self.MuxAccept(io, uni, wait)
    }

    Read(&self, io: Io, id: i64, gen_: u32, buf: mut []u8, wait: bool): net.Error!?int {
        return try self.MuxRead(io, id, gen_, buf, wait)
    }

    Write(&self, io: Io, id: i64, gen_: u32, data: []u8, fin: bool): net.Error!void {
        try self.MuxWrite(io, id, gen_, data, fin)
    }

    Credit(&self, id: i64, gen_: u32, total: i64) {
        self.MuxCredit(id, gen_, total)
    }

    Wait(&self, io: Io, ids: []i64, incoming: bool): net.Error!net.MuxReady {
        return try self.MuxWait(io, ids, incoming)
    }

    Reset(&self, id: i64, gen_: u32, code: u64) {
        self.MuxReset(id, gen_, code)
    }

    Stop(&self, id: i64, gen_: u32, code: u64) {
        self.MuxStop(id, gen_, code)
    }

    Release(&self, id: i64, gen_: u32) {
        self.MuxRelease(id, gen_)
    }

    Limits(&self): net.MuxLimits {
        return self.MuxLimits()
    }

    Usable(&self, io: Io): bool {
        return self.MuxUsable(io)
    }

    PeerAddr(&self): ?net.Addr {
        return self.MuxPeerAddr()
    }

    Close(&self, io: Io, code: u64): net.Error!void {
        try self.MuxClose(io, code)
    }

    Abandon(self) {
        self.MuxAbandon()
    }
}

impl net.MuxListening for Endpoint {
    Run(&self, io: Io): net.Error!void {
        try self.EpRun(io)
    }

    Accept(&self, h: &Heap, io: Io): net.Error!net.Mux from h {
        return try self.EpAccept(h, io)
    }

    Port(&self): int {
        return self.EpPort()
    }

    Abandon(self) {
        self.EpAbandon()
    }
}

DialFn(c: &Config, h: &Heap, io: Io, to: net.Addr, host: str, alpn: str): net.Error!net.Mux from h {
    return try Dial(h, io, c, to, host, alpn)
}

ListenFn(c: &Config, h: &Heap, io: Io, addr: str, alpn: str): net.Error!net.MuxListener from h, c {
    ep := try Endpoint.Listen(h, io, c, addr, alpn)
    var empty_ep: ?Endpoint = none
    var b := Box[?Endpoint].New(h.Alloc(), empty_ep) else {
        if x := empty_ep {
            x.EpAbandon()
        }
        ep.EpAbandon()
        fail OutOfMemory
    }
    sb := core.SealedBox.Fill[Endpoint](b, ep)
    return net.MuxListener.Of[Endpoint](h, sb)
}

/// The client connector over `c`, for `http.ClientLimits.h3` or any program
/// that wants a `net.Mux`. O(1); allocates nothing until it dials.
export Connector(c: &Config): net.MuxDialer from c {
    call := core.ErasedCall.Of[Config, fn(c: &Config, h: &Heap, io: Io, to: net.Addr, host: str, alpn: str): net.Error!net.Mux from h](c, DialFn)
    return net.MuxDialer.Of[Config](call)
}

/// The server's, for `http.ServerOptions.h3`. O(1); allocates nothing
/// until it listens.
export Acceptor(c: &Config): net.MuxServer from c {
    call := core.ErasedCall.Of[Config, fn(st: &Config, h: &Heap, io: Io, addr: str, alpn: str): net.Error!net.MuxListener from h, st](c, ListenFn)
    return net.MuxServer.Of[Config](call)
}
