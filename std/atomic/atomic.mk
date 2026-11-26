// Why: counters and flags shared between tasks need atomic operations that
// are simple to use correctly, without choosing a memory order.
//
// What: AtomicInt (New, Add, Load, Store) and AtomicBool (New, Load, Store,
// Swap).
//
// How: each type is one raw.Atomic word: unique by identity, not a view,
// Send and Share, never Freeze (the private raw.Atomic field counts as
// exported for E0410).
//
// Memory ordering. raw.Atomic offers sequentially consistent operations
// only, so every operation here is seq_cst: each store or read-modify-write
// is a release that synchronizes with every later load or read-modify-write
// of the same word that reads it, and all of them fall in one total order.
// That is stronger than either type promises (their docs state no
// ordering), and it is the ordering callers rely on when they publish plain
// data before a Store(true) and read it after a Load() == true
// (tests/programs/tasks/cancel.mk).

import std.raw

/// An i64 that tasks share and change atomically. Every operation on it is
/// sequentially consistent (SeqCst), so it needs no memory order.
export struct AtomicInt {
    v: raw.Atomic[i64]
}

/// A bool that tasks share and change atomically, for a flag. Every
/// operation on it is sequentially consistent (SeqCst).
export struct AtomicBool {
    v: raw.Atomic[u8]
}

const MaxI64: i64 = 9223372036854775807
const MinI64: i64 = -9223372036854775808

/// Returns an AtomicInt holding n. O(1); it allocates nothing, so a `var`
/// global may be initialized with it.
export AtomicInt.New(n: i64): AtomicInt {
    return AtomicInt(v: raw.Atomic[i64].New(n))
}

/// Adds n and returns the new value. Wait-free: one atomic
/// read-modify-write, however many tasks add at once. O(1); it allocates
/// nothing.
///
/// A sum that overflows i64 panics with code 1, which ends the process.
/// Before the panic, Add subtracts n back, so the wrapped value is in the
/// word only for that moment.
///
/// Ordering: SeqCst. Add reads the latest value of the word, and after it
/// the task sees every write that tasks made before their earlier Store or
/// Add of the word. A Load or Add that reads its result, or a later value,
/// sees every write this task made before the Add.
export AtomicInt.Add(&self, n: i64): i64 {
    // Edge: a seq_cst read-modify-write: acquires the write it reads, releases
    // this task's writes.
    old := self.v.Add(n)
    if (n > 0 && old > MaxI64 - n) || (n < 0 && old < MinI64 - n) {
        // A seq_cst read-modify-write like the first: in the word's
        // modification order it takes this Add back, whatever others added
        // in between (wrapping addition commutes).
        _ = self.v.Add(0 -% n)
        raw.Fail(1, "AtomicInt.Add")
    }
    return old + n
}

/// Returns the value. SeqCst: after the Load, the task sees every write
/// made before the Store or Add whose value it read. O(1); it allocates
/// nothing.
export AtomicInt.Load(&self): i64 {
    // Edge: acquires the write it reads (the doc above).
    return self.v.Load()
}

/// Sets the value to n. SeqCst: a task whose Load or Add reads n also sees
/// every write this task made before the Store. O(1); it allocates nothing.
export AtomicInt.Store(&self, n: i64) {
    // Edge: releases this task's earlier writes (the doc above).
    self.v.Store(n)
}

/// Returns an AtomicBool holding b. O(1); it allocates nothing, so a `var`
/// global may be initialized with it.
export AtomicBool.New(b: bool): AtomicBool {
    return AtomicBool(v: raw.Atomic[u8].New(BoolByte(b)))
}

/// Returns the value. Its ordering is AtomicInt.Load's, an acquire: after
/// it, the task sees every write made before the Store or Swap it read.
/// O(1); it allocates nothing.
export AtomicBool.Load(&self): bool {
    // Edge: acquires the write it reads (the doc above).
    return self.v.Load() != 0
}

/// Sets the value to b. Its ordering is AtomicInt.Store's, a release: a
/// task whose Load or Swap reads b also sees every write this task made
/// before. O(1); it allocates nothing.
export AtomicBool.Store(&self, b: bool) {
    // Edge: releases this task's earlier writes (the doc above).
    self.v.Store(BoolByte(b))
}

/// Sets the value to b and returns the old value, in one atomic step. When
/// any number of tasks call Swap(true) on a false flag, exactly one gets
/// false back: a one-shot flag. SeqCst, an acquire and a release: it sees
/// what the write it replaces published, and publishes this task's earlier
/// writes to the next reader. O(1); it allocates nothing.
export AtomicBool.Swap(&self, b: bool): bool {
    // Edge: acquires the write it reads and releases this task's earlier writes.
    return self.v.Swap(BoolByte(b)) != 0
}

BoolByte(b: bool): u8 {
    return if b { 1 } else { 0 }
}
