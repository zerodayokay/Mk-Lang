// Why: std.fs and std.net must handle every answer an IoTable may give,
// including the ones the native runtime never shows.
// What: TestStdIo_FaultInjection over a scripted fixture Io.
// How: each SysErr maps to one fs.Error or net.Error member, with Dial
// translating a missing host to InvalidAddress first; partial reads,
// writes, recvs and sends are looped over; a bad count or a write with no
// progress is Os(0), WriteError or ReadError, never a moved position, an
// out-of-bounds slice or an endless loop; listen's port arrives in
// IoResult.os. The fixture's handles are 0, which rt.Abandon ignores, so
// every Close and Abandon path runs too.

package std

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// faultCore is the fixture core/io_zfault.mk: the scripted table.
const faultCore = `// Test fixture (std/std-io_fault_test.go): an Io whose table follows a
// script set by FaultIo, to drive std.fs and std.net through the contract
// edges that the native runtime never shows.

import std.raw

struct FaultWords {
    mode: raw.Atomic[u32]
    err: raw.Atomic[u32]
    os: raw.Atomic[i64]
    left: raw.Atomic[i64]
    calls: raw.Atomic[i64]
    at: raw.Atomic[i64]
    base: raw.Atomic[u64]
}

var Fault: FaultWords = FaultWords(mode: raw.Atomic[u32].New(0), err: raw.Atomic[u32].New(0),
    os: raw.Atomic[i64].New(0), left: raw.Atomic[i64].New(0), calls: raw.Atomic[i64].New(0),
    at: raw.Atomic[i64].New(0), base: raw.Atomic[u64].New(0))

const FaultErrs: [17]SysErr = [SysErr.None, SysErr.NotFound, SysErr.Exists, SysErr.Denied,
    SysErr.IsDirectory, SysErr.NotDirectory, SysErr.NoSpace, SysErr.InvalidPath, SysErr.Canceled,
    SysErr.Closed, SysErr.Refused, SysErr.Reset, SysErr.AddrInUse, SysErr.Unreachable,
    SysErr.InvalidAddress, SysErr.Other, SysErr.Unsupported]

const FaultTable = IoTable(open: FOpen, read: FRead, write: FWrite, size: FSize,
    sync: FSync, close: FClose, rename: InertRename, remove: InertPath, listen: FListen,
    accept: FHandle, dial: FPath, recv: FRecv, send: FSend, now: InertNone,
    sleep: FSleep, random: InertRandom, yield: InertNone, signal: InertSignal, wait: InertWait,
    wake: InertWake, mkdir: InertPath, stat: InertStat, opendir: InertPath, readdir: InertReadDir, udp: InertUdp,
    sendto: InertSendTo, recvfrom: InertRecvFrom, resolve: InertResolve, spawn: InertSpawn, procwait: InertHandle,
    prockill: InertHandle, inherited: InertNone,
    enter: InertHook, leave: InertHook, deadline: InertDeadline, stdio: InertStdio,
    call: InertCall)

// Modes: 0 every call succeeds (reads deliver what is left); 1 open,
// listen, dial and accept fail; 2 size fails; 3 reads and writes fail; 4
// they report one byte more than they were given; 5 they report -1; 6
// partial (reads 3 bytes, writes 2); 7 they report 0; 8 close and sync
// fail; 9 accept alone fails (after a listen that succeeded); 10 size is
// 0 (a pipe) and reads deliver what is left; 11 as 10 without filling the
// buffer (benchmarks); 12 positioned reads and writes (pos >= 0) fail, at
// pos -1 they succeed (a pipe on POSIX). A failure is
// FaultErrs[err] with os. After FaultReal(base), open and a close that
// does not fail are base's (real handles): every other entry
// stays scripted and never touches the handle, so a handle a path forgets
// to close or abandon stays open.
export FaultIo(mode: u32, err: u32, os: i64, left: i64): Io {
    Fault.mode.Store(mode)
    Fault.err.Store(err)
    Fault.os.Store(os)
    Fault.left.Store(left)
    Fault.calls.Store(0)
    Fault.at.Store(0)
    return MakeIo(raw.Of(&FaultTable), 0)
}

export FaultCalls(): i64 {
    return Fault.calls.Load()
}

export FaultReal(base: Io) {
    Fault.base.Store(Trunc[u64](raw.Addr(base.table)))
}

// FBase is the real table's Io with io's flags (FaultReal); none before.
FBase(io: Io): ?Io {
    t := Fault.base.Load()
    if t == 0 {
        return none
    }
    unsafe "t is the address of the runtime's global table (FaultReal)" {
        return some(MakeIo(raw.FromAddr[IoTable](Trunc[uint](t)), IoFlags(io)))
    }
}

// ArenaTaken is how many bytes of a's chunks are no longer free: every
// chunk's bytes but the current one's free tail, less the first chunk's
// record and header (measured over a fresh Arena.Sub).
export ArenaTaken(a: &Arena): int {
    unsafe "a's header and chunk list are live while a is" {
        h := a.hdr
        var total := 0
        var c := (*h).chunks
        for !raw.IsNull(c) {
            total += (*c).size
            c = (*c).prev
        }
        return total - ((*h).size - (*h).used) - HdrRoom()
    }
}

FOk(v: i64): IoResult {
    return IoResult(value: v, err: SysErr.None, os: 0)
}

FFail(): IoResult {
    return IoResult(value: 0, err: FaultErrs[int(Fault.err.Load())], os: Fault.os.Load())
}

FFails(m: u32): bool {
    return Fault.mode.Load() == m
}

FOpen(io: Io, p: str, mode: u32): IoResult {
    if FFails(1) {
        return FFail()
    }
    b := FBase(io) else {
        return FOk(0)
    }
    return IoOpen(b, p, mode)
}

FPath(io: Io, p: str): IoResult {
    if FFails(1) {
        return FFail()
    }
    return FOk(0)
}

FHandle(io: Io, h: uint): IoResult {
    if FFails(1) || FFails(9) {
        return FFail()
    }
    return FOk(0)
}

// FSleep returns at once: no scope of a scripted Io is cancelled, and
// std.net's cancellation point (Io.Sleep(0)) reads any failure as Canceled.
FSleep(io: Io, nanos: i64): IoResult {
    return FOk(0)
}

FListen(io: Io, p: str): IoResult {
    if FFails(1) {
        return FFail()
    }
    return IoResult(value: 0, err: SysErr.None, os: 4321)
}

FSize(io: Io, h: uint): IoResult {
    if FFails(2) {
        return FFail()
    }
    if FFails(10) || FFails(11) {
        return FOk(0)
    }
    return FOk(Fault.left.Load())
}

FClose(io: Io, h: uint): IoResult {
    if FFails(8) {
        return FFail()
    }
    b := FBase(io) else {
        return FOk(0)
    }
    return IoClose(b, h)
}

FSync(io: Io, h: uint): IoResult {
    if FFails(8) {
        return FFail()
    }
    return FOk(0)
}

// FCount is a read's or a write's count by the mode, for n bytes asked.
FCount(n: int, reading: bool): i64 {
    m := Fault.mode.Load()
    if m == 4 {
        return i64(n) + 1
    }
    if m == 5 {
        return -1
    }
    if m == 7 {
        return 0
    }
    var k := i64(n)
    if m == 6 {
        if reading && k > 3 {
            k = 3
        }
        if !reading && k > 2 {
            k = 2
        }
    }
    if reading {
        left := Fault.left.Load()
        if k > left {
            k = left
        }
        Fault.left.Store(left - k)
    }
    return k
}

// FFill writes the stream's bytes n bytes into buf: 'a', and '\n' at every
// fifth stream position.
FFill(buf: mut []u8, n: i64) {
    for i in 0..int(n) {
        at := Fault.at.Load()
        Fault.at.Store(at + 1)
        if at % 5 == 4 {
            buf[i] = 0x0A
        } else {
            buf[i] = 0x61
        }
    }
}

FRead(io: Io, h: uint, buf: mut []u8, pos: i64): IoResult {
    _ = Fault.calls.Add(1)
    if FFails(3) || (FFails(12) && pos >= 0) {
        return FFail()
    }
    n := FCount(buf.Len(), true)
    if n > 0 && n <= i64(buf.Len()) && !FFails(11) {
        FFill(mut buf[..], n)
    }
    return FOk(n)
}

FRecv(io: Io, h: uint, buf: mut []u8): IoResult {
    return FRead(io, h, mut buf[..], -1)
}

FWrite(io: Io, h: uint, data: []u8, pos: i64): IoResult {
    _ = Fault.calls.Add(1)
    if FFails(3) || (FFails(12) && pos >= 0) {
        return FFail()
    }
    return FOk(FCount(data.Len(), false))
}

FSend(io: Io, h: uint, data: []u8): IoResult {
    return FWrite(io, h, data, -1)
}
`

// faultFs and faultNet are the fixtures fs/zfault.mk and net/zfault.mk:
// the scripted Io for a program, which cannot import std.core.
const faultFs = `import std.core
import std.sys

export FaultIo(mode: u32, err: u32, os: i64, left: i64): Io {
    return core.FaultIo(mode, err, os, left)
}

export FaultCalls(): i64 {
    return core.FaultCalls()
}

export FaultReal(base: Io) {
    core.FaultReal(base)
}

export ArenaTaken(a: &Arena): int {
    return core.ArenaTaken(a)
}

export Handles(): int {
    return sys.HandleCount()
}
`

const faultNet = `import std.core

export FaultIo(mode: u32, err: u32, os: i64, left: i64): Io {
    return core.FaultIo(mode, err, os, left)
}
`

// faultProgram runs one operation (args[1] the module, fs or net, args[2]
// the operation) over the scripted Io (args[3] to args[6]: mode, error
// index, os, bytes left) and prints "ok" and a
// count, or the error's name; then " calls <n>", the table's read and
// write calls.
const faultProgram = `import std.fs
import std.net
import std.time

FsName(e: fs.Error|OutOfMemory|WriteError): str {
    match e {
        fs.NotFound {
            return "NotFound"
        }
        fs.Exists {
            return "Exists"
        }
        fs.Denied {
            return "Denied"
        }
        fs.IsDirectory {
            return "IsDirectory"
        }
        fs.NotDirectory {
            return "NotDirectory"
        }
        fs.NoSpace {
            return "NoSpace"
        }
        fs.InvalidPath {
            return "InvalidPath"
        }
        fs.Unsupported {
            return "Unsupported"
        }
        Utf8Error {
            return "Utf8Error"
        }
        fs.Os(code: c) {
            if c == 0 {
                return "Os 0"
            }
            return "Os 77"
        }
        OutOfMemory {
            return "OutOfMemory"
        }
        Canceled {
            return "Canceled"
        }
        WriteError {
            return "WriteError"
        }
        _ {
            Panic("fs.Error gained a member this match does not handle")
        }
    }
}

NetName(e: net.Error|OutOfMemory|WriteError|ReadError): str {
    match e {
        net.Refused {
            return "Refused"
        }
        net.Reset {
            return "Reset"
        }
        net.AddrInUse {
            return "AddrInUse"
        }
        net.Unreachable {
            return "Unreachable"
        }
        net.InvalidAddress {
            return "InvalidAddress"
        }
        net.Unsupported {
            return "Unsupported"
        }
        net.SecureFailed {
            return "SecureFailed"
        }
        time.TimedOut {
            return "TimedOut"
        }
        net.Os(code: c) {
            if c == 0 {
                return "Os 0"
            }
            return "Os 77"
        }
        Canceled {
            return "Canceled"
        }
        Closed {
            return "Closed"
        }
        OutOfMemory {
            return "OutOfMemory"
        }
        WriteError {
            return "WriteError"
        }
        ReadError {
            return "ReadError"
        }
        _ {
            Panic("net.Error gained a member this match does not handle")
        }
    }
}

Fs(sys: &System, io: Io, op: str): fs.Error|OutOfMemory|WriteError!int {
    data := "0123456789".AsBytes()
    if op == "readbytes" {
        b := try fs.ReadBytes(sys.arena, io, "x")
        return b.Len()
    }
    if op == "writeall" {
        try fs.WriteAll(io, "x", data)
        return 0
    }
    if op == "open" {
        f := try fs.Open(io, "x")
        f.Abandon()
        return 0
    }
    var f := try fs.Create(io, "x")
    onerr f.Abandon()
    var buf: [8]u8 = [0; 8]
    var n := 0
    if op == "read" {
        n = try f.Read(io, mut buf[..])
        n += try f.Read(io, mut buf[..])
    } else if op == "write" {
        n = try f.Write(io, data)
    } else if op == "readat" {
        n = try f.ReadAt(io, 5, mut buf[..])
    } else if op == "writeat" {
        n = try f.WriteAt(io, 5, data)
    } else if op == "fwriteall" {
        try f.WriteAll(io, data)
    } else if op == "writer" {
        var wb: [4]u8 = [0; 4]
        var w := f.Writer(io, mut wb[..])
        try w.Bytes(data)
        try w.Flush()
    } else if op == "sync" {
        try f.Sync(io)
    }
    try f.Close(io)
    return n
}

Net(sys: &System, io: Io, op: str): net.Error|OutOfMemory|WriteError|ReadError!int {
    data := "0123456789".AsBytes()
    if op == "listen" {
        l := try net.Listen(io, ":0")
        port := l.Port()
        l.Close()
        return port
    }
    if op == "accept" {
        l := try net.Listen(io, ":0")
        defer l.Close()
        c := try l.Accept(io)
        c.Close()
        return 0
    }
    c := try net.Dial(io, "127.0.0.1:1")
    defer c.Close()
    var buf: [8]u8 = [0; 8]
    if op == "recv" {
        return try c.Read(io, mut buf[..])
    }
    if op == "send" {
        return try c.Write(io, data)
    }
    if op == "sendall" {
        try c.WriteAll(io, data)
        return 0
    }
    if op == "reader" {
        var rb: [4]u8 = [0; 4]
        var r := c.Reader(io, mut rb[..])
        var got := try r.Read(mut buf[..])
        got += try r.Read(mut buf[..])
        return got
    }
    if op == "line" {
        var r := c.Reader(io, mut buf[0..0])
        var lines := 0
        for {
            l := try r.Line(sys.arena) else {
                break
            }
            if l != "aaaa" {
                return -1
            }
            lines += 1
        }
        return lines
    }
    if op == "writer" {
        var wb: [4]u8 = [0; 4]
        var w := c.Writer(io, mut wb[..])
        try w.Bytes(data)
        try w.Flush()
        return 0
    }
    return 0
}

export Main(sys: &System): WriteError|ParseError!void {
    var out := sys.Stdout()
    op := sys.args[2]
    mode := Trunc[u32](try sys.args[3].ParseInt())
    err := Trunc[u32](try sys.args[4].ParseInt())
    os := try sys.args[5].ParseInt()
    left := try sys.args[6].ParseInt()
    if sys.args[1] == "net" {
        io := net.FaultIo(mode, err, os, left)
        n := Net(sys, io, op) else e {
            name := NetName(e)
            calls := fs.FaultCalls()
            try Print(mut out, "{name} calls {calls}\n", (name, calls))
            return
        }
        calls := fs.FaultCalls()
        try Print(mut out, "ok {n} calls {calls}\n", (n, calls))
        return
    }
    io := fs.FaultIo(mode, err, os, left)
    n := Fs(sys, io, op) else e {
        name := FsName(e)
        calls := fs.FaultCalls()
        try Print(mut out, "{name} calls {calls}\n", (name, calls))
        return
    }
    calls := fs.FaultCalls()
    try Print(mut out, "ok {n} calls {calls}\n", (n, calls))
}
`

// faultCase is one run: the operation, the script and the expected line.
type faultCase struct {
	op                  string
	mode, err, os, left int
	want                string
}

// sysErrNames is core_iodecl.mk's SysErr in declaration order.
var sysErrNames = []string{"None", "NotFound", "Exists", "Denied", "IsDirectory", "NotDirectory", "NoSpace",
	"InvalidPath", "Canceled", "Closed", "Refused", "Reset", "AddrInUse", "Unreachable", "InvalidAddress", "Other", "Unsupported"}

// fsMember and netMember are 4.12's mapping, the reference model: the
// member of the same name when the set has one, else Os with the code.
func fsMember(k int) string {
	switch n := sysErrNames[k]; n {
	case "NotFound", "Exists", "Denied", "IsDirectory", "NotDirectory", "NoSpace", "InvalidPath", "Canceled", "Unsupported":
		return n
	}
	return "Os 77"
}

func netMember(k int) string {
	switch n := sysErrNames[k]; n {
	case "Refused", "Reset", "AddrInUse", "Unreachable", "InvalidAddress", "Canceled", "Closed", "Unsupported":
		return n
	}
	return "Os 77"
}

func faultCases() []faultCase {
	var cs []faultCase
	// Every SysErr but None through fs.Open, ReadBytes, net.Dial, recv and
	// Listener.Accept after a listen that succeeded (the mapping).
	for k := 1; k < len(sysErrNames); k++ {
		// Dial maps a failed name lookup to InvalidAddress before the common mapping.
		dial := netMember(k)
		if sysErrNames[k] == "NotFound" {
			dial = "InvalidAddress"
		}
		cs = append(cs,
			faultCase{"open", 1, k, 77, 0, fsMember(k) + " calls 0"},
			faultCase{"readbytes", 3, k, 77, 10, fsMember(k) + " calls 1"},
			faultCase{"net.recv", 3, k, 77, 10, netMember(k) + " calls 1"},
			faultCase{"net.dial", 1, k, 77, 0, dial + " calls 0"},
			faultCase{"net.accept", 9, k, 77, 0, netMember(k) + " calls 0"})
	}
	cs = append(cs,
		// fs.ReadBytes: size hint + 1, so 10 bytes are one read and the
		// end-of-input read; partial reads loop; an unknown size (0) still
		// reads all; out-of-range counts and a failed size are errors.
		faultCase{"readbytes", 0, 0, 0, 10, "ok 10 calls 2"},
		faultCase{"readbytes", 6, 0, 0, 10, "ok 10 calls 5"},
		faultCase{"readbytes", 6, 0, 0, 0, "ok 0 calls 1"},
		faultCase{"readbytes", 4, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"readbytes", 5, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"readbytes", 2, 3, 0, 10, "Denied calls 0"},
		// fs.WriteAll: partial writes loop; no progress, too much, a failed
		// write and a failed close are errors.
		faultCase{"writeall", 0, 0, 0, 0, "ok 0 calls 1"},
		faultCase{"writeall", 6, 0, 0, 0, "ok 0 calls 5"},
		faultCase{"writeall", 7, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"writeall", 4, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"writeall", 5, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"writeall", 3, 8, 0, 0, "Canceled calls 1"},
		faultCase{"writeall", 8, 6, 0, 0, "NoSpace calls 1"},
		// File: Read twice over 8 bytes with 10 left; Write and the
		// positioned calls are one call; out-of-range counts are Os(0).
		faultCase{"read", 0, 0, 0, 10, "ok 10 calls 2"},
		faultCase{"read", 6, 0, 0, 10, "ok 6 calls 2"},
		faultCase{"read", 4, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"read", 5, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"write", 0, 0, 0, 0, "ok 10 calls 1"},
		faultCase{"write", 6, 0, 0, 0, "ok 2 calls 1"},
		faultCase{"write", 4, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"write", 5, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"readat", 4, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"readat", 0, 0, 0, 10, "ok 8 calls 1"},
		faultCase{"writeat", 5, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"writeat", 6, 0, 0, 0, "ok 2 calls 1"},
		faultCase{"fwriteall", 6, 0, 0, 0, "ok 0 calls 5"},
		faultCase{"fwriteall", 7, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"fwriteall", 4, 0, 0, 0, "Os 0 calls 1"},
		// File.Writer over 4 bytes: with the buffer empty, 10 bytes go to
		// the table whole (bufio's rule), looped over partial writes.
		faultCase{"writer", 0, 0, 0, 0, "ok 0 calls 1"},
		faultCase{"writer", 6, 0, 0, 0, "ok 0 calls 5"},
		faultCase{"writer", 7, 0, 0, 0, "WriteError calls 1"},
		faultCase{"writer", 4, 0, 0, 0, "WriteError calls 1"},
		faultCase{"writer", 3, 11, 0, 0, "WriteError calls 1"},
		faultCase{"sync", 8, 3, 0, 0, "Denied calls 0"},
		// std.net: the listener's port from IoResult.os.
		faultCase{"net.listen", 0, 0, 0, 0, "ok 4321 calls 0"},
		faultCase{"net.listen", 1, 12, 0, 0, "AddrInUse calls 0"},
		faultCase{"net.listen", 1, 8, 0, 0, "Canceled calls 0"},
		faultCase{"net.accept", 0, 0, 0, 0, "ok 0 calls 0"},
		faultCase{"net.recv", 0, 0, 0, 10, "ok 8 calls 1"},
		faultCase{"net.recv", 0, 0, 0, 0, "ok 0 calls 1"},
		faultCase{"net.recv", 4, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"net.recv", 5, 0, 0, 10, "Os 0 calls 1"},
		faultCase{"net.send", 6, 0, 0, 0, "ok 2 calls 1"},
		faultCase{"net.send", 4, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"net.sendall", 6, 0, 0, 0, "ok 0 calls 5"},
		faultCase{"net.sendall", 7, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"net.sendall", 5, 0, 0, 0, "Os 0 calls 1"},
		faultCase{"net.sendall", 3, 11, 0, 0, "Reset calls 1"},
		// Stream.Reader over 4 bytes, two Reads into 8: 8 >= 4 goes
		// straight to recv (bufio's rule) both times.
		faultCase{"net.reader", 0, 0, 0, 10, "ok 10 calls 2"},
		faultCase{"net.reader", 6, 0, 0, 10, "ok 6 calls 2"},
		faultCase{"net.reader", 4, 0, 0, 10, "ReadError calls 1"},
		faultCase{"net.reader", 5, 0, 0, 10, "ReadError calls 1"},
		faultCase{"net.reader", 3, 9, 0, 10, "ReadError calls 1"},
		// Reader.Line with no inline buffer: ReadLineBytewise, one recv per
		// byte; 15 bytes are 3 lines of "aaaa" and the end-of-stream recv.
		faultCase{"net.line", 0, 0, 0, 15, "ok 3 calls 16"},
		faultCase{"net.line", 4, 0, 0, 15, "ReadError calls 1"},
		faultCase{"net.line", 5, 0, 0, 15, "ReadError calls 1"},
		// Stream.Writer over 4 bytes: 10 bytes go to SockWrite whole, which
		// loops over partial sends.
		faultCase{"net.writer", 0, 0, 0, 0, "ok 0 calls 1"},
		faultCase{"net.writer", 6, 0, 0, 0, "ok 0 calls 5"},
		faultCase{"net.writer", 7, 0, 0, 0, "WriteError calls 1"},
		faultCase{"net.writer", 4, 0, 0, 0, "WriteError calls 1"},
	)
	return cs
}

// TestStdIo_FaultInjection runs every faultCases line over the scripted
// Io and compares the program's line with the reference model's.
func TestStdIo_FaultInjection(t *testing.T) { stdIoFault(t, hostIo) }

func stdIoFault(t *testing.T, h ioHost) {
	exe := h.build(t, map[string]string{"core/io_zfault.mk": faultCore, "fs/zfault.mk": faultFs, "net/zfault.mk": faultNet}, faultProgram)
	cs := faultCases()
	calls := make([]ioCall, len(cs))
	for i, c := range cs {
		mod, op := "fs", c.op
		if strings.HasPrefix(op, "net.") {
			mod, op = "net", op[4:]
		}
		calls[i] = ioCall{args: []string{mod, op, strconv.Itoa(c.mode), strconv.Itoa(c.err), strconv.Itoa(c.os), strconv.Itoa(c.left)}}
	}
	for i, r := range h.runMany(t, exe, calls) {
		c := cs[i]
		got := strings.TrimRight(r.stdout, "\r\n")
		if r.code != 0 || got != c.want {
			t.Errorf("%s mode %d err %s: exit %d, %q, want %q%s", c.op, c.mode, sysErrNames[c.err], r.code, got, c.want, faultTail(r))
		}
	}
}

func faultTail(r nativeRun) string {
	if r.code == 0 {
		return ""
	}
	return fmt.Sprintf("\nstderr: %s", stdioTail(r.stderr))
}
