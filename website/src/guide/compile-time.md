---
description: Compile-time evaluation with ordinary Mk code, branching on the target, fields loops over a struct, and generators that write code for a type with gen.
---

# Compile-time code

There is no separate compile-time language and no macros. A `const` initializer runs ordinary Mk while the program compiles. Any function that takes no `Io` can run there, with no marker. The result is the same on every build, and an initializer that runs too long is a compile error, not a hang.

## Constants computed by the compiler

```mk
// verdict: ok
Crc32Table(): [256]u32 {
    var t: [256]u32 = [0; 256]
    for i in 0..256 {
        var c := Cast[u32](i)
        for _ in 0..8 {
            c = if c & 1 == 1 { 0xEDB88320 ^ (c >> 1) } else { c >> 1 }
        }
        t[i] = c
    }
    return t
}

const Crc32 = Crc32Table() // computed once, while compiling; the program holds only the table

PageSize(): int {
    if Target.arch == Arch.Arm64 { // a constant condition: the dead branch emits no code
        return 16384
    }
    return 4096
}
```

- `Crc32Table` is an ordinary function. Called in a `const`, it runs in the compiler, and the executable contains only the 256 numbers.
- `Target` describes the platform being compiled for. A condition on it is a constant, so the branch not taken produces no code.
- A compile-time evaluation runs on a step budget. Raise it for one constant with `fuel` ([fuel](/reference/language/compile-time#fuel)).

## Visiting a struct's fields

A `fields` loop visits each field of a struct, by name and by reference, under a `Fields[B]` bound:

```mk
// verdict: ok
struct Config {
    export host: str
    export port: int
    export debug: bool
}

Dump[A: Fields[Format] + Plain](w: mut Writer, x: A): WriteError!void {
    for name, value in fields(&x) {
        try w.Str(name)
        try w.Str("=")
        try value.Format(w)
        try w.Str("\n")
    }
}

export Main(sys: &System): WriteError!void {
    var out := sys.Stdout()
    try Dump(mut out, Config(host: "localhost", port: 8080, debug: false))
    // prints host=localhost, port=8080 and debug=false, one per line
}
```

```text
host=localhost
port=8080
debug=false
```

The loop is unrolled at compile time, so each `value` has its own field's type. It visits a struct's fields in declaration order.

## Generators

A generator writes code for a type, such as an `Eq` implementation or a JSON codec. A `gen` line runs it while the program compiles. Its output is plain Mk, checked as if you had written it by hand.

```mk
// verdict: ok
import std.derive
import std.json

struct Color {
    export r: u8
    export g: u8
    export b: u8
}

gen derive.Eq(Color)     // impl Eq for Color, field by field
gen derive.Hash(Color)   // so a Color can be a Map key
gen json.Codec(Color)    // Color.FromJson and Color.FormatJson

Same(a: &Color, b: &Color): bool {
    return a.Eq(b)
}
```

The library's generators:

| Generator | Writes |
|---|---|
| `derive.Eq`, `derive.Hash`, `derive.Format` | Implementations of the interface, field by field ([std.derive](/reference/std/derive)) |
| `json.Codec` | Typed JSON reading and writing ([std.json](/reference/std/json)) |
| `cli.Args` | A command-line parser from a struct ([std.cli](/reference/std/cli)) |
| `regex.Matcher` | A regular expression compiled to code ([std.regex](/reference/std/regex)) |
| `dyn.Of` | A boxed type that dispatches through an interface ([std.dyn](/reference/std/dyn)) |

A generator of your own is an ordinary exported function in another module. It reads a type's fields and signatures, never function bodies, and emits Mk source with `quote` blocks. [Compile time](/reference/language/compile-time) shows how to write one.

## Next

[Unsafe code and C](./unsafe-and-c).
