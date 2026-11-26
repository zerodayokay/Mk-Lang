// Why: std.net's connection surface makes promises the checker must enforce
// on user programs, not only on std's own code.
// What: TestStdNet_Check, a table of programs and the verdicts expected.
// How: each program is checked: NewConn's transport ownership, Secure's
// construction and copying, and E0306 for a match missing net.Error members.

package std

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// net3Check runs mk check on src as main.mk against a copy of std.
func net3Check(t *testing.T, src string) (string, int) {
	t.Helper()
	exe := mkExe(t)
	root := t.TempDir()
	stdDir := copyStd(t, root, nil)
	prog := filepath.Join(root, "main.mk")
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "check", prog)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MK_STD="+stdDir, "MK_CACHE="+filepath.Join(root, "cache"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(out.String(), "\r\n", "\n"), status
}

const net3Mem = `import std.net

linear struct Mem {
    n: Mutex[int]
}

impl net.Transport for Mem {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return 0
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {}
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {}
    PeerAddr(&self): ?net.Addr {
        return none
    }
    Close(&self, io: Io): net.Error!void {}
    Abandon(self) {
        Mem(n) := self
        _ = n.Into()
    }
}
`

// net3Wrap is a transport over another Conn, as a TLS connection is.
const net3Wrap = `import std.net

linear view struct Wrap {
    inner: net.Conn
}

impl net.Transport for Wrap {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return try self.inner.Read(io, buf)
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {}
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {}
    PeerAddr(&self): ?net.Addr {
        return none
    }
    Close(&self, io: Io): net.Error!void {}
    Abandon(self) {
        Wrap(inner) := self
        inner.Abandon()
    }
}
`

// net3Lent is a transport that borrows a Mutex, so a Conn over it holds
// the loan.
const net3Lent = `import std.net

view struct Lent {
    n: &Mutex[int]
}

impl net.Transport for Lent {
    Read(&self, io: Io, buf: mut []u8): net.Error!int {
        return 0
    }
    WriteAll(&self, io: Io, data: []u8): net.Error!void {}
    Shutdown(&self, io: Io, how: net.Shutdown): net.Error!void {}
    PeerAddr(&self): ?net.Addr {
        return none
    }
    Close(&self, io: Io): net.Error!void {}
    Abandon(self) {}
}
`

func TestStdNet_Check(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // diagnostics, each "line:col: Ecode"; none means clean
	}{
		{"NewConn leaves the transport with its caller on OutOfMemory", net3Mem + `
Make(h: &Heap): OutOfMemory!net.Conn {
    t := Mem(n: Mutex[int].New(0))
    c := net.NewConn(h, t) else e {
        t.Abandon()
        fail e
    }
    return c
}
`, nil},
		{"NewConn takes the transport on success", net3Mem + `
Make(h: &Heap): OutOfMemory!net.Conn {
    t := Mem(n: Mutex[int].New(0))
    c := net.NewConn(h, t) else e {
        t.Abandon()
        fail e
    }
    t.Abandon()
    return c
}
`, []string{"29:5: E0401"}},
		{"a connector that wraps conn through NewConn can fail after the move (E0411)", net3Wrap + `
Connect(h: &Heap, io: Io, conn: net.Conn): net.Error|OutOfMemory!net.Conn from h, conn {
    w := Wrap(inner: conn)
    c := net.NewConn(h, w) else e {
        w.Abandon()
        fail e
    }
    return c
}
`, []string{"24:5: E0411"}},
		{"a connector that reserves first and fills last leaves conn with its caller", net3Wrap + `
Connect(h: &Heap, io: Io, conn: net.Conn, refuse: bool): net.Error|OutOfMemory!net.Conn from h, conn {
    slot := try net.ReserveConn[Wrap](h)
    if refuse {
        slot.Free(h.Alloc())
        fail net.SecureFailed(why: net.SecureFailure.Handshake)
    }
    return slot.Fill(Wrap(inner: conn))
}

Caller(sys: &System, conn: net.Conn) {
    c := Connect(sys.heap, sys.io, conn, false) else {
        conn.Abandon()
        return
    }
    c.Abandon()
}
`, nil},
		{"a slot reserved on sys.heap takes a transport that borrows a local", net3Lent + `
export Main(sys: &System) {
    m := Mutex[int].New(7)
    slot := net.ReserveConn[Lent](sys.heap) else {
        _ = m.Into()
        return
    }
    c := slot.Fill(Lent(n: &m))
    c.Abandon()
    _ = m.Into()
}
`, nil},
		{"a Conn over a transport that borrows a local cannot outlive it", net3Lent + `
Make(h: &Heap): ?net.Conn from h {
    m := Mutex[int].New(7)
    slot := net.ReserveConn[Lent](h) else {
        _ = m.Into()
        return none
    }
    c := slot.Fill(Lent(n: &m))
    _ = m.Into()
    return some(c)
}

export Main(sys: &System) {
    if c := Make(sys.heap) {
        c.Abandon()
    }
}
`, []string{"27:9: E0503", "28:5: E0603"}},
		// A Conn whose transport borrows a local reaches no place that
		// outlives the local, now that ConnSlot.Fill returns a Conn from
		// self and t instead of moving into self.
		{"a Conn over a borrowed transport cannot be kept in a global", net3Lent + `
var G: Mutex[?net.Conn] = Mutex[?net.Conn].New(none)

// c1: a Conn over a transport that borrows a local, kept in a global.
export Main(sys: &System) {
    m := Mutex[int].New(7)
    slot := net.ReserveConn[Lent](sys.heap) else {
        _ = m.Into()
        return
    }
    c := slot.Fill(Lent(n: &m))
    var g := G.Lock(sys.io) else {
        c.Abandon()
        _ = m.Into()
        return
    }
    old := Replace(g.Mut(), some(c))
    g.Unlock()
    if o := old {
        o.Abandon()
    }
    _ = m.Into()
}
`, []string{"35:12: E0505", "35:12: E0605"}},
		{"Fill's result holds t's loan: a result from h only is E0602", net3Lent + `
// c2: the result is declared from h only, but holds t's loan.
Make(h: &Heap, t: Lent): OutOfMemory!net.Conn from h {
    slot := try net.ReserveConn[Lent](h)
    return slot.Fill(t)
}

export Main(sys: &System) {
    m := Mutex[int].New(7)
    c := Make(sys.heap, Lent(n: &m)) else {
        _ = m.Into()
        return
    }
    c.Abandon()
    _ = m.Into()
}
`, []string{"23:5: E0602"}},
		{"Fill's result holds t's loan in a generic too", net3Lent + `
// c3: c2 generic in the transport.
Make[T: net.Transport + Send + Share](h: &Heap, t: T): ?net.Conn from h {
    slot := net.ReserveConn[T](h) else {
        t.Abandon()
        return none
    }
    return some(slot.Fill(t))
}

export Main(sys: &System) {
    m := Mutex[int].New(7)
    if c := Make[Lent](sys.heap, Lent(n: &m)) {
        _ = m.Into()
        c.Abandon()
        return
    }
    _ = m.Into()
}
`, []string{"26:5: E0602"}},
		{"a Conn over a borrowed transport cannot go into a list on the heap", net3Lent + `
// c4: the Conn pushed into a list on the process heap that outlives m.
Push(l: mut HeapList[net.Conn], h: &Heap into l) {
    m := Mutex[int].New(7)
    slot := net.ReserveConn[Lent](h) else {
        _ = m.Into()
        return
    }
    c := slot.Fill(Lent(n: &m))
    l.Push(h.Alloc(), c) else e {
        _ = e
        c.Abandon()
    }
    _ = m.Into()
}

export Main(sys: &System) {
    var l := HeapList[net.Conn].New(sys.heap.Alloc())
    Push(mut l, sys.heap)
    for {
        c := l.Pop() else {
            break
        }
        c.Abandon()
    }
    l.FreeEmpty(sys.heap.Alloc())
}
`, []string{"28:5: E0505"}},
		{"a program cannot build a Secure", `import std.net

Forge(s: net.Secure): net.Secure {
    return net.Secure(call: s.call, thunk: Hand)
}

Hand(s: &net.Secure, h: &Heap, io: Io, conn: net.Conn, host: str, offer: []str): net.Error!(conn: net.Conn, chosen: int) from h, conn {
    return (conn: conn, chosen: -1)
}
`, []string{"4:12: E0204"}},
		{"a Secure is plain: a record holding one copies", `import std.net

struct Limits {
    secure: ?net.Secure
}

Twice(l: Limits): (a: Limits, b: Limits) {
    return (a: l, b: l)
}
`, nil},
		{"a match naming only 1.1's members of net.Error is E0306", `import std.net

Kind(e: net.Error): int {
    return match e {
        net.Refused { 1 }
        net.Reset { 2 }
        net.AddrInUse { 3 }
        net.Unreachable { 4 }
        net.InvalidAddress { 5 }
        net.Os(code: _) { 6 }
        Canceled { 7 }
        Closed { 8 }
    }
}
`, []string{"4:12: E0306"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, status := net3Check(t, c.src)
			if strings.Contains(out, "no usable LLVM") {
				t.Skipf("needs the LLVM toolchain: %s", out)
			}
			if len(c.want) == 0 {
				if status != 0 {
					t.Fatalf("want clean, status %d:\n%s", status, out)
				}
				return
			}
			if status == 0 {
				t.Fatalf("want %v, checked clean", c.want)
			}
			for _, w := range c.want {
				if !strings.Contains(out, "main.mk:"+w) {
					t.Errorf("missing main.mk:%s", w)
				}
			}
			if n := strings.Count(out, ": E"); n != len(c.want) {
				t.Errorf("%d diagnostics, want %d", n, len(c.want))
			}
			if t.Failed() {
				t.Logf("output:\n%s", out)
			}
		})
	}
}
