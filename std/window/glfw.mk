// Why: Unix windows need GLFW's platform backend without linking a specific build.
// What: the dynamically loaded GLFW 3.3 window and input ABI.
// How: resolve required symbols once on the window thread and keep their
// library open for the whole process.

import std.atomic
import std.core
import std.raw
import std.sys

type GlfwClose = fn(uint)
type GlfwPairI = fn(uint, i32, i32)
type GlfwPairF = fn(uint, f32, f32)
type GlfwPairD = fn(uint, f64, f64)
type GlfwFlag = fn(uint, i32)
type GlfwKey = fn(uint, i32, i32, i32, i32)
type GlfwChar = fn(uint, u32)
type GlfwButton = fn(uint, i32, i32, i32)

struct GlfwVideoMode {
    width: i32
    height: i32
    red_bits: i32
    green_bits: i32
    blue_bits: i32
    refresh_rate: i32
}

struct GlfwDispatch {
    init: fn(): i32
    get_error: fn(**u8): i32
    window_hint: fn(i32, i32)
    create_window: fn(i32, i32, *u8, uint, uint): uint
    destroy_window: fn(uint)
    poll_events: fn()
    wait_events: fn()
    post_empty_event: fn()
    set_window_user_pointer: fn(uint, uint)
    get_window_user_pointer: fn(uint): uint
    set_window_should_close: fn(uint, i32)
    get_window_size: fn(uint, *i32, *i32)
    get_framebuffer_size: fn(uint, *i32, *i32)
    get_window_content_scale: fn(uint, *f32, *f32)
    get_window_pos: fn(uint, *i32, *i32)
    get_monitors: fn(*i32): *uint
    get_primary_monitor: fn(): uint
    get_monitor_pos: fn(uint, *i32, *i32)
    get_video_mode: fn(uint): *GlfwVideoMode
    set_window_monitor: fn(uint, uint, i32, i32, i32, i32, i32)
    set_window_title: fn(uint, *u8)
    set_window_size: fn(uint, i32, i32)
    show_window: fn(uint)
    hide_window: fn(uint)
    create_standard_cursor: fn(i32): uint
    destroy_cursor: fn(uint)
    set_cursor: fn(uint, uint)
    set_input_mode: fn(uint, i32, i32)
    get_cursor_pos: fn(uint, *f64, *f64)
    get_key_name: fn(i32, i32): *u8
    set_window_close_callback: fn(uint, GlfwClose): GlfwClose
    set_framebuffer_size_callback: fn(uint, GlfwPairI): GlfwPairI
    set_window_pos_callback: fn(uint, GlfwPairI): GlfwPairI
    set_window_content_scale_callback: fn(uint, GlfwPairF): GlfwPairF
    set_window_focus_callback: fn(uint, GlfwFlag): GlfwFlag
    set_window_iconify_callback: fn(uint, GlfwFlag): GlfwFlag
    set_key_callback: fn(uint, GlfwKey): GlfwKey
    set_char_callback: fn(uint, GlfwChar): GlfwChar
    set_mouse_button_callback: fn(uint, GlfwButton): GlfwButton
    set_cursor_pos_callback: fn(uint, GlfwPairD): GlfwPairD
    set_scroll_callback: fn(uint, GlfwPairD): GlfwPairD
    set_cursor_enter_callback: fn(uint, GlfwFlag): GlfwFlag
}

// The address is published by ThreadReady; calls stay on the window thread
// except for GLFW's thread-safe glfwPostEmptyEvent.
Glfw(): *GlfwDispatch {
    unsafe "the address stays allocated after initialization" {
        return raw.FromAddr[GlfwDispatch](UnixThread.dispatch.Load(atomic.Acquire))
    }
}

GlfwLibrary(): sys.OsResult {
    if Target.os == core.Os.Linux {
        return sys.DlOpen("libglfw.so.3", 0)
    }
    app := sys.DlOpen("libglfw.3.dylib", 1)
    if app.err == core.SysErr.None {
        return app
    }
    system := sys.DlOpen("libglfw.3.dylib", 0)
    if system.err == core.SysErr.None {
        return system
    }
    for path in ["/opt/homebrew/opt/glfw/lib/libglfw.3.dylib", "/usr/local/opt/glfw/lib/libglfw.3.dylib"] {
        r := sys.DlOpen(path, 2)
        if r.err == core.SysErr.None {
            return r
        }
    }
    return system
}

GlfwSymbol(lib: uint, name: str): Unsupported!uint {
    p := sys.DlSymbol(lib, name) else { fail Unsupported }
    addr := raw.Addr(p)
    if addr == 0 {
        fail Unsupported
    }
    return addr
}

BindGlfw(lib: uint): Unsupported!GlfwDispatch {
    unsafe "lib is live and each GLFW symbol uses its C signature" {
        return GlfwDispatch(
            init: raw.FnFromAddr[fn(): i32](try GlfwSymbol(lib, "glfwInit")),
            get_error: raw.FnFromAddr[fn(**u8): i32](try GlfwSymbol(lib, "glfwGetError")),
            window_hint: raw.FnFromAddr[fn(i32, i32)](try GlfwSymbol(lib, "glfwWindowHint")),
            create_window: raw.FnFromAddr[fn(i32, i32, *u8, uint, uint): uint](try GlfwSymbol(lib, "glfwCreateWindow")),
            destroy_window: raw.FnFromAddr[fn(uint)](try GlfwSymbol(lib, "glfwDestroyWindow")),
            poll_events: raw.FnFromAddr[fn()](try GlfwSymbol(lib, "glfwPollEvents")),
            wait_events: raw.FnFromAddr[fn()](try GlfwSymbol(lib, "glfwWaitEvents")),
            post_empty_event: raw.FnFromAddr[fn()](try GlfwSymbol(lib, "glfwPostEmptyEvent")),
            set_window_user_pointer: raw.FnFromAddr[fn(uint, uint)](try GlfwSymbol(lib, "glfwSetWindowUserPointer")),
            get_window_user_pointer: raw.FnFromAddr[fn(uint): uint](try GlfwSymbol(lib, "glfwGetWindowUserPointer")),
            set_window_should_close: raw.FnFromAddr[fn(uint, i32)](try GlfwSymbol(lib, "glfwSetWindowShouldClose")),
            get_window_size: raw.FnFromAddr[fn(uint, *i32, *i32)](try GlfwSymbol(lib, "glfwGetWindowSize")),
            get_framebuffer_size: raw.FnFromAddr[fn(uint, *i32, *i32)](try GlfwSymbol(lib, "glfwGetFramebufferSize")),
            get_window_content_scale: raw.FnFromAddr[fn(uint, *f32, *f32)](try GlfwSymbol(lib, "glfwGetWindowContentScale")),
            get_window_pos: raw.FnFromAddr[fn(uint, *i32, *i32)](try GlfwSymbol(lib, "glfwGetWindowPos")),
            get_monitors: raw.FnFromAddr[fn(*i32): *uint](try GlfwSymbol(lib, "glfwGetMonitors")),
            get_primary_monitor: raw.FnFromAddr[fn(): uint](try GlfwSymbol(lib, "glfwGetPrimaryMonitor")),
            get_monitor_pos: raw.FnFromAddr[fn(uint, *i32, *i32)](try GlfwSymbol(lib, "glfwGetMonitorPos")),
            get_video_mode: raw.FnFromAddr[fn(uint): *GlfwVideoMode](try GlfwSymbol(lib, "glfwGetVideoMode")),
            set_window_monitor: raw.FnFromAddr[fn(uint, uint, i32, i32, i32, i32, i32)](try GlfwSymbol(lib, "glfwSetWindowMonitor")),
            set_window_title: raw.FnFromAddr[fn(uint, *u8)](try GlfwSymbol(lib, "glfwSetWindowTitle")),
            set_window_size: raw.FnFromAddr[fn(uint, i32, i32)](try GlfwSymbol(lib, "glfwSetWindowSize")),
            show_window: raw.FnFromAddr[fn(uint)](try GlfwSymbol(lib, "glfwShowWindow")),
            hide_window: raw.FnFromAddr[fn(uint)](try GlfwSymbol(lib, "glfwHideWindow")),
            create_standard_cursor: raw.FnFromAddr[fn(i32): uint](try GlfwSymbol(lib, "glfwCreateStandardCursor")),
            destroy_cursor: raw.FnFromAddr[fn(uint)](try GlfwSymbol(lib, "glfwDestroyCursor")),
            set_cursor: raw.FnFromAddr[fn(uint, uint)](try GlfwSymbol(lib, "glfwSetCursor")),
            set_input_mode: raw.FnFromAddr[fn(uint, i32, i32)](try GlfwSymbol(lib, "glfwSetInputMode")),
            get_cursor_pos: raw.FnFromAddr[fn(uint, *f64, *f64)](try GlfwSymbol(lib, "glfwGetCursorPos")),
            get_key_name: raw.FnFromAddr[fn(i32, i32): *u8](try GlfwSymbol(lib, "glfwGetKeyName")),
            set_window_close_callback: raw.FnFromAddr[fn(uint, GlfwClose): GlfwClose](try GlfwSymbol(lib, "glfwSetWindowCloseCallback")),
            set_framebuffer_size_callback: raw.FnFromAddr[fn(uint, GlfwPairI): GlfwPairI](try GlfwSymbol(lib, "glfwSetFramebufferSizeCallback")),
            set_window_pos_callback: raw.FnFromAddr[fn(uint, GlfwPairI): GlfwPairI](try GlfwSymbol(lib, "glfwSetWindowPosCallback")),
            set_window_content_scale_callback: raw.FnFromAddr[fn(uint, GlfwPairF): GlfwPairF](try GlfwSymbol(lib, "glfwSetWindowContentScaleCallback")),
            set_window_focus_callback: raw.FnFromAddr[fn(uint, GlfwFlag): GlfwFlag](try GlfwSymbol(lib, "glfwSetWindowFocusCallback")),
            set_window_iconify_callback: raw.FnFromAddr[fn(uint, GlfwFlag): GlfwFlag](try GlfwSymbol(lib, "glfwSetWindowIconifyCallback")),
            set_key_callback: raw.FnFromAddr[fn(uint, GlfwKey): GlfwKey](try GlfwSymbol(lib, "glfwSetKeyCallback")),
            set_char_callback: raw.FnFromAddr[fn(uint, GlfwChar): GlfwChar](try GlfwSymbol(lib, "glfwSetCharCallback")),
            set_mouse_button_callback: raw.FnFromAddr[fn(uint, GlfwButton): GlfwButton](try GlfwSymbol(lib, "glfwSetMouseButtonCallback")),
            set_cursor_pos_callback: raw.FnFromAddr[fn(uint, GlfwPairD): GlfwPairD](try GlfwSymbol(lib, "glfwSetCursorPosCallback")),
            set_scroll_callback: raw.FnFromAddr[fn(uint, GlfwPairD): GlfwPairD](try GlfwSymbol(lib, "glfwSetScrollCallback")),
            set_cursor_enter_callback: raw.FnFromAddr[fn(uint, GlfwFlag): GlfwFlag](try GlfwSymbol(lib, "glfwSetCursorEnterCallback")),
        )
    }
}
