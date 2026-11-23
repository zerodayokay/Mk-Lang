// Why: Response.Flush must deliver bytes before the handler returns, and
// Link.Takeover must hand over the connection with nothing lost.
// What: TestStdHttp_TakeoverAndFlushAgainstGo, both under http.ServeWith.
// How: the handler waits on a gate the Go client opens only after reading the
// flushed bytes; a takeover carries a 101, the pipelined rest, and later bytes.

package std

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// http2TakeFlushProg serves ServeWith on args[1]; args[2] is the address
// of the Go side's gate.
const http2TakeFlushProg = `import std.http
import std.net

struct App {
    gate: str
}

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    if req.path == "/flush" {
        try res.Body("first;".AsBytes())
        try res.Flush(io, link)
        // Wait until the client has read "first;": a body that was not
        // sent would wait here for ever.
        Gate(io, c.gate)
        try Add(res, "second;")
        try res.Flush(io, link)
        try Add(res, "third")
        return
    }
    if req.path == "/df10" {
        // DF-chat-10: each Body after a Flush is new bytes, sent whole.
        try res.Body("AAAA".AsBytes())
        try res.Flush(io, link)
        try res.Body("BBBBBBBBBB".AsBytes())
        try res.Flush(io, link)
        try res.Body("CC".AsBytes())
        return
    }
    if req.path == "/flush-error" {
        try res.Body("partial".AsBytes())
        try res.Flush(io, link)
        fail http.TooLarge
    }
    if req.path == "/take" {
        t := link.Takeover() else {
            try res.Body("not taken".AsBytes())
            return
        }
        http.Taken(conn, rest) := t
        Echo(io, &conn, rest)
        conn.Abandon()
        if again := link.Takeover() {
            again.Abandon()
            Panic("a second Takeover gave the connection again")
        }
        return
    }
    try res.Body("ok".AsBytes())
}

// Add appends s to res's body.
Add(res: mut http.Response, s: str): WriteError!void {
    var w := res.Writer()
    try w.Str(s)
}

// Gate dials addr and reads one byte.
Gate(io: Io, addr: str) {
    g := net.Dial(io, addr) else {
        return
    }
    var b: [1]u8 = [0; 1]
    _ = g.Read(io, mut b[..]) else {
        0
    }
    g.Close()
}

// Echo writes a 101 head, then rest, then echoes what comes until 9 bytes
// in all were echoed or the peer closes.
Echo(io: Io, conn: &net.Conn, rest: []u8) {
    conn.WriteAll(io, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n".AsBytes()) else {
        return
    }
    conn.WriteAll(io, rest) else {
        return
    }
    var got := rest.Len()
    var b: [64]u8 = [0; 64]
    for got < 9 {
        n := conn.Read(io, mut b[..]) else {
            return
        }
        if n == 0 {
            return
        }
        conn.WriteAll(io, b[0..n]) else {
            return
        }
        got += n
    }
}

export Main(sys: &System): http.Error!void {
    app := App(gate: sys.args[2])
    try http.ServeWith(sys, sys.args[1], &http.DefaultServerOptions, &app, Handle)
}
`

// TestStdHttp_TakeoverAndFlushAgainstGo: a flushed body reaches Go's client
// before the handler returns, in chunked framing Go decodes; an error after a
// flush cuts the body short; a taken connection carries a 101 the handler
// writes, the bytes the client pipelined behind its request (rest), and what
// it sends after; a second Takeover is none.
func TestStdHttp_TakeoverAndFlushAgainstGo(t *testing.T) {
	exe := http2Build(t, http2TakeFlushProg)
	gate, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	p, err := htLaunch(t, exe, gate.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + p.addr
	tr := &http.Transport{MaxIdleConnsPerHost: 1}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 20 * time.Second}
	t.Run("flush", func(t *testing.T) {
		res, err := cl.Get(base + "/flush")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if len(res.TransferEncoding) != 1 || res.TransferEncoding[0] != "chunked" || res.ContentLength != -1 {
			t.Errorf("framing: TE %q, length %d", res.TransferEncoding, res.ContentLength)
		}
		first := make([]byte, len("first;"))
		if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "first;" {
			t.Fatalf("the flushed bytes: %q, %v", first, err)
		}
		// The handler now waits on the gate: open it.
		g, err := gate.Accept()
		if err != nil {
			t.Fatal(err)
		}
		g.Write([]byte{1})
		g.Close()
		rest, err := io.ReadAll(res.Body)
		if err != nil || string(rest) != "second;third" {
			t.Errorf("the rest: %q, %v", rest, err)
		}
		// The connection stays usable: keep-alive after a flushed reply.
		r2, err := cl.Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		if string(b) != "ok" {
			t.Errorf("after a flushed reply: %q", b)
		}
	})
	t.Run("DF-chat-10-body-after-flush", func(t *testing.T) {
		// Body, Flush, a longer Body, Flush, a shorter Body: Go decodes
		// chunks of 4, 10 and 2 bytes (before the fix, 4 and 6, and "CC"
		// was lost).
		c := http2Dial(t, p.addr)
		io.WriteString(c, "GET /df10 HTTP/1.1\r\nHost: h\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(c)
		res, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil || string(b) != "AAAABBBBBBBBBBCC" {
			t.Errorf("the body: %q, %v", b, err)
		}
	})
	t.Run("flush-then-error", func(t *testing.T) {
		res, err := cl.Get(base + "/flush-error")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err == nil || string(b) != "partial" {
			t.Errorf("an error after a flush must cut the body short: %q, %v", b, err)
		}
	})
	t.Run("takeover", func(t *testing.T) {
		c := http2Dial(t, p.addr)
		io.WriteString(c, "GET /take HTTP/1.1\r\nHost: h\r\n\r\nPIPE")
		r := bufio.NewReader(c)
		res, err := http.ReadResponse(r, nil)
		if err != nil || res.StatusCode != 101 {
			t.Fatalf("101 head: %v %v", res, err)
		}
		io.WriteString(c, "LINED")
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		got := make([]byte, 9)
		if _, err := io.ReadFull(r, got); err != nil || string(got) != "PIPELINED" {
			t.Errorf("echo through the taken connection: %q, %v", got, err)
		}
		if !http2Closed(c, 5*time.Second) {
			t.Errorf("Abandon did not close the taken connection")
		}
	})
	t.Run("http-1.0-flush", func(t *testing.T) {
		// No chunked framing to an HTTP/1.0 request: no length, and the
		// connection's end ends the body.
		go func() {
			g, err := gate.Accept()
			if err == nil {
				g.Write([]byte{1})
				g.Close()
			}
		}()
		c := http2Dial(t, p.addr)
		io.WriteString(c, "GET /flush HTTP/1.0\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		all, _ := io.ReadAll(c)
		s := string(all)
		if !strings.HasPrefix(s, "HTTP/1.1 200") || strings.Contains(strings.ToLower(s), "transfer-encoding") || !strings.HasSuffix(s, "\r\n\r\nfirst;second;third") {
			t.Errorf("HTTP/1.0 flushed reply: %q", s)
		}
	})
}
