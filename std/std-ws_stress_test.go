// Why: the write lock, pong hand-off and frame order of std.websocket must
// hold under real concurrency on OS threads, not only under std.sim.
// What: TestStdWs_ServeStress.
// How: eight Mk senders and a pinging Go client on one connection; the
// client checks every frame and a lost pong stalls it into a failure.

// The end-to-end and native stress test of std.websocket: a Mk server
// built with http.ServeWith whose
// handler takes its connection with Link.Takeover and accepts the
// WebSocket handshake on it with websocket.Accept, and a Go client written
// from RFC 6455 against it, over loopback, on the OS's own carriers (1, 4
// and 16 in dev, 16 in release).
//
// On the one server Conn, eight tasks send 150 binary messages each (one
// in fifty of 20,003 bytes, over the write chunk) while one task receives
// the client's messages. The client pings 300 times, each ping waiting for
// its pong before the next, with a masked binary message after each ping.
// So the server's Recv owes a pong while the senders hold the write lock,
// and the pong's hand-off (the next writer sends it, or Recv after the
// holder's release) runs on real carriers: a lost pong stalls the client,
// which fails after 20 s. The client checks every frame: unmasked, one
// frame per message (frames never interleave), each sender's messages
// in order and each byte of its pattern. The server checks each of the
// client's messages, then closes with 1000 and "recv 300 bad 0", and its
// process exits 0. Each Go connection closes with linger 0.

package std

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// wsStressProgram is the Mk server: args port, senders, messages each.
const wsStressProgram = `
import std.http
import std.proc
import std.websocket

// App is the handler's context: the heap the connection's state goes on,
// and the senders' count and their messages each.
struct App {
    heap: &Heap
    senders: int
    msgs: int
}

export Main(sys: &System): http.Error|OutOfMemory|ParseError!void {
    app := App(heap: sys.heap, senders: Cast[int](try sys.args[2].ParseInt()), msgs: Cast[int](try sys.args[3].ParseInt()))
    addr := try sys.arena.Fmt("127.0.0.1:{p}", (p: sys.args[1]))
    try http.ServeWith(sys, addr, &http.DefaultServerOptions, &app, Handle)
}

AllowAll(origin: str): bool {
    return true
}

// Handle takes the connection, accepts the WebSocket handshake on it, runs
// the session, closes with 1000 and "recv N" (1011 and the failure when
// one happened), and ends the process: 0 when the session was clean.
Handle(c: &App, a: &Arena, io: Io, req: &http.Request, res: mut http.Response, link: &http.Link): http.Error!void {
    t := link.Takeover() else {
        fail http.Status(code: 500)
    }
    ws := websocket.Accept(c.heap, io, req, t, websocket.DefaultOptions.protocols, AllowAll) else {
        t.Abandon()
        proc.Exit(3)
    }
    bad := Mutex[int].New(0)
    var got := 0
    scope s := io.Scope() {
        for k in 0..c.senders {
            s.go Sender(&ws, c.heap, io, k, c.msgs, &bad)
        }
        got = Receiver(&ws, c.heap, io)
    }
    g := bad.Lock(io.NoCancel()) else {
        ws.Abandon()
        proc.Exit(4)
    }
    failures := *g.Ref()
    g.Unlock()
    ok := failures == 0 && got >= 0
    reason := a.Fmt("recv {got} bad {failures}", (got, failures)) else { "oom" }
    ws.Close(io, if ok { 1000 } else { 1011 }, reason) else {
        ws.Abandon()
        proc.Exit(5)
    }
    proc.Exit(if ok { 0 } else { 6 })
}

// Fill writes message seq of sender k into b: k, seq's two bytes, then a
// pattern of both, and returns its length.
Fill(b: mut []u8, k: int, seq: int): int {
    n := if seq % 50 == 49 { 20003 } else { 3 + (seq * 7919 + k * 104729) % 3000 }
    b[0] = Trunc[u8](k)
    b[1] = Trunc[u8](seq)
    b[2] = Trunc[u8](seq >> 8)
    for j in 3..n {
        b[j] = Trunc[u8](k * 31 + seq * 7 + j)
    }
    return n
}

// Sender sends n binary messages of Fill's shape on ws; a failure counts
// in bad and ends it.
Sender(ws: &websocket.Conn, h: &Heap, io: Io, k: int, n: int, bad: &Mutex[int]) {
    var a := Arena.New(h) else {
        Bad(bad, io)
        return
    }
    buf := a.Slice[u8](20003) else {
        a.Free()
        Bad(bad, io)
        return
    }
    for seq in 0..n {
        m := Fill(mut buf[..], k, seq)
        ws.Send(io, websocket.Kind.Binary, buf[0..m]) else {
            Bad(bad, io)
            a.Free()
            return
        }
    }
    a.Free()
}

Bad(bad: &Mutex[int], io: Io) {
    var g := bad.Lock(io.NoCancel()) else {
        return
    }
    *g.Mut() += 1
    g.Unlock()
}

// Receiver receives the peer's binary messages, message i being i's two
// bytes then (i + j) & 0xFF at j, until a text "done": their count, or -1
// when one was wrong or Recv failed.
Receiver(ws: &websocket.Conn, h: &Heap, io: Io): int {
    var a := Arena.New(h) else {
        return -1
    }
    var n := 0
    for {
        m := ws.Recv(&a, io) else {
            a.Free()
            return -1
        }
        if m.kind == websocket.Kind.Text {
            text := str.From(m.data) else { "" }
            done := text == "done"
            a.Free()
            return if done { n } else { -1 }
        }
        d := m.data
        if d.Len() < 2 || int(d[0]) | int(d[1]) << 8 != n & 0xFFFF {
            a.Free()
            return -1
        }
        for j in 2..d.Len() {
            if d[j] != Trunc[u8](n + j) {
                a.Free()
                return -1
            }
        }
        n += 1
        a.Reset()
    }
}
`

// wsStressFill is the Mk sender's message seq of sender k (Fill).
func wsStressFill(k, seq int) []byte {
	n := 3 + (seq*7919+k*104729)%3000
	if seq%50 == 49 {
		n = 20003
	}
	b := make([]byte, n)
	b[0], b[1], b[2] = byte(k), byte(seq), byte(seq>>8)
	for j := 3; j < n; j++ {
		b[j] = byte(k*31 + seq*7 + j)
	}
	return b
}

// wsStressRun runs exe once with senders and msgs, as the Go client, and
// fails t unless every check holds.
func wsStressRun(t *testing.T, exe string, senders, msgs, pings int) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	_, wait := net3Start(t, exe, port, fmt.Sprint(senders), fmt.Sprint(msgs))
	var c net.Conn
	for start := time.Now(); ; {
		c, err = net.DialTimeout("tcp", "127.0.0.1:"+port, 5*time.Second)
		if err == nil {
			break
		}
		if time.Since(start) > 20*time.Second {
			t.Fatalf("the Mk server never listened: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = c.SetDeadline(time.Now().Add(90 * time.Second))
	w := &wsConn{c: c, r: bufio.NewReader(c), masked: true}
	defer w.close()
	key := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210"))
	fmt.Fprintf(c, "GET /chat HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	resp, err := http.ReadResponse(w.r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != wsAccept(key) {
		t.Fatalf("the Mk server's reply: %d %v", resp.StatusCode, resp.Header)
	}

	var wmu sync.Mutex
	write := func(f wsFrame) error {
		wmu.Lock()
		defer wmu.Unlock()
		f.masked = true
		return wsWrite(c, f)
	}
	pongs := make(chan string, pings+1)
	type end struct {
		code   uint16
		reason string
		data   int
		err    error
	}
	done := make(chan end, 1)
	go func() {
		next := make([]int, senders)
		data := 0
		for {
			f, err := wsRead(w.r)
			if err != nil {
				done <- end{data: data, err: err}
				return
			}
			if f.masked || f.rsv != 0 {
				done <- end{data: data, err: fmt.Errorf("a frame from the server masked or with a reserved bit: %+v", f.op)}
				return
			}
			switch f.op {
			case 10:
				pongs <- string(f.data)
			case 9:
				done <- end{data: data, err: fmt.Errorf("the server pinged")}
				return
			case 8:
				e := end{data: data}
				if len(f.data) >= 2 {
					e.code = binary.BigEndian.Uint16(f.data)
					e.reason = string(f.data[2:])
				}
				e.err = write(wsFrame{fin: true, op: 8, data: f.data[:min(2, len(f.data))]})
				done <- e
				return
			case 2:
				if !f.fin || len(f.data) < 3 || int(f.data[0]) >= senders {
					done <- end{data: data, err: fmt.Errorf("a data frame that is not one whole message of a sender: fin %v, %d bytes", f.fin, len(f.data))}
					return
				}
				k := int(f.data[0])
				seq := int(f.data[1]) | int(f.data[2])<<8
				if seq != next[k]&0xFFFF || string(f.data) != string(wsStressFill(k, next[k])) {
					done <- end{data: data, err: fmt.Errorf("sender %d: message %d arrived where %d was due, or its bytes differ", k, seq, next[k])}
					return
				}
				next[k]++
				data++
			default:
				done <- end{data: data, err: fmt.Errorf("a frame of op %d", f.op)}
				return
			}
		}
	}()

	for i := 0; i < pings; i++ {
		p := fmt.Sprintf("p%d", i)
		if err := write(wsFrame{fin: true, op: 9, data: []byte(p)}); err != nil {
			t.Fatal(err)
		}
		m := make([]byte, 2+i%700)
		m[0], m[1] = byte(i), byte(i>>8)
		for j := 2; j < len(m); j++ {
			m[j] = byte(i + j)
		}
		if err := write(wsFrame{fin: true, op: 2, data: m}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-pongs:
			if got != p {
				t.Fatalf("ping %q answered with %q", p, got)
			}
		case e := <-done:
			t.Fatalf("the connection ended before ping %d's pong: %+v", i, e)
		case <-time.After(20 * time.Second):
			t.Fatalf("ping %q was never answered (a lost pong, W4)", p)
		}
	}
	if err := write(wsFrame{fin: true, op: 1, data: []byte("done")}); err != nil {
		t.Fatal(err)
	}
	e := <-done
	if e.err != nil {
		t.Fatalf("after %d data messages: %v", e.data, e.err)
	}
	if e.data != senders*msgs || e.code != 1000 || e.reason != fmt.Sprintf("recv %d bad 0", pings) {
		t.Fatalf("%d data messages of %d, close %d %q", e.data, senders*msgs, e.code, e.reason)
	}
	w.close()
	wait()
}

func TestStdWs_ServeStress(t *testing.T) {
	for _, mode := range []struct {
		flags    []string
		carriers []string
	}{{nil, []string{"1", "4", "16"}}, {[]string{"--release"}, []string{"16"}}} {
		exe := buildNative(t, nil, wsStressProgram, mode.flags...)
		for _, n := range mode.carriers {
			t.Run(fmt.Sprintf("carriers=%s%s", n, strings.Join(mode.flags, "")), func(t *testing.T) {
				t.Setenv("MK_CARRIERS", n)
				for round := 0; round < 3; round++ {
					wsStressRun(t, exe, 8, 150, 300)
				}
			})
		}
	}
}
