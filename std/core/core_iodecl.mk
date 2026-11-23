// Why: every operation that touches the OS goes through an Io value, so the
// same program runs on the native runtime or under std.sim's simulated OS;
// the types live here, apart from the calls, so a freestanding build keeps
// them.
//
// What: Io, IoTable (the table of OS operations), IoResult, SysErr, IoAddr,
// IoStat, IoChild, the IoKind, IoOpen, IoStd, IoEnv and IoRoute constants,
// StackSize, Scope, and InertIo with its InertHook and IoHooked.
//
// How: Io is compiler-known: plain, Send and Share, and holds no loans. It is
// a pointer to a global IoTable plus flags; the runtime finds the current
// task from the stack pointer, so Io names none. InertIo answers every entry
// with SysErr.Other, which lets text formatting run at compile time.

import std.raw

/// The handle through which a program reaches the OS: every operation that
/// may wait or touch the outside world (files, sockets, clocks, sleeping,
/// randomness, task scopes) takes an Io. Main gets one in System.io. The same
/// program runs on the native runtime or under a simulator (std.sim),
/// depending on the Io it is given. A function that takes an Io never runs
/// at compile time.
///
/// An Io is a plain value that holds no loans and may be copied and shared
/// between tasks freely. It names no task: the runtime finds the current task
/// itself.
export struct Io {
    table: *IoTable
    flags: uint
}

/// The result of one Io table entry: err is None on success, and value then
/// holds the result. os is the OS's own error code when err is SysErr.Other,
/// else 0.
export struct IoResult {
    /// The entry's result when err is None, such as a byte count or a runtime
    /// handle.
    export value: i64
    /// None on success, else the failure kind.
    export err: SysErr
    /// The OS's own error code when err is Other. On success some entries put
    /// a second result here, such as listen's bound port.
    export os: i64
}

/// What an Io table entry reports: None on success, else the failure. Other
/// when the OS gave a code outside this list (IoResult.os holds it);
/// Unsupported for an operation this target or this Io does not have.
export enum SysErr: u32 {
    None
    NotFound
    Exists
    Denied
    IsDirectory
    NotDirectory
    NoSpace
    InvalidPath
    Canceled
    Closed
    Refused
    Reset
    AddrInUse
    Unreachable
    InvalidAddress
    Other
    // An OS operation this target does not have (the Nutshell
    // placeholder's new exports, an option the OS lacks). Last, so the
    // members before it keep their values.
    Unsupported
}

/// A network address as the udp, sendto, recvfrom and resolve entries take
/// it. ip is in network order, an IPv4 address in ip[0..4] with the rest 0;
/// port is in host order; zone is the IPv6 scope id. A plain value with no
/// loans.
export struct IoAddr {
    /// The address in network order; an IPv4 address in ip[0..4], the rest 0.
    export ip: [16]u8
    /// The port, in host order.
    export port: u16
    /// Whether ip holds an IPv6 address.
    export v6: bool
    /// The IPv6 scope id, 0 for none.
    export zone: u32
}

/// What the stat and readdir entries report: the kind (IoKindFile, IoKindDir,
/// IoKindLink, IoKindOther), the size in bytes, and the time of the last
/// modification in nanoseconds since the Unix epoch.
export struct IoStat {
    /// IoKindFile, IoKindDir, IoKindLink or IoKindOther.
    export kind: u32
    /// The size in bytes.
    export size: i64
    /// The time of the last modification, in nanoseconds since the Unix epoch.
    export modified: i64
}

/// IoStat.kind of a regular file.
export const IoKindFile: u32 = 1
/// IoStat.kind of a directory.
export const IoKindDir: u32 = 2
/// IoStat.kind of a symbolic link.
export const IoKindLink: u32 = 3
/// IoStat.kind of anything that is not a file, a directory or a link.
export const IoKindOther: u32 = 4

/// The open entry's mode. 0 opens for reading; 1 creates or truncates for
/// reading and writing; 2 opens or creates for reading and writing without
/// truncating. IoOpenOptions selects the options form (fs.OpenWith): any of
/// the flags below, and from bit IoOpenPermShift the permission bits a
/// created file gets (the Unix mode & 0o7777). On Windows only the user write
/// bit counts: when it is clear, the file is read-only.
export const IoOpenOptions: u32 = 0x100
/// The options form's flag: open for reading.
export const IoOpenRead: u32 = 1
/// The options form's flag: open for writing.
export const IoOpenWrite: u32 = 2
/// The options form's flag: open for writing, every write at the end.
export const IoOpenAppend: u32 = 4
/// The options form's flag: create the file when it is missing.
export const IoOpenCreate: u32 = 8
/// The options form's flag: truncate the file to 0 bytes.
export const IoOpenTruncate: u32 = 16
/// The options form's flag: with IoOpenCreate, fail with Exists when the
/// file exists.
export const IoOpenExclusive: u32 = 32
/// Where the options form's permission bits start.
export const IoOpenPermShift: u32 = 16

/// The in-out record of the spawn entry. In: stdio[i] for stdin, stdout and
/// stderr (IoStdInherit, IoStdNull, IoStdPipe), and inherit, a runtime socket
/// handle the child inherits (0 none). Out: pipes[i], the runtime handle of
/// the parent's end of stream i (stdin's is the write end), 0 where stdio[i]
/// is not a pipe; pid, the OS process id.
export struct IoChild {
    /// In: how each of stdin, stdout and stderr is set up, IoStdInherit,
    /// IoStdNull or IoStdPipe.
    export stdio: [3]u32
    /// In: a runtime socket handle the child inherits, 0 for none.
    export inherit: uint
    /// Out: the runtime handle of the parent's end of each piped stream
    /// (stdin's is the write end), 0 where the stream is not a pipe.
    export pipes: [3]uint
    /// Out: the child's OS process id.
    export pid: i64
}

/// IoChild.stdio: the child shares the parent's stream.
export const IoStdInherit: u32 = 0
/// IoChild.stdio: the child's stream is the null device.
export const IoStdNull: u32 = 1
/// IoChild.stdio: the child's stream is a pipe, whose parent end comes back in
/// IoChild.pipes.
export const IoStdPipe: u32 = 2

/// IoSpawnReq.stdio: the child's stream is a duplicate of the runtime handle
/// in IoSpawnReq.src (a file or a pipe end the caller lends).
export const IoStdHandle: u32 = 3
/// IoSpawnReq.stdio[2] only: the child's stderr is its stdout.
export const IoStdJoin: u32 = 4
/// IoSpawnReq.env_mode: the child gets this process's environment.
export const IoEnvInherit: u32 = 0
/// IoSpawnReq.env_mode: the child gets exactly the entries given.
export const IoEnvReplace: u32 = 1
/// IoSpawnReq.env_mode: this process's environment, each given entry
/// replacing the one of its name or added (names compare ignoring case on
/// Windows).
export const IoEnvExtend: u32 = 2

/// The in-out record of the spawn op (IoOpSpawn). A plain value.
///
/// In: stdio (IoStdInherit, IoStdNull, IoStdPipe, IoStdHandle, IoStdJoin) and
/// src (the runtime handles for IoStdHandle); env_mode; group, to start the
/// child as a group leader (a process group on Linux, a job object without
/// kill-on-close on Windows); pty, a pseudo-terminal's term (IoPty.term) that
/// becomes the child's standard streams and controlling terminal, 0 none.
///
/// Out: pipes, the parent's ends of the IoStdPipe streams (runtime handles);
/// pid; job, the group's OS handle on Windows (0 on Linux, where the group id
/// is the pid).
export struct IoSpawnReq {
    export stdio: [3]u32
    export src: [3]uint
    export env_mode: u32
    export group: bool
    export pty: uint
    export pipes: [3]uint
    export pid: i64
    export job: uint
}

/// A pseudo-terminal (IoOpPtyOpen): read and write are the runtime handles of
/// its controlling side (the same handle on Linux); term is the OS value
/// IoSpawnReq.pty takes (on Linux the master's descriptor, live while read
/// is; on Windows the pseudo-console). A plain value.
export struct IoPty {
    export read: uint
    export write: uint
    export term: uint
}

/// The table of OS operations behind an Io. The native runtime, std.sim and
/// the inert table each provide one. h is a runtime handle.
///
/// Function types have no parameter names, so here they are for the entries
/// after wake: mkdir(p); stat(p, st); opendir(p); readdir(h, name, st);
/// udp(addr); sendto(h, data, to); recvfrom(h, buf, from); resolve(host,
/// out); spawn(args, child); procwait(h); prockill(h); inherited().
///
/// The entries after leave: deadline(rec, nanos, op) arms (op 0) the deadline
/// of the scope record rec, nanos from now on this Io's clock, or disarms it
/// (op 1). stdio(i, data) writes data whole to process stream i (0 stdout, 1
/// stderr) under its lock, after its buffered bytes, and is not a
/// cancellation point. call(args) runs one OS operation (IoCallArgs): the
/// native table runs args.job, inline or on the blocking pool as args.route
/// says, and std.sim answers by args.op (the IoOp constants). A program links
/// an operation's code only when it names the std.rt function that makes the
/// call. A table without an op answers Unsupported.
export struct IoTable {
    /// open(path, mode) opens a file; mode is 0, 1, 2 or the options form
    /// (IoOpenOptions). value is the runtime handle.
    export open: fn(Io, str, u32): IoResult
    /// read(h, buf, pos) reads at most buf.Len() bytes at offset pos, or at the
    /// handle's position when pos is -1. value is the count; 0 is the end.
    export read: fn(Io, uint, mut []u8, i64): IoResult
    /// write(h, data, pos) writes data at offset pos, or at the handle's
    /// position when pos is -1. value is the count written.
    export write: fn(Io, uint, []u8, i64): IoResult
    /// size(h) answers the file's size in bytes.
    export size: fn(Io, uint): IoResult
    /// sync(h) flushes the file's data to its device.
    export sync: fn(Io, uint): IoResult
    /// close(h) releases the handle. It is not a cancellation point.
    export close: fn(Io, uint): IoResult
    /// rename(src, dst) renames a file or directory.
    export rename: fn(Io, str, str): IoResult
    /// remove(path) removes the entry at path.
    export remove: fn(Io, str): IoResult
    /// listen(addr) binds a listener to "host:port". value is the runtime
    /// handle and os the bound port, so port 0 answers which port the OS chose.
    export listen: fn(Io, str): IoResult
    /// accept(h) waits for one connection on the listener h. value is the new
    /// connection's runtime handle.
    export accept: fn(Io, uint): IoResult
    /// dial(addr) connects to "host:port". value is the connection's runtime
    /// handle.
    export dial: fn(Io, str): IoResult
    /// recv(h, buf) reads at most buf.Len() bytes from a connection. value is
    /// the count; 0 is the end of the stream.
    export recv: fn(Io, uint, mut []u8): IoResult
    /// send(h, data) writes some of data to a connection. value is the count,
    /// which may be short.
    export send: fn(Io, uint, []u8): IoResult
    /// now() answers the monotonic clock in nanoseconds, from an arbitrary
    /// start.
    export now: fn(Io): IoResult
    /// sleep(nanos) suspends the caller for nanos nanoseconds on this Io's
    /// clock.
    export sleep: fn(Io, i64): IoResult
    /// random(buf) fills buf with random bytes from the OS.
    export random: fn(Io, mut []u8): IoResult
    /// yield() lets other tasks run before the caller continues.
    export yield: fn(Io): IoResult
    /// signal(s) waits until the signal s arrives.
    export signal: fn(Io, u32): IoResult
    /// wait(addr, expected) suspends the caller while the word at addr holds
    /// expected, until a wake on it. It may return early.
    export wait: fn(Io, *u32, u32): IoResult
    /// wake(addr, all) wakes one waiter on the word at addr, or every one when
    /// all is true. It must not suspend.
    export wake: fn(Io, *u32, bool)
    /// mkdir(path) makes a directory.
    export mkdir: fn(Io, str): IoResult
    /// stat(path, st) fills st with the facts of the file at path.
    export stat: fn(Io, str, mut IoStat): IoResult
    /// opendir(path) opens a directory for reading. value is the runtime
    /// handle.
    export opendir: fn(Io, str): IoResult
    /// readdir(h, name, st) reads the next entry of the directory h: its name
    /// into name and its facts into st.
    export readdir: fn(Io, uint, mut []u8, mut IoStat): IoResult
    /// udp(addr) makes a UDP socket bound to addr. value is the runtime handle
    /// and os the bound port.
    export udp: fn(Io, IoAddr): IoResult
    /// sendto(h, data, to) sends data as one datagram to to.
    export sendto: fn(Io, uint, []u8, IoAddr): IoResult
    /// recvfrom(h, buf, from) receives one datagram into buf and its sender's
    /// address into from. value is the count.
    export recvfrom: fn(Io, uint, mut []u8, mut IoAddr): IoResult
    /// resolve(host, out) writes the addresses of host into out. value is the
    /// count; a name with no answer is NotFound.
    export resolve: fn(Io, str, mut []IoAddr): IoResult
    /// spawn(args, child) starts a program with the arguments args, set up and
    /// answered through child. value is the child's runtime handle.
    export spawn: fn(Io, []str, mut IoChild): IoResult
    /// procwait(h) waits for the child h to end. value is its exit code.
    export procwait: fn(Io, uint): IoResult
    /// prockill(h) ends the child h at once.
    export prockill: fn(Io, uint): IoResult
    /// inherited() answers the listening socket this process inherited from its
    /// parent, once; a second call is NotFound.
    export inherited: fn(Io): IoResult
    // std.task calls enter on a task's own stack before the task
    // runs (its first run, and after each suspension point) and leave
    // before each suspension point and once when its function returns, with
    // the task's Io (its scope's) and its id, which no other task in the
    // process has. A suspension inside either hook calls no hook. The
    // native, direct and inert tables make both no-ops; std.sim hands its
    // baton through them, so one seed gives one interleaving.
    /// enter(id) runs on a task's own stack before the task runs and after
    /// each suspension point; id is the task's id.
    export enter: fn(Io, u64)
    /// leave(id) runs before each suspension point of a task and once when its
    /// function returns; id is the task's id.
    export leave: fn(Io, u64)
    /// deadline(rec, nanos, op) arms or disarms the deadline of a scope record.
    export deadline: fn(Io, *u8, i64, u32): IoResult
    /// stdio(i, data) writes data whole to process stream i, 0 stdout or 1
    /// stderr.
    export stdio: fn(Io, u32, []u8): IoResult
    /// call(args) runs one OS operation described by args.
    export call: fn(Io, *IoCallArgs): IoResult
}

/// The scalar words of a call-entry call, in and out; each IoOp constant says
/// what its words mean. A plain value.
export struct IoWords {
    /// The first word; its meaning is the IoOp's.
    export a: i64
    /// The second word; its meaning is the IoOp's.
    export b: i64
    /// The third word; its meaning is the IoOp's.
    export c: i64
    /// The fourth word; its meaning is the IoOp's.
    export d: i64
}

/// One call of the call entry. It lives in the caller's frame until the entry
/// returns.
///
/// - op: an IoOp constant, which std.sim reads.
/// - route: IoRouteInline, IoRoutePool, IoRouteWhole, IoRouteReady,
///   IoRouteChecked or IoRouteLock.
/// - h: the runtime handle the call is on (0 none); os: its OS handle, which
///   the native entry fills.
/// - p, q: the input bytes; out: the output bytes; each a pointer and length
///   into the caller's slices.
/// - w: the words in and out; res: the result.
/// - job: the native body, from std.rt, which reads and writes the record.
/// - stop: on the blocking pool, the address of the job's cancel word (null
///   elsewhere): 0 while the job runs, 1 once a cancel or a deadline asked it
///   to end. A long job, such as a copy, polls it between bounded steps.
export struct IoCallArgs {
    /// The IoOp constant that names the operation.
    export op: u32
    /// How the call runs: an IoRoute constant.
    export route: u32
    /// The runtime handle the call is on, 0 for none.
    export h: uint
    /// The OS handle behind h, which the native entry fills.
    export os: uint
    /// The first input bytes: a pointer, with pn bytes.
    export p: *u8
    /// The length of p in bytes.
    export pn: int
    /// The second input bytes: a pointer, with qn bytes.
    export q: *u8
    /// The length of q in bytes.
    export qn: int
    /// The output bytes: a pointer, with room for outn bytes.
    export out: *u8
    /// The length of out in bytes.
    export outn: int
    /// The scalar words in and out.
    export w: IoWords
    /// The call's result.
    export res: IoResult
    /// The native body that runs the operation, reading and writing this
    /// record.
    export job: fn(*IoCallArgs)
    /// On the blocking pool, the job's cancel word: 0 while it runs, 1 once a
    /// cancel or a deadline asked it to end. Null elsewhere.
    export stop: *u32
}

/// IoCallArgs.route: run job on the calling task, as one non-blocking call.
/// It is not a cancellation point.
export const IoRouteInline: u32 = 0
/// IoCallArgs.route: run job on the blocking pool. Canceled when the scope is
/// cancelled before the job starts; a call already running is the pool's to
/// interrupt.
export const IoRoutePool: u32 = 1
/// IoCallArgs.route: as IoRoutePool, but once job starts it runs to its end
/// whatever a cancel says (a copy that must not stop half way). A cancel
/// while it waits for a pool thread ends it unrun, with Canceled.
export const IoRouteWhole: u32 = 2
/// IoCallArgs.route: no job; wait on the poller until h has input, consuming
/// nothing (IoOpReady). A cancel ends the wait with Canceled.
export const IoRouteReady: u32 = 3
/// IoCallArgs.route: as IoRouteInline, but Canceled first when the scope is
/// cancelled, as the native listen and dial entries are: a cancellation point
/// that holds nothing.
export const IoRouteChecked: u32 = 4
/// IoCallArgs.route: no job; take h's whole-file lock (IoOpLock; w.a 2
/// exclusive) by waiting on the poller (on Windows, an overlapped LockFileEx
/// on the completion port). Canceled before it starts, and ended with
/// Canceled by a cancel; value 1. rt.FileLock uses it only on a native Io,
/// for a handle on the poller of a target whose poller waits for locks.
export const IoRouteLock: u32 = 5

/// Std-internal access to a value's runtime handle. std.net implements it for
/// Listener, so std.proc's Supervise can pass the listener to a child without
/// a public accessor. User code cannot import std.core, so it cannot name
/// this interface. The handle stays owned by the value it came from.
export interface IoHandle {
    IoHandle(&self): uint
}

/// The limits of a scope opened with Io.ScopeWith: at most tasks tasks at
/// once, each with a stack of size stack. s.go waits while the scope is full.
/// tasks must be at least 1; a smaller value panics the scope (code 8).
export struct Limits {
    /// The most tasks the scope runs at once; at least 1.
    export tasks: int
    /// The stack size of each task of the scope.
    export stack: StackSize
}

/// The size of a task's stack. Default is 1 MiB of address space; Large is
/// what Main gets (16 MiB); Bytes(n) is n bytes rounded up to a power of two
/// from 64 KiB to 512 MiB. A larger n, or a reservation the OS refuses,
/// panics the scope (code 8). Stack memory is touched only on demand. Each
/// Bytes size has a region of its own, reserved the first time a scope asks
/// for it. StackSize is not open, so a match on it outside std must cover
/// Bytes.
export enum StackSize {
    /// 1 MiB of address space.
    Default
    /// 16 MiB, the size Main gets.
    Large
    /// n bytes, rounded up to a power of two from 64 KiB to 512 MiB.
    Bytes(n: int)
}

/// A task scope, opened with Io.Scope, Io.ScopeOf or Io.ScopeWith: the
/// tasks spawned in it end before it does, and Cancel and CancelAfter reach
/// every operation under it. Its 128-byte record lives in the scope's local
/// on the caller's frame, so opening one allocates nothing. Scope[E] also
/// holds the scope's error.
export mut view struct Scope {
    words: [16]u64
}

/// The table of an Io that reaches no OS: every entry returns SysErr.Other
/// with os 0, and wake does nothing. The Writer from Text.Writer carries it,
/// so text formatting runs at compile time. On such an Io the cancellation
/// check before an operation fails with Other, which is not Canceled, so
/// the operation goes on.
export const InertIo = IoTable(open: InertOpen, read: InertRead, write: InertWrite, size: InertHandle,
    sync: InertHandle, close: InertHandle, rename: InertRename, remove: InertPath, listen: InertPath,
    accept: InertHandle, dial: InertPath, recv: InertRecv, send: InertSend, now: InertNone,
    sleep: InertSleep, random: InertRandom, yield: InertNone, signal: InertSignal, wait: InertWait,
    wake: InertWake, mkdir: InertPath, stat: InertStat, opendir: InertPath, readdir: InertReadDir,
    udp: InertUdp, sendto: InertSendTo, recvfrom: InertRecvFrom, resolve: InertResolve, spawn: InertSpawn,
    procwait: InertHandle, prockill: InertHandle, inherited: InertNone,
    enter: InertHook, leave: InertHook,
    deadline: InertDeadline, stdio: InertStdio, call: InertCall)

// InertResult is what every inert entry returns.
InertResult(): IoResult {
    return IoResult(value: 0, err: SysErr.Other, os: 0)
}

InertOpen(io: Io, p: str, mode: u32): IoResult {
    return InertResult()
}

InertRead(io: Io, h: uint, buf: mut []u8, pos: i64): IoResult {
    return InertResult()
}

InertWrite(io: Io, h: uint, data: []u8, pos: i64): IoResult {
    return InertResult()
}

InertHandle(io: Io, h: uint): IoResult {
    return InertResult()
}

InertRename(io: Io, src: str, dst: str): IoResult {
    return InertResult()
}

InertPath(io: Io, p: str): IoResult {
    return InertResult()
}

InertRecv(io: Io, h: uint, buf: mut []u8): IoResult {
    return InertResult()
}

InertSend(io: Io, h: uint, data: []u8): IoResult {
    return InertResult()
}

InertNone(io: Io): IoResult {
    return InertResult()
}

InertSleep(io: Io, nanos: i64): IoResult {
    return InertResult()
}

InertRandom(io: Io, buf: mut []u8): IoResult {
    return InertResult()
}

InertSignal(io: Io, s: u32): IoResult {
    return InertResult()
}

InertWait(io: Io, addr: *u32, expected: u32): IoResult {
    return InertResult()
}

InertWake(io: Io, addr: *u32, all: bool) {
}

/// The enter and leave hook that does nothing, for every table whose tasks
/// need no turn-taking (the inert, direct and native tables, and test
/// tables). O(1); it allocates nothing.
export InertHook(io: Io, task: u64) {
}

/// The deadline entry of a table with no clock: it arms nothing and answers
/// Other. O(1); it allocates nothing.
export InertDeadline(io: Io, rec: *u8, nanos: i64, op: u32): IoResult {
    return InertResult()
}

/// The stdio entry of a table that reaches no process stream: it answers
/// Other, which a Writer reports as WriteError. O(1); it allocates nothing.
export InertStdio(io: Io, i: u32, data: []u8): IoResult {
    return InertResult()
}

/// The call entry of a table that reaches no OS: every op answers
/// Unsupported. O(1); it allocates nothing.
export InertCall(io: Io, a: *IoCallArgs): IoResult {
    return IoResult(value: 0, err: SysErr.Unsupported, os: 0)
}

// NativeState is the fast path of CancelPoint (sync_mutex.mk) and of
// std.task's hook test (IoHooked). tab is the address of the table std.task's
// NativeIo returns, registered by NativeInit (0 until std.task starts, so
// every Io takes the full check); live counts the scopes whose cancel flag
// is set and which have not ended (CancelLiveAdd). A table is named by the
// address an Io carries, read from the one Io std.task makes, not by
// `&task.NativeTable`: a const has no single address (each `&` of it may
// materialize its own copy), so a compare against `&NativeTable` at another
// site never matched. On its own cache line: live changes only on a cancel
// and at the end of a cancelled scope, and every cancellation point reads
// it.
align(64) struct NativeState {
    tab: raw.Atomic[u64]
    live: raw.Atomic[i64]
}

var Native: NativeState = NativeState(tab: raw.Atomic[u64].New(0), live: raw.Atomic[i64].New(0))

/// Registers io's table as the native one. std.task calls it once, with its
/// native Io, before its first task runs. O(1); it allocates nothing.
export NativeInit(io: Io) {
    // Edge: stored before any task of the table exists (TaskInit), so
    // every later load on a task (CancelPoint, IsNative) reads it.
    Native.tab.Store(u64(raw.Addr(io.table)))
}

/// Reports whether io's table is the one NativeInit registered; false for
/// every Io before then. O(1); it allocates nothing.
export IsNative(io: Io): bool {
    // Edge: as NativeInit's store; before it, no Io is native, and the
    // table's address is not read as a word (so a compile-time evaluation
    // of this path is not refused).
    t := Native.tab.Load()
    return t != 0 && u64(raw.Addr(io.table)) == t
}

/// Reports whether io's table may have enter and leave hooks, that is,
/// whether it is not the native table, whose hooks do nothing. One compare
/// saves a native task two indirect calls per suspension point. O(1); it
/// allocates nothing.
export IoHooked(io: Io): bool {
    return !IsNative(io)
}

InertStat(io: Io, p: str, st: mut IoStat): IoResult {
    return InertResult()
}

InertReadDir(io: Io, h: uint, name: mut []u8, st: mut IoStat): IoResult {
    return InertResult()
}

InertUdp(io: Io, addr: IoAddr): IoResult {
    return InertResult()
}

InertSendTo(io: Io, h: uint, data: []u8, to: IoAddr): IoResult {
    return InertResult()
}

InertRecvFrom(io: Io, h: uint, buf: mut []u8, from: mut IoAddr): IoResult {
    return InertResult()
}

InertResolve(io: Io, host: str, out: mut []IoAddr): IoResult {
    return InertResult()
}

InertSpawn(io: Io, args: []str, child: mut IoChild): IoResult {
    return InertResult()
}

// InertIoValue is an Io over InertIo; it holds no loan (the table is a
// const, which carries only the static pseudo-loan).
InertIoValue(): Io {
    return Io(table: raw.Of(&InertIo), flags: 0)
}
