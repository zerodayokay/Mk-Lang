// Why: GLFW owns the native Unix window and processes callbacks on one OS thread.
// What: startup, request dispatch and event pumping for Linux and macOS.
// How: Linux waits on a detached OS thread; macOS polls on carrier zero's
// system stack. Both use the shared request queue and window records.

import std.atomic
import std.core
import std.raw
import std.rt
import std.sys
import std.task

const UnixThreadOutOfMemory: u32 = 4

struct UnixThreadState {
    init_state: atomic.Atomic[u32]
    init_err: atomic.Atomic[i64]
    dispatch: atomic.Atomic[uint]
    bell: Doorbell
    linux_awake: atomic.Atomic[u8]
    linux_idle: sys.Sem
}

var UnixThread: UnixThreadState = UnixThreadState(
    init_state: atomic.Atomic[u32].New(ThreadUnstarted),
    init_err: atomic.Atomic[i64].New(0),
    dispatch: atomic.Atomic[uint].New(0),
    bell: DoorbellNew(),
    linux_awake: atomic.Atomic[u8].New(0),
    linux_idle: sys.NewSem(),
)

UnixWindowTarget(): bool {
    return Target.os == core.Os.Linux || Target.os == core.Os.Macos
}

WindowSupported(): bool {
    return Target.os == core.Os.Windows || UnixWindowTarget()
}

EnsureUnixWindowThread(io: Io): Error!void {
    if !core.IsNative(io) {
        fail Unsupported
    }
    for {
        st := UnixThread.init_state.Load(atomic.Acquire)
        if st == ThreadReady {
            return
        }
        if st == UnixThreadOutOfMemory {
            fail OutOfMemory
        }
        if st == ThreadFailed {
            code := UnixThread.init_err.Load(atomic.Acquire)
            if code == 0 {
                fail Unsupported
            }
            fail Os(code: code)
        }
        if st == ThreadUnstarted {
            ce := UnixThread.init_state.CompareExchange(ThreadUnstarted, ThreadStarting, atomic.AcqRel, atomic.Acquire)
            if ce.ok {
                if Target.os == core.Os.Macos {
                    if !task.MainThreadService(UnixMacPump) {
                        UnixInitFailed(0)
                    }
                } else if Target.os == core.Os.Linux {
                    start := sys.ThreadStart(UnixLinuxEntry, raw.Null[u8](), 8 << 20)
                    if start.err != core.SysErr.None {
                        UnixInitFailed(start.os)
                    }
                }
                continue
            }
        }
        try UnixThread.init_state.Wait(io, ThreadStarting)
    }
}

UnixInitFailed(code: i64) {
    UnixThread.init_err.Store(code, atomic.Release)
    UnixThread.init_state.Store(ThreadFailed, atomic.Release)
    UnixThread.init_state.Wake(true)
}

UnixInit(): bool {
    lib := GlfwLibrary()
    if lib.err != core.SysErr.None {
        UnixInitFailed(lib.os)
        return false
    }
    handle := Trunc[uint](lib.value)
    dispatch := BindGlfw(handle) else {
        sys.DlClose(handle)
        UnixInitFailed(0)
        return false
    }
    unsafe "the initialized GLFW ABI is called on the owner; the process heap keeps its dispatch table alive" {
        mem := rt.ProcessHeap().Alloc().Bytes(SizeOf[GlfwDispatch](), AlignOf[GlfwDispatch]())
        if raw.IsNull(mem) {
            sys.DlClose(handle)
            UnixThread.init_state.Store(UnixThreadOutOfMemory, atomic.Release)
            UnixThread.init_state.Wake(true)
            return false
        }
        if dispatch.init() == 0 {
            code := dispatch.get_error(raw.Null[*u8]())
            rt.ProcessHeap().Alloc().FreeBytes(mem, SizeOf[GlfwDispatch](), AlignOf[GlfwDispatch]())
            sys.DlClose(handle)
            UnixInitFailed(i64(code))
            return false
        }
        raw.Write(raw.PtrCast[u8, GlfwDispatch](mem), dispatch)
        UnixThread.dispatch.Store(raw.Addr(mem), atomic.Release)
    }
    UnixThread.init_state.Store(ThreadReady, atomic.Release)
    UnixThread.init_state.Wake(true)
    return true
}

UnixWake() {
    if !UnixThread.bell.Ring() {
        return
    }
    if Target.os == core.Os.Macos {
        task.WakeMainThreadService()
    } else if Target.os == core.Os.Linux {
        if UnixThread.linux_awake.Load(atomic.SeqCst) == 0 {
            sys.SemPost(&UnixThread.linux_idle, 1)
        } else {
            unsafe "GLFW is initialized for the process and glfwPostEmptyEvent is thread safe" {
                (*Glfw()).post_empty_event()
            }
        }
    }
}

UnixLinuxEntry(arg: *u8) {
    _ = arg
    if !UnixInit() {
        return
    }
    for {
        DrainUnixRequests()
        FlushAllWindows()
        if ThreadWinHead.Load(atomic.Relaxed) == 0 {
            UnixThread.linux_awake.Store(0, atomic.SeqCst)
            // A producer that saw linux_awake still set rang the bell
            // before our store, so this check sees it before we park.
            if !UnixThread.bell.IsPending() {
                _ = sys.SemWait(&UnixThread.linux_idle, -1)
            }
        } else {
            UnixThread.linux_awake.Store(1, atomic.SeqCst)
            if !UnixThread.bell.IsPending() {
                unsafe "the dedicated Linux thread owns initialized GLFW" {
                    (*Glfw()).wait_events()
                }
            }
        }
    }
}

UnixMacPump(): bool {
    if UnixThread.init_state.Load(atomic.Acquire) == ThreadStarting && !UnixInit() {
        return false
    }
    if UnixThread.init_state.Load(atomic.Acquire) != ThreadReady {
        return false
    }
    DrainUnixRequests()
    if ThreadWinHead.Load(atomic.Relaxed) == 0 {
        return false
    }
    unsafe "carrier zero is the macOS main thread and owns initialized GLFW" {
        (*Glfw()).poll_events()
    }
    FlushAllWindows()
    return true
}

DrainUnixRequests() {
    UnixThread.bell.Clear()
    for {
        req := PopReq()
        if req.addr == 0 {
            return
        }
        ProcessUnixRequest(req.addr, req.bits)
        ReleaseWindowRecRef(req.addr)
    }
}
