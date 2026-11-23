// Why: std.sys for os "nutshell" is a placeholder that must export exactly
// what std/sys/linux exports, so std checks and builds for the target until
// the Nutshell backend replaces this directory.
//
// What: VmProtect, VmCodeNew, VmCodeCommit, VmCodePerPage, VmCodeBeginWrite,
// VmCodeEndWrite, VmCodeProtect, VmCodeFree, VmFlush and RawSyscall, what
// std.vm and std.linux need.
//
// How: no protection change and no code region can be made, so each fails
// with ENOSYS (38).

import std.raw

/// VmProtect has std/sys/linux's contract; this placeholder returns 38.
/// O(1); allocates nothing.
export VmProtect(p: *u8, size: int, prot: u32): i64 {
    return 38
}

/// VmCode has std/sys/linux's contract.
export struct VmCode {
    export w: *u8
    export x: *u8
    export err: i64
}

/// VmCodeNew has std/sys/linux's contract; this placeholder fails with 38.
/// O(1); allocates nothing.
export VmCodeNew(size: int, commit: bool): VmCode {
    return VmCode(w: raw.Null[u8](), x: raw.Null[u8](), err: 38)
}

/// VmCodeCommit has std/sys/linux's contract; this placeholder returns 38.
/// O(1); allocates nothing.
export VmCodeCommit(c: &VmCode, offset: int, size: int): i64 {
    return 38
}

/// VmCodePerPage has std/sys/linux's contract.
/// O(1); allocates nothing.
export VmCodePerPage(): bool {
    return true
}

/// VmCodeBeginWrite has std/sys/linux's contract; this placeholder returns 38.
/// O(1); allocates nothing.
export VmCodeBeginWrite(c: &VmCode): i64 {
    return 38
}

/// VmCodeEndWrite has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export VmCodeEndWrite(c: &VmCode) {
}

/// VmCodeProtect has std/sys/linux's contract; this placeholder returns 38.
/// O(1); allocates nothing.
export VmCodeProtect(c: &VmCode, offset: int, size: int, writable: bool): i64 {
    return 38
}

/// VmCodeFree has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export VmCodeFree(c: &VmCode, size: int) {
}

/// VmFlush has std/sys/linux's contract; this placeholder does nothing.
/// O(1); allocates nothing.
export VmFlush(x: *u8, n: int) {
}

/// RawSyscall has std/sys/linux's contract; this placeholder makes no system
/// call and returns -38 (-ENOSYS).
/// O(1); allocates nothing.
export RawSyscall(nr: uint, a0: uint, a1: uint, a2: uint, a3: uint, a4: uint, a5: uint): int {
    return -38
}
