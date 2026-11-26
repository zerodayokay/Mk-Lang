// Why: std.sys for os "nutshell" is a placeholder that must export exactly
// what std/sys/linux exports, so std checks and builds for the target until
// the Nutshell backend replaces this directory.
//
// What: PollInit, PollSupports, PollAttach, PollDetach, PollSubmit,
// PollCancel, PollWait, PollWake, PollTestHook and PollTestPark.
//
// How: PollInit answers 0 and nothing is ever submitted. std.task.Start stops
// a program that uses tasks with rt.Fatal ("cannot start the event loop") on
// that 0, so such a program stops at start on the placeholder.

/// PollInit has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export PollInit(force_epoll: bool): u32 {
    return 0
}

/// PollSupports has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export PollSupports(kind: u32): bool {
    return false
}

/// PollAttach has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export PollAttach(h: uint): bool {
    return false
}

/// PollDetach has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export PollDetach(h: uint) {
}

/// PollSubmit has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export PollSubmit(op: *Op): bool {
    return false
}

/// PollCancel has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export PollCancel(op: *Op) {
}

/// PollWait has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export PollWait(timeout_ns: i64, out: mut []*Op): int {
    return 0
}

/// PollWake has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export PollWake() {
}

/// PollTestHook has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export PollTestHook(cb: fn(*u8), arg: *u8, on: bool): u64 {
    return 0
}

/// PollTestPark has std/sys/linux's contract; this placeholder returns `0`.
/// O(1); allocates nothing.
export PollTestPark(cb: fn(*u8), arg: *u8, on: bool): u64 {
    return 0
}

/// Waits as PollWait does. Directed main-thread wakes are macOS-only.
/// O(1) besides the wait; allocates nothing.
export PollWaitMain(timeout_ns: i64, out: mut []*Op): int {
    return PollWait(timeout_ns, out)
}

/// Directed main-thread wakes are macOS-only. Returns false. O(1).
export PollTakeMainWake(): bool {
    return false
}

/// Directed main-thread wakes are macOS-only. O(1); does nothing.
export PollWakeMain() {}
