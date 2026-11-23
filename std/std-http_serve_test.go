// Why: http.Serve, Dispatch and Response.Send must speak HTTP/1.1 to real clients.
// What: TestHttp_Serve, dev and release.
// How: Go's net/http client and raw connections over loopback: keep-alive, pipelining,
// chunked bodies, HEAD, 404 and 405, Status, 100-continue, the limit, bad requests.

package std

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const htServeMain = `import std.http

struct App {
    greeting: str
}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    name := req.Param("name") else {
        fail http.Status(code: 400)
    }
    var w := res.Writer()
    try Print(mut w, "{greeting}, {name}\n", (greeting: c.greeting, name))
}

Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("X-Path", req.path)
    try res.Header("X-Bad", "a\r\nInjected: 1")
    try res.Body(req.body)
}

Chunks(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("Transfer-Encoding", "chunked")
    n := req.ParamInt("n") else {
        fail http.Status(code: 400)
    }
    var w := res.Writer()
    for i in 0..n {
        try Print(mut w, "line {i}\n", (i))
    }
}

Teapot(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Header("X-Kept", "yes")
    try res.Body(b"dropped")
    fail http.Status(code: 418)
}

Boom(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    fail OutOfMemory
}

Rest(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    rest := req.Param("rest") else {
        fail http.Status(code: 400)
    }
    var w := res.Writer()
    try Print(mut w, "rest={rest}\n", (rest))
}

Fixed(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"fixed\n")
}

Empty(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    res.Status(204)
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/hello/{name}", handler: Hello),
    http.Route[App](method: "POST", pattern: "/echo", handler: Echo),
    http.Route[App](method: "PUT", pattern: "/echo", handler: Echo),
    http.Route[App](method: "GET", pattern: "/chunks/{n}", handler: Chunks),
    http.Route[App](method: "GET", pattern: "/teapot", handler: Teapot),
    http.Route[App](method: "GET", pattern: "/boom", handler: Boom),
    http.Route[App](method: "GET", pattern: "/files/fixed", handler: Fixed),
    http.Route[App](method: "GET", pattern: "/files/{rest...}", handler: Rest),
    http.Route[App](method: "DELETE", pattern: "/empty", handler: Empty),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

export Main(sys: &System): http.Error!void {
    app := App(greeting: "hello")
    try http.Serve(sys, sys.args[1], 4096, &app, Handle)
}
`

// htStartServer builds src and starts it on a free loopback port; the
// process is killed when the test ends.
func htStartServer(t *testing.T, root, src, prof string) string {
	t.Helper()
	return htStartServerProc(t, root, src, prof).addr
}

// htProc is a started server: its address, its process, a channel closed
// once it has exited (cmd.ProcessState is then set), its stderr and stdout.
type htProc struct {
	addr   string
	cmd    *exec.Cmd
	exited chan struct{}
	stderr *bytes.Buffer
	stdout *bytes.Buffer
}

// htStartServerProc is htStartServer, also returning the server's process.
// The port is found by a listen and a close, which another process may take
// before the server binds it on a busy machine, so a server that exits
// before it listens is started once more on a fresh port.
func htStartServerProc(t *testing.T, root, src, prof string) *htProc {
	return htStartServerProcArgs(t, root, src, prof)
}

// htStartServerProcArgs is htStartServerProc with more arguments after the
// address.
func htStartServerProcArgs(t *testing.T, root, src, prof string, extra ...string) *htProc {
	t.Helper()
	stdDir := filepath.Join(root, "std")
	if _, err := os.Stat(stdDir); err != nil {
		htStd(t, root, nil)
	}
	dir := filepath.Join(root, "srv-"+prof)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	exe := filepath.Join(dir, "srv")
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
	for attempt := 0; ; attempt++ {
		p, err := htLaunch(t, exe, extra...)
		if err == nil {
			return p
		}
		if attempt == 1 {
			t.Fatal(err)
		}
		t.Logf("retrying on a fresh port: %v", err)
	}
}

// htLaunch starts exe on a free loopback port and waits until it accepts;
// an error if it exits first or does not listen within 5 s.
func htLaunch(t *testing.T, exe string, extra ...string) (*htProc, error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(exe, append([]string{addr}, extra...)...)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	stop := func() {
		cmd.Process.Kill()
		<-exited
	}
	for i := 0; ; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			c.Close()
			t.Cleanup(func() {
				stop()
				if stderr.Len() > 0 {
					t.Logf("server stderr:\n%s", stderr.String())
				}
			})
			return &htProc{addr: addr, cmd: cmd, exited: exited, stderr: &stderr, stdout: &stdout}, nil
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("server exited before it listened on %s: %s", addr, stderr.String())
		default:
		}
		if i == 100 {
			stop()
			return nil, fmt.Errorf("server at %s did not listen: %v\n%s", addr, err, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// htRaw sends req on a new connection and returns everything the server
// sends before it closes the connection (or 5 s pass).
func htRaw(t *testing.T, addr, req string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestHttp_Serve(t *testing.T) {
	root := t.TempDir()
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			addr := htStartServer(t, root, htServeMain, prof)
			base := "http://" + addr
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 1}}
			get := func(method, path string, body io.Reader, hdr ...string) (*http.Response, string) {
				t.Helper()
				req, err := http.NewRequest(method, base+path, body)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i+1 < len(hdr); i += 2 {
					req.Header.Set(hdr[i], hdr[i+1])
				}
				res, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", method, path, err)
				}
				defer res.Body.Close()
				b, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatalf("%s %s: body: %v", method, path, err)
				}
				return res, string(b)
			}
			// Keep-alive: many requests on the client's one connection.
			for i := range 50 {
				res, b := get("GET", fmt.Sprintf("/hello/w%%20%d", i), nil)
				if res.StatusCode != 200 || b != fmt.Sprintf("hello, w %d\n", i) {
					t.Fatalf("hello %d: %d %q", i, res.StatusCode, b)
				}
			}
			res, b := get("POST", "/echo", strings.NewReader("ping"))
			if res.StatusCode != 200 || b != "ping" || res.Header.Get("X-Path") != "/echo" {
				t.Errorf("echo: %d %q %v", res.StatusCode, b, res.Header)
			}
			if v := res.Header.Get("X-Bad"); v != "a  Injected: 1" || res.Header.Get("Injected") != "" {
				t.Errorf("header splitting: X-Bad %q, Injected %q", v, res.Header.Get("Injected"))
			}
			// A chunked request body (Go chunks a body of unknown length).
			big := strings.Repeat("0123456789", 300)
			res, b = get("PUT", "/echo", io.MultiReader(strings.NewReader(big[:1000]), strings.NewReader(big[1000:])))
			if res.StatusCode != 200 || b != big {
				t.Errorf("chunked echo: %d, %d bytes", res.StatusCode, len(b))
			}
			// A chunked response.
			res, b = get("GET", "/chunks/1000", nil)
			if res.StatusCode != 200 || len(res.TransferEncoding) != 1 || !strings.HasPrefix(b, "line 0\nline 1\n") || !strings.HasSuffix(b, "line 999\n") {
				t.Errorf("chunks: %d %v %d bytes", res.StatusCode, res.TransferEncoding, len(b))
			}
			res, b = get("GET", "/teapot", nil)
			if res.StatusCode != 418 || b != "" || res.Header.Get("X-Kept") != "yes" {
				t.Errorf("teapot: %d %q %v", res.StatusCode, b, res.Header)
			}
			res, _ = get("GET", "/nothing", nil)
			if res.StatusCode != 404 {
				t.Errorf("404: %d", res.StatusCode)
			}
			// A known path with another
			// method is 405, with Allow naming the methods that match.
			res, _ = get("DELETE", "/echo", nil)
			if res.StatusCode != 405 || res.Header.Get("Allow") != "POST, PUT" {
				t.Errorf("DELETE /echo: %d Allow %q, want 405 and Allow POST, PUT", res.StatusCode, res.Header.Get("Allow"))
			}
			res, b = get("HEAD", "/hello/x", nil)
			if res.StatusCode != 200 || b != "" || res.ContentLength != int64(len("hello, x\n")) {
				t.Errorf("HEAD: %d %q %d", res.StatusCode, b, res.ContentLength)
			}
			res, b = get("GET", "/files/fixed", nil)
			if b != "fixed\n" {
				t.Errorf("first match, the literal listed before {rest...}: %q", b)
			}
			res, b = get("GET", "/files/a/b%2Fc", nil)
			if b != "rest=a/b/c\n" {
				t.Errorf("{rest...}: %q", b)
			}
			res, b = get("DELETE", "/empty", nil)
			if res.StatusCode != 204 || b != "" {
				t.Errorf("204: %d %q", res.StatusCode, b)
			}
			res, b = get("POST", "/echo", strings.NewReader("continued"), "Expect", "100-continue")
			if res.StatusCode != 200 || b != "continued" {
				t.Errorf("100-continue: %d %q", res.StatusCode, b)
			}
			res, _ = get("GET", "/boom", nil)
			if res.StatusCode != 500 || !res.Close {
				t.Errorf("handler error: %d close=%v", res.StatusCode, res.Close)
			}

			// Pipelining: three requests in one write, three replies in order.
			out := htRaw(t, addr, "GET /hello/a HTTP/1.1\r\nHost: x\r\n\r\nPOST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\nhiGET /hello/b HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
			br := bufio.NewReader(strings.NewReader(out))
			for k, want := range []string{"hello, a\n", "hi", "hello, b\n"} {
				r, err := http.ReadResponse(br, nil)
				if err != nil {
					t.Fatalf("pipelined reply %d: %v\n%q", k, err, out)
				}
				got, _ := io.ReadAll(r.Body)
				if string(got) != want {
					t.Errorf("pipelined reply %d: %q, want %q", k, got, want)
				}
			}
			if rest, _ := io.ReadAll(br); len(rest) != 0 {
				t.Errorf("bytes after the last reply: %q", rest)
			}
			// HTTP/1.0 closes unless asked to keep alive.
			if out := htRaw(t, addr, "GET /hello/z HTTP/1.0\r\n\r\n"); !strings.HasPrefix(out, "HTTP/1.1 200 OK\r\n") || !strings.Contains(out, "Connection: close\r\n") || !strings.HasSuffix(out, "hello, z\n") {
				t.Errorf("HTTP/1.0: %q", out)
			}
			// HTTP/1.0 keep-alive is answered with the field, and the
			// connection serves the next request.
			out = htRaw(t, addr, "GET /hello/k HTTP/1.0\r\nConnection: keep-alive\r\n\r\nGET /hello/l HTTP/1.0\r\n\r\n")
			if strings.Count(out, "HTTP/1.1 200 OK\r\n") != 2 || !strings.Contains(out, "Connection: keep-alive\r\n") || !strings.HasSuffix(out, "hello, l\n") {
				t.Errorf("HTTP/1.0 keep-alive: %q", out)
			}
			// Malformed requests: 400 and the connection closes.
			for _, bad := range []string{
				"GET / HTTP/1.1\nHost: x\r\n\r\n",
				"GET / HTTP/1.1\r\n\r\n",
				"POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\nTransfer-Encoding: chunked\r\n\r\n",
				"POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n",
				"GET /a%zz HTTP/1.1\r\nHost: x\r\n\r\n",
			} {
				if out := htRaw(t, addr, bad); !strings.HasPrefix(out, "HTTP/1.1 400 Bad Request\r\n") || !strings.Contains(out, "Connection: close\r\n") {
					t.Errorf("%q: %q", bad, out)
				}
			}
			// The limit is 4096 bytes of head and body.
			// A body over the limit is read and dropped before the 413 (up to
			// 256 KiB), so a client that sends it all, as Go's, reads the 413
			// instead of a reset; one waiting for 100 Continue sends nothing,
			// and gets the 413 at once.
			for _, n := range []int{5000, 100000, 262144} {
				if out := htRaw(t, addr, fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", n, strings.Repeat("b", n))); !strings.HasPrefix(out, "HTTP/1.1 413 ") {
					t.Errorf("limit, body of %d sent: %q", n, out)
				}
			}
			if out := htRaw(t, addr, "POST /echo HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 5000\r\n\r\n"); !strings.HasPrefix(out, "HTTP/1.1 413 ") || strings.Contains(out, "100 Continue") {
				t.Errorf("limit, 100-continue: %q", out)
			}
			res, err := client.Post(base+"/echo", "text/plain", strings.NewReader(strings.Repeat("c", 20000)))
			if err != nil || res.StatusCode != 413 {
				t.Errorf("limit, Go client with a 20,000-byte body: %v %v", res, err)
			} else {
				res.Body.Close()
			}
			if out := htRaw(t, addr, "GET /"+strings.Repeat("a", 5000)+" HTTP/1.1\r\nHost: x\r\n\r\n"); !strings.HasPrefix(out, "HTTP/1.1 413 ") {
				t.Errorf("long head: %q", out)
			}
			var cb bytes.Buffer
			cw := httputil.NewChunkedWriter(&cb)
			cw.Write([]byte(strings.Repeat("x", 5000)))
			cw.Close()
			if out := htRaw(t, addr, "POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n"+cb.String()+"\r\n"); !strings.HasPrefix(out, "HTTP/1.1 413 ") {
				t.Errorf("chunked over limit: %q", out[:min(len(out), 200)])
			}
			// The server still serves after all of that.
			if res, b := get("GET", "/hello/end", nil); res.StatusCode != 200 || b != "hello, end\n" {
				t.Errorf("after: %d %q", res.StatusCode, b)
			}
		})
	}
}
