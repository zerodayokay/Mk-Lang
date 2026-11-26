// Why: FormatPem writes to a caller's Writer, so a cancel or a failed write must leave a prefix of its output.
// What: encPemCancel, Mk test blocks that cancel or fail FormatPem at each of its writes.
// How: written into a std copy as std/encoding/tests_pemcancel.mk and run by `mk test --std=std.encoding`
// (dev at 1 and 16 carriers, release at 16); TestStdEncoding_PemCancelIsLoadBearing removes the cancel and expects failures.
// Its tests are cancel-safety tests; tools/gate/safety.go lists each.

package std

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// encPemCancelRun runs mk test --std=std.encoding with blocks as
// tests_pemcancel.mk and returns the output and exit status.
func encPemCancelRun(t *testing.T, blocks string, carriers int, flags ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := encStdDir(t, root)
	if err := os.WriteFile(filepath.Join(stdDir, "encoding", "tests_pemcancel.mk"), []byte(blocks), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	t.Setenv("MK_CARRIERS", fmt.Sprint(carriers))
	out, status := mkCmd(t, root, stdDir, append([]string{"test", "--std=std.encoding"}, flags...)...)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	return out, status
}

// encPemCancelNames are the test blocks of encPemCancel.
func encPemCancelNames(t *testing.T) []string {
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^test "([^"]+)"`).FindAllStringSubmatch(encPemCancel, -1) {
		names = append(names, m[1])
	}
	if len(names) != 2 {
		t.Fatalf("%d test blocks in encPemCancel, want 2", len(names))
	}
	return names
}

// TestStdEncoding_PemCancel: FormatPem is prefix under a cancel at each
// of its writes (natively) and a failed write at each (std.sim).
func TestStdEncoding_PemCancel(t *testing.T) {
	names := encPemCancelNames(t)
	for _, c := range []struct {
		carriers int
		flags    []string
	}{{1, nil}, {16, nil}, {16, []string{"--release"}}} {
		t.Run(fmt.Sprintf("carriers=%d%s", c.carriers, strings.Join(c.flags, "")), func(t *testing.T) {
			out, status := encPemCancelRun(t, encPemCancel, c.carriers, c.flags...)
			for _, n := range names {
				if !strings.Contains(out, fmt.Sprintf("PASS std.encoding %q", n)) {
					t.Errorf("%q did not pass (status %d)", n, status)
				}
			}
			if t.Failed() || status != 0 {
				t.Logf("mk test output (status %d):\n%s", status, out)
			}
		})
	}
}

// TestStdEncoding_PemCancelIsLoadBearing: with the cancel removed (the
// native block) and the fault moved to another kind of operation (the
// std.sim block), every write succeeds, and both blocks must fail: they
// test the interruption, not a run that never writes.
func TestStdEncoding_PemCancelIsLoadBearing(t *testing.T) {
	names := encPemCancelNames(t)
	mutant := strings.Replace(encPemCancel, "        sc.Cancel()\n", "        _ = sc\n", 1)
	mutant = strings.Replace(mutant, "s.sm.FailNext(sim.Op.Write,", "s.sm.FailNext(sim.Op.Sync,", 1)
	if strings.Contains(mutant, "sc.Cancel()") || strings.Contains(mutant, "sim.Op.Write") {
		t.Fatal("the mutant still cancels or fails a write")
	}
	out, status := encPemCancelRun(t, mutant, 1)
	for _, n := range names {
		if !strings.Contains(out, fmt.Sprintf("FAIL std.encoding %q", n)) {
			t.Errorf("%q did not fail without its interruption (status %d)", n, status)
		}
	}
	if t.Failed() {
		t.Logf("mk test output (status %d):\n%s", status, out)
	}
}

// encPemCancel is the Mk test file.
const encPemCancel = `// FormatPem's cancel-safety class, reference section 18's table: FormatPem
// writes its block through a Writer in pieces, so an interrupted write
// leaves an unknown prefix of the block written (the Prefix class), and
// FormatPem fails with WriteError at once, writing nothing after it. The
// Writer here is a port over a loopback stream whose write k is the one
// interrupted: natively, that write runs in a cancelled scope (every
// cancellable entry checks the flag first); under std.sim, by a fault
// (Sim.FailNext) just before it, since a std.sim write never waits and
// does not read the cancel flag. Each run interrupts write k for every k
// from 1 to one past the last, with an unbuffered Writer and a 7-byte one:
// the stream then carries a proper prefix of the uninterrupted block, and
// the whole block when no write is interrupted.

import std.core
import std.net
import std.sim

const Label: str = "CERTIFICATE"

// Der is the block's content, 100 bytes: three lines of base64.
Der(a: &Arena): OutOfMemory![]u8 from a {
    var d := try a.Slice[u8](100)
    for i in 0..100 {
        d[i] = Trunc[u8](i * 37 + 11)
    }
    return d[..]
}

// Whole is FormatPem's uninterrupted text.
Whole(a: &Arena, der: []u8): OutOfMemory!str from a {
    var t := Text.New(a)
    var w := t.Writer()
    FormatPem(mut w, Label, der) else {
        Assert(false)
    }
    return try a.Dup(t.Str())
}

// Sink is the port's state: the stream, the writes so far, the write to
// interrupt (from 1; 0 for none) and the writes interrupted.
view struct Sink {
    c: &net.Stream
    n: int
    k: int
    hits: int
}

const Port: core.WritePortMut[Sink] = core.WritePortMut[Sink].New(SinkWrite, SinkFlush)

// SinkWrite sends b; the k-th call sends it in a cancelled scope.
SinkWrite(s: mut Sink, io: Io, b: []u8): WriteError!void {
    s.n += 1
    if s.n != s.k {
        s.c.WriteAll(io, b) else {
            fail WriteError
        }
        return
    }
    s.hits += 1
    var ok := true
    scope sc := io.Scope() {
        sc.Cancel()
        s.c.WriteAll(io, b) else {
            ok = false
        }
    }
    if !ok {
        fail WriteError
    }
}

SinkFlush(s: mut Sink, io: Io): WriteError!void {
}

// SimSink is Sink under std.sim: the k-th call fails by a fault.
view struct SimSink {
    sm: mut sim.Sim
    c: &net.Stream
    n: int
    k: int
    hits: int
}

const SimPort: core.WritePortMut[SimSink] = core.WritePortMut[SimSink].New(SimSinkWrite, SimSinkFlush)

SimSinkWrite(s: mut SimSink, io: Io, b: []u8): WriteError!void {
    s.n += 1
    if s.n == s.k {
        s.hits += 1
        s.sm.FailNext(sim.Op.Write, sim.Fault.NoSpace)
    }
    s.c.WriteAll(io, b) else {
        fail WriteError
    }
}

SimSinkFlush(s: mut SimSink, io: Io): WriteError!void {
}

// Outcome is one run: what the stream carried, the calls that failed
// (FormatPem, or the Flush after it), the writes made and the writes
// interrupted.
type Outcome = (got: str, fails: int, writes: int, hits: int)

// ReadRest reads s to its end into t.
ReadRest(s: &net.Stream, io: Io, t: mut Text): net.Error|OutOfMemory!void {
    var d: [16]u8 = [0; 16]
    for {
        n := try s.Read(io, mut d[..])
        if n == 0 {
            return
        }
        try t.Push(str.From(d[0..n]).Must())
    }
}

// Addr formats the loopback address of port into buf.
Addr(buf: mut []u8, port: int): str from buf {
    var c := core.Capped.New(buf)
    var w := c.Writer()
    Print(mut w, "127.0.0.1:{port}", (port)) else {
    }
    n := c.Bytes().Len()
    return str.From(buf[0..n]) else {
        return ""
    }
}

// NativeRun writes the block through a cap-byte buffer over a loopback
// stream whose write k runs in a cancelled scope.
NativeRun(a: &Arena, io: Io, der: []u8, cap: int, k: int): net.Error|OutOfMemory!Outcome from a {
    l := try net.Listen(io, "127.0.0.1:0")
    defer l.Close()
    var ab: [32]u8 = [0; 32]
    c := try net.Dial(io, Addr(mut ab[..], l.Port()))
    srv := l.Accept(io) else e {
        c.Close()
        fail e
    }
    defer srv.Close()
    var sink := Sink(c: &c, n: 0, k: k, hits: 0)
    var b: [16]u8 = [0; 16]
    port := Port
    var w := port.Writer(io, mut sink, mut b[0..cap])
    var fails := 0
    var ok := true
    FormatPem(mut w, Label, der) else {
        fails += 1
        ok = false
    }
    if ok {
        w.Flush() else {
            fails += 1
        }
    }
    writes := sink.n
    hits := sink.hits
    c.Close()
    var t := Text.New(a)
    try ReadRest(&srv, io, mut t)
    return (got: try a.Dup(t.Str()), fails: fails, writes: writes, hits: hits)
}

// SimRun is NativeRun under std.sim, write k failing by a fault.
SimRun(a: &Arena, sm: mut sim.Sim, der: []u8, cap: int, k: int): net.Error|OutOfMemory!Outcome from a {
    io := sm.Io()
    l := try net.Listen(io, "127.0.0.1:7561")
    defer l.Close()
    c := try net.Dial(io, "127.0.0.1:7561")
    srv := l.Accept(io) else e {
        c.Close()
        fail e
    }
    defer srv.Close()
    var sink := SimSink(sm: sm, c: &c, n: 0, k: k, hits: 0)
    var b: [16]u8 = [0; 16]
    port := SimPort
    var w := port.Writer(io, mut sink, mut b[0..cap])
    var fails := 0
    var ok := true
    FormatPem(mut w, Label, der) else {
        fails += 1
        ok = false
    }
    if ok {
        w.Flush() else {
            fails += 1
        }
    }
    writes := sink.n
    hits := sink.hits
    c.Close()
    var t := Text.New(a)
    try ReadRest(&srv, io, mut t)
    return (got: try a.Dup(t.Str()), fails: fails, writes: writes, hits: hits)
}

// Judge asserts the Prefix class of run k of writes.
Judge(o: &Outcome, whole: str, k: int, writes: int) {
    if k <= writes {
        Assert(o.hits == 1 && o.fails == 1)
        Assert(o.got.Len() < whole.Len() && whole.StartsWith(o.got))
    } else {
        Assert(o.hits == 0 && o.fails == 0 && o.got == whole)
    }
}

test "FormatPem is prefix: a cancel at each of its writes leaves a proper prefix of the block" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    der := try Der(sys.arena)
    whole := try Whole(sys.arena, der)
    for cap in [0, 7] {
        plain := try NativeRun(&ar, sys.io, der, cap, 0)
        Assert(plain.fails == 0 && plain.hits == 0 && plain.got == whole)
        writes := plain.writes
        Assert(writes >= 5)
        ar.Reset()
        for k in 1..writes + 2 {
            o := try NativeRun(&ar, sys.io, der, cap, k)
            Judge(&o, whole, k, writes)
            ar.Reset()
        }
    }
}

test "under std.sim: FormatPem is prefix: a failed write at each of its writes leaves a proper prefix of the block" (sys: &System) {
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    der := try Der(sys.arena)
    whole := try Whole(sys.arena, der)
    for cap in [0, 7] {
        var writes := 0
        for k in 0..64 {
            if k > 0 && k > writes + 1 {
                break
            }
            var sm := try sim.Sim.New(sys.arena, Cast[u64](300 + k))
            defer sm.Free()
            o := try SimRun(&ar, mut sm, der, cap, k)
            if k == 0 {
                Assert(o.fails == 0 && o.hits == 0 && o.got == whole)
                writes = o.writes
                Assert(writes >= 5)
            } else {
                Judge(&o, whole, k, writes)
            }
            ar.Reset()
        }
    }
}
`
