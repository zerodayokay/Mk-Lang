// Every part runs in a task, so each user extern below runs on its carrier's
// system stack (std.task's SystemStack), and every fault here has its pc in
// host code or in the package's C source. With no argument it adds Watch,
// which counts the faults it sees and passes them on, except one on its
// guard page, which it resumes, and prints one line per part:
// 1. kernelbase's IsBadReadPtr of address 16 faults inside its own __try:
//    Watch sees the fault and passes it, and IsBadReadPtr's __except answers
//    1, as with no filter;
// 2. the C source's __try around ntdll's RtlMoveMemory from address 16: the
//    pc is outside the image, Watch passes it, the C __except runs (1);
// 3. the C source's __try around its own read of address 16: the pc is in
//    the image, Watch passes it, the C __except runs (1);
// 4. RtlMoveMemory from a page protected to None: Watch makes the page
//    readable and resumes ntdll's copy at its faulting instruction, which
//    then copies the page's 7;
// then how many faults Watch saw (4). With "claimed" it adds Claim first,
// which ends the process with status 71 for a fault in the first page, and
// copies from address 16 with RtlMoveMemory and no handler; with
// "claimed-seh" it does the same inside the C source's __try: a filter sees
// the fault before the C code's own __except, so the status is 71 both
// times. Want hostfault.out and exit 0, or exit 71.
import std.raw
import std.runtime
import std.vm

extern RtlMoveMemory(dst: *u8, src: *u8, len: uint)
extern IsBadReadPtr(p: *u8, n: uint): i32
extern TerminateProcess(h: uint, code: u32): i32
extern host_copy_try(src: *u8): i32
extern own_read_try(src: *u8): i32

// The filters run on the faulting thread and see only globals: Seen counts
// the faults Watch saw; Guard is the guard page's base and GuardRegion the
// address of its Region (part 4); Got0 to Got3 hold the parts' answers.
var Seen: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Guard: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var GuardRegion: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Got0: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Got1: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Got2: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Got3: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// Claim ends the process with status 71 for a fault in the first page, an
// address it guards, wherever the fault's pc is.
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
    unsafe "the globals are this process's; GuardRegion is part 4's Region, live in GuardCopy's frame while the copy runs" {
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

// Parts runs the parts of mode (0 all four, 1 claimed, 2 claimed-seh).
Parts(mode: int) {
    unsafe "the externs are kernel32's and the package's C; address 16 is never mapped" {
        null16 := raw.FromAddr[u8](16)
        if mode == 1 {
            var dst: u64 = 0
            RtlMoveMemory(raw.PtrCast[u64, u8](raw.OfMut(mut dst)), null16, 8)
            return
        }
        if mode == 2 {
            _ = host_copy_try(null16)
            return
        }
        Got0.Store(u64(Cast[u32](IsBadReadPtr(null16, 8))))
        Got1.Store(u64(Cast[u32](host_copy_try(null16))))
        Got2.Store(u64(Cast[u32](own_read_try(null16))))
        Got3.Store(GuardCopy())
    }
}

export Main(sys: &System): WriteError!void {
    var mode := 0
    if sys.args.Len() > 1 {
        mode = if sys.args[1] == "claimed" { 1 } else { 2 }
    }
    unsafe "AddFaultFilter is unsafe only" {
        if mode != 0 {
            _ = runtime.AddFaultFilter(Claim)
        }
        _ = runtime.AddFaultFilter(Watch)
    }
    scope s := sys.io.Scope() {
        s.go Parts(mode)
    }
    if mode != 0 {
        return
    }
    var g: [4]u64 = [0; 4]
    var n: u64 = 0
    unsafe "the counters are this process's" {
        g = [Got0.Load(), Got1.Load(), Got2.Load(), Got3.Load()]
        n = Seen.Load()
    }
    var out := sys.Stdout()
    try Print(mut out, "IsBadReadPtr: kernelbase's own __except answered {r}\n", (r: g[0]))
    try Print(mut out, "RtlMoveMemory in C's __try: the C __except ran ({r})\n", (r: g[1]))
    try Print(mut out, "a C read in its own __try: the C __except ran ({r})\n", (r: g[2]))
    try Print(mut out, "RtlMoveMemory from a guard page: the filter resumed the copy, which copied {r}\n", (r: g[3]))
    try Print(mut out, "the filter saw {n} faults\n", (n: n))
}
