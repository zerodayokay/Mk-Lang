// Why: dialing a QUIC client connection binds a connected UDP socket with a
// 2 MiB receive buffer, sets IPv4 DF where the OS permits, runs the TLS 1.3
// handshake, and hands the completed QConn to net.Mux.
//
// What: Dial and SetDontFragmentUdp.
//
// How: binds an ephemeral UDP socket matching `to`'s address family, connects
// it to `to`, runs QConn.NewClient + QConn.Handshake, and boxes the live
// QConn into net.Mux via core.SealedBox.Fill.

import std.core
import std.net

// Receive buffer size requested on client and server QUIC UDP sockets (3.6.7).
const QuicUdpRecvBuf: int = 2_097_152

// Sets the IPv4/IPv6 Don't Fragment option when supported by the OS.
// Errors are ignored on targets or virtual Ios that do not expose it.
SetDontFragmentUdp(udp: &net.Datagram) {
    udp.SetDontFragment() else {}
}

// Dials a QUIC connection to `to`, authenticating `host` and negotiating `alpn`,
// and returns a net.Mux allocated on `h`.
Dial(h: &Heap, io: Io, cfg: &Config, to: net.Addr, host: str, alpn: str): net.Error!net.Mux from h {
    core.SyncCancel(io) else { fail Canceled }
    bind_addr := if to.v6 { "[::]:0" } else { "0.0.0.0:0" }
    var udp := try net.Bind(io, bind_addr)
    udp.SetBuffers(QuicUdpRecvBuf, 0) else {}
    SetDontFragmentUdp(&udp)
    udp.Connect(io, to) else e {
        udp.Close()
        fail e
    }
    sock := udp.Share()
    onerr sock.Close()
    var qc := QConn.NewClient(h, io, sock, to, cfg) else err {
        fail err
    }
    var empty_qc: ?QConn = none
    var b := Box[?QConn].New(h.Alloc(), empty_qc) else {
        if x := empty_qc {
            x.MuxAbandon()
        }
        qc.MuxAbandon()
        fail OutOfMemory
    }
    alpns: [1]str = [alpn]
    _ = qc.Handshake(h, io, cfg.Tls(), host, alpns[..]) else e {
        qc.MuxAbandon()
        if x := b.Take(h.Alloc()) {
            x.MuxAbandon()
        }
        fail e
    }
    sb := core.SealedBox.Fill[QConn](b, qc)
    return net.Mux.Of[QConn](h, sb)
}
