// Why: std.sys's lazy resolver loads a DLL whose initializer may need a deep
// stack, while its caller may be a task with a 64 KiB slot; std.sys sits
// below std.task, so it cannot call SystemStack itself.
//
// What: MkSystemRun, which runs a function on the carrier's system stack
// when called from a task stack, and directly otherwise.
//
// How: SystemStack, then mk_call_system (the support pack's switch) and
// SystemStackDone, as a switched extern call does (internal/codegen
// sysstack.go). std.sys reaches MkSystemRun by its symbol with no import
// (std.task imports std.sys); the build collects it only when it binds a
// function lazily (internal/mono, LazyRuntime), so no other build has it.

import std.raw

// mk_call_system runs f(arg) on the system stack ss describes, under its
// NT_TIB words on Windows (internal/codegen support_x64win.go).
extern mk_call_system(f: fn(*u8), arg: *u8, ss: *SysStack)

/// MkSystemRun runs f(arg) on the calling carrier's system stack when it is
/// called on a task stack; on a carrier's system stack or a thread Mk did
/// not start it calls f(arg) where it is, since that stack is the thread's
/// own. f must not suspend: a suspension there panics (suspend in
/// callback). O(1) beside f; allocates nothing.
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
