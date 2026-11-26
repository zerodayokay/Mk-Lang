// Why: Go's standard library has no WebSocket, so std.websocket needs an
// independent peer to prove it speaks RFC 6455 on the wire.
// What: TestStdWs_GoPeer, TestStdWs_GoPeerChecksTheMaskBit and the peer wsConn.
// How: a Go server and client written from the RFC check every byte the Mk
// side writes over loopback.

// A Go peer written against RFC 6455 as the oracle for std.websocket:
// over loopback, a Mk client (Dial) talks to
// a Go server and a Go client talks to a Mk server (Accept over a
// connection the program read the request from), each side echoing what
// it receives. Go checks every byte the Mk side writes: the 101 and its
// accept value, the mask bit of each frame (set from a client, clear from
// a server), the echoed payloads of RFC 6455 5.7's frames, fragmented
// messages with a Ping between their fragments (answered with a Pong of
// the same payload), messages over 64 KiB, the close handshake begun by
// either side with its status, the Close status the Mk side sends for each
// violation, and a Close the Go side never answers, which must end with
// time.TimedOut at the close limit. Go's standard library has no
// WebSocket, so the peer below is written from the RFC. Each Go connection
// is closed with linger 0 after the Mk side's close, so a case leaves no
// TIME_WAIT entry here.

package std

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// wsPeerProgram is the Mk side; its first argument picks the role.
//
//	client URL  dial URL, echo every message, print "closed CODE after N"
//	server      listen on 127.0.0.1:0, print "port N", accept one
//	            handshake supporting superchat and chat, print
//	            "protocol P" (- for none), echo as client does
//	closer URL  dial, send "bye", close with 1000 "done", print "closed ok"
//	stall URL   dial with a 300 ms close limit, close, print the error
const wsPeerProgram = `import std.http
import std.net
import std.time
import std.url
import std.websocket

export Main(sys: &System): websocket.Error|url.Error|http.Error|WriteError!void {
    mode := sys.args[1]
    if mode == "server" {
        try Serve(sys)
        return
    }
    // The URL keeps its input, an argument, so it lives in an arena of
    // its own, not in sys's.
    var ua := try Arena.New(sys.heap)
    defer ua.Free()
    u := try url.Parse(&ua, sys.args[2], none)
    var o := websocket.DefaultOptions
    if mode == "stall" {
        o.close = time.Millis(300)
    }
    c := websocket.Dial(sys.heap, sys.arena, sys.io, &u, &o) else e {
        try sys.Print("dial {e}\n", (e))
        return
    }
    if mode == "closer" {
        c.Send(sys.io, websocket.Kind.Text, "bye".AsBytes()) else e {
            c.Abandon()
            fail e
        }
        c.Close(sys.io, 1000, "done") else e {
            c.Abandon()
            try sys.Print("close {e}\n", (e))
            return
        }
        try sys.Print("closed ok\n")
        return
    }
    if mode == "stall" {
        c.Close(sys.io, 1000, "") else e {
            c.Abandon()
            try sys.Print("close {e}\n", (e))
            return
        }
        try sys.Print("closed ok\n")
        return
    }
    Echo(sys, c)
}

// Say prints one line and flushes it, so the Go side reads it at once.
Say[A: Fields[Format] + Plain](sys: &System, const f: str, args: A): WriteError!void {
    var w := sys.Stdout()
    try Print(mut w, f, args)
    try w.Flush()
}

// Echo sends back every message c receives, each in an arena of its own,
// until the peer closes or the connection fails, then prints how it ended.
Echo(sys: &System, c: websocket.Conn) {
    var n := 0
    for {
        var a := Arena.New(sys.heap) else {
            c.Abandon()
            return
        }
        m := c.Recv(&a, sys.io) else e {
            a.Free()
            match e {
                websocket.PeerClosed(code) {
                    c.Close(sys.io, 1000, "") else {
                        c.Abandon()
                    }
                    sys.Print("closed {code} after {n}\n", (code, n)) else {}
                }
                _ {
                    c.Abandon()
                    sys.Print("error {e}\n", (e)) else {}
                }
            }
            return
        }
        c.Send(sys.io, m.kind, m.data) else e {
            a.Free()
            c.Abandon()
            sys.Print("send {e}\n", (e)) else {}
            return
        }
        a.Free()
        n += 1
    }
}

AllowAll(origin: str): bool {
    return true
}

// Head reads one request head from s into a (the request's
// fields are views of its head in a, so a stores its own loan).
Head(a: &Arena into a, io: Io, s: &net.Stream): http.Error!http.Request from a {
    var rb: [4096]u8 = [0; 4096]
    var r := s.Reader(io, mut rb[..])
    return try http.ReadRequest(a, io, mut r, 16384)
}

Serve(sys: &System): websocket.Error|http.Error|WriteError!void {
    l := try net.Listen(sys.io, "127.0.0.1:0")
    defer l.Close()
    try Say(sys, "port {p}\n", (p: l.Port()))
    // The request lives in an arena of its own: ReadRequest's arena
    // stores its own loan.
    var ra := try Arena.New(sys.heap)
    defer ra.Free()
    s := try l.Accept(sys.io)
    req := Head(&ra, sys.io, &s) else e {
        s.Close()
        fail e
    }
    t := http.Taken(conn: s.Conn(), rest: "".AsBytes())
    supported: [2]str = ["superchat", "chat"]
    c := websocket.Accept(sys.heap, sys.io, &req, t, supported[..], AllowAll) else e {
        t.Abandon()
        try sys.Print("accept {e}\n", (e))
        return
    }
    p := if q := c.Protocol() { q } else { "-" }
    Say(sys, "protocol {p}\n", (p: p)) else e {
        c.Abandon()
        fail e
    }
    Echo(sys, c)
}
`

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsAccept is RFC 6455 4.2.2's accept value of key.
func wsAccept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// wsFrame is one frame as RFC 6455 5.2 lays it out.
type wsFrame struct {
	fin    bool
	rsv    byte
	op     byte
	masked bool
	data   []byte
}

// wsWrite writes f, masked with a random key when f.masked.
func wsWrite(w io.Writer, f wsFrame) error {
	var h []byte
	b0 := f.op | f.rsv<<4
	if f.fin {
		b0 |= 0x80
	}
	var mbit byte
	if f.masked {
		mbit = 0x80
	}
	n := len(f.data)
	switch {
	case n <= 125:
		h = []byte{b0, mbit | byte(n)}
	case n <= 0xFFFF:
		h = []byte{b0, mbit | 126, byte(n >> 8), byte(n)}
	default:
		h = []byte{b0, mbit | 127, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(h[2:], uint64(n))
	}
	data := f.data
	if f.masked {
		var key [4]byte
		_, _ = rand.Read(key[:])
		h = append(h, key[:]...)
		data = make([]byte, n)
		for i := range data {
			data[i] = f.data[i] ^ key[i%4]
		}
	}
	_, err := w.Write(append(h, data...))
	return err
}

// wsRead reads one frame, unmasking it.
func wsRead(r io.Reader) (wsFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return wsFrame{}, err
	}
	f := wsFrame{fin: h[0]&0x80 != 0, rsv: (h[0] >> 4) & 7, op: h[0] & 0x0F, masked: h[1]&0x80 != 0}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return f, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return f, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var key [4]byte
	if f.masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return f, err
		}
	}
	if n > 1<<26 {
		return f, fmt.Errorf("a frame of %d bytes", n)
	}
	f.data = make([]byte, n)
	if _, err := io.ReadFull(r, f.data); err != nil {
		return f, err
	}
	if f.masked {
		for i := range f.data {
			f.data[i] ^= key[i%4]
		}
	}
	return f, nil
}

// wsMessage reads one data message, answering nothing, and returns its
// opcode and its fragments joined, the control frames read between its
// fragments, and every frame it read (data and control, in order).
func wsMessage(r io.Reader) (byte, []byte, []wsFrame, []wsFrame, error) {
	var ctl, all []wsFrame
	var op byte
	var data []byte
	for {
		f, err := wsRead(r)
		if err != nil {
			return 0, nil, ctl, all, err
		}
		all = append(all, f)
		if f.op >= 8 {
			ctl = append(ctl, f)
			if f.op == 8 {
				return 8, f.data, ctl, all, nil
			}
			continue
		}
		if f.op != 0 {
			op = f.op
		}
		data = append(data, f.data...)
		if f.fin {
			return op, data, ctl, all, nil
		}
	}
}

// wsMaskErr is nil when every frame of fs carries the mask bit the Mk side
// must set (RFC 6455 5.1, 5.3): set from a client, clear from a server,
// the opposite of goMasked, the Go side's own; data frames, pongs and
// closes alike.
func wsMaskErr(fs []wsFrame, goMasked bool) error {
	for i, f := range fs {
		if f.masked == goMasked {
			return fmt.Errorf("frame %d (op %d, %d bytes) has mask bit %v; the Mk side must send %v", i, f.op, len(f.data), f.masked, !goMasked)
		}
	}
	return nil
}

// wsConn is a Go endpoint's side of a connection: masked is true for a
// client.
type wsConn struct {
	c      net.Conn
	r      *bufio.Reader
	masked bool
}

func (w *wsConn) send(t *testing.T, f wsFrame) {
	t.Helper()
	f.masked = w.masked
	if err := wsWrite(w.c, f); err != nil {
		t.Fatal(err)
	}
}

// expect reads one message and fails t unless it is op with data, every
// frame of it, the control frames between its fragments included, masked
// as the Mk side must mask (the opposite of w).
func (w *wsConn) expect(t *testing.T, op byte, data []byte) []wsFrame {
	t.Helper()
	got, payload, ctl, all, err := wsMessage(w.r)
	if err != nil {
		t.Fatalf("reading the echo of op %d: %v", op, err)
	}
	if got != op || !bytes.Equal(payload, data) {
		t.Fatalf("echo: op %d, %d bytes; want op %d, %d bytes", got, len(payload), op, len(data))
	}
	if err := wsMaskErr(all, w.masked); err != nil {
		t.Fatalf("echo of op %d: %v", op, err)
	}
	return ctl
}

// close ends w's connection with a reset, so this side keeps no TIME_WAIT.
func (w *wsConn) close() {
	if tc, ok := w.c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = w.c.Close()
}

// wsServe accepts one connection on l and answers its handshake as RFC
// 6455 4.2.2 says; run is the rest of the conversation.
func wsServe(t *testing.T, l net.Listener, run func(w *wsConn)) chan error {
	done := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			done <- err
			return
		}
		_ = c.SetDeadline(time.Now().Add(60 * time.Second))
		r := bufio.NewReader(c)
		req, err := http.ReadRequest(r)
		if err != nil {
			done <- err
			c.Close()
			return
		}
		key := req.Header.Get("Sec-WebSocket-Key")
		if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
			done <- fmt.Errorf("the client's key %q is not 16 bytes in base64", key)
			c.Close()
			return
		}
		if req.Header.Get("Sec-WebSocket-Version") != "13" || !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
			req.Header.Get("Sec-WebSocket-Extensions") != "" || req.URL.RequestURI() != "/chat?x=1" ||
			req.Host != l.Addr().String() {
			done <- fmt.Errorf("a bad request: %v %v host %q", req.URL, req.Header, req.Host)
			c.Close()
			return
		}
		fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsAccept(key))
		w := &wsConn{c: c, r: r, masked: false}
		run(w)
		done <- nil
	}()
	return done
}

// wsEchoScript sends RFC 6455 5.7's messages and more from w and checks
// each echo; ping is answered with a pong of the same payload.
func wsEchoScript(t *testing.T, w *wsConn) {
	w.send(t, wsFrame{fin: true, op: 1, data: []byte("Hello")})
	w.expect(t, 1, []byte("Hello"))
	big := make([]byte, 70000)
	for i := range big {
		big[i] = byte(i * 31)
	}
	w.send(t, wsFrame{fin: true, op: 2, data: big})
	w.expect(t, 2, big)
	w.send(t, wsFrame{fin: false, op: 1, data: []byte("Hel")})
	w.send(t, wsFrame{fin: true, op: 9, data: []byte("ping!")})
	w.send(t, wsFrame{fin: false, op: 0, data: []byte("l")})
	w.send(t, wsFrame{fin: true, op: 0, data: []byte("o é")})
	ctl := w.expect(t, 1, []byte("Hello é"))
	if len(ctl) != 1 || ctl[0].op != 10 || string(ctl[0].data) != "ping!" {
		t.Fatalf("the ping's answer: %+v", ctl)
	}
	w.send(t, wsFrame{fin: true, op: 2, data: nil})
	w.expect(t, 2, []byte{})
	w.send(t, wsFrame{fin: true, op: 8, data: []byte{0x03, 0xE8, 'b', 'y', 'e'}})
	f, err := wsRead(w.r)
	if err != nil || f.op != 8 || len(f.data) < 2 || binary.BigEndian.Uint16(f.data) != 1000 || f.masked == w.masked {
		t.Fatalf("the close echo: %+v %v", f, err)
	}
}

func TestStdWs_GoPeer(t *testing.T) {
	exe := buildNative(t, nil, wsPeerProgram)

	t.Run("mk client, go server", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		done := wsServe(t, l, func(w *wsConn) {
			defer w.close()
			wsEchoScript(t, w)
		})
		r := runNative(t, exe, "", "client", "ws://"+l.Addr().String()+"/chat?x=1")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if r.code != 0 || !strings.Contains(r.stdout, "closed 1000 after 4") {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
	})

	t.Run("go client, mk server", func(t *testing.T) {
		readLine, wait := net3Start(t, exe, "", "server")
		port := strings.TrimPrefix(readLine(), "port ")
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(60 * time.Second))
		w := &wsConn{c: c, r: bufio.NewReader(c), masked: true}
		key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
		fmt.Fprintf(c, "GET /chat HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n", key)
		resp, err := http.ReadResponse(w.r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) ||
			resp.Header.Get("Sec-WebSocket-Extensions") != "" || !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("the Mk server's reply: %d %v", resp.StatusCode, resp.Header)
		}
		if p, ok := resp.Header["Sec-Websocket-Protocol"]; ok {
			t.Fatalf("no subprotocol was offered, but the 101 names %q", p)
		}
		if line := readLine(); line != "protocol -" {
			t.Fatalf("the Mk server said %q, want protocol -", line)
		}
		wsEchoScript(t, w)
		line := readLine()
		w.close()
		wait()
		if line != "closed 1000 after 4" {
			t.Fatalf("the Mk server said %q", line)
		}
	})

	// A browser that offers subprotocols fails the handshake when
	// the 101 names none, so the server must answer with one. The client
	// offers in two fields (one list, RFC 6455 11.3.4); the Mk server
	// supports superchat and chat, and must answer with the client's first
	// one it supports, chat, in exactly one field.
	t.Run("go client offering subprotocols, mk server", func(t *testing.T) {
		readLine, wait := net3Start(t, exe, "", "server")
		port := strings.TrimPrefix(readLine(), "port ")
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(60 * time.Second))
		w := &wsConn{c: c, r: bufio.NewReader(c), masked: true}
		key := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210"))
		fmt.Fprintf(c, "GET /chat HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: v1.example, chat\r\nSec-WebSocket-Protocol: superchat\r\n\r\n", key)
		resp, err := http.ReadResponse(w.r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) {
			t.Fatalf("the Mk server's reply: %d %v", resp.StatusCode, resp.Header)
		}
		if p := resp.Header.Values("Sec-WebSocket-Protocol"); len(p) != 1 || p[0] != "chat" {
			t.Fatalf("the 101 names subprotocols %q, want exactly chat", p)
		}
		if line := readLine(); line != "protocol chat" {
			t.Fatalf("the Mk server said %q, want protocol chat", line)
		}
		wsEchoScript(t, w)
		line := readLine()
		w.close()
		wait()
		if line != "closed 1000 after 4" {
			t.Fatalf("the Mk server said %q", line)
		}
	})

	t.Run("mk closes first", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		done := wsServe(t, l, func(w *wsConn) {
			defer w.close()
			w.expect(t, 1, []byte("bye"))
			f, err := wsRead(w.r)
			if err != nil || f.op != 8 || !f.masked || string(f.data) != "\x03\xe8done" {
				t.Errorf("the Mk side's close: %+v %v", f, err)
			}
			w.send(t, wsFrame{fin: true, op: 8, data: f.data[:2]})
			// RFC 6455 7.1.1: the server closes the TCP connection first.
		})
		r := runNative(t, exe, "", "closer", "ws://"+l.Addr().String()+"/chat?x=1")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if r.code != 0 || strings.TrimSpace(r.stdout) != "closed ok" {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
	})

	t.Run("a peer that never reads or answers: Close ends with TimedOut at its limit", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		release := make(chan struct{})
		done := wsServe(t, l, func(w *wsConn) {
			<-release
			w.close()
		})
		start := time.Now()
		r := runNative(t, exe, "", "stall", "ws://"+l.Addr().String()+"/chat?x=1")
		took := time.Since(start)
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if r.code != 0 || !strings.Contains(r.stdout, "close TimedOut") {
			t.Fatalf("exit %d after %s\n%s%s", r.code, took, r.stdout, r.stderr)
		}
		if took > 20*time.Second {
			t.Fatalf("Close took %s with a 300 ms limit", took)
		}
	})

	// Each violation a Go server sends: the Close status the Mk client
	// must send back, masked, and the error it reports.
	for _, v := range []struct {
		name   string
		frames []wsFrame
		code   uint16
	}{
		{"a masked frame from a server", []wsFrame{{fin: true, op: 1, masked: true, data: []byte("x")}}, 1002},
		{"a reserved bit", []wsFrame{{fin: true, rsv: 4, op: 1, data: []byte("x")}}, 1002},
		{"a reserved opcode", []wsFrame{{fin: true, op: 3, data: nil}}, 1002},
		{"a fragmented ping", []wsFrame{{fin: false, op: 9, data: nil}}, 1002},
		{"a continuation with no message", []wsFrame{{fin: true, op: 0, data: []byte("x")}}, 1002},
		{"text that is not UTF-8", []wsFrame{{fin: true, op: 1, data: []byte{0xED, 0xA0, 0x80}}}, 1007},
		{"text split inside a character, ending bad", []wsFrame{{fin: false, op: 1, data: []byte{0xC3}}, {fin: true, op: 0, data: []byte{0x28}}}, 1007},
		{"a close status of 1005", []wsFrame{{fin: true, op: 8, data: []byte{0x03, 0xED}}}, 1002},
	} {
		t.Run(v.name, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := wsServe(t, l, func(w *wsConn) {
				defer w.close()
				for _, f := range v.frames {
					if err := wsWrite(w.c, f); err != nil {
						t.Error(err)
						return
					}
				}
				f, err := wsRead(w.r)
				if err != nil || f.op != 8 || !f.masked || len(f.data) != 2 || binary.BigEndian.Uint16(f.data) != v.code {
					t.Errorf("the Mk side's close: %+v %v; want status %d", f, err, v.code)
				}
			})
			r := runNative(t, exe, "", "client", "ws://"+l.Addr().String()+"/chat?x=1")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprint(v.code)
			if r.code != 0 || !strings.HasPrefix(r.stdout, "error Protocol") || !strings.Contains(r.stdout, want) {
				t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
			}
		})
	}
}

// wsUnmaskedClient is std/websocket/conn.mk with a client's frames sent
// unmasked: the mask bit clear and the payload as it is, so only the bit
// is wrong (a planted mutation).
func wsUnmaskedClient(t *testing.T) string {
	b, err := os.ReadFile("websocket/conn.mk")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, r := range [][2]string{
		{"var off := PutHead(mut w.buf[..], op, n, true, key)", "var off := PutHead(mut w.buf[..], op, n, false, key)"},
		{"phase = Mask(mut w.buf[off..off + k], key, phase)", "phase = (phase + k) & 3"},
	} {
		if strings.Count(src, r[0]) != 1 {
			t.Fatalf("conn.mk no longer holds %q once; update the mutation", r[0])
		}
		src = strings.Replace(src, r[0], r[1], 1)
	}
	return src
}

// TestStdWs_GoPeerChecksTheMaskBit: the Go oracle refuses a Mk
// client's frame that is not masked, although its payload is right. The
// Mk client is built with wsUnmaskedClient, and the Go server's check of
// the echo of "Hello", wsMaskErr, must name the bit.
func TestStdWs_GoPeerChecksTheMaskBit(t *testing.T) {
	exe := buildNative(t, map[string]string{"websocket/conn.mk": wsUnmaskedClient(t)}, wsPeerProgram)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var payload []byte
	var maskErr, readErr error
	done := wsServe(t, l, func(w *wsConn) {
		defer w.close()
		w.send(t, wsFrame{fin: true, op: 1, data: []byte("Hello")})
		var all []wsFrame
		_, payload, _, all, readErr = wsMessage(w.r)
		maskErr = wsMaskErr(all, w.masked)
	})
	r := runNative(t, exe, "", "client", "ws://"+l.Addr().String()+"/chat?x=1")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if readErr != nil || string(payload) != "Hello" {
		t.Fatalf("the unmasked client's echo: %q, %v (exit %d)\n%s%s", payload, readErr, r.code, r.stdout, r.stderr)
	}
	if maskErr == nil {
		t.Fatal("wsMaskErr passed an unmasked frame from a client")
	}
}
