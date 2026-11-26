// Why: a freestanding program (a kernel, firmware) has no std.rt to print
// a panic and exit, so the program itself must decide what a panic does.
//
// What: the panic hooks of x86_64-freestanding: PanicInfo, and PanicAt,
// PanicMsg and PanicErr, which hand it to the program's MkPanicHook.
//
// How: a trap block calls PanicAt, PanicMsg or PanicErr (the code
// generator picks them by target); each builds a PanicInfo and calls the
// program's MkPanicHook, which the program exports with
// `export extern MkPanicHook(info: &free.PanicInfo): never`. Without one
// the support pack's weak default executes a trap instruction. Nothing
// here allocates, prints or unwinds.

/// A panic's report, which the program's MkPanicHook receives. code is
/// the panic code, numbered as on hosted targets (1 overflow, 2 bounds,
/// 3 divide by zero, ..., 8 Panic). message is the text given to Panic
/// or a contract's text, and otherwise the code's name, such as
/// "overflow", "bounds" or "must": Must and Assert give the code's name,
/// since a freestanding program has no table of error names. file, line
/// and column are where the check failed. A program receives it by
/// exporting `export extern MkPanicHook(info: &free.PanicInfo): never`;
/// without one a panic executes a trap instruction. Making the report
/// allocates nothing.
export struct PanicInfo {
    export code: u32
    export message: str
    export file: str
    export line: int
    export column: int
}

// A source position as every trap block passes it: std.rt.Site's layout
// (file, line, col), which is the layout of codegen's site rows.
struct Site {
    file: str
    line: u32
    col: u32
}

// The program's panic hook, or the support pack's weak trap.
extern MkPanicHook(info: &PanicInfo): never

// The code's text: types.PanicCode.String(), as std.rt.PanicText, for
// every code a freestanding program can raise; 17 (unresolved function)
// comes only from Windows' lazy binding.
Text(code: u32): str {
    return match code {
        1 { "overflow" }
        2 { "bounds" }
        3 { "divide by zero" }
        4 { "shift amount" }
        5 { "cast" }
        6 { "must" }
        7 { "assert" }
        8 { "explicit" }
        9 { "stack limit" }
        10 { "str boundary" }
        11 { "free non-empty" }
        12 { "wrong allocator" }
        13 { "enum tag" }
        14 { "contract" }
        15 { "unreachable" }
        16 { "suspend in callback" }
        _ { "panic" }
    }
}

// Hook calls MkPanicHook with the report of code at site.
Hook(code: u32, site: &Site, message: str): never {
    info := PanicInfo(code: code, message: message, file: site.file, line: int(site.line), column: int(site.col))
    unsafe "the program's hook, or the weak trap; it never returns" {
        MkPanicHook(&info)
    }
}

// PanicAt is the hook of a check with no message: the code's text.
PanicAt(code: u32, site: &Site): never {
    Hook(code, site, Text(code))
}

// PanicMsg: the message alone (Panic's, Must's, a contract's); the code
// says which kind of panic it was.
PanicMsg(code: u32, site: &Site, msg: str): never {
    Hook(code, site, msg)
}

// PanicErr: Must on an error. No error-name table exists without std.rt,
// so the message is the code's text.
PanicErr(code: u32, site: &Site, err: u32): never {
    Hook(code, site, Text(code))
}
