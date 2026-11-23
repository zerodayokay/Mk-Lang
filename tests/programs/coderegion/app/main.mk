// With no argument it prints one line per part and exits 0:
// 1. code written through CodeRegion.Edit (`mov eax, 42; ret`), flushed, runs at
//    its entry and returns 42;
// 2. a write to a page protected to None faults; a filter makes the page read-write and resumes
//    the thread at the same instruction, which then succeeds (a guard page a program manages);
// 3. code in a region that loads from address 0x10 faults; a filter resumes 8 bytes later, past
//    the load, and the code returns 42;
// 4. code in a region that starts with `ud2` faults with FaultKind.Illegal on both targets
//    (Linux takes SIGILL with SIGSEGV's routing); a filter resumes 2 bytes later, and the
//    code returns 42.
// With the argument "unresumed" it runs part 3's code with no filter added, and with
// "unresumed-illegal" part 4's, so the fault in the code region reaches Mk's own report:
// "panic: fault in a code region", exit 101. The filters see only the
// faults they test for and pass every other on.
import std.raw
import std.runtime
import std.vm

// `mov eax, 42; ret`.
const Return42: [6]u8 = [0xB8, 0x2A, 0x00, 0x00, 0x00, 0xC3]

// `mov rax, [0x10]` (8 bytes, an access fault at the region's first byte), then
// `mov eax, 42; ret`.
const LoadNull: [14]u8 = [0x48, 0x8B, 0x04, 0x25, 0x10, 0x00, 0x00, 0x00, 0xB8, 0x2A, 0x00, 0x00, 0x00, 0xC3]

// `ud2` (2 bytes, an illegal instruction at the region's first byte), then `mov eax, 42; ret`.
const Illegal: [8]u8 = [0x0F, 0x0B, 0xB8, 0x2A, 0x00, 0x00, 0x00, 0xC3]

// The guard page's Region (part 2) and the code region's entry (part 3), for the filters, which
// run on the faulting thread and see only globals. Faults counts the faults the filters took.
var Guard: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var GuardRegion: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var CodeLo: raw.Atomic[u64] = raw.Atomic[u64].New(0)
var Faults: raw.Atomic[u64] = raw.Atomic[u64].New(0)

// CallCode calls the code at addr, which returns a u32 in eax.
CallCode(addr: uint): u32 {
    var r: u32 = 0
    unsafe "addr is a code region's entry holding Return42 or LoadNull, which touch no Mk memory" {
        asm("sub rsp, 128\ncall {a}\nadd rsp, 128", a: in(reg) addr, out("eax") r, clobber_abi("C"))
    }
    return r
}

// Unguard resumes a write to the guard page after making the page read-write again.
Unguard(c: &runtime.FaultContext): runtime.FaultAction {
    unsafe "the globals are this process's; GuardRegion is the address of part 2's Region, live in Main's frame while the write that faults runs" {
        page := Trunc[uint](Guard.Load())
        if page == 0 || c.Addr() < page || c.Addr() >= page + Trunc[uint](vm.PageSize()) {
            return runtime.FaultAction.Pass
        }
        r := raw.FromAddr[vm.Region](Trunc[uint](GuardRegion.Load()))
        (*r).Protect(0, vm.PageSize(), vm.Prot.ReadWrite) else {
            return runtime.FaultAction.Pass
        }
        Faults.Store(Faults.Load() + 1)
    }
    return runtime.FaultAction.Resume(ctx: *c)
}

// SkipLoad resumes an access fault at the code region's first instruction 8 bytes later, past
// the load.
SkipLoad(c: &runtime.FaultContext): runtime.FaultAction {
    unsafe "the globals are this process's" {
        lo := Trunc[uint](CodeLo.Load())
        if lo == 0 || c.Pc() != lo || c.Kind() != runtime.FaultKind.Access {
            return runtime.FaultAction.Pass
        }
        Faults.Store(Faults.Load() + 1)
    }
    var d := *c
    d.SetPc(c.Pc() + 8)
    return runtime.FaultAction.Resume(ctx: d)
}

// SkipIllegal resumes an illegal-instruction fault at the code region's first instruction 2 bytes
// later, past the `ud2`.
SkipIllegal(c: &runtime.FaultContext): runtime.FaultAction {
    unsafe "the globals are this process's" {
        lo := Trunc[uint](CodeLo.Load())
        if lo == 0 || c.Pc() != lo || c.Kind() != runtime.FaultKind.Illegal {
            return runtime.FaultAction.Pass
        }
        Faults.Store(Faults.Load() + 1)
    }
    var d := *c
    d.SetPc(c.Pc() + 2)
    return runtime.FaultAction.Resume(ctx: d)
}

// Run writes code into a new region, runs it from its entry and returns its result; -1 when the
// region cannot be made.
Run(code: []u8, lo: bool): i64 {
    unsafe "std.vm is unsafe only" {
        var c := vm.CodeRegion.New(64) else {
            return -1
        }
        defer c.Release()
        var w := c.Edit() else {
            return -1
        }
        var b := w.Bytes()
        for i in 0..code.Len() {
            b[i] = code[i]
        }
        w.Flush(0, code.Len())
        w.Done()
        if lo {
            CodeLo.Store(u64(c.Entry(0)))
        }
        r := CallCode(c.Entry(0))
        CodeLo.Store(0)
        return i64(r)
    }
}

// GuardWrite writes 5 and then 6 into a page protected to None; Unguard resumes the first
// write. It returns what the page then holds, -1 when the region cannot be made.
GuardWrite(): i64 {
    unsafe "std.vm and AddFaultFilter are unsafe only; the region outlives every access to it" {
        ps := vm.PageSize()
        r := vm.Reserve(ps, ps) else {
            return -1
        }
        r.Commit(0, ps) else {
            r.Release()
            return -1
        }
        r.Protect(0, ps, vm.Prot.None) else {
            r.Release()
            return -1
        }
        GuardRegion.Store(u64(raw.Addr(raw.Of(&r))))
        Guard.Store(u64(r.Base()))
        p := raw.FromAddr[u64](r.Base() + 64)
        raw.Write(p, 5)
        raw.Write(p, raw.Read(p) + 1)
        v := raw.Read(p)
        Guard.Store(0)
        r.Release()
        return Cast[i64](v)
    }
}

export Main(sys: &System): WriteError!int {
    if sys.args.Len() > 1 && sys.args[1] == "unresumed" {
        _ = Run(LoadNull[..14], false)
        return 0
    }
    if sys.args.Len() > 1 && sys.args[1] == "unresumed-illegal" {
        _ = Run(Illegal[..8], false)
        return 0
    }
    var out := sys.Stdout()
    try Print(mut out, "code region: written code returned {r}\n", (r: Run(Return42[..6], false)))
    unsafe "AddFaultFilter is unsafe only" {
        _ = runtime.AddFaultFilter(Unguard)
        _ = runtime.AddFaultFilter(SkipLoad)
        _ = runtime.AddFaultFilter(SkipIllegal)
    }
    g := GuardWrite()
    try Print(mut out, "guard page: the write resumed after the filter; the page holds {g}\n", (g: g))
    s := Run(LoadNull[..14], true)
    try Print(mut out, "code-region fault: the filter resumed past the load; the code returned {s}\n", (s: s))
    k := Run(Illegal[..8], true)
    try Print(mut out, "illegal instruction: the filter saw FaultKind.Illegal and resumed past it; the code returned {k}\n", (k: k))
    var n: u64 = 0
    unsafe "the counter is this process's" {
        n = Faults.Load()
    }
    try Print(mut out, "filters resumed {n} faults\n", (n: n))
    return 0
}
