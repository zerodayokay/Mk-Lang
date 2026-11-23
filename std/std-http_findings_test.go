// Why: regression tests for framing, limit and cancellation bugs found in
// http.Fetch and http.Serve.
// What: TestHttp_FetchFindings, TestHttp_ServeFindings and TestHttp_BodyCommit.
// How: a Mk client against Go servers that send exactly the bytes a case needs or
// stall, and a Mk server driven by raw Go connections; dev and release, Windows host.

package std

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// htFetchOutMain runs one call and prints its outcome: mode is "fetch"
// (http.Fetch of method url with a body of n bytes and an optional field)
// or "readreq" (http.ReadRequest from a connection to url); a task cancels
// the scope after ms milliseconds when ms > 0. Kinds: 1 Canceled, 2
// net.Reset, 3 WriteError, 4 TooLarge, 5 BadRequest, 6 Closed, 9 other.
const htFetchOutMain = `import std.http
import std.net
import std.time

struct Out {
    kind: int
    off: int
    status: int
    blen: int
    seen: str
}

Stop(s: &Scope, io: Io, ms: i64) {
    io.Sleep(time.Millis(ms)) else {}
    s.Cancel()
}

Fault(e: http.Error): Out {
    var k := 9
    var off := -1
    match e {
        Canceled {
            k = 1
        }
        net.Reset {
            k = 2
        }
        WriteError {
            k = 3
        }
        http.TooLarge {
            k = 4
        }
        http.BadRequest(offset) {
            k = 5
            off = offset
        }
        Closed {
            k = 6
        }
        _ {}
    }
    return Out(kind: k, off: off, status: 0, blen: 0, seen: "")
}

// ReadOne dials addr and reads one request from it.
ReadOne(a: &Arena into a, io: Io, addr: str): http.Error!int {
    conn := try net.Dial(io, addr)
    defer conn.Close()
    var rb: [512]u8 = [0; 512]
    var r := conn.Reader(io, mut rb[..])
    req := try http.ReadRequest(a, io, mut r, 1 << 20)
    return req.body.Len()
}

Do(a: &Arena into a, io: Io, mode: str, method: str, url: str, hs: []http.Header, body: []u8): Out from a {
    if mode == "readreq" {
        n := ReadOne(a, io, url) else e {
            return Fault(e)
        }
        return Out(kind: 0, off: -1, status: 0, blen: n, seen: "")
    }
    reply := http.Fetch(a, io, method, url, hs, body, 67108864) else e {
        return Fault(e)
    }
    var seen := ""
    for i in 0..reply.headers.Len() {
        if reply.headers[i].name == "X-Seen" {
            seen = reply.headers[i].value
        }
    }
    return Out(kind: 0, off: -1, status: reply.status, blen: reply.body.Len(), seen: seen)
}

export Main(sys: &System): http.Error|ParseError!void {
    io := sys.io
    a := sys.arena
    n := try sys.args[4].ParseInt()
    ms := try sys.args[5].ParseInt()
    var body := try a.Slice[u8](int(n))
    for i in 0..body.Len() {
        body[i] = 0x62
    }
    var hs := List[http.Header].New(a)
    if sys.args.Len() > 7 {
        try hs.Push(http.Header(name: sys.args[6], value: sys.args[7]))
    }
    var out := Out(kind: 0, off: -1, status: 0, blen: 0, seen: "")
    scope s := io.Scope() {
        if ms > 0 {
            s.go Stop(&s, io, ms)
        }
        out = Do(a, io, sys.args[1], sys.args[2], sys.args[3], hs.Items(), body[..])
        s.Cancel()
    }
    var o := sys.Stdout()
    if out.kind != 0 {
        try Print(mut o, "err {k} off {f}\n", (k: out.kind, f: out.off))
    } else {
        try Print(mut o, "status {st} len {n} seen {v}\n", (st: out.status, n: out.blen, v: out.seen))
    }
    try o.Flush()
}
`

// htFindServeMain is a server whose handlers break framing rules Serve
// must not take on trust, and a route table for Dispatch (most specific
// match, and 405 when only the method differs).
const htFindServeMain = `import std.http

struct App {
    n: int
}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"hi\n")
}

Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(req.body)
}

LenLie(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Content-Length", "5")
    try res.Body(b"12345")
    fail http.Status(code: 404)
}

Short(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Content-Length", "3")
    try res.Body(b"0123456789")
}

Gzip(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Transfer-Encoding", "gzip")
    try res.Body(b"abc")
}

NoContent(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Transfer-Encoding", "chunked")
    try res.Body(b"abc")
    res.Status(204)
}

FailCode(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    n := req.ParamInt("n") else {
        fail http.Status(code: 400)
    }
    fail http.Status(code: int(n))
}

SetCode(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    n := req.ParamInt("n") else {
        fail http.Status(code: 400)
    }
    res.Status(int(n))
}

CloseMe(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Connection", "close")
    try res.Body(b"bye\n")
}

A(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"A")
}

B(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"B")
}

AB(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"ab")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/hello", handler: Hello),
    http.Route[App](method: "POST", pattern: "/echo", handler: Echo),
    http.Route[App](method: "GET", pattern: "/lenlie", handler: LenLie),
    http.Route[App](method: "GET", pattern: "/short", handler: Short),
    http.Route[App](method: "GET", pattern: "/gzip", handler: Gzip),
    http.Route[App](method: "GET", pattern: "/nocontent", handler: NoContent),
    http.Route[App](method: "GET", pattern: "/fail/{n}", handler: FailCode),
    http.Route[App](method: "GET", pattern: "/set/{n}", handler: SetCode),
    http.Route[App](method: "GET", pattern: "/closeme", handler: CloseMe),
    http.Route[App](method: "GET", pattern: "/notes/{id}", handler: A),
    http.Route[App](method: "GET", pattern: "/notes/new", handler: B),
    http.Route[App](method: "POST", pattern: "/x", handler: A),
    http.Route[App](method: "GET", pattern: "/ab", handler: AB),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    app := App(n: 0)
    try http.Serve(sys, sys.args[1], 65536, &app, Handle)
}
`

func htBuildProg(t *testing.T, root, name, src, prof string) string {
	t.Helper()
	stdDir := filepath.Join(root, "std")
	if _, err := os.Stat(stdDir); err != nil {
		htStd(t, root, nil)
	}
	dir := filepath.Join(root, name+"-"+prof)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	exe := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	args := []string{"build", "-o", exe, "main.mk"}
	if prof == "release" {
		args = []string{"build", "--release", "-o", exe, "main.mk"}
	}
	out, status := mkCmd(t, dir, stdDir, args...)
	if status != 0 {
		if strings.Contains(out, "no usable LLVM") {
			t.Skipf("needs the LLVM toolchain: %s", out)
		}
		t.Fatalf("mk build: exit %d\n%s", status, out)
	}
	return exe
}

// htScript serves every connection with script and then holds it open
// until the test ends, so a stalled peer stays stalled.
func htScript(t *testing.T, script func(c net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
			go script(c)
		}
	}()
	return l.Addr().String()
}

// htFetchLimit is the byte limit htFetchOutMain passes to http.Fetch.
const htFetchLimit = 67108864

func TestHttp_FetchFindings(t *testing.T) {
	head200 := "HTTP/1.1 200 OK\r\n\r\n"
	// A reply delimited by the end of the connection: the head, then n
	// bytes, then the close.
	closing := func(n int) string {
		return htScript(t, func(c net.Conn) {
			defer c.Close()
			br := bufio.NewReader(c)
			if _, err := http.ReadRequest(br); err != nil {
				return
			}
			io.WriteString(c, head200)
			chunk := []byte(strings.Repeat("z", 1<<16))
			for left := n; left > 0; {
				k := min(left, len(chunk))
				if _, err := c.Write(chunk[:k]); err != nil {
					return
				}
				left -= k
			}
		})
	}
	interim := "HTTP/1.1 100 Continue\r\n\r\n"
	interims := func(k int, final string) string {
		return htScript(t, func(c net.Conn) {
			defer c.Close()
			for range k {
				if _, err := io.WriteString(c, interim); err != nil {
					return
				}
			}
			io.WriteString(c, final)
			if k < 0 {
				// Without end.
				for {
					if _, err := io.WriteString(c, interim); err != nil {
						return
					}
				}
			}
		})
	}
	stall := func(prefix string) string {
		return htScript(t, func(c net.Conn) {
			if prefix != "" {
				io.WriteString(c, prefix)
			}
		})
	}
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen", fmt.Sprintf("te=%v cl=%d", r.TransferEncoding, r.ContentLength))
		w.Write(b)
	}))
	defer echo.Close()
	budget := htFetchLimit - len(head200)
	cases := []struct {
		name              string
		mode, method, url string
		body              int
		ms                int
		hdr               []string
		want              string
	}{
		{"G-H1 close-delimited body at the limit", "fetch", "GET", "http://" + closing(budget) + "/", 0, 0, nil, fmt.Sprintf("status 200 len %d seen ", budget)},
		{"G-H1 close-delimited body one byte over the limit", "fetch", "GET", "http://" + closing(budget+1) + "/", 0, 0, nil, "err 4 off -1"},
		{"G-H2 five interim replies then the final one", "fetch", "GET", "http://" + interims(5, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok") + "/", 0, 0, nil, "status 200 len 2 seen "},
		{"G-H2 a sixth interim reply", "fetch", "GET", "http://" + interims(6, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok") + "/", 0, 0, nil, fmt.Sprintf("err 5 off %d", 5*len(interim))},
		{"G-H2 interim replies without end", "fetch", "GET", "http://" + interims(-1, "") + "/", 0, 0, nil, fmt.Sprintf("err 5 off %d", 5*len(interim))},
		{"G-H6 the caller's Transfer-Encoding is left out", "fetch", "POST", echo.URL + "/", 2, 0, []string{"Transfer-Encoding", "chunked"}, "status 200 len 2 seen te=[] cl=2"},
		{"G-H7 C-HTTP-CC-1 Fetch cancelled mid-head", "fetch", "GET", "http://" + stall("HTTP/1.1 200 OK\r\nContent-Le") + "/", 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 Fetch cancelled mid Content-Length body", "fetch", "GET", "http://" + stall("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc") + "/", 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 Fetch cancelled mid chunked body", "fetch", "GET", "http://" + stall("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab") + "/", 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 Fetch cancelled mid close-delimited body", "fetch", "GET", "http://" + stall("HTTP/1.1 200 OK\r\n\r\nabc") + "/", 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 Fetch cancelled in the head read", "fetch", "GET", "http://" + stall("") + "/", 0, 500, nil, "err 1 off -1"},
		{"C-HTTP-CC-1 Fetch cancelled writing a body the server never reads", "fetch", "POST", "http://" + stall("") + "/", 16 << 20, 1000, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 ReadRequest cancelled mid-head", "readreq", "GET", stall("GET / HTTP/1.1\r\nHo"), 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 ReadRequest cancelled mid-body", "readreq", "GET", stall("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nab"), 0, 500, nil, "err 1 off -1"},
		{"G-H7 C-HTTP-CC-1 ReadRequest cancelled mid chunked body", "readreq", "GET", stall("POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab"), 0, 500, nil, "err 1 off -1"},
		{"ReadRequest without a cancel reads the request", "readreq", "GET", stall("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\n\r\nabc"), 0, 0, nil, "status 0 len 3 seen "},
	}
	root := t.TempDir()
	for _, prof := range []string{"dev", "release"} {
		exe := htBuildProg(t, root, "fetchout", htFetchOutMain, prof)
		for _, c := range cases {
			t.Run(prof+"/"+c.name, func(t *testing.T) {
				args := append([]string{c.mode, c.method, c.url, fmt.Sprint(c.body), fmt.Sprint(c.ms)}, c.hdr...)
				r := runNative(t, exe, "", args...)
				if r.code != 0 {
					t.Fatalf("exit %d\n%s", r.code, r.stderr)
				}
				if got := strings.TrimSpace(strings.ReplaceAll(r.stdout, "\r\n", "\n")); got != strings.TrimSpace(c.want) {
					t.Errorf("got %q, want %q", got, c.want)
				}
				if c.ms > 0 && r.took > time.Duration(c.ms)*time.Millisecond+10*time.Second {
					t.Errorf("took %v after a cancel at %d ms", r.took, c.ms)
				}
			})
		}
	}
}

// htReplies reads every reply on c until the end of the stream, each with
// its body, and the bytes left over that are no reply.
func htReplies(t *testing.T, addr, req string) ([]string, string) {
	t.Helper()
	out := htRaw(t, addr, req)
	br := bufio.NewReader(strings.NewReader(out))
	var got []string
	for {
		if _, err := br.Peek(1); err != nil {
			return got, ""
		}
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			rest, _ := io.ReadAll(br)
			return got, string(rest)
		}
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return got, "body: " + err.Error()
		}
		got = append(got, fmt.Sprintf("%d %q close=%v", res.StatusCode, b, res.Close))
	}
}

func TestHttp_ServeFindings(t *testing.T) {
	root := t.TempDir()
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			p := htStartServerProcArgs(t, root, htFindServeMain, prof)
			addr := p.addr
			req := func(path string) string {
				return "GET " + path + " HTTP/1.1\r\nHost: x\r\n\r\n"
			}
			last := "GET /hello HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"
			check := func(name, stream string, want ...string) {
				t.Helper()
				got, rest := htReplies(t, addr, stream)
				if strings.Join(got, "\n") != strings.Join(want, "\n") || rest != "" {
					t.Errorf("%s: replies\n%s\nwant\n%s\nleft over %q", name, strings.Join(got, "\n"), strings.Join(want, "\n"), rest)
				}
			}
			hi := `200 "hi\n" close=true`
			check("G-H3 a handler's Content-Length, then Status(404)", req("/lenlie")+last, `404 "" close=false`, hi)
			check("G-H3 a Content-Length that disagrees with the body", req("/short")+last, `200 "0123456789" close=false`, hi)
			check("G-H3 a Transfer-Encoding other than chunked", req("/gzip")+last, `200 "abc" close=false`, hi)
			check("G-H3 204 with a handler's Transfer-Encoding", req("/nocontent")+last, `204 "" close=false`, hi)
			if out, _, _ := strings.Cut(htRaw(t, addr, req("/nocontent")+last), "HTTP/1.1 200"); strings.Contains(out, "Transfer-Encoding") || strings.Contains(out, "Content-Length") {
				t.Errorf("G-H3 204: framing fields on the wire: %q", out)
			}
			check("G-H4 Status(code: 42)", req("/fail/42")+last, `500 "" close=true`)
			check("G-H4 Status(code: 100)", req("/fail/100")+last, `500 "" close=true`)
			check("G-H4 Status(code: 1000)", req("/fail/1000")+last, `500 "" close=true`)
			check("G-H4 Response.Status(42) without a fail", req("/set/42")+last, `500 "" close=true`)
			check("G-H4 Status(code: 599) is a status", req("/fail/599")+last, `599 "" close=false`, hi)
			check("G-H4 the server still serves", last, hi)
			check("G-H10 a handler's Connection: close", req("/closeme")+last, `200 "bye\n" close=true`)
			check("the most specific route wins (was the first match)", req("/notes/new")+last, `200 "B" close=false`, hi)
			check("only the method differs: 405 (was 404)", req("/x")+last, `405 "" close=false`, hi)
			check("G-H8 a literal matches the decoded segment", req("/a%62")+last, `200 "ab" close=false`, hi)
		})
	}
}

// TestHttp_BodyCommit: a declared Content-Length commits no
// server memory before its bytes arrive. n connections each send a head
// that declares a 65,000-byte body and nothing more; the server's private
// bytes per connection must stay near those of n connections that sent
// only half a head (Windows: private bytes; the measure is htPrivateBytes').
func TestHttp_BodyCommit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("private bytes are measured on Windows")
	}
	const n = 200
	p := htStartServerProcArgs(t, t.TempDir(), htFindServeMain, "release")
	pid := p.cmd.Process.Pid
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	open := func(send string) int64 {
		t.Helper()
		for range n {
			c, err := net.Dial("tcp", p.addr)
			if err != nil {
				t.Fatal(err)
			}
			io.WriteString(c, send)
			conns = append(conns, c)
		}
		// Every connection is accepted and has read: a later one answered.
		if got, _ := htReplies(t, p.addr, "GET /hello HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); len(got) != 1 {
			t.Fatalf("hello after the connections: %v", got)
		}
		time.Sleep(300 * time.Millisecond)
		v, _, err := htPrivateBytes(pid)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	base, _, err := htPrivateBytes(pid)
	if err != nil {
		t.Fatal(err)
	}
	afterHead := open("POST /echo HTTP/1.1\r\nHo")
	afterBody := open("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 65000\r\n\r\n")
	perHead := (afterHead - base) / n
	perBody := (afterBody - afterHead) / n
	t.Logf("private bytes per connection: %d mid-head, %d with a declared 65,000-byte body (base %d)", perHead, perBody, base)
	if perBody-perHead > 8192 {
		t.Errorf("a declared body commits %d bytes per connection more than half a head; want under 8 KiB", perBody-perHead)
	}
}
