// Why: std.http's client must interoperate with a real server on framing,
// decoding and redirects; Go's net/http is a trusted oracle server.
// What: TestStdHttp_ClientAgainstGoServer (Client.Fetch and Client.Do) and
// TestStdHttp_ServeChecksTheWholeHead.
// How: a Mk program sends one request on loopback; Go checks what the server
// saw (method, target, fields, body) and what the Mk side printed.

package std

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// http2ClientProg: args are URL, mode (fetch, do, deny, gzip, small,
// redir2, redir0, body1m, head1k, twice, twicez, fuzz), method,
// body, and an Authorization value (or "-"). It prints "status N", "h
// name:value" per field, "b BODY" and "end", or "error E".
const http2ClientProg = `import std.compress
import std.http
import std.net
import std.time
import std.url

const Decoders: []compress.Content = [compress.GzipContent, compress.DeflateContent]
const NoHeaders: []http.Header = []

// NotLoopback refuses 127.0.0.0/8 and ::1.
NotLoopback(a: &net.Addr): bool {
    if a.v6 {
        for i in 0..15 {
            if a.ip[i] != 0 {
                return true
            }
        }
        return a.ip[15] != 1
    }
    return a.ip[0] != 127
}

export Main(sys: &System): http.Error|url.Error|WriteError!void {
    var out := sys.Stdout()
    a := sys.arena
    args := sys.args
    u := try url.Parse(a, args[1], none)
    mode := args[2]
    var l := http.DefaultClientLimits
    if mode == "gzip" {
        l.decoders = Decoders
    }
    if mode == "deny" {
        l.allow = some(NotLoopback)
    }
    if mode == "small" {
        l.header_bytes = 64
    }
    if mode == "redir2" {
        l.redirects = 2
    }
    if mode == "redir0" || mode == "do0" {
        l.redirects = 0
    }
    if mode == "body1m" {
        l.body_bytes = 1000000
    }
    if mode == "head1k" {
        l.header_bytes = 1024
    }
    if mode == "twicez" {
        l.decoders = Decoders
    }
    if mode == "fuzz" {
        // Any reply bytes: decode what can be, follow nothing, give up on
        // a head after 2 s.
        l.decoders = Decoders
        l.redirects = 0
        l.response_header = time.Seconds(2)
    }
    c := http.Client.New(sys.heap, l)
    defer c.Close()
    hs: [2]http.Header = [http.Header(name: "Authorization", value: args[5]), http.Header(name: "X-Kind", value: mode)]
    req := http.ClientRequest(method: args[3], url: &u, headers: if args[5] == "-" { hs[1..] } else { hs[..] }, body: args[4].AsBytes())
    if mode == "do" || mode == "do0" {
        try Stream(sys, &c, &req)
        return
    }
    if mode == "twice" || mode == "twicez" {
        // Two requests on one client: the second goes to args[4], with no
        // body, and reuses the first one's connection when it was kept.
        try Once(sys, &c, &req)
        u2 := try url.Parse(a, args[4], none)
        req2 := http.ClientRequest(method: "GET", url: &u2, headers: NoHeaders, body: "".AsBytes())
        try Once(sys, &c, &req2)
        return
    }
    reply := c.Fetch(a, sys.io, &req) else e {
        try Print(mut out, "error {e}\n", (e))
        try out.Flush()
        return
    }
    try Print(mut out, "status {s}\n", (s: reply.status))
    for h in reply.headers {
        try Print(mut out, "h {n}:{v}\n", (n: h.name, v: h.value))
    }
    try out.Str("b ")
    try out.Bytes(reply.body)
    try out.Str("\nend\n")
    try out.Flush()
}

// Once fetches req and prints its status and body on one line.
Once(sys: &System, c: &http.Client, req: &http.ClientRequest): http.Error|WriteError!void {
    var out := sys.Stdout()
    reply := c.Fetch(sys.arena, sys.io, req) else e {
        try Print(mut out, "error {e}\n", (e))
        try out.Flush()
        return
    }
    try Print(mut out, "got {s} ", (s: reply.status))
    try out.Bytes(reply.body)
    try out.Str("\n")
    try out.Flush()
}

// Stream reads the body through Do and Exchange.Body in 7-byte reads.
Stream(sys: &System, c: &http.Client, req: &http.ClientRequest): http.Error|WriteError!void {
    var out := sys.Stdout()
    var arena := try Arena.New(sys.heap)
    defer arena.Free()
    var ex := c.Do(&arena, sys.io, req) else e {
        try Print(mut out, "error {e}\n", (e))
        try out.Flush()
        return
    }
    onerr ex.Abandon()
    try Print(mut out, "status {s}\n", (s: ex.status))
    for h in ex.headers {
        try Print(mut out, "h {n}:{v}\n", (n: h.name, v: h.value))
    }
    try out.Str("b ")
    try Copy(mut ex, mut out)
    try out.Str("\nend\n")
    try out.Flush()
    try ex.Close(sys.io)
}

Copy(ex: mut http.Exchange, out: mut Writer): http.Error|WriteError!void {
    var rb: [16]u8 = [0; 16]
    var r := ex.Body(mut rb[..])
    var piece: [7]u8 = [0; 7]
    for {
        n := try r.Read(mut piece[..])
        if n == 0 {
            return
        }
        try out.Bytes(piece[0..n])
    }
}
`

// http2Seen is what the oracle server saw of one request.
type http2Seen struct {
	mu   sync.Mutex
	reqs []string
}

func (s *http2Seen) add(r *http.Request, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, fmt.Sprintf("%s %s auth=%q kind=%q ae=%q len=%d body=%q", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Kind"), r.Header.Get("Accept-Encoding"), r.ContentLength, body))
}

func (s *http2Seen) all() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.reqs, "\n")
}

// TestStdHttp_ClientAgainstGoServer: Content-Length and chunked replies,
// gzip and deflate bodies with the decoders the client advertises, redirects
// (303 to GET, 307 keeping the body, credentials dropped across origins, the
// redirect limit), HEAD, 204 and 304 with no body read, the head limit,
// NoSecureTransport, and allow refusing loopback before the connect.
func TestStdHttp_ClientAgainstGoServer(t *testing.T) {
	exe := http2Build(t, http2ClientProg)
	seen := &http2Seen{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.add(r, b)
		io.WriteString(w, "other origin")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.add(r, b)
		if strings.HasPrefix(r.URL.Path, "/uri") {
			// The raw target, its decoded path and query value.
			fmt.Fprintf(w, "%s\t%s\t%s", r.RequestURI, r.URL.Path, r.URL.Query().Get("q"))
			return
		}
		switch r.URL.Path {
		case "/echo":
			w.Header().Set("X-Got", string(b))
			fmt.Fprintf(w, "%s %s", r.Method, b)
		case "/chunked":
			for i := range 50 {
				fmt.Fprintf(w, "piece %d;", i)
				w.(http.Flusher).Flush()
			}
		case "/gzip":
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			io.WriteString(zw, strings.Repeat("gzipped ", 100))
			zw.Close()
		case "/deflate":
			w.Header().Set("Content-Encoding", "deflate")
			zw := zlib.NewWriter(w)
			io.WriteString(zw, "deflated body")
			zw.Close()
		case "/see-other":
			http.Redirect(w, r, "/echo", http.StatusSeeOther)
		case "/temporary":
			http.Redirect(w, r, "/echo", http.StatusTemporaryRedirect)
		case "/away":
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/ftp":
			http.Redirect(w, r, "ftp://example.com/", http.StatusFound)
		case "/nocontent":
			w.Header().Set("X-A", "1")
			w.WriteHeader(204)
		case "/notmodified":
			w.WriteHeader(304)
		case "/port":
			io.WriteString(w, r.RemoteAddr)
		case "/port-close":
			w.Header().Set("Connection", "close")
			io.WriteString(w, r.RemoteAddr)
		case "/drop":
			// A full reply that allows keep-alive, then the connection
			// closed under the client: its next request finds it stale.
			hj, _ := w.(http.Hijacker)
			conn, bw, err := hj.Hijack()
			if err != nil {
				return
			}
			bw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\ndrop")
			bw.Flush()
			conn.Close()
		case "/bighead":
			w.Header().Set("X-Big", strings.Repeat("x", 100))
		case "/trailer":
			// A chunked reply whose trailer field is 4,096 bytes.
			w.Header().Set("Trailer", "X-T")
			io.WriteString(w, "body")
			w.(http.Flusher).Flush()
			w.Header().Set("X-T", strings.Repeat("t", 4096))
		case "/zport":
			// A deflate (zlib) body sent chunked: the zlib stream
			// ends before the last chunk.
			w.Header().Set("Content-Encoding", "deflate")
			zw := zlib.NewWriter(w)
			io.WriteString(zw, r.RemoteAddr)
			zw.Flush()
			w.(http.Flusher).Flush()
			zw.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cases := []struct {
		path, mode, method, body, auth string
		want                           []string // lines of the Mk output, in order
		saw                            string   // the server's log must hold this
	}{
		{"/echo", "fetch", "POST", "hello", "-", []string{"status 200", "h X-Got:hello", "b POST hello", "end"}, `POST /echo auth="" kind="fetch" ae="" len=5 body="hello"`},
		{"/echo?q=1&r=%20", "fetch", "GET", "", "-", []string{"status 200", "b GET ", "end"}, `GET /echo?q=1&r=%20`},
		{"/chunked", "fetch", "GET", "", "-", []string{"status 200", "b " + http2Pieces(), "end"}, ""},
		{"/chunked", "do", "GET", "", "-", []string{"status 200", "b " + http2Pieces(), "end"}, ""},
		{"/gzip", "gzip", "GET", "", "-", []string{"status 200", "b " + strings.Repeat("gzipped ", 100), "end"}, `ae="gzip, deflate"`},
		{"/deflate", "gzip", "GET", "", "-", []string{"status 200", "b deflated body", "end"}, ""},
		{"/gzip", "fetch", "GET", "", "-", []string{"status 200", "h Content-Encoding:gzip"}, ""},
		{"/see-other", "fetch", "POST", "data", "-", []string{"status 200", "b GET ", "end"}, `GET /echo auth="" kind="fetch" ae="" len=0 body=""`},
		{"/temporary", "fetch", "POST", "kept", "-", []string{"status 200", "b POST kept", "end"}, `POST /echo auth="" kind="fetch" ae="" len=4 body="kept"`},
		{"/away", "fetch", "GET", "", "Bearer secret", []string{"status 200", "b other origin", "end"}, `GET /landed auth="" kind="fetch"`},
		{"/temporary", "fetch", "GET", "", "Bearer same", []string{"status 200"}, `GET /echo auth="Bearer same"`},
		{"/loop", "fetch", "GET", "", "-", []string{"error TooManyRedirects"}, ""},
		{"/loop", "redir2", "GET", "", "-", []string{"error TooManyRedirects"}, ""},
		// Redirects 0 returns the 3xx reply as it is, through
		// Fetch and through Do.
		{"/loop", "redir0", "GET", "", "-", []string{"status 302", "h Location:/loop", "end"}, ""},
		{"/see-other", "redir0", "POST", "data", "-", []string{"status 303", "h Location:/echo", "end"}, ""},
		{"/loop", "do0", "GET", "", "-", []string{"status 302", "h Location:/loop", "end"}, ""},
		// A trailer past the client's header_bytes (1,024) fails the
		// body read; under the old constant 1 MiB bound it was accepted.
		{"/trailer", "head1k", "GET", "", "-", []string{"error ReadError"}, ""},
		{"/trailer", "fetch", "GET", "", "-", []string{"status 200", "b body", "end"}, ""},
		{"/ftp", "fetch", "GET", "", "-", []string{"status 302"}, ""},
		{"/nocontent", "fetch", "GET", "", "-", []string{"status 204", "h X-A:1", "b ", "end"}, ""},
		{"/notmodified", "fetch", "GET", "", "-", []string{"status 304", "b ", "end"}, ""},
		{"/echo", "fetch", "HEAD", "", "-", []string{"status 200", "b ", "end"}, ""},
		{"/bighead", "small", "GET", "", "-", []string{"error TooLarge"}, ""},
		{"/echo", "deny", "GET", "", "-", []string{"error Refused"}, ""},
	}
	for _, c := range cases {
		t.Run(c.mode+c.path+c.method, func(t *testing.T) {
			r := runNative(t, exe, "", srv.URL+c.path, c.mode, c.method, c.body, c.auth)
			http2Want(t, r.stdout+r.stderr, c.want)
			if c.saw != "" && !strings.Contains(seen.all(), c.saw) {
				t.Errorf("the server never saw %q; it saw:\n%s", c.saw, seen.all())
			}
		})
	}
	t.Run("keep-alive", func(t *testing.T) {
		// The second request reuses the first one's connection: the
		// server sees one peer port twice.
		r := runNative(t, exe, "", srv.URL+"/port", "twice", "GET", srv.URL+"/port", "-")
		got := http2Got(r.stdout)
		if len(got) != 2 || got[0] != got[1] {
			t.Errorf("the connection was not reused: %q\n%s", got, r.stdout+r.stderr)
		}
		// Connection: close is honoured: two connections.
		r = runNative(t, exe, "", srv.URL+"/port-close", "twice", "GET", srv.URL+"/port", "-")
		got = http2Got(r.stdout)
		if len(got) != 2 || got[0] == got[1] {
			t.Errorf("a connection the server closed was reused: %q\n%s", got, r.stdout+r.stderr)
		}
		// A pooled connection the server closed is retried on a new one.
		r = runNative(t, exe, "", srv.URL+"/drop", "twice", "GET", srv.URL+"/port", "-")
		http2Want(t, r.stdout, []string{"got 200 drop"})
		if got := http2Got(r.stdout); len(got) != 2 || !strings.HasPrefix(got[1], "127.0.0.1:") {
			t.Errorf("the stale connection was not retried: %q\n%s", got, r.stdout+r.stderr)
		}
	})
	t.Run("H2-11-deflate-chunked-reply-is-pooled", func(t *testing.T) {
		// A chunked deflate reply decoded by the client: the zlib stream
		// ends before the last chunk, which the client reads on, so the
		// next request reuses the connection (one peer port).
		r := runNative(t, exe, "", srv.URL+"/zport", "twicez", "GET", srv.URL+"/port", "-")
		got := http2Got(r.stdout)
		if len(got) != 2 || got[0] != got[1] {
			t.Errorf("the deflate reply's connection was not reused: %q\n%s", got, r.stdout+r.stderr)
		}
	})
	t.Run("H2-09-target-bytes-outside-RFC-3986-are-escaped", func(t *testing.T) {
		r := runNative(t, exe, "", srv.URL+"/uri|[x]{y}?q={1}^`|", "fetch", "GET", "", "-")
		line := ""
		for _, l := range strings.Split(strings.ReplaceAll(r.stdout, "\r\n", "\n"), "\n") {
			if b, ok := strings.CutPrefix(l, "b "); ok {
				line = b
			}
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("no reply: %s%s", r.stdout, r.stderr)
		}
		for i := 0; i < len(parts[0]); i++ {
			c := parts[0][i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~!$&'()*+,;=:@/?%", c) >= 0) {
				t.Errorf("the target %q holds %q, outside RFC 3986's sets", parts[0], c)
			}
		}
		if parts[1] != "/uri|[x]{y}" || parts[2] != "{1}^`|" {
			t.Errorf("the target %q decodes to %q, q=%q", parts[0], parts[1], parts[2])
		}
		if _, err := url.ParseRequestURI(parts[0]); err != nil {
			t.Errorf("Go refuses the target %q: %v", parts[0], err)
		}
	})
	t.Run("https-without-a-connector", func(t *testing.T) {
		r := runNative(t, exe, "", "https://127.0.0.1:1/", "fetch", "GET", "", "-")
		http2Want(t, r.stdout, []string{"error NoSecureTransport"})
	})
	for _, l := range strings.Split(seen.all(), "\n") {
		if strings.HasPrefix(l, "GET /landed") && strings.Contains(l, "Bearer") {
			t.Errorf("Authorization crossed to another origin: %s", l)
		}
	}
}

func http2Pieces() string {
	var b bytes.Buffer
	for i := range 50 {
		fmt.Fprintf(&b, "piece %d;", i)
	}
	return b.String()
}

// http2Want fails t unless want's lines appear in out in order.
func http2Want(t *testing.T, out string, want []string) {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	i := 0
	for _, l := range lines {
		if i < len(want) && l == want[i] {
			i++
		}
	}
	if i < len(want) {
		t.Errorf("missing %q in output:\n%s", want[i], out)
	}
}

// http2Build builds src as main.mk against a copy of std: the tree's, or
// the one MK_HTTP2_STD names (a snapshot, for when a module in the tree is
// mid-edit and does not parse).
func http2Build(t *testing.T, src string) string {
	t.Helper()
	from := os.Getenv("MK_HTTP2_STD")
	if from == "" {
		return buildNative(t, nil, src)
	}
	root := t.TempDir()
	stdDir := filepath.Join(root, "std")
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".mk") || strings.HasSuffix(p, ".def")) {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(stdDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "main")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	out, status := mkCmd(t, root, stdDir, "build", "-o", exe, "main.mk")
	if status != 0 {
		t.Fatalf("mk build: exit %d: %s", status, out)
	}
	return exe
}

// http2Got is the bodies of the "got 200 BODY" lines of out.
func http2Got(out string) []string {
	var r []string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if b, ok := strings.CutPrefix(l, "got 200 "); ok {
			r = append(r, b)
		}
	}
	return r
}

// http2ServeLimitProg serves with a limit of 64 bytes, head and body.
const http2ServeLimitProg = `import std.http

struct App {
    n: int
}

Ok(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"ok")
}

export Main(sys: &System): http.Error!void {
    app := App(n: 0)
    try http.Serve(sys, sys.args[1], 64, &app, Ok)
}
`

// TestStdHttp_ServeChecksTheWholeHead: a head longer than Serve's limit is
// 413 also when it arrives whole in one read.
func TestStdHttp_ServeChecksTheWholeHead(t *testing.T) {
	exe := http2Build(t, http2ServeLimitProg)
	p, err := htLaunch(t, exe)
	if err != nil {
		t.Fatal(err)
	}
	short := "GET / HTTP/1.1\r\nHost: h\r\n\r\n"
	if got := htRaw(t, p.addr, short+"GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 200") {
		t.Errorf("a short head: %q", got)
	}
	long := "GET / HTTP/1.1\r\nHost: h\r\nX-Long: " + strings.Repeat("a", 100) + "\r\n\r\n"
	if got := htRaw(t, p.addr, long); !strings.HasPrefix(got, "HTTP/1.1 413") {
		t.Errorf("a head over the limit, sent whole: %q", got)
	}
	huge := "GET / HTTP/1.1\r\nHost: h\r\nX-Long: " + strings.Repeat("a", 9000) + "\r\n\r\n"
	if got := htRaw(t, p.addr, huge); !strings.HasPrefix(got, "HTTP/1.1 413") {
		t.Errorf("a head over the limit and over 8 KiB: %q", got)
	}
}
