// Why: AppKit must receive window work on the initial macOS thread.
// What: the single main-thread service hook used by the window module.
// How: carrier zero runs it on its system stack when requested, and ticks
// while the service has live windows. A directed poller wake carries requests
// from any other thread. The hook never suspends.

import std.raw
import std.sys

const MainServiceInterval: i64 = 8_000_000

var MainServiceEnabled: raw.Atomic[u32] = raw.Atomic[u32].New(0)
var MainServiceHook: raw.Atomic[uint] = raw.Atomic[uint].New(0)

/// Installs the process's main-thread service once on macOS. The hook runs
/// on the initial OS thread's system stack and must not suspend. It answers
/// true to request periodic polling while active. Returns false outside a
/// standalone macOS program with the task scheduler, or when a hook is installed.
/// O(1); allocates nothing.
export MainThreadService(f: fn(): bool): bool {
    // Edge: MainServiceEnabled is published by Start before MainEntry runs.
    if Target.os == Os.Macos && MainServiceEnabled.Load() != 0 {
        unsafe "a named static function is one machine word and stays callable for the process" {
            var slot := f
            word := raw.Read(raw.PtrCast[fn(): bool, uint](raw.Of(&slot)))
            // Edge: publishes the hook pointer before WakeMainThreadService wakes carrier 0.
            if MainServiceHook.Cas(0, word) {
                WakeMainThreadService()
                return true
            }
        }
    }
    return false
}

/// Schedules the installed hook once on the initial macOS thread. Before
/// installation it does nothing. The caller publishes its requests before
/// waking. O(1); allocates nothing.
export WakeMainThreadService() {
    // Edge: observes the hook installed by MainThreadService before waking carrier 0.
    if Target.os == Os.Macos && MainServiceHook.Load() != 0 {
        sys.PollWakeMain()
    }
}

MainServiceDue(c: *Carrier): i64 {
    unsafe "c is this thread's carrier" {
        return (*c).main_service_due
    }
}

RunMainService(c: *Carrier) {
    // Edge: acquires the hook pointer published by MainThreadService's Cas.
    hook := MainServiceHook.Load()
    if hook == 0 {
        return
    }
    pending := sys.PollTakeMainWake()
    due := MainServiceDue(c)
    if pending || (due != 0 && sys.Now() >= due) {
        unsafe "the hook is the static function installed by MainThreadService" {
            active := raw.FnFromAddr[fn(): bool](hook)()
            (*c).main_service_due = if active { sys.Now() + MainServiceInterval } else { 0 }
        }
    }
}

WaitMainService(c: *Carrier, timeout: i64, out: mut []*sys.Op): int {
    var wait := timeout
    due := MainServiceDue(c)
    if due != 0 {
        left := due - sys.Now()
        if left <= 0 {
            wait = 0
        } else if wait < 0 || left < wait {
            wait = left
        }
    }
    return sys.PollWaitMain(wait, out)
}
