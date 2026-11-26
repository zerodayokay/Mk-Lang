// Why: the TLS 1.3 handshake state machine runs identically over TCP
// records (RFC 8446 5) and QUIC CRYPTO frames (RFC 9001 4.1); isolating the
// transport wire behind one generic interface avoids duplicating the
// flights while compiling direct calls for each transport.
//
// What: the private Wire interface, RecordWire (the TCP record-layer
// implementation), and InstallRead / InstallWrite.
//
// How: RecordWire holds the net.Conn reference and exclusive loans to the
// connection's ReadHalf and WriteHalf. Feed runs the TLS 1.3 record loop
// (ChangeCipherSpec, early-data skip, plaintext/encrypted alerts and record
// decryption); QuicWire in quic.mk provides the QUIC implementation.

import std.net

// InstallRead turns r's protection to the traffic secret t of suite.
InstallRead(r: mut ReadHalf, suite: Suite, t: &Secret) {
    var k := TrafficKeyOf(suite, t)
    r.aead.Wipe()
    r.aead = Aead.New(&k)
    k.Wipe()
}

// InstallWrite turns w's protection to the traffic secret t of suite.
InstallWrite(w: mut WriteHalf, suite: Suite, t: &Secret) {
    var k := TrafficKeyOf(suite, t)
    w.aead.Wipe()
    w.aead = Aead.New(&k)
    k.Wipe()
}

// Wire is the transport abstraction for ClientFlights and ServerFlights:
// reading handshake bytes into Hs, sending handshake messages, sending
// compatibility ChangeCipherSpec, installing traffic secrets, handing off
// to TLS 1.2, sending fatal alerts, delivering peer QUIC transport
// parameters, and reporting whether the transport is QUIC.
interface Wire {
    Feed(mut self, hs: mut Hs, io: Io): Error!void
    Send(mut self, io: Io, data: []u8): Error!void
    Compat(mut self, io: Io): Error!void
    Read(mut self, suite: Suite, secret: &Secret, app: bool): Error!void
    Write(mut self, suite: Suite, secret: &Secret, app: bool): Error!void
    Tls12(mut self, h: &Heap, a: &Arena, io: Io, c: &Config, hs: mut Hs, m: &Hello, n: int, name: str): Error!Chosen
    Alert(mut self, io: Io, code: u8)
    Peer(mut self, params: []u8): Error!void
    Quic(&self): bool
}

// RecordWire runs the TLS 1.3 handshake over a net.Conn's locked read and
// write halves.
struct RecordWire {
    conn: &net.Conn
    rd: mut ReadHalf
    wr: mut WriteHalf
}

impl Wire for RecordWire {
    Feed(mut self, hs: mut Hs, io: Io): Error!void {
        n := try self.rd.Record(self.conn, io, mut hs.alert)
        at := self.rd.start
        ty := self.rd.buf[at]
        if ty == TypeChangeCipherSpec {
            if hs.ccs || hs.hello_due || n != 1 || self.rd.buf[at + 5] != 1 {
                try Bad(mut hs.alert, AlertUnexpectedMessage)
            }
            hs.ccs = true
            self.rd.Consume(6)
            return
        }
        if !self.rd.aead.on {
            // Before the keys, the peer's alert is plaintext; after them
            // a plaintext alert is a forgery (RFC 8446 5).
            if ty == TypeAlert {
                code := try AlertOf(self.rd.buf[at + 5..at + 5 + n], mut hs.alert)
                self.rd.Consume(5 + n)
                fail Alert(code: code)
            }
            if ty == TypeAppData && 5 + n <= hs.early {
                // Early data before a second ClientHello (4.2.10).
                hs.early -= 5 + n
                self.rd.Consume(5 + n)
                return
            }
            if ty != TypeHandshake || n == 0 {
                try Bad(mut hs.alert, AlertUnexpectedMessage)
            }
            try hs.Append(self.rd.buf[at + 5..at + 5 + n])
            self.rd.Consume(5 + n)
            return
        }
        if ty != TypeAppData {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
        var head: [5]u8 = [0; 5]
        (mut head[..]).CopyFrom(self.rd.buf[at..at + 5])
        var bad: u8 = 0
        r := self.rd.aead.Open(head[..], self.rd.buf[at + 5..at + 5 + n], mut self.rd.plain[..], mut bad) else e {
            if bad != AlertBadRecordMac || 5 + n > hs.early {
                hs.alert = bad
                fail e
            }
            // Early data under a key this server does not have: skipped
            // (4.2.10); a record that fails to open moves no sequence
            // number.
            hs.early -= 5 + n
            self.rd.Consume(5 + n)
            return
        }
        // The first record that opens ends the early data.
        hs.early = 0
        self.rd.Consume(5 + n)
        if r.ty == TypeAlert {
            fail Alert(code: try AlertOf(self.rd.plain[0..r.n], mut hs.alert))
        }
        if r.ty != TypeHandshake || r.n == 0 {
            try Bad(mut hs.alert, AlertUnexpectedMessage)
        }
        try hs.Append(self.rd.plain[0..r.n])
    }

    Send(mut self, io: Io, data: []u8): Error!void {
        try self.wr.Send(self.conn, io, TypeHandshake, data)
    }

    Compat(mut self, io: Io): Error!void {
        try self.wr.Send(self.conn, io, TypeChangeCipherSpec, Ccs[..])
    }

    Read(mut self, suite: Suite, secret: &Secret, app: bool): Error!void {
        InstallRead(mut *self.rd, suite, secret)
        if app {
            self.rd.secret = *secret
        }
    }

    Write(mut self, suite: Suite, secret: &Secret, app: bool): Error!void {
        InstallWrite(mut *self.wr, suite, secret)
        if app {
            self.wr.secret = *secret
        }
    }

    Tls12(mut self, h: &Heap, a: &Arena, io: Io, c: &Config, hs: mut Hs, m: &Hello, n: int, name: str): Error!Chosen {
        return try ClientFlights12(h, a, io, self.conn, c, hs, m, n, name, mut *self.rd, mut *self.wr)
    }

    Alert(mut self, io: Io, code: u8) {
        SendAlert(mut *self.wr, self.conn, io, code) else {}
    }

    Peer(mut self, params: []u8): Error!void {
    }

    Quic(&self): bool {
        return false
    }
}
