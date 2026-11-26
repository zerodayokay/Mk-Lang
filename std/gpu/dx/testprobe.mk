// Why: the device tests need facts about the process that the portable
// layer does not expose: the x86_64 floating-point control word a driver
// must leave unchanged, the d3d10warp.dll version the WARP goldens are keyed
// by, and how many heap allocations a device makes.
//
// What: ReadMxcsr, WarpDllVersion, and CountingHeapState with
// NewCountingHeapState, MakeCountingHeap and CountingHeapAllocs.
//
// How: stmxcsr into a stack word; the VS_FIXEDFILEINFO of d3d10warp.dll
// through version.dll loaded from System32; a core.Heap whose table counts
// each call in atomics and forwards it to an inner allocator.

import std.core
import std.raw
import std.dl

/// The x86_64 MXCSR register: the SSE rounding mode, denormal handling and
/// exception masks a driver call must leave as it found them. Zero on other
/// architectures. No errors. O(1); allocates nothing.
export ReadMxcsr(): u32 {
    if Target.arch != Arch.X64 {
        return 0
    }
    var mx: u32 = 0
    unsafe "stmxcsr stores exactly 4 bytes at its operand, which is the address of the live stack word mx" {
        asm("stmxcsr dword ptr [{p}]", p: in(reg) raw.OfMut(mut mx))
    }
    return mx
}

/// The file version of the d3d10warp.dll this process would load, as
/// "major.minor.build.revision", or "" when it is not Windows or the
/// version resource cannot be read. No errors. O(size of the version
/// resource); allocates that resource and the string in a.
export WarpDllVersion(a: &Arena, io: Io): str from a {
    if Target.os != Os.Windows {
        return ""
    }
    lib := dl.Open(io, "version.dll", dl.Search.System) else { return "" }
    defer lib.Close(io)
    s_sz := lib.Symbol("GetFileVersionInfoSizeW") else { return "" }
    s_get := lib.Symbol("GetFileVersionInfoW") else { return "" }
    s_val := lib.Symbol("VerQueryValueW") else { return "" }

    path_s := "d3d10warp.dll\0"
    var path_w: [32]u16 = [0; 32]
    for i in 0..path_s.AsBytes().Len() {
        path_w[i] = u16(path_s.AsBytes()[i])
    }
    sub_w: [2]u16 = [u16(92), 0]

    unsafe "the three symbols come from System32 version.dll and have these Win32 prototypes; path_w and sub_w are NUL-terminated UTF-16 stack arrays; buf holds the sz bytes GetFileVersionInfoW may write; VerQueryValueW returns a pointer into buf, which a outlives, and info_len bounds the two words read at offsets 8 and 12" {
        fn_sz := raw.FnFromAddr[fn(*u16, *u32): u32](raw.Addr(s_sz))
        fn_get := raw.FnFromAddr[fn(*u16, u32, u32, *u8): i32](raw.Addr(s_get))
        fn_val := raw.FnFromAddr[fn(*u8, *u16, **u8, *u32): i32](raw.Addr(s_val))
        var handle: u32 = 0
        sz := fn_sz(raw.Data(path_w[..]), raw.OfMut(mut handle))
        if sz == 0 {
            return ""
        }
        var buf := a.Slice[u8](int(sz)) else { return "" }
        if fn_get(raw.Data(path_w[..]), 0, sz, raw.DataMut(buf)) == 0 {
            return ""
        }
        var info_p: *u8 = raw.Null[u8]()
        var info_len: u32 = 0
        if fn_val(raw.Data(buf[..]), raw.Data(sub_w[..]), raw.OfMut(mut info_p), raw.OfMut(mut info_len)) == 0 || raw.IsNull(info_p) || info_len < 16 {
            return ""
        }
        sig := raw.Read(raw.PtrCast[u8, u32](info_p))
        if sig != 0xFEEF04BD {
            return ""
        }
        ms := raw.Read(raw.PtrCast[u8, u32](raw.Offset(info_p, 8)))
        ls := raw.Read(raw.PtrCast[u8, u32](raw.Offset(info_p, 12)))
        return a.Fmt("{a}.{b}.{c}.{d}", (a: ms >> 16, b: ms & 0xFFFF, c: ls >> 16, d: ls & 0xFFFF)) else { "" }
    }
}

/// The counters behind a counting heap: every allocation and resize call
/// made through it, counted with atomics so any thread may allocate.
export struct CountingHeapState {
    inner: *core.Alloc
    allocs: raw.Atomic[i64]
    resizes: raw.Atomic[i64]
}

CountingHeapAlloc(ctx: *u8, size: int, align: int): *u8 {
    unsafe "ctx is the CountingHeapState that MakeCountingHeap stored, alive while its heap is borrowed" {
        st := raw.PtrCast[u8, CountingHeapState](ctx)
        _ = (*st).allocs.Add(1)
        return core.AllocBytes(&*(*st).inner, size, align)
    }
}

CountingHeapResize(ctx: *u8, p: *u8, old: int, new: int, align: int): bool {
    unsafe "ctx is the CountingHeapState that MakeCountingHeap stored, alive while its heap is borrowed" {
        st := raw.PtrCast[u8, CountingHeapState](ctx)
        _ = (*st).resizes.Add(1)
        return core.ResizeBytes(&*(*st).inner, p, old, new, align)
    }
}

CountingHeapFree(ctx: *u8, p: *u8, size: int, align: int) {
    unsafe "ctx is the CountingHeapState that MakeCountingHeap stored, alive while its heap is borrowed" {
        st := raw.PtrCast[u8, CountingHeapState](ctx)
        core.FreeBytes(&*(*st).inner, p, size, align)
    }
}

const CountingHeapTable = core.AllocTable(alloc: CountingHeapAlloc, resize: CountingHeapResize, free: CountingHeapFree)

/// Counters at zero that forward every request to inner. No errors. O(1);
/// allocates nothing.
export NewCountingHeapState(inner: &core.Alloc): CountingHeapState {
    return CountingHeapState(inner: raw.Of(inner), allocs: raw.Atomic[i64].New(0), resizes: raw.Atomic[i64].New(0))
}

/// A heap that counts each call in st and passes it to st's inner
/// allocator. The heap borrows st. No errors. O(1); allocates nothing.
export MakeCountingHeap(st: &CountingHeapState): core.Heap from st {
    unsafe "the heap's context is st, which the result borrows, so st outlives every call through the table; the entries change st only through its atomics, so a shared loan suffices; CountingHeapTable is a constant" {
        return core.MakeHeap(raw.PtrCast[CountingHeapState, u8](raw.Of(st)), raw.Of(&CountingHeapTable))
    }
}

/// The allocation and resize calls made through st since it was created.
/// No errors. O(1); allocates nothing.
export CountingHeapAllocs(st: &CountingHeapState): (allocs: i64, resizes: i64) {
    return (allocs: st.allocs.Load(), resizes: st.resizes.Load())
}
