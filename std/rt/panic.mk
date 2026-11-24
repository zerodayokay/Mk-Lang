// Why: every panic, from any thread, must end the process with one readable
// report and exit status 101, even when the report itself panics.
//
// What: the panic hooks PanicAt, PanicMsg and PanicErr, PanicText, Report,
// Reporting and the report writer, with the Digits and Put formatting helpers.
//
// How: the first panicking thread wins a compare-and-swap on Panics and
// reports; other threads park for good, and a nested panic on the winner's
// thread replays the kept report. Nothing unwinds: once a panic starts, no
// defer, onerr or other task's code runs.

import std.core
import std.raw
import std.sys

// The protocol words (who reports, and how far), and the first report's message (three
// pieces, concatenated) and site, kept so a nested panic on the same
// thread can print the first report.
struct PanicWords {
    panicking: raw.Atomic[u64]
    stage: raw.Atomic[u32]
    site: raw.Atomic[u64]
    ptr: [3]raw.Atomic[u64]
    len: [3]raw.Atomic[u64]
    // code is the panic code the hook receives (hook.mk): set by Report and
    // by a stack-limit fault before Reporting, 0 for any other fault.
    code: raw.Atomic[u32]
}

var Panics: PanicWords = PanicWords(panicking: raw.Atomic[u64].New(0), stage: raw.Atomic[u32].New(0),
    site: raw.Atomic[u64].New(0), ptr: [raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0)],
    len: [raw.Atomic[u64].New(0), raw.Atomic[u64].New(0), raw.Atomic[u64].New(0)], code: raw.Atomic[u32].New(0))

/// PanicText is the message of a PanicCode: exactly types.PanicCode.String()
/// (the test RT-F8-PanicTextIsCodeName compares them).
/// O(1); allocates nothing (the texts are constants).
export PanicText(code: u32): str {
    return match code {
        1 { "overflow" }
        2 { "bounds" }
        3 { "divide by zero" }
        4 { "shift amount" }
        5 { "cast" }
        6 { "must" }
        7 { "assert" }
        8 { "explicit" }
        9 { "stack limit" }
        10 { "str boundary" }
        11 { "free non-empty" }
        12 { "wrong allocator" }
        13 { "enum tag" }
        14 { "contract" }
        15 { "unreachable" }
        16 { "suspend in callback" }
        _ { "panic" }
    }
}

/// PanicStackLimit is the code of a stack overflow.
export const PanicStackLimit: u32 = 9
/// PanicExplicit is the code of a `panic` statement.
export const PanicExplicit: u32 = 8
/// PanicSuspendInCallback is the code of a suspension inside a callback
/// that must not suspend.
export const PanicSuspendInCallback: u32 = 16

/// PanicAt is the std.rt.Panic hook: `Panic` is a universe name (E0102),
/// so the key is std.rt.PanicAt. It reports the code's text.
/// Its cost is the flush of the standard streams and the report's writes;
/// allocates nothing. Never returns: the process exits with 101 (Reporting).
export PanicAt(code: u32, site: &Site): never {
    unsafe "site is a static site constant a trap block passes, alive for the whole process" {
        Report(code, raw.Of(site), PanicText(code), "")
    }
}

/// PanicMsg reports a panic with a message: msg alone for code 8 (explicit),
/// else "<text>: <msg>".
/// Its cost is the flush of the standard streams and the report's writes;
/// allocates nothing. Never returns: the process exits with 101 (Reporting).
export PanicMsg(code: u32, site: &Site, msg: str): never {
    unsafe "site is a static site constant a trap block passes, alive for the whole process" {
        if code == PanicExplicit {
            Report(code, raw.Of(site), msg, "")
        }
        Report(code, raw.Of(site), PanicText(code), msg)
    }
}

/// PanicErr is PanicMsg with the error's display name as the message.
/// Its cost is the flush of the standard streams and the report's writes;
/// allocates nothing. Never returns: the process exits with 101 (Reporting).
export PanicErr(code: u32, site: &Site, err: u32): never {
    PanicMsg(code, site, ErrorName(err))
}

// Keep stores the first report's pieces.
Keep(site: *Site, a: str, b: str, c: str) {
    // Edge: the pieces are stored before stage 1 (Reporting), and a nested
    // panic on this thread reads them only after it reads stage 1.
    Panics.site.Store(u64(raw.Addr(site)))
    Panics.ptr[0].Store(u64(raw.Addr(raw.StrData(a))))
    Panics.len[0].Store(Trunc[u64](a.Len()))
    Panics.ptr[1].Store(u64(raw.Addr(raw.StrData(b))))
    Panics.len[1].Store(Trunc[u64](b.Len()))
    Panics.ptr[2].Store(u64(raw.Addr(raw.StrData(c))))
    Panics.len[2].Store(Trunc[u64](c.Len()))
}

// Kept is piece i of the first report.
Kept(i: int): str {
    unsafe "the pieces Keep stored; their frames never resume" {
        // Edge: Keep's stores, before the stage store this reader saw.
        return raw.Str(raw.FromAddr[u8](Trunc[uint](Panics.ptr[i].Load())), Trunc[int](Panics.len[i].Load()))
    }
}

/// Report records the panic code and starts Reporting with text + ": " + msg
/// (or text alone when msg is empty).
/// Its cost is the flush of the standard streams and the report's writes;
/// allocates nothing. Never returns: the process exits with 101 (Reporting).
export Report(code: u32, site: *Site, text: str, msg: str): never {
    // Edge: a racing panic of another thread may overwrite it; only the
    // winner's hook reads it, and a lost race costs only the code it shows.
    Panics.code.Store(code)
    if msg.Len() == 0 {
        Reporting(site, text, "", "")
    }
    Reporting(site, text, ": ", msg)
}

/// Reporting reports a panic with the message a + b + c: the first
/// panicking thread flushes and reports; a second panic on that thread
/// writes the kept report and exits; any other thread parks for good.
/// Writes from a fixed buffer and allocates nothing. Never returns.
export Reporting(site: *Site, a: str, b: str, c: str): never {
    me := sys.ThreadId()
    // Edge: the first reporter wins this compare-and-swap; every
    // other thread sees a nonzero owner and parks. A nested panic on the
    // winner's own thread reads its own id and stage (program order).
    if !Panics.panicking.Cas(0, me) {
        if Panics.panicking.Load() == me {
            // A panic or fault inside this thread's own report.
            if Panics.stage.Load() == 1 {
                unsafe "the first report's site, or null" {
                    WriteReport(raw.FromAddr[Site](Trunc[uint](Panics.site.Load())), Kept(0), Kept(1), Kept(2))
                }
            }
            sys.FatalExit(101)
        }
        // Another thread reports and exits the process: park for good.
        var s := sys.NewSem()
        _ = sys.SemInit(mut s)
        for {
            _ = sys.SemWait(&s, -1)
            sys.ThreadYield()
        }
    }
    Keep(site, a, b, c)
    // Edge: stage 1 after Keep (a nested panic replays the kept report), stage
    // 2 before the report itself (a nested panic then only exits).
    Panics.stage.Store(1)
    if !raw.IsNull(R()) {
        core.TryFlushStd(Proc(), DirectIo())
    }
    // The terminal is put back (raw mode off, the alternate screen
    // left) before the report, so the report is readable.
    TermExitHook()
    // The program's MkPanicHook, once, before the report; a panic
    // inside it is nested (stage 1) and replays the kept report.
    // Edge: the winner of the panicking compare-and-swap reads the code it
    // or a racing thread stored before Reporting (program order on this
    // thread; a racing store costs only the code the hook sees).
    CallPanicHook(Panics.code.Load(), site, a, b, c)
    // Edge: program order only: stage is read on this thread alone (a nested
    // panic here, past the Load of panicking == me), so stage 2 is set
    // before the report and that panic then only exits.
    Panics.stage.Store(2)
    WriteReport(site, a, b, c)
    // The backtrace: the frames after the first line, when
    // MK_BACKTRACE asks (backtrace.mk); a fault in the walk is a nested
    // panic at stage 2, which only exits, after the line above.
    if BacktraceWanted() {
        WriteBacktrace()
    }
    sys.FatalExit(101)
}

/// Digits writes n in decimal at buf[at..] and returns the new end
/// (counting on when it does not fit, as Put does).
/// O(digits); allocates nothing.
export Digits(buf: mut []u8, at: int, var n: u64): int {
    // The digits are counted first and written from the last, so no
    // buffer is reversed: the loops divide and cannot be vectorized, which
    // keeps every program's report code small (hello's size budget).
    // n has at most 20 digits.
    var end := at +% 1
    var m := n / 10
    for m != 0 {
        end = end +% 1
        m = m / 10
    }
    // i runs from end - 1 down to at, which end's count checked.
    var i := end
    for {
        i = i -% 1
        if i >= 0 && i < buf.Len() {
            buf[i] = Trunc[u8](48 + n % 10)
        }
        n = n / 10
        if n == 0 {
            return end
        }
    }
}

/// Put copies s into buf at at and returns the new end (counting on when it
/// does not fit).
/// O(len(s)); allocates nothing.
export Put(buf: mut []u8, at: int, s: str): int {
    b := s.AsBytes()
    for i in 0..b.Len() {
        if at + i < buf.Len() {
            buf[at + i] = b[i]
        }
    }
    return at + b.Len()
}

// WriteReport writes "panic: " a b c " at <file>:<line>:<col>\n" (or no
// site): in one write when it fits 1,024 bytes, else in order, never
// truncated.
WriteReport(site: *Site, a: str, b: str, c: str) {
    var pos: [64]u8 = [0; 64]
    var n := 0
    var file := ""
    if !raw.IsNull(site) {
        unsafe "site is not null: a static site constant, alive for the whole process" {
            file = (*site).file
            n = Put(mut pos[..], 0, ":")
            n = Digits(mut pos[..], n, u64((*site).line))
            n = Put(mut pos[..], n, ":")
            n = Digits(mut pos[..], n, u64((*site).col))
        }
    }
    n = Put(mut pos[..], n, "\n")
    var line: [1024]u8 = [0; 1024]
    var m := Put(mut line[..], 0, "panic: ")
    m = Put(mut line[..], m, a)
    m = Put(mut line[..], m, b)
    m = Put(mut line[..], m, c)
    if !raw.IsNull(site) {
        m = Put(mut line[..], m, " at ")
        m = Put(mut line[..], m, file)
    }
    for i in 0..n {
        if m + i < 1024 {
            line[m + i] = pos[i]
        }
    }
    m += n
    if m <= 1024 {
        sys.FatalWrite(line[..m])
        return
    }
    sys.FatalWrite("panic: ".AsBytes())
    sys.FatalWrite(a.AsBytes())
    sys.FatalWrite(b.AsBytes())
    sys.FatalWrite(c.AsBytes())
    if !raw.IsNull(site) {
        sys.FatalWrite(" at ".AsBytes())
        sys.FatalWrite(file.AsBytes())
    }
    sys.FatalWrite(pos[..n])
}
