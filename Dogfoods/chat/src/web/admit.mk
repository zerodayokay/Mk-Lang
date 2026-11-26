// Why: Web clients count against the same limits and slots as TCP clients.
//
// What: Admit and Serve.
//
// How: Admit takes new web clients from the front end's queue and runs each as
// an ordinary server session over a bridge; Serve runs the HTTP server.

import std.http
import std.net
import src.hub
import src.server

/// Starts a session for each web client that arrives, as the accept loop
/// does for TCP: a permit, a slot, and the client's bridge as its
/// connection, until the scope is cancelled.
export Admit(io: Io, f: &Front, hb: &hub.Hub, ends: &server.Endings) *hb into hb {
    scope s := io.ScopeWith(Limits(tasks: hb.cfg.max_clients, stack: StackSize.Default)) {
        for {
            arrival := f.arrivals.Recv(io) else {
                break
            }
            b := &f.bridges[arrival.bridge]
            hb.admit.Acquire(io, 1) else {
                Refuse(b, hb, io)
                break
            }
            id := hb.TakeSlot(io) else {
                hb.admit.Release(1)
                Refuse(b, hb, io)
                break
            }
            link := Link.Of(b, arrival.peer)
            conn := net.NewConn(hb.heap, link) else {
                hb.log.Error(io, "no memory for a web client") else {}
                _ = hb.GiveBack(io, id)
                Refuse(b, hb, io)
                continue
            }
            s.go server.Client(conn, io, hb, id, ends)
        }
    }
}

/// Ends a web client no session was started for: its handler reads the end
/// and lets go of its side, and this lets go of the session's. The log says
/// so at Warn, since the client saw its WebSocket open.
Refuse(b: &Bridge, hb: &hub.Hub, io: Io) {
    calm := io.NoCancel()
    hb.log.Warn(calm, "a web client was turned away: no session could start") else {}
    b.down.Send(calm, Packet.End()) else {}
    b.Let()
}

/// Serves the web front end on `addr` until the scope is cancelled; a
/// failure to serve is logged and kept in `failed`.
export Serve(sys: &System, addr: str, o: &http.ServerOptions, f: &Front, failed: mut ?http.Error) {
    http.ServeWith(sys, addr, o, f, Handle) else e {
        match e {
            Canceled {}
            _ {
                f.hb.log.Error(sys.io, "web front end on {addr} stopped: {e}", (addr, e)) else {}
                *failed = some(e)
            }
        }
    }
}
