// Why: a function of a lazily bound DLL (pkg.Link kind "lazy", or a
// <dll>.lazy.def list of std's) is called through a slot that starts at a
// stub; something must load the DLL and find the function on the first
// call, from any thread and any stack, without a lock or an allocation.
//
// What: MkLazyResolve, which the lazy stub calls (internal/codegen/lazy.go),
// MkLazyPresent, which dl.Present calls, and LazyBind behind both.
//
// How: the descriptor the build emits for a name is its slot, then its
// library's record, the NUL-terminated name, the first stub's address and
// the name's length; the record is the module handle (0 until loaded),
// the UTF-16 and ASCII file names, the ASCII length and the
// LoadLibraryExW flags. LazyBind runs LoadLibraryExW (which runs the
// DLL's initializer) and GetProcAddress (which may load a forwarded DLL)
// through MkSystemRun, so on the carrier's system stack when the caller is
// a task; std.task defines MkSystemRun, reached by its symbol (std.task
// imports std.sys). No lock: two first calls may both load, the loader
// returns the one module to both (a second reference, never released, as
// the library is never unloaded), and both store the same words. The slot
// is published by one aligned 8-byte store; a failed load is not
// remembered, so a later call tries again.

import std.raw

// The descriptor's fields, in bytes from its start (the slot).
const LazyLibAt: int = 8
const LazyNameAt: int = 16
const LazyFirstAt: int = 24
const LazyNameLenAt: int = 32

// The library record's fields, in bytes from its start (the handle).
const LazyWideAt: int = 8
const LazyTextAt: int = 16
const LazyTextLenAt: int = 24
const LazyFlagsAt: int = 32

// LazyPanic is the panic code of an unresolved lazy function: 17,
// "unresolved function" (std.rt's PanicText).
const LazyPanic: u32 = 17

// LazyReportMax bounds the panic report's message.
const LazyReportMax: int = 600

// LazyJob is one load and lookup, run where MkSystemRun runs it: lib is
// the library record, name the NUL-terminated symbol; module is the
// handle in and out (0: not loaded, or the load failed), addr the
// function (0: not found).
struct LazyJob {
    lib: *u8
    name: *u8
    module: uint
    addr: uint
}

// MkSystemRun runs f(arg) on the carrier's system stack when called on a
// task stack, else where it is (std/task/sysrun.mk).
extern MkSystemRun(f: fn(*u8), arg: *u8)

// LazyPresent is std.dl's Present for std.sys's own lazily bound externs
// (std.sys cannot import std.dl): whether f, named directly, can be
// called, loading its library first when it binds lazily; true for any
// other extern. The same intrinsic (internal/codegen present.go).
LazyPresent[F](f: F): bool

/// MkLazyResolve binds the lazily bound function whose descriptor is d and
/// returns its slot, which then holds the function's address: the lazy stub
/// jumps through it with the caller's arguments. A call of a bound function
/// never reaches it. Panics with code 17 (unresolved function), naming the
/// function and the library, when the library cannot be loaded or does not
/// export the name. O(1) beside the loader's work; allocates nothing.
export extern MkLazyResolve(d: *u8): *u8 {
    if !LazyBind(d) {
        LazyFail(d)
    }
    return d
}

/// MkLazyPresent reports whether the lazily bound function whose
/// descriptor is d can be called: it loads its library if needed (running
/// the library's initializer) and finds the function, as a first call
/// would, without calling it. O(1) beside the loader's work; allocates
/// nothing.
export extern MkLazyPresent(d: *u8): bool {
    return LazyBind(d)
}

// LazyWord is the 8-byte word at p as an atomic.
LazyWord(p: *u8): &raw.Atomic[u64] {
    unsafe "p is a slot or a handle of the lazy-binding object: 8-byte aligned, static, and only ever accessed atomically after the image loads" {
        return &*raw.PtrCast[u8, raw.Atomic[u64]](p)
    }
}

// LazyField is the 8-byte word at offset off of the record or descriptor p.
LazyField(p: *u8, off: int): u64 {
    unsafe "off is a field of a record or descriptor the build emitted, written by the linker and never after" {
        return raw.Read(raw.PtrCast[u8, u64](raw.Offset(p, off)))
    }
}

// LazyPtr is the pointer field at offset off of the record or descriptor p.
LazyPtr(p: *u8, off: int): *u8 {
    unsafe "off is a pointer field of a record or descriptor the build emitted, a static address" {
        return raw.FromAddr[u8](Trunc[uint](LazyField(p, off)))
    }
}

// LazyBind reports whether d's slot holds the function, binding it first
// when it still holds the first stub.
LazyBind(d: *u8): bool {
    slot := LazyWord(d)
    // Edge: the slot's Load pairs with the Store below on another thread:
    // reading the function's address means its library's load returned.
    if slot.Load() != LazyField(d, LazyFirstAt) {
        return true
    }
    lib := LazyPtr(d, LazyLibAt)
    handle := LazyWord(lib)
    var job := LazyJob(lib: lib, name: LazyPtr(d, LazyNameAt), module: Trunc[uint](handle.Load()), addr: 0)
    unsafe "job lives across the call, which neither suspends nor keeps p" {
        MkSystemRun(LazyRun, raw.PtrCast[LazyJob, u8](raw.OfMut(mut job)))
    }
    if job.module == 0 {
        return false
    }
    // Edge: every racing loader stores the one module the loader returned.
    handle.Store(u64(job.module))
    if job.addr == 0 {
        return false
    }
    slot.Store(u64(job.addr))
    return true
}

// LazyRun is the job MkSystemRun runs: LoadLibraryExW with the record's
// flags when the module is not loaded yet, then GetProcAddress by name.
LazyRun(p: *u8) {
    unsafe "kernel32; p is LazyBind's LazyJob, whose record and name are the build's, NUL-terminated" {
        job := raw.PtrCast[u8, LazyJob](p)
        if (*job).module == 0 {
            wide := raw.FromAddr[u16](Trunc[uint](LazyField((*job).lib, LazyWideAt)))
            flags := raw.Read(raw.PtrCast[u8, u32](raw.Offset((*job).lib, LazyFlagsAt)))
            (*job).module = LoadLibraryExW(wide, 0, flags)
            if (*job).module == 0 {
                return
            }
        }
        (*job).addr = raw.Addr(GetProcAddress((*job).module, (*job).name))
    }
}

// LazyFail panics for d's function: code 17, with the function's and the
// library's names and which of the two the loader could not find.
LazyFail(d: *u8): never {
    lib := LazyPtr(d, LazyLibAt)
    var buf: [LazyReportMax]u8 = [0; LazyReportMax]
    var n := 0
    n = LazyPut(mut buf[..], n, LazyText(d, LazyNameAt, LazyNameLenAt))
    n = LazyPut(mut buf[..], n, " of ")
    n = LazyPut(mut buf[..], n, LazyText(lib, LazyTextAt, LazyTextLenAt))
    if LazyWord(lib).Load() == 0 {
        n = LazyPut(mut buf[..], n, ": the library could not be loaded")
    } else {
        n = LazyPut(mut buf[..], n, ": the library does not export it")
    }
    unsafe "buf's frame never resumes: the panic ends the process" {
        raw.Fail(LazyPanic, raw.Str(raw.Data(buf[..]), n))
    }
}

// LazyText is the ASCII name a descriptor or record holds at the pointer
// field at and the length field len.
LazyText(p: *u8, at: int, len: int): str {
    unsafe "the build's names: ASCII, as long as their length fields say, static" {
        return raw.Str(raw.FromAddr[u8](Trunc[uint](LazyField(p, at))), Trunc[int](LazyField(p, len)))
    }
}

// LazyPut appends s to buf at n, as much as fits, and returns the new
// length.
LazyPut(buf: mut []u8, n: int, s: str): int {
    var at := n
    for c in s.AsBytes() {
        if at == buf.Len() {
            return at
        }
        buf[at] = c
        at += 1
    }
    return at
}
