// Why: std.vm hands out raw address space; reserve, commit, protect and the
// code regions must work as documented on every target.
// What: tests of Region and CodeRegion, run by `mk test --std=std.vm`.
// How: in std.vm's own module the unsafe-only rule does not apply
// (internal/check's unsafeDoors); the unsafe blocks below are for the raw
// reads and writes. A read of a Protect(None) page faults; that test is
// std.runtime's (tests_fault.mk, with runtime.AddFaultFilter).

import std.core
import std.raw

test "VM reserve 32 GiB, commit 32 KiB at each end, write and read them" {
    size := 32 << 30
    r := Reserve(size, 1 << 20) else {
        Assert(false)
        return
    }
    Assert(r.Size() == size)
    Assert(r.Base() % (1 << 20) == 0)
    block := 32 << 10
    r.Commit(0, block) else {
        Assert(false)
    }
    r.Commit(size - block, block) else {
        Assert(false)
    }
    unsafe "both blocks were just committed read-write" {
        lo := raw.FromAddr[u64](r.Base())
        hi := raw.FromAddr[u64](r.Base() + Trunc[uint](size - 8))
        Assert(raw.Read(lo) == 0 && raw.Read(hi) == 0)
        raw.Write(lo, 0x1122334455667788)
        raw.Write(hi, 0x8877665544332211)
        Assert(raw.Read(lo) == 0x1122334455667788)
        Assert(raw.Read(hi) == 0x8877665544332211)
    }
    r.Decommit(0, block)
    r.Commit(0, block) else {
        Assert(false)
    }
    unsafe "recommitted: a fresh page reads zero" {
        Assert(raw.Read(raw.FromAddr[u64](r.Base())) == 0)
    }
    r.Release()
}

test "VM misuse is Misaligned" {
    ps := PageSize()
    Assert(ps >= 4096 && ps % 4096 == 0)
    Assert(ReserveMisaligned(0, ps))
    Assert(ReserveMisaligned(ps + 1, ps))
    Assert(ReserveMisaligned(ps, 3))
    r := Reserve(4 * ps, ps) else {
        Assert(false)
        return
    }
    r.Commit(1, ps) else e {
        Assert(IsMisaligned(e))
    }
    r.Commit(0, ps + 1) else e {
        Assert(IsMisaligned(e))
    }
    r.Commit(3 * ps, 2 * ps) else e {
        Assert(IsMisaligned(e))
    }
    r.Commit(-ps, ps) else e {
        Assert(IsMisaligned(e))
    }
    r.Protect(0, 5 * ps, Prot.Read) else e {
        Assert(IsMisaligned(e))
    }
    r.Release()
}

test "VM Protect Read keeps a committed page readable, ReadWrite writable again" {
    ps := PageSize()
    r := Reserve(2 * ps, ps) else {
        Assert(false)
        return
    }
    r.Commit(0, 2 * ps) else {
        Assert(false)
    }
    p := r.Base()
    unsafe "the page is committed read-write" {
        raw.Write(raw.FromAddr[u32](p), 7)
    }
    r.Protect(0, ps, Prot.Read) else {
        Assert(false)
    }
    unsafe "the page is committed and readable" {
        Assert(raw.Read(raw.FromAddr[u32](p)) == 7)
    }
    r.Protect(0, ps, Prot.ReadWrite) else {
        Assert(false)
    }
    unsafe "the page is read-write again" {
        raw.Write(raw.FromAddr[u32](p), 9)
        Assert(raw.Read(raw.FromAddr[u32](p)) == 9)
    }
    r.Protect(ps, ps, Prot.None) else {
        Assert(false)
    }
    r.Release()
}

// IsMisaligned is whether e is Misaligned.
IsMisaligned(e: Error): bool {
    return match e {
        Misaligned { true }
        _ { false }
    }
}

// ReserveMisaligned is whether Reserve(size, align) fails with Misaligned.
ReserveMisaligned(size: int, align: int): bool {
    r := Reserve(size, align) else e {
        return IsMisaligned(e)
    }
    r.Release()
    return false
}

// RetArm64 is the aarch64 code `movz w0, #0; ret`, whose imm16 is bits 5
// to 20 of the first word; WriteRet writes it, or x86_64's
// `mov eax, v; ret`, with the value it returns.
const RetArm64: [8]u8 = [
    0x00, 0x00, 0x80, 0x52, // movz w0, #0
    0xC0, 0x03, 0x5F, 0xD6  // ret
]

// RetLen is the length of the target's `return v` code.
RetLen(): int {
    return if Target.arch == Arch.Arm64 { 8 } else { 6 }
}

// CallAt calls the code at addr, which takes nothing and returns a u32 in
// eax or w0, from inline assembly (raw.FnFromAddr is tested in std.mem).
CallAt(addr: uint): u32 {
    var r: u32 = 0
    if Target.arch == Arch.X64 {
        unsafe "addr is a code region's executable entry holding WriteRet's code, which touches no memory and preserves every callee-saved register" {
            // The call pushes below rsp: step over System V's 128-byte red
            // zone first, so it never overwrites a leaf frame's locals.
            asm("sub rsp, 128\ncall {a}\nadd rsp, 128", a: in(reg) addr, out("eax") r, clobber_abi("C"))
        }
    }
    if Target.arch == Arch.Arm64 {
        unsafe "addr is a code region's executable entry holding WriteRet's code, which touches no memory and preserves every callee-saved register" {
            // blr writes only x30, and AAPCS64 has no red zone below sp.
            asm("blr {a}", a: in(reg) addr, out("w0") r, clobber_abi("C"))
        }
    }
    return r
}

// EmitRet opens a CodeWriter with defer w.Done(), writes `return v` at o,
// and flushes it.
EmitRet(c: mut CodeRegion, o: int, v: u8): Error!void {
    var w := try c.Edit()
    defer w.Done()
    WriteRet(w.Bytes(), o, v)
    w.Flush(o, RetLen())
}

test "VM a code region runs code written through Edit" {
    var c := CodeRegion.New(100) else {
        Assert(false)
        return
    }
    defer c.Release()
    Assert(c.Size() == PageSize())
    n := RetLen()
    var w := c.Edit() else {
        Assert(false)
        return
    }
    var b := w.Bytes()
    waddr := raw.Addr(raw.DataMut(b))
    WriteRet(b, 0, 42)
    w.Flush(0, n)
    w.Done()
    e := c.Entry(0)
    first: u8 = if Target.arch == Arch.Arm64 { 0x40 } else { 0xB8 }
    last: u8 = if Target.arch == Arch.Arm64 { 0xD6 } else { 0xC3 }
    unsafe "the executable view is readable: it holds the bytes Edit wrote" {
        Assert(raw.Read(raw.FromAddr[u8](e)) == first)
        Assert(raw.Read(raw.FromAddr[u8](e + Trunc[uint](n - 1))) == last)
    }
    if Target.os == core.Os.Macos {
        Assert(e == waddr)
    } else {
        Assert(e != waddr)
    }
    Assert(CallAt(e) == 42)
    // A second function, written later at another offset.
    EmitRet(mut c, 64, 7) else {
        Assert(false)
    }
    Assert(CallAt(c.Entry(64)) == 7)
    Assert(CallAt(e) == 42)
}

// TaskEmit writes `return v` at 0 of a region another task made.
TaskEmit(c: mut CodeRegion, v: u8, ok: mut bool) {
    EmitRet(c, 0, v) else {
        return
    }
    *ok = true
}

// TaskCall runs the code at 0 of a region shared by several tasks.
TaskCall(c: &CodeRegion, out: mut u32) {
    *out = CallAt(c.Entry(0))
}

// TaskRelease ends a region another task made.
TaskRelease(c: CodeRegion) {
    c.Release()
}

// A CodeRegion is Send and Share; a CodeWriter stays on its task.
test "VM a code region crosses tasks: one writes it, two run it, one releases it" (sys: &System) {
    var c := CodeRegion.New(PageSize()) else {
        Assert(false)
        return
    }
    var ok := false
    scope sc := sys.io.Scope() {
        sc.go TaskEmit(mut c, 9, mut ok)
    }
    Assert(ok)
    var a: u32 = 0
    var b: u32 = 0
    scope sc := sys.io.Scope() {
        sc.go TaskCall(&c, mut a)
        sc.go TaskCall(&c, mut b)
    }
    Assert(a == 9 && b == 9)
    scope sc := sys.io.Scope() {
        sc.go TaskRelease(c)
    }
}

test "VM a code region of a size that is not positive is Misaligned" {
    c := CodeRegion.New(0) else e {
        Assert(IsMisaligned(e))
        return
    }
    c.Release()
    Assert(false)
}

test "VM Protect of an uncommitted page is Os(487) on every target" {
    ps := PageSize()
    r := Reserve(4 * ps, ps) else {
        Assert(false)
        return
    }
    r.Commit(0, ps) else {
        Assert(false)
    }
    var code: i64 = 0
    r.Protect(0, 2 * ps, Prot.Read) else e {
        code = match e {
            Os(code: c) { c }
            _ { -1 }
        }
    }
    Assert(code == 487)
    r.Protect(0, ps, Prot.Read) else {
        Assert(false)
    }
    r.Decommit(0, ps)
    code = 0
    r.Protect(0, ps, Prot.ReadWrite) else e {
        code = match e {
            Os(code: c) { c }
            _ { -1 }
        }
    }
    Assert(code == 487)
    r.Release()
}

test "VM Decommit of a range that is not whole pages panics [exit 101: Region.Decommit: not whole pages inside the region]" {
    ps := PageSize()
    r := Reserve(2 * ps, ps) else {
        return
    }
    r.Decommit(1, ps)
    r.Release()
}

test "VM CodeRegion.Entry outside the region panics [exit 101: CodeRegion.Entry: offset outside the region]" {
    c := CodeRegion.New(64) else {
        return
    }
    _ = c.Entry(c.Size())
    c.Release()
}

// Commit over committed pages keeps
// their contents on every target; it commits only the runs of uncommitted
// pages (on Linux a map-again would zero them: 0 on bd81ffa2's Linux run).
test "VM Commit over committed pages keeps their contents and commits only the rest" {
    ps := PageSize()
    r := Reserve(8 * ps, ps) else {
        Assert(false)
        return
    }
    r.Commit(ps, ps) else {
        Assert(false)
    }
    r.Commit(3 * ps, 2 * ps) else {
        Assert(false)
    }
    unsafe "pages 1, 3 and 4 were just committed read-write" {
        raw.Write(raw.FromAddr[u64](r.Base() + Trunc[uint](ps)), 77)
        raw.Write(raw.FromAddr[u64](r.Base() + Trunc[uint](3 * ps)), 78)
        raw.Write(raw.FromAddr[u64](r.Base() + Trunc[uint](4 * ps + 8)), 79)
    }
    r.Commit(0, 8 * ps) else {
        Assert(false)
    }
    unsafe "every page is committed now" {
        Assert(raw.Read(raw.FromAddr[u64](r.Base() + Trunc[uint](ps))) == 77)
        Assert(raw.Read(raw.FromAddr[u64](r.Base() + Trunc[uint](3 * ps))) == 78)
        Assert(raw.Read(raw.FromAddr[u64](r.Base() + Trunc[uint](4 * ps + 8))) == 79)
        for k in [0, 2, 5, 6, 7] {
            a := raw.FromAddr[u64](r.Base() + Trunc[uint](k * ps))
            Assert(raw.Read(a) == 0)
            raw.Write(a, 1)
        }
    }
    r.Protect(0, 8 * ps, Prot.Read) else {
        Assert(false)
    }
    r.Release()
}

// The commit bitmap is kept a word (64 pages) at a time; ranges that
// start and end inside words and cross them are marked and tested exactly.
test "VM the commit bitmap is exact across its 64-page words" {
    ps := PageSize()
    r := Reserve(200 * ps, ps) else {
        Assert(false)
        return
    }
    r.Commit(60 * ps, 75 * ps) else {
        Assert(false)
    }
    r.Protect(60 * ps, 75 * ps, Prot.Read) else {
        Assert(false)
    }
    var cut := 0
    r.Protect(59 * ps, 2 * ps, Prot.Read) else {
        cut += 1
    }
    r.Protect(134 * ps, 2 * ps, Prot.Read) else {
        cut += 1
    }
    Assert(cut == 2)
    r.Decommit(64 * ps, 64 * ps)
    r.Protect(60 * ps, 4 * ps, Prot.ReadWrite) else {
        Assert(false)
    }
    r.Protect(128 * ps, 7 * ps, Prot.ReadWrite) else {
        Assert(false)
    }
    r.Protect(127 * ps, ps, Prot.ReadWrite) else {
        cut += 1
    }
    r.Protect(64 * ps, ps, Prot.ReadWrite) else {
        cut += 1
    }
    Assert(cut == 4)
    r.Release()
}

// WriteRet writes the target's code `return v` at offset o of w: x86_64's
// `mov eax, v; ret`, or RetArm64 with v in its immediate.
WriteRet(w: mut []u8, o: int, v: u8) {
    if Target.arch == Arch.Arm64 {
        for i in 0..8 {
            w[o + i] = RetArm64[i]
        }
        SetImm(w, o, v)
        return
    }
    w[o] = 0xB8
    w[o + 1] = v
    w[o + 2] = 0
    w[o + 3] = 0
    w[o + 4] = 0
    w[o + 5] = 0xC3
}

// SetImm writes v into the immediate of the code WriteRet wrote at offset
// o of w: x86_64's imm32 byte 1; aarch64's imm16, bits 5 to 12 of v's
// value, which lie in bytes 0 and 1.
SetImm(w: mut []u8, o: int, v: u8) {
    if Target.arch == Arch.Arm64 {
        imm := u32(v) << 5
        w[o] = RetArm64[0] | Trunc[u8](imm)
        w[o + 1] = RetArm64[1] | Trunc[u8](imm >> 8)
        return
    }
    w[o + 1] = v
}

// ImmOf is the value SetImm wrote at offset o of w.
ImmOf(w: []u8, o: int): u8 {
    if Target.arch == Arch.Arm64 {
        return Trunc[u8]((u32(w[o]) >> 5) | (u32(w[o + 1]) << 3))
    }
    return w[o + 1]
}

// Outside Edit the region is sealed and Entry's code runs; opening another
// Edit patches across pages and re-seals on Done. The write that faults
// outside Edit is std.runtime's test (tests_fault.mk), with a filter that
// sees it.
test "VM Edit opens a write window, seals on Done, and supports repeated patches" {
    var c := CodeRegion.New(2 * PageSize()) else {
        Assert(false)
        return
    }
    defer c.Release()
    EmitRet(mut c, 0, 42) else {
        Assert(false)
    }
    e := c.Entry(0)
    Assert(CallAt(e) == 42)
    // Open a second write window, inspect existing bytes, and patch two pages.
    var w := c.Edit() else {
        Assert(false)
        return
    }
    var b := w.Bytes()
    Assert(ImmOf(b[..], 0) == 42)
    WriteRet(b, 0, 7)
    WriteRet(b, PageSize(), 9)
    w.Flush(0, 2 * PageSize())
    w.Done()
    Assert(CallAt(e) == 7)
    Assert(CallAt(c.Entry(PageSize())) == 9)
}

test "VM nested Edit on two code regions keeps outer writer writable after inner Done" {
    var a := CodeRegion.New(PageSize()) else {
        Assert(false)
        return
    }
    defer a.Release()
    var b := CodeRegion.New(PageSize()) else {
        Assert(false)
        return
    }
    defer b.Release()
    var wa := a.Edit() else {
        Assert(false)
        return
    }
    var wb := b.Edit() else {
        wa.Done()
        Assert(false)
        return
    }
    WriteRet(wb.Bytes(), 0, 19)
    wb.Flush(0, RetLen())
    wb.Done()
    // wa is still live after wb.Done().
    WriteRet(wa.Bytes(), 0, 23)
    wa.Flush(0, RetLen())
    wa.Done()
    Assert(CallAt(a.Entry(0)) == 23)
    Assert(CallAt(b.Entry(0)) == 19)
}

test "VM a reserved code region runs what Commit took, and keeps it over a second Commit" {
    ps := PageSize()
    var c := CodeRegion.Reserve(8 * ps) else {
        Assert(false)
        return
    }
    defer c.Release()
    Assert(c.Size() == 8 * ps)
    c.Commit(ps, ps) else {
        Assert(false)
    }
    c.Commit(4 * ps, 2 * ps) else {
        Assert(false)
    }
    var w := c.Edit() else {
        Assert(false)
        return
    }
    var b := w.Bytes()
    Assert(b[ps] == 0 && b[5 * ps + 8] == 0)
    WriteRet(b, ps, 42)
    WriteRet(b, 5 * ps, 5)
    // Only committed pages are flushed: aarch64's cache maintenance (dc
    // cvau / sys_icache_invalidate) faults on a page not committed.
    w.Flush(ps, RetLen())
    w.Flush(5 * ps, RetLen())
    w.Done()
    Assert(CallAt(c.Entry(ps)) == 42)
    Assert(CallAt(c.Entry(5 * ps)) == 5)
    // Pages 1, 4 and 5 committed, the others not: Edit and Done go by
    // runs of committed pages (Windows refuses one call over a gap, 487).
    c.Commit(0, 8 * ps) else {
        Assert(false)
    }
    Assert(CallAt(c.Entry(ps)) == 42)
    Assert(CallAt(c.Entry(5 * ps)) == 5)
    EmitRet(mut c, 7 * ps, 3) else {
        Assert(false)
    }
    Assert(CallAt(c.Entry(7 * ps)) == 3)
}

test "VM CodeRegion.Commit outside whole pages of the region is Misaligned" {
    ps := PageSize()
    var c := CodeRegion.Reserve(4 * ps) else {
        Assert(false)
        return
    }
    var bad := 0
    c.Commit(1, ps) else e {
        if IsMisaligned(e) {
            bad += 1
        }
    }
    c.Commit(0, 0) else e {
        if IsMisaligned(e) {
            bad += 1
        }
    }
    c.Commit(3 * ps, 2 * ps) else e {
        if IsMisaligned(e) {
            bad += 1
        }
    }
    c.Commit(-ps, ps) else e {
        if IsMisaligned(e) {
            bad += 1
        }
    }
    Assert(bad == 4)
    c.Release()
    var n := CodeRegion.New(ps) else {
        Assert(false)
        return
    }
    n.Commit(0, ps) else {
        Assert(false)
    }
    n.Commit(0, 2 * ps) else e {
        Assert(IsMisaligned(e))
    }
    n.Release()
}

test "VM a write to a page a reserved code region has not committed faults [exit 101: panic: invalid memory access at]" {
    var c := CodeRegion.Reserve(2 * PageSize()) else {
        return
    }
    defer c.Release()
    c.Commit(0, PageSize()) else {}
    var w := c.Edit() else {
        return
    }
    defer w.Done()
    w.Bytes()[PageSize() + 8] = 1
}

// The executable view of a page not committed is inaccessible too: a read
// of it faults (a committed page would read 0, so this tells the two apart,
// where a call into a zero page faults in either case).
test "VM a read through the executable view of a page a reserved code region has not committed faults [exit 101: panic: invalid memory access at]" {
    var c := CodeRegion.Reserve(2 * PageSize()) else {
        return
    }
    c.Commit(0, PageSize()) else {}
    unsafe "the page is reserved, not committed: the read is meant to fault" {
        Assert(raw.Read(raw.FromAddr[u8](c.Entry(PageSize()))) == 0)
    }
    c.Release()
}

// EditRangeErr tries EditRange and returns its error, or none after closing the writer.
EditRangeErr(r: mut CodeRegion, offset: int, len: int): ?Error {
    var w := r.EditRange(offset, len) else e {
        return some(e)
    }
    w.Done()
    return none
}

test "VM CodeRegion.EditRange rejects partial pages, out-of-bounds, and uncommitted pages; len == 0 is a no-op" {
    ps := PageSize()
    var r := CodeRegion.Reserve(4 * ps) else {
        Assert(false)
        return
    }
    r.Commit(0, ps) else {
        Assert(false)
    }

    var mis := 0
    for i in 0..5 {
        off := if i == 0 { 1 } else if i == 2 { -ps } else if i == 4 { 3 * ps } else { 0 }
        ln := if i == 1 { ps + 1 } else if i == 3 { -ps } else if i == 4 { 2 * ps } else { ps }
        match EditRangeErr(mut r, off, ln) {
            some(e) {
                if IsMisaligned(e) {
                    mis += 1
                }
            }
            none {}
        }
    }
    Assert(mis == 5)

    // Uncommitted page in Reserve -> Os(code: 487).
    var uncomm := false
    match EditRangeErr(mut r, ps, ps) {
        some(e) {
            match e {
                Os(code) { uncomm = code == 487 }
                _ {}
            }
        }
        none {}
    }
    Assert(uncomm)

    // Aligned zero-length window on committed or uncommitted aligned offset is a clean no-op.
    var w0 := r.EditRange(ps, 0) else {
        Assert(false)
        r.Release()
        return
    }
    Assert(w0.Bytes().Len() == 0)
    w0.Flush(0, 0)
    w0.Done()

    var w1 := r.EditRange(4 * ps, 0) else {
        Assert(false)
        r.Release()
        return
    }
    Assert(w1.Bytes().Len() == 0)
    w1.Done()
    r.Release()
}

test "VM CodeRegion.EditRange edits page 0 and page 1 independently" {
    ps := PageSize()
    var c := CodeRegion.New(2 * ps) else {
        Assert(false)
        return
    }

    // Seal page 0 after emitting return 111.
    var w0 := c.EditRange(0, ps) else {
        Assert(false)
        c.Release()
        return
    }
    var b0 := w0.Bytes()
    Assert(b0.Len() == ps)
    WriteRet(b0, 0, 111)
    w0.Flush(0, RetLen())
    w0.Done()
    Assert(CallAt(c.Entry(0)) == 111)

    // Edit and seal page 1 independently; page 0 remains intact and executable.
    var w1 := c.EditRange(ps, ps) else {
        Assert(false)
        c.Release()
        return
    }
    var b1 := w1.Bytes()
    Assert(b1.Len() == ps)
    WriteRet(b1, 0, 222)
    w1.Flush(0, RetLen())
    w1.Done()
    Assert(CallAt(c.Entry(0)) == 111)
    Assert(CallAt(c.Entry(ps)) == 222)
    c.Release()
}
