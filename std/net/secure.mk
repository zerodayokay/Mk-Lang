// Why: https and wss need TLS, but std.http and std.websocket must not
// import std.tls; net.Secure is the connector they call instead.
// What: Secure, Secure.Of, Secure.OfAlpn, Secure.Connect and
// Secure.Negotiate.
// How: a function and its state, erased by std.core so callers never name
// the state's type. Only std makes one: Secure.Of takes a core.ErasedCall
// and Secure.OfAlpn two core.TypedCalls, which only a std module can build
// (std.core is std-internal), and tls.Connector and tls.Acceptor are the
// two that ship. A connector made by Of has no negotiation call: its
// Negotiate is Connect.

import std.core

/// A plain value that makes a connection secure, made only by std:
/// tls.Connector gives one for a client, tls.Acceptor for a server. Pass it
/// to std.http and std.websocket, which import no TLS of their own. Copy
/// it freely; it borrows the configuration it was made from. call and
/// nego are std's; a program has no use for them.
export struct Secure {
    /// The call std makes the connection secure through; a program has no use
    /// for it.
    export call: core.ErasedCall
    thunk: fn(&Secure, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn
    /// The call std negotiates an application protocol through, none for a
    /// connector made by Of; a program has no use for it.
    export nego: ?core.ErasedCall
    negotiate: fn(&Secure, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn
}

/// The connector over c, which std made with core.ErasedCall.Of[T, F] from
/// a state of type T and its function, F being
/// `fn(&T, h: &Heap into conn, io: Io, conn: net.Conn, host: str): net.Error!net.Conn from h, conn`:
/// given its state, a heap for the new connection's state, the plain
/// connection and the host name (empty on a server), the secure
/// connection. Only std calls this: no program can build a
/// core.ErasedCall. A c made from another pair of types panics (code 8)
/// at Connect, before the function runs. O(1); allocates nothing.
export Secure.Of[T: Share](c: core.ErasedCall): Secure from c {
    return Secure(call: c, thunk: SecureCall[T], nego: none, negotiate: Unnegotiated)
}

/// The connector over c, as Of's, that also negotiates an application
/// protocol through n: given what c's function is given and the protocols
/// to negotiate in preference order (empty for the connector's own), the
/// secure connection and the index in offer of the protocol chosen, -1 for
/// none. Both are the same state's, and std made each with
/// core.TypedCall.Of, so a function of another type is refused at compile
/// time. Only std calls this: no program can build a core.TypedCall.
/// Provisional. O(1); allocates nothing.
export Secure.OfAlpn[T: Share](c: core.TypedCall[fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn], n: core.TypedCall[fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn]): Secure from c, n {
    return Secure(call: c.call, thunk: SecureCall[T], nego: some(n.call), negotiate: NegotiateCall[T])
}

// SecureCall recovers the state and the function as the types Of was
// given; ErasedCall checks them (a mismatch panics with code 8).
SecureCall[T: Share](s: &Secure, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    f := s.call.Fn[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn]()
    st := s.call.State[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn]()
    return try f(st, h, io, conn, host)
}

// NegotiateCall recovers the negotiation call's state and function as the
// types OfAlpn was given, and calls it with offer.
NegotiateCall[T: Share](s: &Secure, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn {
    n := s.nego else {
        // OfAlpn sets nego with this thunk, so this is not reached.
        return try Unnegotiated(s, h, io, conn, host, offer)
    }
    f := n.Fn[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn]()
    st := n.State[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn]()
    return try f(st, h, io, conn, host, offer)
}

// Unnegotiated is the Negotiate of a connector made by Of: its Connect,
// offer unused, and chosen -1.
Unnegotiated(s: &Secure, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn {
    thunk := s.thunk
    c := try thunk(s, h, io, conn, host)
    return (conn: c, chosen: -1)
}

/// Makes conn secure for host (empty on a server) by running the
/// connector's handshake, through one indirect call (O(1) here plus the
/// handshake's cost). On success conn is inside the result. On failure it
/// stays the caller's, mid-handshake, to Close or Abandon. Errors: the
/// connector's; a failed handshake is SecureFailed(why), its time limit
/// time.TimedOut, a cancel Canceled, and after a cancel part of the
/// handshake may have been sent. It allocates on h what the connector's
/// state needs. The connection is boxed on h and holds conn, so h's loan
/// may reach what conn reaches (`h: &Heap into conn`).
export Secure.Connect(&self, h: &Heap into conn, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    thunk := self.thunk
    return try thunk(self, h, io, conn, host)
}

/// Like Connect, but negotiates the application protocol (ALPN, RFC
/// 7301) from offer, in preference order, in place of the connector's own
/// list, for this one handshake: a client offers them, a server accepts
/// them and picks the first of offer the client also offered. chosen is
/// the index in offer of the protocol the handshake chose, -1 when the
/// peer chose none (a client that sent no ALPN, a server that answered
/// none); with offer empty the handshake is Connect's, and chosen is -1.
/// A connector made by Of negotiates nothing: this is its Connect, offer
/// unused, and chosen is -1. Provisional. Errors: Connect's. On a client,
/// an offer std.tls cannot send (a name that is empty or over 255 bytes,
/// or names of more than 65,535 bytes in all) is SecureFailed(why:
/// Handshake) before any I/O; a server never matches such a name. A
/// server that shares none of the client's protocols sends the
/// no_application_protocol alert, and both sides fail with
/// SecureFailed(why: Handshake). O(1) here plus the handshake's cost,
/// which reads the names; allocates what Connect does.
export Secure.Negotiate(&self, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn {
    negotiate := self.negotiate
    return try negotiate(self, h, io, conn, host, offer)
}
