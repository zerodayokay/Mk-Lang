// Why: local services talk over Unix-domain sockets, and processes pass
// sockets and files to each other beside a stream's bytes.
// What: ListenUnix, DialUnix, Pass (a lent handle), Passed with Adopt and
// Close, and Stream.SendHandles and RecvHandles (SCM_RIGHTS).
// How: over std.rt's UnixListen and UnixDial, which link only into a program
// that names them. A Unix listener and stream are the TCP ones' types, so
// Accept, Read, Write, Shutdown and Close serve both. Windows has them from
// 10 1803; before that, and on a file system that cannot hold a socket (WSL's
// Windows drive), they are Unsupported.

import std.core
import std.fs
import std.rt
import std.sys

/// Listens at path with a Unix-domain stream socket (the OS's largest
/// backlog). The socket file stays after Listener.Close: remove it before
/// listening at path again. Unix sockets need Linux, or Windows 10 1803 or
/// later. On Windows a read that is waiting when the peer closes may never
/// see the end of the stream, an OS defect, so a server reading Unix
/// streams there puts a deadline on each read.
/// Errors: InvalidAddress for an empty path, one holding a NUL, or one of
/// 108 bytes or more (sun_path), before any system call; AddrInUse when a
/// file exists at path; Unsupported where the target or its file system
/// has no Unix sockets; Canceled in a cancelled scope, checked first; Os
/// for any other refusal of the bind. O(1) system calls; allocates
/// nothing.
export ListenUnix(io: Io, path: str): Error!Listener {
    h := try Check(rt.UnixListen(io, path.AsBytes(), 0))
    return Listener(h: Trunc[uint](h), bound: 0, local: ZeroAddr, kind: StreamUnix, no_delay: false)
}

/// Connects to the Unix-domain listener at path. While its backlog is full
/// the dial waits, holding nothing, so a cancel or a deadline ends it with
/// Canceled and nothing lost: call again.
/// Errors: InvalidAddress as ListenUnix; Refused when nothing listens at
/// path (or, on Linux, nothing exists there); Unsupported as ListenUnix;
/// Canceled; Os for any other refusal. O(1) system calls per try;
/// allocates nothing.
export DialUnix(io: Io, path: str): Error!Stream {
    var r := rt.UnixDial(io, path.AsBytes())
    // Linux answers ENOENT for a path with no file: nothing listens there.
    if r.err == core.SysErr.NotFound {
        r = core.IoResult(value: 0, err: core.SysErr.Refused, os: 0)
    }
    h := try Check(r)
    return Stream(h: Trunc[uint](h), peer: none, kind: StreamUnix, nosh: [])
}

/// A handle sent beside a Unix stream's bytes (Stream.SendHandles), lent:
/// the receiver gets its own copy and the sender keeps this one. Plain.
export open enum Pass {
    /// An open file's handle.
    File(f: &fs.File)
    /// A connected stream.
    Stream(s: &Stream)
    /// A listener.
    Listener(l: &Listener)
}

/// A handle passed to this process, beside a Unix stream's bytes
/// (Stream.RecvHandles) or named by its parent's Command (proc.Inherited),
/// typed by what the OS says it is: a listening stream socket is a
/// Listener, any other stream socket a Stream (on Windows, where only a
/// listener is inherited, any other socket is Unsupported), anything else
/// (a file, a pipe end, a device) a File, which reads and writes from
/// position 0, or at the stream position when the handle has none.
/// Linear: take it apart with match, or Close it. A socket in it is
/// shared with the process it came from: closing either copy releases
/// only that copy, with no shutdown, and the peer sees the end when the
/// last copy closes.
export open enum Passed {
    /// Any handle that is not a stream socket: a file, a pipe end or a device.
    File(f: fs.File)
    /// A stream socket that is not listening.
    Stream(s: Stream)
    /// A listening stream socket.
    Listener(l: Listener)
}

/// The handle p carries, typed by its kind. Only std calls this
/// (RecvHandles, proc.Inherited): no program can build an rt.Passed. A
/// Stream's peer address is read once, as Dial's is (none for a Unix
/// stream). O(1), plus one system call that does not wait for a TCP
/// stream; allocates nothing.
export Passed.Adopt(p: rt.Passed): Passed {
    if p.kind == rt.PassListener {
        k := if p.family == sys.Unix { StreamUnix } else { StreamTcp }
        return Passed.Listener(l: Listener(h: p.h, bound: p.port, local: ZeroAddr, kind: k, no_delay: false))
    }
    if p.kind == rt.PassStream {
        if p.family == sys.Unix {
            return Passed.Stream(s: Stream(h: p.h, peer: none, kind: StreamUnix, nosh: []))
        }
        return Passed.Stream(s: NewStream(rt.NativeIo(), p.h))
    }
    return Passed.File(f: fs.File.Adopt(p))
}

/// Closes the handle, whatever its kind: Close for a Stream or a Listener,
/// Abandon for a File, whose close has nothing to report here. Infallible
/// and not cancellable. O(1); allocates nothing.
export Passed.Close(self) {
    match self {
        .File(f) { f.Abandon() }
        .Stream(s) { s.Close() }
        .Listener(l) { l.Close() }
        _ {}
    }
}

// std's handle seam (core.IoHandle): std.proc's Spawn lends the stream's
// runtime handle to a child (Command.handles). Not an export.
impl core.IoHandle for Stream {
    IoHandle(&self): uint {
        return self.h
    }
}

/// Sends data with the handles attached to its first byte (SCM_RIGHTS):
/// the peer takes them with RecvHandles, which reads that byte.
/// The rest of data follows as WriteAll's bytes. Each handle is lent: the
/// peer gets its own copy, and a socket among them is shared from then on
/// (closing a copy releases only that one, with no shutdown). A peer that
/// reads the handles' byte with Read instead gets the bytes, and the
/// handles are closed in its process.
/// Errors: Os(code: 0) for an empty data, no handles, more than 64, or a
/// handle with no OS handle behind it (a simulated Io's); Unsupported on
/// Windows (an AF_UNIX socket there carries no handles), for a stream that
/// is not a Unix stream, and under a simulated Io; Canceled when the scope
/// is cancelled, at entry or while the socket's buffer is full (nothing
/// sent: call again), or while the rest is written (the handles and a
/// prefix of data sent: close the stream); Closed or Reset for a peer gone;
/// Os for any other failure. O(1) system calls per try, plus WriteAll's
/// for the rest; while the buffer is full it sleeps between tries (1 ms
/// doubling to 50 ms); allocates nothing.
export Stream.SendHandles(&self, io: Io, data: []u8, handles: []Pass): Error!void {
    if data.Len() == 0 || handles.Len() == 0 || handles.Len() > 64 {
        fail Os(code: 0)
    }
    var hs: [64]uint = [0; 64]
    for i, p in handles {
        h := match p {
            .File(f) { f.IoHandle() }
            .Stream(s) { s.h }
            .Listener(l) { l.h }
            _ { 0 }
        }
        if !rt.PassNative(h) {
            fail Os(code: 0)
        }
        hs[i] = h
    }
    n := try CheckCount(rt.SendFds(io, self.h, data, hs[0..handles.Len()]), data.Len())
    if n == 0 {
        fail Os(code: 0)
    }
    if n < data.Len() {
        try self.WriteAll(io, data[n..])
    }
}

/// Receives at most buf.Len() bytes, and the handles that came with them
/// (SendHandles' first byte), each pushed onto into in the order sent:
/// the bytes, 0 at the end of the stream. Bytes with no handles are
/// returned as Read returns them, so a caller that expects handles at a
/// known place in the stream reads up to it with Read and then calls this.
/// Waits while nothing is queued; a cancel or a deadline ends the wait with
/// Canceled and nothing taken: call again.
/// Errors: Os(code: 0) for an empty buf; Unsupported as SendHandles, and
/// for a handle received that is a socket but no stream socket (a foreign
/// sender's: the bytes are consumed and every handle of the message
/// closed); Os(code: 90) for more than 64 handles in one message (the
/// same); OutOfMemory when into cannot grow or std cannot allocate the
/// record it keeps for a handle (the same; the handles already pushed stay in
/// into); Canceled; Reset for a peer gone; Os for any other failure. O(1)
/// system calls per try; allocates a runtime record per handle and what
/// into's Push takes from a.
export Stream.RecvHandles(&self, a: &Alloc, io: Io, buf: mut []u8, into: mut HeapList[Passed]): Error!int {
    if buf.Len() == 0 {
        fail Os(code: 0)
    }
    var ps: [64]rt.Passed = [rt.Passed(h: 0, kind: 0, family: 0, port: 0); 64]
    r := rt.RecvFds(io, self.h, buf, mut ps[..])
    if r.err == core.SysErr.Other && r.os == 0 {
        fail OutOfMemory
    }
    n := try CheckCount(r, buf.Len())
    k := Trunc[int](r.os)
    for i in 0..k {
        p := Passed.Adopt(ps[i])
        into.Push(a, p) else {
            p.Close()
            for j in i + 1..k {
                Passed.Adopt(ps[j]).Close()
            }
            fail OutOfMemory
        }
    }
    return n
}
