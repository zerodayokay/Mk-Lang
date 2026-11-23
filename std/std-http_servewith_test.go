// Why: http.ServeWith must serve real clients and refuse smuggling, with its
// time, size and connection limits holding.
// What: TestStdHttp_ServeWithAgainstGoClient, ServeWithSlowReader and
// ServeWithConnectionCap.
// How: Go's net/http client is the oracle for good requests (and reports a
// reused connection); raw connections send the smuggling corpus.

package std

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

// http2ServeWithProg serves ServeWith with options from its arguments:
// connections, header_bytes, body_bytes, request_header ms, keep_alive ms.
// Every request is answered "METHOD PATH BODYLEN", but /big, answered with
// 32 MiB of zeros (a reply larger than the socket buffers).
const http2ServeWithProg = `import std.http
import std.time

struct App {
    n: int
}

Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    if req.path == "/big" {
        body := try a.Slice[u8](33554432)
        try res.Body(body[..])
        return
    }
    var w := res.Writer()
    try Print(mut w, "{m} {p} {n}", (m: req.method, p: req.path, n: req.body.Len()))
}

export Main(sys: &System): http.Error|ParseError!void {
    args := sys.args
    var o := http.DefaultServerOptions
    o.connections = Cast[int](try args[2].ParseInt())
    o.header_bytes = Cast[int](try args[3].ParseInt())
    o.body_bytes = Cast[int](try args[4].ParseInt())
    o.request_header = time.Millis(try args[5].ParseInt())
    o.keep_alive = time.Millis(try args[6].ParseInt())
    app := App(n: 0)
    try http.ServeWith(sys, args[1], &o, &app, Echo)
}
`

// http2Dial opens a raw connection that closes with a reset (linger 0),
// so a test leaves no TIME_WAIT entry.
func http2Dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.(*net.TCPConn).SetLinger(0)
	t.Cleanup(func() { c.Close() })
	return c
}

// http2ReadReply reads one reply's status line and body from r.
func http2ReadReply(t *testing.T, r *bufio.Reader) (string, string) {
	t.Helper()
	res, err := http.ReadResponse(r, nil)
	if err != nil {
		return "", "error: " + err.Error()
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res.Status, string(b)
}

// http2Closed reports whether c is closed by the peer within d.
func http2Closed(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	var b [1]byte
	_, err := c.Read(b[:])
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return err != nil
}

// TestStdHttp_ServeWithAgainstGoClient: requests with Content-Length and
// chunked bodies, Expect: 100-continue, keep-alive (Go reports the connection
// reused), and over raw connections the smuggling corpus refused with 400 and
// a close, the body limit (413), the head limit (431), and the keep-alive and
// request-header time limits kept apart.
func TestStdHttp_ServeWithAgainstGoClient(t *testing.T) {
	exe := http2Build(t, http2ServeWithProg)
	// request_header 300 ms and keep_alive 1,500 ms, so the two limits can
	// be told apart.
	p, err := htLaunch(t, exe, "4", "1024", "100", "300", "1500")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + p.addr
	tr := &http.Transport{MaxIdleConnsPerHost: 1, ExpectContinueTimeout: 2 * time.Second}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	get := func(req *http.Request) (string, bool) {
		reused := false
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}))
		res, err := cl.Do(req)
		if err != nil {
			return "error: " + err.Error(), reused
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.Status + " " + string(b), reused
	}
	t.Run("content-length-keep-alive-chunked-expect", func(t *testing.T) {
		req, _ := http.NewRequest("POST", base+"/a/b?q", strings.NewReader("hello"))
		if got, _ := get(req); got != "200 OK POST /a/b 5" {
			t.Errorf("Content-Length body: %q", got)
		}
		req, _ = http.NewRequest("GET", base+"/again", nil)
		if got, reused := get(req); got != "200 OK GET /again 0" || !reused {
			t.Errorf("keep-alive: %q reused=%v", got, reused)
		}
		pr, pw := io.Pipe()
		go func() { pw.Write([]byte("abc")); pw.Write([]byte("defg")); pw.Close() }()
		req, _ = http.NewRequest("PUT", base+"/chunked", pr)
		if got, _ := get(req); got != "200 OK PUT /chunked 7" {
			t.Errorf("chunked body: %q", got)
		}
		req, _ = http.NewRequest("POST", base+"/expect", strings.NewReader("12345678"))
		req.Header.Set("Expect", "100-continue")
		if got, _ := get(req); got != "200 OK POST /expect 8" {
			t.Errorf("Expect: 100-continue: %q", got)
		}
	})
	t.Run("smuggling-and-limits-over-the-wire", func(t *testing.T) {
		for _, raw := range []string{
			"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
			"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: gzip, chunked\r\n\r\n",
			"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\nabcd",
			"GET /a\\b HTTP/1.1\r\nHost: h\r\n\r\n",
			"GET http://a/ HTTP/1.1\r\nHost: b\r\n\r\n",
			"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n3\nabc\r\n0\r\n\r\n",
		} {
			c := http2Dial(t, p.addr)
			io.WriteString(c, raw)
			status, _ := http2ReadReply(t, bufio.NewReader(c))
			if !strings.HasPrefix(status, "400") || !http2Closed(c, 2*time.Second) {
				t.Errorf("%q: %q, then not closed", raw, status)
			}
		}
		c := http2Dial(t, p.addr)
		io.WriteString(c, "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 101\r\n\r\n"+strings.Repeat("x", 101))
		if status, _ := http2ReadReply(t, bufio.NewReader(c)); !strings.HasPrefix(status, "413") {
			t.Errorf("a body over body_bytes: %q", status)
		}
		c = http2Dial(t, p.addr)
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: h\r\nX: "+strings.Repeat("x", 1100)+"\r\n\r\n")
		if status, _ := http2ReadReply(t, bufio.NewReader(c)); !strings.HasPrefix(status, "431") {
			t.Errorf("a head over header_bytes (431): %q", status)
		}
	})
	t.Run("time-limits", func(t *testing.T) {
		// A kept-alive connection idle 700 ms (past request_header,
		// inside keep_alive) still serves its next request.
		c := http2Dial(t, p.addr)
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: h\r\n\r\n")
		r := bufio.NewReader(c)
		if status, _ := http2ReadReply(t, r); !strings.HasPrefix(status, "200") {
			t.Fatalf("first request: %q", status)
		}
		time.Sleep(700 * time.Millisecond)
		io.WriteString(c, "GET /next HTTP/1.1\r\nHost: h\r\n\r\n")
		if status, body := http2ReadReply(t, r); !strings.HasPrefix(status, "200") || body != "GET /next 0" {
			t.Errorf("a request 700 ms after the last reply (keep_alive 1,500 ms): %q %q", status, body)
		}
		// keep_alive 1,500 ms: then an idle connection is closed after it.
		if !http2Closed(c, 4*time.Second) {
			t.Errorf("an idle kept-alive connection was not closed after keep_alive")
		}
		// A head split across two writes after 700 ms idle is
		// answered: request_header runs from its first byte.
		c = http2Dial(t, p.addr)
		time.Sleep(700 * time.Millisecond)
		io.WriteString(c, "GET /split HTTP/1.1\r\n")
		time.Sleep(50 * time.Millisecond)
		io.WriteString(c, "Host: h\r\n\r\n")
		if status, body := http2ReadReply(t, bufio.NewReader(c)); !strings.HasPrefix(status, "200") || body != "GET /split 0" {
			t.Errorf("a head split after an idle wait: %q %q", status, body)
		}
		// request_header 300 ms: a head left unfinished is closed, not
		// before it.
		c = http2Dial(t, p.addr)
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: h\r\n")
		if http2Closed(c, 150*time.Millisecond) {
			t.Errorf("an unfinished head was closed before request_header")
		}
		if !http2Closed(c, 3*time.Second) {
			t.Errorf("an unfinished head was not closed after request_header")
		}
	})
}

// TestStdHttp_ServeWithSlowReader: with connections 1 and
// request_header 300 ms, a first client asks for a 32 MiB reply and never
// reads it. Its connection holds the one permit only until the server's
// write waits past request_header for the client to take a piece; then
// the connection is closed and a second client is served. Before the fix
// the second client waited for ever.
func TestStdHttp_ServeWithSlowReader(t *testing.T) {
	exe := http2Build(t, http2ServeWithProg)
	p, err := htLaunch(t, exe, "1", "1024", "100", "300", "5000")
	if err != nil {
		t.Fatal(err)
	}
	// htLaunch's probe connection may still hold the permit: wait until a
	// request is answered, then close that connection.
	w := http2Dial(t, p.addr)
	io.WriteString(w, "GET /w HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n")
	if status, _ := http2ReadReply(t, bufio.NewReader(w)); !strings.HasPrefix(status, "200") {
		t.Fatalf("warm-up request: %q", status)
	}
	w.Close()
	a := http2Dial(t, p.addr)
	a.(*net.TCPConn).SetReadBuffer(4096)
	io.WriteString(a, "GET /big HTTP/1.1\r\nHost: h\r\n\r\n")
	b := http2Dial(t, p.addr)
	io.WriteString(b, "GET /b HTTP/1.1\r\nHost: h\r\n\r\n")
	b.SetReadDeadline(time.Now().Add(15 * time.Second))
	start := time.Now()
	if status, body := http2ReadReply(t, bufio.NewReader(b)); !strings.HasPrefix(status, "200") || body != "GET /b 0" {
		t.Fatalf("the second client was not served while the first never read (%v): %q %q", time.Since(start), status, body)
	}
	t.Logf("the second client was served after %v", time.Since(start))
	// The first client's reply was cut short: it never gets 32 MiB.
	a.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, _ := io.Copy(io.Discard, a)
	if n >= 33554432 {
		t.Errorf("the slow reader got its whole reply (%d bytes)", n)
	}
}

// TestStdHttp_ServeWithConnectionCap: with connections 1, a second
// connection's request waits until the first connection closes.
func TestStdHttp_ServeWithConnectionCap(t *testing.T) {
	exe := http2Build(t, http2ServeWithProg)
	p, err := htLaunch(t, exe, "1", "1024", "100", "5000", "5000")
	if err != nil {
		t.Fatal(err)
	}
	// htLaunch's probe connection may still hold the one permit; wait
	// until a request is answered.
	a := http2Dial(t, p.addr)
	io.WriteString(a, "GET /a HTTP/1.1\r\nHost: h\r\n\r\n")
	ra := bufio.NewReader(a)
	if status, body := http2ReadReply(t, ra); !strings.HasPrefix(status, "200") {
		t.Fatalf("first connection: %q %q", status, body)
	}
	b := http2Dial(t, p.addr)
	io.WriteString(b, "GET /b HTTP/1.1\r\nHost: h\r\n\r\n")
	b.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	rb := bufio.NewReader(b)
	if _, err := rb.Peek(1); err == nil {
		t.Fatalf("the second connection was served while the first held the only permit")
	}
	a.Close()
	b.SetReadDeadline(time.Now().Add(5 * time.Second))
	if status, body := http2ReadReply(t, rb); !strings.HasPrefix(status, "200") || body != "GET /b 0" {
		t.Errorf("second connection after the first closed: %q %q", status, body)
	}
}
