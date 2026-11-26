// Why: plugins and optional system libraries are found only at run time,
// so a program needs to load a shared library and look up its symbols.
//
// What: Open, Library with Symbol, Close and Detach, the Search modes and
// the module's errors. Provisional: may change in a minor release.
//
// How: loading a library at run time, looking up an exported name as a raw
// address, and releasing the library. No unsafe: the loader calls are
// std.sys's (sys/<os>/dl.mk), and calling an address Symbol gives is the
// caller's unsafe. Library holds one plain word, so it is Send and Share by the
// kind rules.

import std.sys

/// No library of that name where the Search looks. On Windows this is
/// error 126. On Linux it is every failed load under System, and a file
/// that is absent under Application and Path.
export error NotFound

/// The file is not a library this program can load: another architecture,
/// or not an executable image. On Windows this is error 193. On Linux it
/// is a failed load of a file that exists, under Application and Path.
export error Invalid

/// The library's initializer failed (Windows error 1114). Linux does not
/// report it apart from the failures above.
export error InitFailed

/// The name has the wrong form for its Search: empty, holding a NUL, over
/// 1,024 UTF-16 units, a separator or a colon in a bare name, or a
/// relative Path.
export error BadName

/// This program cannot load libraries at run time: a Linux bin outside
/// system-libc mode, or a target with no loader.
export error Unsupported

/// Any other failure, with the OS's own code (Windows only).
export error Os(code: i64)

/// Every way Open fails. The set is open, so a match on it outside std
/// keeps a `_` arm.
export open type Error = NotFound | Invalid | InitFailed | BadName | Unsupported | Os

/// Where Open looks for a library. It never looks in the current
/// directory or PATH.
export enum Search {
    /// On Windows a bare name, looked up in System32 only. On Linux a
    /// soname, looked up in ld.so's order, which honours LD_LIBRARY_PATH.
    System
    /// On Windows a bare name, looked up in the executable's directory, then
    /// System32, for the library and for its own dependencies. On Linux a
    /// name resolved against the directory of /proc/self/exe.
    Application
    /// An absolute path, on both targets.
    Path
}

/// An open library. It is linear: end it with Close, or with Detach to
/// keep the library loaded for the life of the process. An address from
/// Symbol is dead after Close. It holds one word, so it is Send and Share.
export linear struct Library {
    h: uint
}

/// Loads the library name where search says and returns it open. It runs
/// the library's initializers on the calling carrier and blocks it, and it
/// cannot be cancelled. Opening a library already loaded adds a reference,
/// and each Library releases its own. On Linux it calls dlopen with
/// RTLD_NOW | RTLD_LOCAL.
///
/// Fails with BadName for a name of the wrong form for search, NotFound,
/// Invalid or InitFailed as their docs say, Os for any other Windows
/// error, and Unsupported on Linux unless the bin is in system-libc mode.
/// O(name) plus the loader's work; allocates nothing.
export Open(io: Io, name: str, search: Search): Error!Library {
    r := sys.DlOpen(name, SearchCode(search))
    match r.err {
        .None { return Library(h: Trunc[uint](r.value)) }
        .NotFound { fail NotFound }
        .InvalidPath { fail BadName }
        .Unsupported { fail Unsupported }
        _ {}
    }
    if r.os == sys.DlInvalid {
        fail Invalid
    }
    if r.os == sys.DlInitFailed {
        fail InitFailed
    }
    fail Os(code: r.os)
}

// SearchCode is std.sys's code for s: 0 System, 1 Application, 2 Path.
SearchCode(s: Search): int {
    match s {
        .System { return 0 }
        .Application { return 1 }
        .Path { return 2 }
    }
}

/// The address of the exported function or variable name, or none. It is
/// also none for a name that is empty, over 255 bytes or holds a NUL. The
/// address is valid until Close; calling it is the caller's unsafe.
/// O(name) plus the lookup; allocates nothing.
export Library.Symbol(&self, name: str): ?*u8 {
    return sys.DlSymbol(self.h, name)
}

/// Present reports whether f, a bodiless extern function named directly
/// (`dl.Present(io, GetThing)`), can be called. For a function of a lazily
/// bound library (pkg.Link kind "lazy") it loads the library if it is not
/// loaded yet, running its initializers on the carrier's system stack and
/// blocking the carrier as Open does (it cannot be cancelled), and finds
/// the function without calling it; a later call of f then costs one
/// indirect call. For any other extern it is true when the program is built, since the loader
/// resolved the function before Main. Naming f here needs no unsafe:
/// Present never calls it. A failed load is not remembered: asking again
/// loads again. Calling a lazily bound function that is not present panics
/// (code 17, unresolved function), so a program that may run without the
/// library asks first. Any other argument is E0705 reason
/// present-argument. O(1) plus the loader's work on the first ask;
/// allocates nothing.
export Present[F](io: Io, f: F): bool

/// Releases the library. The OS unloads it, running its detach code, when
/// this was its last reference. It never fails: a release the OS refuses
/// leaves the library loaded. It blocks the carrier and cannot be
/// cancelled. O(1) plus the loader's work; allocates nothing.
export Library.Close(self, io: Io) {
    sys.DlClose(self.h)
    Library(h: _) := self
}

/// Ends the handle and keeps the library loaded for the life of the
/// process, so addresses from Symbol stay valid. O(1); allocates nothing.
export Library.Detach(self) {
    Library(h: _) := self
}
