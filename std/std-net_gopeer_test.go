// Why: std.net must interoperate with a real, independent network stack.
// What: TestStdNet_GoPeer, Go's net as the peer on loopback, TCP and Unix.
// How: Mk dials and is dialled by Go; Go checks addresses, Shutdown(Write) and
// options, including an IPv4-mapped peer on [::] that Mk reports as IPv4.

package std

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// net3PeerProgram is the Mk side: its first argument picks the role.
//
//	dial ADDR        dial, print "local L peer P", send "ping", half-close,
//	                 read to the end, print "got TEXT"
//	listen           ListenWith no_delay on 127.0.0.1:0, print "port N",
//	                 accept one, print "peer P local L", read to the end,
//	                 answer "pong:" and what it read, close
//	listen6          as listen, on [::]:0 (dual-stack)
//	dialunix PATH    as dial, over a Unix socket, through a Conn
//	listenunix PATH  as listen, at PATH, printing "ready"
const net3PeerProgram = `import std.net
import std.time

export Main(sys: &System): net.Error|WriteError!void {
    args := sys.args
    mode := args[1]
    if mode == "dial" || mode == "dialunix" {
        try Dial(sys, mode, args[2])
        return
    }
    try Serve(sys, mode, if args.Len() > 2 { args[2] } else { "" })
}

// Say prints one line and flushes it, so the Go side reads it at once.
Say[A: Fields[Format] + Plain](sys: &System, const f: str, args: A): WriteError!void {
    var w := sys.Stdout()
    try Print(mut w, f, args)
    try w.Flush()
}

Dial(sys: &System, mode: str, addr: str): net.Error|WriteError!void {
    io := sys.io
    s := if mode == "dial" { try net.Dial(io, addr) } else { try net.DialUnix(io, addr) }
    if mode == "dial" {
        s.SetNoDelay(true) else e {
            s.Close()
            fail e
        }
        s.SetKeepAlive(some(time.Seconds(15))) else e {
            s.Close()
            fail e
        }
    }
    local := s.LocalAddr()
    c := s.Conn()
    onerr c.Abandon()
    if p := c.PeerAddr() {
        try Say(sys, "local {local} peer {p}\n", (local, p))
    } else {
        try Say(sys, "unix\n", ())
    }
    try c.WriteAll(io, "ping".AsBytes())
    try c.Shutdown(io, net.Shutdown.Write)
    var buf: [64]u8 = [0; 64]
    var n := 0
    for {
        k := try c.Read(io, mut buf[n..])
        if k == 0 {
            break
        }
        n += k
    }
    try sys.Print("got {t}\n", (t: str.From(buf[0..n]) else { "?" }))
    try c.Close(io)
}

Serve(sys: &System, mode: str, path: str): net.Error|WriteError!void {
    io := sys.io
    var o := net.DefaultListenOptions
    o.no_delay = true
    tcp := mode == "listen" || mode == "listen6"
    l := if mode == "listen" {
        try net.ListenWith(io, "127.0.0.1:0", o)
    } else if mode == "listen6" {
        try net.ListenWith(io, "[::]:0", o)
    } else {
        try net.ListenUnix(io, path)
    }
    defer l.Close()
    if tcp {
        try Say(sys, "port {p}\n", (p: l.Port()))
    } else {
        try Say(sys, "ready\n", ())
    }
    s := try l.Accept(io)
    peer := s.PeerAddr()
    local := s.LocalAddr()
    c := s.Conn()
    onerr c.Abandon()
    if tcp {
        try Say(sys, "peer {peer} local {local}\n", (peer, local))
    }
    var buf: [64]u8 = [0; 64]
    var n := 0
    for {
        k := try c.Read(io, mut buf[n..])
        if k == 0 {
            break
        }
        n += k
    }
    try c.WriteAll(io, "pong:".AsBytes())
    try c.WriteAll(io, buf[0..n])
    try c.Close(io)
}
`

// net3Start returns a line reader and an exit check. Cleanup reaps the child
// even when an assertion fails before the normal wait.
func net3Start(t *testing.T, exe, dir string, args ...string) (line func() string, wait func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	if dir == "" {
		cmd.Dir = filepath.Dir(exe)
	}
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var waited bool
	reap := func() error {
		waited = true
		defer cancel()
		return cmd.Wait()
	}
	t.Cleanup(func() {
		if !waited {
			cancel()
			_ = reap()
		}
	})
	r := bufio.NewReader(out)
	line = func() string {
		t.Helper()
		s, err := r.ReadString('\n')
		if err != nil {
			status := reap()
			address := ""
			if len(args) == 2 && strings.HasSuffix(args[0], "unix") {
				address = fmt.Sprintf(" (socket address %d bytes)", len(args[1]))
			}
			t.Fatalf("%s %q in %q%s: read %q: %v; exit: %v\n%s",
				filepath.Base(exe), args, cmd.Dir, address, s, err, status, errb.String())
		}
		return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
	}
	wait = func() {
		t.Helper()
		_, _ = io.Copy(io.Discard, r)
		if err := reap(); err != nil {
			t.Fatalf("%s %v: %v\n%s", filepath.Base(exe), args, err, errb.String())
		}
	}
	return line, wait
}

// net3Serve answers one connection on l as the Mk listener does; it
// reports what it read and the two addresses.
func net3Serve(t *testing.T, l net.Listener, got chan<- string) {
	c, err := l.Accept()
	if err != nil {
		got <- "accept: " + err.Error()
		return
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	b, err := io.ReadAll(c)
	if err != nil {
		got <- "read: " + err.Error()
		return
	}
	if _, err := c.Write(append([]byte("pong:"), b...)); err != nil {
		got <- "write: " + err.Error()
		return
	}
	// The Mk side half-closed first, so its socket, not this one, waits
	// in TIME_WAIT: one entry per run.
	got <- string(b) + "|" + c.RemoteAddr().String() + "|" + c.LocalAddr().String()
}

// net3Client dials addr over network as a Go client: it sends "ping",
// half-closes, and reads the answer.
func net3Client(t *testing.T, network, addr string) (string, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	switch cc := c.(type) {
	case *net.TCPConn:
		err = cc.CloseWrite()
	case *net.UnixConn:
		err = cc.CloseWrite()
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), c
}

func TestStdNet_GoPeer(t *testing.T) {
	exe := buildNative(t, nil, net3PeerProgram)

	t.Run("mk dials go", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		got := make(chan string, 1)
		go net3Serve(t, l, got)
		readLine, wait := net3Start(t, exe, "", "dial", l.Addr().String())
		line := readLine()
		res := <-got
		final := readLine()
		wait()
		parts := strings.Split(res, "|")
		if len(parts) != 3 || parts[0] != "ping" {
			t.Fatalf("the Go server saw %q", res)
		}
		// Mk's local is Go's remote, and Mk's peer is Go's listener.
		if want := "local " + parts[1] + " peer " + l.Addr().String(); line != want {
			t.Errorf("Mk said %q, Go says %q", line, want)
		}
		if final != "got pong:ping" {
			t.Errorf("Mk read %q after its half-close", final)
		}
	})

	t.Run("go dials mk", func(t *testing.T) {
		readLine, wait := net3Start(t, exe, "", "listen")
		port := strings.TrimPrefix(readLine(), "port ")
		reply, c := net3Client(t, "tcp", "127.0.0.1:"+port)
		line := readLine()
		local, remote := c.LocalAddr().String(), c.RemoteAddr().String()
		c.Close()
		wait()
		if reply != "pong:ping" {
			t.Errorf("Go read %q", reply)
		}
		if want := "peer " + local + " local " + remote; line != want {
			t.Errorf("Mk said %q, Go says %q", line, want)
		}
	})

	// Mk listens on [::]; Go dials it over tcp4 and over tcp6.
	// An IPv4 client of a dual-stack listener is an IPv4-mapped peer to the
	// OS, which Mk reports as IPv4, as Go's LocalAddr shows it.
	for _, c := range []struct{ name, network, host string }{
		{"go dials mk's [::] listener over tcp4", "tcp4", "127.0.0.1"},
		{"go dials mk's [::] listener over tcp6", "tcp6", "[::1]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.network == "tcp6" {
				probe, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skipf("no IPv6 loopback here: %v", err)
				}
				probe.Close()
			}
			readLine, wait := net3Start(t, exe, "", "listen6")
			port := strings.TrimPrefix(readLine(), "port ")
			reply, conn := net3Client(t, c.network, c.host+":"+port)
			line := readLine()
			local, remote := conn.LocalAddr().String(), conn.RemoteAddr().String()
			conn.Close()
			wait()
			if reply != "pong:ping" {
				t.Errorf("Go read %q", reply)
			}
			if want := "peer " + local + " local " + remote; line != want {
				t.Errorf("Mk said %q, Go says %q", line, want)
			}
			if c.network == "tcp4" && (strings.Contains(line, "ffff") || !strings.HasPrefix(local, "127.0.0.1:")) {
				t.Errorf("an IPv4 peer of a [::] listener: Mk said %q, Go's local is %q", line, local)
			}
		})
	}

	// Each direction shares a private current directory with the child.
	// Windows counts the bytes of the relative address, even under long TEMP.
	const path = "sock"
	t.Run("mk dials go over unix", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		l, err := net.Listen("unix", path)
		if errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.Errno(10047)) {
			t.Skipf("no Unix sockets here: %v", err)
		}
		if err != nil {
			t.Fatalf("listen unix %q in %q: %v", path, dir, err)
		}
		defer l.Close()
		got := make(chan string, 1)
		go net3Serve(t, l, got)
		readLine, wait := net3Start(t, exe, dir, "dialunix", path)
		if line := readLine(); line != "unix" {
			t.Fatalf("Mk said %q", line)
		}
		res := <-got
		final := readLine()
		wait()
		if !strings.HasPrefix(res, "ping|") || final != "got pong:ping" {
			t.Errorf("Go saw %q, Mk read %q", res, final)
		}
	})

	t.Run("go dials mk over unix", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		readLine, wait := net3Start(t, exe, dir, "listenunix", path)
		if line := readLine(); line != "ready" {
			t.Fatalf("Mk said %q", line)
		}
		reply, c := net3Client(t, "unix", path)
		c.Close()
		wait()
		if reply != "pong:ping" {
			t.Errorf("Go read %q", reply)
		}
	})
}
