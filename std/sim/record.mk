// Why: a failure seen once in a real run, which read the clock, the random
// source, files and sockets, should rerun with the same answers and no OS
// behind it, so it can be debugged.
//
// What: Recorder (New, Io, Log, Free), an Io over another that logs every
// call at the Io boundary, and Replayer (New, Io, Diverged, Done, Free), an
// Io that answers from such a log; the log format and its record ops.
//
// How: one record per call, keyed by op and an FNV-1a digest of the call's
// inputs; a replayed call that does not match the next record diverges the
// replay. Both types are linear, keep their state on std's heap as a Sim
// does, and every Io they made answers Canceled once freed.
//
// Log format, one record per call, little-endian:
//   op u8 | digest u64 | value i64 | err u64 | os i64 | n u64 | n bytes
// digest is FNV-1a 64 over the call's inputs other than its handle (paths,
// data, positions, sizes, addresses); a handle, in the digest and as a
// call's new handle, is logged as its number in the recording (1 for the
// first it made), and a replay maps its own handles back to those. The
// n bytes are the call's outputs: the bytes a read or receive filled, the
// random bytes, a stat or readdir record, the resolved addresses, the
// address a datagram came from. A replayed call whose op or digest differs
// from the next record is divergence: it and every later call answer
// Canceled, and Diverged reports it.
//
// Three kinds of record are not a table entry's:
// - ODeadline, a scope's deadline armed through the Io (tape.mk's
//   TDeadline): the digest is over the nanoseconds and the scope's arm
//   number (the first scope armed through the tape is 1), value the
//   deadline the scope then holds on the inner Io's clock (-1 for none:
//   the inner Io has no clock), err the inner entry's, os 1 when the
//   scope's own deadline cancelled it during the arm (a d <= 0 fires at
//   once), else 0.
// - OFire, a listed scope's deadline that fired: value its arm number,
//   os 1 when a futex wait logged it, else 0, no payload. A Recorder logs
//   it before the first record it writes after the fire set the scope's
//   flag (os 0), and at a futex wait (TWait), on entry or when the wait
//   ends Canceled (os 1); several fires logged at once go in the order of
//   their instants. A Replayer fires the scope's deadline when the log
//   holds the record next, before it answers the call that meets it, and
//   a futex wait's entry fires only the os 1 ones, so a fire logged before
//   a later call's record is not replayed at an earlier wait.
// - OCall + op, a call op the tape replays (tape.mk's TapeOp): the digest
//   is over the op, its route, its p and q bytes, its out length and its
//   four words; the payload is the four words after the call (32 bytes),
//   then the out bytes it wrote (value of them for ReadLink, Cwd,
//   Absolute and TimeZone).
//
// A replayed handle is a fresh foreign runtime record with no ops
// (disk.mk's MarkForeign), so rt.Abandon and Close stay safe with no OS handle behind it.
// The tape's own slot lock is released while a recorded call runs, so a
// call that blocks (accept, receive) blocks no other task of the recorder.

import std.core
import std.hash
import std.raw
import std.rt
import std.task

const RecordHeader: int = 41

// The ops, by IoTable order.
const OOpen: u8 = 0
const ORead: u8 = 1
const OWrite: u8 = 2
const OSize: u8 = 3
const OSync: u8 = 4
const OClose: u8 = 5
const ORename: u8 = 6
const ORemove: u8 = 7
const OListen: u8 = 8
const OAccept: u8 = 9
const ODial: u8 = 10
const ORecv: u8 = 11
const OSend: u8 = 12
const ONow: u8 = 13
const OSleep: u8 = 14
const ORandom: u8 = 15
const OYield: u8 = 16
const OSignal: u8 = 17
const OWait: u8 = 18
const OMkdir: u8 = 20
const OStat: u8 = 21
const OOpenDir: u8 = 22
const OReadDir: u8 = 23
const OUdp: u8 = 24
const OSendTo: u8 = 25
const ORecvFrom: u8 = 26
const OResolve: u8 = 27
const OSpawn: u8 = 28
const OProcWait: u8 = 29
const OProcKill: u8 = 30
const OInherited: u8 = 31
// OWall is the call entry's wall clock (core.IoOpWall), an input as now is.
const OWall: u8 = 32
// ODeadline is a deadline armed through the tape, OFire a listed scope's
// deadline that fired.
const ODeadline: u8 = 33
const OFire: u8 = 34
const OUdpConnect: u8 = 35
const OTryRecvFrom: u8 = 36
// OCall + op is the call op op (an IoOp below 192) that the tape replays.
const OCall: u8 = 64

// CallTag is the record op of the call op op.
CallTag(op: u32): u8 {
    return OCall + Trunc[u8](op)
}

// WordsSize is a call record's payload of words: a, b, c and d.
const WordsSize: int = 32

// DlRec is a scope a tape armed a deadline for, listed until its Join
// disarms it: rec its record's address, num its arm number in the log, at
// the instant it holds (-1 until a Recorder's arm has read it), fired once
// its fire is logged (a Recorder) or replayed (a Replayer), arming while a
// Recorder's arm runs on the inner Io, so a fire inside the arm goes in
// the arm's record, never in an OFire before it (LogFires skips it).
struct DlRec {
    rec: uint
    num: u64
    at: i64
    fired: bool
    arming: bool
}

// TapeMaxInstant is the latest instant a log may hold for a deadline or a
// clock reading: std.task saturates a deadline below i64's maximum, so a
// Remaining over two logged instants never overflows. A larger one, or a
// negative one, is a log no run wrote: the replay diverges.
const TapeMaxInstant: i64 = 0x7FFF_FFFF_FFFF_FFFE

// Handle effects of a call, for Take.
const HNone: int = 0
const HNew: int = 1
const HClose: int = 2

struct HPair {
    rec: uint
    live: uint
}

// tasks counts the tasks born under the tape's Io that have not ended
// (their born and last hooks); dls lists the scopes whose deadline a
// Recorder armed on its inner Io, or a Replayer recorded from the log, and
// whose Join has not disarmed it yet (tape.mk's TDeadline), and arms
// counts the arm numbers given out; freed marks a Recorder freed while
// some of either were left: its slot then stays, for their hooks and
// disarms only, until the last is gone (TapeGone).
linear struct Tape {
    inner: Io
    replay: bool
    log: Vec[u8]
    pos: int
    diverged: bool
    hmap: Vec[HPair]
    made: u64
    tasks: int
    dls: Vec[DlRec]
    arms: u64
    freed: bool
}

// TapeGone: a freed Recorder's slot may go: no armed deadline is left to
// disarm through it, and no task is left whose hooks reach a scheduler
// behind it (a native inner Io has none).
TapeGone(t: &Tape): bool {
    return t.freed && t.dls.Len() == 0 && (t.tasks <= 0 || core.IsNative(t.inner))
}

/// An Io over another Io that forwards every call and logs its inputs'
/// digest and its outputs, so a Replayer can answer the same calls later.
/// It is linear: end it with Free.
export linear struct Recorder {
    slot: int
    t: *Tape
}

/// An Io that answers every call from a Recorder's log, with no OS behind
/// it. It is linear: end it with Free.
export linear struct Replayer {
    slot: int
    t: *Tape
}

// ---------------------------------------------------------------------------
// The digest.

const FnvSeed: u64 = 0xCBF29CE484222325
const FnvPrime: u64 = 0x100000001B3

FnvBytes(d: u64, b: []u8): u64 {
    var h := d
    for i in 0..b.Len() {
        h = (h ^ u64(b[i])) *% FnvPrime
    }
    return h
}

// Digest is the recorder's digest of a call's bulk input (written and
// sent bytes): XXH64 seeded by the digest so far (std.hash's
// Xx64, about an order of magnitude faster than a byte-at-a-time FNV-1a,
// whose serial multiply chain bounds it near 1 GB/s). FNV-1a stays for the
// few fixed-size fields (FnvU64, FnvI).
Digest(d: u64, b: []u8): u64 {
    return hash.Xx64(b, d)
}

FnvU64(d: u64, x: u64): u64 {
    var h := d
    for k in 0..8 {
        h = (h ^ ((x >> Trunc[u64](k * 8)) & 0xFF)) *% FnvPrime
    }
    return h
}

FnvI(d: u64, x: i64): u64 {
    return FnvU64(d, Trunc[u64](x))
}

FnvAddr(d: u64, a: core.IoAddr): u64 {
    var h := FnvBytes(d, a.ip[..])
    h = FnvU64(h, u64(a.port))
    if a.v6 {
        h = FnvU64(h, 1)
    }
    return FnvU64(h, u64(a.zone))
}

// ---------------------------------------------------------------------------
// Encoding.

PutU64(v: mut Vec[u8], x: u64): bool {
    var b: [8]u8 = [0; 8]
    for k in 0..8 {
        b[k] = Trunc[u8](x >> Trunc[u64](k * 8))
    }
    return AppendBytes(v, b[..])
}

GetU64(b: []u8, at: int): u64 {
    var x: u64 = 0
    for k in 0..8 {
        x = x | (u64(b[at + k]) << Trunc[u64](k * 8))
    }
    return x
}

// AddrBytes and StatBytes are the payload layouts of an IoAddr (23 bytes)
// and an IoStat (20 bytes).
const AddrSize: int = 23
const StatSize: int = 20

PutAddr(out: mut []u8, at: int, a: core.IoAddr) {
    for k in 0..16 {
        out[at + k] = a.ip[k]
    }
    out[at + 16] = Trunc[u8](a.port)
    out[at + 17] = Trunc[u8](a.port >> 8)
    var v6: u8 = 0
    if a.v6 {
        v6 = 1
    }
    out[at + 18] = v6
    for k in 0..4 {
        out[at + 19 + k] = Trunc[u8](a.zone >> Trunc[u32](k * 8))
    }
}

GetAddr(b: []u8, at: int): core.IoAddr {
    var ip: [16]u8 = [0; 16]
    for k in 0..16 {
        ip[k] = b[at + k]
    }
    var zone: u32 = 0
    for k in 0..4 {
        zone = zone | (u32(b[at + 19 + k]) << Trunc[u32](k * 8))
    }
    return core.IoAddr(ip: ip, port: u16(b[at + 16]) | (u16(b[at + 17]) << 8), v6: b[at + 18] == 1, zone: zone)
}

PutStat(out: mut []u8, at: int, s: core.IoStat) {
    for k in 0..4 {
        out[at + k] = Trunc[u8](s.kind >> Trunc[u32](k * 8))
    }
    for k in 0..8 {
        out[at + 4 + k] = Trunc[u8](Trunc[u64](s.size) >> Trunc[u64](k * 8))
        out[at + 12 + k] = Trunc[u8](Trunc[u64](s.modified) >> Trunc[u64](k * 8))
    }
}

GetStat(b: []u8, at: int): core.IoStat {
    var kind: u32 = 0
    for k in 0..4 {
        kind = kind | (u32(b[at + k]) << Trunc[u32](k * 8))
    }
    return core.IoStat(kind: kind, size: Trunc[i64](GetU64(b, at + 4)), modified: Trunc[i64](GetU64(b, at + 12)))
}

// ---------------------------------------------------------------------------
// The registry hold.

struct TEntry {
    slot: int
    t: *Tape
    ok: bool
}

EnterTape(io: Io): TEntry {
    e := EnterSlot(io)
    if e.ok && TapeOf(&e).freed {
        // A freed Recorder kept only for its tasks' hooks: no call reaches
        // its log, and every entry answers Canceled.
        UnlockSlot(e.slot)
        return TEntry(slot: 0, t: raw.Null[Tape](), ok: false)
    }
    return e
}

// EnterSlot is EnterTape for the hooks and the wake, which a freed
// Recorder still forwards: it locks io's slot and checks its token only.
EnterSlot(io: Io): TEntry {
    f := core.IoFlags(io)
    i := Cast[int](f >> 48)
    tok := u64((f >> 1) & uint(TokenMask))
    if i >= SlotCount || tok == 0 {
        return TEntry(slot: 0, t: raw.Null[Tape](), ok: false)
    }
    LockSlot(i)
    if SlotToken(i) != tok {
        UnlockSlot(i)
        return TEntry(slot: 0, t: raw.Null[Tape](), ok: false)
    }
    var t := raw.Null[Tape]()
    unsafe "a tape table's Io names a slot whose word holds its live Tape while the token matches" {
        t = raw.FromAddr[Tape](uint(SlotWorld(i)))
    }
    return TEntry(slot: i, t: t, ok: true)
}

TapeOf(e: &TEntry): mut Tape {
    unsafe "an ok entry holds its slot's lock, and the slot's tape lives until Free takes that lock" {
        return mut *e.t
    }
}

// Mode is what a call needs before it runs: whether the tape is live, if
// it replays, and the inner Io (with the caller's NoCancel bit).
struct Mode {
    ok: bool
    replay: bool
    inner: Io
}

ModeOf(io: Io): Mode {
    e := EnterTape(io)
    if !e.ok {
        return Mode(ok: false, replay: false, inner: core.MakeIo(raw.Of(&core.InertIo), 0))
    }
    t := TapeOf(&e)
    // The caller's mask as it is (core.IoMaskAs), not a new NoCancel at
    // this call's depth, so a boundary opened from the tape's Io is past
    // the inner Io's mask as it is past the caller's.
    m := Mode(ok: true, replay: t.replay, inner: core.IoMaskAs(t.inner, io))
    UnlockSlot(e.slot)
    return m
}

// HookMode is ModeOf for a hook or a wake: ok also for a freed Recorder
// whose tasks have not all ended, so they reach the inner Io until they do.
HookMode(io: Io): Mode {
    e := EnterSlot(io)
    if !e.ok {
        return Mode(ok: false, replay: false, inner: core.MakeIo(raw.Of(&core.InertIo), 0))
    }
    t := TapeOf(&e)
    m := Mode(ok: true, replay: t.replay, inner: t.inner)
    UnlockSlot(e.slot)
    return m
}

// LeaveMode is HookMode for a leave hook, which also counts the Recorder's
// tasks: x's born fact adds one, its last fact takes one away (std.task's
// hook facts). A freed Recorder whose last task just ended gives up
// its slot here, under the slot's lock, so no later hook finds a freed
// tape, and the tape's block is freed after the lock: the returned inner
// Io is a copy, which the caller forwards x to.
LeaveMode(io: Io, x: u64): Mode {
    e := EnterSlot(io)
    if !e.ok {
        return Mode(ok: false, replay: false, inner: core.MakeIo(raw.Of(&core.InertIo), 0))
    }
    t := TapeOf(&e)
    if t.replay {
        // Read before the unlock: a Replayer.Free may free t after it.
        m := Mode(ok: true, replay: true, inner: t.inner)
        UnlockSlot(e.slot)
        return m
    }
    fact := x & task.NoteBits
    if fact == task.BornBit {
        t.tasks += 1
    } else if fact == task.LastBit {
        t.tasks -= 1
    }
    m := Mode(ok: true, replay: false, inner: t.inner)
    gone := TapeGone(&*t)
    if gone {
        SetSlot(e.slot, 0, 0)
    }
    UnlockSlot(e.slot)
    if gone {
        FreeTape(e.t)
    }
    return m
}

// Post appends a recorded call; a recorder freed meanwhile logs nothing.
Post(io: Io, op: u8, h: uint, d: u64, r: core.IoResult, out: []u8) {
    var empty: [0]u8 = [0; 0]
    Post2(io, op, h, d, r, out, empty[..])
}

// Post2 appends a recorded call whose payload is a then b, so a caller
// with a small header and a large buffer copies neither into a temporary.
// The whole record is reserved before any byte is appended: out
// of memory leaves the log ending on a record boundary and marks the
// recording incomplete (t.diverged), which Recorder.Log then reports as
// OutOfMemory instead of handing out a cut log as if it were whole.
Post2(io: Io, op: u8, h: uint, d: u64, r: core.IoResult, a: []u8, b: []u8) {
    e := EnterTape(io)
    if !e.ok {
        return
    }
    t := TapeOf(&e)
    // A handle is logged as its number in the recording (1 for the first
    // it made), never as the runtime's record address, so one run's log is
    // the same bytes in every process (a replay maps either form).
    hn := LogHandle(&*t, h)
    var value := r.value
    if r.err == core.SysErr.None && MakesHandle(op) {
        t.made += 1
        live := Trunc[uint](Cast[u64](r.value))
        // A handle abandoned without a Close through this Io left its
        // pair; the runtime may reuse its record for this one.
        DropHandle(t, live)
        if t.hmap.Push(HPair(rec: Trunc[uint](t.made), live: live)) {
            value = Cast[i64](t.made)
        }
    }
    if r.err == core.SysErr.None && op == OClose {
        DropHandle(t, h)
    }
    LogFires(t, false)
    Append(t, op, FnvU64(d, u64(hn)), value, r.err, r.os, a, b)
    UnlockSlot(e.slot)
}

// Append writes one record, its header's digest dg as it is, under the
// tape's lock. The whole record is reserved before any byte is appended
// (Post2).
Append(t: mut Tape, op: u8, dg: u64, value: i64, err: core.SysErr, os: i64, a: []u8, b: []u8) {
    if t.diverged {
        return
    }
    n := a.Len() + b.Len()
    if !t.log.Reserve(t.log.Len() + RecordHeader + n) {
        t.diverged = true
        return
    }
    var hdr: [41]u8 = [0; 41]
    hdr[0] = op
    PutAt(mut hdr[..], 1, dg)
    PutAt(mut hdr[..], 9, Trunc[u64](value))
    PutAt(mut hdr[..], 17, u64(u32(err)))
    PutAt(mut hdr[..], 25, Trunc[u64](os))
    PutAt(mut hdr[..], 33, Cast[u64](n))
    // Reserved above: none of these can fail.
    _ = AppendBytes(mut t.log, hdr[..])
    _ = AppendBytes(mut t.log, a)
    _ = AppendBytes(mut t.log, b)
}

// FireDigest is an OFire record's digest: it has no inputs.
const FireDigest: u64 = 0x1D2B3C4D5E6F7081

// LogFires logs, for a Recorder under the tape's lock, an OFire record
// for each listed scope whose own deadline has set its flag since the
// last record: so the fire comes in the log before the record of
// any call that completed after it; os 1 when wait (a futex wait's,
// NoteFires). Several go deeper scope first, then by instant (a tie: the
// later arm first; FiresFirst): a descendant's fire before its
// ancestor's, whose walk would otherwise take its flag on replay, whatever
// order the inner Io fired them in. A scope still arming is skipped: its arm logs its own fire. A
// listed record is live: its Join takes it off the list under this lock
// first (TapeDrop). O(listed scopes squared); allocates the log's growth.
LogFires(t: mut Tape, wait: bool) {
    for {
        var best := -1
        for i in 0..t.dls.Len() {
            x := t.dls.Get(i)
            if x.fired || x.arming {
                continue
            }
            var hit := false
            unsafe "a listed scope record is live until its Join takes it off the list, under the lock the caller holds" {
                hit = task.DeadlineExpired(raw.FromAddr[u8](x.rec))
            }
            if hit && (best < 0 || FiresFirst(x, t.dls.Get(best))) {
                best = i
            }
        }
        if best < 0 {
            return
        }
        x := t.dls.Get(best)
        t.dls.Set(best, DlRec(rec: x.rec, num: x.num, at: x.at, fired: true, arming: false))
        var empty: [0]u8 = [0; 0]
        Append(t, OFire, FireDigest, Cast[i64](x.num), core.SysErr.None, if wait { 1 } else { 0 }, empty[..], empty[..])
    }
}

// FiresFirst: x's deadline is logged before y's, both fired (LogFires):
// the deeper scope first, so a logged descendant always replays before a
// logged ancestor's walk, whatever order the inner Io's timer fired them
// in (both fire in instant order, a Sim a tie in its list order;
// racing native carriers can set a descendant's flag before an earlier
// ancestor's, SIM-Tape-DepthFirst); then the earlier instant, and
// on a tie the later arm.
FiresFirst(x: DlRec, y: DlRec): bool {
    var dx: u32 = 0
    var dy: u32 = 0
    unsafe "listed scope records are live under the lock LogFires' caller holds" {
        dx = task.DeadlineDepth(raw.FromAddr[u8](x.rec))
        dy = task.DeadlineDepth(raw.FromAddr[u8](y.rec))
    }
    if dx != dy {
        return dx > dy
    }
    return x.at < y.at || (x.at == y.at && x.num > y.num)
}

// FindDl is the index of the scope record a in the tape's list, or -1.
FindDl(t: &Tape, a: uint): int {
    for i in 0..t.dls.Len() {
        if t.dls.Get(i).rec == a {
            return i
        }
    }
    return -1
}

// ReplayFires fires, for a Replayer under the tape's lock, the deadlines
// of the OFire records the log holds next, before the call that
// meets them is answered: each names a scope the replay listed when it
// took that scope's arm record. At a futex wait's entry (wait) it stops at
// a fire that no wait logged (os 0): that one came before a later call's
// record. The fire runs under the lock, as a Sim's (FireDeadlines): the
// listed record is live until its Join takes it off the list under this
// lock, and a cancel walk calls no Io entry. A fire that names no listed
// scope, or one already fired, diverges the replay, and so does one that
// loses its scope's flag (a Cancel, or an ancestor's fire, came first on
// replay), since every logged fire set its flag in the recording.
// O(fires times listed scopes plus each fire's walk); allocates nothing.
ReplayFires(t: mut Tape, wait: bool) {
    for !t.diverged {
        b := t.log.View()
        at := t.pos
        if at + RecordHeader > b.Len() || b[at] != OFire {
            return
        }
        os := GetU64(b, at + 25)
        if wait && os == 0 {
            return
        }
        num := GetU64(b, at + 9)
        i := FindNum(&*t, num)
        if GetU64(b, at + 1) != FireDigest || GetU64(b, at + 33) != 0 || os > 1 || i < 0 || t.dls.Get(i).fired {
            t.diverged = true
            return
        }
        t.pos = at + RecordHeader
        x := t.dls.Get(i)
        t.dls.Set(i, DlRec(rec: x.rec, num: x.num, at: x.at, fired: true, arming: false))
        var won := false
        unsafe "a listed scope record is live until its Join takes it off the list, under the lock the caller holds" {
            task.DeadlineFireRec(raw.FromAddr[u8](x.rec))
            won = task.DeadlineExpired(raw.FromAddr[u8](x.rec))
        }
        if !won {
            t.diverged = true
            return
        }
    }
}

// FindNum is the index of the listed scope with arm number num, or -1.
FindNum(t: &Tape, num: u64): int {
    for i in 0..t.dls.Len() {
        if t.dls.Get(i).num == num {
            return i
        }
    }
    return -1
}

// MakesHandle: a successful op returns a new handle (tape.mk's HNew
// entries, and the call ops IoOpDirOpenAt and IoOpListen).
MakesHandle(op: u8): bool {
    return op == OOpen || op == OAccept || op == OListen || op == ODial || op == OOpenDir || op == OUdp ||
        op == OInherited || op == CallTag(core.IoOpDirOpenAt) || op == CallTag(core.IoOpListen)
}

// LogHandle is the number a recording logs for its live handle h: the one
// it gave h when a call made it, else h itself (0, or a handle the
// recording did not make).
LogHandle(t: &Tape, h: uint): uint {
    for k in 0..t.hmap.Len() {
        p := t.hmap.Get(k)
        if p.live == h {
            return p.rec
        }
    }
    return h
}

// DropHandle forgets a recording's closed handle h.
DropHandle(t: mut Tape, h: uint) {
    for k in 0..t.hmap.Len() {
        if t.hmap.Get(k).live == h {
            _ = t.hmap.RemoveAt(k)
            return
        }
    }
}

// PutAt writes x little-endian at b[at..at + 8].
PutAt(b: mut []u8, at: int, x: u64) {
    for k in 0..8 {
        b[at + k] = Trunc[u8](x >> Trunc[u64](k * 8))
    }
}

// RecOf is the recorded handle value behind a replay handle h (h itself
// when unmapped).
RecOf(t: &Tape, h: uint): uint {
    for k in 0..t.hmap.Len() {
        p := t.hmap.Get(k)
        if p.live == h {
            return p.rec
        }
    }
    return h
}

// Take answers a replayed call from the next record, copying its payload
// into out (at most out.Len() bytes); hfx says whether the call makes a
// handle (HNew: a fresh record is made and mapped) or ends one (HClose).
Take(io: Io, op: u8, h: uint, d: u64, out: mut []u8, hfx: int): core.IoResult {
    return TakeV(io, op, h, d, out, hfx, Bound(max: -1, hdr: 0, unit: 0))
}

// A replayed count's bound: a successful result's value must be
// in 0..max (no check when max < 0), and the payload must hold at least
// hdr + value * unit bytes, so a caller that copies value items out of
// the payload into a buffer of max items stays in bounds whatever the log
// says. A log edited, truncated or corrupt diverges instead.
struct Bound {
    max: i64
    hdr: int
    unit: int
}

TakeV(io: Io, op: u8, h: uint, d: u64, out: mut []u8, hfx: int, b: Bound): core.IoResult {
    var empty: [0]u8 = [0; 0]
    var n := 0
    return TakeV2(io, op, h, d, out, mut empty[..], hfx, b, mut n)
}

// TakeV2 is TakeV with the payload split: its first out.Len() bytes into
// out, the rest into out2; n gets the payload's length (0 when the call
// diverged). The deadline fires the log holds before the record are
// replayed first (ReplayFires).
TakeV2(io: Io, op: u8, h: uint, d: u64, out: mut []u8, out2: mut []u8, hfx: int, b: Bound, n: mut int): core.IoResult {
    e := EnterTape(io)
    if !e.ok {
        return Fail(core.SysErr.Canceled)
    }
    t := TapeOf(&e)
    ReplayFires(t, false)
    r := TakeLocked2(t, op, h, d, out, out2, hfx, b, n)
    UnlockSlot(e.slot)
    return r
}

TakeLocked(t: mut Tape, op: u8, h: uint, d: u64, out: mut []u8, hfx: int, bd: Bound): core.IoResult {
    var empty: [0]u8 = [0; 0]
    var n := 0
    return TakeLocked2(t, op, h, d, out, mut empty[..], hfx, bd, mut n)
}

TakeLocked2(t: mut Tape, op: u8, h: uint, d: u64, out: mut []u8, out2: mut []u8, hfx: int, bd: Bound, got: mut int): core.IoResult {
    *got = 0
    if t.diverged {
        return Fail(core.SysErr.Canceled)
    }
    b := t.log.View()
    at := t.pos
    if at + RecordHeader > b.Len() {
        t.diverged = true
        return Fail(core.SysErr.Canceled)
    }
    rec := RecOf(&*t, h)
    n := Cast[int](GetU64(b, at + 33) & 0xFFFF_FFFF)
    if b[at] != op || GetU64(b, at + 1) != FnvU64(d, u64(rec)) || at + RecordHeader + n > b.Len() {
        t.diverged = true
        return Fail(core.SysErr.Canceled)
    }
    value := Trunc[i64](GetU64(b, at + 9))
    errw := Trunc[u32](GetU64(b, at + 17))
    os := Trunc[i64](GetU64(b, at + 25))
    // a payload longer than the caller's buffer, or a count the
    // caller could not hold, is a log this call did not write.
    if n > out.Len() + out2.Len() {
        t.diverged = true
        return Fail(core.SysErr.Canceled)
    }
    if errw == u32(core.SysErr.None) && bd.max >= 0 {
        if value < 0 || value > bd.max || (value > 0 || n > 0) && i64(bd.hdr) + value * i64(bd.unit) > i64(n) {
            t.diverged = true
            return Fail(core.SysErr.Canceled)
        }
    }
    k := if n > out.Len() { out.Len() } else { n }
    src := at + RecordHeader
    if k > 0 {
        unsafe "k <= out.Len(), and at + RecordHeader + n <= b.Len() was checked above" {
            raw.Copy(raw.DataMut(mut out[0..k]), raw.Data(b[src..src + k]), k)
        }
    }
    if n > k {
        unsafe "n - k <= out2.Len() (n <= out.Len() + out2.Len()), and at + RecordHeader + n <= b.Len() was checked above" {
            raw.Copy(raw.DataMut(mut out2[0..n - k]), raw.Data(b[src + k..src + n]), n - k)
        }
    }
    *got = n
    t.pos = at + RecordHeader + n
    var err := core.SysErr.Other
    for c in 0..AllErrs.Len() {
        if u32(ErrOf(c)) == errw {
            err = ErrOf(c)
        }
    }
    var r := core.IoResult(value: value, err: err, os: os)
    if err == core.SysErr.None && hfx == HNew {
        live := rt.NewSocketHandle(0, 0)
        // A replay handle abandoned without a Close through this Io left
        // its pair; the runtime may reuse its record for this one.
        DropHandle(t, live)
        if live == 0 || !t.hmap.Push(HPair(rec: Trunc[uint](Cast[u64](value)), live: live)) {
            if live != 0 {
                MarkForeign(live, raw.Null[u8](), 0, 0)
                rt.Abandon(live)
            }
            return Fail(core.SysErr.NoSpace)
        }
        // Foreign with no ops: rt frees it with no OS call.
        MarkForeign(live, raw.Null[u8](), 0, 0)
        r = core.IoResult(value: Cast[i64](live), err: err, os: os)
    }
    if err == core.SysErr.None && hfx == HClose {
        for j in 0..t.hmap.Len() {
            if t.hmap.Get(j).live == h {
                _ = t.hmap.RemoveAt(j)
                _ = rt.CloseRuntimeHandle(h)
                break
            }
        }
    }
    return r
}

// AllErrs is every SysErr in its declaration order (core_iodecl.mk), so
// ErrOf(c) is the SysErr with code c. A member added to SysErr goes here
// too: a code missing here replays as Other. SIM-Tape-SimErrs replays each code a Sim gives.
const AllErrs: [17]core.SysErr = [core.SysErr.None, core.SysErr.NotFound, core.SysErr.Exists, core.SysErr.Denied,
    core.SysErr.IsDirectory, core.SysErr.NotDirectory, core.SysErr.NoSpace, core.SysErr.InvalidPath,
    core.SysErr.Canceled, core.SysErr.Closed, core.SysErr.Refused, core.SysErr.Reset, core.SysErr.AddrInUse,
    core.SysErr.Unreachable, core.SysErr.InvalidAddress, core.SysErr.Other, core.SysErr.Unsupported]

ErrOf(c: int): core.SysErr {
    return AllErrs[c]
}

// Filled is the part of buf a successful count-returning call wrote.
Filled(buf: []u8, r: core.IoResult): []u8 {
    if r.err != core.SysErr.None || r.value <= 0 {
        return buf[0..0]
    }
    n := if Cast[int](r.value) > buf.Len() { buf.Len() } else { Cast[int](r.value) }
    return buf[0..n]
}

// ---------------------------------------------------------------------------
// The API.

NewTape(inner: Io, replay: bool, log: []u8, slot: mut int): OutOfMemory!*Tape {
    p := rt.HeapAlloc(SizeOf[Tape](), AlignOf[Tape]())
    if raw.IsNull(p) {
        fail OutOfMemory
    }
    var t := raw.Null[Tape]()
    unsafe "p is a fresh block of Tape's size" {
        t = raw.PtrCast[u8, Tape](p)
        raw.Write(t, Tape(inner: inner, replay: replay, log: Vec[u8].Empty(), pos: 0, diverged: false,
            hmap: Vec[HPair].Empty(), made: 0, tasks: 0, dls: Vec[DlRec].Empty(), arms: 0, freed: false))
    }
    var ok := true
    unsafe "t was written above and is not registered" {
        ok = AppendBytes(mut (*t).log, log)
    }
    tok := NewToken()
    i := if ok { ClaimTape(t, tok) } else { -1 }
    if i < 0 {
        FreeTape(t)
        fail OutOfMemory
    }
    *slot = i
    return t
}

ClaimTape(t: *Tape, tok: u64): int {
    for i in 0..SlotCount {
        LockSlot(i)
        if SlotWorld(i) == 0 {
            SetSlot(i, tok, u64(raw.Addr(t)))
            SetSlotTape(i, true)
            UnlockSlot(i)
            return i
        }
        UnlockSlot(i)
    }
    return -1
}

FreeTape(t: *Tape) {
    unsafe "t is a tape no slot names any more" {
        tp := mut *t
        // A replay handle the program still owns stays a valid record: its
        // own Close or Abandon frees it, so only the map goes here.
        tp.hmap.Free()
        tp.log.Free()
        tp.dls.Free()
        rt.HeapFree(raw.PtrCast[Tape, u8](t), SizeOf[Tape](), AlignOf[Tape]())
    }
}

TapeIo(slot: int): Io {
    return core.MakeIo(raw.Of(&TapeTable), (Cast[uint](slot) << 48) | (uint(SlotToken(slot)) << 1))
}

ReleaseSlot(slot: int) {
    LockSlot(slot)
    SetSlot(slot, 0, 0)
    UnlockSlot(slot)
}

/// A Recorder whose Io forwards every call to inner and logs it. Fails
/// with OutOfMemory when the process heap cannot hold the recorder, or 64
/// Sims, Recorders and Replayers are live. O(1); allocates the recorder on
/// the process heap until Free, and the log grows there with every call.
/// It keeps its state there, not in an arena, since its Io reaches it from
/// any task with no loan.
///
/// The log holds the calls in the order they completed, with no task
/// named. So a recording replays only when one task made its calls, or
/// the tasks' calls never overlapped: a Replayer hands the next record to
/// whichever task asks first. A handle is logged as its number in the
/// recording, so one seed's recording is the same bytes in every process.
/// A recorder that runs out of memory stops recording, and Log then
/// fails.
export Recorder.New(inner: Io): OutOfMemory!Recorder {
    var slot := 0
    var empty: [0]u8 = [0; 0]
    t := try NewTape(inner, false, empty[..], mut slot)
    return Recorder(slot: slot, t: t)
}

/// The recording Io. Every call through it is forwarded to the inner Io
/// and logged with the inner Io's answer as it is, a NoCancel copy's
/// cancellation check (a sleep of 0) too. It holds no loan. After Free it
/// answers Canceled, except a NoCancel copy's check, which goes on unless
/// a scope cancelled with Cancel() and opened from that copy covers it.
/// O(1); allocates nothing.
export Recorder.Io(&self): Io {
    return TapeIo(self.slot)
}

/// A copy of the log so far, in a. It is a copy, not a view, since a
/// call through the Io, which holds no loan, may grow the log at any time.
/// Fails with OutOfMemory when a cannot hold it, and also when the
/// recording itself ran out of memory and stopped, so a cut log is never
/// taken for a whole one. O(n) in the log's length; allocates n bytes in
/// a.
export Recorder.Log(&self, a: &Arena): OutOfMemory![]u8 from a {
    LockSlot(self.slot)
    var n := 0
    var cut := false
    unsafe "the recorder owns its tape, alive until Free" {
        n = (*self.t).log.Len()
        cut = (*self.t).diverged
    }
    UnlockSlot(self.slot)
    if cut {
        fail OutOfMemory
    }
    out := try a.Slice[u8](n)
    if n > 0 {
        LockSlot(self.slot)
        unsafe "the recorder owns its tape, alive until Free; the log only grows, so its first n bytes stay" {
            v := (*self.t).log.View()
            raw.Copy(raw.DataMut(mut out[0..n]), raw.Data(v[0..n]), n)
        }
        UnlockSlot(self.slot)
    }
    return out[..]
}

/// Ends the recording and frees the log. The Io answers Canceled from then
/// on, except a NoCancel copy's cancellation check, which goes on (see
/// Recorder.Io). Tasks spawned under the Io stay scheduled by the inner Io
/// until they end, so a Sim behind it goes on running them; the recorder
/// counts against the 64 live ones until the last of them ends. O(log's
/// blocks); allocates nothing.
export Recorder.Free(self) {
    Recorder(slot, t) := self
    // The recorder owns its slot until here, so its own Io's token matches.
    e := EnterSlot(TapeIo(slot))
    var keep := false
    if e.ok {
        tp := TapeOf(&e)
        tp.freed = true
        keep = !TapeGone(&*tp)
        if keep {
            // The log and the handle map go now; the block stays, named by
            // the slot, for the hooks and the disarms (LeaveMode or
            // TDeadline frees it).
            tp.hmap.Free()
            tp.log.Free()
        } else {
            SetSlot(slot, 0, 0)
        }
        UnlockSlot(slot)
    }
    if !keep {
        FreeTape(t)
    }
}

/// A Replayer whose Io answers every call from log, which it copies.
/// Fails with OutOfMemory as Recorder.New does. O(n) in the log's length;
/// allocates the copy of log on the process heap until Free.
///
/// A replay is exact for one task's calls. The log names no task, so when
/// several tasks call at once the next record goes to whichever asks
/// first, and a call that is not the record's diverges.
export Replayer.New(log: []u8): OutOfMemory!Replayer {
    var slot := 0
    t := try NewTape(core.MakeIo(raw.Of(&core.InertIo), 0), true, log, mut slot)
    return Replayer(slot: slot, t: t)
}

/// The replaying Io: every call through it is answered from the log. A
/// call the next record does not match, or one past the log's end,
/// diverges the replay and answers Canceled. A NoCancel copy's
/// cancellation check (a sleep of 0) diverges it and goes on, as after
/// Free, since a replay that cannot go on is not a scope cancellation; a
/// scope cancelled with Cancel() and opened from that copy still cancels
/// it. It holds no loan; after Free it answers Canceled. O(1); allocates
/// nothing.
export Replayer.Io(&self): Io {
    return TapeIo(self.slot)
}

/// Whether some call differed from the log, or came after its end. O(1),
/// under the replayer's lock; allocates nothing.
export Replayer.Diverged(&self): bool {
    LockSlot(self.slot)
    var d := false
    unsafe "the replayer owns its tape, alive until Free" {
        d = (*self.t).diverged
    }
    UnlockSlot(self.slot)
    return d
}

/// Whether every record of the log was used, and none differed. O(1),
/// under the replayer's lock; allocates nothing.
export Replayer.Done(&self): bool {
    LockSlot(self.slot)
    var d := false
    unsafe "the replayer owns its tape, alive until Free" {
        d = !(*self.t).diverged && (*self.t).pos == (*self.t).log.Len()
    }
    UnlockSlot(self.slot)
    return d
}

/// Frees the replayer and its copy of the log. The Io answers Canceled
/// from then on, except a NoCancel copy's cancellation check, which goes
/// on (see Replayer.Io). O(log's blocks); allocates nothing.
export Replayer.Free(self) {
    Replayer(slot, t) := self
    ReleaseSlot(slot)
    FreeTape(t)
}
