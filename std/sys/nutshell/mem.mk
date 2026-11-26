// Why: std.sys for os "nutshell" is a placeholder that must export exactly
// what std/sys/linux exports, so std checks and builds for the target until
// the Nutshell backend replaces this directory.
//
// What: Reserve, Commit, CommitStack, Decommit, Discard, Release, GuardPage,
// GuardPages, GuardStack and AddressLimited.
//
// How: no memory can be reserved or committed.

import std.raw

/// Reserve has std/sys/linux's contract; this placeholder returns `raw.Null[u8]()`.
/// O(1); allocates nothing.
export Reserve(size: int, align: int): *u8 {
    return raw.Null[u8]()
}

/// Commit has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export Commit(p: *u8, size: int): bool {
    return false
}

/// CommitStack has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export CommitStack(p: *u8, size: int): bool {
    return false
}

/// Decommit has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export Decommit(p: *u8, size: int) {
}

/// Discard has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export Discard(p: *u8, size: int) {
}

/// AddressLimited has std/sys/linux's contract; this placeholder returns
/// `false`.
/// O(1); allocates nothing.
export AddressLimited(): bool {
    return false
}

/// Release has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export Release(p: *u8, size: int) {
}

/// GuardPage has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export GuardPage(p: *u8): bool {
    return false
}

/// GuardPages has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export GuardPages(p: *u8, size: int): bool {
    return false
}

/// GuardStack has std/sys/linux's contract; this placeholder returns `false`.
/// O(1); allocates nothing.
export GuardStack(p: *u8): bool {
    return false
}
