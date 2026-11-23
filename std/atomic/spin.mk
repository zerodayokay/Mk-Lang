// Why: a kernel or an interrupt-free section needs a lock that never
// suspends a task and needs no Io.
//
// What: Spin[T] (New, Lock, Into) and its guard SpinGuard[T] (Ref, Mut,
// Store, Unlock). A task that holds it should hold it for a few steps: a
// waiter burns its carrier.
//
// How: test-and-test-and-set with bounded exponential backoff (Anderson
// 1990) and a `pause` after each failed take, in core.SpinLock, which
// also holds the storage and its unsafe, since std.atomic is not in
// unsafe_modules.
// Spin[T] is Send and Share for T: Send, as Mutex[T] (core.SpinCore asserts
// them).
//
// Memory ordering: the Cas(0, 1) that takes the lock is the acquire edge
// from the release store of 0 in the last unlock, which ends that critical
// section; so every write made under the lock is seen by the next holder.

import std.core

/// A lock around a value of T that spins and never suspends a task, so it
/// needs no Io. Lock returns a SpinGuard; Into takes the value out. Spin is
/// Send and Share when T is Send.
export view struct Spin[T] {
    c: core.SpinCore[T]
}

/// Proof that a Spin is held, returned by Spin.Lock. It borrows the Spin,
/// which stays alive and locked until Unlock. The guard is linear: every
/// path must end it with Unlock.
export linear view struct SpinGuard[T] {
    p: *core.SpinCore[T]
    marker: [0]mut T
}

/// Returns a Spin holding x, unlocked. O(1); it allocates nothing, so a
/// `var` global may be initialized with it.
export Spin[T].New(x: T): Spin[T] {
    return Spin[T](c: core.SpinNew(x))
}

/// Spins until the lock is free, takes it, and returns the guard. It never
/// fails and never suspends the task, and it is not fair: a waiter may be
/// passed over. It allocates nothing. The writes made under the lock by
/// its last holder are visible once Lock returns.
///
/// A waiter reads the lock word until it is free, then tries to take it,
/// and pauses after each failed take, twice as long each time. From the
/// 64th look on, Lock yields the OS thread before each look instead,
/// through the yield hook MkThreadYield, which needs no import: a hosted
/// program gets the OS's yield, and a freestanding one only pauses unless
/// it exports its own.
/// It never yields the task, so with one carrier a task spinning on a lock
/// that another task holds never lets that task run.
export Spin[T].Lock(&self): SpinGuard[T] {
    return SpinGuard[T](p: core.SpinLock(&self.c), marker: [])
}

/// Consumes the Spin and returns its value. A Spin passed by value has no
/// guard borrowing it, so it is unlocked. O(1); it allocates nothing.
export Spin[T].Into(self): T {
    Spin(c) := self
    return core.SpinInto(c)
}

/// Returns the guarded value, for reading. O(1); it allocates nothing.
export SpinGuard[T].Ref(&self): &T {
    return core.SpinRef(self.p)
}

/// Returns the guarded value, for writing. O(1); it allocates nothing.
export SpinGuard[T].Mut(mut self): mut T {
    return core.SpinMut(self.p)
}

/// Replaces the guarded value with x and drops the old one. For a linear
/// T the call is E0701: use Replace(g.Mut(), x) to get the old value back.
/// O(1); it allocates nothing.
export SpinGuard[T].Store(mut self, x: T into self) {
    core.SpinStore(self.p, x)
}

/// Releases the lock with a Release store and ends the guard. The next
/// holder sees every write made under the lock. The store is not SeqCst, so
/// it orders no later load of this task. O(1); it allocates nothing.
export SpinGuard[T].Unlock(self) {
    SpinGuard(p, marker: _) := self
    core.SpinUnlock(p)
}
