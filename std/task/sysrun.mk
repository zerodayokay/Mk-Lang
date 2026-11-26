// Why: std code that calls deep into the OS (a lazy DLL's load, which runs
// its initializer; a graphics driver's device or shader creation) may need
// far more stack than a task slot has, and std's externs run on the
// caller's stack; std.sys sits below std.task, so it cannot call
// SystemStack itself.
//
// What: MkSystemRun, std's one way to run a function on the carrier's
// system stack: there when called from a task stack, directly otherwise.
//
// How: SystemStack, then mk_call_system (the support pack's switch) and
// SystemStackDone, as a switched extern call does (internal/codegen
// sysstack.go). A module above std.task may call it as a function (none
// does yet, and no test does); std.sys reaches it by its symbol with no
// import (std.task imports std.sys), and the build collects it for std.sys
// only when it binds a function lazily (internal/mono, LazyRuntime).

import std.raw

// mk_call_system runs f(arg) on the system stack ss describes, under its
// NT_TIB words on Windows (internal/codegen support_x64win.go).
extern mk_call_system(f: fn(*u8), arg: *u8, ss: *SysStack)

/// MkSystemRun runs f(arg) on the calling carrier's system stack when it is
/// called on a task stack; on a carrier's system stack (inside another
/// switched call included) or a thread Mk did not start it calls f(arg)
/// where it is, since that stack is the thread's own. It is std's one
/// system-stack runner: std code wraps a deep OS call in it, and keeps hot
/// calls direct. f runs as an extern call does: it blocks the carrier, it
/// must not suspend (a suspension there panics, suspend in callback), it
/// may allocate, and a panic in it is reported as anywhere else, its
/// backtrace listing the frames on the system stack. O(1) beside f;
/// allocates nothing.
export extern MkSystemRun(f: fn(*u8), arg: *u8) {
    ss := SystemStack()
    if raw.IsNull(ss) {
        f(arg)
        return
    }
    unsafe "ss is this carrier's system stack, which SystemStack just prepared for one call" {
        mk_call_system(f, arg, ss)
    }
    SystemStackDone()
}
