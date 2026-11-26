// Why: connectors (tls.Accept, tls.Connect, websocket's CutOver) fill a
// ConnSlot with a transport over another Conn and a borrowed &Mutex, and
// must check with no `h into conn` or `count into inner` clause.
// What: TestConnectorClauses, the checker's verdict on each shape.
// How: ConnSlot.Fill takes t with no `into` clause, so the call stores
// nothing the caller keeps and the checker's inferred call stores
// (storeArg in internal/borrow/call.go) never run there. A callee that
// takes t `into` a holder runs them, and then what t's own loans can write
// takes t's loans too. A net.Conn is a read-only view, so count reaches no
// part of inner and the holder case is clean; its twin, whose inner is a
// &Cell holding a loan, makes count reach inner (E0604). The last case is
// a write of one held loan through another, which needs `*self into self`.

package std

import (
	"strings"
	"testing"
)

// p1Cut is websocket's test transport Cut (std/websocket/tests_sweep.mk):
// an inner Conn and a counter it borrows.
const p1Cut = `import std.net

linear view struct Cut {
    inner: net.Conn
    count: &Mutex[int]
}

impl net.Transport for Cut {
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
        Cut(inner, count: _) := self
        inner.Abandon()
    }
}
`

// keepPut is a holder of one transport and a generic callee that stores a
// transport into it (`t into k`).
const keepPut = `
linear struct Keep[T: net.Transport] {
    t: ?T
}

Put[T: net.Transport](k: mut Keep[T], t: T into k) {
    if old := Replace(mut k.t, some(t)) {
        old.Abandon()
    }
}
`

// cellCut is p1Cut with inner a &Cell holding a loan, which takes a store
// through a shared reference, in place of the read-only Conn.
const cellCut = `import std.net

linear view struct Cut {
    inner: &Cell[?&Mutex[int]]
    count: &Mutex[int]
}

impl net.Transport for Cut {
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
        Cut(inner: _, count: _) := self
    }
}
`

func TestConnectorClauses(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // diagnostics, each "line:col: Ecode"; none means clean
		text string   // what the report of a rejected case must say
	}{
		{"CutOver fills its slot with no `count into inner`", p1Cut + `
CutOver(h: &Heap, inner: net.Conn, count: &Mutex[int]): OutOfMemory!net.Conn from h, inner, count {
    slot := try net.ReserveConn[Cut](h)
    return slot.Fill(Cut(inner: inner, count: count))
}
`, nil, ""},
		{"a TLS-shaped connector reserves, fails and fills with no `h into conn`", p1Cut + `
Accept(h: &Heap, count: &Mutex[int], io: Io, conn: net.Conn, refuse: bool): net.Error|OutOfMemory!net.Conn from h, conn, count {
    slot := try net.ReserveConn[Cut](h)
    if refuse {
        slot.Free(h.Alloc())
        fail net.SecureFailed(why: net.SecureFailure.Handshake)
    }
    return slot.Fill(Cut(inner: conn, count: count))
}
`, nil, ""},
		{"a callee takes a transport over a read-only Conn into a holder", p1Cut + keepPut + `
Over(k: mut Keep[Cut], inner: net.Conn into k, count: &Mutex[int] into k) {
    Put(k, Cut(inner: inner, count: count))
}
`, nil, ""},
		{"a transport whose inner takes stores makes count reach inner", cellCut + keepPut + `
Over(k: mut Keep[Cut], inner: &Cell[?&Mutex[int]] into k, count: &Mutex[int] into k) {
    Put(k, Cut(inner: inner, count: count))
}
`, []string{"34:5: E0604"}, "a loan of count into inner"},
		{"a callee writes one held loan through another without a clause", `struct Deep {
    a: &i32
    b: mut &i32
}

Deep.Mix(mut self) {
    *self.b = self.a
}
`, []string{"7:5: E0604"}, "write `*self into self`"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, status := net3Check(t, c.src)
			var got []string
			for _, l := range strings.Split(out, "\n") {
				if i := strings.Index(l, "main.mk:"); i >= 0 {
					rest := l[i+len("main.mk:"):]
					if f := strings.Fields(rest); len(f) >= 2 {
						got = append(got, f[0]+" "+f[1])
					}
				}
			}
			if len(c.want) == 0 {
				if status != 0 || len(got) != 0 {
					t.Fatalf("want clean, got exit %d:\n%s", status, out)
				}
				return
			}
			if status == 0 || strings.Join(got, ",") != strings.Join(c.want, ",") || !strings.Contains(out, c.text) {
				t.Fatalf("got exit %d and %v, want %v with %q:\n%s", status, got, c.want, c.text, out)
			}
		})
	}
}
