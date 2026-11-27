---
description: unsafe blocks with a stated reason, raw pointers, the unsafe_modules list in pkg.mk, calling C and being called from C, C imports, and the freestanding target.
---

# Unsafe code and C

Code that the compiler cannot check goes in an `unsafe` block, which states the reason it is sound. Inside one you can dereference a raw pointer `*T`, call a C function, and run inline assembly.

```mk
// verdict: ok
import std.raw

// Sum adds n values starting at p. The caller promises they are readable.
Sum(p: *i32, n: int): i32 {
    var total: i32 = 0
    for i in 0..n {
        unsafe "the caller passes n readable values at p" {
            total += *raw.Offset(p, i)
        }
    }
    return total
}

export Main(sys: &System): WriteError!void {
    xs: [4]i32 = [1, 2, 3, 4]
    var t: i32 = 0
    unsafe "xs holds four i32 values for the whole call" {
        t = Sum(raw.Data(xs[..]), xs.Len())
    }
    try sys.Print("{t}\n", (t)) // prints 10
}
```

- Every `unsafe` block carries a string saying why it is sound, so a reviewer can check the claim.
- Pointer arithmetic is a function, `raw.Offset(p, i)`, not `p + i`. [`std.raw`](/reference/std/raw) has the unsafe intrinsics.
- References made from raw pointers must still obey the loan rules; breaking them is undefined behaviour ([the memory model](/reference/memory)).

## Who may write unsafe code

A module may hold `unsafe` blocks only when the package's manifest lists it in `unsafe_modules` (E1102), so one line of the manifest shows every module that needs review. To run the program above, put it in a package and list its module:

```mk
// verdict: parse
const Package = pkg.Package(
    name: "sum",
    mk: "0.6",
    bins: [pkg.Bin(name: "sum", root: "src", subsystem: "console")],
    unsafe_modules: ["src"],
)
```

A single file run with `mk run` cannot use `unsafe`.

## Calling C, and being called from C

An `extern` function, and any Mk function used as a value, uses the platform's C calling convention, so C can call it and it can call C:

```mk
// verdict: parse
import c.sqlite3

extern abs(n: i32): i32     // a C function, linked by name

export extern MkTwice(n: i32): i32 { // an Mk function C can call
    return n * 2
}

Version(): str {
    unsafe "sqlite3_libversion returns a static string" {
        return str.FromC(sqlite3.sqlite3_libversion())
    }
}
```

- `import c.sqlite3` turns the C headers named in the manifest (`pkg.CImport`) into Mk declarations. `mk cimport` prints what each import binds.
- C sources listed in `c_sources` are compiled with the bundled clang and linked against [`std.libc`](/reference/std/libc), a C library written in Mk.
- [`std.db.sqlite`](/reference/std/sqlite) wraps SQLite this way behind a safe API, and the cookbook shows how to [call a function in a Windows DLL](/examples/cookbook#call-a-function-in-a-windows-dll) and [load a library at run time](/examples/cookbook#load-a-library-at-run-time).

## Systems programming

For kernels and boot code, Mk also has `asm`, `naked` functions, linker sections, volatile register access ([`std.mmio`](/reference/std/mmio)) and a freestanding x86-64 target with no operating system. The [complete programs](/examples/programs) include a small kernel, and [unsafe and C](/reference/language/unsafe-and-c) covers all of it.

## Next

You have reached the end of the language guide. From here:

- [Code style](./style) and [organising a codebase](./codebase), for larger programs.
- The [cookbook](/examples/cookbook), for programs to start from.
- The [language reference](/reference/), to look up a rule.
