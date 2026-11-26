// Why: connectors (tls.Accept, tls.Connect, websocket's CutOver) fill a
// ConnSlot with a transport over another Conn and a borrowed &Mutex, and
// must check with no `h into conn` or `count into inner` clause.
// What: TestConnectorClauses, the checker's verdict on each shape.
// How: ConnSlot.Fill takes t with no `into` clause, so the call stores
// nothing the caller keeps and the checker's inferred call stores
// (storeArg in internal/borrow/call.go) never run there. The holder case
// forwards both loans with declared flows. A separate write-through case
// requires a clause in the callee that makes the store.

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

func TestConnectorClauses(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // diagnostics, each "line:col: Ecode"; none means clean
	}{
		{"CutOver fills its slot with no `count into inner`", p1Cut + `
CutOver(h: &Heap, inner: net.Conn, count: &Mutex[int]): OutOfMemory!net.Conn from h, inner, count {
    slot := try net.ReserveConn[Cut](h)
    return slot.Fill(Cut(inner: inner, count: count))
}
`, nil},
		{"a TLS-shaped connector reserves, fails and fills with no `h into conn`", p1Cut + `
Accept(h: &Heap, count: &Mutex[int], io: Io, conn: net.Conn, refuse: bool): net.Error|OutOfMemory!net.Conn from h, conn, count {
    slot := try net.ReserveConn[Cut](h)
    if refuse {
        slot.Free(h.Alloc())
        fail net.SecureFailed(why: net.SecureFailure.Handshake)
    }
    return slot.Fill(Cut(inner: conn, count: count))
}
`, nil},
		{"a callee stores the complete transport with both loans declared", p1Cut + `
linear struct Keep[T: net.Transport] {
    t: ?T
}

Put[T: net.Transport](k: mut Keep[T], t: T into k) {
    if old := Replace(mut k.t, some(t)) {
        old.Abandon()
    }
}

Over(k: mut Keep[Cut], inner: net.Conn into k, count: &Mutex[int] into k) {
    Put(k, Cut(inner: inner, count: count))
}
`, nil},
		{"a callee writes one held loan through another without a clause", `struct Deep {
    a: &i32
    b: mut &i32
}

Deep.Mix(mut self) {
    *self.b = self.a
}
`, []string{"7:5: E0604"}},
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
			if status == 0 || strings.Join(got, ",") != strings.Join(c.want, ",") || !strings.Contains(out, "write `*self into self`") {
				t.Fatalf("got exit %d and %v, want %v with `*self into self`:\n%s", status, got, c.want, out)
			}
		})
	}
}
