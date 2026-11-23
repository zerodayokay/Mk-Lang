// Why: a server reads bytes a hostile client chooses; it must never exit,
// panic or hang, and must send only HTTP/1.1 replies.
// What: FuzzStdHttp_ServeWith: any request bytes to one ServeWith process
// over loopback, a new connection per input (time limits 100 ms here).
// How: the client reads to the server's close and resets, so neither side
// keeps a TIME_WAIT. go test runs the seeds; go test -fuzz explores.
// Its fuzz targets read untrusted input; tools/gate/safety.go lists each untrusted-input target.

package std

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// http2ServeFuzzProg serves with small limits and echoes each request's
// method, target and body.
const http2ServeFuzzProg = `import std.http
import std.net
import std.time

struct App {
    n: int
}

Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    try res.Header("X-Method", req.method)
    try res.Body(req.body)
}

// Watch dials the fuzz worker's lifeline at addr and cancels s when it
// ends: the worker closes it at stop, and the OS closes it when the worker
// dies, so a worker killed at the run's end leaves no server behind.
Watch(io: Io, addr: str, s: &Scope) {
    c := net.Dial(io, addr) else {
        s.Cancel()
        return
    }
    var b: [64]u8 = [0; 64]
    for {
        n := c.Read(io, mut b[..]) else {
            break
        }
        if n == 0 {
            break
        }
    }
    c.Close()
    s.Cancel()
}

export Main(sys: &System): http.Error!void {
    var o := http.DefaultServerOptions
    o.connections = 64
    o.header_bytes = 4096
    o.body_bytes = 65536
    o.request_header = time.Millis(100)
    o.keep_alive = time.Millis(100)
    app := App(n: 0)
    scope top := sys.io.Scope() {
        top.go Watch(sys.io, sys.args[2], &top)
        try http.ServeWith(sys, sys.args[1], &o, &app, Echo)
    }
}
`

// http2FuzzServer is a ServeWith process the fuzz target keeps for all of
// its inputs.
type http2FuzzServer struct {
	addr   string
	cmd    *exec.Cmd
	exited chan struct{}
	stderr bytes.Buffer
	life   net.Conn
}

// http2StartFuzzServer starts exe on a free loopback port and waits until
// it accepts.
func http2StartFuzzServer(t *testing.T, exe string) *http2FuzzServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	s := &http2FuzzServer{addr: addr, exited: make(chan struct{})}
	// The lifeline: the server cancels itself when this connection ends,
	// at stop or when this process dies.
	life, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer life.Close()
	s.cmd = exec.Command(exe, addr, life.Addr().String())
	s.cmd.Stderr = &s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		s.cmd.Wait()
		close(s.exited)
	}()
	life.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Second))
	lc, err := life.Accept()
	if err != nil {
		s.cmd.Process.Kill()
		<-s.exited
		t.Fatalf("the server did not dial its lifeline: %v: %s", err, s.stderr.String())
	}
	s.life = lc
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			http2ResetClose(c)
			return s
		}
		select {
		case <-s.exited:
			t.Fatalf("the server exited before it listened: %s", s.stderr.String())
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.stop()
	t.Fatalf("the server did not listen on %s", addr)
	return nil
}

func (s *http2FuzzServer) stop() {
	http2ResetClose(s.life)
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
		<-s.exited
	}
}

// http2ResetClose closes c with a reset (linger 0): no TIME_WAIT entry on
// either side.
func http2ResetClose(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.Close()
}

// http2Exchange sends req on a new connection and reads until the server
// closes it: the bytes, and whether the end came within 2 s.
func http2Exchange(t *testing.T, addr string, req []byte) ([]byte, bool) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer http2ResetClose(c)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	// A write the server refuses midway (it answered and closed) is fine:
	// what it sent is still read below.
	c.Write(req)
	var out bytes.Buffer
	_, err = io.Copy(&out, c)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return out.Bytes(), false
	}
	return out.Bytes(), true
}

// FuzzStdHttp_ServeWith: any bytes a client sends (request heads in the four
// target forms, Content-Length and chunked bodies, Expect: 100-continue,
// pipelined requests, the smuggling shapes std.http refuses) go to one
// ServeWith process over loopback, a new connection per input. Every
// connection ends within its time limits (100 ms here, so 2 s without an end
// is a hang), and whatever the server sends is HTTP/1.1 replies. go test runs
// the seeds; go test -fuzz FuzzStdHttp_ServeWith -parallel 2 explores (at
// most 5 minutes a run).
func FuzzStdHttp_ServeWith(f *testing.F) {
	for _, s := range []string{
		"GET / HTTP/1.1\r\nHost: h\r\n\r\n",
		"GET /a?b=c HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n",
		"POST /p HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhello",
		"POST /p HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n5;x=1\r\nhello\r\n0\r\nT: v\r\n\r\n",
		"POST /p HTTP/1.1\r\nHost: h\r\nExpect: 100-continue\r\nContent-Length: 2\r\n\r\nok",
		"GET /1 HTTP/1.1\r\nHost: h\r\n\r\nGET /2 HTTP/1.1\r\nHost: h\r\n\r\nGET /3 HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n",
		"GET http://h/x HTTP/1.1\r\nHost: h\r\n\r\n",
		"GET http://other/x HTTP/1.1\r\nHost: h\r\n\r\n",
		"CONNECT h:443 HTTP/1.1\r\nHost: h:443\r\n\r\n",
		"OPTIONS * HTTP/1.1\r\nHost: h\r\n\r\n",
		"GET /a/%2e%2E/b HTTP/1.1\r\nHost: h\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nContent-Length: 4\r\n\r\nabcd",
		"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: gzip, chunked\r\n\r\n0\r\n\r\n",
		"POST / HTTP/1.0\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\nfffffffffffffffffff\r\n",
		"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n5\nhello\r\n0\r\n\r\n",
		"GET / HTTP/1.1\nHost: h\n\n",
		"GET / HTTP/1.1\r\nHost: h\r\nX: a\r\n b\r\n\r\n",
		"GET / HTTP/1.1\r\n Host: h\r\n\r\n",
		"GET / HTTP/1.1\r\nHost : h\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: h\r\nX: a\x00b\r\n\r\n",
		"GET / HTTP/1.1\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: h\r\nX-Long: " + strings.Repeat("a", 5000) + "\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: h\r\nX-Long: " + strings.Repeat("a", 3000) + "\r\n\r\n",
		"\r\n\r\nGET / HTTP/1.1\r\nHost: h\r\n\r\n",
		"GET / HTTP/1.1\r\nHo",
		// The fuzzer's first find: a chunked trailer that never ends held
		// the connection for ever before BodyRead.
		"POST /p HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n5;x=1\r\nhello\r\n0\r\nT: v\xb6\xb6\r\n",
	} {
		f.Add([]byte(s))
	}
	dir := f.TempDir()
	var once sync.Once
	var srv *http2FuzzServer
	f.Cleanup(func() {
		if srv != nil {
			srv.stop()
		}
	})
	f.Fuzz(func(t *testing.T, req []byte) {
		once.Do(func() {
			built := http2Build(t, http2ServeFuzzProg)
			b, err := os.ReadFile(built)
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, filepath.Base(built))
			if err := os.WriteFile(exe, b, 0o755); err != nil {
				t.Fatal(err)
			}
			srv = http2StartFuzzServer(t, exe)
		})
		if srv == nil {
			t.Skip("the server did not start")
		}
		if len(req) > 1<<16 {
			req = req[:1<<16]
		}
		out, ended := http2Exchange(t, srv.addr, req)
		select {
		case <-srv.exited:
			t.Fatalf("the server exited on %q:\n%s", req, srv.stderr.String())
		default:
		}
		if !ended {
			t.Fatalf("the connection did not end within 2 s on %q (got %q)", req, out)
		}
		if len(out) > 0 && !bytes.HasPrefix(out, []byte("HTTP/1.1 ")) {
			t.Fatalf("not an HTTP/1.1 reply on %q: %q", req, out)
		}
	})
}
