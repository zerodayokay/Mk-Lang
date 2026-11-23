// Why: std.core's type erasure (ErasedBox, ErasedCall, the ports, Capped) lets
// std modules with no unsafe code of their own hold transports and callbacks.
// What: the std.coreprobe fixture module, its blocks, and the reject and panic tests.
// How: the fixture is written into a copy of std (std.core is std-internal, E0202
// outside std) and run by `mk test` with the counting heap, so a leak fails.
//
// std.coreprobe uses the erasure as std.net, std.http, std.compress and
// std.log do, and is not in pkg.mk's unsafe_modules, which is the property
// the erasure exists for.
// Its tests are cancel-safety tests; tools/gate/safety.go lists each.

package std

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// core8Conn is std.net's erasure in miniature: a Transport interface, a
// Conn that boxes one on a &Heap and dispatches through fn values, with a
// lock per direction, and a connector (net.Secure's shape) over an erased
// state and function.
const core8Conn = `// A connection over any transport, as std.net builds net.Conn.

import std.core

export error Broken
export type Error = Broken | Canceled

export interface Transport {
    Read(&self, io: Io, buf: mut []u8): Error!int
    WriteAll(&self, io: Io, data: []u8): Error!void
    Close(&self, io: Io): Error!void
    Abandon(self)
}

export linear view struct Conn {
    box: core.SealedBox
    h: &Heap
    reads: Mutex[int]
    writes: Mutex[int]
    do_read: fn(&core.SealedBox, Io, mut []u8): Error!int
    do_write: fn(&core.SealedBox, Io, []u8): Error!void
    do_close: fn(&core.SealedBox, Io): Error!void
    do_abandon: fn(core.SealedBox, &Heap)
}

export NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t {
    empty: ?T = none
    b := Box[?T].New(h.Alloc(), empty) else e {
        match empty {
            some(x) { x.Abandon() }
            none {}
        }
        fail e
    }
    return Conn(box: core.SealedBox.Fill[T](b, t), h: h, reads: Mutex[int].New(0), writes: Mutex[int].New(0), do_read: ConnRead[T], do_write: ConnWrite[T], do_close: ConnClose[T], do_abandon: ConnAbandon[T])
}

DoConnRead[T: Transport + Send + Share](x: &?T, a: (io: Io, buf: mut []u8, out: mut (n: int, err: ?Error))) {
    (io, buf, out) := a
    if t := &*x {
        match t.Read(io, buf) {
            ok(n) { *out = (n: n, err: none) }
            err(e) { *out = (n: 0, err: some(e)) }
        }
    }
}

DoConnWrite[T: Transport + Send + Share](x: &?T, a: (io: Io, data: []u8, err: mut ?Error)) {
    (io, data, err) := a
    if t := &*x {
        match t.WriteAll(io, data) {
            ok(_) { *err = none }
            err(e) { *err = some(e) }
        }
    }
}

DoConnClose[T: Transport + Send + Share](x: &?T, a: (io: Io, err: mut ?Error)) {
    (io, err) := a
    if t := &*x {
        match t.Close(io) {
            ok(_) { *err = none }
            err(e) { *err = some(e) }
        }
    }
}

DoConnAbandon[T: Transport + Send + Share](box: Box[?T], h: &Heap) {
    match box.Take(h.Alloc()) {
        some(t) { t.Abandon() }
        none {}
    }
}

ConnRead[T: Transport + Send + Share](b: &core.SealedBox, io: Io, buf: mut []u8): Error!int {
    var out: (n: int, err: ?Error) = (n: 0, err: none)
    b.With[?T, (io: Io, buf: mut []u8, out: mut (n: int, err: ?Error))]((io: io, buf: buf, out: mut out), DoConnRead[T])
    if e := out.err {
        fail e
    }
    return out.n
}

ConnWrite[T: Transport + Send + Share](b: &core.SealedBox, io: Io, data: []u8): Error!void {
    var err: ?Error = none
    b.With[?T, (io: Io, data: []u8, err: mut ?Error)]((io: io, data: data, err: mut err), DoConnWrite[T])
    if e := err {
        fail e
    }
}

ConnClose[T: Transport + Send + Share](b: &core.SealedBox, io: Io): Error!void {
    var err: ?Error = none
    b.With[?T, (io: Io, err: mut ?Error)]((io: io, err: mut err), DoConnClose[T])
    if e := err {
        fail e
    }
}

ConnAbandon[T: Transport + Send + Share](b: core.SealedBox, h: &Heap) {
    b.TakeWith[?T, &Heap](h, DoConnAbandon[T])
}

export Conn.Read(&self, io: Io, buf: mut []u8): Error!int {
    g := try self.reads.Lock(io)
    defer g.Unlock()
    f := self.do_read
    return try f(&self.box, io, buf)
}

export Conn.WriteAll(&self, io: Io, data: []u8): Error!void {
    g := try self.writes.Lock(io)
    defer g.Unlock()
    f := self.do_write
    try f(&self.box, io, data)
}

// Close speaks to the peer through &T, so a failure leaves the Conn with
// the caller unchanged (consume on success, E0411), then releases.
export Conn.Close(self, io: Io): Error!void {
    close := self.do_close
    try close(&self.box, io)
    self.Abandon()
}

export Conn.Abandon(self) {
    Conn(box, h, reads, writes, do_abandon, ..) := self
    _ = reads.Into()
    _ = writes.Into()
    do_abandon(box, h)
}

ConnFill(c: &Conn, io: Io, dst: mut []u8): ReadError!int {
    return c.Read(io, dst) else {
        fail ReadError
    }
}

ConnSend(c: &Conn, io: Io, b: []u8): WriteError!void {
    c.WriteAll(io, b) else {
        fail WriteError
    }
}

ConnFlush(c: &Conn, io: Io): WriteError!void {
}

const ConnReads = core.ReadPort[Conn].New(ConnFill)
const ConnWrites = core.WritePort[Conn].New(ConnSend, ConnFlush)

export Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf {
    return (&ConnReads).Reader(io, self, buf)
}

export Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf {
    return (&ConnWrites).Writer(io, self, buf)
}

// Secure is net.Secure's shape: plain, so an options record can hold it.
export struct Secure {
    export call: core.ErasedCall
    thunk: fn(&Secure, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn
}

export Secure.Of[T: Share](state: &T, f: fn(&T, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn): Secure from state {
    return Secure(call: core.ErasedCall.Of[T, fn(&T, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn](state, f), thunk: SecureCall[T])
}

SecureCall[T: Share](s: &Secure, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    f := s.call.Fn[T, fn(&T, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn]()
    st := s.call.State[T, fn(&T, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn]()
    return try f(st, h, io, conn, host)
}

export Secure.Connect(&self, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    thunk := self.thunk
    return try thunk(self, h, io, conn, host)
}
`

// core8Ports are a module's own states read and written through the ports,
// with fault injection by fill or write number for the cancel-safety tests.
const core8Ports = `// Readers and Writers over this module's own state.

import std.core

// Src hands out text in order to every Reader made over it.
struct Src {
    text: str
    pos: Mutex[int]
}

SrcFill(s: &Src, io: Io, dst: mut []u8): ReadError!int {
    var g := s.pos.Lock(io) else {
        fail ReadError
    }
    p := *g.Ref()
    var n := s.text.Len() - p
    if n > dst.Len() {
        n = dst.Len()
    }
    (mut dst[0..n]).CopyFrom(s.text.AsBytes()[p..p + n])
    *g.Mut() = p + n
    g.Unlock()
    return n
}

const SrcPort = core.ReadPort[Src].New(SrcFill)

Src.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf {
    return (&SrcPort).Reader(io, self, buf)
}

// Flaky gives at most chunk bytes a fill and fails the fill numbered
// fail_at, consuming nothing: an interruption at that await point.
struct Flaky {
    data: str
    pos: int
    chunk: int
    fail_at: int
    calls: int
}

FlakyFill(f: mut Flaky, io: Io, dst: mut []u8): ReadError!int {
    f.calls += 1
    if f.calls == f.fail_at {
        fail ReadError
    }
    var n := f.data.Len() - f.pos
    if n > f.chunk {
        n = f.chunk
    }
    if n > dst.Len() {
        n = dst.Len()
    }
    (mut dst[0..n]).CopyFrom(f.data.AsBytes()[f.pos..f.pos + n])
    f.pos += n
    return n
}

const FlakyPort = core.ReadPortMut[Flaky].New(FlakyFill)

Flaky.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf {
    return (&FlakyPort).Reader(io, self, buf)
}

// Upper decodes by upper-casing ASCII letters, counting its fills.
struct Upper {
    fills: int
}

UpperFill(u: mut Upper, src: mut Reader, dst: mut []u8): ReadError!int {
    u.fills += 1
    n := try src.Read(dst)
    for i in 0..n {
        if dst[i] >= 0x61 && dst[i] <= 0x7A {
            dst[i] -= 0x20
        }
    }
    return n
}

const UpperPort = core.FilterPort[Upper].New(UpperFill)

// Sink counts the bytes it is given, from any task.
struct Sink {
    bytes: Mutex[int]
}

SinkWrite(s: &Sink, io: Io, b: []u8): WriteError!void {
    var g := s.bytes.Lock(io) else {
        fail WriteError
    }
    *g.Mut() += b.Len()
    g.Unlock()
}

SinkFlush(s: &Sink, io: Io): WriteError!void {
}

const SinkPort = core.WritePort[Sink].New(SinkWrite, SinkFlush)

// Rec keeps the bytes it is given, failing the write numbered fail_at and
// the flush numbered flush_fail_at.
struct Rec {
    data: [256]u8
    len: int
    calls: int
    flushes: int
    fail_at: int
    flush_fail_at: int
}

RecWrite(r: mut Rec, io: Io, b: []u8): WriteError!void {
    r.calls += 1
    if r.calls == r.fail_at {
        fail WriteError
    }
    (mut r.data[r.len..r.len + b.Len()]).CopyFrom(b)
    r.len += b.Len()
}

RecFlush(r: mut Rec, io: Io): WriteError!void {
    r.flushes += 1
    if r.flushes == r.flush_fail_at {
        fail WriteError
    }
}

const RecPort = core.WritePortMut[Rec].New(RecWrite, RecFlush)

NewRec(fail_at: int, flush_fail_at: int): Rec {
    return Rec(data: [0; 256], len: 0, calls: 0, flushes: 0, fail_at: fail_at, flush_fail_at: flush_fail_at)
}

// Record formats into a 16-byte record: the kept count times 1000 plus the
// dropped count, or -1.
Record(): int {
    var buf: [16]u8 = [0; 16]
    var c := core.Capped.New(mut buf[..])
    var w := c.Writer()
    Print(mut w, "abc {n} defghijklmnop", (n: 12345)) else {
        return -1
    }
    return c.Bytes().Len() * 1000 + c.Dropped()
}

const RecordAtCompileTime = Record()

// Same reports whether b holds exactly the bytes of s.
Same(b: []u8, s: str): bool {
    t := str.From(b) else {
        return false
    }
    return t == s
}
`

// core8Blocks are the erasure's behaviour tests.
const core8Blocks = `import std.core

// Mem is an in-memory transport: it counts the bytes written, and Read
// reports the count in 8 bytes.
linear struct Mem {
    total: Mutex[int]
}

impl Transport for Mem {
    Read(&self, io: Io, buf: mut []u8): Error!int {
        g := try self.total.Lock(io)
        n := *g.Ref()
        g.Unlock()
        for i in 0..8 {
            buf[i] = Trunc[u8](n >> (8 * i))
        }
        return 8
    }
    WriteAll(&self, io: Io, data: []u8): Error!void {
        var g := try self.total.Lock(io)
        *g.Mut() += data.Len()
        g.Unlock()
    }
    // Fails while exactly 5 bytes were written: a close the peer refused.
    Close(&self, io: Io): Error!void {
        g := try self.total.Lock(io)
        n := *g.Ref()
        g.Unlock()
        if n == 5 {
            fail Broken
        }
    }
    Abandon(self) {
        Mem(total) := self
        _ = total.Into()
    }
}

Hammer(io: Io, c: &Conn) {
    var buf: [8]u8 = [0; 8]
    var wb: [3]u8 = [0; 3]
    for _ in 0..100 {
        c.WriteAll(io, "abcd".AsBytes()) else {
            return
        }
        _ = c.Read(io, mut buf[..]) else {
            return
        }
        // Through the Conn's own Writer and Reader, from many tasks at once.
        var w := c.Writer(io, mut wb[..])
        w.Str("xy") else {
            return
        }
        w.Flush() else {
            return
        }
    }
}

MemTotal(c: &Conn, io: Io): Error!int {
    var buf: [8]u8 = [0; 8]
    n := try c.Read(io, mut buf[..])
    Assert(n == 8)
    var total := 0
    for i in 0..8 {
        total |= int(buf[i]) << (8 * i)
    }
    return total
}

test "ERASE-1 an erased in-memory transport boxed, shared by 16 tasks and freed" (sys: &System) {
    m := Mem(total: Mutex[int].New(0))
    c := NewConn(sys.heap, m) else e {
        m.Abandon()
        fail e
    }
    onerr c.Abandon()
    scope s := sys.io.Scope() {
        for _ in 0..16 {
            s.go Hammer(sys.io, &c)
        }
    }
    Assert(try MemTotal(&c, sys.io) == 16 * 100 * 6)
    try c.Close(sys.io)
}

test "ERASE-5 a Close that fails leaves the Conn with its caller, and Abandon ends it" (sys: &System) {
    m := Mem(total: Mutex[int].New(0))
    c := NewConn(sys.heap, m) else e {
        m.Abandon()
        fail e
    }
    onerr c.Abandon()
    try c.WriteAll(sys.io, "12345".AsBytes())
    c.Close(sys.io) else e {
        var eb: [16]u8 = [0; 16]
        var ec := core.Capped.New(mut eb[..])
        var ew := ec.Writer()
        Print(mut ew, "{e}", (e)) else {
        }
        Assert(Same(ec.Bytes(), "Broken"))
        c.Abandon()
        return
    }
    Assert(false)
}

test "ERASE-2 a Conn's Reader reads through its port" (sys: &System) {
    m := Mem(total: Mutex[int].New(0))
    c := NewConn(sys.heap, m) else e {
        m.Abandon()
        fail e
    }
    defer c.Abandon()
    try c.WriteAll(sys.io, "0123456789".AsBytes())
    var rb: [3]u8 = [0; 3]
    var r := c.Reader(sys.io, mut rb[..])
    var out: [8]u8 = [0; 8]
    Assert(try r.ReadAll(mut out[..]) == 8)
    Assert(out[0] == 10 && out[1] == 0)
}

test "ERASE-3 ErasedBox.Is and Ref name the type it was made from" (sys: &System) {
    b := try Box[Mutex[int]].New(sys.heap.Alloc(), Mutex[int].New(41))
    e := core.ErasedBox.Of[Mutex[int]](b)
    Assert(e.Is[Mutex[int]]())
    Assert(!e.Is[Mutex[i64]]())
    _ = e.Ref[Mutex[int]]()
    back := e.Take[Mutex[int]]()
    m := back.Take(sys.heap.Alloc())
    Assert(m.Into() == 41)
}

// Fill moves a value into an empty Box[?T] and erases it as ?T.
test "ERASE-4 ErasedBox.Fill erases an empty box with the value moved in, as ?T" (sys: &System) {
    empty: ?Mutex[int] = none
    b := try Box[?Mutex[int]].New(sys.heap.Alloc(), empty)
    e := core.ErasedBox.Fill[Mutex[int]](b, Mutex[int].New(43))
    Assert(e.Is[?Mutex[int]]())
    Assert(!e.Is[Mutex[int]]())
    Assert(e.Ref[?Mutex[int]]().IsSome())
    back := e.Take[?Mutex[int]]()
    match back.Take(sys.heap.Alloc()) {
        some(m) { Assert(m.Into() == 43) }
        none { Assert(false) }
    }
}

// Counter is a connector's state: it adds the host's length per connect.
struct Counter {
    n: Mutex[int]
    tag: str
}

CountConnect(c: &Counter, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    var g := try c.n.Lock(io)
    *g.Mut() += host.Len()
    g.Unlock()
    return conn
}

test "ERASE-4 an erased connector is plain and calls its function over its state" (sys: &System) {
    c := Counter(n: Mutex[int].New(0), tag: "x")
    sec := Secure.Of[Counter](&c, CountConnect)
    copy := sec
    m := Mem(total: Mutex[int].New(0))
    conn := NewConn(sys.heap, m) else e {
        m.Abandon()
        fail e
    }
    c2 := copy.Connect(sys.heap, sys.io, conn, "host") else e {
        conn.Abandon()
        fail e
    }
    c3 := sec.Connect(sys.heap, sys.io, c2, "ab") else e {
        c2.Abandon()
        fail e
    }
    c3.Abandon()
    g := try c.n.Lock(sys.io)
    Assert(*g.Ref() == 6)
    g.Unlock()
}

ShareSink(io: Io, s: &Sink) {
    var b: [5]u8 = [0; 5]
    var w := (&SinkPort).Writer(io, s, mut b[..])
    for _ in 0..50 {
        w.Str("abc") else {
            return
        }
    }
    w.Flush() else {
        return
    }
}

test "PORT-1 ReadPort: Readers over one shared state see its bytes in order" (sys: &System) {
    s := Src(text: "hello, ports", pos: Mutex[int].New(0))
    var b1: [4]u8 = [0; 4]
    var r := s.Reader(sys.io, mut b1[..])
    var out: [32]u8 = [0; 32]
    n := try r.ReadAll(mut out[..])
    Assert(Same(out[0..n], "hello, ports"))
    // An empty inline buffer: every Read is one fill, Line one fill a byte.
    s2 := Src(text: "ab\ncd", pos: Mutex[int].New(0))
    var nobuf: [0]u8 = []
    var r2 := s2.Reader(sys.io, mut nobuf[..])
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert((try r2.Line(&ar)).Must() == "ab")
    Assert((try r2.Line(&ar)).Must() == "cd")
    Assert((try r2.Line(&ar)).IsNone())
}

test "PORT-2 FilterPort: a filter Reader decodes its source through a buffer" (sys: &System) {
    s := Src(text: "mixed Case text, line one\nline two\n", pos: Mutex[int].New(0))
    var sb: [5]u8 = [0; 5]
    var src := s.Reader(sys.io, mut sb[..])
    var f := core.Filtered[Upper].New(Upper(fills: 0))
    var fb: [64]u8 = [0; 64]
    var r := (&UpperPort).Reader(mut f, mut src, mut fb[..])
    var ar := try Arena.New(sys.heap)
    defer ar.Free()
    Assert((try r.Line(&ar)).Must() == "MIXED CASE TEXT, LINE ONE")
    Assert((try r.Line(&ar)).Must() == "LINE TWO")
    Assert((try r.Line(&ar)).IsNone())
    Assert(f.Ref().fills >= 2)
    f.Mut().fills = 0
    u := f.Into()
    Assert(u.fills == 0)
}

test "PORT-3 WritePort: Writers over one shared state from 16 tasks" (sys: &System) {
    s := Sink(bytes: Mutex[int].New(0))
    scope sc := sys.io.Scope() {
        for _ in 0..16 {
            sc.go ShareSink(sys.io, &s)
        }
    }
    g := try s.bytes.Lock(sys.io)
    Assert(*g.Ref() == 16 * 50 * 3)
    g.Unlock()
}

test "PORT-4 WritePortMut: a Writer over a state it holds exclusively" (sys: &System) {
    var r := NewRec(0, 0)
    var b: [4]u8 = [0; 4]
    var w := (&RecPort).Writer(sys.io, mut r, mut b[..])
    try w.Str("abc")
    try w.Bytes("defgh".AsBytes())
    try Print(mut w, "{n}", (n: 42))
    try w.Flush()
    Assert(Same(r.data[0..r.len], "abcdefgh42"))
    Assert(r.flushes == 1)
}

test "CAP-1 Capped keeps what fits and counts the rest, at compile time too" (sys: &System) {
    Assert(RecordAtCompileTime == Record())
    Assert(RecordAtCompileTime == 16 * 1000 + 7)
    var buf: [4]u8 = [0; 4]
    var c := core.Capped.New(mut buf[..])
    var w := c.Writer()
    try w.Str("ab")
    try w.Str("")
    try w.Str("cdef")
    try w.Flush()
    Assert(Same(c.Bytes(), "abcd"))
    Assert(c.Dropped() == 2)
    c.Reset()
    Assert(c.Bytes().Len() == 0 && c.Dropped() == 0)
    var empty: [0]u8 = []
    var z := core.Capped.New(mut empty[..])
    var zw := z.Writer()
    try zw.Str("xyz")
    Assert(z.Bytes().Len() == 0 && z.Dropped() == 3)
}
`

// core8Reject are programs the erasure must refuse: a transport that
// borrows a local cannot outlive it through NewConn, nor a
// connector's state through Secure.Of.
const core8Reject = `linear struct Lent {
    m: &Mutex[int]
}

impl Transport for Lent {
    Read(&self, io: Io, buf: mut []u8): Error!int {
        return 0
    }
    Close(&self, io: Io): Error!void {
    }
    WriteAll(&self, io: Io, data: []u8): Error!void {
    }
    Abandon(self) {
        Lent(m) := self
        _ = m
    }
}

Make(h: &Heap): OutOfMemory!Conn {
    m := Mutex[int].New(0)
    l := Lent(m: &m)
    return NewConn(h, l) else e {
        l.Abandon()
        fail e
    }
}

struct Holder {
    n: int
}

Hold(c: &Holder, h: &Heap, io: Io, conn: Conn, host: str): Error!Conn from h, conn {
    return conn
}

Leak(): Secure {
    c := Holder(n: 1)
    return Secure.Of[Holder](&c, Hold)
}

// The anchor field is exported: reassigning it does not shed the loan.
const Fixed = Holder(n: 2)

Reanchor(): Secure {
    c := Holder(n: 1)
    var s := Secure.Of[Holder](&c, Hold)
    t := Secure.Of[Holder](&Fixed, Hold)
    s.call.anchor = t.call.anchor
    return s
}
`

// core8Loans are uses the ports' and Capped's unsafe reasons rely on the
// checker to refuse: the state of a Reader or Writer used while it lives,
// a filter's source used or outliving it, Capped read while its Writer
// lives, and an ErasedCall over a function value that is not Send and
// Share.
const core8Loans = `import std.core

StateWhileReading(io: Io): ReadError!int {
    var f := Flaky(data: "ab", pos: 0, chunk: 1, fail_at: 0, calls: 0)
    var b: [4]u8 = [0; 4]
    var r := f.Reader(io, mut b[..])
    f.pos = 1
    var d: [2]u8 = [0; 2]
    return try r.Read(mut d[..])
}

StateWhileWriting(io: Io): WriteError!int {
    var rec := NewRec(0, 0)
    var b: [4]u8 = [0; 4]
    var w := (&RecPort).Writer(io, mut rec, mut b[..])
    rec.len = 0
    try w.Flush()
    return 0
}

SourceWhileFiltering(io: Io): ReadError!int {
    s := Src(text: "ab", pos: Mutex[int].New(0))
    var sb: [4]u8 = [0; 4]
    var src := s.Reader(io, mut sb[..])
    var f := core.Filtered[Upper].New(Upper(fills: 0))
    var fb: [4]u8 = [0; 4]
    var r := (&UpperPort).Reader(mut f, mut src, mut fb[..])
    var d: [2]u8 = [0; 2]
    _ = try src.Read(mut d[..])
    return try r.Read(mut d[..])
}

FilterOverLocal(io: Io, f: mut core.Filtered[Upper], buf: mut []u8): Reader from f, buf {
    s := Src(text: "ab", pos: Mutex[int].New(0))
    var sb: [4]u8 = [0; 4]
    var src := s.Reader(io, mut sb[..])
    return (&UpperPort).Reader(f, mut src, buf)
}

PeekWhileWriting(): WriteError!int {
    var buf: [4]u8 = [0; 4]
    var c := core.Capped.New(mut buf[..])
    var w := c.Writer()
    n := c.Bytes().Len()
    try w.Str("x")
    return n
}
`

// core8Bound is an ErasedCall over a value that is not Send and Share.
const core8Bound = `import std.core

const Fixed2 = Src2(n: 2)

struct Src2 {
    n: int
}

CellCall(c: &Cell[u8]): core.ErasedCall from c {
    return core.ErasedCall.Of[Src2, &Cell[u8]](&Fixed2, c)
}
`

// core8Mismatch recovers an erased value as another type: a std module
// bug, which panics (code 8) before a byte is read.
const core8Mismatch = `import std.core

test "MISMATCH-1 an ErasedBox taken as another type panics" (sys: &System) {
    b := try Box[Mutex[int]].New(sys.heap.Alloc(), Mutex[int].New(1))
    e := core.ErasedBox.Of[Mutex[int]](b)
    _ = e.Ref[Mutex[u8]]()
    x := e.Take[Mutex[int]]()
    _ = x.Take(sys.heap.Alloc()).Into()
}

Two(c: &Counter2, x: int): int {
    return x
}

struct Counter2 {
    n: int
}

test "MISMATCH-2 an ErasedCall called as another type panics" (sys: &System) {
    c := Counter2(n: 1)
    call := core.ErasedCall.Of[Counter2, fn(&Counter2, int): int](&c, Two)
    _ = call.State[Counter2, fn(&Counter2, i64): int]()
}

// Take would rebuild a Box of another type and size, and Fn
// would give a function pointer typed as another fn type.
test "MISMATCH-3 an ErasedBox taken back as another type panics" (sys: &System) {
    b := try Box[Mutex[int]].New(sys.heap.Alloc(), Mutex[int].New(1))
    e := core.ErasedBox.Of[Mutex[int]](b)
    x := e.Take[Mutex[u8]]()
    _ = x.Take(sys.heap.Alloc()).Into()
}

test "MISMATCH-4 an ErasedCall's function recovered as another fn type panics" (sys: &System) {
    c := Counter2(n: 1)
    call := core.ErasedCall.Of[Counter2, fn(&Counter2, int): int](&c, Two)
    f := call.Fn[Counter2, fn(&Counter2, i64): int]()
    Assert(f(&c, 1) == 1)
}
`

// core8Fixtures is the std.coreprobe module with the given test files.
func core8Fixtures(tests map[string]string) map[string]string {
	fx := map[string]string{
		"coreprobe/conn.mk":  core8Conn,
		"coreprobe/ports.mk": core8Ports,
	}
	for name, text := range tests {
		fx["coreprobe/"+name] = text
	}
	return fx
}

// core8MkTest runs mk test --std=std.coreprobe in a fresh copy of std with
// the fixtures, MK_CARRIERS set, and returns the output and status.
func core8MkTest(t *testing.T, fixtures map[string]string, carriers int, flags ...string) (string, int) {
	t.Helper()
	root := t.TempDir()
	stdDir := copyStd(t, root, fixtures)
	t.Setenv("MK_CACHE", filepath.Join(root, "cache"))
	t.Setenv("MK_CARRIERS", fmt.Sprint(carriers))
	args := append([]string{"test", "--std=std.coreprobe"}, flags...)
	out, status := mkCmd(t, root, stdDir, args...)
	if strings.Contains(out, "no usable LLVM") {
		t.Skipf("needs the LLVM toolchain: %s", out)
	}
	return out, status
}

// core8WantPass fails t unless every test block named in files passed.
func core8WantPass(t *testing.T, out string, status int, files ...string) {
	t.Helper()
	n := 0
	for _, f := range files {
		for _, m := range regexp.MustCompile(`(?m)^test "([^"]+)"`).FindAllStringSubmatch(f, -1) {
			n++
			if !strings.Contains(out, fmt.Sprintf("PASS std.coreprobe %q", m[1])) {
				t.Errorf("%q did not pass (status %d)", m[1], status)
			}
		}
	}
	if n == 0 {
		t.Fatal("no test block found")
	}
	if t.Failed() || status != 0 {
		t.Logf("mk test output (status %d):\n%s", status, out)
	}
}

// TestCore_ErasureBlocks: the erasure, the ports and Capped, through a
// module with no unsafe code, at 1 and 16 carriers in dev and at 16 in
// release; the counting heap fails a block that leaks the box.
func TestCore_ErasureBlocks(t *testing.T) {
	for _, c := range []struct {
		carriers int
		flags    []string
	}{{1, nil}, {16, nil}, {16, []string{"--release"}}} {
		t.Run(fmt.Sprintf("carriers=%d%s", c.carriers, strings.Join(c.flags, "")), func(t *testing.T) {
			out, status := core8MkTest(t, core8Fixtures(map[string]string{"tests_erasure.mk": core8Blocks}), c.carriers, c.flags...)
			core8WantPass(t, out, status, core8Blocks)
		})
	}
}

// TestCore_LinuxBlocks: the erasure blocks, the cancel-safety and std.sim
// stress blocks on x86_64-linux, under the default I/O backend and under
// MK_IO=epoll, which must give the same verdicts: at 1
// carrier, and at 16 with the scheduler's jitter points under MK_TEST_SEED
// 1 and 2, in dev (release runs on Windows only, TestCore_StressSeeds:
// a WSL hang there is not understood, so the Linux matrix stays small).
// mk runs each block through wsl.exe, so this runs only with MK_LINUX=1,
// under tools/wsl-lock.ps1, with go test -p 1.
func TestCore_LinuxBlocks(t *testing.T) {
	linuxGate(t)
	// WSL passes on only the variables WSLENV names (as internal/stdtest's
	// Linux runs do): without them the child would run at its defaults.
	wslenv := "MK_CARRIERS/u:MK_TEST_SEED/u:MK_IO/u"
	if old := os.Getenv("WSLENV"); old != "" {
		wslenv = old + ":" + wslenv
	}
	t.Setenv("WSLENV", wslenv)
	var runs []core8Run
	for _, io := range []string{"", "epoll"} {
		runs = append(runs, core8Run{carriers: 1, io: io}, core8Run{carriers: 16, seed: 1, io: io}, core8Run{carriers: 16, seed: 2, io: io})
	}
	for name, file := range map[string]string{"tests_erasure.mk": core8Blocks, "tests_cancel.mk": core8Cancel, "tests_simstress.mk": core8SimStress} {
		t.Run(name, func(t *testing.T) {
			core8MkMatrix(t, core8Fixtures(map[string]string{name: file}), []string{"--target", "x86_64-linux"}, runs, file)
		})
	}
}

// TestCore_ErasureRejects: a transport or a connector state that borrows
// a local cannot leave the function through the erasure (E0603), not even
// with the exported anchor field reassigned to a static's, and the probe
// module needs no unsafe (it is not in unsafe_modules).
func TestCore_ErasureRejects(t *testing.T) {
	out, status := core8MkTest(t, core8Fixtures(map[string]string{"tests_reject.mk": core8Reject}), 1)
	if status == 0 {
		t.Fatalf("mk test accepted the escaping borrows:\n%s", out)
	}
	for _, want := range []string{
		"tests_reject.mk:22:5: E0603",
		"tests_reject.mk:38:5: E0603",
		"tests_reject.mk:49:5: E0603",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if n := strings.Count(out, ": E"); n != 3 {
		t.Errorf("%d diagnostics, want 3 (an unsafe use in the probe would be E1101 or E1102)", n)
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

// TestCore_LoanRejects: each use core8Loans lists is refused at its line.
func TestCore_LoanRejects(t *testing.T) {
	for _, c := range []struct {
		file, text string
		want       []string
	}{
		{"tests_loans.mk", core8Loans, []string{
			"tests_loans.mk:7:5: E0502",   // the state of a live ReadPortMut Reader
			"tests_loans.mk:16:5: E0502",  // the state of a live WritePortMut Writer
			"tests_loans.mk:29:13: E0501", // a filter's source while the filter reads
			"tests_loans.mk:37:5: E0603",  // a filter Reader over a local source
			"tests_loans.mk:44:10: E0504", // Capped read while its Writer lives
		}},
		{"tests_bound.mk", core8Bound, []string{
			"tests_bound.mk:10:12: E0701 bound-unmet: `&Cell[u8]` does not satisfy `F: Send`",
			"tests_bound.mk:10:12: E0701 bound-unmet: `&Cell[u8]` does not satisfy `F: Share`",
		}},
	} {
		out, status := core8MkTest(t, core8Fixtures(map[string]string{c.file: c.text}), 1)
		if status == 0 {
			t.Fatalf("%s: mk test accepted the uses:\n%s", c.file, out)
		}
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("missing %s", w)
			}
		}
		if n := strings.Count(out, ": E"); n != len(c.want) {
			t.Errorf("%s: %d diagnostics, want %d", c.file, n, len(c.want))
		}
		if t.Failed() {
			t.Logf("output:\n%s", out)
		}
	}
}

// TestCore_TypeMismatchPanics: recovering an erased value as another type
// panics with the erasure's message before a byte is read.
func TestCore_TypeMismatchPanics(t *testing.T) {
	out, _ := core8MkTest(t, core8Fixtures(map[string]string{"tests_mismatch.mk": core8Mismatch}), 1)
	for _, name := range []string{"MISMATCH-1", "MISMATCH-2", "MISMATCH-3", "MISMATCH-4"} {
		re := regexp.MustCompile(`FAIL std\.coreprobe "` + name + `[^"]*": panic[^\n]*\n(?:[^\n]*\n){0,3}?[^\n]*an erased value recovered as a type it was not made from`)
		if !re.MatchString(out) {
			t.Errorf("%s did not panic with the erasure's message", name)
		}
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

// core8FillFull fills a box that already holds a value: Fill panics
// (code 8) before it writes.
const core8FillFull = `import std.core

test "FILL-1 ErasedBox.Fill of a box that holds some panics" (sys: &System) {
    full: ?int = some(1)
    b := try Box[?int].New(sys.heap.Alloc(), full)
    e := core.ErasedBox.Fill[int](b, 2)
    x := e.Take[?int]()
    _ = x.Take(sys.heap.Alloc())
}
`

// TestCore_FillFullBoxPanics: ErasedBox.Fill of a box that is not empty
// panics with its message.
func TestCore_FillFullBoxPanics(t *testing.T) {
	out, _ := core8MkTest(t, core8Fixtures(map[string]string{"tests_fillfull.mk": core8FillFull}), 1)
	re := regexp.MustCompile(`FAIL std\.coreprobe "FILL-1[^"]*": panic[^\n]*\n(?:[^\n]*\n){0,3}?[^\n]*ErasedBox\.Fill: the box is not empty`)
	if !re.MatchString(out) {
		t.Errorf("FILL-1 did not panic with Fill's message\noutput:\n%s", out)
	}
}

// core8FillReject holds the uses that ErasedBox.Fill's `from b, x` refuses
// (the probes zzbm, zzb1, zzb2, zzb3 and zzb5). With Fill declared
// `from b` alone, the first four lose the loan of x that the ErasedBox
// holds, and FillPlant's result would read a dead stack slot (a raw-pointer
// body must not do what a checked body could not). FillArenaFreed guards the
// other source: it is refused by b's loan of the arena, and only with Fill
// declared `from x` alone would it check clean.
const core8FillReject = `import std.core

view struct FillL2 {
    n: &i64
}

view struct FillLent {
    n: &Mutex[int]
}

// zzbm: the box is on h and the result is from h, but it holds v's loan.
FillPlant(h: &Heap): ?core.ErasedBox from h {
    empty: ?FillL2 = none
    b := Box[?FillL2].New(h.Alloc(), empty) else {
        return none
    }
    v: i64 = 1234
    e := core.ErasedBox.Fill[FillL2](b, FillL2(n: &v))
    return some(e)
}

// zzb1: the ErasedBox holds m's loan, which the signature does not name.
FillFromBox(b: Box[?FillLent], m: &Mutex[int]): core.ErasedBox from b {
    return core.ErasedBox.Fill[FillLent](b, FillLent(n: m))
}

// zzb2: the Box that Take gives back holds m's loan too.
FillTakeBack(b: Box[?FillLent], m: &Mutex[int]): Box[?FillLent] from b {
    e := core.ErasedBox.Fill[FillLent](b, FillLent(n: m))
    return e.Take[?FillLent]()
}

// zzb3: m is moved while the ErasedBox holds its loan.
FillMoveLocal(h: &Heap): int {
    empty: ?FillLent = none
    b := Box[?FillLent].New(h.Alloc(), empty) else {
        return -1
    }
    m := Mutex[int].New(5)
    e := core.ErasedBox.Fill[FillLent](b, FillLent(n: &m))
    v := m.Into()
    bx := e.Take[?FillLent]()
    _ = bx.Take(h.Alloc())
    return v
}

// zzb5: the box is on an arena's memory, freed while the ErasedBox lives.
FillArenaFreed(h: &Heap): int {
    var ar := Arena.New(h) else {
        return -1
    }
    empty: ?FillLent = none
    b := Box[?FillLent].New(ar.Alloc(), empty) else {
        ar.Free()
        return -1
    }
    m := Mutex[int].New(5)
    e := core.ErasedBox.Fill[FillLent](b, FillLent(n: &m))
    ar.Free()
    bx := e.Take[?FillLent]()
    _ = bx.Take(h.Alloc())
    return m.Into()
}
`

// TestCore_FillRejects: each use core8FillReject lists is refused at its line.
func TestCore_FillRejects(t *testing.T) {
	out, status := core8MkTest(t, core8Fixtures(map[string]string{"tests_fillreject.mk": core8FillReject}), 1)
	if status == 0 {
		t.Fatalf("mk test accepted the uses:\n%s", out)
	}
	want := []string{
		"tests_fillreject.mk:19:5: E0603",  // zzbm: the result borrows v
		"tests_fillreject.mk:24:5: E0602",  // zzb1: the result holds m's loan
		"tests_fillreject.mk:30:5: E0602",  // zzb2: Take's Box holds m's loan
		"tests_fillreject.mk:41:10: E0503", // zzb3: m moved under e's loan
		"tests_fillreject.mk:59:5: E0503",  // zzb5: the arena freed under e's loan (b's, not x's)
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %s", w)
		}
	}
	// A signature line can hold "): ErasedBox", so count diagnostic heads.
	if n := len(regexp.MustCompile(`(?m):\d+:\d+: E\d{4} `).FindAllString(out, -1)); n != len(want) {
		t.Errorf("%d diagnostics, want %d", n, len(want))
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

// cappedModel is the Go model of Capped: keep the prefix that fits,
// count the rest.
type cappedModel struct {
	cap     int
	kept    []byte
	dropped int
}

func (c *cappedModel) write(b []byte) {
	k := min(c.cap-len(c.kept), len(b))
	c.kept = append(c.kept, b[:k]...)
	c.dropped += len(b) - k
}

// TestCore_CappedAgainstModel: random write sequences into Capped
// buffers of random capacity, one Mk block per sequence, checked against
// the Go model; seeded, so a failure reproduces.
func TestCore_CappedAgainstModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 30))
	var src strings.Builder
	src.WriteString("import std.core\n\n")
	alphabet := "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := 0; i < 40; i++ {
		capacity := rng.IntN(20)
		if i%5 == 0 {
			capacity = 0
		}
		m := cappedModel{cap: capacity}
		fmt.Fprintf(&src, "test \"CAPMODEL-%d capacity %d\" (sys: &System) {\n", i, capacity)
		fmt.Fprintf(&src, "    var buf: [%d]u8 = [0; %d]\n", capacity, capacity)
		src.WriteString("    var c := core.Capped.New(mut buf[..])\n    var w := c.Writer()\n")
		for j, n := 0, 1+rng.IntN(8); j < n; j++ {
			l := rng.IntN(12)
			var piece []byte
			for range l {
				piece = append(piece, alphabet[rng.IntN(len(alphabet))])
			}
			m.write(piece)
			if j%2 == 0 {
				fmt.Fprintf(&src, "    try w.Str(%q)\n", piece)
			} else {
				fmt.Fprintf(&src, "    try w.Bytes(%q.AsBytes())\n", piece)
			}
			if rng.IntN(3) == 0 {
				src.WriteString("    try w.Flush()\n")
			}
		}
		fmt.Fprintf(&src, "    Assert(Same(c.Bytes(), %q))\n    Assert(c.Dropped() == %d)\n}\n\n", m.kept, m.dropped)
	}
	text := src.String()
	out, status := core8MkTest(t, core8Fixtures(map[string]string{"tests_capmodel.mk": text}), 1)
	core8WantPass(t, out, status, text)
}
