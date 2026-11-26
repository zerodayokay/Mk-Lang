// Why: a crashing program, often from its MkPanicHook, may want a dump a
// debugger can open.
//
// What: runtime.WriteDump and its errors Unsupported and OsError.
//
// How: on Windows a minidump through std.sys's MiniDump, with dbghelp.dll
// loaded at run time, so no executable imports it unless it calls
// WriteDump. Other targets answer Unsupported.

import std.sys

/// This target cannot write a dump: only Windows can.
export error Unsupported
/// The OS refused, with its code (GetLastError on Windows).
export error OsError(code: i64)

/// Every way a std.runtime operation fails.
export open type Error = Unsupported | OsError

/// Writes a crash dump of this process to path, which is created or
/// truncated. On Windows the dump is a minidump that a debugger opens:
/// every thread's stack and registers, and the list of loaded modules.
/// It runs on the calling stack and needs a few pages of it; the other
/// threads are suspended while they are read.
///
/// Fails with Unsupported on every target but Windows, and on Windows
/// when dbghelp.dll cannot be loaded; with OsError and the OS's code when
/// the file cannot be written. O(the process's threads and modules);
/// allocates nothing of its own.
export WriteDump(path: str): Error!void {
    r := sys.MiniDump(path)
    if r < 0 {
        fail Unsupported
    }
    if r > 0 {
        fail OsError(code: r)
    }
}
