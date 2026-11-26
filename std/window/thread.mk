// Why: Win32 windows belong to the OS thread that created them and modal
// move/resize loops block that thread inside DispatchMessageW, so std.window
// runs one dedicated OS window thread for all windows so rendering tasks never
// stall.
//
// What: WindowRec (shared per-window heap record), WinThread (singleton thread
// state and HWND_MESSAGE doorbell), the shared intrusive request queue,
// EnsureWindowThread, OpenWindow, EnqueueReq, ReleaseWindowRecRef and
// FlushWindow/FlushAllWindows.
//
// How: sys.ThreadStart starts WinThreadEntry with a 1 MiB stack; it sets
// PER_MONITOR_AWARE_V2 DPI awareness, registers window classes, creates the
// message-only doorbell window, and pumps GetMessageW/PeekMessageW/Dispatch.

import c.windows
import std.atomic
import std.core
import std.mem
import std.raw
import std.runtime
import std.sys
import std.time

// Request bitmasks coalesced per WindowRec inside WindowRequests.
const ReqBitOpen: u32 = 1
const ReqBitSetters: u32 = 2
const ReqBitHide: u32 = 4
const ReqBitDestroy: u32 = 8

// Window thread initialization states.
const ThreadUnstarted: u32 = 0
const ThreadStarting: u32 = 1
const ThreadReady: u32 = 2
const ThreadFailed: u32 = 3

// Open request states.
const OpenPending: u32 = 0
const OpenOk: u32 = 1
const OpenFailed: u32 = 2
const OpenAbandoned: u32 = 3

// Timer ID for 8 ms modal-loop accumulator flushes.
const ModalTimerId: u64 = 1
const ModalTimerMs: u32 = 8

struct ReqList {
    head: uint
    tail: uint
}

struct WinThreadState {
    init_state: atomic.Atomic[u32]
    init_err: atomic.Atomic[i64]
    msg_hwnd: atomic.Atomic[uint]
    hinstance: atomic.Atomic[uint]
    bell: Doorbell
}

var WinThread: WinThreadState = WinThreadState(
    init_state: atomic.Atomic[u32].New(ThreadUnstarted),
    init_err: atomic.Atomic[i64].New(0),
    msg_hwnd: atomic.Atomic[uint].New(0),
    hinstance: atomic.Atomic[uint].New(0),
    bell: DoorbellNew(),
)

var WindowRequests: atomic.Spin[ReqList] = atomic.Spin[ReqList].New(ReqList(head: 0, tail: 0))

// Intrusive list head of live windows, touched ONLY by the window thread.
var ThreadWinHead: atomic.Atomic[uint] = atomic.Atomic[uint].New(0)

// Count of allocated WindowRec blocks not yet freed (for test cleanup sync).
var LiveWindowRecs: atomic.Atomic[u32] = atomic.Atomic[u32].New(0)

// Shared per-window record allocated in Heap and freed when refs hits zero.
struct WindowRec {
    heap: &Heap
    refs: atomic.Atomic[u32]
    alive_claims: atomic.Atomic[u32]
    open_state: atomic.Atomic[u32]
    open_err: atomic.Atomic[i64]
    open_waker: runtime.Waker
    event_waker: runtime.Waker
    width: atomic.Atomic[int]
    height: atomic.Atomic[int]
    scale_bits: atomic.Atomic[u32]
    close_requested: atomic.Atomic[u8]
    native_handle: atomic.Atomic[uint]
    hinst_val: atomic.Atomic[uint]
    held_keys: [4]atomic.Atomic[u64]
    held_buttons: atomic.Atomic[u8]
    in_sizemove_flag: atomic.Atomic[u8]
    ring: Ring
    setters: SetterState
    req_next: uint
    req_pending: u32
    req_queued: bool
    init_title: [255]u8
    init_title_len: u8
    init_w: int
    init_h: int
    init_resizable: bool
    init_fullscreen: Fullscreen
    init_visible: bool
    // Fields below are owned exclusively by the window thread.
    hwnd: windows.Hwnd
    win_next: uint
    acc: Accumulator
    open_ready: bool
    minimized: bool
    last_w: int
    last_h: int
    hovering: bool
    timer_active: bool
    high_surrogate: u32
    buttons_down_mask: u8
    cursor_shape: Cursor
    cursor_mode: CursorMode
    hcursor: windows.Hcursor
    unix_visible: bool
    unix_fullscreen: bool
    unix_cursor: uint
    unix_saved: [4]i32
    unix_pixel_scale: [2]f64
    is_fullscreen: bool
    saved_style: i64
    saved_ex_style: i64
    saved_placement: windows.Windowplacement
}

// InstantFromNanos constructs a time.Instant from monotonic nanoseconds.
InstantFromNanos(nanos: i64): time.Instant {
    var n := nanos
    unsafe "time.Instant is a single-field struct holding nanos: i64 of 8 bytes" {
        return raw.Read(raw.PtrCast[i64, time.Instant](raw.OfMut(mut n)))
    }
}

// ReleaseWindowRecRef drops one reference on WindowRec and frees it at zero.
ReleaseWindowRecRef(addr: uint) {
    if addr == 0 {
        return
    }
    unsafe "addr points to a live WindowRec with refs >= 1" {
        r := raw.FromAddr[WindowRec](addr)
        prev := (*r).refs.FetchSub(1, atomic.AcqRel)
        if prev == 1 {
            h := (*r).heap
            ring := raw.Read[Ring](raw.Of(&(*r).ring))
            ring.RingClose(h)
            h.Alloc().FreeBytes(raw.PtrCast[WindowRec, u8](r), SizeOf[WindowRec](), AlignOf[WindowRec]())
            _ = LiveWindowRecs.FetchSub(1, atomic.AcqRel)
        }
    }
}

// EnqueueReq adds bit to rec's pending requests and rings the doorbell on 0->1.
EnqueueReq(rec_addr: uint, bit: u32) {
    if !WindowSupported() {
        return
    }
    if rec_addr == 0 {
        return
    }
    var g := WindowRequests.Lock()
    list := g.Mut()
    unsafe "rec_addr points to a live WindowRec pinned by refs >= 1" {
        r := raw.FromAddr[WindowRec](rec_addr)
        (*r).req_pending = (*r).req_pending | bit
        if !(*r).req_queued {
            // The queue owns a reference until ProcessReq finishes.
            _ = (*r).refs.FetchAdd(1, atomic.AcqRel)
            (*r).req_queued = true
            (*r).req_next = 0
            if list.tail == 0 {
                list.head = rec_addr
                list.tail = rec_addr
            } else {
                tail_r := raw.FromAddr[WindowRec](list.tail)
                (*tail_r).req_next = rec_addr
                list.tail = rec_addr
            }
        }
    }
    g.Unlock()
    if Target.os == core.Os.Linux || Target.os == core.Os.Macos {
        UnixWake()
        return
    }
    if Target.os == core.Os.Windows && WinThread.bell.Ring() {
        mh := WinThread.msg_hwnd.Load(atomic.Acquire)
        if mh != 0 {
            unsafe "mh is the live HWND_MESSAGE doorbell window" {
                _ = windows.PostMessageW(raw.FromAddr[windows.HwndStruct](mh), Trunc[u32](windows.WmApp), 0, 0)
            }
        }
    }
}

// PopReq removes the oldest WindowRec from the request queue, or returns 0.
PopReq(): (addr: uint, bits: u32) {
    var g := WindowRequests.Lock()
    list := g.Mut()
    if list.head == 0 {
        g.Unlock()
        return (addr: 0, bits: 0)
    }
    addr := list.head
    var bits: u32 = 0
    unsafe "addr is a live WindowRec in the request queue with refs >= 1" {
        r := raw.FromAddr[WindowRec](addr)
        next := (*r).req_next
        list.head = next
        if next == 0 {
            list.tail = 0
        }
        (*r).req_next = 0
        (*r).req_queued = false
        bits = (*r).req_pending
        (*r).req_pending = 0
    }
    g.Unlock()
    return (addr: addr, bits: bits)
}

// EnsureWindowThread starts the singleton Win32 window thread on first call.
EnsureWindowThread(io: Io): Error!void {
    if Target.os != core.Os.Windows {
        fail Unsupported
    }
    for {
        st := WinThread.init_state.Load(atomic.Acquire)
        if st == ThreadReady {
            return
        }
        if st == ThreadFailed {
            fail Os(code: WinThread.init_err.Load(atomic.Acquire))
        }
        if st == ThreadUnstarted {
            ce := WinThread.init_state.CompareExchange(ThreadUnstarted, ThreadStarting, atomic.AcqRel, atomic.Acquire)
            if ce.ok {
                res := sys.ThreadStart(WinThreadEntry, raw.Null[u8](), 1 << 20)
                if res.err != core.SysErr.None {
                    WinThread.init_err.Store(res.os, atomic.Release)
                    WinThread.init_state.Store(ThreadFailed, atomic.Release)
                    WinThread.init_state.Wake(true)
                    fail Os(code: res.os)
                }
                try WinThread.init_state.Wait(io, ThreadStarting)
                continue
            }
        }
        try WinThread.init_state.Wait(io, ThreadStarting)
    }
}

// OpenWindow allocates a WindowRec, sends ReqBitOpen and waits for creation.
OpenWindow(heap: &Heap, io: Io, o: WindowOptions): Error!Window from heap {
    if Target.os == core.Os.Linux || Target.os == core.Os.Macos {
        try EnsureUnixWindowThread(io)
    } else {
        try EnsureWindowThread(io)
    }
    ring := try RingNew(heap, o.events)
    var ptr: *u8 = raw.Null[u8]()
    unsafe "allocates one WindowRec block from heap" {
        ptr = heap.Alloc().Bytes(SizeOf[WindowRec](), AlignOf[WindowRec]())
    }
    if raw.IsNull(ptr) {
        ring.RingClose(heap)
        fail OutOfMemory
    }
    var rec_ptr: *WindowRec = raw.Null[WindowRec]()
    unsafe "ptr is non-null and aligned for WindowRec" {
        rec_ptr = raw.PtrCast[u8, WindowRec](ptr)
    }
    rec_addr := raw.Addr(rec_ptr)

    // Copy up to 255 UTF-8 title bytes safely.
    src := o.title.AsBytes()
    var tlen := src.Len()
    if tlen > 255 {
        tlen = 255
        for tlen > 0 && (src[tlen] & 0xC0) == 0x80 {
            tlen -= 1
        }
    }
    var tbuf: [255]u8 = [0; 255]
    for i in 0..tlen {
        tbuf[i] = src[i]
    }

    unsafe "rec_ptr is freshly allocated and unshared until EnqueueReq" {
        _ = LiveWindowRecs.FetchAdd(1, atomic.AcqRel)
        raw.Write(rec_ptr, WindowRec(
            heap: heap,
            refs: atomic.Atomic[u32].New(2),
            alive_claims: atomic.Atomic[u32].New(1),
            open_state: atomic.Atomic[u32].New(OpenPending),
            open_err: atomic.Atomic[i64].New(0),
            open_waker: runtime.Waker.New(),
            event_waker: runtime.Waker.New(),
            width: atomic.Atomic[int].New(0),
            height: atomic.Atomic[int].New(0),
            scale_bits: atomic.Atomic[u32].New(f32(1.0).Bits()),
            close_requested: atomic.Atomic[u8].New(0),
            native_handle: atomic.Atomic[uint].New(0),
            hinst_val: atomic.Atomic[uint].New(0),
            held_keys: [
                atomic.Atomic[u64].New(0),
                atomic.Atomic[u64].New(0),
                atomic.Atomic[u64].New(0),
                atomic.Atomic[u64].New(0),
            ],
            held_buttons: atomic.Atomic[u8].New(0),
            in_sizemove_flag: atomic.Atomic[u8].New(0),
            ring: ring,
            setters: SetterStateNew(),
            req_next: 0,
            req_pending: 0,
            req_queued: false,
            init_title: tbuf,
            init_title_len: Trunc[u8](tlen),
            init_w: o.width,
            init_h: o.height,
            init_resizable: o.resizable,
            init_fullscreen: o.fullscreen,
            init_visible: o.visible,
            hwnd: raw.Null[windows.HwndStruct](),
            win_next: 0,
            acc: AccumulatorNew(),
            open_ready: false,
            minimized: false,
            last_w: 0,
            last_h: 0,
            hovering: false,
            timer_active: false,
            high_surrogate: 0,
            buttons_down_mask: 0,
            cursor_shape: Cursor.Arrow,
            cursor_mode: CursorMode.Normal,
            hcursor: raw.Null[windows.HiconStruct](),
            unix_visible: false,
            unix_fullscreen: false,
            unix_cursor: 0,
            unix_saved: [0; 4],
            unix_pixel_scale: [1.0; 2],
            is_fullscreen: false,
            saved_style: 0,
            saved_ex_style: 0,
            saved_placement: mem.Zeroed[windows.Windowplacement](),
        ))
    }

    EnqueueReq(rec_addr, ReqBitOpen)

    for {
        var st: u32 = OpenPending
        unsafe "rec_ptr stays live until ReleaseWindowRecRef below" {
            st = (*rec_ptr).open_state.Load(atomic.Acquire)
        }
        if st == OpenOk {
            return Window(heap: heap, rec: Cell[uint].New(rec_addr))
        }
        if st == OpenFailed {
            var err_code: i64 = 0
            unsafe "rec_ptr is live while we hold our ref" {
                err_code = (*rec_ptr).open_err.Load(atomic.Acquire)
            }
            ReleaseWindowRecRef(rec_addr)
            fail Os(code: err_code)
        }
        unsafe "rec_ptr stays live until ReleaseWindowRecRef below" {
            (*rec_ptr).open_waker.Wait(io) else {
                prev := (*rec_ptr).open_state.Swap(OpenAbandoned, atomic.AcqRel)
                if prev == OpenOk {
                    EnqueueReq(rec_addr, ReqBitDestroy)
                }
                ReleaseWindowRecRef(rec_addr)
                fail Canceled
            }
        }
    }
}

// Utf8ToUtf16 decodes up to 255 UTF-8 bytes into a NUL-terminated UTF-16 buffer.
Utf8ToUtf16(src: []u8, dst: mut [256]u16) {
    n := src.Len()
    var i := 0
    var out := 0
    for i < n && out < 254 {
        b0 := u32(src[i])
        if b0 < 0x80 {
            dst[out] = Trunc[u16](b0)
            out += 1
            i += 1
        } else if (b0 & 0xE0) == 0xC0 && i + 1 < n {
            cp := ((b0 & 0x1F) << 6) | (u32(src[i + 1]) & 0x3F)
            dst[out] = Trunc[u16](cp)
            out += 1
            i += 2
        } else if (b0 & 0xF0) == 0xE0 && i + 2 < n {
            cp := ((b0 & 0x0F) << 12) | ((u32(src[i + 1]) & 0x3F) << 6) | (u32(src[i + 2]) & 0x3F)
            dst[out] = Trunc[u16](cp)
            out += 1
            i += 3
        } else if (b0 & 0xF8) == 0xF0 && i + 3 < n && out < 253 {
            cp := ((b0 & 0x07) << 18) | ((u32(src[i + 1]) & 0x3F) << 12) | ((u32(src[i + 2]) & 0x3F) << 6) | (u32(src[i + 3]) & 0x3F)
            v := cp - 0x10000
            dst[out] = Trunc[u16](0xD800 + (v >> 10))
            dst[out + 1] = Trunc[u16](0xDC00 + (v & 0x3FF))
            out += 2
            i += 4
        } else {
            dst[out] = 0xFFFD
            out += 1
            i += 1
        }
    }
    dst[out] = 0
}

// WinThreadEntry runs the dedicated Win32 message-pump thread.
WinThreadEntry(arg: *u8) {
    _ = arg
    if Target.os != core.Os.Windows {
        return
    }
    // Class names: "MkWindowDoorbell" and "MkWindow".
    doorbell_cls: [17]u16 = [77, 107, 87, 105, 110, 100, 111, 119, 68, 111, 111, 114, 98, 101, 108, 108, 0]
    win_cls: [9]u16 = [77, 107, 87, 105, 110, 100, 111, 119, 0]

    unsafe "sets Per-Monitor v2 DPI awareness, registers Win32 classes and creates HWND_MESSAGE" {
        _ = windows.SetThreadDpiAwarenessContext(raw.FromAddr[windows.DpiAwarenessContextStruct](windows.DpiAwarenessContextPerMonitorAwareV2))
        hinst := windows.GetModuleHandleW(raw.Null[u16]())
        WinThread.hinstance.Store(raw.Addr(hinst), atomic.Release)

        var wc_msg := mem.Zeroed[windows.Wndclassexw]()
        wc_msg.cb_size = Trunc[u32](SizeOf[windows.Wndclassexw]())
        wc_msg.lpfn_wnd_proc = some(MsgWndProc)
        wc_msg.h_instance = hinst
        wc_msg.lpsz_class_name = raw.Data(doorbell_cls[..])
        if windows.RegisterClassExW(raw.Of(&wc_msg)) == 0 {
            WinThread.init_err.Store(i64(windows.GetLastError()), atomic.Release)
            WinThread.init_state.Store(ThreadFailed, atomic.Release)
            WinThread.init_state.Wake(true)
            return
        }

        var wc_app := mem.Zeroed[windows.Wndclassexw]()
        wc_app.cb_size = Trunc[u32](SizeOf[windows.Wndclassexw]())
        wc_app.style = Trunc[u32](windows.CsOwndc)
        wc_app.lpfn_wnd_proc = some(AppWndProc)
        wc_app.h_instance = hinst
        wc_app.h_cursor = windows.LoadCursorW(raw.Null[windows.HinstanceStruct](), raw.FromAddr[u16](windows.IdcArrow))
        wc_app.lpsz_class_name = raw.Data(win_cls[..])
        if windows.RegisterClassExW(raw.Of(&wc_app)) == 0 {
            WinThread.init_err.Store(i64(windows.GetLastError()), atomic.Release)
            WinThread.init_state.Store(ThreadFailed, atomic.Release)
            WinThread.init_state.Wake(true)
            return
        }

        mh := windows.CreateWindowExW(
            0,
            raw.Data(doorbell_cls[..]),
            raw.Data(doorbell_cls[..]),
            0,
            0,
            0,
            0,
            0,
            raw.FromAddr[windows.HwndStruct](windows.HwndMessage),
            raw.Null[windows.HmenuStruct](),
            hinst,
            raw.Null[void](),
        )
        if raw.IsNull(mh) {
            WinThread.init_err.Store(i64(windows.GetLastError()), atomic.Release)
            WinThread.init_state.Store(ThreadFailed, atomic.Release)
            WinThread.init_state.Wake(true)
            return
        }

        WinThread.msg_hwnd.Store(raw.Addr(mh), atomic.Release)
        WinThread.init_state.Store(ThreadReady, atomic.Release)
        WinThread.init_state.Wake(true)

        var msg := mem.Zeroed[windows.Msg]()
        for windows.GetMessageW(raw.OfMut(mut msg), raw.Null[windows.HwndStruct](), 0, 0) > 0 {
            _ = windows.TranslateMessage(raw.Of(&msg))
            _ = windows.DispatchMessageW(raw.Of(&msg))
            for windows.PeekMessageW(raw.OfMut(mut msg), raw.Null[windows.HwndStruct](), 0, 0, Trunc[u32](windows.PmRemove)) != 0 {
                if msg.message == Trunc[u32](windows.WmQuit) {
                    return
                }
                _ = windows.TranslateMessage(raw.Of(&msg))
                _ = windows.DispatchMessageW(raw.Of(&msg))
            }
            FlushAllWindows()
        }
    }
}

// MsgWndProc handles doorbell WM_APP notifications on the message-only window.
MsgWndProc(hwnd: windows.Hwnd, msg: u32, wp: windows.Wparam, lp: windows.Lparam): windows.Lresult {
    if Target.os != core.Os.Windows {
        return 0
    }
    if msg == Trunc[u32](windows.WmApp) {
        DrainRequests()
        return 0
    }
    unsafe "DefWindowProcW is the default Win32 handler" {
        return windows.DefWindowProcW(hwnd, msg, wp, lp)
    }
}

// DrainRequests clears the doorbell and processes all queued WindowRec requests.
DrainRequests() {
    if Target.os != core.Os.Windows {
        return
    }
    WinThread.bell.Clear()
    for {
        popped := PopReq()
        if popped.addr == 0 {
            break
        }
        ProcessReq(popped.addr, popped.bits)
        ReleaseWindowRecRef(popped.addr)
    }
}

// ProcessReq executes pending Open, Setters, Hide and Destroy bits for rec.
ProcessReq(addr: uint, bits: u32) {
    if Target.os != core.Os.Windows {
        return
    }
    if (bits & ReqBitOpen) != 0 {
        ProcessReqOpen(addr)
    }
    if (bits & ReqBitSetters) != 0 {
        if (bits & ReqBitDestroy) == 0 {
            ProcessReqSetters(addr)
        }
    }
    if (bits & ReqBitHide) != 0 && (bits & ReqBitDestroy) == 0 {
        unsafe "addr is live on the window thread until ReqBitDestroy" {
            r := raw.FromAddr[WindowRec](addr)
            if !raw.IsNull((*r).hwnd) {
                _ = windows.ShowWindow((*r).hwnd, windows.SwHide)
            }
        }
    }
    if (bits & ReqBitDestroy) != 0 {
        ProcessReqDestroy(addr)
    }
}

// ThreadLinkWindow prepends rec onto the window-thread-only active list.
ThreadLinkWindow(addr: uint) {
    unsafe "called only on the window thread for a live WindowRec" {
        r := raw.FromAddr[WindowRec](addr)
        (*r).win_next = ThreadWinHead.Load(atomic.Relaxed)
        ThreadWinHead.Store(addr, atomic.Relaxed)
    }
}

// ThreadUnlinkWindow removes rec from the window-thread-only active list.
ThreadUnlinkWindow(addr: uint) {
    unsafe "called only on the window thread" {
        var prev: uint = 0
        var cur := ThreadWinHead.Load(atomic.Relaxed)
        for cur != 0 {
            r := raw.FromAddr[WindowRec](cur)
            next := (*r).win_next
            if cur == addr {
                if prev == 0 {
                    ThreadWinHead.Store(next, atomic.Relaxed)
                } else {
                    pr := raw.FromAddr[WindowRec](prev)
                    (*pr).win_next = next
                }
                (*r).win_next = 0
                return
            }
            prev = cur
            cur = next
        }
    }
}

// FlushWindow flushes rec's coalescing accumulator and wakes the consumer if any.
FlushWindow(addr: uint) {
    unsafe "called only on the window thread for a live WindowRec" {
        r := raw.FromAddr[WindowRec](addr)
        if (*r).acc.Flush(&(*r).ring) {
            (*r).event_waker.Wake()
        }
    }
}

// FlushAllWindows flushes every live window's accumulator after queue drain.
FlushAllWindows() {
    var cur := ThreadWinHead.Load(atomic.Relaxed)
    for cur != 0 {
        FlushWindow(cur)
        unsafe "ThreadWinHead links live WindowRecs on the window thread" {
            cur = (*raw.FromAddr[WindowRec](cur)).win_next
        }
    }
}

// WindowStyleFor returns the Win32 style flags for a windowed window.
WindowStyleFor(resizable: bool): u32 {
    if Target.os != core.Os.Windows {
        return 0
    }
    base := windows.WsOverlappedwindow
    if !resizable {
        return Trunc[u32](base & ((windows.WsThickframe | windows.WsMaximizebox) ^ -1))
    }
    return Trunc[u32](base)
}

// LoadCursorShape returns the standard Win32 cursor handle for c.
LoadCursorShape(c: Cursor): windows.Hcursor {
    if Target.os != core.Os.Windows {
        unsafe "never called off Windows" {
            return raw.Null[windows.HiconStruct]()
        }
    }
    var id: uint = windows.IdcArrow
    match c {
        Cursor.Arrow { id = windows.IdcArrow }
        Cursor.Text { id = windows.IdcIbeam }
        Cursor.Hand { id = windows.IdcHand }
        Cursor.Crosshair { id = windows.IdcCross }
        Cursor.ResizeHorizontal { id = windows.IdcSizewe }
        Cursor.ResizeVertical { id = windows.IdcSizens }
        Cursor.NotAllowed { id = windows.IdcNo }
        _ { id = windows.IdcArrow }
    }
    unsafe "LoadCursorW loads a system shared cursor resource" {
        return windows.LoadCursorW(raw.Null[windows.HinstanceStruct](), raw.FromAddr[u16](id))
    }
}

// ApplyBorderlessFullscreen switches hwnd between windowed and borderless fullscreen.
ApplyBorderlessFullscreen(r: *WindowRec, enable: bool) {
    if Target.os != core.Os.Windows {
        return
    }
    unsafe "r is a live WindowRec on the window thread with a valid hwnd" {
        hwnd := (*r).hwnd
        if raw.IsNull(hwnd) || (*r).is_fullscreen == enable {
            return
        }
        cur_style := windows.GetWindowLongPtrW(hwnd, windows.GwlStyle)
        vis_bit := cur_style & i64(windows.WsVisible)
        if enable {
            (*r).saved_style = cur_style
            (*r).saved_ex_style = windows.GetWindowLongPtrW(hwnd, windows.GwlExstyle)
            var wp := mem.Zeroed[windows.Windowplacement]()
            wp.length = Trunc[u32](SizeOf[windows.Windowplacement]())
            _ = windows.GetWindowPlacement(hwnd, raw.OfMut(mut wp))
            (*r).saved_placement = wp

            mon := windows.MonitorFromWindow(hwnd, Trunc[u32](windows.MonitorDefaulttonearest))
            var mi := mem.Zeroed[windows.Monitorinfo]()
            mi.cb_size = Trunc[u32](SizeOf[windows.Monitorinfo]())
            if windows.GetMonitorInfoW(mon, raw.OfMut(mut mi)) != 0 {
                pop_style := (cur_style & (i64(windows.WsOverlappedwindow) ^ -1)) | i64(windows.WsPopup) | vis_bit
                _ = windows.SetWindowLongPtrW(hwnd, windows.GwlStyle, pop_style)
                rc := mi.rc_monitor
                flags := Trunc[u32](windows.SwpNoownerzorder | windows.SwpFramechanged | windows.SwpNoactivate)
                _ = windows.SetWindowPos(
                    hwnd,
                    raw.FromAddr[windows.HwndStruct](windows.HwndTop),
                    rc.left,
                    rc.top,
                    rc.right - rc.left,
                    rc.bottom - rc.top,
                    flags,
                )
                (*r).is_fullscreen = true
            }
        } else {
            restored := ((*r).saved_style & (i64(windows.WsVisible) ^ -1)) | vis_bit
            _ = windows.SetWindowLongPtrW(hwnd, windows.GwlStyle, restored)
            var wp := (*r).saved_placement
            _ = windows.SetWindowPlacement(hwnd, raw.Of(&wp))
            flags := Trunc[u32](windows.SwpNomove | windows.SwpNosize | windows.SwpNozorder | windows.SwpNoownerzorder | windows.SwpFramechanged | windows.SwpNoactivate)
            _ = windows.SetWindowPos(hwnd, raw.Null[windows.HwndStruct](), 0, 0, 0, 0, flags)
            (*r).is_fullscreen = false
        }
    }
}

// ProcessReqOpen creates the Win32 window for addr on the window thread.
ProcessReqOpen(addr: uint) {
    if Target.os != core.Os.Windows {
        return
    }
    win_cls: [9]u16 = [77, 107, 87, 105, 110, 100, 111, 119, 0]
    unsafe "addr points to a live WindowRec owned by the open request" {
        r := raw.FromAddr[WindowRec](addr)
        if (*r).open_state.Load(atomic.Acquire) == OpenAbandoned {
            ReleaseWindowRecRef(addr)
            return
        }
        hinst := raw.FromAddr[windows.HinstanceStruct](WinThread.hinstance.Load(atomic.Acquire))
        var title_w: [256]u16 = [0; 256]
        tlen := int((*r).init_title_len)
        Utf8ToUtf16((*r).init_title[..tlen], mut title_w)

        style := WindowStyleFor((*r).init_resizable)
        hwnd := windows.CreateWindowExW(
            0,
            raw.Data(win_cls[..]),
            raw.Data(title_w[..]),
            style,
            windows.CwUsedefault,
            windows.CwUsedefault,
            1280,
            720,
            raw.Null[windows.HwndStruct](),
            raw.Null[windows.HmenuStruct](),
            hinst,
            raw.Null[void](),
        )
        if raw.IsNull(hwnd) {
            (*r).open_err.Store(i64(windows.GetLastError()), atomic.Release)
            (*r).open_state.Store(OpenFailed, atomic.Release)
            (*r).open_waker.Wake()
            ReleaseWindowRecRef(addr)
            return
        }

        _ = windows.SetWindowLongPtrW(hwnd, windows.GwlpUserdata, Trunc[i64](addr))
        (*r).hwnd = hwnd
        (*r).native_handle.Store(raw.Addr(hwnd), atomic.Release)
        (*r).hinst_val.Store(raw.Addr(hinst), atomic.Release)
        (*r).hcursor = LoadCursorShape(Cursor.Arrow)

        var dpi := windows.GetDpiForWindow(hwnd)
        if dpi == 0 {
            dpi = Trunc[u32](windows.UserDefaultScreenDpi)
        }
        scale := Cast[f32](dpi) / 96.0
        (*r).scale_bits.Store(scale.Bits(), atomic.Release)

        target_w := if (*r).init_w > 0 { (*r).init_w } else { int((1280 * i64(dpi) + 48) / 96) }
        target_h := if (*r).init_h > 0 { (*r).init_h } else { int((720 * i64(dpi) + 48) / 96) }

        var rect := windows.Rect(left: 0, top: 0, right: Trunc[i32](target_w), bottom: Trunc[i32](target_h))
        _ = windows.AdjustWindowRectExForDpi(raw.OfMut(mut rect), style, 0, 0, dpi)
        flags := Trunc[u32](windows.SwpNomove | windows.SwpNozorder | windows.SwpNoactivate)
        _ = windows.SetWindowPos(hwnd, raw.Null[windows.HwndStruct](), 0, 0, rect.right - rect.left, rect.bottom - rect.top, flags)

        match (*r).init_fullscreen {
            Fullscreen.Borderless { ApplyBorderlessFullscreen(r, true) }
            _ {}
        }

        var cr := mem.Zeroed[windows.Rect]()
        _ = windows.GetClientRect(hwnd, raw.OfMut(mut cr))
        cw := int(cr.right - cr.left)
        ch := int(cr.bottom - cr.top)
        (*r).width.Store(cw, atomic.Release)
        (*r).height.Store(ch, atomic.Release)
        (*r).last_w = cw
        (*r).last_h = ch
        (*r).open_ready = true

        ce := (*r).open_state.CompareExchange(OpenPending, OpenOk, atomic.AcqRel, atomic.Acquire)
        if !ce.ok {
            _ = windows.SetWindowLongPtrW(hwnd, windows.GwlpUserdata, 0)
            _ = windows.DestroyWindow(hwnd)
            (*r).hwnd = raw.Null[windows.HwndStruct]()
            (*r).native_handle.Store(0, atomic.Release)
            ReleaseWindowRecRef(addr)
            return
        }

        ThreadLinkWindow(addr)
        if (*r).init_visible {
            _ = windows.ShowWindow(hwnd, windows.SwShow)
        }
        (*r).open_waker.Wake()
    }
}

// ProcessReqSetters applies coalesced SetterState updates to hwnd.
ProcessReqSetters(addr: uint) {
    if Target.os != core.Os.Windows {
        return
    }
    unsafe "addr points to a live WindowRec on the window thread" {
        r := raw.FromAddr[WindowRec](addr)
        hwnd := (*r).hwnd
        if raw.IsNull(hwnd) {
            return
        }
        snap := (*r).setters.Take()
        if (snap.dirty & DirtyTitle) != 0 {
            var title_w: [256]u16 = [0; 256]
            Utf8ToUtf16(snap.title_buf[..int(snap.title_len)], mut title_w)
            _ = windows.SetWindowTextW(hwnd, raw.Data(title_w[..]))
        }
        if (snap.dirty & DirtyFullscreen) != 0 {
            var want_fs := false
            match snap.fullscreen {
                Fullscreen.Borderless { want_fs = true }
                _ {}
            }
            ApplyBorderlessFullscreen(r, want_fs)
        }
        if (snap.dirty & DirtySize) != 0 && !(*r).is_fullscreen && snap.width > 0 && snap.height > 0 {
            var dpi := windows.GetDpiForWindow(hwnd)
            if dpi == 0 {
                dpi = Trunc[u32](windows.UserDefaultScreenDpi)
            }
            style := Trunc[u32](windows.GetWindowLongPtrW(hwnd, windows.GwlStyle))
            ex_style := Trunc[u32](windows.GetWindowLongPtrW(hwnd, windows.GwlExstyle))
            var rect := windows.Rect(left: 0, top: 0, right: Trunc[i32](snap.width), bottom: Trunc[i32](snap.height))
            _ = windows.AdjustWindowRectExForDpi(raw.OfMut(mut rect), style, 0, ex_style, dpi)
            flags := Trunc[u32](windows.SwpNomove | windows.SwpNozorder | windows.SwpNoactivate)
            _ = windows.SetWindowPos(hwnd, raw.Null[windows.HwndStruct](), 0, 0, rect.right - rect.left, rect.bottom - rect.top, flags)
        }
        if (snap.dirty & DirtyCursor) != 0 {
            (*r).cursor_shape = snap.cursor
            (*r).hcursor = LoadCursorShape(snap.cursor)
            if (*r).hovering && (*r).cursor_mode == CursorMode.Normal {
                _ = windows.SetCursor((*r).hcursor)
            }
        }
        if (snap.dirty & DirtyCursorMode) != 0 {
            (*r).cursor_mode = snap.cursor_mode
            if (*r).hovering {
                if (*r).cursor_mode == CursorMode.Hidden {
                    _ = windows.SetCursor(raw.Null[windows.HiconStruct]())
                } else {
                    _ = windows.SetCursor((*r).hcursor)
                }
            }
        }
        if (snap.dirty & DirtyVisible) != 0 {
            _ = windows.ShowWindow(hwnd, if snap.visible { windows.SwShow } else { windows.SwHide })
        }
    }
}

// ProcessReqDestroy destroys hwnd, unlinks rec and releases the thread's ref.
ProcessReqDestroy(addr: uint) {
    if Target.os != core.Os.Windows {
        return
    }
    ThreadUnlinkWindow(addr)
    unsafe "addr points to a live WindowRec whose thread ref is held until ReleaseWindowRecRef" {
        r := raw.FromAddr[WindowRec](addr)
        hwnd := (*r).hwnd
        if !raw.IsNull(hwnd) {
            if (*r).timer_active {
                _ = windows.KillTimer(hwnd, ModalTimerId)
                (*r).timer_active = false
            }
            _ = windows.SetWindowLongPtrW(hwnd, windows.GwlpUserdata, 0)
            _ = windows.DestroyWindow(hwnd)
            (*r).hwnd = raw.Null[windows.HwndStruct]()
            (*r).native_handle.Store(0, atomic.Release)
        }
    }
    ReleaseWindowRecRef(addr)
}
