// Why: std.http's client and std.websocket's Dial open https and wss
// connections through a net.Secure; this is TLS's.
//
// What: tls.Connector and its functions Connect and ConnectAlpn, and
// Secured, a private net.Transport over a tls.Conn that maps tls.Error to
// net.Error.
//
// How: the connection is boxed by net.ReserveConn and ConnSlot.Fill: the
// box is reserved first and filled last, so a failed handshake leaves the
// connection with the caller. A caller's ALPN offer (net.Secure.Negotiate)
// runs the handshake on a stack copy of the Config with offer as its
// alpn, so it is checked and used as Config.alpn is.

import std.core
import std.net
import std.time
import std.x509

// Secured is a tls.Conn as a net.Transport. Its Close sends close_notify
// within the Config's close limit and may be called again; Abandon
// releases.
struct Secured {
    c: Conn
}

impl net.Transport for Secured {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return self.c.Read(io, buf) else e {
            fail NetError(&e)
        }
    }

    WriteAll(&self, io: Io, data: []u8): net.Error!void {
        self.c.WriteAll(io, data) else e {
            fail NetError(&e)
        }
    }

    // Conn.Shutdown (conn.mk's L6): a Read it wakes returns 0, a WriteAll it
    // wakes fails with Closed, and the write side sends close_notify first (RFC
    // 8446 6.1) unless a WriteAll is sending, so the peer reads the end of the
    // stream as TLS's, not as a truncation.
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {
        self.c.Shutdown(io, how) else e {
            fail NetError(&e)
        }
    }

    PeerAddr(&self): ?net.Addr {
        return self.c.conn.PeerAddr()
    }

    Close(&self, io: Io): net.Error!void {
        self.c.Notify(io) else e {
            fail NetError(&e)
        }
    }

    Abandon(self) {
        Secured(c) := self
        c.Abandon()
    }
}

// NetError is a failure of an established connection as net.Error: the
// connection's own errors as they are, the protocol's as
// SecureFailed(Protocol).
NetError(e: &Error): net.Error {
    return match *e {
        Alert | Protocol | Unsupported { net.SecureFailed(why: net.SecureFailure.Protocol) }
        x509.Malformed | x509.UnknownAuthority | x509.Expired | x509.NameMismatch | x509.BadSignature | x509.BadUsage | x509.TooComplex | x509.Unsupported {
            net.SecureFailed(why: net.SecureFailure.BadCertificate)
        }
        OutOfMemory { OutOfMemory }
        net.Refused { net.Refused }
        net.Reset { net.Reset }
        net.AddrInUse { net.AddrInUse }
        net.Unreachable { net.Unreachable }
        net.InvalidAddress { net.InvalidAddress }
        net.Os(code) { net.Os(code: code) }
        Canceled { Canceled }
        Closed { Closed }
        time.TimedOut { time.TimedOut }
        net.Unsupported { net.Unsupported }
        net.SecureFailed(why) { net.SecureFailed(why: why) }
        net.StreamReset(code) { net.StreamReset(code: code) }
        net.MuxClosed(code, app, peer) { net.MuxClosed(code: code, app: app, peer: peer) }
    }
}

// HandshakeError is a failed handshake as net.Error: why it failed,
// as net.SecureFailure says it, since no connection exists to ask.
HandshakeError(e: &Error): net.Error {
    return match *e {
        Alert | Protocol | Unsupported { net.SecureFailed(why: net.SecureFailure.Handshake) }
        x509.UnknownAuthority { net.SecureFailed(why: net.SecureFailure.UnknownAuthority) }
        x509.Expired { net.SecureFailed(why: net.SecureFailure.Expired) }
        x509.NameMismatch { net.SecureFailed(why: net.SecureFailure.NameMismatch) }
        x509.Malformed | x509.BadSignature | x509.BadUsage | x509.TooComplex | x509.Unsupported {
            net.SecureFailed(why: net.SecureFailure.BadCertificate)
        }
        _ { NetError(e) }
    }
}

// ConnectFn is the type of the connectors' functions, the call
// net.Secure.Connect makes; NegotiateFn of the ones net.Secure.Negotiate
// makes.
type ConnectFn = fn(&Config, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn
type NegotiateFn = fn(&Config, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn

// WithOffer is c with offer as its ALPN list, or c's own list when offer
// is empty: a copy of the record on the stack, so the handshake checks
// and uses offer exactly as it does Config.alpn. O(1).
WithOffer(c: &Config, offer: []str): Config from c, offer {
    var n := *c
    if offer.Len() > 0 {
        n.alpn = offer
    }
    return n
}

// OfferIndex is the index in offer of the protocol t's handshake chose,
// -1 for none. O(offer's length); allocates nothing.
OfferIndex(t: &Conn, offer: []str): int {
    p := t.Alpn() else {
        return -1
    }
    for i in 0..offer.Len() {
        if offer[i] == p {
            return i
        }
    }
    return -1
}

// ConnectAlpn is the client connector's function: tls.Client over conn
// for host, offering offer's protocols when it is not empty, in a box
// reserved before the handshake and filled after it. Like AcceptAlpn, it
// needs no `h into conn` of its own.
ConnectAlpn(c: &Config, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    slot := try net.ReserveConn[Secured](h)
    n := WithOffer(c, offer)
    t := Client(h, io, conn, &n, host) else e {
        slot.Free(h.Alloc())
        fail HandshakeError(&e)
    }
    chosen := OfferIndex(&t, offer)
    return (conn: slot.Fill(Secured(c: t)), chosen: chosen)
}

// Connect is ConnectAlpn with c's own protocols.
Connect(c: &Config, h: &Heap, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn {
    own: [0]str = []
    (conn: t, chosen: _) := try ConnectAlpn(c, h, io, conn, host, own[..])
    return t
}

/// A client connector over c, to put in http.ClientLimits.secure or
/// websocket.Options.secure: their https and wss connections then go through
/// tls.Client with c, for the URL's host. O(1); allocates nothing until it
/// connects.
///
/// A failed handshake is net.SecureFailed(why): Handshake for an alert or
/// a protocol fault, and BadCertificate, UnknownAuthority, Expired or
/// NameMismatch for the chain. One past c.handshake is time.TimedOut.
/// Its Negotiate offers the caller's protocols in place of c.alpn, with the
/// same checks. It is a plain value that borrows c; copy it freely.
export Connector(c: &Config): net.Secure from c {
    return net.Secure.OfAlpn[Config](core.TypedCall[ConnectFn].Of[Config](c, Connect), core.TypedCall[NegotiateFn].Of[Config](c, ConnectAlpn))
}
