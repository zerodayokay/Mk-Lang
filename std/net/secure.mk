// Why: https and wss need TLS, but std.http and std.websocket must not
// import std.tls; net.Secure is the connector they call instead.
// What: Secure, Secure.Of, Secure.Connect and Secure.Negotiate.
// How: a function and its state, erased by std.core so callers never name
// the state's type. Only std makes one: Secure.Of takes a core.ErasedCall,
// which only a std module can build (std.core is std-internal), and
// tls.Connector and tls.Acceptor are the two that ship. Connect is
// Negotiate with no protocols of the caller's.

import std.core

/// A plain value that makes a connection secure, made only by std:
/// tls.Connector gives one for a client, tls.Acceptor for a server. Pass it
/// to std.http and std.websocket, which import no TLS of their own. Copy
/// it freely; it borrows the configuration it was made from. call is
/// std's; a program has no use for it.
export struct Secure {
    /// The call std makes the connection secure through; a program has no use
    /// for it.
    export call: core.ErasedCall
    thunk: fn(&Secure, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn
}

/// The connector over c, which std made with core.ErasedCall.Of[T, F] from
/// a state of type T and its function, F being
/// `fn(&T, h: &Heap into conn, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn`:
/// given its state, a heap for the new connection's state, the plain
/// connection, the host name (empty on a server) and the application
/// protocols to negotiate in preference order (empty for the connector's
/// own), the secure connection and the index in offer of the protocol
/// chosen, -1 for none. Only std calls this: no program can build a
/// core.ErasedCall. A c made from another pair of types panics (code 8)
/// at Connect, before the function runs. O(1); allocates nothing.
export Secure.Of[T: Share](c: core.ErasedCall): Secure from c {
    return Secure(call: c, thunk: SecureCall[T])
}

// SecureCall recovers the state and the function as the types Of was
// given; ErasedCall checks them (a mismatch panics with code 8).
SecureCall[T: Share](s: &Secure, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn {
    f := s.call.Fn[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn]()
    st := s.call.State[T, fn(&T, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn]()
    return try f(st, h, io, conn, host, offer)
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
    own: [0]str = []
    (conn: c, chosen: _) := try thunk(self, h, io, conn, host, own[..])
    return c
}

/// Like Connect, but negotiates the application protocol (ALPN, RFC
/// 7301) from offer, in preference order, in place of the connector's own
/// list, for this one handshake: a client offers them, a server accepts
/// them and picks the first of offer the client also offered. chosen is
/// the index in offer of the protocol the handshake chose, -1 when the
/// peer chose none (a client that sent no ALPN, a server that answered
/// none); with offer empty it is Connect, and chosen is -1. Provisional.
/// Errors: Connect's. On a client, a name in offer that is empty or over
/// 255 bytes is SecureFailed(why: Handshake) before anything is sent (a
/// server never matches one). A server that shares none of the client's
/// protocols sends the no_application_protocol alert, and both sides fail
/// with SecureFailed(why: Handshake). O(offer's length) here plus the
/// handshake's cost, which reads the names; allocates what Connect does.
export Secure.Negotiate(&self, h: &Heap into conn, io: Io, conn: Conn, host: str, offer: []str): Error!(conn: Conn, chosen: int) from h, conn {
    thunk := self.thunk
    return try thunk(self, h, io, conn, host, offer)
}
