// hostfault's parts 1 and 4 and its "claimed" case in a program with no
// task (nothing here spawns, so Main runs on the main thread's stack and the
// externs are called there directly). With no argument it adds Watch and
// prints: kernelbase's IsBadReadPtr of address 16 answers 1 from its own
// __except after Watch passed the fault; ntdll's RtlMoveMemory from a page
// protected to None copies 7 after Watch made the page readable and resumed
// it; Watch saw 2 faults. With "claimed" it adds Claim first and copies from
// address 16 with no handler: status 71. Want hostfaultmain.out and exit 0,
// or exit 71.
import std.raw
import std.runtime
import std.vm

extern RtlMoveMemory(dst: *u8, src: *u8, len: uint)
extern IsBadReadPtr(p: *u8, n: uint): i32
extern TerminateProcess(h: uint, code: u32): i32

// Seen counts the faults Watch saw; Guard is the guard page's base and
// GuardRegion the address of its Region.
var Seen: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Guard: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var GuardRegion: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// Claim ends the process with status 71 for a fault in the first page.
Claim(c: &runtime.FaultContext): runtime.FaultAction {
    if c.Addr() < 4096 {
        unsafe "the current-process pseudo-handle" {
            _ = TerminateProcess(0xFFFF_FFFF_FFFF_FFFF, 71)
        }
    }
    return runtime.FaultAction.Pass
}

// Watch counts every fault and passes it on, except an access to the guard
// page, which it makes readable and resumes at the same instruction.
Watch(c: &runtime.FaultContext): runtime.FaultAction {
    unsafe "the globals are this process's; GuardRegion is GuardCopy's Region, live in its frame while the copy runs" {
        Seen.Store(Seen.Load() + 1)
        page := Trunc[uint](Guard.Load())
        if page == 0 || c.Addr() < page || c.Addr() >= page + Trunc[uint](vm.PageSize()) {
            return runtime.FaultAction.Pass
        }
        r := raw.FromAddr[vm.Region](Trunc[uint](GuardRegion.Load()))
        (*r).Protect(0, vm.PageSize(), vm.Prot.Read) else {
            return runtime.FaultAction.Pass
        }
    }
    return runtime.FaultAction.Resume(ctx: *c)
}

// GuardCopy copies 8 bytes holding 7 with RtlMoveMemory from a page
// protected to None, which Watch resumes; it returns what was copied, or
// 0 when the region cannot be made.
GuardCopy(): u64 {
    unsafe "std.vm is unsafe only; the region outlives every access to it" {
        ps := vm.PageSize()
        r := vm.Reserve(ps, ps) else {
            return 0
        }
        r.Commit(0, ps) else {
            r.Release()
            return 0
        }
        raw.Write(raw.FromAddr[u64](r.Base() + 64), 7)
        r.Protect(0, ps, vm.Prot.None) else {
            r.Release()
            return 0
        }
        GuardRegion.Store(u64(raw.Addr(raw.Of(&r))))
        Guard.Store(u64(r.Base()))
        var v: u64 = 0
        RtlMoveMemory(raw.PtrCast[u64, u8](raw.OfMut(mut v)), raw.FromAddr[u8](r.Base() + 64), 8)
        Guard.Store(0)
        r.Release()
        return v
    }
}

export Main(sys: &System): WriteError!void {
    claimed := sys.args.Len() > 1
    var bad: i32 = 0
    var copied: u64 = 0
    var n: u64 = 0
    unsafe "AddFaultFilter is unsafe only; the externs are kernel32's; address 16 is never mapped" {
        if claimed {
            _ = runtime.AddFaultFilter(Claim)
        }
        _ = runtime.AddFaultFilter(Watch)
        if claimed {
            var dst: u64 = 0
            RtlMoveMemory(raw.PtrCast[u64, u8](raw.OfMut(mut dst)), raw.FromAddr[u8](16), 8)
            return
        }
        bad = IsBadReadPtr(raw.FromAddr[u8](16), 8)
        copied = GuardCopy()
        n = Seen.Load()
    }
    var out := sys.Stdout()
    try Print(mut out, "IsBadReadPtr: kernelbase's own __except answered {bad}\n", (bad: bad))
    try Print(mut out, "RtlMoveMemory from a guard page: the filter resumed the copy, which copied {copied}\n", (copied: copied))
    try Print(mut out, "the filter saw {n} faults\n", (n: n))
}
