// Why: on macOS every system service is reached through libSystem, whose
// system-call numbers are private and change between releases, and every
// function std.sys calls must be declared once, with one Mk signature.
//
// What: the libSystem functions std.sys calls, as bodiless externs, each
// named exactly once in libSystem.def, which internal/driver reads to write
// the stub the link reads.
//
// How: C int is i32, size_t uint, ssize_t int, off_t i64, pid_t i32; a
// pointer is *u8 (or *i32, *u64 where the callee writes a word). open,
// fcntl and ioctl are variadic in libSystem, and Apple's arm64 convention
// passes variadic arguments on the stack, so they are declared with `...`
// and every variadic argument is passed as the C int or pointer the
// callee reads. On x86_64 the stat family's 64-bit-inode entry points are
// fstat64, stat64 and lstat64; on arm64 they are fstat, stat and lstat (the
// arm64 names have only the 64-bit-inode layout). Private to std.sys; the
// names that fold onto std.sys's own are in std.sys.lc.

extern __error(): *i32
extern __ulock_wait(op: u32, addr: *u8, value: u64, timeout_us: u32): i32
extern __ulock_wake(op: u32, addr: *u8, value: u64): i32
extern _NSGetExecutablePath(buf: *u8, size: *u32): i32
extern _dyld_get_image_header(i: u32): *u8
// bzero and memset_pattern4, 8 and 16 are never called by std itself: LLVM
// lowers a zeroing memset to bzero on Darwin, and a loop storing a repeated
// 4-, 8- or 16-byte value to memset_pattern*, so the link must find them in
// libSystem.
extern bzero(p: *u8, n: uint)
extern memset_pattern16(p: *u8, pattern: *u8, n: uint)
extern memset_pattern4(p: *u8, pattern: *u8, n: uint)
extern memset_pattern8(p: *u8, pattern: *u8, n: uint)
extern accept(fd: i32, addr: *u8, len: *u32): i32
extern bind(fd: i32, addr: *u8, len: u32): i32
extern chdir(path: *u8): i32
extern chmod(path: *u8, mode: u16): i32
extern clock_gettime(clock: u32, ts: *i64): i32
extern closedir(dir: *u8): i32
extern connect(fd: i32, addr: *u8, len: u32): i32
extern dup(fd: i32): i32
extern dup2(fd: i32, to: i32): i32
extern fcntl(fd: i32, cmd: i32, ...): i32
extern fdopendir(fd: i32): *u8
extern fstat(fd: i32, st: *u8): i32
extern fstat64(fd: i32, st: *u8): i32
extern fsync(fd: i32): i32
extern ftruncate(fd: i32, len: i64): i32
extern getcwd(buf: *u8, size: uint): *u8
extern getentropy(buf: *u8, n: uint): i32
extern getpeername(fd: i32, addr: *u8, len: *u32): i32
extern getpid(): i32
extern getsockname(fd: i32, addr: *u8, len: *u32): i32
extern getsockopt(fd: i32, level: i32, name: i32, val: *u8, len: *u32): i32
extern ioctl(fd: i32, req: uint, ...): i32
extern isatty(fd: i32): i32
extern kevent(kq: i32, changes: *u8, nchanges: i32, events: *u8, nevents: i32, timeout: *i64): i32
extern kill(pid: i32, sig: i32): i32
extern kqueue(): i32
extern listen(fd: i32, backlog: i32): i32
extern lseek(fd: i32, off: i64, whence: i32): i64
extern lstat(path: *u8, st: *u8): i32
extern lstat64(path: *u8, st: *u8): i32
extern madvise(addr: *u8, len: uint, advice: i32): i32
extern mkdir(path: *u8, mode: u16): i32
extern mmap(addr: *u8, len: uint, prot: i32, flags: i32, fd: i32, off: i64): *u8
extern mprotect(addr: *u8, len: uint, prot: i32): i32
extern munmap(addr: *u8, len: uint): i32
extern nanosleep(req: *i64, rem: *i64): i32
extern open(path: *u8, flags: i32, ...): i32
extern pread(fd: i32, buf: *u8, n: uint, off: i64): int
extern pthread_attr_destroy(attr: *u8): i32
extern pthread_attr_init(attr: *u8): i32
extern pthread_attr_setdetachstate(attr: *u8, state: i32): i32
extern pthread_attr_setstacksize(attr: *u8, size: uint): i32
extern pthread_create(t: *u64, attr: *u8, start: fn(*u8): *u8, arg: *u8): i32
extern pthread_get_stackaddr_np(t: u64): *u8
extern pthread_get_stacksize_np(t: u64): uint
extern pthread_jit_write_protect_np(enabled: i32)
extern pthread_jit_write_protect_supported_np(): i32
extern pthread_join(t: u64, ret: *u8): i32
extern pthread_kill(t: u64, sig: i32): i32
extern pthread_main_np(): i32
extern pthread_self(): u64
extern pthread_threadid_np(t: u64, id: *u64): i32
extern pwrite(fd: i32, buf: *u8, n: uint, off: i64): int
extern read(fd: i32, buf: *u8, n: uint): int
extern readdir(dir: *u8): *u8
extern realpath(path: *u8, out: *u8): *u8
extern recvfrom(fd: i32, buf: *u8, n: uint, flags: i32, addr: *u8, len: *u32): int
extern rmdir(path: *u8): i32
extern sched_yield(): i32
extern sendto(fd: i32, buf: *u8, n: uint, flags: i32, addr: *u8, len: u32): int
extern setsockopt(fd: i32, level: i32, name: i32, val: *u8, len: u32): i32
extern shutdown(fd: i32, how: i32): i32
extern sigaction(sig: i32, act: *u8, old: *u8): i32
extern sigaltstack(ss: *u8, old: *u8): i32
extern socket(domain: i32, kind: i32, proto: i32): i32
extern stat(path: *u8, st: *u8): i32
extern stat64(path: *u8, st: *u8): i32
extern sys_icache_invalidate(p: *u8, n: uint)
extern sysconf(name: i32): int
extern unlink(path: *u8): i32
extern waitpid(pid: i32, status: *i32, options: i32): i32
extern write(fd: i32, buf: *u8, n: uint): int
