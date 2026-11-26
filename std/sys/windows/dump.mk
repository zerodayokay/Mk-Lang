// Why: runtime.WriteDump must leave a post-mortem record of a Windows
// process that a debugger can open, without making every executable
// depend on dbghelp.dll.
//
// What: MiniDump, which writes a minidump of the whole process to a path.
//
// How: dbghelp.dll's MiniDumpWriteDump, loaded at run time from System32
// on the first call, so no executable imports dbghelp and one that never
// asks for a dump loads nothing new.

import std.raw

// MiniDumpNormal with MiniDumpWithThreadInfo (0x1000): every thread's
// stack and registers, the module list and the thread times.
const MiniDumpType: u32 = 0x1000

// LOAD_LIBRARY_SEARCH_SYSTEM32: dbghelp.dll from System32 only.
const LoadSystem32: u32 = 0x800

/// MiniDump writes a minidump of this process to path, created or
/// truncated: 0, or the OS's error code (GetLastError), or -1 when dbghelp
/// or its entry cannot be loaded. It runs on the calling stack, which
/// MiniDumpWriteDump uses a few pages of; the other threads are suspended
/// while it reads them. O(the process's threads and modules); allocates
/// nothing of its own.
export MiniDump(path: str): i64 {
    var p16: [1024]u16 = [0; 1024]
    if path.Len() >= 1023 || Wide(path, mut p16[..]) < 0 {
        return 123 // ERROR_INVALID_NAME
    }
    // "dbghelp.dll" and its NUL, as UTF-16.
    name: [12]u16 = [100, 98, 103, 104, 101, 108, 112, 46, 100, 108, 108, 0]
    unsafe "kernel32 and dbghelp; p16 and name are NUL-terminated; the entry's type is its documented signature" {
        m := LoadLibraryExW(raw.Data(name[..]), 0, LoadSystem32)
        if m == 0 {
            return -1
        }
        e := GetProcAddress(m, raw.StrData("MiniDumpWriteDump\u{0}"))
        if raw.IsNull(e) {
            return -1
        }
        var ew := u64(raw.Addr(e))
        write := raw.Read(raw.PtrCast[u64, fn(uint, u32, uint, u32, *u8, *u8, *u8): i32](raw.OfMut(mut ew)))
        h := CreateFileW(raw.Data(p16[..]), GenericWrite, 0, raw.Null[u8](), CreateAlways, AttributeNormal, 0)
        if h == InvalidHandle {
            return i64(GetLastError())
        }
        ok := write(GetCurrentProcess(), GetCurrentProcessId(), h, MiniDumpType, raw.Null[u8](), raw.Null[u8](), raw.Null[u8]())
        var err: u32 = 0
        if ok == 0 {
            err = GetLastError()
            if err == 0 {
                err = 31 // ERROR_GEN_FAILURE: dbghelp reported failure with no code
            }
        }
        _ = CloseHandle(h)
        return i64(err)
    }
}
