// Why: cancelling the scope that runs http.Serve must end every connection at
// every await point, and Serve must then return.
// What: TestHttp_Cancel, dev and release.
// How: connections are held at every await of Serve and its handlers (idle, mid-head,
// mid-body, 100 Continue, a full socket, Lock, Sleep), then a /cancel request
// cancels the scope; every client must see its connection end, with no 500.

package std

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const htCancelMain = `import std.http
import std.time

struct State {
    held: int
    waiting: int
    big: int
    cancel: bool
}

struct App {
    gate: Mutex[int]
    state: Mutex[State]
}

// Bump adds to the counters /state reports.
Bump(c: &App, io: Io, held: int, waiting: int, big: int): Canceled!void {
    var g := try c.state.Lock(io)
    var st := *g.Ref()
    st.held += held
    st.waiting += waiting
    st.big += big
    g.Store(st)
    g.Unlock()
}

Hello(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(b"hi\n")
}

Echo(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try res.Body(req.body)
}

// Wait holds the gate's lock, so a second request waits in Lock.
Wait(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try Bump(c, io, 0, 1, 0)
    var g := try c.gate.Lock(io)
    defer g.Unlock()
    try Bump(c, io, 1, -1, 0)
    io.Sleep(time.Seconds(60)) else {
        fail Canceled
    }
}

// Big answers 16 MiB, more than the loopback buffers hold, to a client
// that never reads, so Send blocks.
Big(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    b := try a.Slice[u8](16777216)
    try res.Body(b[..])
    try Bump(c, io, 0, 0, 1)
}

StateOf(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    g := try c.state.Lock(io)
    st := *g.Ref()
    g.Unlock()
    var w := res.Writer()
    try Print(mut w, "held {h} waiting {wt} big {b}\n", (h: st.held, wt: st.waiting, b: st.big))
}

CancelNow(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    var g := try c.state.Lock(io)
    var st := *g.Ref()
    st.cancel = true
    g.Store(st)
    g.Unlock()
    try res.Body(b"ok\n")
}

const Routes: []http.Route[App] = [
    http.Route[App](method: "GET", pattern: "/hello", handler: Hello),
    http.Route[App](method: "POST", pattern: "/echo", handler: Echo),
    http.Route[App](method: "GET", pattern: "/wait", handler: Wait),
    http.Route[App](method: "GET", pattern: "/big", handler: Big),
    http.Route[App](method: "GET", pattern: "/state", handler: StateOf),
    http.Route[App](method: "GET", pattern: "/cancel", handler: CancelNow),
]

Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response): http.Error!void {
    try http.Dispatch(a, io, Routes, c, req, res)
}

// Stop cancels s once /cancel has set the flag.
Stop(s: &Scope, io: Io, c: &App) {
    for {
        io.Sleep(time.Millis(10)) else {
            break
        }
        g := c.state.Lock(io) else {
            break
        }
        done := g.Ref().cancel
        g.Unlock()
        if done {
            break
        }
    }
    s.Cancel()
}

export Main(sys: &System): http.Error!void {
    app := App(gate: Mutex[int].New(0), state: Mutex[State].New(State(held: 0, waiting: 0, big: 0, cancel: false)))
    // got is 1 when Serve failed with Canceled, 2 for another error, 3 when
    // it returned.
    var got := 0
    scope s := sys.io.Scope() {
        s.go Stop(&s, sys.io, &app)
        http.Serve(sys, sys.args[1], 65536, &app, Handle) else e {
            match e {
                Canceled {
                    got = 1
                }
                _ {
                    got = 2
                }
            }
        }
        if got == 0 {
            got = 3
        }
    }
    var o := sys.Stdout()
    try Print(mut o, "serve ended: {got}\n", (got))
}
`

func TestHttp_Cancel(t *testing.T) {
	root := t.TempDir()
	for _, prof := range []string{"dev", "release"} {
		t.Run(prof, func(t *testing.T) {
			p := htStartServerProc(t, root, htCancelMain, prof)
			addr := p.addr
			type held struct {
				name string
				c    net.Conn
			}
			var conns []held
			defer func() {
				for _, h := range conns {
					h.c.Close()
				}
			}()
			dial := func(name, send string) net.Conn {
				t.Helper()
				c, err := net.Dial("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				c.SetDeadline(time.Now().Add(60 * time.Second))
				if send != "" {
					if _, err := io.WriteString(c, send); err != nil {
						t.Fatal(err)
					}
				}
				conns = append(conns, held{name, c})
				return c
			}
			get := func(path string) string {
				t.Helper()
				c, err := net.Dial("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", path)
				res, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatalf("GET %s: %v", path, err)
				}
				b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
				res.Body.Close()
				return string(b)
			}
			// waitState polls /state until it reads want.
			waitState := func(want string) {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for {
					got := get("/state")
					if got == want+"\n" {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("state %q, want %q", got, want)
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			// Idle keep-alive connections, each after one reply.
			for i := range 8 {
				c := dial(fmt.Sprintf("idle %d", i), "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
				res, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil || res.StatusCode != 200 {
					t.Fatalf("idle connection: %v %v", res, err)
				}
				io.Copy(io.Discard, res.Body)
			}
			dial("no byte", "")
			dial("mid-head", "GET /hel")
			dial("large head", "GET /"+strings.Repeat("a", 10240))
			dial("mid body", "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nfirst half")
			dial("mid chunked", "POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab")
			dial("after 100 Continue", "POST /echo HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 1000\r\n\r\n")
			// Over the 65,536 limit and under DrainMax: drained before the 413.
			dial("mid drain", "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 200000\r\n\r\n"+strings.Repeat("d", 100000))
			dial("Send blocked", "GET /big HTTP/1.1\r\nHost: x\r\n\r\n")
			dial("Sleep holding the lock", "GET /wait HTTP/1.1\r\nHost: x\r\n\r\n")
			waitState("held 1 waiting 0 big 1")
			dial("waiting in Lock", "GET /wait HTTP/1.1\r\nHost: x\r\n\r\n")
			waitState("held 1 waiting 1 big 1")
			// Every connection above is accepted: the accept loop is one
			// task, so a later connection answered means all came before.
			if got := get("/hello"); got != "hi\n" {
				t.Fatalf("hello after the held connections: %q", got)
			}
			// Let Send of the 16 MiB reply fill the socket.
			time.Sleep(300 * time.Millisecond)
			start := time.Now()
			if got := get("/cancel"); got != "ok\n" {
				t.Fatalf("/cancel: %q", got)
			}
			select {
			case <-p.exited:
			case <-time.After(15 * time.Second):
				t.Fatalf("the program did not exit within 15 s of the cancel")
			}
			t.Logf("exited %v after the cancel", time.Since(start))
			if code := p.cmd.ProcessState.ExitCode(); code != 0 {
				t.Fatalf("exit %d: %s", code, p.stderr.String())
			}
			// Serve returns on cancellation.
			if out := p.stdout.String(); !strings.Contains(out, "serve ended: 3\n") {
				t.Fatalf("stdout %q, want Serve to return (3)", out)
			}
			// Every connection has ended: a read sees the end or a reset, not
			// the deadline; and none received a 500.
			for _, h := range conns {
				h.c.SetReadDeadline(time.Now().Add(10 * time.Second))
				var got strings.Builder
				var b [65536]byte
				for {
					n, err := h.c.Read(b[:])
					if got.Len() < 4096 {
						got.Write(b[:min(n, 4096-got.Len())])
					}
					if err == nil {
						continue
					}
					if ne, ok := err.(net.Error); ok && ne.Timeout() {
						t.Errorf("%s: still open after Serve returned", h.name)
					}
					break
				}
				if strings.Contains(got.String(), "HTTP/1.1 500") {
					t.Errorf("%s: a 500 reached the client after the cancel: %q", h.name, got.String()[:min(got.Len(), 200)])
				}
			}
		})
	}
}
