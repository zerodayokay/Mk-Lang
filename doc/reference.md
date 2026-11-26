# Mk language reference

This is the reference for the Mk language: each construct, an example, and the rules the compiler enforces. It is for programmers who write Mk and want to look something up. Sections 1 to 16 describe the language. Sections 17 and 18 list the standard library, and section 19 lists the names the examples use without declaring them.

## 1. Start here

To learn Mk in order, read [the tour](overview.md) first and come back here to look a rule up. Sections 2 to 16 share one shape: what a construct is for, an example with its output, the rules, and the errors the compiler reports. Here is a whole program:

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    try sys.Print("hello {name}, {n} args\n", (name: "world", n: sys.args.Len()))
}
```

```text
$ mk run hello.mk
hello world, 1 args
$ mk run hello.mk -- a b
hello world, 3 args
```

Arguments after `--` go to the program. `sys.args[0]` is the program's own path, so `sys.args` has one element when you pass none. [Hello world](overview.md#hello-world) in the tour shows `mk run` and `mk build`, and [the tools guide](tools.md) has every command.

### The program

A program is a module that exports a function `Main`. `Main` receives `sys`, a `&System`. It holds everything the program may touch: its arguments and environment, standard input, output and error, a heap, an arena, and the `Io` handle for files, the network and tasks.

```mk
// verdict: ok
export Main(sys: &System): int {
    if sys.args.Len() < 2 {
        sys.PrintErr("usage: greet NAME\n") else {} // ignore a failed write
        return 2 // the exit status
    }
    name := sys.args[1]
    sys.Print("hello, {name}\n", (name)) else {}
    return 0
}
```

```text
$ mk run greet.mk
usage: greet NAME
$ echo $?
2
$ mk run greet.mk -- Ada
hello, Ada
```

- `Main` returns `void`, `int`, `E!void` or `E!int`. [Errors from `Main`](#errors-from-main) gives the exit status of each.
- There are no global functions for I/O or memory. A function can print, allocate or open a file only if a parameter gives it the means, so a signature shows what a function can touch. A function with no `Io` cannot do I/O and cannot suspend.
- By convention, parameters come in one order: the receiver, then the arena or allocator, then `io`, then the rest ([style](style.md#2-judged-rules)). When several capabilities travel together, pass them in a [`Ctx` struct](codebase.md#3-ctx-structs).
- A local may take its type's name: `arena := try Arena.New(sys.heap)`.
- `sys.arena` lives as long as the program. For memory with a shorter life, see [allocators and arenas](std/README.md#3-memory-allocators-and-arenas).

### Printing

`sys.Print` takes a format string and a record whose fields fill its holes.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    x := 3
    try sys.Print("x is {x}, twice {y}\n", (x, y: x * 2)) // (x) is short for (x: x)
    try sys.Print("done\n") // no holes: no record
    try sys.PrintErr("to stderr\n")
}
```

```text
x is 3, twice 6
done
to stderr
```

- The hole `{x}` takes the record's field `x`. A field written `(x)` is short for `(x: x)`.
- The compiler checks the holes against the record: every hole needs a field, and every field needs a hole (E0706).
- A string with no holes takes no record.
- `sys.PrintErr` writes to standard error. `Print(mut w, ...)` writes to any other `Writer`, and `arena.Fmt(...)` builds a `str`.
- Printing can fail, so `Print` returns `WriteError!void`. Pass the error on with `try`, or drop it with `else {}`.

See also: [format strings](std/core.md#formatting-and-printing) for holes, specs and `Format`, [section 9](#9-errors) for `try` and `else`.

### How to read the examples

The first line of every example in this reference says what the compiler does with it:

| First line | Meaning |
|---|---|
| `// verdict: ok` | The example compiles. |
| `// verdict: reject E0501` | Compiling fails with exactly the codes listed. Each line that fails ends with `// <- E0501`. |
| `// verdict: parse` | The example parses. It may use names that are not declared. |

The output of an example that prints follows it in a `text` block. Each example is a module of its own, as if its package listed it in `unsafe_modules`, so it may use `unsafe`, `section` and `extern var` ([section 14](#14-unsafe-and-c)). A file you run by itself cannot (E1102): put it in a package whose `pkg.mk` lists it ([packages](packages.md)). The names in [section 19](#19-test-prelude) (`Point`, `Res`, `Acquire` and others) are declared for every example.

When the compiler rejects your code, `mk explain E0501` prints what the code means, how to fix it and an example. [The error catalogue](errors.md) lists every code.

### Finding a name

[Section 17](#17-module-map) maps the standard library's modules and [section 18](#18-library-surface) lists every exported signature. `mk query members List` lists a type's methods, and `mk query find` searches by a word you know from another language ([finding Mk names](#how-habits-are-reported), [the query API](tools.md#5-query-api)).

### The sections

| Section | What it covers |
|---|---|
| [2. Lexical rules](#2-lexical-rules) | Source text, comments, names, keywords, literals, operators |
| [3. Types and kinds](#3-types-and-kinds) | Numbers, `str`, slices, arrays, optionals, results, records, structs, enums, type aliases, conversions; which values copy and which move |
| [4. Declarations](#4-declarations) | Structs, enums, constants, global variables, functions and methods, layout |
| [5. Statements](#5-statements) | Bindings, assignment, `if`, `for`, `match`, `defer`, `return`, tail calls; `if`, `match` and `else` as values |
| [6. Patterns](#6-patterns) | What `match`, `if x := e` and destructuring can bind |
| [7. Expressions](#7-expressions) | Operators, indexing, literals, calls, function values and `fn` literals |
| [8. Ownership and borrowing](#8-ownership-and-borrowing) | Moves, borrows, `from` and `into` clauses, linear values; the rules are in [the memory model](memory.md) |
| [9. Errors](#9-errors) | Error sets, `E!T`, `fail`, `try`, `else`, `onerr`, panics |
| [10. Generics and interfaces](#10-generics-and-interfaces) | Type parameters, bounds, `interface`, `impl`, specialization, `const` parameters |
| [11. Compile time](#11-compile-time) | `const` evaluation, the stage rule, `fields`, generators with `gen` |
| [12. Concurrency](#12-concurrency) | `Io`, `scope`, tasks, channels, cancellation |
| [13. Modules and build](#13-modules-and-build) | Modules, `import`, `export`, targets; manifests are in [packages](packages.md) |
| [14. Unsafe and C](#14-unsafe-and-c) | `unsafe`, raw pointers, `extern`, C imports, `asm` |
| [15. Contracts and tests](#15-contracts-and-tests) | `requires`, `ensures`, `invariant`, `test` blocks |
| [16. Habits from other languages](#16-habits-from-other-languages) | C, Go, Rust and Zig forms and what to write instead |
| [17. Module map](#17-module-map) | The standard library's modules |
| [18. Library surface](#18-library-surface) | Every exported signature of the standard library |
| [19. Test prelude](#19-test-prelude) | Names the examples use without declaring them |

[The documentation index](README.md) lists the other documents: the memory model, the cookbook, the examples and the standard library guide.

## 2. Lexical rules

A Mk source file is UTF-8 text. The compiler reads it as a sequence of tokens: comments, names, keywords, literals and punctuation. Newlines are tokens too: a newline ends a statement, so Mk has no semicolons.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    mask: u8 = 0xFF // an integer literal takes the type it is expected to have
    big := 1_000_000 // int, when no type is expected
    ratio := 1.5 // f64, when no type is expected
    letter := 'a' // a char: one Unicode scalar
    code := b'a' // a u8
    path := r"C:\temp" // a raw string: no escapes
    verb := b"GET" // a []u8
    text := """
        two
        lines
        """
    try sys.Print("{mask} {big} {ratio} {letter} {code} {path}\n", (mask, big, ratio, letter, code, path))
    n := verb.Len() + text.Len()
    try sys.Print("{n}\n", (n))
}
```

```text
255 1000000 1.5 a 97 C:\temp
13
```

| Item | Rule |
|---|---|
| Source | UTF-8. Names and keywords are ASCII. |
| Comments | `//` runs to the end of the line. `///` documents the next item or field. |
| Statements | A newline ends a statement when the line can end there ([line breaks](#line-breaks)). |
| Names | Items are `PascalCase`, locals and fields `snake_case`. The compiler checks both (E0207). |
| Integers | `1_000`, `0xFF`, `0o17`, `0b101`. A literal takes the integer type it is expected to have, else `int`. |
| Floats | `1.5`, `1e9`. A literal takes the float type it is expected to have, else `f64`. |
| Characters | `'a'` is a `char`. `b'a'` is a `u8`. |
| Strings | `"a\n"` is a `str`, with the escapes `\n \r \t \0 \\ \' \"`, `\xHH` and `\u{1F600}`. `r"raw"` has no escapes. `b"bytes"` is a `[]u8`. |
| Multi-line strings | `"""`: the newline after the opening quotes is dropped, and the closing line's indentation is removed from every line. |

A lexical mistake is E0101, the syntax error, except for tokens from other languages, which have codes of their own ([below](#operators-and-punctuation)). Its message says what is wrong: `no leading zeros`, `unknown escape`, `Mk has no block comments`.

### Source text

A file is UTF-8 text. Lines end in a newline; a carriage return and newline pair reads as one newline. Spaces and tabs separate tokens.

```mk
// verdict: ok
Greeting(): str {
    name := "café" // a string may hold any UTF-8 text
    // so may a comment: ½, π, 日本
    return name
}
```

- Names and keywords are ASCII. Text in any other script belongs in a comment or a literal.
- A byte-order mark may open the file and is ignored. Anywhere else it is E0101.
- A carriage return without a newline after it, a form feed, a vertical tab and invalid UTF-8 are E0101.
- `@`, `#`, `` ` `` and `\` outside a literal are E0101. Mk has no attributes and no backquotes.
- An item's syntax tree is at most 1,000 levels deep. Every bracket, block and binary operator adds at least one level, so about 1,000 nested `(`, about 500 nested `if` blocks or a chain of about 1,000 `+` is E0101 `nesting too deep`.

A name with a non-ASCII letter:

```mk
// verdict: reject E0101
Greeting(): str {
    naïve := "café" // <- E0101
    return "hello"
}
```

### Comments

`//` starts a comment that runs to the end of the line. A comment that starts with exactly three slashes, `///`, is a doc comment: it documents the item or field that follows it.

```mk
// verdict: ok
/// Pixel is a point on the screen.
export struct Pixel {
    x: int
    y: int
}

/// Manhattan returns the grid distance from the origin, for a pixel right of and below it.
export Manhattan(p: Pixel): int {
    // an ordinary comment
    //// four slashes make an ordinary comment too
    return p.x + p.y // a comment may follow code
}
```

`mk doc` prints, under the module's heading, each documented item's declaration and its doc comment, as Markdown:

````text
### `Manhattan`

```
export Manhattan(p: Pixel): int
```

Manhattan returns the grid distance from the origin, for a pixel right of and below it.

### `Pixel`

```
export struct Pixel
```

Pixel is a point on the screen.
````

A field's doc comment is not printed by `mk doc`; a generator reads it with `f.Doc()` ([section 11](#the-generator-api)).

- Give every exported item a `///` line that says what it does ([the style guide](style.md)).
- Mk has no block comments. `/* ... */` is E0101.

```mk
// verdict: reject E0101
Area(w: int, h: int): int {
    return w * h /* square units */ // <- E0101
}
```

See also: [`mk doc`](tools.md#mk-doc).

### Line breaks

A newline ends a statement when the last token on the line can end one: a name, a literal, `)`, `]`, `}`, `return`, `break` or `continue`. A statement also ends before `}`. Elsewhere a newline is ignored, so a statement continues on the next line.

```mk
// verdict: ok
Add(
    a: int,
    b: int, // a trailing comma is allowed in a comma list
): int {
    return a + b
}

export Main(sys: &System): WriteError!void {
    xs := [
        1,
        2,
    ]
    total := Add(xs[0], xs[1])
    small := total > 2 && // an operator at the end: the line goes on
        total < 10
    if small { try sys.Print("small\n") } // `}` ends the statement inside
    try sys.Print("{total} {small}\n", (total, small))
}
```

```text
small
3 true
```

- Newlines inside `( )` and `[ ]` are ignored. A `{` inside them makes newlines count again, so a `fn` literal passed as an argument has a statement body.
- An operator ends a line, never starts one. Break a long expression after the operator.
- A method chain breaks after the dot: `s.` then `Len()` on the next line.
- `else` goes on the line of the closing `}`: `} else {`.
- A function body's `{` goes at the end of the signature's line. After a continued signature (below) it may also start a line of its own.
- `return` followed by a newline returns nothing. Write the value on the line of `return`.

A signature may go on over several lines. Each further line begins with `from`, `ends`, `*`, `requires` or `ensures`, or with `cpu` or `section` and a string. The body's `{` may then start a line of its own:

```mk
// verdict: ok
Longer(a: str, b: str): str
    from a, b // the result borrows from a or b
{
    if a.Len() >= b.Len() {
        return a
    }
    return b
}

Mean(xs: []int): int
    requires xs.Len() > 0
{
    var sum := 0
    for x in xs {
        sum += x
    }
    return sum / xs.Len()
}

export Main(sys: &System): WriteError!void {
    l := Longer("pear", "fig")
    m := Mean([2, 4, 9][..])
    try sys.Print("{l} {m}\n", (l, m))
}
```

```text
pear 5
```

These words start a continuation only after a signature. In a body, `from := 0` and `cpu := simd.Cpu()` are ordinary statements.

An operator at the start of a line:

```mk
// verdict: reject E0101
Sum(a: int, b: int): int {
    x := a
        + b // <- E0101
    return x
}
```

A `-` at the start of a line is legal, but it starts a new statement: `-b`, whose value is dropped (E0209). The line before it has already ended:

```mk
// verdict: reject E0209
Diff(a: int, b: int): int {
    x := a
        - b // <- E0209
    return x
}
```

The other prefix operators do the same. A line that starts with `*`, `&`, `&&`, `!` or `~` is a new statement: `&& b` is E0209 on a `&&bool`, and `* b` is E0308. A line that starts with `||` reads as a closure (E0609). Break after `&&` and `||`.

A dot at the start of a line, and `else` on a line of its own:

```mk
// verdict: reject E0101
Size(s: str): int {
    n := s
        .Len() // <- E0101
    if n > 3 {
        return n
    }
    else { // <- E0101
        return 0
    }
}
```

A body's `{` on its own line after a one-line signature:

```mk
// verdict: reject E0101
Twice(n: int): int
{ // <- E0101
    return n * 2
}
```

See also: [the grammar](grammar.md#newlines-inside-a-signature), [flows](#8-ownership-and-borrowing), [contracts](#15-contracts-and-tests).

### Names

A name starts with an ASCII letter and continues with letters, digits and underscores. Its case says what it names, and the compiler checks it.

| Case | Names | Examples |
|---|---|---|
| PascalCase: an upper-case letter, then letters and digits | Functions, methods, types, interfaces, enum variants, errors, constants, globals, type parameters | `MaxLen`, `HTTPServer`, `Area`, `T` |
| snake_case: a lower-case letter, then lower-case letters, digits and underscores | Locals, parameters, fields, loop labels | `user_count`, `x2`, `self` |

```mk
// verdict: ok
const MaxLen = 8

error TooLong(len: int)

struct HTTPServer {
    port: int
    max_conns: int
}

HTTPServer.Limit(&self): int {
    return self.max_conns * 2
}

Check(name: str): TooLong!int {
    name_len := name.Len()
    if name_len > MaxLen {
        fail TooLong(len: name_len)
    }
    return name_len
}

export Main(sys: &System): WriteError!void {
    srv := HTTPServer(port: 8080, max_conns: 4)
    limit := srv.Limit()
    n := Check("ada") else { 0 }
    try sys.Print("{limit} {n}\n", (limit, n))
}
```

```text
8 3
```

- A name in the wrong case is E0207. The message gives the right spelling: `` `do_thing` should be PascalCase: `DoThing` ``.
- A name cannot start with `_`: `_x` is E0207. `_` alone is the blank, as in `_ = F()`.
- Lookup is exact. When nothing matches, the compiler ignores case and underscores to find what you meant: `.len()` and `userCount` are E0207, and the message names `Len` and `user_count`. `mk fix` applies the correction.
- Two names in one scope cannot differ only in case and underscores: `max_len` and `maxlen` in one struct are E0206. A type's fields are one scope and its variants and methods another, so a field `cpu` and a method `Cpu` may stand side by side ([structs](#structs)). A local cannot shadow another local or a parameter ([bindings](#bindings)).
- An `extern` function's or var's name is its C symbol. It is used as written and is outside the case rule: `extern strlen(s: *u8): uint`.

A local spelled differently from its declaration:

```mk
// verdict: reject E0207
Count(s: str): int {
    user_count := s.Len()
    return userCount // <- E0207
}
```

Two fields that differ only in case and underscores:

```mk
// verdict: reject E0206
struct Account {
    max_len: int
    maxlen: int // <- E0206
}
```

See also: [the style guide](style.md) for choosing names.

### Keywords

Keywords: `import export struct enum error interface impl type const var linear view gen quote test extern if else for in match return break continue defer onerr fail try scope go unsafe asm become mut fn is requires ensures invariant`. Contextual: `from into ends fuel packed plain open self Self result`.

A keyword cannot be a name. A contextual word is a keyword only where the grammar expects it, and a plain name elsewhere:

| Where it is a keyword | Word | See |
|---|---|---|
| In a signature's flows: what a result or a parameter borrows | `from`, `into`, `ends` | [section 8](#8-ownership-and-borrowing) |
| After a `const` initializer or a `gen` call: the step budget of compile-time evaluation | `fuel` | [section 11](#11-compile-time) |
| Before `struct`: a layout with no padding | `packed` | [section 4](#4-declarations) |
| Before `view struct`: a copyable, `Freeze` carrier view | `plain` | [section 4](#4-declarations) |
| Before `enum` or `type`: a set a later release may extend | `open` | [error sets](#error-sets) |
| The receiver of a method, and its type | `self`, `Self` | [section 4](#4-declarations) |
| In an `ensures` clause: the function's return value | `result` | [section 15](#15-contracts-and-tests) |

The same holds for the systems words `naked`, `cpu`, `section` and `align`, and for the operand words inside `asm(...)`: each is a keyword only in its position in [section 14](#14-unsafe-and-c).

```mk
// verdict: ok
struct Valve {
    open: bool // a field named open
    fuel: int
}

export Main(sys: &System): WriteError!void {
    into := 3 // contextual words are plain names here
    ends := 4
    result := into + ends
    v := Valve(open: true, fuel: result)
    open := v.open
    try sys.Print("{result} {open}\n", (result, open))
}
```

```text
7 true
```

A keyword used as a name is E0102. For a local or a parameter, the fix renames it to `type_`:

```mk
// verdict: reject E0102
Kind(): int {
    type := 1 // <- E0102
    return 0
}
```

The universe names are built in and visible in every module, with no import.

Universe names: `true false some none ok err fields Assert Panic Cast Trunc SizeOf AlignOf Target Plain Send Share Fields EvalArena`. `ok` and `err` are pattern heads only, so they may be declared. Declaring any other universe name, or a primitive type name such as `int` or `str`, as an item, local, parameter or field is E0102. A member in a type's namespace is exempt. The built-in bounds `Plain Send Share Fields` are universe names ([built-in bounds](#built-in-bounds)).

```mk
// verdict: ok
enum Op {
    Cast // a variant may be named after a universe name
    Panic
}

struct Check {
    level: int
}

Check.Assert(&self): bool { // so may a method
    return self.level > 0
}

Run(c: Check): int {
    ok := c.Assert() // ok and err may be declared
    return if ok { 1 } else { 0 }
}
```

Variants and methods are in the type's namespace; fields are not, so a field named `some` is E0102.

A primitive type name and a universe name used as names:

```mk
// verdict: reject E0102
struct Field {
    str: int // <- E0102
}

Count(): int {
    some := 1 // <- E0102
    return some
}
```

### Integer literals

An integer literal is decimal, or hexadecimal, octal or binary with a prefix. `_` may separate digits.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 1_000_000 // decimal
    b := 0xFF // hexadecimal: 0x, digits 0-9 a-f A-F
    c := 0o17 // octal: 0o
    d := 0b1010_1010 // binary: 0b
    e := 0x_FF // _ may follow a prefix
    try sys.Print("{a} {b} {c} {d} {e}\n", (a, b, c, d, e))
}
```

```text
1000000 255 15 170 255
```

- The prefixes `0x`, `0o` and `0b` are lower case. Hex digits may be either case.
- A decimal literal of more than one digit does not start with `0`: `007` is E0101. Mk has no octal by a leading `0`; write `0o7`.
- `_` stands only between two digits or right after a prefix. `1__0` and `1_` are E0101.
- No literal takes a type suffix: `10u8` is E0101. Write `x: u8 = 10` or `u8(10)`.
- A literal is at most 2^64-1. Its type then limits it further ([below](#how-a-literal-gets-its-type)).
- A literal has no sign. `-128` is negation applied to `128`, and `x: i8 = -128` is allowed.

```mk
// verdict: reject E0101
Codes(): int {
    a := 007 // <- E0101
    b := 10u8 // <- E0101
    return a + int(b)
}
```

### Float literals

A float literal has a fraction, an exponent or both: `1.5`, `1e9`, `2.5e-3`, `6.02E23`.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 1.5
    b := 1e9 // an exponent alone makes a float
    c := 2.5e-3
    d := 1_000.000_1 // _ between digits, as in integers
    half: f32 = 0.5 // adapts to f32
    try sys.Print("{a} {b} {c} {d} {half}\n", (a, b, c, d, half))
}
```

```text
1.5 1000000000 0.0025 1000.0001 0.5
```

- A float has digits on both sides of the `.`. Write `1.0` and `0.5`, not `1.` or `.5`.
- The exponent is `e` or `E`, an optional sign, and digits.
- A float does not start with `0` before more digits: `01.5` is E0101. Mk has no hexadecimal floats.
- The decimal text is rounded once, to the literal's type. A literal too large for `f64` is E0101; one too large for `f32` where an `f32` is expected is E0303.

### How a literal gets its type

A number literal has no fixed type. It takes the type the context expects: the declared type of a binding, a parameter's type, the function's result type, or the type of the other operand.

```mk
// verdict: ok
Scale(x: f32, by: f32): f32 {
    return x * by
}

export Main(sys: &System): WriteError!void {
    b: u8 = 200 // u8: the declared type
    c := b + 50 // u8: the other operand's type
    s := Scale(1.5, 4.0) // f32: the parameters' type
    n := 7 // int: nothing is expected
    r := 7.0 // f64: nothing is expected
    try sys.Print("{c} {s} {n} {r}\n", (c, s, n, r))
}
```

```text
250 6 7 7
```

- An integer literal takes an integer type, else `int`. A float literal takes a float type, else `f64`.
- An integer literal never becomes a float: `x: f64 = 1` and `f * 3` on an `f32` are E0301. Write `1.0` and `3.0`.
- A literal whose value does not fit its type is E0303: `x: u8 = 300`, `x: i32 = 0xFFFF_FFFF`.
- An operator whose operands are all integer literals is judged by its value at its type, as it would run: `x: u8 = 200 + 100` is E0303, because the addition panics with `overflow`, and so are `0 - 1` as a `u32`, `7 / 0` and `1 << 64`. The wrapping operators `+% -% *%` never are, and any other operand, a variable, a named constant, a call or a byte literal, leaves the check to run time. The check does not depend on whether the code runs: an operator under `if false`, on the decided side of `&&` or `||`, or in a `requires` clause is judged as one that runs, as a literal that does not fit is. In the initializer of a `const` or a global `var`, which runs at compile time, the same overflow is E0901.
- A `char` is not a number: `x: char = 65` and `x: u8 = 'a'` are E0301. Use `b'a'` for a byte.

```mk
// verdict: reject E0303 E0301
Limits(): int {
    small: u8 = 300 // <- E0303
    ratio: f64 = 1 // <- E0301
    _ = ratio
    return int(small)
}
```

See also: [conversions](#conversions), [arithmetic](#arithmetic).

### Character literals

`'a'` is a `char`: one Unicode scalar value. It takes the same escapes as a string.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 'a'
    smile := '\u{1F600}' // a scalar by its code point
    tick := '\'' // an escaped quote
    e := 'é' // any scalar, written as itself
    try sys.Print("{a} {smile} {tick} {e}\n", (a, smile, tick, e))
}
```

```text
a 😀 ' é
```

- A `char` literal holds exactly one scalar. `''` and `'ab'` are E0101.
- `'\u{D800}'` is E0101: a surrogate is not a scalar value.
- Mk has no lifetimes. `'a` without a closing quote is E1203.

### String literals

`"..."` is a `str`: valid UTF-8 text, on one line.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    s := "tab\there, quote \" and backslash \\"
    t := "\x41 and \u{e9}"
    n := "café".Len() // length in bytes
    try sys.Print("{s}\n{t} {n}\n", (s, t, n))
}
```

```text
tab	here, quote " and backslash \
A and é 5
```

| Escape | Meaning |
|---|---|
| `\n` `\r` `\t` | Newline, carriage return, tab |
| `\0` | The zero byte |
| `\\` `\'` `\"` | Backslash, single quote, double quote |
| `\xHH` | The byte `HH`, two hex digits. At most `7F` outside byte literals |
| `\u{H}` | The scalar `H`, one to six hex digits |

- A string ends on the line it starts. Use `"""` for text on several lines.
- A tab may appear as itself. Every other control character needs an escape (E0101).
- An unknown escape such as `\q` is E0101. So is `\x80` in a `str`: write `\u{80}`, or use a byte string.
- `{` and `}` are ordinary characters in a literal; `Print` and `Fmt` read holes from their format string ([section 1](#1-start-here)).
- `str` has no `+` (E0308). Build text with `Text` or `arena.Fmt` ([comparison and text](#comparison-and-text)).

```mk
// verdict: reject E0101
Path(): str {
    return "C:\query" // <- E0101
}
```

### Raw strings

`r"..."` is a `str` with no escapes: each backslash is itself. Use it for Windows paths and regular expressions.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    path := r"C:\temp\new"
    pattern := r"\d+\.\d+"
    try sys.Print("{path} {pattern}\n", (path, pattern))
}
```

```text
C:\temp\new \d+\.\d+
```

- A raw string is one line and holds no `"`. For either, use `"""`, where `\` starts an escape again: write `\\`.

### Byte literals

`b'a'` is a `u8`, and `b"..."` is a `[]u8`. Use them for protocols and binary formats.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    verb := b"GET\r\n" // a []u8 of 5 bytes
    high: u8 = b'\xFF' // \x takes any byte here
    c := b'A'
    n := verb.Len()
    try sys.Print("{n} {high} {c}\n", (n, high, c))
}
```

```text
5 255 65
```

- Byte literals hold printable ASCII. A byte string may also hold a tab. Anything else is written with `\xHH`, which takes any value up to `FF`.
- `\u{...}` is E0101 in a byte literal, and so is non-ASCII text.
- `b"..."` is a slice, `[]u8`, not an array: `x: [3]u8 = b"GET"` is E0301.

```mk
// verdict: reject E0101
Header(): []u8 {
    return b"café" // <- E0101
}
```

### Multi-line strings

`"""` opens a `str` that spans lines. The closing `"""` starts a line of its own, and its indentation is removed from every line.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    query := """
        SELECT name
          FROM users

        WHERE id = "7"
        """
    try sys.Print("[{query}]\n", (query))
}
```

```text
[SELECT name
  FROM users

WHERE id = "7"
]
```

- Nothing follows the opening `"""` on its line. The newline after it is not part of the text.
- Each line keeps its newline, the last one included.
- Every line that is not blank starts with the closing line's indentation, byte for byte: a tab and four spaces do not match. A line indented less is E0101.
- Quotes need no escape. The other escapes work as in `"..."`. Write `\"""` to put `"""` in the text.
- `"""text"""` on one line is E0101. Use `"..."`.

```mk
// verdict: reject E0101
Usage(): str {
    return """
      usage: tool [flags] // <- E0101
        """
}
```

### Operators and punctuation

| Use | Tokens |
|---|---|
| Arithmetic. `+% -% *%` wrap instead of panicking on overflow ([arithmetic](#arithmetic)). `+` also joins bounds, `[T: Plain + Send]` | `+ - * / %` |
| Bitwise and, or, xor, not (`~x`; `^x` is E0101) and shifts. `&x` also takes a shared reference, and `\|` also joins error sets (`A \| B`) and pattern alternatives (`A \| B { ... }`) | `& \| ^ ~ << >>` |
| Comparison | `== != < <= > >=` |
| Logic. `!` also writes a result type, `E!T` | `&& \|\| !` |
| Assignment ([section 5](#5-statements)) | `= += -= *= /= %= +%= -%= *%= <<= >>= &= \|= ^=` |
| Declare and bind a local | `x := e` |
| A type annotation `x: int`, a result type `F(): int`, a type parameter's bound `[T: Plain]`, a loop label `outer:`, and a field name in a struct, record or error value, `Point(x: 1)` | `:` |
| A member, a method call, a module path | `.` |
| A range in `for`, in slices and in patterns (`0..10`), and the rest of a record pattern | `..` |
| The variadic tail of an `extern` C function ([section 14](#14-unsafe-and-c)) | `...` |
| An optional type, `?T` | `?` |
| A raw pointer type `*T`, a dereference, and a flow in a signature ([section 8](#8-ownership-and-borrowing)) | `*` |
| A splice in a `quote` block ([section 11](#11-compile-time)) | `$` |
| Only in an array repeat, `[0; 8]` | `;` |
| Grouping, indexing and type arguments, blocks, separators | `( ) [ ] { } ,` |

The longest token wins: `a<<=b` is `a <<= b`.

Some tokens from other languages are recognised so the compiler can name the Mk form: `;` at the end of a statement is E1206, `::` is E1213, `->` is E1211 and `=>` is E1214. See [section 16](#16-habits-from-other-languages).

### Compiler errors for lexical rules

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | A lexical mistake: a bad literal, a non-ASCII name, a block comment, a misplaced line break ([line breaks](#line-breaks)) |
| E0102 | reserved-word | A keyword, a primitive type name or a universe name used as a name ([keywords](#keywords)) |
| E0206 | duplicate-name | Two names in one scope that differ only in case and underscores ([names](#names)) |
| E0207 | naming | A name in the wrong case, or spelled differently from its declaration ([names](#names)) |
| E0209 | unused-result | A value dropped, such as a `-b` that a line break made a statement of its own ([line breaks](#line-breaks)) |
| E0301 | type-mismatch | An integer literal where a float is expected, or a `char` where a number is ([literal types](#how-a-literal-gets-its-type)) |
| E0303 | lossy-conversion | A literal whose value does not fit its type, or an operator over literals that would panic ([literal types](#how-a-literal-gets-its-type)) |
| E0308 | no-operator | `+` on `str`, or a `*` that a line break put at the start of a statement ([string literals](#string-literals), [line breaks](#line-breaks)) |
| E0901 | const-failed | An overflow in a `const` initializer ([literal types](#how-a-literal-gets-its-type)) |
| E1203 | lifetime-syntax | A lifetime such as `'a` ([characters](#character-literals)) |
| E1206 | semicolon | A `;` that ends a statement ([punctuation](#operators-and-punctuation)) |
| E1211 | arrow-return | `->` from another language ([punctuation](#operators-and-punctuation)) |
| E1213 | double-colon | `::` from another language ([punctuation](#operators-and-punctuation)) |
| E1214 | fat-arrow | `=>` from another language ([punctuation](#operators-and-punctuation)) |

See also: [the grammar](grammar.md), the productions in EBNF.

## 3. Types and kinds

Every value in Mk has a type, known at compile time. The built-in types are numbers, `bool`, `char`, strings, arrays, slices, references, optionals, results, records and function values; you add structs, enums and errors of your own. Every type also has a kind, which says whether a value is copied or moved when you use it.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    n: u8 = 200 // an integer of the expected type
    x := 2.5 // a float: f64
    ok := n > 100 // a bool
    c := 'λ' // a char
    s := "Mk" // a str
    a := [1, 2, 3] // an array: [3]int
    r := (lo: 1, hi: 9) // a record
    o: ?int = some(4) // an optional
    try sys.Print("{n} {x} {ok} {c} {s} {len} {lo} {o}\n", (n, x, ok, c, s, len: a.Len(), lo: r.lo, o))
}
```

```text
200 2.5 true λ Mk 3 1 some(4)
```

| Type | Meaning | Kind |
|---|---|---|
| `i8 i16 i32 i64 u8 u16 u32 u64` | Fixed-width integers | Plain |
| `int uint` | Pointer-sized integers: 64 bits on every supported target. Lengths and counts are `int` | Plain |
| `f32 f64` | IEEE 754 floats | Plain |
| `bool` | `true` or `false` | Plain |
| `char` | A Unicode scalar value | Plain |
| `void`, `never` | No value; a `never` function does not return | Plain |
| `str` | A view of valid UTF-8 | Plain |
| `[N]T` | Array: `N` values of `T`, held by value | As `T` |
| `[]T`, `mut []T` | Shared and exclusive views of consecutive `T` | Plain, Unique |
| `&T`, `mut T` | Shared and exclusive references. Never null | Plain, Unique |
| `*T` | Raw pointer, dereferenced only in `unsafe` | Plain |
| `?T` | `some(x)` or `none` | As `T` |
| `E!T` | An error from the set `E`, or a value `T` | As `T` |
| `(a: T, b: U)` | Record: a structural type with named fields | Worst of its fields |
| `fn(T): U` | Function value, with no captures | Plain |
| `struct`, `enum`, `error` | Types you declare ([section 4](#4-declarations)) | Worst of their fields, or as declared (`linear`, `view`) |

There are no implicit conversions: an `i32` never becomes an `i64`, and an integer never becomes a float, unless you write the conversion ([below](#conversions)).

Only numbers, `bool`, `char`, enums without payloads and `str` have `==` ([comparison](#comparison-and-text)); vectors compare lane by lane and give a `Mask` ([vectors](#vectors)).

### Integers

Mk has signed and unsigned integers of 8, 16, 32 and 64 bits. `int` and `uint` are pointer-sized, which is 64 bits on every supported target. Lengths and counts are `int`, and an index may be any integer type.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a: u8 = 250 // a literal takes the type it is expected to have
    b := 1_000_000 // nothing expected: int
    c := a +% 10 // wrapping add: 260 wraps to 4
    d := -7 / 2 // division truncates toward zero
    e := -7 % 2 // the remainder has the sign of the dividend
    f: i8 = -128
    g := f >> 1 // >> on a signed integer keeps the sign
    h := a << 1 // << drops the bits shifted out
    i := ~a // bitwise not
    try sys.Print("{a} {b} {c} {d} {e} {g} {h} {i}\n", (a, b, c, d, e, g, h, i))
    j := a + 10 // 260 does not fit a u8
    try sys.Print("{j}\n", (j))
}
```

```text
250 1000000 4 -3 -1 -64 244 5
panic: overflow at main.mk:13:10
backtrace:
  Main (main.mk)
```

| Type | Range |
|---|---|
| `i8`, `i16`, `i32`, `i64`, `int` | -2^(n-1) to 2^(n-1) - 1 |
| `u8`, `u16`, `u32`, `u64`, `uint` | 0 to 2^n - 1 |

- A literal takes the integer type it is expected to have, and is `int` when nothing is expected. A literal that does not fit, and arithmetic on literals, are covered in [how a literal gets its type](#how-a-literal-gets-its-type).
- `int` and `i64` are different types, as are `uint` and `u64`. Two operands of an operator have the same type, else E0301. Convert one of them ([below](#conversions)).

The operators and their panics are in [arithmetic](#arithmetic) and [bit operations](#bit-operations).

```mk
// verdict: reject E0303
Small(): u8 {
    return 256 // <- E0303
}
```

Operands of different integer types do not mix:

```mk
// verdict: reject E0301
Sum(a: i32, b: i64): i64 {
    return a + b // <- E0301
}
```

Write `i64(a) + b`.

### Floats

`f32` and `f64` are IEEE 754 binary floats of 32 and 64 bits. A float literal is `f64` unless an `f32` is expected.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 1.5 // nothing expected: f64
    b: f32 = 0.1
    c := 1.0 / 3.0
    d := 1.0 / 0.0 // float division by zero does not panic
    e := 0.0 / 0.0
    f := e == e // NaN equals nothing, itself included
    g := 2.5e-3
    try sys.Print("{a} {b} {c} {d} {e} {f} {g}\n", (a, b, c, d, e, f, g))
}
```

```text
1.5 0.1 0.3333333333333333 inf nan false 0.0025
```

- A float literal has a `.` or an exponent: `1.0`, `1e9`, `2.5e-3`. An integer literal is never a float ([how a literal gets its type](#how-a-literal-gets-its-type)).
- Rounding, division by zero and NaN are in [arithmetic](#arithmetic).
- `f32` widens to `f64` with `f64(x)`. The other way is `Cast[f32](x)` ([conversions](#conversions)).
- `std.math` has the constants and functions, such as `math.Inf` and `math.Sqrt` ([std.math](std/math.md)).

```mk
// verdict: reject E0301
Half(x: f64): f64 {
    return x / 2 // <- E0301
}
```

Write `x / 2.0`.

### `bool`

`bool` is `true` or `false`. `!` negates it, and `&&` and `||` evaluate their right side only when needed.

```mk
// verdict: ok
InRange(x: int, lo: int, hi: int): bool {
    return x >= lo && x < hi
}

Outside(x: int): bool {
    return !InRange(x, 0, 10)
}
```

- A condition (`if`, `for`, `Assert`) must be a `bool`. An integer is not a condition (E0301): write `n != 0`.
- `bool` converts to nothing, and nothing converts to it: `int(true)` is E0302.
- A `bool` takes one byte.

```mk
// verdict: reject E0301
Any(n: int): bool {
    if n { // <- E0301
        return true
    }
    return false
}
```

### Characters and bytes

`char` is one Unicode scalar value: a code point from 0 to 0x10FFFF, other than the surrogates 0xD800 to 0xDFFF. It takes 4 bytes. A byte is a `u8`, written `b'a'`.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    c := 'é'
    nl := '\n'
    smile := '\u{1F600}'
    code := u32(c) // a char widens to any integer of 32 bits or more
    byte := b'A' // a u8
    letter := char(byte) // a u8 widens to a char
    next := Cast[char](u32(c) + 1) // back from an integer: Cast checks the value
    later := c < 'z' // chars compare by scalar value
    try sys.Print("{c} {code} {byte} {letter} {next} {later} {smile}{nl}", (c, code, byte, letter, next, later, smile, nl))
}
```

```text
é 233 65 A ê false 😀
```

- The escapes are in [character literals](#character-literals) and [byte literals](#byte-literals).
- `char` supports `==` and the comparisons, by scalar value. It has no arithmetic (E0308): convert to `u32`, compute, and convert back with `Cast[char]`, which panics on a value that is not a scalar.
- A `u8` converts to `char` with `char(b)`, since every byte value is a scalar. Any wider integer needs `Cast[char]`.
- Text is UTF-8 bytes, not `char`s: a `str` iterates its scalars with `s.Scalars()` ([strings](#strings-str)).

```mk
// verdict: reject E0308
Offset(c: char): char {
    return c - 'a' // <- E0308
}
```

### Conversions

Mk converts between types only when you say so. There are three forms:

| Form | Converts | When the value does not fit |
|---|---|---|
| `T(x)` | Only when no value can change | Does not compile (E0303) |
| `Cast[T](x)` | Between integers, floats, `char` and enums with a tag type | Panics with `cast`. An integer to a float rounds instead |
| `Trunc[T](x)` | Between integers | Keeps the low bits |

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    small: i32 = 300
    a := i64(small) // widening: every i32 fits an i64
    b := f64(small) // every i32 is exact in an f64
    c := Cast[u8](small / 2) // 150 fits a u8
    d := Trunc[u8](small) // the low 8 bits: 300 is 0x12C, so 0x2C
    e := Cast[i32](-2.7) // toward zero
    f := Cast[f32](0.1) // rounded to the nearest f32
    g := u8(200) // a constant that fits needs no Cast
    try sys.Print("{a} {b} {c} {d} {e} {f} {g}\n", (a, b, c, d, e, f, g))
    h := Cast[u8](small) // 300 does not fit a u8
    try sys.Print("{h}\n", (h))
}
```

```text
300 300 150 44 -2 0.1 200
panic: cast at main.mk:12:10
backtrace:
  Main (main.mk)
```

`T(x)` accepts these conversions, and no others:

| From | To |
|---|---|
| A signed integer | A signed integer at least as wide |
| An unsigned integer | An unsigned integer at least as wide, or a signed one wider |
| An integer of up to 16 bits | `f32` |
| An integer of up to 32 bits | `f64` |
| `f32` | `f64` |
| `u8` | `char` |
| `char` | An integer of 32 bits or more |
| An enum with a tag type ([enum types](#enum-types)) | Its tag type, or what the tag type converts to |

- `int` and `uint` count as 64 bits, so `int(x)` takes an `i64` or a `u32`, and `int(u)` of a `uint` is E0303.
- A constant whose value fits passes `T(x)`: `u8(200)`, `i32(Max)` for a `const Max: int = 100`.
- A float literal or named float constant whose value is exactly an `f32` passes `f32(x)`: `f32(0.5)`, `f32(-1.5e3)`, `f32(Half)` for a `const Half: f64 = 0.5`. `f32(0.1)` and `f32(1e39)` would round or overflow, so they are E0303, and so is an operator over float literals or constants, `f32(0.25 + 0.25)` or `f32(Half + Half)`, which is not judged by its value: write `x: f32 = 0.1`, where the literal takes `f32` and is rounded once, or `Cast[f32](0.1)`.
- `Cast` to an integer truncates a float toward zero, and panics on NaN, an infinity or a value out of range.
- `Cast` from an integer to a float rounds to the nearest value, ties to even, and never panics: `Cast[f64](n)`.
- `Cast` from `f64` to `f32` rounds to nearest-even. It panics only when a finite value rounds to an infinity; NaN and the infinities pass through.
- `Cast[char](n)` panics when `n` is not a scalar value. `Cast[E](n)` for an enum `E` panics when `n` names no variant.
- `bool`, `str`, references and slices have no conversions (E0302). A struct is never converted: for a struct `Feet`, `Feet(m)` is a construction, and reports E0305. An enum with no tag type converts to and from nothing.

```mk
// verdict: ok
Widen(a: i32): i64 {
    return i64(a)
}
```

```mk
// verdict: reject E0303
Narrow(a: i64): i32 {
    return i32(a) // <- E0303
}
```

Write `Cast[i32](a)` to check the value at run time, or `Trunc[i32](a)` to keep its low bits.

```mk
// verdict: reject E0302
Flag(b: bool): int {
    return int(b) // <- E0302
}
```

Write `if b { 1 } else { 0 }`.

A constant that overflows fails at compile time:

```mk
// verdict: reject E0901
const Big: u8 = 200 + 100 // <- E0901
```

### `void` and `never`

`void` is the result of a function that returns no value; leaving out the result type means `void`. `never` is the result of a function that does not return at all, such as `Panic`.

```mk
// verdict: ok
Fatal(msg: str): never {
    Panic(msg)
}

Digit(c: char): int {
    if c < '0' || c > '9' {
        Fatal("not a digit") // a never call ends this path: no return needed
    }
    return int(u32(c) - u32('0'))
}

Log(sys: &System, n: int) { // no result type: void
    sys.Print("{n}\n", (n)) else {}
}

export Main(sys: &System) {
    Log(sys, Digit('7'))
    Log(sys, Digit('x'))
}
```

```text
7
panic: not a digit at main.mk:3:5
backtrace:
  Fatal (main.mk)
  Digit (main.mk)
  Main (main.mk)
```

- `void` appears only as a result type, as the `T` of `E!void`, as the pointee of `*void`, and as a type argument of a generic call, written or inferred (`raw.Null[void]()` is a null `*void`). A local or field of type `void` is E0301, and so is `void` as the argument of a generic type, `Box[void]`.
- A `never` function must not return. `return` in one is E0301; every path ends in a call of another `never` function, such as `Panic`, or loops forever. A body that can reach its end is E0805 ([functions](#functions-and-methods)).
- A call of a `never` function ends its path, so it may close an `else` block or a `match` arm where a value is expected.

```mk
// verdict: reject E0301
Stop(): never {
    return // <- E0301
}
```

### Strings: `str`

A `str` is a read-only view of valid UTF-8 bytes that live elsewhere: a literal, part of a file you read, or text built in an arena. It is a pointer and a length, 16 bytes, and it is plain.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    s := "naïve"
    n := s.Len() // bytes, not characters: ï takes two
    head := s[0..2] // byte offsets
    tail := s[4..]
    first := s.Byte(0) // one byte, as a u8
    var count := 0
    for _ in s.Scalars() { // the characters, decoded
        count += 1
    }
    same := s == "naïve" // == compares the bytes
    before := "apple" < "banana"
    try sys.Print("{n} {head} {tail} {first} {count} {same} {before}\n", (n, head, tail, first, count, same, before))
    cut := s[0..3] // byte 3 is inside ï
    try sys.Print("{cut}\n", (cut))
}
```

```text
6 na ve 110 5 true true
panic: str boundary at main.mk:15:12
backtrace:
  Main (main.mk)
```

- `Len` counts bytes. `s[a..b]` slices by byte offsets, which must fall on a scalar boundary, else it panics with `str boundary`.
- A `str` is sliced, never indexed: `s[0]` is E0308. Read a byte with `s.Byte(i)`, all bytes with `s.AsBytes()`, and the characters with `s.Scalars()`.
- `==` and `!=` compare contents. `<`, `<=`, `>` and `>=` compare bytes, which for UTF-8 is the order of scalar values.
- `str` has no `+`. Build text with `arena.Fmt` or a `Text` ([strings and text](std/core.md#strings-and-text)).
- `b"..."` is a `[]u8`, not a `str`. Bytes become a `str` with `str.From(bytes)`, which checks the UTF-8 and can fail with `Utf8Error`.
- A `str` borrows what it points to, like a slice. A literal points to static data and lives for the whole program. Who may hold a `str` built in an arena is part of [the memory model](memory.md#r7-values-hold-loans).

```mk
// verdict: reject E0308
First(s: str): u8 {
    return s[0] // <- E0308
}
```

### Arrays: `[N]T`

An array holds `N` values of `T` inline, with no pointer and no header. It is a value: assigning or passing one copies every element.

```mk
// verdict: ok
const Rows: int = 2

Total(xs: []int): int {
    var t := 0
    for x in xs {
        t += x
    }
    return t
}

export Main(sys: &System): WriteError!void {
    var a := [1, 2, 3] // [3]int
    b := a // a copy of all three elements
    a[0] = 10
    var grid: [Rows][3]u8 = [[0; 3]; Rows] // [value; N] repeats a value
    grid[1][2] = 7
    t := Total(a[..]) // a[..] is a slice of the whole array
    n := a.Len()
    size := SizeOf[[Rows][3]u8]()
    try sys.Print("{a0} {b0} {g} {t} {n} {size}\n", (a0: a[0], b0: b[0], g: grid[1][2], t, n, size))
    i := sys.args.Len() + 2
    try sys.Print("{x}\n", (x: a[i])) // index 3 of a [3]int
}
```

```text
10 1 7 15 3 6
panic: bounds at main.mk:23:32
backtrace:
  Main (main.mk)
```

- `N` is a compile-time constant: a literal, a `const`, or a const generic such as `N` in `[const N: int]` ([section 10](#const-parameters)). A run-time length is E0705; use a slice from an arena instead.
- Array literals, `[e; N]` and indexing: see [array literals](#array-record-and-struct-literals) and [indexing](#indexing-and-slices).
- An array is not a slice. Pass `a[..]` where a `[]T` is expected, and `mut a[..]` where a `mut []T` is.
- An array has the kind of its element, and its size is `N` times the element's.
- `[a, b] := xs` takes an array apart, each element into its own binding; it is how an array of linear values is consumed ([destructuring](#destructuring)).
- In a `const` initializer an array literal may also be a `[]T` over static data ([section 11](#compile-time-arenas-and-static-slices)).

```mk
// verdict: reject E0705
Zeros(n: int): int {
    a: [n]int = [0; 4] // <- E0705
    return a.Len()
}
```

### Slices: `[]T` and `mut []T`

A slice is a view of consecutive values that live elsewhere: in an array, a `List`, an arena or static data. It is a pointer and a length, 16 bytes. `[]T` reads the elements; `mut []T` may also write them.

```mk
// verdict: ok
Sum(xs: []int): int { // a shared slice: read only
    var s := 0
    for x in xs {
        s += x
    }
    return s
}

Clear(xs: mut []int) { // an exclusive slice: the elements may change
    for i in 0..xs.Len() {
        xs[i] = 0
    }
}

DropFirst(xs: mut ([]int)) { // a reference to a slice value: the slice may change
    *xs = (*xs)[1..]
}

export Main(sys: &System): WriteError!void {
    var a := [1, 2, 3, 4, 5]
    Clear(mut a[3..]) // a holds [1, 2, 3, 0, 0]
    var part := a[..]
    DropFirst(mut part) // part covers a[1..]
    s := Sum(part)
    n := part.Len()
    try sys.Print("{s} {n}\n", (s, n))
}
```

```text
5 4
```

| Form | Type |
|---|---|
| `xs[a..b]`, `xs[a..]`, `xs[..b]`, `xs[..]` | `[]T`, a shared view |
| `mut xs[a..b]` | `mut []T`, an exclusive view; `xs` must be `var` or itself exclusive |
| `mut ([]T)` | An exclusive reference to a slice value, to change which elements it covers |

- `[]T` is plain: copying one copies the view, not the elements. `mut []T` is unique: it moves, because only one exclusive view of an element may be live.
- Indexing and slicing, and their bounds checks, are in [indexing](#indexing-and-slices).
- `xs[i] = v` writes through a `mut []T`. Through a `[]T` it is E0507.
- A slice borrows what it views. The rules for how long it may live are in [the memory model](memory.md#r7-values-hold-loans).
- Slices, arrays and lists share their methods (`Len`, `Sort`, `Search`, `Reverse`, `Chunks` and others; [slices and lists](std/core.md#slices-and-lists)).

```mk
// verdict: reject E0301
Total(xs: []int): int {
    return xs.Len()
}

Count(): int {
    a := [1, 2, 3]
    return Total(a) // <- E0301
}
```

Write `Total(a[..])`.

### Struct types

A struct is a named type with named fields. Two structs are different types even when their fields are the same. Declaring a struct, its methods and its layout is in [section 4](#4-declarations).

```mk
// verdict: ok
struct Size {
    export w: int
    export h: int
}

struct Window {
    title: str
    size: Size // a struct field holds the whole value, not a pointer
}

Grow(win: mut Window, by: int) {
    win.size.w += by // fields read and write through a reference
}

export Main(sys: &System): WriteError!void {
    var win := Window(title: "main", size: Size(w: 640, h: 480))
    before := win // Window is plain: a copy
    Grow(mut win, 100)
    Window(title, size) := win // destructure into locals named after the fields
    try sys.Print("{title} {size.w}x{size.h}, before {c.w}\n", (title, size, c: before.size))
}
```

```text
main 740x480, before 640
```

- Building a struct and writing its fields: [structs](#structs).
- A struct's kind is the worst kind of its fields ([kinds](#kinds)). A struct can be declared `linear` or `view`.
- To compare two structs, compare their fields, or implement `Eq` and call `a.Eq(&b)`.
- A struct with no `Format` prints only through its fields: `{size.w}` names an exported field.

```mk
// verdict: reject E0301
struct Meters {
    v: f64
}

struct Feet {
    v: f64
}

ToFeet(m: Meters): Feet {
    return m // <- E0301
}
```

Write `Feet(v: m.v * 3.28084)`.

### Enum types

An enum value is one of a fixed list of variants. A variant may carry fields of its own, so an enum is also a tagged union.

```mk
// verdict: ok
enum Dir {
    North
    East
    South
    West
}

enum Level: u8 { // a tag type: u8, with chosen tag values
    Debug = 10
    Info // one more than the previous: 11
    Error = 40
}

enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

Turn(d: Dir): Dir {
    return match d {
        .North { Dir.East }
        .East { Dir.South }
        .South { Dir.West }
        .West { Dir.North }
    }
}

Area(s: Shape): f64 {
    return match s {
        .Circle(r) { 3.0 * r * r }
        .Rect(w, h) { w * h }
        .Empty { 0.0 }
    }
}

export Main(sys: &System): WriteError!void {
    back := Turn(Turn(Dir.North)) == Dir.South // == on an enum without payloads
    info := u8(Level.Info) // an enum with a tag type converts to it
    level := Cast[Level](40) // Cast checks the tag
    err := level == Level.Error
    area := Area(Shape.Rect(w: 2.0, h: 3.0))
    try sys.Print("{back} {info} {err} {area}\n", (back, info, err, area))
    bad := Cast[Level](3) // 3 names no variant
    try sys.Print("{b}\n", (b: u8(bad)))
}
```

```text
true 11 true 6
panic: cast at main.mk:45:12
backtrace:
  Main (main.mk)
```

- Declaring enums, their variants and tag types, and `open enum`: [enums](#enums), [enum tags](#enum-tags). A pattern may leave the type out: `.North` ([section 6](#6-patterns)).
- To compare enums whose variants carry fields, use `match`: `==` on them is E0308.
- An enum has no built-in `Format`; using one in a format string is E0706. Convert it, `match` it, or implement `Format`.
- An enum's kind is the worst kind among its variants' fields. Its size is the tag plus the largest variant.

```mk
// verdict: reject E0308
enum Shape {
    Circle(r: f64)
    Empty
}

IsEmpty(s: Shape): bool {
    return s == Shape.Empty // <- E0308
}
```

Use a `match` with an `.Empty { true }` arm and a `_ { false }` arm, one arm per line.

### Record types

A record is a struct with no name: `(lo: int, hi: int)`. It is structural: two records with the same field names and types are the same type. Records carry several results, group the arguments of `Print`, and name small values without a declaration.

```mk
// verdict: ok
type Range = (lo: int, hi: int) // a name for a record type

Bounds(xs: []int): (lo: int, hi: int) {
    var lo := xs[0]
    var hi := xs[0]
    for x in xs {
        if x < lo {
            lo = x
        }
        if x > hi {
            hi = x
        }
    }
    return (lo, hi) // (lo) is short for (lo: lo)
}

Width(r: Range): int {
    return r.hi - r.lo
}

export Main(sys: &System): WriteError!void {
    xs := [4, -2, 9]
    b := Bounds(xs[..])
    w := Width(b) // the same fields make the same type
    flipped: (hi: int, lo: int) = b // field order does not matter
    (lo, hi) := flipped // destructure by field name
    try sys.Print("{lo} {hi} {w}\n", (lo, hi, w))
}
```

```text
-2 9 11
```

- Every field has a name: `(1, 2)` is E0101. A name alone, `(lo)`, is short for `(lo: lo)`.
- A record is the set of its fields. `(lo: int, hi: int)` and `(hi: int, lo: int)` are one type. A record with an extra field is a different type (E0301).
- Fields are laid out in the byte order of their names, and `fields` visits them in that order ([section 11](#fields)).
- `(lo, hi) := r` destructures by name. `r.lo` reads a field.
- A record's kind is the worst kind of its fields. Records have no methods.

```mk
// verdict: reject E0101
Pair(): (a: int, b: int) {
    return (1, 2) // <- E0101
}
```

### Error sets and results

An error set `A | B` is the one union type that you write with `|`: a value of it is one of the errors listed. `E!T` is either an error from the set `E` or a value `T`. Both are described in [section 9](#9-errors).

- Their rules are in [error sets](#error-sets) and [results](#results-et).
- `E!T` has the kind of `T`. An error payload holds plain data and static strings: a `str` field holds only a string that lives as long as the program (E0605), and a slice, `&T`, or any other type holding a reference is E0408 ([declaring errors](#declaring-errors)).

### Optionals: `?T`

`?T` holds either a `T`, written `some(x)`, or nothing, written `none`. Mk has no null: a value that may be missing has an optional type.

```mk
// verdict: ok
Find(xs: []int, v: int): ?int {
    for i, x in xs {
        if x == v {
            return some(i)
        }
    }
    return none
}

export Main(sys: &System): WriteError!void {
    xs := [10, 20, 30]
    if i := Find(xs[..], 20) { // binds i only when there is a value
        try sys.Print("20 is at {i}\n", (i))
    }
    j := Find(xs[..], 99) else { -1 } // a default for none
    k := match Find(xs[..], 30) {
        some(v) { v }
        none { -1 }
    }
    var slot: ?int = none
    empty := slot.IsNone()
    slot = some(5)
    try sys.Print("{j} {k} {empty} {slot}\n", (j, k, empty, slot))
    m := Find(xs[..], 7).Must() // a panic on none
    try sys.Print("{m}\n", (m))
}
```

```text
20 is at 1
-1 2 true some(5)
panic: must at main.mk:25:10
backtrace:
  Main (main.mk)
```

| Form | Meaning |
|---|---|
| `some(x)`, `none` | The two values. `none` takes its type from where it is used |
| `if x := o { ... }` | Run the block with `x` bound when `o` holds a value |
| `o else { d }` | The value, or `d` |
| `match o { some(x) { ... } none { ... } }` | Handle both cases |
| `o.Must()` | The value, or a panic |
| `o.IsSome()`, `o.IsNone()` | Test without taking the value |

- Optionals have no `==`. `o == none` is E0308, and its message names `.IsNone()`.
- `try` does not work on an optional (E0804): there is no error to pass on. Use `else` or `if x := o`.
- `?&T`, `?mut T` and `?fn(...)` are pointer-sized: `none` is the null address. Other optionals add a tag, so `?int` is 16 bytes.
- `?T` has the kind of `T`, so a `?Res` must be consumed like a `Res`. Optionals nest: `??int` can be `some(none)`.
- An optional prints as `some(5)` or `none`.

```mk
// verdict: reject E0308
Missing(o: ?int): bool {
    return o == none // <- E0308
}
```

### References: `&T` and `mut T`

A reference points at a value that lives elsewhere. `&T` is shared: many may exist at once, and none may write. `mut T` is exclusive: while it is live, nothing else reaches the value, and it may write. A reference is never null.

```mk
// verdict: ok
struct Tally {
    n: int
}

Bump(t: mut Tally) {
    t.n += 1 // a field through a reference: no * needed
}

Double(x: mut int) {
    *x = *x * 2 // the whole value through a reference: write *
}

Read(t: &Tally): int {
    return t.n
}

export Main(sys: &System): WriteError!void {
    var t := Tally(n: 0) // mut t needs a var
    Bump(mut t)
    Bump(mut t)
    var v := 21
    Double(mut v)
    r := &t // a shared reference
    n := Read(r)
    snapshot := *r // * reads the whole value: a copy of t
    try sys.Print("{n} {c} {v}\n", (n, c: snapshot.n, v))
}
```

```text
2 2 42
```

- `&x` makes a shared reference and `mut x` an exclusive one. `mut x` needs `x` to be `var`, or itself reached through an exclusive reference (E0508, [bindings](#bindings)).
- Field access and method calls go through a reference with no `*`. `*r` reads or writes the whole value.
- `&T` is plain and is copied. `mut T` is unique and moves; passing it to a call lends it for the call ([reborrowing](memory.md#r12-reborrowing)).
- A reference is 8 bytes. It has no arithmetic. For a reference that may be missing, use `?&T`.
- The compiler checks that no reference outlives its value and that no two references conflict. The rules are in [the memory model](memory.md), and [section 8](#8-ownership-and-borrowing) summarizes them.

A `mut T` is not a `&T`: no conversion turns one into the other. Where a `&T` is expected, write the shared reborrow `&*p`. E0301 names it. `mk fix` writes it when it is the whole operand of `return`, the function has no `defer` or `onerr`, and the reference is read through a parameter or is a call or `try` result; elsewhere a later use may conflict with the new shared loan, or the loan may be of a local, so it is offered, not applied:

```mk
// verdict: reject E0301
struct Node {
    next: ?&Node
    v: int
}

Push(a: &Arena, head: ?&Node into a, v: int): OutOfMemory!&Node from a, head {
    p := try a.Put(Node(next: head, v: v))
    return p // <- E0301
}
```

Write `return &*p`:

```mk
// verdict: ok
struct Node {
    next: ?&Node
    v: int
}

Push(a: &Arena, head: ?&Node into a, v: int): OutOfMemory!&Node from a, head {
    p := try a.Put(Node(next: head, v: v))
    return &*p
}
```

### Raw pointer types: `*T`

`*T` is an address with no checks: it may be null, dangle or alias. You need one only to talk to C, to hardware, or to build a container the borrow rules cannot express. A raw pointer is plain and 8 bytes; `*void` is an untyped address, C's `void *`, made from a typed pointer with `raw.PtrCast[T, void](p)`.

```mk
// verdict: ok
import std.raw

export Main(sys: &System): WriteError!void {
    var x := 41
    p: *int = raw.OfMut(mut x) // an address from a reference: safe
    unsafe "p points at x, which is live" {
        *p = *p + 1 // a dereference: unsafe only
    }
    q := raw.Null[int]()
    z := raw.IsNull(q)
    try sys.Print("{x} {z}\n", (x, z))
}
```

```text
42 true
```

This program runs only in a module listed in the manifest's `unsafe_modules` ([packages](packages.md)), which is also the only kind of module outside std that may import `std.raw`.

- A raw pointer has no `==`: compare two with `raw.Same`.
- What you may do with one, and where, is in [raw pointers](#raw-pointers). [Undefined behaviour](#undefined-behaviour) lists what `unsafe` code must never do.

```mk
// verdict: reject E1103
Load(p: *int): int {
    return *p // <- E1103
}
```

### Function types

`fn(int, int): int` is the type of a function value: a declared function named without a call, or a function literal that uses no locals. A function value holds no captured state. It is plain and 8 bytes.

```mk
// verdict: ok
Add(a: int, b: int): int {
    return a + b
}

Max(a: int, b: int): int {
    return if a > b { a } else { b }
}

type Combine = fn(int, int): int // an alias for a function type

Fold(xs: []int, start: int, f: Combine): int {
    var acc := start
    for x in xs {
        acc = f(acc, x)
    }
    return acc
}

struct Op {
    export name: str
    apply: fn(int, int): int // a field may hold a function
}

export Main(sys: &System): WriteError!void {
    xs := [3, 9, 4]
    ops := [Op(name: "sum", apply: Add), Op(name: "max", apply: Max)]
    for op in ops[..] {
        r := Fold(xs[..], 0, op.apply)
        try sys.Print("{op.name}: {r}\n", (op, r))
    }
    square := fn(x: int): int { return x * x } // a literal that uses no locals
    var hook: ?fn(int): int = none
    hook = some(square)
    if f := hook {
        try sys.Print("{n}\n", (n: f(7)))
    }
}
```

```text
sum: 16
max: 9
49
```

- Parameter names in a `fn` type are optional: `fn(int): int`. A type with `from` or `into` clauses must name the parameters they mention: `fn(a: &str, b: &str): &str from a`.
- A function fits a `fn` type when its parameter and result types are the same, and the type's `from` and `into` clauses cover the function's (E0608).
- A `fn` value is never null. For an optional callback, use `?fn(...)`.
- `fn` values have no `==`.
- Function values, literals and generic instances as values: [function values](#function-values), [function literals](#function-literals).

```mk
// verdict: reject E0201
First(a: &str, b: &str): &str from a {
    return a
}

Pick(): fn(&str, &str): &str from a { // <- E0201
    return First
}
```

Write `fn(a: &str, b: &str): &str from a`.

### Type aliases

`type Name = T` gives a type a second name. The alias and `T` are the same type and mix freely. For a distinct type that does not mix, declare a struct with one field.

```mk
// verdict: ok
type Meters = f64 // another name for f64
type Pair[T] = (first: T, second: T) // an alias may take type parameters
type Bytes = []u8

struct Celsius { // a distinct type: a struct with one field
    deg: f64
}

Flip[T: Plain](p: Pair[T]): Pair[T] {
    return (first: p.second, second: p.first)
}

Total(a: Meters, b: f64): Meters {
    return a + b // Meters is f64: they mix freely
}

Warm(c: Celsius): bool {
    return c.deg > 20.0
}

export Main(sys: &System): WriteError!void {
    d := Total(1.5, 2.0)
    p := Flip[int]((first: 1, second: 2))
    b: Bytes = b"abc"
    w := Warm(Celsius(deg: 25.0))
    try sys.Print("{d} {p.first} {n} {w}\n", (d, p, n: b.Len(), w))
}
```

```text
3.5 2 3 true
```

- An alias names any type: a number, a record, a slice, an error set (`type LoadError = NotFound | Denied`) or a `fn` type.
- An alias may have type parameters: `Pair[int]` is `(first: int, second: int)`.
- `open type` marks an error set that a later release may extend ([open sets](#error-sets)).
- Methods belong to the type, not to the alias. A method through an alias of a type from another module, such as `f64`, is E0704.

```mk
// verdict: reject E0704
type Meters = f64

Meters.Double(self): f64 { // <- E0704
    return self * 2.0
}
```

### Kinds

Every type has a kind, which says what happens when you use a value of it: pass it, assign it, return it. Every type is plain, unique or linear: [R1](memory.md#r1-value-kinds) gives what each means and which types are which.

```mk
// verdict: ok
struct Spot { // plain: every field is plain
    x: int
    y: int
}

struct Bag { // unique: a List is unique
    items: List[int]
}

linear struct Pass { // linear: must be consumed
    id: int
}

Pass.Use(self): int {
    Pass(id) := self // destructuring consumes the value
    return id
}

Count(b: Bag): int {
    return b.items.Len()
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := Spot(x: 1, y: 2)
    b := a // a copy: a is still usable
    var bag := Bag(items: List[int].New(sys.arena))
    try bag.items.Push(5)
    moved := bag // a move: bag cannot be used again
    p := Pass(id: 7)
    id := p.Use() // consumes p; leaving p unconsumed is E0402
    try sys.Print("{ax} {bx} {n} {id}\n", (ax: a.x, bx: b.x, n: Count(moved), id))
}
```

```text
1 1 1 7
```

- How a type gets its kind: [value kinds](memory.md#r1-value-kinds).
- How a linear value is consumed, and E0402: [linear values](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path).
- Declaring a type `linear` or `view`: [`linear` and `view` structs](#linear-and-view-structs).
- What an exported struct must declare about its kind (E0410): [exported kinds](#exported-kinds).
- In a generic function, an unbounded `T` may be linear. `T: Plain` asks for a plain type (E0701 otherwise; [section 10](#10-generics-and-interfaces)).
- `Send` and `Share` say whether a value may cross to another task, and be shared by `&` between tasks. The compiler computes them from the fields ([section 12](#12-concurrency)).

A value that moved cannot be used again:

```mk
// verdict: reject E0401
struct Bag {
    items: List[int]
}

Both(b: Bag): int {
    c := b // b moves into c
    return b.items.Len() + c.items.Len() // <- E0401
}
```

A unique type is not `Plain`:

```mk
// verdict: reject E0701
struct Bag {
    items: List[int]
}

Keep[T: Plain](x: T): T {
    return x
}

Again(b: Bag): Bag {
    return Keep(b) // <- E0701
}
```

The library's types have these kinds:

| Library types | Kind | Release | Crosses tasks by value | Shared across tasks by `&` |
|---|---|---|---|---|
| `List Map Set Text` | Unique | None. The arena ends them | No: they hold `&Arena` | No |
| `Arena` | Linear | `Free()`, or `Reset()` to reuse | No | No |
| `Heap` | Reached by `&Heap` | Never | | Yes |
| `HeapList HeapMap TextMap HeapText Box Shared` | Linear | `Free(a)`; `Shared` lives on the heap and takes `&Heap` | If contents are | If contents are |
| `fs.File net.Stream` | Linear | `File.Close(io)` can fail, with `Abandon()` on error paths. `Stream.Close()` | Yes | No |
| `net.Listener` | Linear | `Close()` | Yes | Yes |
| `Guard` | Linear | `Unlock()` | No | No |
| `Mutex[T] Chan[T]` | Unique, or linear if `T` is | `Mutex.Into()` gives `T` back; `Chan.Free(a)` | If `T: Send` | If `T: Send` |
| `Cell[T]` | Unique | None | If `T` is | No |
| `Reader Writer Alloc` | Unique, `&Alloc` plain | None | No | No |
| `Io`, `&Scope` | Plain | None | Yes | Yes |

Every type in the table is a `view`, because its memory sits behind a private pointer, except these:

- `Cell` and `Mutex`, which store `T` inline;
- `File`, `Stream` and `Listener`, which hold no references;
- `Io`, `Heap` and `Alloc`, which are handles. `Io` holds no loans.

The iterators `Split`, `Lines`, `Words`, `Scalars`, `MapIter`, `TextMapIter`, `Chunks`, `ChunksMut` and `PoolIter` are views too.

### Zero-valid types

A type is zero-valid when all-zero bytes are a value of it. `Arena.Slice[T](n)` returns zeroed elements, and [`mem.Zeroed[T]()`](std/mem.md#layout) returns one zeroed value, so both need one.

A zero-valid type is plain, and is one of: an integer, a float, `bool`, `char`, `void`, a raw pointer, a `?T` of a plain `T`, an array, record or struct whose parts are all zero-valid, an enum whose variant with tag 0 exists and has only zero-valid fields, or an `E!T` whose `T` is zero-valid. A reference, `fn` value, `str`, slice, error-set value, type parameter, or struct with an `invariant` is not.

```mk
// verdict: reject E0701
Names(a: &Arena): OutOfMemory!int {
    xs := try a.Slice[str](4) // <- E0701
    return xs.Len()
}
```

`mem.Zeroed` is the way to fill a plain record of many fields, such as a C import's struct, without naming each one: every integer and float is 0, every `bool` false, every raw pointer null and every optional `none`. It is safe, because its `T` must be zero-valid. Its `T` is its result, so it is not an error union either: `mem.Zeroed[E!T]()` is E0701, and `mem.Zeroed[T]()` makes the value. Mk still has no implicit zero value: a local is assigned before it is read, and `mem.Zeroed` is a call that says so.

```mk
// verdict: ok
import std.mem

struct Stats {
    count: int
    total: f64
    last: ?u32
    buckets: [8]u32
}

export Main(sys: &System): WriteError!void {
    var s := mem.Zeroed[Stats]()
    s.count += 1
    try sys.Print("{c} {b}\n", (c: s.count, b: s.buckets[7]))
}
```

```mk
// verdict: reject E0701
import std.mem

struct Named {
    id: int
    name: str
}

Blank(): Named {
    return mem.Zeroed[Named]() // <- E0701
}
```

### Sizes

`SizeOf[T]()` and `AlignOf[T]()` give a type's size and alignment in bytes, as an `int`.

```mk
// verdict: ok
enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

export Main(sys: &System): WriteError!void {
    try sys.Print("bool {a}, char {b}, int {c}\n", (a: SizeOf[bool](), b: SizeOf[char](), c: SizeOf[int]()))
    try sys.Print("&int {a}, []int {b}, str {c}\n", (a: SizeOf[&int](), b: SizeOf[[]int](), c: SizeOf[str]()))
    try sys.Print("?int {a}, ?&int {b}, fn {c}\n", (a: SizeOf[?int](), b: SizeOf[?&int](), c: SizeOf[fn(int): int]()))
    try sys.Print("[3]u16 {a}, Shape {b}, align {c}\n", (a: SizeOf[[3]u16](), b: SizeOf[Shape](), c: AlignOf[(a: u8, b: i64)]()))
}
```

```text
bool 1, char 4, int 8
&int 8, []int 16, str 16
?int 16, ?&int 8, fn 8
[3]u16 6, Shape 24, align 8
```

- Every integer and float has its width as its size and alignment.
- A reference, a raw pointer and a `fn` value are one word; a slice and a `str` are two.
- A struct is laid out in declaration order with natural alignment, as in C. `packed` and `align(n)` change that ([layout](#layout)).

### Vectors

`simd.Vec[T, N]` is `N` lanes of `T`, operated on together. Comparing two vectors gives a `simd.Mask[T, N]`, which holds one truth value per lane.

```mk
// verdict: ok
import std.simd

export Main(sys: &System): WriteError!void {
    xs: [4]i32 = [1, 5, 2, 8]
    v := simd.Vec[i32, 4].FromSlice(xs[..], 0)
    limit := simd.Vec[i32, 4].Splat(4)
    big := v > limit // a Mask[i32, 4]: one truth value per lane
    try sys.Print("{n} {any}\n", (n: big.CountSet(), any: big.Any()))
}
```

```text
2 true
```

- `T` is bounded by `simd.Lane`, which the integer types, `f32` and `f64` satisfy. Like `Plain`, the bound is built in: an `impl` of it is E0704.
- Both types are plain, `Send`, `Share` and zero-valid, and hold no loans.
- `Vec[T, N]` has the size of `[N]T` and the alignment of `T`, whatever the target.
- `Mask[T, N]` has the shape of `Vec[U, N]`, where `U` is the unsigned integer as wide as `T`. Each lane is all zeros or all ones.
- Every operation on them requires `N` to be 2, 4, 8, 16, 32 or 64, as a compile-time `requires`. The type itself is valid for any `N`, so a function generic over `N` restates the clause ([section 10](#compile-time-requirements)).

[Section 7](#vector-operators) lists the vector operators, and [std.simd](std/simd.md) the operations.

### Compiler errors for types

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | A record with an unnamed field, `(1, 2)` ([records](#record-types)) |
| E0201 | unknown-name | A `fn` type whose `from` names a parameter it does not name ([function types](#function-types)) |
| E0301 | type-mismatch | Operands of different types; an integer literal where a float is expected; a non-`bool` condition; two different structs; a `void` local; `return` in a `never` function |
| E0302 | no-conversion | A conversion that does not exist, such as `int(true)` ([conversions](#conversions)) |
| E0303 | lossy-conversion | `T(x)` that can change the value, a literal that does not fit its type, or an operator over literals that would panic ([integers](#integers)) |
| E0305 | field-names | A struct or variant built without naming every field ([structs](#structs)) |
| E0308 | no-operator | `==` on a type without it; arithmetic on `char`; indexing a `str` |
| E0401 | use-after-move | A unique or linear value used after it moved ([kinds](#kinds)) |
| E0402 | not-consumed | A linear value not consumed on some path ([linear values](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path)) |
| E0408 | not-plain | An error payload with a reference other than a `str` ([error sets and results](#error-sets-and-results)) |
| E0410 | must-declare-kind | An exported struct whose private fields make it `view`, `mut view` or `linear`, without the keywords ([exported kinds](#exported-kinds)) |
| E0507 | mut-through-shared | Writing an element through a `[]T` ([slices](#slices-t-and-mut-t)) |
| E0508 | mut-of-immutable | `mut x` of a local that is not `var` ([references](#references-t-and-mut-t)) |
| E0608 | flow-mismatch | A function given as a `fn` type whose clauses do not cover the function's ([function types](#function-types)) |
| E0701 | bound-unmet | A unique or linear type where `Plain` is needed; `Arena.Slice` or `mem.Zeroed` of a type that is not zero-valid, or `mem.Zeroed` of an error union |
| E0704 | impl-conflict | A method through an alias of another module's type ([aliases](#type-aliases)) |
| E0705 | not-const | An array length that is not a compile-time constant ([arrays](#arrays-nt)) |
| E0706 | requires-failed | An enum, or a struct with no `Format`, in a format string ([enums](#enum-types)) |
| E0804 | try-on-non-error | `try` on a `?T` ([optionals](#optionals-t)) |
| E0805 | missing-return | A `never` function whose body can reach its end ([void and never](#void-and-never)) |
| E0901 | const-failed | A `const` whose evaluation overflows ([conversions](#conversions)) |
| E1103 | raw-outside-unsafe | Dereferencing a `*T` outside `unsafe` ([raw pointers](#raw-pointer-types-t)) |

See also: [the memory model](memory.md) for moves, borrows and kinds in full, [section 4](#4-declarations) for declaring structs and enums, [section 7](#7-expressions) for operators, [section 14](#14-unsafe-and-c) for raw pointers and `unsafe`.

## 4. Declarations

A module is a list of declarations: structs, enums, errors, interfaces, constants, global variables, type aliases and functions. Their order in the file does not matter: an item may use one declared further down, or in another file of the module.

```mk
// verdict: ok
struct User {
    export id: i64
    name: str
}

enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

enum Color: u8 {
    Red = 1
    Green = 2
}

const MaxUsers: int = 1024

type Pair = (left: i32, right: i32)

Unit(): Shape {
    return Shape.Circle(r: 1.0)
}

Area(s: Shape): f64 {
    match s {
        .Circle(r) {
            return 3.14 * r * r
        }
        .Rect(w, h) {
            return w * h
        }
        .Empty {
            return 0.0
        }
    }
}
```

| Form | Declares |
|---|---|
| `struct Name { f: T }` | A [struct](#structs): named fields, held by value |
| `linear struct`, `plain view struct`, `view struct`, `mut view struct` | A struct with a stated [kind](#linear-and-view-structs) |
| `enum Name { ... }` | An [enum](#enums): one of several variants, one per line, each with its own fields |
| `enum Name: u8 { A = 1 }` | An enum with an integer [tag](#enum-tags) |
| `error Name(f: T)` | An error ([section 9](#9-errors)) |
| `interface Name { ... }`, `impl I for T { ... }` | An interface and its implementation ([section 10](#10-generics-and-interfaces)) |
| `const Name: T = e` | A [constant](#constants), computed at compile time |
| `var Name: T = e` | A [global variable](#global-variables) |
| `type Name = T` | A [type alias](#type-aliases) |
| `Name(p: T): R { ... }` | A [function](#functions-and-methods) |
| `Type.Name(&self): R { ... }` | A [method](#methods), or a function in a type's namespace |
| `extern Name(p: T): R` | A C function ([section 14](#14-unsafe-and-c)) |
| `test "name" { ... }` | A test ([section 15](#15-contracts-and-tests)) |
| `import std.derive`, `gen derive.Eq(User)` | An import ([section 13](#13-modules-and-build)), a generator call ([section 11](#11-compile-time)) |

`export` before an item, or before a field, makes it visible outside the module. See [exports and visibility](#exports-and-visibility).

### Structs

A struct groups named fields into one value. You build one by calling the struct's name with every field named.

```mk
// verdict: ok
struct Vec2 {
    export x: int
    export y: int
}

struct Line {
    export start: Vec2
    export stop: Vec2
    export label: str
}

export Main(sys: &System): WriteError!void {
    var a := Vec2(x: 1, y: 2) // every field named
    b := Vec2(y: 5, x: 4) // in any order
    a.x = 10 // a is var, so its fields may change
    c := a // Vec2 is plain: c is a copy
    a.y = 0
    l := Line(start: c, stop: b, label: "diagonal")
    try sys.Print("{a.x} {a.y} {c.y} {l.stop.x} {l.label}\n", (a, c, l))
}
```

```text
10 0 2 4 diagonal
```

- Construction names every field, in any order, and no field has a default (E0305). There is no positional form, and no shorthand: `Vec2(x, y)` is E0305 even when locals `x` and `y` exist.
- `s.f` reads a field. You change a field through a `var` binding or a `mut` reference ([section 5](#5-statements)).
- A struct has no `==` and no `Format` of its own (E0308, E0706). After `import std.derive`, `gen derive.Eq(Vec2)` and `gen derive.Format(Vec2)` write both for you ([section 11](#11-compile-time)): then `a.Eq(&b)` compares two values and `{a}` prints `Vec2(x: 1, y: 2)`.
- A struct cannot hold itself by value, directly or through other structs (E0907). Hold it through a reference, `next: ?&Chain`, or a `Box`.
- Field names are `snake_case` (E0207). A field and a method of one type may have names that differ only in case and underscores: a field `cpu` and a method `Cpu` are two members, and `m.cpu` reads one while `m.Cpu()` calls the other. Two fields, or two methods, or a method and a variant, that differ only in case and underscores are E0206, as is a field spelled exactly as a method.

```mk
// verdict: ok
struct Machine {
    cpu: int
}

Machine.Cpu(&self): int { // a method beside the field `cpu`
    return self.cpu * 2
}

Load(m: &Machine): int {
    return m.cpu + m.Cpu()
}
```
- `invariant c` among the fields states a condition every value must meet ([section 15](#15-contracts-and-tests)).

A struct may take type parameters. Construction infers them from the fields, or you write them:

```mk
// verdict: ok
struct Both[T] {
    export first: T
    export second: T
}

Both[T].Swap(self): Both[T] {
    Both(first, second) := self
    return Both(first: second, second: first)
}

export Main(sys: &System): WriteError!void {
    p := Both(first: 1, second: 2) // Both[int]
    q := Both[i64](first: 3, second: 4).Swap()
    try sys.Print("{p.first} {q.first}\n", (p, q))
}
```

```text
1 4
```

Construction that leaves out a field:

```mk
// verdict: reject E0305
Short(): Point {
    return Point(x: 1) // <- E0305
}
```

A struct that contains itself has no finite size:

```mk
// verdict: reject E0907
struct Chain { // <- E0907
    next: Chain
}
```

See also: [struct patterns](#struct-patterns) and [destructuring](#destructuring), [generics](#10-generics-and-interfaces).

### `linear` and `view` structs

A struct's kind is the worst kind of its fields ([section 3](#3-types-and-kinds)). A struct that holds a linear value is linear, with no keyword. Four forms state a kind or view behaviour the fields do not show:

- `linear struct` makes the struct linear even when its fields are plain. Every value must then be consumed on every path: passed on by value, or taken apart by a destructure. A struct with private fields is taken apart only in its own module, usually in a method that takes `self`.
- `plain view struct` declares a copyable, shared-only carrier view (`Kind == Plain`, `Carrier == true`, `Freeze == true`). Every field must be `Plain` and `Freeze` under the struct's own bounds (no unique/linear fields, no raw pointers, no `Cell`/`Mutex`/`Atomic`, and no unconstrained type parameter `T` stored by value); it forbids `ends` and holder `into` clauses on non-`mut` parameters (`self` / `&self`), while keeping phantom handles such as `plain view struct Ref[T] { idx: u32 }` copyable for any `T`.
- `view struct` declares a unique carrier of loans, such as a struct built on raw pointers, whose `&self` methods do not store loans through interior state. A `view struct` without `plain` moves on use.
- `mut view struct` declares an interior-writable view: a carrier whose `&self` methods store loans behind raw pointers or interior state (such as `Arena` or `dyn.Ref`). It is unique and not `Freeze`. See [`view` and `ends`](memory.md#r18-view-and-ends).

```mk
// verdict: ok
linear struct Ticket {
    seat: int
}

Ticket.Redeem(self): int {
    Ticket(seat) := self // taking it apart consumes it
    return seat
}

export Main(sys: &System): WriteError!void {
    t := Ticket(seat: 12)
    seat := t.Redeem()
    try sys.Print("seat {seat}\n", (seat))
}
```

```text
seat 12
```

A linear value that a path leaves unconsumed is E0402:

```mk
// verdict: reject E0402
linear struct Ticket {
    seat: int
}

Ticket.Redeem(self): int {
    Ticket(seat) := self
    return seat
}

Maybe(keep: bool): int {
    t := Ticket(seat: 1) // <- E0402
    if keep {
        return t.Redeem()
    }
    return 0
}
```

An exported struct must say `linear`, `view` or `mut view` when its private fields give it that kind (E0410). See [exported kinds](#exported-kinds) and [linear values](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path).

### Enums

`enum` lists the variants of a type, and each variant may declare fields. Methods on an enum usually `match` on `self`.

```mk
// verdict: ok
enum Cmd {
    Move(dx: int, dy: int)
    Say(text: str)
    Quit
}

Cmd.Describe(&self): str {
    match self {
        .Move(dx, dy) { // through &self, dx and dy are &int
            return if *dx == 0 && *dy == 0 { "stay" } else { "move" }
        }
        .Say(text) {
            return *text
        }
        .Quit {
            return "quit"
        }
    }
}

enum Dir {
    North
    East
    South
    West
}

Dir.Turn(self): Dir {
    return match self {
        .North { Dir.East }
        .East { Dir.South }
        .South { Dir.West }
        .West { Dir.North }
    }
}

export Main(sys: &System): WriteError!void {
    cmds := [Cmd.Move(dx: 0, dy: 0), Cmd.Say(text: "hello"), Cmd.Quit]
    for c in &cmds {
        d := c.Describe()
        try sys.Print("{d}\n", (d))
    }
    back := Dir.North.Turn().Turn()
    south := back == Dir.South // fieldless enums compare with ==
    try sys.Print("{south}\n", (south))
}
```

```text
stay
hello
quit
true
```

- A value names its enum: `Cmd.Quit`, `Cmd.Move(dx: 1, dy: 0)`. A pattern may leave the enum out, `.Quit`, because the type of the scrutinee is known. An expression may not: `return .Quit` is E0101.
- A variant with fields is built like a struct, every field named (E0305).
- Its fields are reached only through a pattern. `c.dx` is E0203.
- Methods are declared as for a struct ([methods](#methods)).
- An enum may take type parameters, as `enum Tree[T]` with the variants `Leaf(v: T)` and `Node(l: &Tree[T], r: &Tree[T])`, one per line. Its variants infer them as a struct's fields do.
- `open enum` lets a library add variants in a later release. See [open sets](codebase.md#open-sets).
- Comparison with `==`, kinds, sizes and printing of enums are in [enum types](#enum-types).

A struct has no `==`, a variant's field has no dot, and a plain enum converts to no number:

```mk
// verdict: reject E0203 E0302 E0308
struct Vec2 {
    x: int
    y: int
}

enum Cmd {
    Move(dx: int, dy: int)
    Quit
}

enum Dir {
    North
    South
}

Same(a: Vec2, b: Vec2): bool {
    return a == b // <- E0308
}

Dx(c: Cmd): int {
    return c.dx // <- E0203
}

Code(d: Dir): int {
    return int(d) // <- E0302
}
```

See also: [variant patterns](#variant-patterns), [exhaustiveness](#exhaustiveness).

### Enum tags

An enum whose variants have no fields may name an integer tag type. Each variant then has a number, and the enum converts to and from that type. Use it for values that cross a file format, a protocol or C.

```mk
// verdict: ok
enum Level: i8 {
    Debug // 0
    Info = 5
    Warn // 6: one above the variant before it
    Off = -1
}

export Main(sys: &System): WriteError!void {
    w := i8(Level.Warn) // to the tag: never loses information
    o := i8(Level.Off)
    l := Cast[Level](5) // from a number: panics if no variant has it
    info := l == Level.Info
    size := SizeOf[Level]()
    try sys.Print("{w} {o} {info} {size}\n", (w, o, info, size))
}
```

```text
6 -1 true 1
```

- The tag type is an integer type (E0301). The enum has its size and alignment.
- A variant without `= n` has the tag one above the variant before it. The first defaults to 0.
- Two variants may not share a tag (E0206), and every tag must fit the type (E0303).
- An enum with fields has no tag type (E0101).
- `T(e)` converts to the tag type, or to any type the tag type converts to, and `Cast[E](n)` back. `E(n)` is E0303, because not every number names a variant. An enum without a tag type converts to no number (E0302). See [conversions](#conversions).

```mk
// verdict: reject E0206 E0303
enum Op: u8 {
    Push = 1
    Pop = 1 // <- E0206
    Halt = 300 // <- E0303
}
```

See also: [conversions](#conversions), [C enums](#c-imports).

### Constants

`const` names a value computed at compile time. The type may be left out when the value gives it.

```mk
// verdict: ok
struct Vec2 {
    export x: int
    export y: int
}

const Width = 4
const Origin: Vec2 = Vec2(x: 0, y: 0)
const Primes: [4]int = [2, 3, 5, 7]
const Names: []str = ["ada", "grace"]
const Volume = Cube(Width) // a function call, run by the compiler

Cube(n: int): int {
    return n * n * n
}

export Main(sys: &System): WriteError!void {
    row: [Width]u8 = [0; Width] // a constant can size an array
    n := row.Len()
    p := Primes[2]
    x := Origin.x
    k := Names.Len()
    try sys.Print("{n} {p} {x} {k} {Volume}\n", (n, p, x, k, Volume))
}
```

```text
4 5 0 2 64
```

- The value is computed once, by the compiler, and stored in the program. It may call ordinary functions. What a compile-time evaluation may do is in [section 11](#11-compile-time).
- The type must be plain (E0408). `&Limit` is a valid reference that lives as long as the program ([globals](memory.md#r16-globals)).
- A constant cannot be assigned (E0301).
- A literal that does not fit the type is E0303. An evaluation that panics, and constants in a cycle, are compile errors ([evaluating constants](#evaluating-constants), [the stage rule](#the-stage-rule)).
- A constant of an integer, `bool`, `char` or `str` type is a pattern, matched by its value ([literal patterns](#literal-patterns)).

### Global variables

A module-level `var` is one instance shared by the whole program and every task. Its type must be safe to share (`Share`, E1002), so it is usually an atomic or a `Mutex`.

```mk
// verdict: ok
import std.atomic

var Hits: atomic.Atomic[int] = atomic.Atomic[int].New(0)

Hit() {
    _ = Hits.FetchAdd(1, atomic.Relaxed)
}

export Main(sys: &System): WriteError!void {
    Hit()
    Hit()
    n := Hits.Load(atomic.Relaxed)
    try sys.Print("{n}\n", (n))
}
```

```text
2
```

- A global is never assigned or borrowed `mut` (E0507), nor moved out (E0409): its methods change it. The rules are in [globals](memory.md#r16-globals).
- Its initial value is computed at compile time, as a constant's is.

```mk
// verdict: reject E0507
var Count: int = 0

Reset() {
    Count = 1 // <- E0507
}
```

### Functions and methods

A function has a name, optional type parameters, parameters, a result type and a body. With no result type, the function returns nothing.

```mk
// verdict: ok
Point.Origin(): Point {
    return Point(x: 0, y: 0)
}

Point.Add(mut self, dx: i32) {
    self.x += dx
}

export DoSomething[T](name: T, override: bool): i64 {
    Use(&name)
    Take(name)
    return 0
}

struct Stack2[T: Plain] {
    items: List[T]
}

Stack2[T].New(a: &Arena): Stack2[T] {
    return Stack2[T](items: List[T].New(a))
}

Stack2[T].Push(mut self, x: T into self): OutOfMemory!void {
    try self.items.Push(x)
}

Move() {
    var p := Point.Origin()
    p.Add(3)
}
```

```mk
// verdict: ok
Clamp(var n: int, top: int): int {
    if n > top {
        n = top // n is var, so it may be assigned
    }
    return n
}

Stop(msg: str): never {
    Panic(msg)
}

Half(n: int): int {
    if n % 2 == 0 {
        return n / 2
    }
    Stop("odd") // a never call ends the path: no return needed after it
}

export Main(sys: &System): WriteError!void {
    c := Clamp(15, 10)
    h := Half(8)
    try sys.Print("{c} {h}\n", (c, h))
}
```

```text
10 4
```

- Arguments are passed by position. Mk has no named arguments (E0305), default arguments, variadic functions or overloading. The argument count must match (E0304), and a second function of the same name is E0206.
- A by-value parameter cannot be assigned (E0404) unless it is declared `var`: `Clamp(var n: int)`. Consuming a parameter, as `conn.Close()` does, needs no `var`.
- Every parameter has a name. A parameter that is not used is allowed.
- A function with a result must return a value on every path, or end the path with a call to a `never` function such as `Panic` (E0805).
- To return several values, return a [record](#record-types): `(lo: int, hi: int)`.
- A function name is `PascalCase` (E0207, [names](#2-lexical-rules)). A function is a value too: `f := Next` ([function values](#7-expressions)).

Parameters and results can carry more:

| Form | See |
|---|---|
| `F[T: Bound](x: T)`, `F[const N: int]()` | [Generics](#10-generics-and-interfaces) |
| `x: &T`, `x: mut T`, `x: T into self`, `: &T from a` | [Ownership and borrowing](#8-ownership-and-borrowing) |
| `: E!T` | [Errors](#9-errors) |
| `requires c`, `ensures c` | [Contracts](#15-contracts-and-tests) |
| `extern`, `naked`, `cpu "..."`, `section "..."` | [Unsafe and C](#14-unsafe-and-c) |

A path that can reach the end without a value:

```mk
// verdict: reject E0805
Sign(n: int): int {
    if n < 0 {
        return -1
    }
} // <- E0805
```

A parameter is not a variable unless it says `var`:

```mk
// verdict: reject E0404
Bump(n: int): int {
    n += 1 // <- E0404
    return n
}
```

#### Methods

`Type.Name` declares a function in a type's namespace. If its first parameter is `self`, `&self` or `mut self`, it is a method, and you can call it with a dot.

```mk
// verdict: ok
struct Counter {
    export n: int
}

Counter.New(): Counter { // no self: called as Counter.New()
    return Counter(n: 0)
}

Counter.Get(&self): int { // reads the counter
    return self.n
}

Counter.Bump(mut self, by: int) { // changes it
    self.n += by
}

Counter.Done(self): int { // takes it by value
    return self.n * 10
}

export Main(sys: &System): WriteError!void {
    var c := Counter.New()
    c.Bump(2) // c must be var to be borrowed mut
    Counter.Bump(mut c, 3) // the same call, written in full
    g := c.Get()
    get := Counter.Get // a method is a function value
    h := get(&c)
    d := c.Done()
    try sys.Print("{g} {h} {d}\n", (g, h, d))
}
```

```text
5 5 50
```

| Receiver | The method gets | At the call |
|---|---|---|
| `self` | The value: a copy if it is plain, else the value moves in | `c.Done()` consumes `c` unless it is plain |
| `&self` | A shared reference | `c` is borrowed for the call |
| `mut self` | An exclusive reference | `c` must be `var` (E0508), and is borrowed `mut` for the call |

- The prefix supplies the receiver's type, and `self`, `&self` or `mut self` its mode. This is the only way to write a receiver.
- `c.M(a)` is `Type.M(&c, a)`, `Type.M(mut c, a)` or `Type.M(c, a)`, as the receiver's mode says, and borrows or moves `c` to match. A `mut T` `c` is reborrowed: `mut *c` or `&*c`.
- A function without `self`, such as `Counter.New`, is called through the type's name. It is how a type offers constructors.
- A function in a type's namespace must be declared in that type's module. A method on `int` or on another module's type is E0704: write a free function that takes the value.
- On a generic type, the prefix names the type parameters, `Stack2[T].Push`, and their bounds come from the type.
- Mark a method `export`, `export Counter.Get(&self): int`, to make it part of the module's public API.
- A method of an interface is declared inside its `impl` block ([section 10](#10-generics-and-interfaces)).

A `mut self` method needs a `var` receiver:

```mk
// verdict: reject E0508
struct Counter {
    n: int
}

Counter.Bump(mut self) {
    self.n += 1
}

Once() {
    c := Counter(n: 0)
    c.Bump() // <- E0508
}
```

Methods belong to the module of their type:

```mk
// verdict: reject E0704
int.Double(self): int { // <- E0704
    return self * 2
}
```

### Layout

A struct is laid out in declaration order with natural alignment, which is C's layout. Three prefixes change it.

```mk
// verdict: ok
struct Header {
    tag: u8
    len: u32
    flags: u16
}

packed struct Wire {
    tag: u8
    len: u32
    flags: u16
}

packed(2) struct Half {
    tag: u8
    len: u32
    flags: u16
}

align(64) struct Slot {
    count: u64
}

export Main(sys: &System): WriteError!void {
    a := SizeOf[Header]()
    b := AlignOf[Header]()
    c := SizeOf[Wire]()
    d := AlignOf[Wire]()
    e := SizeOf[Half]()
    f := AlignOf[Half]()
    g := SizeOf[Slot]()
    h := AlignOf[Slot]()
    try sys.Print("Header {a} {b}\nWire {c} {d}\nHalf {e} {f}\nSlot {g} {h}\n", (a, b, c, d, e, f, g, h))
}
```

```text
Header 12 4
Wire 7 1
Half 8 2
Slot 64 64
```

| Form | Effect |
|---|---|
| `packed struct` | No padding. Alignment 1. |
| `packed(2) struct` | Each field's alignment capped at `n`, which is 1, 2, 4 or 8 (else E0101). |
| `align(4096) struct` | Alignment raised to the given power of two, from 1 to 65,536. The size is rounded up to a multiple of it. |

- Every `var`, `const`, local and allocation of an `align(n)` type sits at that alignment, and `AlignOf` reports it. On macOS a `var` or `const` keeps at most the OS page (16 KiB on arm64, 4 KiB on x86_64): dyld moves the executable by whole pages. Locals and allocations keep any `n`.
- An `n` that is not such a power of two, or `align` together with `packed`, is E0706.
- A field of a packed struct may sit below its type's alignment. Reading it is safe. Borrowing it, `&w.len`, needs an `unsafe` block (E1101, [section 14](#14-unsafe-and-c)).
- A record lays out its fields in byte order of their names, not in the order written ([section 3](#3-types-and-kinds)). Use a struct where the layout matters.

```mk
// verdict: reject E0706
// align takes a power of two, and never with packed
align(48) struct Line { // <- E0706
    bytes: [48]u8
}

packed align(8) struct Both { // <- E0706
    x: u8
}
```

### Compiler errors for declarations

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | `.Quit` as an expression; a tag type on an enum with fields; two variants on one line; `packed(n)` with another `n` |
| E0203 | unknown-member | A variant's field read with a dot ([enums](#enums)) |
| E0206 | duplicate-name | Two items, two variants' tags, two fields or two methods whose names differ only in case, or a field spelled exactly as a method ([functions](#functions-and-methods), [structs](#structs), [tags](#enum-tags)) |
| E0207 | naming | A field not in `snake_case`, a function not in `PascalCase` |
| E0301 | type-mismatch | A tag type that is not an integer; an assignment to a constant |
| E0302 | no-conversion | A number from an enum without a tag type ([tags](#enum-tags)) |
| E0303 | lossy-conversion | A tag or a constant that does not fit its type; `E(n)` for an enum ([tags](#enum-tags)) |
| E0304 | argument-count | A call with the wrong number of arguments |
| E0305 | field-names | Construction without every field named; a named argument ([structs](#structs)) |
| E0308 | no-operator | `==` on a struct ([structs](#structs)) |
| E0402 | not-consumed | A linear value left unconsumed ([`linear`](#linear-and-view-structs)) |
| E0404 | assign-immutable | An assignment to a parameter that is not `var` ([functions](#functions-and-methods)) |
| E0408 | not-plain | A constant whose type is not plain ([constants](#constants)) |
| E0409 | move-out-of-reference | A move out of a global ([globals](#global-variables)) |
| E0507 | mut-through-shared | An assignment to a global, or a `mut` borrow of one ([globals](#global-variables)) |
| E0508 | mut-of-immutable | A `mut self` method called on a binding that is not `var` ([methods](#methods)) |
| E0704 | impl-conflict | A method declared outside its type's module ([methods](#methods)) |
| E0706 | requires-failed | A bad `align`, or `align` with `packed` ([layout](#layout)) |
| E0805 | missing-return | A path that reaches the end of a function without a value ([functions](#functions-and-methods)) |
| E0901 | const-failed | A constant whose evaluation panics ([constants](#constants)) |
| E0907 | layout-cycle | A struct that holds itself by value ([structs](#structs)) |
| E0909 | const-cycle | Constants that depend on each other ([constants](#constants)) |
| E1101 | needs-unsafe | `&` of a field of a packed struct ([layout](#layout)) |

E0410 and E0412, about exported declarations, are in [section 13](#13-modules-and-build), and E1002, about globals, in [the memory model](memory.md#r16-globals).

## 5. Statements

A function body is a block: a list of statements, one per line, between `{` and `}`. Statements bind and assign locals, choose and repeat, and leave the function.

```mk
// verdict: ok
struct Countdown {
    left: int
}

Countdown.Next(mut self): ?int {
    if self.left == 0 {
        return none
    }
    self.left -= 1
    return some(self.left)
}

Total(xs: []i32): i64 {
    var sum: i64 = 0
    for i in 0..xs.Len() {
        sum += i64(xs[i])
    }
    for x in xs {
        sum += i64(x)
    }
    for n in Countdown(left: 3) {
        sum += i64(n)
    }
    return sum
}

Sign(n: i32): i32 {
    return if n < 0 { -1 } else if n == 0 { 0 } else { 1 }
}

Sum(m: &Map[str, int]): int {
    var sum := 0
    for (key, value) in m.Iter() {
        sum += key.Len() + *value
    }
    return sum
}
```

| Statement | Meaning |
|---|---|
| `x := e`, `var x := e`, `x: T = e`, `x: T` | Bind a local ([bindings](#bindings)) |
| `(a, b) := r`, `Point(x, y) := p`, `[a, b] := xs` | Take a record, struct or array apart ([destructuring](#destructuring)) |
| `p = e`, `p += e` | Assign ([assignment](#assignment)) |
| `F(x)`, `_ = e` | Call for the effect, or drop a value ([expression statements](#expression-statements)) |
| `if c { } else { }`, `if x := e { }` | Choose ([`if`](#if)) |
| `for ... { }` | Repeat ([`for` loops](#for-loops)) |
| `break`, `continue`, `outer: for` | Leave or restart a loop ([labels](#break-continue-and-labels)) |
| `match e { }` | Choose by pattern ([`match`](#match)) |
| `return e` | Leave the function ([`return`](#return)) |
| `defer s` | Run `s` when the block exits ([`defer`](#defer)) |
| `become f(a)` | A tail call ([tail calls](#tail-calls)) |
| `fail e`, `onerr s` | Errors ([section 9](#9-errors)) |
| `scope s := e { }`, `s.go F(a)` | Tasks ([section 12](#12-concurrency)) |
| `unsafe "why" { }`, `asm(...)` | Unchecked code ([section 14](#14-unsafe-and-c)) |

A newline ends a statement, and the rules for breaking a long line are in [line breaks](#line-breaks). A bare `{ ... }` block is not a statement (E0101): put the code in a function, or in the arm of an `if` or `for`.

### Bindings

`x := e` declares the local `x` and gives it the value of `e`. A binding cannot be assigned again. `var x := e` declares one that can.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    name := "ada" // a binding: name cannot change
    var count := 0 // var: count may be assigned again
    count += 1
    small: i8 = -3 // with a type: the literal is an i8
    var total: i64 = 100
    total += i64(small)
    label: str // declared here, assigned below on every path
    if count > 0 {
        label = "some"
    } else {
        label = "none"
    }
    try sys.Print("{name} {count} {small} {total} {label}\n", (name, count, small, total, label))
}
```

```text
ada 1 -3 97 some
```

- Without `var`, a local cannot be assigned again (E0404) or borrowed `mut` (E0508).
- A literal binds as `int` or `f64` unless you give a type: `one: i32 = 1`. Every other value keeps the type it has. See [how a literal gets its type](#how-a-literal-gets-its-type).
- `x: T` declares `x` with no value. You assign it before each use, on every path (E0403). Without `var`, it is assigned once.
- Every local must be used (E0208). Drop a value with `_ = e`; `_ := e` is E0101. In a pattern or a loop header, `_` stands for a part you do not need: `for _ in 0..2`.
- A local cannot shadow another local of the same function, even in an inner block (E0206). It may hide a name from outside the function, such as an imported module's. A module-level name may hide a prelude name: a `struct Arena` of your own replaces the prelude's `Arena` in its module.
- Loop variables and `if x := e` bindings follow the same rules: they cannot be assigned.

A binding without `var` is fixed once it has its value:

```mk
// verdict: reject E0404
Count(): int {
    n := 0
    n = 1 // <- E0404
    return n
}
```

A local declared without a value needs one on every path before it is read:

```mk
// verdict: reject E0403
Pick(c: bool): int {
    x: int
    if c {
        x = 1
    }
    return x // <- E0403
}
```

A local in an inner block cannot reuse an outer local's name:

```mk
// verdict: reject E0206
Twice(n: int): int {
    x := n
    if n > 0 {
        x := n * 2 // <- E0206
        return x
    }
    return x
}
```

**Destructuring.** A binding can take a record, a struct or a fixed array apart: `(lo, hi) := r`, `Point(x, y) := p`, `var Point(x, y) := p`, `[a, b] := xs`. A record or struct pattern names every field or ends with `..`; an array pattern names every element. Taking a struct or array apart consumes it. See [destructuring](#destructuring) in section 6 for the rules.

See also: [kinds](#kinds) for what a binding copies or moves, [the memory model](memory.md#r3-assignment-before-use) for assignment before use.

### Assignment

`p = e` stores the value of `e` in the place `p`. A place is a `var` local, a field or element of one, or what a `mut` reference points to.

```mk
// verdict: ok
struct Account {
    owner: str
    balance: int
}

Deposit(a: mut Account, amount: int) {
    a.balance += amount // a field through a mut reference
}

export Main(sys: &System): WriteError!void {
    var acct := Account(owner: "ada", balance: 10)
    acct.balance = 20 // a field of a var local
    Deposit(mut acct, 5)
    var counts := [0, 0, 0]
    counts[1] += 2 // an element
    n := mut counts[2]
    *n = 7 // through a reference to an int: write *n
    var flags: u8 = 0b0001
    flags |= 0b0100
    flags <<= 1
    b := acct.balance
    c1 := counts[1]
    c2 := counts[2]
    try sys.Print("{b} {c1} {c2} {flags}\n", (b, c1, c2, flags))
}
```

```text
25 2 7 10
```

`p op= e` is `p = p op e`, with the same checks as the operator:

| Assignment | Operators |
|---|---|
| `=` | Store |
| `+= -= *= /= %=` | [Arithmetic](#arithmetic). Panics on overflow and on a zero divisor |
| `+%= -%= *%=` | Wrapping arithmetic |
| `<<= >>=` | Shifts |
| `&= \|= ^=` | Bit operations |

- An assignment is a statement, not an expression. `a = b = 1` is E0101.
- A field through a reference needs no `*`: `a.balance = 0`. A whole value through a reference does: `*n = 7`.
- A place reached through a `&T` cannot be written (E0507). Take a `mut T` instead.
- The right side is evaluated first, then the place. See [order of evaluation](memory.md#r13-order-of-evaluation).

```mk
// verdict: reject E0507
struct Spot {
    x: int
}

Reset(p: &Spot) {
    p.x = 0 // <- E0507
}
```

```mk
// verdict: reject E0101
Both(): int {
    var a := 0
    var b := 0
    a = b = 1 // <- E0101
    return a + b
}
```

### Expression statements

A call can stand alone as a statement when its result is `void`. A result that can fail must be handled, even an `E!void` (E0802). Any other result must be used (E0209), so a value you forget is an error rather than a silent drop.

```mk
// verdict: reject E0209
Ignored() {
    MakePoint() // <- E0209
}
```

- `_ = e` evaluates `e` and drops the value. It works for a value that is neither linear nor an error.
- An error must be handled even under `_ =` (E0802): write `_ = F() else { 0 }`. See [`else`](#else).
- A linear value cannot be dropped (E0402): consume it. See [kinds](#kinds).
- An expression that is not a call, such as `x + 1`, is E0209 as a statement too.

### `if`

`if` runs a block when a condition holds. `else if` and `else` add more blocks, tried in order.

```mk
// verdict: ok
Find(xs: []int, v: int): ?int {
    for i, x in xs {
        if x == v {
            return some(i)
        }
    }
    return none
}

export Main(sys: &System): WriteError!void {
    xs := [4, 8, 15]
    for v in [8, 16] {
        if i := Find(xs[..], v) { // runs when Find returns some(i)
            try sys.Print("{v} at {i}\n", (v, i))
        } else {
            try sys.Print("{v} missing\n", (v))
        }
    }
    var slot: ?int = some(1)
    if p := mut slot { // p is a mut int into the payload
        *p += 41
    }
    n := slot.Must()
    if n < 0 {
        try sys.Print("negative\n")
    } else if n == 0 {
        try sys.Print("zero\n")
    } else {
        try sys.Print("{n}\n", (n))
    }
}
```

```text
8 at 1
16 missing
42
```

- The condition is a `bool` (E0301). An integer or an optional is not a condition. It takes no parentheses: `if (c)` is the record `(c: c)`, and E0301. The blocks always need braces.
- `if x := e` runs the first block when the optional `e` is `some`, with `x` bound to the payload. `x` exists only in that block.
- `if x := &e` binds a `&T` to the payload, and `if x := mut e` a `mut T` through which you change it.
- `e` must be an optional (E0307). For an `E!T`, use [`else`](#else) or [`match`](#match-on-a-result).
- `if T is u8 { }` tests a type parameter at compile time. See [specialization](#specialization-if-t-is).
- Used as an expression, an `if` with an `else` gives a value, as an operand too. See [below](#if-match-and-else-as-values).

```mk
// verdict: reject E0307
Check(n: int): int {
    if x := n { // <- E0307
        return x
    }
    return 0
}
```

### `for` loops

`for` is Mk's only loop. Its header says what it repeats over.

| Form | Repeats |
|---|---|
| `for i in a..b { }` | For each integer from `a` up to, not including, `b` |
| `for x in xs { }` | For each element of a slice or array, by copy |
| `for i, x in xs { }` | The same, with the index `i` |
| `for x in &xs { }` | For each element, as a `&T` |
| `for x in mut xs { }` | For each element, as a `mut T` |
| `for (a, b) in xs { }` | For each element, taken apart by field name |
| `for x in it { }` | For each value an iterator gives |
| `for x := e { }` | While the optional `e` is `some` |
| `for cond { }` | While `cond` holds |
| `for { }` | Forever, until a `break` or `return` |
| `for name, value in fields(&x) { }` | For each field, at compile time ([section 11](#fields)) |

### Counting with a range

`for i in a..b` counts from `a` up to, not including, `b`.

```mk
// verdict: ok
Limit(sys: &System): int {
    sys.Print("limit\n") else {}
    return 3
}

export Main(sys: &System): WriteError!void {
    for i in 0..Limit(sys) { // the bounds are evaluated once
        try sys.Print("{i} ", (i))
    }
    try sys.Print("\n")
    for _ in 0..2 { // _ when the count is not needed
        try sys.Print("tick\n")
    }
    for i in 5..3 { // an empty range runs no pass
        try sys.Print("never {i}\n", (i))
    }
    n: u8 = 3
    for i in 0..n { // i is a u8, the type of the bounds
        try sys.Print("{i}", (i))
    }
    try sys.Print("\n")
}
```

```text
limit
0 1 2 
tick
tick
012
```

- Both bounds have one integer type, which is the type of `i` (E0301). A literal bound takes the other bound's type.
- The bounds are evaluated once, before the first pass.
- When `a` is not below `b`, the body never runs.
- A range counts up by one. To count down or step, use a conditional loop, `var i := n` then `for i > 0 { i -= 1 ... }`, or compute `n - 1 - i` in an upward loop.
- A range has no index form: `for i, x in 0..3` is E0101.
- `..` belongs to the loop header and to [slice brackets](#indexing-and-slices). It is not an operator that makes a value.

### Loops over slices

`for x in xs` runs once for each element of a slice, an array, or a type that implements `Slice[T]`, such as `List`. The way you write `xs` says how you get each element.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    var xs := [10, 20, 30]
    for i, x in xs { // x is a copy, i its index
        try sys.Print("{i}:{x} ", (i, x))
    }
    try sys.Print("\n")
    for x in mut xs { // x is a mut int into the array
        *x += 1
    }
    for x in mut (mut xs[1..]) { // only the elements of a sub-slice
        *x *= 2
    }
    for x in &xs { // x is a &int
        try sys.Print("{x} ", (x))
    }
    try sys.Print("\n")
    pairs := [(name: "a", n: 1), (name: "b", n: 2)]
    for (name, n) in pairs { // each element taken apart
        try sys.Print("{name}={n} ", (name, n))
    }
    try sys.Print("\n")
}
```

```text
0:10 1:20 2:30 
11 42 62 
a=1 b=2 
```

- `for x in xs` copies each element, so the element type must be plain (E0409). For other elements, write `for x in &xs` or `for x in mut xs`.
- A `str` is not a slice (E0301): loop over `s.AsBytes()` or `s.Scalars()` ([strings](#strings-str)).
- `for i, x in xs` adds the index `i`, an `int`. It works with `&xs` and `mut xs` too.
- `for x in mut xs` needs `xs` to be a `var` local (E0508) or a `mut` reference (E0507). Each `x` borrows one element; see [disjoint elements](memory.md#r20-disjoint-elements).
- In a loop header, `mut xs[1..]` reads as `mut` and the shared view `xs[1..]` (E0507). Parenthesize the exclusive sub-slice: `for x in mut (mut xs[1..])`.
- A record pattern takes each element apart by copy: `for (name, n) in pairs`. With `&` or `mut` it is E0101: write `for p in &pairs` and read `p.name`.
- The loop variable cannot be assigned (E0404).

A loop that copies elements that are not plain:

```mk
// verdict: reject E0409
Lengths(lists: []List[int]): int {
    var n := 0
    for l in lists { // <- E0409
        n += l.Len()
    }
    return n
}
```

### Iterators

`for x in it` loops over any value `it` that is not a slice and has a method `Next(mut self): ?T`. The loop calls `Next` until it returns `none`.

```mk
// verdict: ok
struct Evens {
    at: int
    end: int
}

Evens.Next(mut self): ?int {
    if self.at >= self.end {
        return none
    }
    n := self.at
    self.at += 2
    return some(n)
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    for n in Evens(at: 0, end: 7) {
        try sys.Print("{n} ", (n))
    }
    try sys.Print("\n")
    var ages := Map[str, int].New(sys.arena)
    try ages.Set("ada", 36)
    for (key: name, value: age) in ages.Iter() { // each key and value, renamed
        try sys.Print("{name} is {age}\n", (name, age))
    }
    var e := Evens(at: 10, end: 14)
    for n := e.Next() { // the same loop, written by hand
        try sys.Print("got {n}\n", (n))
    }
}
```

```text
0 2 4 6 
ada is 36
got 10
got 12
```

- `it` is evaluated once and taken by value. `for x in &it` and `for x in mut it` are E0301.
- `x` takes each payload. There is no index form.
- `x` may be a record pattern: `for (key, value) in m.Iter()`. A record pattern with `&`, `mut`, a range or `fields` is E0101.
- The standard library's iterators, such as `Split`, `Lines` and `Scalars` on a `str` ([strings](#strings-str)) and `Iter` on `Map` and the maps of [`std.coll`](std/coll.md), follow this rule.

```mk
// verdict: reject E0301
struct Bag {
    n: int
}

Loop(b: Bag) {
    for x in b { // <- E0301
        _ = x
    }
}
```

### Conditional loops

`for x := e` repeats while the optional `e` is `some`, with `x` bound to the payload. `for cond` repeats while `cond` holds. `for` alone repeats until a `break` or a `return`.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    var n := 27
    var steps := 0
    for n != 1 { // a while loop
        n = if n % 2 == 0 { n / 2 } else { 3 * n + 1 }
        steps += 1
    }
    var k := 1
    for { // an endless loop
        k *= 2
        if k > 100 {
            break
        }
    }
    try sys.Print("{steps} {k}\n", (steps, k))
}
```

```text
111 128
```

- `e` in `for x := e` and `cond` in `for cond` are evaluated again before each pass.
- The condition is a `bool` (E0301), and `e` an optional (E0307).

### `break`, `continue` and labels

`break` leaves the innermost loop. `continue` starts its next pass. A label before a `for` lets them name an outer loop.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    grid := [[1, 2, 3], [4, -1, 6], [7, 8, 9]]
    var sum := 0
    rows: for row in grid {
        for x in row {
            if x < 0 {
                continue rows // skip the rest of this row
            }
            if x > 7 {
                break rows // stop both loops
            }
            sum += x
        }
    }
    try sys.Print("{sum}\n", (sum))
}
```

```text
17
```

- A label is a name and a colon before `for`: `rows: for ...`. Only loops take labels.
- `break` or `continue` outside a loop is E0101. A label that names no enclosing loop is E0201.
- A `break` or `continue` cannot leave a `defer` or `onerr` body (E0806).
- Leaving a loop from inside an `else` handler is an error exit, which runs `onerr`. See [cleanup on the error path](#cleanup-on-the-error-path-onerr).

```mk
// verdict: reject E0201
Scan(xs: []int): int {
    for x in xs {
        if x == 0 {
            break outer // <- E0201
        }
    }
    return 0
}
```

### `match`

`match` compares a value against patterns, top to bottom, and runs the block of the first arm that matches. [Section 6](#6-patterns) lists every pattern.

```mk
// verdict: ok
enum Shape {
    Circle(r: int)
    Rect(w: int, h: int)
}

export Main(sys: &System): WriteError!void {
    a := Shape.Rect(w: 3, h: 4)
    match a {
        .Circle(r) {
            try sys.Print("circle {r}\n", (r))
        }
        .Rect(w, h) {
            try sys.Print("rect {w} by {h}\n", (w, h))
        }
    }
    area := match a { // a match as a value
        .Circle(r) { 3 * r * r }
        .Rect(w, h) { w * h }
    }
    try sys.Print("{area}\n", (area))
}
```

```text
rect 3 by 4
12
```

- The arms cover every value of the type (E0306). See [exhaustiveness](#exhaustiveness).
- Each arm is a pattern and a block, one arm per line. There is no fall-through between arms and no guard: test a condition inside the arm.
- `match e` moves a value that is not plain, and `match &e` and `match mut e` do not. See [what a binding holds](#what-a-binding-holds).
- A `match` on a result or an error set is in [section 9](#match-on-a-result).

A match that moves its scrutinee:

```mk
// verdict: reject E0401
enum Job {
    Batch(items: List[int])
    Idle
}

Size(j: &Job): int {
    return 0
}

Run(j: Job): int {
    match j {
        .Batch(items) {
            _ = items.Len()
        }
        .Idle {
        }
    }
    return Size(&j) // <- E0401
}
```

Write `match &j` to look without moving.

### `return`

`return e` leaves the function with the value `e`. A bare `return` leaves a function whose result is `void` or `E!void`.

- A function with a result must return on every path (E0805; see [functions](#functions-and-methods)). A body that ends in a `match` or `if` whose arms all return is complete.
- The value has the function's result type (E0301). A `return 1` in a `void` function and a bare `return` in an `int` function are both E0301.
- In a function that can fail, `return x` returns the success value and `fail e` the error. See [`fail`](#fail).
- Code after a `return` is allowed. Its locals still count as unused (E0208).
- `return e` evaluates `e`, then runs the pending `defer` blocks, then leaves. It cannot appear inside a `defer` or `onerr` body (E0806).

### `defer`

`defer s` runs the statement or block `s` when the enclosing block exits, by any path: the end of the block, `return`, `break`, `continue`, `fail` or `try`. Use it to release what the block acquired, next to the line that acquired it.

```mk
// verdict: ok
Step(sys: &System) {
    defer {
        sys.Print("cleanup a\n") else {}
        sys.Print("cleanup b\n") else {}
    }
    sys.Print("in Step\n") else {}
}

export Main(sys: &System): WriteError!void {
    var x := 1
    defer sys.Print("x is {x}\n", (x)) else {} // reads x at exit
    x = 2
    for i in 0..3 {
        defer sys.Print("end of pass {i}\n", (i)) else {}
        if i == 1 {
            continue
        }
        try sys.Print("pass {i}\n", (i))
    }
    Step(sys)
}
```

```text
pass 0
end of pass 0
end of pass 1
pass 2
end of pass 2
in Step
cleanup a
cleanup b
x is 2
```

- `s` runs when its block exits, not when the function returns. A `defer` in a loop body runs at the end of each pass.
- `s` is evaluated when it runs, so it sees the values of that moment.
- Handle errors inside `s` with `else`. The order of the pending `defer` and `onerr` blocks, and what their bodies cannot do (E0806), are in [cleanup on the error path](#cleanup-on-the-error-path-onerr).
- `onerr s` is a `defer` that runs only at an error exit. See [cleanup on the error path](#cleanup-on-the-error-path-onerr).
- A panic runs no `defer` ([panics](#panics)).

See also: [linear values](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path) for releasing them on every path, [the memory model](memory.md#r21-onerr-and-exits) for the exit rules.

### Tail calls

`become f(a)` calls `f` in place of the current function: the call reuses the frame, so the stack does not grow. Use it for a loop written as recursion, or a state machine of functions that call each other.

```mk
// verdict: ok
Gcd(a: u64, b: u64): u64 {
    if b == 0 {
        return a
    }
    become Gcd(b, a % b)
}

Count(n: int, acc: int): int {
    if n == 0 {
        return acc
    }
    become Count(n - 1, acc + n) // a million calls, one frame
}

export Main(sys: &System): WriteError!void {
    g := Gcd(1071, 462)
    s := Count(1000000, 0)
    try sys.Print("{g} {s}\n", (g, s))
}
```

```text
21 500000500000
```

A `become` must meet all of these:

| Rule | Error |
|---|---|
| `f` has the caller's parameter types, result, error set and flows | E0312 |
| The caller has no `ensures` clause and no `const` parameter, does not return a struct with an invariant, and is not a `mut self` method of one | E0312 |
| No `defer` or `onerr` is pending, and the `become` is not inside a `scope` block | E0406 |
| No linear value is left unconsumed | E0402 |
| No argument borrows a local of the caller | E0603 |
| Each argument is register-sized, or is the caller's own parameter at the same position and not a `var` parameter; through a `fn` value, not a parameter that some target passes as a copy in memory | E0313 |
| `f` is not an `extern` function, an intrinsic, a function with `cpu` features the caller lacks, or a method reached through an interface bound | E0314 |
| The operand is a call | E0101 |

Register-sized means an integer, a float, `bool`, `char`, a reference, a raw pointer, a `fn` value, or a plain struct or record of 0, 1, 2, 4 or 8 bytes. An enum or an optional is never register-sized, however small: pass the caller's own parameter unchanged, or an integer. A `naked` function cannot use `become` (E0314).

x86_64-linux passes a struct of more than 16 bytes, and one of more than 8 once the argument registers run out, as a copy in memory. A `become` that names its callee passes such a parameter on in constant stack; a `become` through a `fn` value cannot, so it is E0313 on every target. Name the function, or pass a reference.

```mk
// verdict: reject E0313
struct Big {
    a: int
    b: int
    c: int
}

Sum(b: Big, n: int): int {
    return b.a + b.b + b.c + n
}

const Table: [1]fn(Big, int): int = [Sum]

Through(b: Big, n: int): int {
    become Table[0](b, n) // <- E0313
}
```

```mk
// verdict: reject E0406
Down(sys: &System, n: int) {
    defer sys.Print("done\n") else {}
    if n == 0 {
        return
    }
    become Down(sys, n - 1) // <- E0406
}
```

A callee of another signature:

```mk
// verdict: reject E0312
Wide(n: i64): int {
    return 0
}

Narrow(n: int): int {
    become Wide(i64(n)) // <- E0312
}
```

```mk
// verdict: reject E0603
Walk(p: &int, n: int): int {
    if n == 0 {
        return *p
    }
    x := *p + 1
    become Walk(&x, n - 1) // <- E0603
}
```

When a rule fails, `return f(a)` is the same call without the frame reuse.

### `if`, `match` and `else` as values

Used as an expression, an `if` with an `else` and a `match` give a value: the last line of the arm that runs. They stand wherever an expression does, as an operand too: `1 + if c { 2 } else { 3 }`. An `e else { }` block gives one too ([section 9](#else)), and in parentheses it can also be an operand: `1 + (F() else { 0 })`.

```mk
// verdict: ok
Twice(n: i32): i32 {
    return n * 2
}

export Main(sys: &System): WriteError!void {
    small: i32 = 5
    c := sys.args.Len() > 0
    a := if c { small } else { 1 } // i32, the type of the arm that is not a literal
    b := Twice(if c { 10 } else { 20 }) // an argument
    r := (name: if c { "yes" } else { "no" }, n: 1) // a record field
    var d := 0
    d = match b { // the right side of an assignment
        20 {
            try sys.Print("twenty\n")
            1 // the arm's last line is its value
        }
        _ {
            2
        }
    }
    name := r.name
    try sys.Print("{a} {b} {name} {d}\n", (a, b, name, d))
}
```

```text
twenty
5 20 yes 1
```

An `if` or `match` is a value anywhere an expression stands: the right side of a binding or an assignment, the operand of `return`, an argument, a field or element of a literal, an operand of an operator, an index, a condition, and the last line of an arm that is itself a value. Suffixes and operators may follow its closing brace, and bind to the whole construct: `if c { 1 } else { 2 } * 3` multiplies the chosen value.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    c := sys.args.Len() > 0
    xs := [10, 20, 30]
    a := 1 + if c { 2 } else { 3 } // an operand
    b := xs[if c { 0 } else { 2 }] // an index
    n := match b {
        10 { "ten" }
        _ { "other" }
    }.Len() // a suffix on the whole match
    var k := 0
    if a == if c { 3 } else { 4 } { // a condition's operand
        k = 1
    }
    try sys.Print("{a} {b} {n} {k}\n", (a, b, n, k))
}
```

```text
3 10 3 1
```

The rules:

- All arms give one type: the expected type if there is one, else the type of the first arm whose value is not a literal (E0301).
- An arm that does not fall through, because it returns, breaks, continues or fails, needs no value.
- An arm whose last line is not a value is E0310. An `if` without an `else` is E0311. If an arm cannot end in a value, use the statement form and assign a local in each arm.
- Under `_ =`, an `else` block may fall through without a value; the error is ignored.
- Operands are evaluated left to right: in `x + if c { .. } else { .. }` whose arm assigns `x`, the left `x` is read before the arm runs, as it is before a call that changes `x`.
- At the start of a statement, `if` and `match` are statements, so nothing may follow the closing brace on that line (E0101). Wrap one in parentheses to use its value there: `(if c { a } else { b }).Close()`.

```mk
// verdict: reject E0311 E0310
Pick(c: bool, n: i32): i32 {
    var y := n
    x := if c { 1 } // <- E0311
    z := if c {
        2
    } else {
        y = 3 // <- E0310
    }
    return x + y + z
}
```

### Compiler errors for statements

| Code | Name | Section |
|---|---|---|
| E0101 | syntax | [bindings](#bindings), [assignment](#assignment), [ranges](#counting-with-a-range), [labels](#break-continue-and-labels), [values](#if-match-and-else-as-values) |
| E0201 | unknown-name | [labels](#break-continue-and-labels) |
| E0206 | duplicate-name | [bindings](#bindings) |
| E0208 | unused | [bindings](#bindings) |
| E0209 | unused-result | [expression statements](#expression-statements) |
| E0301 | type-mismatch | [`if`](#if), [ranges](#counting-with-a-range), [loops over slices](#loops-over-slices), [iterators](#iterators), [`return`](#return) |
| E0306 | non-exhaustive | [`match`](#match) |
| E0307 | not-optional | [`if`](#if), [conditional loops](#conditional-loops) |
| E0310 | arm-without-value | [values](#if-match-and-else-as-values) |
| E0311 | value-if-without-else | [values](#if-match-and-else-as-values) |
| E0312 | tail-call-signature | [tail calls](#tail-calls) |
| E0313 | tail-call-argument | [tail calls](#tail-calls) |
| E0314 | tail-call-target | [tail calls](#tail-calls) |
| E0401 | use-after-move | [`match`](#match) |
| E0402 | not-consumed | [expression statements](#expression-statements), [tail calls](#tail-calls) |
| E0403 | use-before-assign | [bindings](#bindings) |
| E0404 | assign-immutable | [bindings](#bindings) |
| E0406 | reserved-by-defer | [tail calls](#tail-calls) |
| E0409 | move-out-of-reference | [loops over slices](#loops-over-slices) |
| E0507 | mut-through-shared | [assignment](#assignment) |
| E0508 | mut-of-immutable | [bindings](#bindings) |
| E0603 | returns-local-borrow | [tail calls](#tail-calls) |
| E0802 | error-unhandled | [expression statements](#expression-statements) |
| E0805 | missing-return | [`return`](#return) |
| E0806 | try-in-defer | [`defer`](#defer) |

## 6. Patterns

A pattern describes the shape of a value and names its parts. Patterns appear in three places: the arms of a `match`, a destructuring binding `p := e`, and, with a record pattern, the head of a `for` loop. An array pattern `[a, b]` appears only in a destructuring binding.

```mk
// verdict: ok
Class(c: char): int {
    match c {
        'a'..'{' {
            return 1
        }
        '0' | '1' {
            return 2
        }
        other {
            return int(u32(other))
        }
    }
}
```

| Pattern | Matches |
|---|---|
| `.Rect(w, h)` | A variant, binding its fields by name |
| `.Rect(w: width, h: height)` | The same, with new names |
| `.Move(dx: 0, dy)` | A variant whose field `dx` matches the pattern `0` |
| `.Empty`, `.Move(dx)` | A variant. Fields left out match anything |
| `Shape.Rect(w, h)` | A variant, with its enum named |
| `Vec2(x: 0, y)` | A struct, by its fields |
| `NotFound(id)`, `NotFound` | An error, in a `match` on an error set |
| `some(x)`, `none`, `ok(v)`, `err(e)` | An optional or a result |
| `1`, `-1`, `true`, `"text"`, `'a'` | A literal |
| `Limit`, `codes.OpAdd` | A named const, by its value ([literals](#literal-patterns)) |
| `0..10`, `'a'..'{'`, `200..`, `..0` | A half-open range of integers or characters |
| `A \| B` | Either pattern |
| `_` | Anything, binding nothing |
| `other` | Anything, bound to the name `other` |

Patterns nest: a field's pattern may be any pattern, so `some(.Move(dx: 0, dy))` is one pattern.

### Variant patterns

A variant pattern matches one variant of an enum, and names the fields it binds.

```mk
// verdict: ok
enum Cmd {
    Move(dx: int, dy: int)
    Say(text: str)
    Quit
}

Describe(c: Cmd): str {
    match c {
        .Move(dx: 0, dy: 0) { // both fields must be 0
            return "stay"
        }
        .Move(dx: 0, dy) { // dx must be 0; dy binds the field
            return if dy > 0 { "up" } else { "down" }
        }
        .Move(dx: 1..10) { // dy left out: any value
            return "short hop"
        }
        Cmd.Move(dx: step) { // the enum named, the field renamed
            return if step < 0 { "back" } else { "far" }
        }
        .Say(text: "") {
            return "silence"
        }
        .Say(text) {
            return text
        }
        .Quit {
            return "quit"
        }
    }
}

export Main(sys: &System): WriteError!void {
    cmds := [
        Cmd.Move(dx: 0, dy: 0),
        Cmd.Move(dx: 0, dy: -2),
        Cmd.Move(dx: 3, dy: 1),
        Cmd.Move(dx: -5, dy: 0),
        Cmd.Say(text: ""),
        Cmd.Say(text: "hello"),
        Cmd.Quit,
    ]
    for c in cmds {
        d := Describe(c)
        try sys.Print("{d}\n", (d))
    }
}
```

```text
stay
down
short hop
back
silence
hello
quit
```

- Fields are named, never positional. `dx` alone binds the field `dx`, `dx: step` binds it to `step`, and `dx: 0` matches it against a pattern. A name that is not a field is E0309.
- Fields may be left out, in any number. A left-out field matches anything. `..` is not written in a `match` (E0101).
- `.Move` leaves out the enum, which the scrutinee's type gives. `Cmd.Move` names it.
- A bare name with no dot is a [binding](#bindings-and-wildcards), not a variant. `Quit { ... }` matches every value and binds it to `Quit`. The compiler reports the unused binding (E0208), which catches the missing dot.

```mk
// verdict: reject E0208
enum Cmd {
    Move(dx: int, dy: int)
    Quit
}

Steps(c: Cmd): int {
    match c {
        Quit { // <- E0208
            return 0
        }
        .Move(dx) {
            return dx
        }
    }
}
```

A field that the variant does not have:

```mk
// verdict: reject E0309
enum Cmd {
    Move(dx: int, dy: int)
    Quit
}

Steps(c: Cmd): int {
    match c {
        .Move(dz) { // <- E0309
            return dz
        }
        .Quit {
            return 0
        }
    }
}
```

See also: [enums](#enums).

### Struct patterns

A struct pattern names the struct and matches its fields, as a variant pattern does.

```mk
// verdict: ok
struct Vec2 {
    export x: int
    export y: int
}

Axis(v: Vec2): str {
    return match v {
        Vec2(x: 0, y: 0) { "origin" }
        Vec2(x: 0) { "on the y axis" }
        Vec2(y: 0) { "on the x axis" }
        _ { "elsewhere" }
    }
}

export Main(sys: &System): WriteError!void {
    for v in [Vec2(x: 0, y: 0), Vec2(x: 0, y: 3), Vec2(x: 2, y: 0), Vec2(x: 1, y: 1)] {
        a := Axis(v)
        try sys.Print("{a}\n", (a))
    }
}
```

```text
origin
on the y axis
on the x axis
elsewhere
```

- The struct's name is always written. Type arguments are never written: `Both(first: 0)`, not `Both[int](first: 0)` (E0101).
- Outside the struct's module, a pattern may name only exported fields (E0204).
- A record has no pattern in a `match` (E0101). Take it apart with a [destructuring binding](#destructuring), or compare its fields with `if`.

### Literal patterns

An integer, `bool`, `char` or `str` literal, or a named const of one of those types, matches a value equal to it.

```mk
// verdict: ok
Command(s: str): int {
    return match s {
        "start" | "go" { 1 } // a str pattern compares the contents
        "stop" { 2 }
        _ { 0 }
    }
}

Delta(n: int): str {
    return match n {
        -1 { "down" }
        0 { "same" }
        1 { "up" }
        _ { "jump" }
    }
}

Flag(b: bool): str {
    return match b {
        true { "on" }
        false { "off" }
    }
}

export Main(sys: &System): WriteError!void {
    a := Command("go")
    b := Command("halt")
    d := Delta(-1)
    f := Flag(false)
    try sys.Print("{a} {b} {d} {f}\n", (a, b, d, f))
}
```

```text
1 0 down off
```

- A negative integer is written with its `-`: `-1`.
- A float is not a pattern (E0308). Compare floats with `if`.
- A literal of a type other than the value's is E0301.
- A named const, `Limit` or `codes.Limit`, matches a value equal to its own, as the literal of that value would. Its type must be the value's (E0301, as for `==`) and one a literal takes: an integer, `bool`, `char` or `str`. A float const is E0308, and a const of any other type is E0301: match its shape instead. It covers its one value for [exhaustiveness](#exhaustiveness), as a literal does. Its value is needed before the module's own functions are ready, so a const that calls one of them is E0908 there ([the stage rule](#the-stage-rule)). Outside an alternative, a name that is no const in scope is a [binding](#bindings-and-wildcards): a misspelled const, or another module's const written without its module, matches every value, and is E0208 when its arm does not use it. In an alternative, where a name never binds, an unknown one is E0201.

```mk
// verdict: ok
const OpAdd: u8 = 1
const OpSub: u8 = 2
const Yes = true

Op(code: u8): str {
    return match code {
        OpAdd { "add" } // the value 1
        OpSub | 3 { "sub" } // a const among literals
        _ { "?" }
    }
}

Halts(b: bool): bool {
    return match b { // Yes and false cover bool
        Yes { true }
        false { false }
    }
}

export Main(sys: &System): WriteError!void {
    a := Op(2)
    h := Halts(true)
    try sys.Print("{a} {h}\n", (a, h))
}
```

```text
sub true
```

```mk
// verdict: reject E0308
Half(x: f64): bool {
    match x {
        0.5 { // <- E0308
            return true
        }
        _ {
            return false
        }
    }
}
```

### Range patterns

`lo..hi` matches an integer or a `char` from `lo` up to, not including, `hi`. Either end may be left out: `lo..` reaches the type's maximum, `..hi` starts at its minimum.

```mk
// verdict: ok
Grade(score: u8): char {
    return match score { // the four ranges cover every u8: no _ needed
        90.. { 'A' }
        80..90 { 'B' }
        60..80 { 'C' }
        ..60 { 'F' }
    }
}

Sign(n: int): str {
    return match n {
        ..0 { "negative" }
        0 { "zero" }
        1.. { "positive" }
    }
}

Kind(c: char): str {
    return match c {
        'a'..'{' | 'A'..'[' { "letter" } // '{' follows 'z', '[' follows 'Z'
        '0'..':' { "digit" }
        ' ' | '\t' | '\n' { "space" }
        _ { "other" }
    }
}

export Main(sys: &System): WriteError!void {
    scores: [4]u8 = [95, 85, 70, 12]
    for s in scores {
        g := Grade(s)
        try sys.Print("{s}: {g}\n", (s, g))
    }
    for n in [-3, 0, 8] {
        x := Sign(n)
        try sys.Print("{n}: {x}\n", (n, x))
    }
    for c in "q7 ?".Scalars() {
        k := Kind(c)
        try sys.Print("'{c}': {k}\n", (c, k))
    }
}
```

```text
95: A
85: B
70: C
12: F
-3: negative
0: zero
8: positive
'q': letter
'7': digit
' ': space
'?': other
```

- Ranges are half-open, as `for i in a..b` is. `0..10` holds 0 to 9.
- Each end is a literal: an integer, possibly negative, or a `char`.
- A range whose low end is not below its high end is empty, and is E0315.
- The compiler counts the intervals for [exhaustiveness](#exhaustiveness), so ranges that together cover a type need no `_`.

```mk
// verdict: reject E0315
Bucket(n: int): int {
    match n {
        10..5 { // <- E0315
            return 1
        }
        _ {
            return 0
        }
    }
}
```

### Alternatives

`A | B` matches when either pattern does. Alternatives may appear at any depth: `some(1 | 2)`.

- Alternatives bind no names (E0101). To share code between variants that carry data, call a function from each arm.

```mk
// verdict: reject E0101
enum Cmd {
    Move(dx: int, dy: int)
    Jump(dx: int, dy: int)
}

Dx(c: Cmd): int {
    match c {
        .Move(dx) | .Jump(dx) { // <- E0101
            return dx
        }
    }
}
```

### Bindings and wildcards

A name in a pattern matches anything and binds the value there. `_` matches anything and binds nothing.

- A binding has the type of the value it stands for. At the top of an arm, `other` has the scrutinee's whole type.
- A binding must be used (E0208). Write `_` for a part you do not need, as in `.Move(dx: _)`.
- A binding cannot shadow a local or a parameter (E0206).

```mk
// verdict: reject E0206
enum Cmd {
    Move(dx: int, dy: int)
    Quit
}

Steps(c: Cmd, dx: int): int {
    match c {
        .Move(dx) { // <- E0206
            return dx
        }
        .Quit {
            return 0
        }
    }
}
```

### What a binding holds

A binding takes its part of the value the way the scrutinee is held:

| Scrutinee | A binding of a field of type `T` is |
|---|---|
| A value, `match e` | A `T`: copied if it is plain, else moved out of `e` |
| A shared reference, `match &e`, or `e` of type `&S` | A `&T` |
| An exclusive reference, `match mut e`, or `e` of type `mut S` | A `mut T`, through which you change the field |

```mk
// verdict: ok
enum Account {
    Open(balance: int)
    Closed
}

Deposit(a: mut Account, n: int) {
    match a {
        .Open(balance) { // balance is a mut int
            *balance += n
        }
        .Closed {}
    }
}

Balance(a: &Account): int {
    match a {
        .Open(balance) { // balance is a &int
            return *balance
        }
        .Closed {
            return -1
        }
    }
}

export Main(sys: &System): WriteError!void {
    var acct := Account.Open(balance: 10)
    Deposit(mut acct, 5)
    match mut acct {
        .Open(balance) {
            *balance *= 2
        }
        .Closed {}
    }
    b := Balance(&acct)
    try sys.Print("{b}\n", (b))
}
```

```text
30
```

A `match` on a value that is not plain moves it, so the value cannot be used afterwards: [`match`](#match) shows the error and the fix.

See also: [the memory model](memory.md#r4-places-and-moves-out-of-them) for moves out of a place.

### Optionals, results and errors

`some(p)` and `none` match an optional. `ok(p)` and `err(p)` match the result of a call that can fail, or an `E!T` value. Inside them, `p` is any pattern.

```mk
// verdict: ok
enum Color {
    Red
    Green
}

Find(xs: []int, v: int): ?int {
    for i, x in xs {
        if x == v {
            return some(i)
        }
    }
    return none
}

Where(xs: []int, v: int): str {
    return match Find(xs, v) {
        some(0) { "first" }
        some(1..4) { "near the start" }
        some(_) { "later" }
        none { "absent" }
    }
}

Paint(c: ?Color): str {
    return match c {
        some(.Red) { "red" }
        some(_) { "not red" }
        none { "no color" }
    }
}

export Main(sys: &System): WriteError!void {
    data := [5, 6, 7, 8, 9, 10]
    for v in [5, 7, 10, 3] {
        w := Where(data[..], v)
        try sys.Print("{v}: {w}\n", (v, w))
    }
    p := Paint(some(Color.Green))
    try sys.Print("{p}\n", (p))
}
```

```text
5: first
7: near the start
10: later
3: absent
not red
```

- `err(e)` binds the error, of the call's whole error set. `err(NotFound(id))` matches one member and binds its payload. `err(NotFound)` matches the member whatever its payload.

```mk
// verdict: ok
error NotFound(id: int)
error Denied

Load(id: int): NotFound|Denied!int {
    if id < 0 {
        fail Denied
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return id * 10
}

Code(id: int): int {
    return match Load(id) {
        ok(n) { n }
        err(NotFound) { -1 } // any NotFound, whatever its id
        err(Denied) { -2 }
    }
}

export Main(sys: &System): WriteError!void {
    try sys.Print("{a} {b} {c}\n", (a: Code(0), b: Code(-1), c: Code(4)))
}
```

```text
-1 -2 40
```

- In a `match` on an error-set value, the members are written without `err`: `NotFound(id) { ... }`.
- `ok(p)` and `err(p)` nest like any pattern wherever an `E!T` value sits: in the ok payload of an `E!(F!T)`, which a generic whose result is its type parameter returns ([results](#results-et)), as `ok(ok(n))` and `ok(err(F))`, and in a `some`, a field or a variant's payload that a type argument makes an `E!T`, as `some(ok(v))` and `S(r: err(F))`. An alternative may join both sides: `ok(0) | err(_)`.

```mk
// verdict: ok
error Late
error Empty(slot: int)

Read(n: int): Empty!int {
    if n < 0 {
        fail Empty(slot: -n)
    }
    return n
}

Fetch[T: Plain](x: &T, late: bool): Late!T {
    if late {
        fail Late
    }
    return *x
}

Show(n: int, late: bool): int {
    var r: Empty!int = Read(n)
    return match Fetch(&r, late) {
        ok(ok(0)) | err(_) { 0 }
        ok(ok(v)) { v }
        ok(err(Empty(slot))) { 100 + slot }
    }
}

export Main(sys: &System): WriteError!void {
    try sys.Print("{a} {b} {c} {d}\n", (a: Show(5, false), b: Show(-2, false), c: Show(5, true), d: Show(0, false)))
}
```

```text
5 102 0 0
```

- `ok` and `err` exist only as pattern heads.
- [`match` on a result](#match-on-a-result) shows `ok` and `err` arms in a full example.

### Exhaustiveness

The arms of a `match` must cover every value of the scrutinee's type (E0306). The error lists up to eight values no arm covers.

| Type | Covered by |
|---|---|
| An enum | An arm for each variant, or `_` |
| `bool` | `true` and `false` |
| `?T` | `some(_)` and `none`, with the payload covered too |
| `E!T` | `ok(_)` and `err(_)`, or `ok(_)` and an arm per error |
| An error set | An arm per member |
| An integer or `char` | Literals and ranges that leave no gap, or `_` |
| `str` | Always needs `_` or a binding |

- Arms are tried from top to bottom ([`match`](#match)), so put specific patterns before general ones. An arm that an earlier arm already covers is not reported. It never runs.
- An `open enum` or open error set from another package needs `_` or a binding as well, for the members a later release may add. See [open sets](codebase.md#open-sets).
- A destructuring binding needs a pattern that cannot fail (below).

```mk
// verdict: reject E0306
Band(n: u8): int {
    match n { // <- E0306
        0..10 {
            return 1
        }
        10..200 {
            return 2
        }
    }
}
```

The error names the gap, `200..`.

### Destructuring

A binding `p := e` takes a record, a struct or a fixed array apart. Its pattern must match every value of the type: it cannot fail.

```mk
// verdict: ok
struct Vec2 {
    export x: int
    export y: int
}

struct Segment {
    start: Vec2
    stop: Vec2
    label: str
}

Divide(n: int, d: int): (q: int, r: int) {
    return (q: n / d, r: n % d)
}

export Main(sys: &System): WriteError!void {
    (q, r) := Divide(17, 5) // a record, by field name
    (r: rest, q: whole) := Divide(9, 4) // renamed, in any order
    v := Vec2(x: 1, y: 2)
    Vec2(x, y) := v // a struct
    var Vec2(x: vx, ..) := v // .. skips the rest; var makes vx assignable
    vx += 10
    s := Segment(start: v, stop: Vec2(x: 5, y: 6), label: "s")
    Segment(start: Vec2(x: sx, y: _), stop, ..) := s // nested
    try sys.Print("{q} {r} {whole} {rest} {x} {y} {vx} {sx} {stop.x}\n", (q, r, whole, rest, x, y, vx, sx, stop))
}
```

```text
3 2 2 1 1 2 11 1 5
```

- A record pattern `(a, b)` names fields, not positions, so `(b, a) := e` binds the same values. A name that is not a field is E0309.
- A destructure names every field, or ends with `..` to skip the rest (E0305). Only the outermost pattern may end with `..`: a nested struct pattern names every field, with `_` for those you skip.
- A field skipped by `..` must be plain (E0408), because skipping it would drop it.
- Destructuring moves the value, or copies it if it is plain. It is how a method that takes `self` consumes a [`linear`](#linear-and-view-structs) struct.
- `var` before the pattern makes every binding `var`.
- A pattern that can fail, such as a literal, is E0306. Use `match`.
- Only a record, a struct or an array pattern destructures. A variant, `some`, `ok` or `err` pattern on the left of `:=` is E0306: take an enum or an optional apart with `match`, or an optional with `if v := o`.
- Outside the struct's module, a struct with private fields cannot be destructured (E0204).
- A `for` loop may destructure each element with a record pattern: `for (key, value) in m.Iter()`. A struct pattern there is E0101. See [`for`](#for-loops).

An array pattern `[a, b] := xs` takes a fixed array `[N]T` apart by position: the i-th name binds element i. It is how a function consumes an array of [linear](#linear-and-view-structs) values, whose elements cannot be moved out one at a time (`xs[0]` is E0409, see [R4](memory.md#r4-places-and-moves-out-of-them)).

```mk
// verdict: ok
linear struct Conn {
    id: int
}

Conn.Close(self): int {
    Conn(id) := self
    return id
}

Ends(): [2]Conn {
    return [Conn(id: 1), Conn(id: 2)]
}

export Main(sys: &System): WriteError!void {
    ends := Ends()
    [near, far] := ends // moves ends; each element into its own binding
    n := far.Close() * 10 + near.Close()
    [x, y, z] := [4, 5, 6] // plain elements are copied
    try sys.Print("{n} {x} {y} {z}\n", (n, x, y, z))
}
```

```text
21 4 5 6
```

- An array pattern names every element, one plain name each, so every element is bound exactly once: fewer or more names than `N` is E0305. It takes no `..`, `_` or nested pattern (E0101); to keep only some elements of a plain array, index it.
- `N` must be known where the destructure is checked: an array whose length is a const generic of the function, `[N]T`, is E0301. Its element type may be generic, as in `Split[T](xs: [2]T)`.
- Destructuring moves the whole array, or copies it if its elements are plain. `var [a, b] := xs` makes both bindings `var`.
- `(a, b) := xs` on an array is E0301: a record pattern names fields, and an array has none.

An array pattern that leaves out an element:

```mk
// verdict: reject E0305
linear struct Conn {
    id: int
}

Conn.Close(self) {
    Conn(id: _) := self
}

First(ends: [2]Conn) {
    [near] := ends // <- E0305
    near.Close()
}
```

A destructure that can fail:

```mk
// verdict: reject E0306
struct Vec2 {
    x: int
    y: int
}

OnAxis(v: Vec2): int {
    Vec2(x: 0, y) := v // <- E0306
    return y
}
```

A destructure that leaves out a field without `..`:

```mk
// verdict: reject E0305
struct Vec2 {
    x: int
    y: int
}

X(v: Vec2): int {
    Vec2(x) := v // <- E0305
    return x
}
```

A skipped field that is not plain:

```mk
// verdict: reject E0408
struct Bag {
    name: str
    items: List[int]
}

Name(b: Bag): str {
    Bag(name, ..) := b // <- E0408
    return name
}
```

### Compiler errors for patterns

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | `..` in a `match` pattern; alternatives that bind names; a record as a pattern; type arguments in a pattern; a struct pattern in a `for` head; anything but a name in an array pattern |
| E0204 | not-exported | A pattern or destructure that names private fields of another module's struct |
| E0206 | duplicate-name | A binding that shadows a local ([bindings](#bindings-and-wildcards)) |
| E0208 | unused | A binding that is never used, often a variant written without its dot ([variants](#variant-patterns)) |
| E0301 | type-mismatch | A pattern whose type is not the value's, such as `.Move(dx: "x")`; a const of a type a literal cannot have ([literals](#literal-patterns)); an array pattern on a value that is not an array of known length, or a record pattern on an array ([destructuring](#destructuring)) |
| E0305 | field-names | A destructure that neither names every field nor ends with `..`; an array pattern that does not name every element ([destructuring](#destructuring)) |
| E0306 | non-exhaustive | A `match` that misses values; a destructure that can fail ([exhaustiveness](#exhaustiveness)) |
| E0308 | no-operator | A float literal or a float const as a pattern ([literals](#literal-patterns)) |
| E0309 | record-names | A field name the type does not have ([variants](#variant-patterns)) |
| E0315 | empty-range | A range whose low end is not below its high end ([ranges](#range-patterns)) |
| E0408 | not-plain | A field skipped by `..` that is not plain ([destructuring](#destructuring)) |

## 7. Expressions

An expression computes a value: a literal, a name, an operator applied to operands, a call, a field, an index. Mk's operators are C's, with these differences:

- `<<`, `>>` and `&` bind like `*`, and `|` and `^` like `+`, so all of them bind tighter than comparisons.
- Integer `+ - *` panic on overflow, and `+% -% *%` wrap.
- Operands are never converted implicitly.
- `!`, `&&` and `||` take a `bool`, and `& | ^ ~` an integer.
- Comparisons do not chain.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a := 1 + 2 * 3 << 1 // * and << bind alike, left to right: 1 + ((2 * 3) << 1)
    flags: u8 = 0b0110
    set := flags & 0b0100 != 0 // & binds tighter than !=, unlike C
    in_range := 0 < a && a < 20 // comparisons do not chain
    try sys.Print("{a} {set} {in_range}\n", (a, set, in_range))
}
```

```text
13 true true
```

Operators bind in this order, tightest first. Operators of one row group left to right: `10 - 3 - 2` is `(10 - 3) - 2`.

| Precedence, high to low | Operators |
|---|---|
| Postfix | `a.b`, `a[i]`, `a[i..j]`, `f(x)` |
| Prefix | `- ! ~ & mut * try` |
| Multiply | `* / % *% << >> &` |
| Add | `+ - +% -% \| ^` |
| Compare, no chaining | `== != < <= > >=` |
| Logic | `&&`, then `\|\|` |
| Fallback | `e else { }`, `e else x { }` |

- `a < b < c` is E0101. Write `a < b && b < c`.
- `&&` binds tighter than `||`: `a || b && c` is `a || (b && c)`.
- Parentheses group: `(a + b) * c`. A lone name in parentheses, `(x)`, is a one-field record `(x: x)`, not a grouping. See [record literals](#array-record-and-struct-literals).
- `else` binds loosest, so `1 + F() else { 0 }` is `(1 + F()) else { 0 }`. `+` uses the result of `F()` before its error is handled (E0802), and the handler has no failure to replace (E0209). Write `1 + (F() else { 0 })`.
- `try` is a prefix operator and applies to the whole postfix chain after it: `try Load(x).Len()` tries `Len()`. Write `(try Load(x)).Len()`. See [`try`](#try).

```mk
// verdict: reject E0101
Between(a: int, b: int, c: int): bool {
    return a < b < c // <- E0101
}
```

```mk
// verdict: reject E0802 E0209
error Bad

Get(): Bad!int {
    fail Bad
}

Plus(): int {
    x := 1 + Get() else { 0 } // <- E0802 E0209
    return x
}
```

### Arithmetic

`+ - * / %` work on two operands of one integer or float type. Integer arithmetic is checked: a result that does not fit panics, in every build, release included.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    q := 7 / -2 // division truncates toward zero
    m := 7 % -2 // the remainder has the sign of the dividend
    m2 := -7 % 2
    try sys.Print("{q} {m} {m2}\n", (q, m, m2))
    big: u8 = 250
    w := big +% 10 // wraps: 260 - 256
    k: i8 = 127
    kw := k +% 1 // wraps to the minimum
    try sys.Print("{w} {kw}\n", (w, kw))
    f := 7.0 / 2.0
    fm := -7.5 % 2.0
    zero := 0.0
    inf := 1.0 / zero // floats do not panic
    nan := zero / zero
    same := nan == nan // NaN equals nothing, itself included
    try sys.Print("{f} {fm} {inf} {same}\n", (f, fm, inf, same))
}
```

```text
-3 1 -1
4 -128
3.5 -1.5 inf false
```

| Operator | On integers | On floats |
|---|---|---|
| `+ - *` | Panics on overflow | IEEE 754 |
| `/` | Truncates toward zero. Panics on a zero divisor, and on the minimum divided by -1 | IEEE 754 |
| `%` | Remainder with the sign of the dividend. Panics as `/` does | Remainder with the sign of the dividend |
| `+% -% *%` | Wraps around, never panics | None (E0308) |
| `-x` | Panics on the minimum. Signed types only (E0308) | IEEE 754 |

- Both operands have one type (E0301). Mk converts nothing implicitly: write `i64(a) + b`. See [conversions](#conversions).
- A literal takes the type of the other operand ([how a literal gets its type](#how-a-literal-gets-its-type)).
- Each float operation is rounded once. No build fuses or reassociates float operations, so a float result is the same in every build. A NaN that arithmetic produces has an unspecified sign and payload.
- For arithmetic that reports overflow (`AddOverflow`, `SubOverflow`, `MulOverflow`, `CheckedPow`) and for the 128-bit product `MulWide`, see [`std.math`](std/math.md#checked-and-wrapping-arithmetic).

The checks are panics, reported at the expression:

```mk
// verdict: ok
export Main(sys: &System): int {
    x: u8 = 255
    n := sys.args.Len() // 1: the program's path
    y := x + Cast[u8](n)
    return int(y)
}
```

```text
panic: overflow at main.mk:5:10
backtrace:
  Main (main.mk)
```

The process exits with status 101. [Panics](#panics) lists every panic message.

```mk
// verdict: reject E0301 E0308
Mix(a: i32, b: i64): i64 {
    return a + b // <- E0301
}

Flip(a: u32): u32 {
    return -a // <- E0308
}
```

### Bit operations

`& | ^` combine the bits of two integers of one type. `~` flips every bit. `<<` and `>>` shift.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    x: u8 = 0b1100
    y: u8 = 0b1010
    and := x & y
    or := x | y
    xor := x ^ y
    not := ~x
    try sys.Print("{and} {or} {xor} {not}\n", (and, or, xor, not))
    big: u8 = 250
    left := big << 4 // bits shifted out are dropped
    right := big >> 4
    neg: i8 = -128
    sr := neg >> 1 // >> on a signed type copies the sign bit
    try sys.Print("{left} {right} {sr}\n", (left, right, sr))
}
```

```text
8 14 6 243
160 15 -64
```

- `& | ^ ~` work on integers only. On `bool`, use `&&`, `||` and `!` (E0308).
- The shift amount may be any integer type. An amount below 0, or not below the bit width, panics with `shift amount`.
- `<<` never panics on overflow: it drops the bits shifted out, on signed types too.
- `>>` is arithmetic on signed types and logical on unsigned ones.
- `&` binds like `*`, and `|` and `^` like `+`. So `flags & mask == 0` compares `flags & mask` with 0.

### Comparison and text

`== !=` test equality and `< <= > >=` order. Each gives a `bool`.

| Operation | Works on |
|---|---|
| `==`, `!=` | Numbers, `bool`, `char`, enums without payloads, and `str` by content |
| `<`, `<=`, `>`, `>=` | Numbers, `char`, and `str` by bytes |
| `+` on `str` | Nothing: `str` has no `+`. Build text with `Text` or `arena.Fmt` |

```mk
// verdict: ok
enum Color {
    Red
    Green
}

struct Pair {
    a: int
    b: int
}

impl Eq for Pair {
    Eq(&self, other: &Pair): bool {
        return self.a == other.a && self.b == other.b
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    lt := "Apple" < "apple" // by bytes: 'A' is 0x41, 'a' is 0x61
    eq := "ab" == "ab" // by content
    d := Color.Red != Color.Green
    p := Pair(a: 1, b: 2).Eq(&Pair(a: 1, b: 2)) // other types: .Eq
    approx := 0.1 + 0.2 == 0.3
    try sys.Print("{lt} {eq} {d} {p} {approx}\n", (lt, eq, d, p, approx))
    name := "ada"
    n := 3
    s := try sys.arena.Fmt("{name}-{n}", (name, n)) // build a str
    try sys.Print("{s}\n", (s))
}
```

```text
true true true true false
ada-3
```

- Structs, records, arrays, slices, optionals, references, enums with payloads, `fn` values and raw pointers have no `==` (E0308). Compare raw pointers with `raw.Same`. A type with an `impl Eq` compares with `a.Eq(&b)`, and one with `impl Ord` orders with `a.Less(&b)`. See [interfaces in the library](#interfaces-in-the-library).
- Optionals have no `==`: test one with `if x := o` or `o.IsNone()` ([optionals](#optionals-t)).
- Errors have no `==` either: test for one with `match` ([section 9](#match-on-a-result)).
- Enums and `bool` have no order (E0308).
- A float `==` is IEEE equality: NaN is not equal to itself, and `0.0 == -0.0`.

```mk
// verdict: reject E0308
struct Pair {
    a: int
    b: int
}

Same(x: Pair, y: Pair): bool {
    return x == y // <- E0308
}
```

### Logical operators

`&&`, `||` and `!` work on `bool`. `&&` and `||` evaluate their right operand only when the left does not decide the result.

```mk
// verdict: ok
Loud(sys: &System, name: str): bool {
    sys.Print("{name} ", (name)) else {}
    return true
}

export Main(sys: &System): WriteError!void {
    a := false && Loud(sys, "and") // not evaluated
    b := true || Loud(sys, "or") // not evaluated
    c := true && Loud(sys, "evaluated")
    try sys.Print("{a} {b} {c}\n", (a, b, c))
}
```

```text
evaluated false true true
```

- The operands are `bool`. An integer operand is E0308, as in `n && ok`, and so is `!n`: write `n != 0`.
- A vector comparison gives a `Mask`, which is not a `bool` either (E0301). See [vector operators](#vector-operators).

### Borrowing and dereferencing

`&x` borrows `x` shared and gives a `&T`. `mut x` borrows it exclusively and gives a `mut T`. `*r` is the value behind a reference.

```mk
// verdict: ok
struct Counter {
    n: int
}

Counter.Bump(mut self) {
    self.n += 1
}

Counter.Get(&self): int {
    return self.n
}

Reset(c: mut Counter) {
    c.n = 0 // a field through a reference: no * needed
    c.Bump() // a method through a reference
}

export Main(sys: &System): WriteError!void {
    var c := Counter(n: 5)
    c.Bump() // borrows mut c for the call
    r := &c
    n := r.Get() // the same call as (*r).Get()
    Reset(mut c)
    m := c.Get()
    var x := 3
    p := mut x
    *p += 1 // * reads or writes the int behind p
    try sys.Print("{n} {m} {x}\n", (n, m, x))
}
```

```text
6 1 4
```

- `r.f` and `r.M()` reach through any number of references ([references](#references-t-and-mut-t)). A method call borrows or moves its receiver as its `self` mode says ([methods](#methods)).
- `xs[i].M()`, where `M` takes `mut self`, borrows `mut xs[i]`, as `xs[i].f = v` does.
- What a borrow allows, and for how long, is [the memory model](memory.md#r5-the-conflict-table)'s subject. `*p` on a raw pointer is [unsafe](#14-unsafe-and-c).

### Indexing and slices

`xs[i]` is the element at index `i`. `xs[a..b]` is a view of the elements from `a` up to, not including, `b`.

```mk
// verdict: ok
Zero(xs: mut []int) {
    for x in mut xs {
        *x = 0
    }
}

Sum(xs: []int): int {
    var n := 0
    for x in xs {
        n += x
    }
    return n
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var xs := [1, 2, 3, 4, 5]
    i: u8 = 1
    a := xs[i] // any integer type can index
    Zero(mut xs[3..]) // an exclusive view of the last two elements
    xs[0] = 9
    head := xs[..2] // a shared view of the first two
    n := head.Len()
    total := Sum(xs[..]) // the whole array as a slice
    try sys.Print("{a} {n} {total}\n", (a, n, total))

    var list := List[int].New(sys.arena)
    try list.Push(7)
    try list.Push(8)
    list[1] += 1 // a List indexes like a slice
    b := list[1]
    rest := Sum(list[1..])
    try sys.Print("{b} {rest}\n", (b, rest))

    s := "héllo"
    t := s[0..3] // byte offsets: "h" is one byte, "é" two
    k := s.Len()
    try sys.Print("{t} {k}\n", (t, k))
}
```

```text
2 2 14
9 9
hé 6
```

| Form | Rule |
|---|---|
| `xs[i]`, `xs[i] = e` | Work on `[]T`, `[N]T` and every implementer of `Slice[T]`, such as `List` and `HeapList`. The index may be any integer type, and the bounds check uses its value |
| `xs[a..b]`, `xs[a..]`, `xs[..b]` | Shared views. A missing bound is the start or the end |
| `mut xs[a..b]` | An exclusive view |
| `xs[..]` | The whole of an array or list as a slice. An array does not convert to a slice by itself (E0301) |
| `s[a..b]` on a `str` | A `str` of the bytes from `a` to `b`, which must fall on character boundaries |

- Every index and bound is checked, in every build. An index out of range, or a slice with `a > b` or `b` past the end, panics with `bounds`. A `str` slice off a character boundary panics with `str boundary`.
- A `str` is sliced, never indexed: `s[i]` is E0308. Read a byte with `s.Byte(i)`.
- Inside the brackets, `..` binds loosest: `xs[i + 1..]` needs no parentheses.
- A slice holds a loan on what it views. See [the memory model](memory.md#r7-values-hold-loans).
- Copying an element out by `xs[i]` needs a plain element type (E0409). Borrow others: `&xs[i]`, `mut xs[i]`.

### Array, record and struct literals

A literal builds a compound value in place.

```mk
// verdict: ok
struct Span {
    from: int
    to: int
}

export Main(sys: &System): WriteError!void {
    primes := [2, 3, 5, 7] // an array: [4]int
    small: [3]u8 = [1, 2, 3] // the expected type sets the element type
    zeros := [0; 8] // eight zeros
    span := Span(from: 2, to: 5) // a struct: every field, by name
    from := 9
    r := (from, len: 4) // a record: (from: from, len: 4)
    n := primes.Len() + small.Len() + zeros.Len()
    w := span.to - span.from
    try sys.Print("{n} {w} {r.from} {r.len}\n", (n, w, r))
}
```

```text
15 3 9 4
```

| Literal | Builds |
|---|---|
| `[a, b, c]` | An array `[3]T`. Without an expected type, `T` is the first element's type, and every element has it (E0301) |
| `[e; n]` | An array of `n` copies of `e`. `n` is a compile-time constant (E0705) and `e` is plain (E0408) |
| `[]` | An empty array. It needs an expected type (E0711) |
| `Point(x: 1, y: 2)` | A struct. Every field is named, none has a default (E0305). See [structs](#structs) |
| `Shape.Circle(r: 1.0)`, `Color.Red` | An enum value |
| `(a: 1, b: x)` | A record. Fields are named |
| `(a, b: e)` | A bare name `a` is short for `a: a`. So `(x)` is the record `(x: x)` |

```mk
// verdict: reject E0705 E0301
Buffer(n: int): int {
    xs := [0; n] // <- E0705
    return xs.Len()
}

Mixed(a: i32): int {
    ys := [1, a] // <- E0301
    return ys.Len()
}
```

`[1, a]` is an `[2]int` from its first element, so `a` does not fit. Write `[a, 1]`, or give the type: `ys: [2]i32 = [1, a]`.

### Calls and order of evaluation

`f(a, b)` calls the function `f` with arguments by position. `x.M(a)` calls the method `M` with `x` as its receiver. `r.f` reads the field `f`, through references if `r` is one. Arguments and literal fields are evaluated left to right.

```mk
// verdict: ok
Note(sys: &System, label: str, v: int): int {
    sys.Print("{label} ", (label)) else {}
    return v
}

Pair(a: int, b: int): int {
    return a * 10 + b
}

export Main(sys: &System): WriteError!void {
    v := Pair(Note(sys, "first", 1), Note(sys, "second", 2))
    try sys.Print("= {v}\n", (v))
}
```

```text
first second = 12
```

- Arguments are positional only (E0305), and their count must match (E0304). See [functions](#functions-and-methods) for what a signature allows.
- Each argument has its parameter's type (E0301).
- Type arguments are inferred from the arguments, then from the expected type. Otherwise you write them: `Parse[i32](s)` (E0711). See [generic functions](#generic-functions).
- A `const` parameter takes a value known at compile time (E0705). See [const parameters](#const-parameters).
- A call that can fail gives an `E!T`, which you handle at once with `try`, `else` or `match` ([section 9](#9-errors)).
- Evaluation runs left to right, for arguments and constructor fields alike. The exception: `&p` and `mut p` operands, receivers included, are formed after the others, so `list.Push(list.Len())` works. See [order of evaluation](memory.md#r13-order-of-evaluation).
- A field of another module's struct is visible only if it is exported (E0204). See [section 13](#13-modules-and-build).
- A temporary lives until the end of its statement. See [temporaries](memory.md#r14-temporaries).

### Function values

A declared function's name, written without a call, is a `fn` value.

```mk
// verdict: ok
Add(a: i32, b: i32): i32 {
    return a + b
}

Apply(f: fn(i32, i32): i32): i32 {
    return f(1, 2)
}

Three(): i32 {
    return Apply(Add)
}
```

- Its type is a [`fn` type](#function-types); a generic function names its instance, `Same[int]` ([generic functions as values](#generic-functions-as-values)).
- A method named without a call, `c.Get`, would capture `c`, and is E0609. Pass `T.Get` and the receiver instead.

```mk
// verdict: reject E0609
struct Gauge {
    n: int
}

Gauge.Get(&self): int {
    return self.n
}

Read(g: Gauge): int {
    f := g.Get // <- E0609
    return f(&g)
}
```

Write `f := Gauge.Get`: a `fn(&Gauge): int`.

### Function literals

`fn(a: i32): bool { return a > 0 }` is a function literal. Mk has no closures: a literal that uses no local of the enclosing function is a `fn` value, and one that does is not a value at all.

```mk
// verdict: ok
Apply(f: fn(int): int, x: int): int {
    return f(x) // a call through a fn value
}

Double(n: int): int {
    return n * 2
}

// ByDistance sorts xs by distance from origin. The literal reads origin.
ByDistance(xs: mut []int, origin: int) {
    xs.Sort(fn(a: &int, b: &int): bool {
        da := *a - origin
        db := *b - origin
        return da * da < db * db
    })
}

export Main(sys: &System): WriteError!void {
    a := Apply(Double, 5) // a declared function as a value
    b := Apply(fn(n: int): int { return n + 1 }, 5) // a literal that captures nothing
    var f: fn(int): int = Double
    f = fn(n: int): int { return -n }
    c := f(3)
    try sys.Print("{a} {b} {c}\n", (a, b, c))
    var xs := [1, 9, 4, 6, 5]
    ByDistance(mut xs[..], 5)
    for x in xs {
        try sys.Print("{x} ", (x))
    }
    try sys.Print("\n")
}
```

```text
10 6 -3
5 4 6 1 9 
```

A literal that uses a local of the enclosing function is allowed in one place only: as the argument of a `const` parameter of `fn` type, such as `Sort`'s `less`. Anywhere else it is E0609.

```mk
// verdict: reject E0609
Count(xs: []i32, keep: fn(&i32): bool): int {
    var n := 0
    for x in xs {
        if keep(&x) {
            n += 1
        }
    }
    return n
}

Above(xs: []i32, limit: i32): int {
    return Count(xs, fn(x: &i32): bool { return *x > limit }) // <- E0609
}
```

`keep` is not `const`, so the literal must be a value, and a value cannot capture `limit`.

- A literal passed to a `const` parameter of `fn` type has the `from` and `into` clauses of the parameter's `fn` type, and writes none of its own. A store beyond them is E0604.
- What a literal may do with what it captures — reading via `&`, mutating `var` locals or reborrowing `mut` references and slices via `mut` (E0508 if not `var`), and the borrows and loan rules that enforce statement scope — is in [captures](memory.md#r13-order-of-evaluation).

```mk
// verdict: ok
Sorted(xs: mut []i32): int {
    var compares := 0
    xs.Sort(fn(a: &i32, b: &i32): bool {
        compares += 1
        return *a < *b
    })
    return compares
}
```

```mk
// verdict: reject E0609
// a capturing literal cannot escape as a value, and cannot move out of a capture
Over(limit: i32): fn(&i32): bool {
    return fn(x: &i32): bool { return *x > limit } // <- E0609
}

MoveCapture(xs: mut []i32, ys: mut []i32) {
    xs.Sort(fn(a: &i32, b: &i32): bool {
        m := ys // <- E0609
        return m.Len() > 0 && *a < *b
    })
}
```

To store or return a callback, store a `fn` value beside its plain state. See [keep a callback for later](cookbook.md#keep-a-callback-for-later).

### Other expressions

These forms are expressions too, each with its own section:

| Form | Meaning |
|---|---|
| `T(x)`, `Cast[T](x)`, `Trunc[T](x)` | Conversions ([conversions](#conversions)) |
| `try e`, `e else { }`, `e.Must()` | Handle a failure ([section 9](#9-errors)) |
| `if c { a } else { b }`, `match e { }` | A value wherever an expression stands, an operand too ([values](#if-match-and-else-as-values)) |
| `SizeOf[T]()`, `AlignOf[T]()` | Layout facts at compile time ([section 11](#11-compile-time)) |
| `quote { ... }` | Code as data, in a generator ([generators](#generators)) |

### Vector operators

`simd.Vec` ([vectors](#vectors)) has its own operator rules:

- Both operands are the same `simd.Vec[T, N]` type. A scalar beside a vector is E0301; use `Splat`.
- Float lanes take `+ - * /` and unary `-`. They have no `%`.
- Integer lanes take `+% -% *%`, `& | ^ ~`, and `<< >>` by a scalar amount. The amount follows the scalar rule: out of range, it panics and is never masked. `>>` is arithmetic on signed lanes.
- On integer lanes, `+ - *`, unary `-`, `/` and `%` are E0308. The message names `+%`, `AddSat` or `DivTrunc`.
- Comparisons give a `Mask[T, N]`, which takes `& | ^ !`.
- A `Mask` is not a `bool`. As a condition or an operand of `&&`, it is E0301; use `.All()` or `.Any()`.
- Vectors and masks implement no `Eq`, `Ord`, `Hash` or `Format`.

```mk
// verdict: ok
import std.simd

// CountByte counts b in xs: 16 bytes at a time, then the tail one by one.
CountByte(xs: []u8, b: u8): int {
    var n := 0
    var i := 0
    needle := simd.Vec[u8, 16].Splat(b)
    for i + 16 <= xs.Len() {
        v := simd.Vec[u8, 16].FromSlice(xs, i)
        n += (v == needle).CountSet() // == gives a Mask, one lane per byte
        i += 16
    }
    for i < xs.Len() {
        if xs[i] == b {
            n += 1
        }
        i += 1
    }
    return n
}

export Main(sys: &System): WriteError!void {
    text := "a line, another line, and a last line of text"
    n := CountByte(text.AsBytes(), b' ')
    try sys.Print("{n} spaces\n", (n))
}
```

```text
9 spaces
```

```mk
// verdict: reject E0308
// integer vector lanes have no trapping +
import std.simd

Twice(v: simd.Vec[i32, 4]): simd.Vec[i32, 4] {
    return v + v // <- E0308
}
```

```mk
// verdict: reject E0301
// a Mask is not a bool
import std.simd

Same(v: simd.Vec[i32, 4], w: simd.Vec[i32, 4]): bool {
    if v == w { // <- E0301
        return true
    }
    return false
}
```

Write `(v == w).All()`. See [`std.simd`](std/simd.md) for the operations on vectors.

### Compiler errors for expressions

| Code | Name | Section |
|---|---|---|
| E0101 | syntax | [precedence](#7-expressions) |
| E0204 | not-exported | [calls](#calls-and-order-of-evaluation) |
| E0209 | unused-result | [precedence](#7-expressions) |
| E0301 | type-mismatch | [arithmetic](#arithmetic), [logic](#logical-operators), [indexing](#indexing-and-slices), [literals](#array-record-and-struct-literals), [calls](#calls-and-order-of-evaluation), [vectors](#vector-operators) |
| E0304 | argument-count | [calls](#calls-and-order-of-evaluation) |
| E0305 | field-names | [literals](#array-record-and-struct-literals), [calls](#calls-and-order-of-evaluation) |
| E0308 | no-operator | [arithmetic](#arithmetic), [bit operations](#bit-operations), [comparison](#comparison-and-text), [logic](#logical-operators), [indexing](#indexing-and-slices), [vectors](#vector-operators) |
| E0408 | not-plain | [literals](#array-record-and-struct-literals) |
| E0409 | move-out-of-reference | [indexing](#indexing-and-slices) |
| E0604 | store-undeclared | [function literals](#function-literals) |
| E0609 | literal-captures | [function values](#function-values), [function literals](#function-literals) |
| E0705 | not-const | [literals](#array-record-and-struct-literals), [calls](#calls-and-order-of-evaluation) |
| E0711 | cannot-infer | [literals](#array-record-and-struct-literals), [calls](#calls-and-order-of-evaluation) |
| E0802 | error-unhandled | [precedence](#7-expressions) |

## 8. Ownership and borrowing

Every value in Mk has one owner, and the compiler checks at compile time that no reference outlives what it points to and that no two references conflict. There is no garbage collector and there are no lifetime annotations. [The memory model](memory.md) states every rule, with an accepted program, a rejected one and the fix for each.

```mk
// verdict: ok
linear struct Ticket {
    id: int
}

Ticket.Redeem(self): int { // self by value: the call consumes the ticket
    Ticket(id) := self
    return id
}

struct Names {
    all: List[str]
}

// into: Add stores name inside self, so name must live as long as self
Names.Add(mut self, name: str into self): OutOfMemory!void {
    try self.all.Push(name)
}

// from: the result comes from a or from b
Longer(a: str, b: str): str from a, b {
    return if a.Len() >= b.Len() { a } else { b }
}

struct Pos {
    line: int
    col: int
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    p := Pos(line: 1, col: 2)
    q := p // Pos is plain: a copy, and p stays usable

    t := Ticket(id: 7)
    u := t // Ticket is linear: t moves into u and cannot be used again
    n := u.Redeem() // u must be consumed: dropping it is a compile error

    var names := Names(all: List[str].New(sys.arena))
    try names.Add("ada") // borrows names exclusively, for this call only
    try names.Add("grace")
    all := &names.all // a shared borrow of one field
    w := Longer(all[0], all[1])
    try sys.Print("{l} {c} {n} {w}\n", (l: p.line, c: q.col, n, w))
}
```

```text
1 2 7 grace
```

| Form | Meaning | Rules |
|---|---|---|
| `b := a`, `a` plain | Copies `a`. Numbers, `bool`, `str`, `&T`, `[]T` and structs of plain fields are plain | [R1](memory.md#r1-value-kinds) |
| `b := a`, `a` unique or linear | Moves `a` into `b`. Using `a` afterwards is E0401 | [R1](memory.md#r1-value-kinds), [R4](memory.md#r4-places-and-moves-out-of-them) |
| `&p` | A shared borrow: any number at once, and no writes except through a `Cell`, `Mutex`, `Chan`, `Arena`, or `mut view` | [R5](memory.md#r5-the-conflict-table) |
| `mut p` | An exclusive borrow: reads and writes, nothing else touches `p` meanwhile. A local `p` must be `var` (E0508) | [R5](memory.md#r5-the-conflict-table) |
| `self`, `&self`, `mut self` | A method takes its receiver by value, by shared borrow or by exclusive borrow | [Functions and methods](#functions-and-methods) |
| `for x in mut xs` | Borrows each element exclusively in turn | [R20](memory.md#r20-disjoint-elements) |
| `from`, `into`, `ends`, `plain view struct`, `view struct`, `mut view struct`, `linear struct` | What signatures and type declarations promise about loans and consumption. A receiver takes `into` like a parameter: `Names.Keys(&self into arena, arena: &Arena)` | [Signatures at a glance](memory.md#5-signatures-at-a-glance) |
| `defer s`, `onerr s` | Run `s` at every exit, or at error exits only | [R21](memory.md#r21-onerr-and-exits), [`onerr`](#cleanup-on-the-error-path-onerr) |

- A borrow lasts until the last use of anything that holds it, not until the end of the block.
- Most signatures need no `from`: the types decide where a result comes from. A function that stores one parameter inside another says so with `into`. When a clause is missing, the compiler asks for it (E0601, E0604) and its message proposes one.
- A caller sees only signatures, never a body. Each function is checked against its own signature.
- Where a `[]T` is expected, pass a `mut []T` local `xs` as its shared view `xs[..]`. Passing `xs` itself is E0301.
- `from p.f` on a `view` field, and clauses that name more sources than the body uses: [R10](memory.md#r10-where-a-result-comes-from). When locals end: [R14](memory.md#r14-temporaries). Containers made in an arena: [R11](memory.md#r11-into-and-q-into-p).

See [reading a borrow error](memory.md#reading-a-borrow-error) for what the compiler prints and the four ways to fix a conflict.

### Fallible calls consume only on success

A function that can fail takes ownership of a by-value argument that is not plain only when it succeeds. When `try` or `else` sees an error, the caller still owns the argument and must consume it, usually in an `onerr`. So the callee may move or change such a parameter only once nothing after it can fail; otherwise it is E0411 at the change:

```mk
// verdict: reject E0411
error Full

Register(a: &Alloc, list: mut HeapList[Res], r: Res, ok: bool): Full|OutOfMemory!void {
    try list.Push(a, r) // <- E0411
    if !ok {
        fail Full
    }
}
```

Check first and move last, or split the function in two. The full rule, with the accepted form, is in [R2](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path).

See also: [the memory model](memory.md), [kinds](#kinds), [the cookbook](cookbook.md#return-heap-owned-data-that-outlives-an-arena) for patterns, and [the hard cases](memory.md#6-hard-cases) for programs other memory models get wrong.

## 9. Errors

An error is a value that a function returns when it cannot do its job. A function that can fail says so in its return type: `E!T` holds either a `T` or an error from the set `E`. The caller must handle the error or pass it on, and the compiler checks that it does.

```mk
// verdict: ok
error NotFound(id: i64)
error Denied

type LoadError = NotFound | Denied

Load(id: i64): LoadError!str {
    if id < 0 {
        fail Denied
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return "data"
}

Show(id: i64): LoadError!int {
    s := try Load(id) // on error, return it to the caller
    return s.Len()
}

Quiet(sys: &System, id: i64): int {
    s := Load(id) else e { // handle the error here
        sys.PrintErr("load {id}: {e}\n", (id, e)) else {}
        ""
    }
    return s.Len()
}
```

| Form | Meaning |
|---|---|
| `error Name`, `error Name(f: T)` | Declare an error, with or without a payload |
| `A \| B` | The set holding the errors `A` and `B` |
| `E!T` | A `T`, or an error from the set `E` |
| `fail e` | Return the error `e` |
| `try e` | The value of `e`, or return its error to the caller |
| `e else { ... }` | The value of `e`, or run the block |
| `e else x { ... }` | The same, with the error bound to `x` |
| `match e { ok(v) { ... } err(x) { ... } }` | Handle both cases, or each error separately |
| `e.Must()` | The value of `e`, or a panic |
| `onerr s` | Run `s` when the block exits with an error |

Errors are for failures a program should expect: a missing file, a full disk, bad input, a cancelled operation. A bug, such as an index out of bounds, is a [panic](#panics) instead.

### Declaring errors

`error` declares one error. It may carry a payload: named fields, written like a struct's.

```mk
// verdict: ok
error Timeout
error BadByte(offset: int, value: u8)

struct Pos {
    line: int
    col: int
}

error Unexpected(at: Pos, found: char)

Check(data: []u8): BadByte!void {
    for i in 0..data.Len() {
        if data[i] > 127 {
            fail BadByte(offset: i, value: data[i]) // name every field
        }
    }
}

Where(e: Unexpected): int {
    return e.at.line // payload fields read like struct fields
}

export Main(sys: &System): WriteError!void {
    data: [3]u8 = [104, 105, 200]
    Check(data[..]) else e {
        at := e.offset
        try sys.Print("{e} at offset {at}\n", (e, at))
    }
    u := Unexpected(at: Pos(line: 4, col: 2), found: '}')
    line := Where(u)
    try sys.Print("line {line}\n", (line))
}
```

```text
BadByte(offset: 2, value: 200) at offset 2
line 4
```

- A payload is plain data: integers, floats, `bool`, `char`, and arrays, records, structs and enums of plain data. A field may also be a `str`.
- A payload holds no other reference, so no slice, `&T`, `?str`, `[N]str` or struct holding a `str` (E0408).
- A `str` field holds only a static string: a literal, a `const`, a global, or a `str` a function returns that comes from none of its parameters. Building the error with any other `str`, such as a parameter or a view of a local buffer, is E0605, and so are assigning such a `str` to the field and a `mut` borrow of the field. An error never borrows from the function that failed, so it can always travel up the stack and into another task. To carry text made at run time, use a fixed array and a length, `error BadName(name: [16]u8, len: int)`, or a position or an id from which the caller formats the message.
- Since an error's `str` is static, reading it gives a `str` that borrows nothing, even through a reference: for `e: &Bad`, `e.msg` outlives `e`. A `match` on a `mut` of the error would bind the field as a `mut str`, which is E0605: bind the error, as `err(e)`, and read `e.msg`.
- Construction names every field, `BadByte(offset: 3, value: 200)`, as for a struct (E0305).
- An error has no type parameters.

```mk
// verdict: reject E0408
error NoSuchKey(key: []u8) // <- E0408
```

A `str` field holds text that lives as long as the program:

```mk
// verdict: ok
error Unsupported(feature: str, line: int)

const Generators: str = "generators"

Feature(code: u8): str {
    return match code {
        1 { "classes" }
        _ { "modules" }
    }
}

Compile(code: u8, line: int): Unsupported!void {
    if code == 0 {
        fail Unsupported(feature: Generators, line: line)
    }
    fail Unsupported(feature: Feature(code), line: line) // Feature's result borrows nothing
}

export Main(sys: &System): WriteError!void {
    Compile(1, 7) else e {
        try sys.Print("{e}\n", (e))
    }
}
```

```text
Unsupported(feature: classes, line: 7)
```

A `str` that borrows from the failing function, or from its caller, could outlive its bytes, so `fail NoSuchKey(key: key)` with a parameter `key` is E0605 ([the memory model](memory.md#r16-globals) has the example). Carry what the caller cannot recompute, such as an index, and let the caller format the key it passed in.

A payload is built by naming its fields:

```mk
// verdict: reject E0305
error TooLong(len: int)

Check(s: str): TooLong!void {
    if s.Len() > 8 {
        fail TooLong(s.Len()) // <- E0305
    }
}
```

### Error sets

`|` joins errors into a set. A single error is a set of one. A type alias names a set, so a signature stays short.

```mk
// verdict: ok
error Empty
error TooLong(len: int)
error BadChar(at: int)

type FormatError = Empty | TooLong
type NameError = FormatError | BadChar // sets nest: this is Empty | TooLong | BadChar

CheckLen(s: str): FormatError!void {
    if s.Len() == 0 {
        fail Empty
    }
    if s.Len() > 8 {
        fail TooLong(len: s.Len())
    }
}

CheckName(s: str): NameError!void {
    try CheckLen(s) // a FormatError fits in a NameError
    var at := 0
    for c in s.Scalars() {
        if c == ' ' {
            fail BadChar(at: at)
        }
        at += 1
    }
}

export Main(sys: &System): WriteError!void {
    for name in ["ada", "", "grace hopper", "al an"] {
        CheckName(name) else e {
            try sys.Print("{name}: {e}\n", (name, e))
            continue
        }
        try sys.Print("{name}: ok\n", (name))
    }
}
```

```text
ada: ok
: Empty
grace hopper: TooLong(len: 12)
al an: BadChar(at: 2)
```

- Sets are structural. `A | B`, `B | A` and `A | B | A` are the same type, and an alias is another name for its set.
- A value of a smaller set converts to any set that holds all its members, with no cast.
- An error set is a type like any other: a parameter, a local or a field can hold one, as in `Kind(e: NameError): str`.

**Open sets.** A library marks a set `open` when a later release may add members to it, as `std.fs` does with `export open type Error = NotFound | Exists | ...`. Outside the package that declares it, a `match` on an open set needs a `_` arm (E0306):

```mk
// verdict: reject E0306
import std.fs

Describe(e: fs.Error): str {
    return match e { // <- E0306
        fs.NotFound { "not found" }
        fs.Denied { "denied" }
    }
}
```

Add `_ { "other" }` and it compiles.

See also: [open sets](codebase.md#open-sets) for the rules of writing `open`, [types](#3-types-and-kinds) for type aliases.

### Results: `E!T`

`E!T` is the result of a function that can fail. `E!void` fails or returns nothing.

```mk
// verdict: ok
error Negative

Root(n: int): Negative!int {
    if n < 0 {
        fail Negative
    }
    var r := 0
    for (r + 1) * (r + 1) <= n {
        r += 1
    }
    return r // a plain value: the success case
}

Check(n: int): Negative!void {
    _ = try Root(n) // E!void: fails or returns nothing
}

export Main(sys: &System): WriteError!void {
    for n in [17, -4] {
        r := Root(n) else e {
            try sys.Print("{n}: {e}\n", (n, e))
            continue
        }
        try sys.Print("{n}: {r}\n", (n, r))
    }
    Check(9) else {
        try sys.Print("unreachable\n")
    }
}
```

```text
17: 4
-4: Negative
```

- A signature always names its error set. Mk never infers one, so a function's errors are visible at its declaration.
- An `E!T` value must be handled where it is made: by `try`, `else` or `match` (E0802). It cannot be passed, returned by a function that can fail, or stored in a field or element as it is; only a binding that writes its type, `var r: Negative!int = Root(n)`, keeps one to handle later. `return Root(n)` in a function of the same result type is also E0802: write `return try Root(n)`.
- Inside the function, `return x` returns the success value and `fail e` the error.

```mk
// verdict: reject E0802
error Negative

Root(n: int): Negative!int {
    if n < 0 {
        fail Negative
    }
    return n
}

Twice(n: int): Negative!int {
    return Root(n) // <- E0802
}
```

A function can fail only when its signature names an error set, as a result written `E!T` does. A generic whose result is its type parameter, called with an `E!T` for it, cannot fail, and neither can a function whose result is an alias of an `E!T`: each returns the `E!T` as a value, which the caller handles with `try`, `else` or `match` as any other. So does an interface method whose result is such a type parameter or alias, and an `impl` method must fail exactly when its interface method can (E0707).

```mk
// verdict: ok
error Negative

Root(n: int): Negative!int {
    if n < 0 {
        fail Negative
    }
    return n * 2
}

Copy[T: Plain](x: &T): T {
    return *x
}

export Main(sys: &System): WriteError!void {
    var a: Negative!int = Root(4)
    var b: Negative!int = Root(-4)
    for r in [&a, &b] {
        v := Copy(r) else e { // Copy cannot fail: else handles the E!T it returns
            try sys.Print("{e}\n", (e: e))
            continue
        }
        try sys.Print("{v}\n", (v: v))
    }
}
```

```text
8
Negative
```

### `fail`

`fail e` returns the error `e` from the function. `e` is an error value: a constructed error, or a value of an error set, such as one bound by `else`.

```mk
// verdict: ok
error Busy
error Gone

type PortError = Busy | Gone

Open(port: int): PortError!int {
    if port == 0 {
        fail Gone
    }
    if port < 1024 {
        fail Busy
    }
    return port
}

Retry(port: int): PortError!int {
    a := Open(port) else e {
        match e {
            Busy {
                return try Open(port + 1024) // try another port
            }
            _ {
                fail e // pass every other error on unchanged
            }
        }
    }
    return a
}

export Main(sys: &System): WriteError!void {
    for port in [8080, 80, 0] {
        p := Retry(port) else e {
            try sys.Print("{port}: {e}\n", (port, e))
            continue
        }
        try sys.Print("{port}: opened {p}\n", (port, p))
    }
}
```

```text
8080: opened 8080
80: opened 1104
0: Gone
```

- The error must belong to the function's set (E0801).
- `fail` ends the function, as `return` does. The `defer` and `onerr` blocks of the blocks it leaves run first.

```mk
// verdict: reject E0801
error Busy
error Gone

Open(port: int): Busy!int {
    if port == 0 {
        fail Gone // <- E0801
    }
    return port
}
```

### `try`

`try e` evaluates `e`, a value of type `E!T`. On success it is the `T`. On error it returns that error from the enclosing function.

```mk
// verdict: ok
error Short

Byte(data: []u8, at: int): Short!u8 {
    if at >= data.Len() {
        fail Short
    }
    return data[at]
}

Word(data: []u8, at: int): Short!int {
    hi := try Byte(data, at)
    lo := try Byte(data, at + 1)
    return int(hi) * 256 + int(lo)
}

Label(data: []u8): Short!str {
    if data.Len() < 4 {
        fail Short
    }
    return "word"
}

LabelLen(data: []u8): Short!int {
    return (try Label(data)).Len() // parentheses: try the call, then use the value
}

export Main(sys: &System): Short|WriteError!void {
    data: [3]u8 = [1, 2, 3]
    w := try Word(data[..], 0)
    try sys.Print("{w}\n", (w))
    n := try LabelLen(data[..])
    try sys.Print("{n}\n", (n))
}
```

```text
258
error: main.Short
```

`Word` reads two bytes, and `LabelLen` fails with `Short`, which `Main` passes on with `try`. An error that leaves `Main` ends the program with status 1 ([below](#errors-from-main)).

- The error set of `e` must fit in the function's set (E0801). Widen the function's set, or handle the errors it lacks with `else` or `match`.
- `try` applies to the outermost call of the expression after it. In `try Label(data).Len()` that is `Len()`, and `Label`'s result stays unhandled (E0802). Write `(try Label(data)).Len()`.
- `try` needs a value that can fail. On a `?T` it is E0804: an optional has no error to pass on, so use `else` or `if x := e`.
- `try` is allowed in a `test` block. An error that leaves the test fails it.
- A `defer` or `onerr` body cannot `try` (E0806).

```mk
// verdict: reject E0801
error Short
error Bad

Byte(data: []u8): Short|Bad!u8 {
    if data.Len() == 0 {
        fail Short
    }
    return data[0]
}

First(data: []u8): Short!u8 {
    return try Byte(data) // <- E0801
}
```

An optional has no error for `try` to pass on:

```mk
// verdict: reject E0804
Find(xs: []int, v: int): ?int {
    for i in 0..xs.Len() {
        if xs[i] == v {
            return some(i)
        }
    }
    return none
}

At(xs: []int): int {
    return try Find(xs, 3) // <- E0804
}
```

### `else`

`e else { ... }` handles a failure where it happens. When `e` succeeds, the result is its value. When it fails, the block runs. `else` works on `E!T` and on an optional `?T`, where `none` is the failure.

```mk
// verdict: ok
error Missing(key: int)

Lookup(key: int): Missing!int {
    if key > 3 {
        fail Missing(key: key)
    }
    return key * 100
}

Save(key: int): Missing!void {
    _ = try Lookup(key)
}

export Main(sys: &System): WriteError!void {
    // as a value: the block's last line is the value
    a := Lookup(7) else { 0 }

    // with a binding: e holds the error
    b := Lookup(9) else e {
        try sys.Print("using a default for {e}\n", (e))
        -1
    }

    // as a statement: on error, run the block and carry on
    Save(5) else e {
        try sys.Print("not saved: {e}\n", (e))
    }

    // ignore the error of an E!void on purpose
    Save(6) else {}

    // leave a loop from the handler
    var sum := 0
    for key in 1..10 {
        v := Lookup(key) else {
            break
        }
        sum += v
    }
    try sys.Print("{a} {b} {sum}\n", (a, b, sum))
}
```

```text
using a default for Missing(key: 9)
not saved: Missing(key: 5)
0 -1 600
```

- `e else x { ... }` binds the error to `x` inside the block. On a `?T` there is no error to bind (E0307).
- Where a value is needed, the block's last line is that value. A block that ends in `return`, `fail`, `break`, `continue` or a call returning `never`, such as `Panic`, needs no value. Any other last line is E0803.
- As a statement, `else` is allowed on `E!void`. On an `E!T` with a value, the value must be used: `F() else {}` drops it (E0209). Write `_ = F() else { 0 }` to discard it.
- `F() else {}` with an empty block ignores the error. Write it only where the failure does not matter, and say why in a comment.
- The block may `try`, `fail` and `return`. A `break` or `continue` out of the handler leaves its loop as usual and counts as an error exit for [`onerr`](#cleanup-on-the-error-path-onerr).

```mk
// verdict: reject E0803
error Missing

Lookup(key: int): Missing!int {
    fail Missing
}

Total(keys: []int): int {
    var misses := 0
    var sum := 0
    for k in keys {
        v := Lookup(k) else { // <- E0803
            misses += 1
        }
        sum += v
    }
    return sum - misses
}
```

A handler that drops a value it should supply:

```mk
// verdict: reject E0209
error Missing

Lookup(key: int): Missing!int {
    fail Missing
}

Warm() {
    Lookup(1) else {} // <- E0209
}
```

A binding on an optional's `else`. The unused binding is also reported, as E0208:

```mk
// verdict: reject E0208 E0307
Find(v: int): ?int {
    return none
}

Get(): int {
    a := Find(3) else e { 0 } // <- E0208 E0307
    return a
}
```

### `match` on a result

`match` handles success and failure in one place. The `ok(v)` arm binds the value; an `err(x)` arm binds the error. An `err` arm may also name one member of the set, with a pattern for its payload.

```mk
// verdict: ok
error NotFound(id: int)
error Denied

type LoadError = NotFound | Denied

Load(id: int): LoadError!str {
    if id < 0 {
        fail Denied
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return "data"
}

Describe(id: int): str {
    match Load(id) {
        ok(s) {
            return s
        }
        err(NotFound(id: n)) {
            return if n == 0 { "zero" } else { "missing" }
        }
        err(Denied) {
            return "denied"
        }
    }
}

Kind(e: LoadError): str {
    return match e { // a match on an error-set value
        NotFound { "not found" }
        Denied { "denied" }
    }
}

export Main(sys: &System): WriteError!void {
    for id in [-1, 0, 3] {
        d := Describe(id)
        try sys.Print("{id}: {d}\n", (id, d))
    }
    e := Load(-1) else x {
        k := Kind(x)
        try sys.Print("{x} is {k}\n", (x, k))
        "?"
    }
    try sys.Print("{e}\n", (e))
}
```

```text
-1: denied
0: zero
3: data
Denied is denied
?
```

- A `match` on a result or on an error set must cover every case (E0306). An `err(x)` arm covers every error.
- Errors have no `==`: `e == Denied` is E0301. Test for a member with `match`.

```mk
// verdict: reject E0301
error Busy
error Gone

IsBusy(e: Busy | Gone): bool {
    return e == Busy // <- E0301
}
```

### `Must`

`e.Must()` is the value of `e`, or a panic when `e` holds an error or `none`. It works on `E!T` and on `?T`. Use it where a failure is a bug: a value the program itself built and knows to be valid.

```mk
// verdict: ok
error Denied

Load(ok: bool): Denied!int {
    if !ok {
        fail Denied
    }
    return 7
}

export Main(sys: &System): WriteError!void {
    a := Load(true).Must()
    try sys.Print("{a}\n", (a))
    b := Load(sys.args.Len() > 5).Must()
    try sys.Print("{b}\n", (b))
}
```

```text
7
panic: must: main.Denied at main.mk:14:10
backtrace:
  Main (main.mk)
```

On `none` the first line reads `panic: must at main.mk:...`. The process exits with status 101, as for every [panic](#panics).

### Cleanup on the error path: `onerr`

`onerr s` runs `s` when its block exits with an error. `defer s` runs `s` at every exit. They are one stack: at an exit, the pending blocks run in reverse order of declaration.

```mk
// verdict: ok
error Bad

Step(sys: &System, fails: bool): Bad!void {
    defer sys.Print("defer 1\n") else {}
    onerr sys.Print("onerr 2\n") else {}
    defer sys.Print("defer 3\n") else {}
    if fails {
        fail Bad
    }
    sys.Print("body done\n") else {}
}

export Main(sys: &System): WriteError!void {
    Step(sys, false) else {}
    try sys.Print("--\n")
    Step(sys, true) else e {
        try sys.Print("caller saw {e}\n", (e))
    }
}
```

```text
body done
defer 3
defer 1
--
defer 3
onerr 2
defer 1
caller saw Bad
```

An error exit is a `fail`, a `try` that passes an error on, or a `break` or `continue` that leaves the block from inside an `else` handler. A `return` is never an error exit, even in a function that can fail:

```mk
// verdict: ok
error Bad

Get(i: int): Bad!int {
    if i == 2 {
        fail Bad
    }
    return i * 10
}

export Main(sys: &System): WriteError!int {
    var total := 0
    for i in 0..5 {
        onerr sys.Print("onerr at {i}\n", (i)) else {}
        v := Get(i) else {
            continue // an error exit of the loop body
        }
        if i == 3 {
            return 7 // not an error exit: the onerr does not run
        }
        total += v
    }
    return total
}
```

```text
onerr at 2
```

The process exits with status 7.

The common use of `onerr` is a linear value that the success path hands on, and the error path must release. A fallible call consumes its by-value argument only when it succeeds, so after an error the value is still yours:

```mk
// verdict: ok
import std.fs

Save(io: Io, data: []u8): fs.Error!void {
    var f := try fs.Create(io, "out.txt")
    onerr f.Abandon() // runs only if a later step fails
    try f.WriteAll(io, data)
    try f.Close(io) // success: Close consumes f
}
```

- `onerr` is skipped where its value has already moved, so `f.Abandon()` never runs after `Close` succeeded.
- One `onerr` consumes at most one linear value (E0807). Write one `onerr` per value.
- A `defer` or `onerr` body cannot `try`, `fail` or `return`, and cannot `break` or `continue` out of itself (E0806).
- A function that can fail must leave a by-value argument unchanged until it can no longer fail (E0411). The rules are in [R2](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path), with an example in [section 8](#fallible-calls-consume-only-on-success).

```mk
// verdict: reject E0806
error Bad

Flush(): Bad!void {
    fail Bad
}

Write(): Bad!void {
    onerr {
        try Flush() // <- E0806
    }
    fail Bad
}
```

Two linear values need two `onerr`s. `Res` and `Acquire` are from the [test prelude](#19-test-prelude):

```mk
// verdict: reject E0807
error Full

Store(): Full!void {
    fail Full
}

Pair(): Full!void {
    a := Acquire("a")
    b := Acquire("b")
    onerr { // <- E0807
        a.Close()
        b.Close()
    }
    try Store()
    a.Close()
    b.Close()
}
```

See also: [`defer` and `onerr`](memory.md#r21-onerr-and-exits) in the memory model, and [fallible calls](#fallible-calls-consume-only-on-success) in section 8.

### How errors print

Every error has a built-in `Format`. It prints the error's name, then its payload fields in declaration order as `name: value`.

```mk
// verdict: ok
error Denied
error Syntax(line: int, ratio: f64, ok: bool, c: char)

export Main(sys: &System): WriteError!void {
    a := Denied
    b := Syntax(line: 3, ratio: 0.5, ok: true, c: 'x')
    try sys.Print("{a}\n{b}\n", (a, b))
}
```

```text
Denied
Syntax(line: 3, ratio: 0.5, ok: true, c: x)
```

- An error set's value prints the member it holds.
- An error whose payload has a field with no `Format` has no `Format` either. Using one in a format string is E0706. When such an error leaves `Main`, only its name is printed.
- Inside a program, an error prints without its module: `NotFound`. When it leaves `Main`, it prints with the module that declares it: `main.NotFound(id: 3)`, `fs.NotFound`.
- You cannot write `impl Format` for an error (E0704). To print an error your own way, `match` on it.

```mk
// verdict: reject E0704
error Denied

impl Format for Denied { // <- E0704
    Format(&self, w: mut Writer): WriteError!void {
        try w.Str("no")
    }
}
```

An error whose payload has no `Format` cannot be printed with `{e}`:

```mk
// verdict: reject E0706
struct Span {
    from: int
    to: int
}

error Unexpected(at: Span)

Report(sys: &System, e: Unexpected) {
    sys.PrintErr("{e}\n", (e)) else {} // <- E0706
}
```

### Errors from `Main`

`Main` may return `void`, `int`, `E!void` or `E!int`. Its result decides the process's exit status.

| `Main` ends with | Exit status |
|---|---|
| `void`, or success of `E!void` | 0 |
| an `int`, or success of `E!int` | that `int` |
| an error | 1, after printing `error: ` and the error to standard error |
| a panic | 101, after the panic report |

Standard output and standard error are flushed before the process exits. If that last flush fails, the process prints `error: WriteError on stdout` to standard error and exits with 1 where it would have exited with 0.

`Main`'s error set may name errors private to its module. Every other exported signature needs its errors exported ([below](#errors-across-modules)).

**Error return traces.** In a development build, an error that ends `Main` is printed with its trace: the `fail` that raised it, then each `try` that passed it on.

```mk
// verdict: ok
import std.fs

error Empty

Config(a: &Arena, io: Io): fs.Error|OutOfMemory|Empty!str {
    text := try fs.ReadText(a, io, "app.conf")
    if text.Len() == 0 {
        fail Empty
    }
    return text
}

export Main(sys: &System): fs.Error|OutOfMemory|Empty|WriteError!void {
    conf := try Config(sys.arena, sys.io)
    try sys.Print("{conf}\n", (conf))
}
```

Run in a directory with an empty `app.conf`:

```text
error: main.Empty
    at main.mk:9:9
    at main.mk:15:13
```

Run where there is no `app.conf`, the trace starts inside the standard library:

```text
error: fs.NotFound
    at std/fs/fs.mk:18:21
    at std/fs/fs.mk:46:10
    at std/fs/fs.mk:71:10
    at std/fs/fs.mk:98:10
    at main.mk:7:13
    at main.mk:15:13
```

- Each row is the line and column of a `fail` or `try`, the `fail` first.
- A trace keeps 12 rows. Further `try`s are counted on a last line, `    ... 10 more`.
- An error handled by `else` or `match` leaves no rows behind: the next `fail` starts a new trace.
- A release build (`mk build --release`) records nothing and prints the error line alone. A build with `--profile checked` records the trace, as a development build does.
- The trace is recorded on a task. `Main` runs on one in a program that uses `sys.io` (files, the network, sleeping, scopes). A program that never uses `Io` prints the error line alone.

### Errors across modules

An error belongs to the module that declares it. Other modules name it with the module's prefix, as `fs.NotFound`. Two modules may each declare a `NotFound`: they are different errors.

```mk
// verdict: ok
import std.fs

// Size returns the length of a file, or -1 when it does not exist.
Size(a: &Arena, io: Io, path: str): fs.Error|OutOfMemory!int {
    text := fs.ReadText(a, io, path) else e {
        match e {
            fs.NotFound {
                return -1
            }
            _ {
                fail e // every other error goes to the caller
            }
        }
    }
    return text.Len()
}
```

- An error named in an exported signature must be exported itself: `export error NotFound(id: i64)` (E0412). `Main` is the exception.
- By convention a module exports one set named `Error` with every error its functions can return, such as `fs.Error` or `net.Error`. Each function still names the precise set it can fail with, which is often smaller.
- The shared errors in the prelude, such as `OutOfMemory` and `Canceled`, are one error each, whichever module returns them. `fs.Error` holds `Canceled`, and a `match` on it can name `Canceled` without a prefix.

```mk
// verdict: reject E0412
error Missing
export error Denied

export Open(path: str): Missing|Denied!int { // <- E0412
    if path.Len() == 0 {
        fail Missing
    }
    fail Denied
}
```

### Errors in tests and tasks

A `test` block may use `try`. An error that leaves the test fails it, and `mk test` prints the error:

```mk
// verdict: ok
error Denied

Load(ok: bool): Denied!int {
    if !ok {
        fail Denied
    }
    return 1
}

test "load works" {
    n := try Load(true)
    Assert(n == 1)
}

test "load fails" {
    n := try Load(false)
    Assert(n == 1)
}
```

```text
PASS main "load works"
FAIL main "load fails": error return (exit 1)
    error: main.Denied
1 passed, 1 failed
```

A task cannot return an error to its caller directly. Open the scope with `io.ScopeOf[E]()`: each task in it returns `E!void`, the first error cancels the other tasks, and `try scope` passes that error on. See [section 12](#12-concurrency).

### Errors from the standard library

The prelude declares the errors that many modules share:

| Error | Returned when |
|---|---|
| `OutOfMemory` | An allocator could not satisfy a request |
| `Canceled` | The operation's scope was cancelled, or its deadline passed |
| `Closed` | The channel or handle was closed |
| `ReadError`, `WriteError` | A `Reader` or `Writer` failed |
| `ParseError` | Text was not in the expected form |
| `Utf8Error` | Bytes were not valid UTF-8 |

- Anything that allocates can fail with `OutOfMemory`: `List.Push`, `Arena.New`, `str.Replace`. A program that cannot go on without memory passes it up to `Main` with `try`.
- An operation that can wait, such as a read, a lock or a channel send, has `Canceled` in its set. Under a cancelled scope it keeps returning `Canceled` until the scope's block exits. See [cancellation](std/README.md#6-cancellation).
- Library calls that consume an argument consume it only on success ([std's errors](std/README.md#5-errors)).
- Each module's own errors are listed on [its page](#182-module-pages).

### Panics

A panic is not an error. It reports a bug: a state the program should never reach. It prints a report to standard error and ends the process with status 101. Nothing unwinds: no `defer` or `onerr` runs, and no caller can catch it.

```mk
// verdict: ok
export Main(sys: &System): int {
    xs := [1, 2, 3]
    i := sys.args.Len() + 5
    return xs[i]
}
```

```text
panic: bounds at main.mk:5:12
backtrace:
  Main (main.mk)
```

The first line names the cause and the position. A backtrace of the calls follows, unless the environment variable `MK_BACKTRACE` is `0` or `off`. It names each function and its file, innermost first, down to `Main`, the task's function or the test (`test "name"`). The runtime's start below them is left out. Code in the executable that is no Mk function of the program or std gets no line of its own: the functions the compiler adds, the runtime's support routines, and C code linked into the executable. A frame outside the executable, such as one in a system DLL, prints its address.

| Message | Cause |
|---|---|
| the text of `msg` | `Panic(msg)`, a function that returns `never` |
| `assert` | `Assert(c)` with `c` false |
| `must` | `Must()` on `none` or an error, with the error after a colon |
| `contract` | A `requires`, `ensures` or `invariant` that is false. Checked in development and test builds and under `--profile checked`, never in a release build ([section 15](#15-contracts-and-tests)) |
| `overflow` | Integer `+`, `-`, `*` or negation out of range. The wrapping forms `+% -% *%` never panic ([section 7](#7-expressions)) |
| `divide by zero` | Integer `/` or `%` by zero, and the type's minimum divided by -1 |
| `shift amount` | A shift amount below 0, or not below the bit width |
| `bounds` | An index or a slice bound out of range |
| `str boundary` | A `str` slice that does not fall on a character boundary |
| `cast` | `Cast[T](x)` with a value that does not fit in `T` |
| `stack limit` | The stack is exhausted |
| `enum tag` | An enum value from C whose tag names no variant |
| `free non-empty` | `FreeEmpty` on a container that is not empty |
| `wrong allocator` | A heap container given a different allocator from the one it was made with |
| `suspend in callback` | A wait in code that runs off a task, such as a callback from C on a system stack |

Library functions also panic on misuse their documentation names: a `Chunks` length below 1, a negative `Chan` capacity, or an erased `dyn` reference recovered as a type it was not made from.

See also: [`mk query panics`](tools.md#51-member-wide-panic-sites), which lists every place in your code that can panic, and [contracts](#15-contracts-and-tests).

### Designing error sets

- Name the condition, not the operation: `NotFound`, `Denied`, `TooLong`, not `LoadFailed`.
- Put in the payload what the caller needs to act or to report: the id that was missing, the byte offset that was bad. Leave out what the caller already knows, such as the path it passed in.
- Give each function the smallest set it can fail with. A caller can then handle every case, and the signature tells the reader what can go wrong.
- Name a set with an alias when several functions share it, and export one `Error` set per module that holds them all.
- Handle an error where something useful can be done about it: retry, use a default, report it. Elsewhere, pass it on with `try`.
- Use `Must` and `Assert` for conditions that only a bug can break, and errors for everything the outside world can cause.
- In a library, mark a set `open` if a later release may add to it. Marking an existing set `open` later breaks every exhaustive `match` on it outside your package.

### Compiler errors for errors

| Code | Name | Reported for |
|---|---|---|
| E0209 | unused-result | `F() else {}` on an `E!T` whose value is dropped ([`else`](#else)) |
| E0301 | type-mismatch | `e == Denied` on an error ([`match`](#match-on-a-result)) |
| E0305 | field-names | An error constructed without naming its fields ([declaring](#declaring-errors)) |
| E0306 | non-exhaustive | A `match` that misses an error, or lacks `_` on an open set ([sets](#error-sets)) |
| E0307 | not-optional | `else x` on a `?T`, which has no error to bind ([`else`](#else)) |
| E0402 | not-consumed | A linear temporary still live when `try` or `else` leaves the statement (example in [the memory model](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path)) |
| E0408 | not-plain | A payload field that holds a reference other than a `str` ([declaring](#declaring-errors)) |
| E0411 | moved-before-failure | A by-value argument changed before a later failure (example in [section 8](#fallible-calls-consume-only-on-success)) |
| E0412 | private-in-signature | A private error in an exported signature ([modules](#errors-across-modules)) |
| E0605 | loan-into-global | A payload `str` that is not static, stored when the error is built or assigned, or a `mut` borrow of such a field ([declaring](#declaring-errors), example in [the memory model](memory.md#r16-globals)) |
| E0704 | impl-conflict | `impl Format` for an error ([printing](#how-errors-print)) |
| E0706 | requires-failed | An error with no `Format` in a format string ([printing](#how-errors-print)) |
| E0801 | error-not-in-set | A `fail` or `try` whose error is not in the function's set ([`fail`](#fail), [`try`](#try)) |
| E0802 | error-unhandled | An `E!T` used without `try`, `else` or `match` ([results](#results-et)) |
| E0803 | else-falls-through | [`else`](#else) |
| E0804 | try-on-non-error | `try` on a value that cannot fail, such as a `?T` ([`try`](#try)) |
| E0806 | try-in-defer | `try`, `fail`, `return`, `break` or `continue` out of a `defer` or `onerr` body ([`onerr`](#cleanup-on-the-error-path-onerr)) |
| E0807 | onerr-two-values | One `onerr` that consumes two linear values ([`onerr`](#cleanup-on-the-error-path-onerr)) |

See also: [errors in the standard library](std/README.md#5-errors), [linear values and the error path](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path), [`defer` and `onerr`](memory.md#r21-onerr-and-exits), [cancellation](#cancellation).

## 10. Generics and interfaces

A generic function or type takes type parameters in brackets, and works for every type argument that meets its bounds. A bound is usually an interface: a named list of methods that a type implements with an `impl`. The compiler checks a generic body once, against its bounds, so a call that meets the bounds never fails inside the body.

```mk
// verdict: ok
interface Weight {
    Grams(&self): int
}

struct Apple {
    g: int
}

struct Crate {
    apples: int
    empty: int
}

impl Weight for Apple {
    Grams(&self): int {
        return self.g
    }
}

impl Weight for Crate {
    Grams(&self): int {
        return self.empty + self.apples * 150
    }
}

// T is any type that implements Weight
Heavier[T: Weight](a: &T, b: &T): bool {
    return a.Grams() > b.Grams()
}

export Main(sys: &System): WriteError!void {
    x := Heavier(&Apple(g: 120), &Apple(g: 180)) // T is Apple
    y := Heavier(&Crate(apples: 4, empty: 900), &Crate(apples: 9, empty: 0)) // T is Crate
    try sys.Print("{x} {y}\n", (x, y))
}
```

```text
false true
```

| Form | Meaning |
|---|---|
| `F[T](x: T)` | A generic function with the type parameter `T` |
| `struct Queue[T]`, `enum Tree[T]` | A generic type |
| `F[T: A + B]` | `T` must implement both `A` and `B` |
| `F[const N: int]`, `F(const k: int)` | A const parameter: a value known at compile time |
| `F(x)`, `F[i32](x)` | A call that infers the type arguments, or names them |
| `interface I { M(&self): int }` | An interface: methods a type can implement |
| `impl I for T { ... }` | `T` implements `I` |
| `impl[T: Eq] I for Box[T] { ... }` | Every `Box[T]` with `T: Eq` implements `I` |
| `Plain`, `Send`, `Share`, `Fields[B]` | Built-in bounds the compiler computes from a type's fields |
| `if T is u8 { ... }` | A branch for one type argument |
| `F[i32]`, `Queue[i32].Push` | One instance of a generic, as a `fn` value |

### Generic functions

A function's type parameters follow its name. Each parameter can stand for any type that meets its bounds; with no bound, the body can only move the value, borrow it and pass it on.

```mk
// verdict: ok
Occurs[T: Eq](xs: []T, x: &T): int {
    var n := 0
    for y in &xs {
        if y.Eq(x) { // Eq is the bound's one method
            n += 1
        }
    }
    return n
}

Nothing[T](): ?T {
    return none
}

export Main(sys: &System): WriteError!void {
    nums := [4, 9, 4]
    a := Occurs(nums[..], &4) // T inferred from the arguments: int
    b := Occurs[f64]([1.5, 0.5, 1.5][..], &1.5) // T written out
    c: ?str = Nothing() // T inferred from the expected type: str
    empty := c.IsNone()
    try sys.Print("{a} {b} {empty}\n", (a, b, empty))
}
```

```text
2 2 true
```

- Type arguments are inferred from the arguments first, then from the type the result is expected to have.
- When neither fixes a parameter, write the arguments in brackets: `Nothing[str]()`. Otherwise it is E0711.
- Mk has no overloading: one name is one function, generic or not.

```mk
// verdict: reject E0711
Nothing[T](): ?T {
    return none
}

Check(): bool {
    return Nothing().IsNone() // <- E0711
}
```

### Generic types

A struct or an enum takes type parameters, and const parameters, after its name. Its methods name the parameters in their prefix, `Queue[T].Push`, and take their bounds from the type.

```mk
// verdict: ok
struct Pair[A, B] {
    first: A
    second: B
}

// Swap takes self by value, so it consumes the pair and may move its fields
Pair[A, B].Swap(self): Pair[B, A] {
    Pair(first, second) := self
    return Pair[B, A](first: second, second: first)
}

enum Tree[T: Plain] {
    Leaf(v: T)
    Empty
}

Tree[T].Or(&self, other: T): T {
    return match *self {
        .Leaf(v) { v }
        .Empty { other }
    }
}

struct Queue[T: Plain] {
    items: List[T]
    head: int
}

Queue[T].New(a: &Arena): Queue[T] {
    return Queue[T](items: List[T].New(a), head: 0)
}

Queue[T].Push(mut self, x: T into self): OutOfMemory!void {
    try self.items.Push(x)
}

// Next takes items in the order they were pushed
Queue[T].Next(mut self): ?T {
    if self.head == self.items.Len() {
        return none
    }
    x := self.items[self.head]
    self.head += 1
    return some(x)
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    p := Pair(first: 1, second: "one").Swap() // Pair[int, str] becomes Pair[str, int]
    t := Tree[int].Leaf(v: 3)
    var q := Queue[str].New(sys.arena)
    try q.Push("a")
    try q.Push("b")
    front := q.Next().Must()
    try sys.Print("{f} {n} {v} {front}\n", (f: p.first, n: p.second, v: t.Or(0), front))
}
```

```text
one 1 3 a
```

- A constructor call infers the type arguments from the fields, `Pair(first: 1, second: "one")`, or names them, `Tree[int].Leaf(v: 3)`.
- A type's bounds hold wherever the type is named. `Tree[Ticket]`, with a linear `Ticket`, is E0701 at the type.
- A method may add type parameters of its own after its name: `Queue[T].Find[U: Eq](&self, x: &U)`.
- A method cannot add a bound to its type's parameters: `Queue[T: Eq].Has(...)` is E0101. Write a function, `Contains[T: Eq + Plain](q: &Queue[T], x: &T): bool`, or put the bound on the type.
- A generic type is plain, unique or linear according to its fields after substitution: `Pair[int, Ticket]` is linear ([kinds](#kinds)).

```mk
// verdict: reject E0701
linear struct Ticket {
    id: int
}

struct Holder[T: Plain] {
    v: T
}

Keep(t: Ticket): Holder[Ticket] { // <- E0701
    return Holder[Ticket](v: t) // <- E0701
}
```

### Interfaces

An interface lists method signatures. A type that implements every one of them, in an `impl`, can be passed where the interface is a bound.

```mk
// verdict: ok
interface Source[T] {
    Next(mut self): ?T
}

interface Merge {
    With(&self, other: &Self): Self // Self is the implementing type
}

struct Count {
    n: int
    stop: int
}

impl Source[int] for Count {
    Next(mut self): ?int {
        if self.n == self.stop {
            return none
        }
        self.n += 1
        return some(self.n)
    }
}

struct Span {
    lo: int
    hi: int
}

impl Merge for Span {
    With(&self, other: &Span): Span {
        lo := if self.lo < other.lo { self.lo } else { other.lo }
        hi := if self.hi > other.hi { self.hi } else { other.hi }
        return Span(lo: lo, hi: hi)
    }
}

Sum[S: Source[int]](s: mut S): int {
    var total := 0
    for {
        v := s.Next() else {
            break
        }
        total += v
    }
    return total
}

Cover[T: Merge + Plain](xs: []T): T {
    var acc := xs[0]
    for x in xs {
        acc = acc.With(&x)
    }
    return acc
}

export Main(sys: &System): WriteError!void {
    var c := Count(n: 0, stop: 4)
    total := Sum(mut c) // 1 + 2 + 3 + 4
    spans := [Span(lo: 3, hi: 5), Span(lo: 1, hi: 2), Span(lo: 4, hi: 9)]
    s := Cover(spans[..])
    try sys.Print("{total} {lo}..{hi}\n", (total, lo: s.lo, hi: s.hi))
}
```

```text
10 1..9
```

- A method's receiver is `&self`, `mut self` or `self`, as for any method ([functions and methods](#functions-and-methods)).
- `Self` names the implementing type, inside an interface and its `impl`s. An `impl` may write the type itself instead, as `Merge for Span` does.
- An interface may take type parameters, `Source[T]`.
- A method may have type parameters of its own, `Each[T: Format](&self, x: T)`, and `const` parameters.
- A member without a receiver, `New(): Self`, is called on a concrete type, `Count.New()`. A type parameter is not a value, so `T.New()` is E0301.
- An interface has no default method bodies (E0101), no associated types and no inheritance. To require two interfaces, bound by both: `T: Source[int] + Merge`.

An interface is a bound, never the type of a value. `&Shape` is E0301; take a type parameter `[T: Shape]`, or see [values of an interface](#values-of-an-interface-dynof):

```mk
// verdict: reject E0301
interface Shape {
    Area(&self): f64
}

Total(s: &Shape): f64 { // <- E0301
    return s.Area()
}
```

### `impl`

`impl I for T { ... }` makes `T` implement `I`. It holds one method for each method of `I`, with the same signature. An impl's method fails exactly when the method of `I` names an error set (E0707). Where the result of `I`'s method is a type parameter of `I` and the impl's argument is an `E!T`, as `Get(&self): T` in an `impl Source[E!int]`, the method cannot fail: it returns the `E!T` as a value. A generic impl writes that result as its own type parameter, and any other as an alias of the `E!T` (`type R = E!int`, then `Get(&self): R`), since a result written `E!int` fails.

```mk
// verdict: ok
interface Shape {
    Area(&self): f64
}

struct Box[T] {
    v: T
    size: f64
}

// one impl covers Box[T] for every T that is Format and Plain
impl[T: Format + Plain] Format for Box[T] {
    Format(&self, w: mut Writer): WriteError!void {
        try Print(w, "box({v})", (v: self.v))
    }
}

impl[T] Shape for Box[T] {
    Area(&self): f64 {
        return self.size * self.size
    }
}

export Main(sys: &System): WriteError!void {
    b := Box[int](v: 7, size: 1.5)
    a := b.Area() // an impl's methods are called like any method
    try sys.Print("{b} {a}\n", (b, a))
}
```

```text
box(7) 2.25
```

- A type has at most one `impl` of each interface, whatever the arguments: `impl Shape for Box[int]` with `impl Shape for Box[f64]`, or `impl Source[int]` with `impl Source[str]` for one type, are E0704.
- An `impl` lives in the module that declares the interface or the one that declares the type (E0704 elsewhere). So `impl Format for int` is possible only in the library.
- `impl[T: Eq] I for Box[T]` implements `I` for each `Box[T]` whose `T` meets the bound. An `impl` for a bare type parameter, `impl[T] I for T`, is E0704.
- A missing method, an extra one, or a signature that differs from the interface's, such as `mut self` for `&self`, is E0707.
- Calling an interface's method on a type with no `impl` of it is E0702, and the message names the `impl` to write.

```mk
// verdict: reject E0707
interface Shape {
    Area(&self): f64
    Name(&self): str
}

struct Square {
    side: f64
}

impl Shape for Square { // <- E0707
    Area(&self): f64 {
        return self.side * self.side
    }
}
```

```mk
// verdict: reject E0704
interface Shape {
    Area(&self): f64
}

impl[T] Shape for T { // <- E0704
    Area(&self): f64 {
        return 1.0
    }
}

impl Format for int { // <- E0704
    Format(&self, w: mut Writer): WriteError!void {
        try w.Str("n")
    }
}
```

```mk
// verdict: reject E0702
interface Shape {
    Area(&self): f64
}

struct Square {
    side: f64
}

Get(q: &Square): f64 {
    return q.Area() // <- E0702
}
```

### Bounds

`T: A + B` requires `T` to implement `A` and `B`. Bounds are written inline, in the brackets; there is no `where` clause.

The body may use only what its bounds give. An operator or a method the bounds do not provide is an error in the generic body, even before any call:

```mk
// verdict: reject E0703
Biggest[T](a: &T, b: &T): bool {
    return *a > *b // <- E0703
}
```

Even under `T: Eq` or `T: Ord`, a generic body cannot use `==`, `!=`, `<` or `>` (E0703): it calls the interface's method. Write `Biggest[T: Ord](a: &T, b: &T)` with `return b.Less(a)`, and `a.Eq(b)` for equality.

A call whose type argument does not meet a bound is an error at the call:

```mk
// verdict: reject E0701
struct Circle {
    r: f64
}

interface Shape {
    Area(&self): f64
}

Measure[T: Shape](x: &T): f64 {
    return x.Area()
}

Bad(c: &Circle): f64 {
    return Measure(c) // <- E0701
}
```

A body that meets its bounds compiles for every type argument that meets them. The one error instantiation itself can cause is [runaway instantiation](#runaway-instantiation) (E0708).

### Built-in bounds

Four bounds are not interfaces: the compiler computes them from a type's fields. `impl Plain` and `impl Fields[B]` are E0704. `Send` and `Share` can be asserted only with `unsafe impl` in a module that `pkg.mk` lists in `unsafe_modules` ([section 14](#14-unsafe-and-c)); a plain `impl Send` is E1101.

| Bound | `T` meets it when | See |
|---|---|---|
| `Plain` | `T` is copied on use, so the body may copy and drop it | [kinds](#kinds) |
| `Send` | A value of `T` may move to another task | [section 12](#12-concurrency) |
| `Share` | A `&T` may be shared with another task | [section 12](#12-concurrency) |
| `Fields[B]` | Every exported field of `T` meets `B`, so `fields(&x)` can visit them | [`fields`](#fields) |

**Linear type parameters.** A parameter with no `Plain` bound may stand for a linear type. So the body must consume each value of it on every path: return it, store it or pass it on. Dropping one is E0402. With `T: Plain`, the body may copy a value or let it go.

```mk
// verdict: reject E0402
Forget[T](x: T) { // <- E0402
}
```

```mk
// verdict: ok
Forget[T: Plain](x: T) {
}

Keep[T](x: T): T {
    return x
}
```

### Interfaces in the library

The prelude declares the interfaces most code meets ([the module map](#17-module-map)).

| Interface | Methods | Used by |
|---|---|---|
| `Format` | `Format(&self, w: mut Writer): WriteError!void` | `{x}` holes in `Print` and `Fmt` |
| `Eq` | `Eq(&self, other: &Self): bool` | `Has`, `Index`, `Map` and `Set` keys |
| `Ord` | `Less(&self, other: &Self): bool` | `math.Min`, `math.Max`, `BTree` keys, `PriorityQueue` |
| `Hash` | `Hash(&self, h: mut Hasher)` | `Map` and `Set` keys |
| `Free` | `Free(self, a: &Alloc)` | Releasing a value that owns heap memory: `HeapList`, `Box`, `Chan` and the other heap containers implement it |
| `Slice[T]` | `Items(&self): []T`, `ItemsMut(mut self): mut []T` | Every slice function |

For your own types, write the `impl` or generate it with `gen derive.Eq(T)` and its siblings ([generators](#generators)).

`Slice[T]` is the one interface with functions of its own: `Len`, `Sort`, `Index` and the rest are declared once as `Slice[T].Name`, and work on `[]T`, arrays, `List`, `HeapList` and any type of yours that implements it. Only `Slice[T]` has them: a user interface has no namespace, so `Shape.Name(...)` is E0301.

```mk
// verdict: ok
struct Ring {
    data: [4]int
}

impl Slice[int] for Ring {
    Items(&self): []int {
        return self.data[..]
    }
    ItemsMut(mut self): mut []int {
        return mut self.data[..]
    }
}

export Main(sys: &System): WriteError!void {
    var r := Ring(data: [4, 1, 3, 2])
    r.Sort(fn(a: &int, b: &int): bool { return *a < *b }) // Slice[int].Sort
    n := r.Len()
    first := r.data[0]
    try sys.Print("{n} {first}\n", (n, first))
}
```

```text
4 1
```

### Values of an interface: `dyn.Of`

An interface is not a type, so to hold values of several types behind one interface, generate a type with `gen dyn.Of(Shape)`: see [the tour](overview.md#methods-and-interfaces) and [std.dyn](std/dyn.md).

### Ownership in interface methods

An interface method's signature carries `from` and `into` clauses like any other ([section 8](#8-ownership-and-borrowing)). A caller through a bound sees only the interface's signature, so an `impl` method may borrow or store no more than the interface declares (E0608). It may declare less.

```mk
// verdict: reject E0608
interface Peek {
    Get(&self, s: str): str from s
}

struct Saved {
    s: str
}

impl Peek for Saved {
    Get(&self, s: str): str from self, s { // <- E0608
        return self.s
    }
}
```

### Specialization: `if T is`

`if T is u8 { ... }` runs its branch only when the type argument `T` is `u8`, and inside the branch `T` is `u8`. Use it for a faster path on one type.

```mk
// verdict: ok
Memchr(xs: []u8, x: u8): ?int {
    for i, y in xs {
        if y == x {
            return some(i)
        }
    }
    return none
}

IndexOf[T: Eq](xs: []T, x: &T): ?int {
    if T is u8 {
        return Memchr(xs, *x) // here xs is []u8
    }
    for i, y in &xs {
        if y.Eq(x) {
            return some(i)
        }
    }
    return none
}

linear struct Handle {
    id: int
}

Handle.Close(self) {
    Handle(id: _) := self
}

Finish[T](x: T, done: fn(T)): str {
    if T is int {
        return "dropped" // an int is plain: nothing has to consume x
    }
    done(x)
    return "handed on"
}

CloseHandle(h: Handle) {
    h.Close()
}

Ignore(n: int) {
}

export Main(sys: &System): WriteError!void {
    bytes: [3]u8 = [7, 8, 9]
    i := IndexOf(bytes[..], &9).Must()
    a := Finish(5, Ignore)
    b := Finish(Handle(id: 1), CloseHandle)
    try sys.Print("{i} {a} {b}\n", (i, a, b))
}
```

```text
2 dropped handed on
```

- The compiler checks both branches when it checks the generic, not per instance.
- A value whose type is plain inside the branch needs no consuming there: it ends at the branch's end.
- `if T is` names one type and takes `else` and `else if T is` like any `if`; `||` and `&&` are E0101.

### Generic functions as values

`F[T]`, `Type[T].M` or `Type[T].Name`, written without a call, is a `fn` value of that one instance, as `Span.With` is for a method of a type that is not generic ([function values](#function-values)).

```mk
// verdict: ok
// an instance named with its type arguments is a fn value
Same[T](x: T): T {
    return x
}

Apply(f: fn(i32): i32, x: i32): i32 {
    return f(x)
}

Four(): i32 {
    return Apply(Same[i32], 4)
}

struct Holder[T: Plain] {
    v: T
}

Holder[T].Get(&self): T {
    return self.v
}

Read(h: &Holder[i32], get: fn(&Holder[i32]): i32): i32 {
    return get(h)
}

Five(): i32 {
    h := Holder[i32](v: 5)
    return Read(&h, Holder[i32].Get)
}
```

- Every type argument must be written. They are never inferred for a value, even where the expected type would fix them (E0711).
- A function with a `const` parameter is never a `fn` value (E0705).

```mk
// verdict: reject E0711
// a generic named as a value takes its type arguments
Same[T](x: T): T {
    return x
}

Apply(f: fn(i32): i32, x: i32): i32 {
    return f(x)
}

Four(): i32 {
    return Apply(Same, 4) // <- E0711
}
```

### Const parameters

A `const` parameter takes a value known at compile time. A function takes it among its parameters, `Pad(s: str, const o: Opts)`; a type, and a function that is generic over it, take it in brackets, `struct Matrix[const R: int, const C: int]`.

```mk
// verdict: ok
struct Matrix[const R: int, const C: int] {
    cells: [R][C]f64
}

Matrix[R, C].Zero(): Matrix[R, C] {
    return Matrix[R, C](cells: [[0.0; C]; R])
}

Matrix[R, C].Rows(&self): int {
    return R // a const generic is a value in the body
}

struct Opts {
    width: int
    fill: u8
}

enum Mode {
    Fast
    Safe
}

const Default: Opts = Opts(width: 4, fill: 46)
const Rows: int = 2
const Small: i8 = 3

Pad(s: str, const o: Opts): int {
    return o.width - s.Len()
}

Speed(const m: Mode): int {
    return match m {
        .Fast { 2 }
        .Safe { 1 }
    }
}

Width(const w: int): int {
    return w
}

export Main(sys: &System): WriteError!void {
    m := Matrix[Rows, Rows + 1].Zero() // a 2 by 3 matrix
    rows := m.Rows()
    a := Pad("ab", Default) // a named const
    b := Pad("ab", Opts(width: 10, fill: 32)) // a struct literal of constants
    c := Speed(Mode.Fast) // a variant without a payload
    d := Width(int(Small) * 2 + SizeOf[u32]()) // an expression of constants
    try sys.Print("{rows} {a} {b} {c} {d}\n", (rows, a, b, c, d))
}
```

```text
2 2 8 2 10
```

The argument of a `const` parameter is one of:

- a literal, a named const, a `const` parameter or generic, or a field of a `const` parameter (`o.width`);
- a struct, record or array literal of such values;
- an enum variant without a payload;
- `+`, `-` and `*` over integers of these;
- an expression over literals and named consts alone, with operators, conversions, `Cast`, `Trunc`, `SizeOf`, `AlignOf` or an array's `Len`: `2 + 3`, `-K`, `int(K8)`.

Anything else is E0705: a variable, a call, `/` or another operator over a `const` parameter (`k / 2`), an index such as `Sizes[1]`, or a field of a named const such as `Default.width` or of an element such as `Rows[0].width`. An array length follows the same rule.

**`const` parameters of `fn` type.** `Sort`'s `const less: fn(&T, &T): bool` takes a function literal that may read or mutate the caller's locals ([function literals](#function-literals)). In exchange, the body may use such a parameter in two ways only:

- call it;
- pass it on as the argument of another `const` `fn` parameter, in a call that is not `s.go`, at most once per call.

Any other use is E0705: binding it, storing it, returning it, passing the same `const` `fn` parameter to two parameters of one call, or using it inside a `fn` literal or a contract. A type's const parameter cannot be of `fn` type (E0705 at the type).

```mk
// verdict: ok
Count(xs: []i32, const keep: fn(&i32): bool): int {
    var n := 0
    for x in xs {
        if keep(&x) { // called
            n += 1
        }
    }
    return n
}

CountAll(xs: []i32, const keep: fn(&i32): bool): int {
    return Count(xs, keep) // passed on to another const fn parameter
}

export Main(sys: &System): WriteError!void {
    xs: [4]i32 = [3, 8, 1, 9]
    limit: i32 = 2
    n := CountAll(xs[..], fn(x: &i32): bool { return *x > limit }) // reads limit
    try sys.Print("{n}\n", (n))
}
```

```text
3
```

```mk
// verdict: reject E0705
Count(xs: []i32, const keep: fn(&i32): bool): fn(&i32): bool {
    return keep // <- E0705
}

Scale(x: int, const k: int): int {
    return x * k
}

AsValue(): fn(int, int): int {
    return Scale // <- E0705
}

Run(var k: int): int {
    return Scale(1, k) // <- E0705
}
```

- A `const` parameter of any type other than `fn`, such as `const f: F` with `F` a type parameter, is an ordinary value in the body. Its argument cannot be a literal that captures (E0609), and a `const` `fn` parameter cannot be passed on to it (E0705).
- Inside a `fn` literal, a captured `const` parameter is an ordinary value, not a const argument (E0705).
- A `const f: str` passed on as the format of `Print` or `Arena.Fmt` is checked at each call against that call's text ([formatting](std/core.md#formatting-and-printing)).

**Inferring a const generic.** A call may leave out a function's const generic when an argument's type names it, as it may leave out a type parameter: the length of an array argument gives the `N` of a `[N]T` parameter. A const generic that no argument's type names is written in brackets (E0711 otherwise), and two arguments that give different values are E0301.

```mk
// verdict: ok
Total[const N: int](xs: &[N]int): int {
    var sum := 0
    for i in 0..N {
        sum += xs[i]
    }
    return sum
}

First[T: Plain, const N: int](xs: [N]T): T {
    return xs[0]
}

export Main(sys: &System): WriteError!void {
    xs := [1, 2, 3]
    ys: [2]u8 = [9, 8]
    a := Total(&xs)    // N is 3, from the argument's type
    b := First(ys)     // T is u8 and N is 2
    c := Total[3](&xs) // or written
    try sys.Print("{a} {b} {c}\n", (a, b, c))
}
```

```text
6 9 6
```

### Const arguments in a generic

Inside a generic body, a const argument, such as an array length, must be simple:

- a const generic, such as `N`;
- a literal;
- `SizeOf[T]()`;
- `simd.Lanes[T]()` of a concrete type;
- a named const that mentions no parameter;
- an enum variant without a payload.

There is no arithmetic: `[N - 1]u8` is E0709. Use a slice. `AlignOf[T]()` and `simd.Lanes[T]()` of a type parameter are not allowed either (E0709, E0703). An array length takes a const generic `[const N: int]`, not a `const` value parameter: `b: [k]u8` with `const k: int` is E0705.

```mk
// verdict: reject E0709
Buf[T, const N: int](x: &T): int {
    b: [N - 1]u8 = [0; N - 1] // <- E0709 E0709
    return b.Len()
}
```

### Compile-time requirements

A `requires` clause that reads only const parameters, consts and literals, or calls functions on them, is checked at compile time, at each call, in every build. A false clause is E0706 at the call.

```mk
// verdict: ok
Lane(const k: int): int
    requires k < 4
{
    return k * 8
}

Forward(const k: int): int
    requires k < 4 // restates Lane's clause, so it can pass k on
{
    return Lane(k)
}

export Main(sys: &System): WriteError!void {
    a := Lane(3)
    b := Forward(1)
    try sys.Print("{a} {b}\n", (a, b))
}
```

```text
24 8
```

```mk
// verdict: reject E0706
Lane(const k: int): int
    requires k < 4
{
    return k * 8
}

High(): int {
    return Lane(4) // <- E0706
}

Forward(const k: int): int {
    return Lane(k) // <- E0706
}
```

`High` breaks the clause. `Forward` passes its own `const` parameter on without restating the clause, so the compiler cannot check `Lane`'s clause at `Forward`'s callers.

Unlike a const argument in a type, such a clause may use arithmetic and `SizeOf` (`requires M * 2 == N`). It may also call an imported type's method on the parameter (`requires o.CanLoad()`). Any other clause is a run-time contract ([section 15](#15-contracts-and-tests)). A run-time contract is checked on entry in development and test builds, and not at all in a release build.

#### At a call with every argument known

These rules apply at a call, or at a function named as a value, outside a generic.

- **Imported function.** Its clause runs at compile time, whatever it names. False is E0706 and true passes. A clause that needs a run-time operation, such as reading a `var` or doing I/O, is left to run time.
- **Function of the same module.** The compiler computes the clause from operators over const arguments, consts and literals, and `SizeOf` and `AlignOf` of a type parameter. It makes no other call, not even `SizeOf[u8]()`, and `&&` and `||` do not short-circuit. A clause it cannot compute this way is left to run time.
- A const argument `SizeOf[E]()` of a known type counts as its value.
- A function named as a value, `F[3]`, is checked where it is named, as a call would be: false is E0706 at the value.
- At a value, an imported clause whose evaluation panics, runs out of fuel or breaks the [stage rule](#the-stage-rule) is not reported.

#### Inside a generic: restating a clause

- A clause that reads only arguments known there is checked as above, and only false is reported.
- A clause that reads an unknown argument and names a `var` or a parameter that is not `const` is a run-time contract, and needs no restating. For an imported function this is decided from the clause's text, so a `var` reached through an import (`cfg.Limit`) does need restating.
- Any other clause must be restated by the enclosing function, as `Forward` restates `requires k < 4`. So must a clause that reads a `const` parameter or generic that the caller passes on. A missing restatement is E0706 at the inner call.
- The restated clause writes the argument in place of the callee's parameter, and the callee's type parameter, bare or in `SizeOf[T]()`, as the call writes it. Every other name must mean what it means in the callee, and an item of another module is named through the file's import. A clause that names an item the caller cannot name the same way is E0706 at the call: `cannot be judged here`.
- The restated clause is then checked at each call of the enclosing function, by these same rules.
- No restatement is needed when the caller's own clauses list each value of every `const` generic the clause names, as `requires N == 2 || N == 4`. Each may list at most 64 values. The clause must hold for every combination, at most 4,096 of them. A range such as `N >= 0 && N < 3` is not a list.
- In a `fn` literal, a `requires` or an `ensures`, the enclosing function is the one it is written in. A struct invariant has nowhere to restate a clause, so a clause there is not checked.

```mk
// verdict: ok
Lane[const K: int](): int
    requires K < 4
{
    return K * 8
}

Pick[const N: int](): int
    requires N == 1 || N == 2 // Lane's K < 4 holds for both, so no restating
{
    return Lane[N]()
}
```

#### Checked only on entry

These clauses are neither checked at compile time nor restated. They are run-time contracts:

- Inside a generic, a clause that reads no unknown `const` generic and reads a type built on a type parameter: a type generic whose argument is such a type, or the type itself (`SizeOf[[2]T]()`). If such a clause also reads a `const` value parameter that the caller passes on, the caller restates it, with the type as the call writes it. The restated clause is checked on entry too.
- Inside a generic, a clause that reads a `const` argument no clause can spell. That is a `SizeOf` of a type other than a bare type parameter, or a value that is neither an integer nor a `bool`. If such a clause also reads a `const` the caller passes on, it is E0706 at the call. The exception is a `const` generic whose values the caller's clauses list.

```mk
// verdict: ok
Small[T](x: &T): int
    requires SizeOf[[2]T]() < 64
{
    return 1
}

Outer[U](x: &U): int {
    return Small(x) // checked when Small is entered: U is not known here
}

export Main(sys: &System): WriteError!void {
    n := Outer(&7)
    try sys.Print("{n}\n", (n))
}
```

```text
1
```

#### Methods of an `impl`

A method of an interface's `impl` may not have a compile-time `requires` that names a generic or a `const` parameter (E0706 at the clause). A caller through a bound could not check it. A run-time clause, or one over consts and literals alone, is allowed. It is checked at a direct call, and only on entry through a bound.

```mk
// verdict: reject E0706
interface Pick {
    Get(&self, const i: int): int
}

struct Row {
    n: int
}

impl Pick for Row {
    Get(&self, const i: int): int
        requires i < 4 // <- E0706
    {
        return self.n + i
    }
}
```

### Runaway instantiation

A generic that calls itself with an ever larger type argument would need instances without end. The compiler stops past a depth of 64 along one chain, or 100,000 instances in one build, and reports E0708 at the outermost concrete call, with the chain.

```mk
// verdict: reject E0708
struct Wrap[T: Plain] {
    v: T
}

Nest[T: Plain](x: T, n: int): int {
    if n == 0 {
        return 0
    }
    return Nest(Wrap[T](v: x), n - 1) + 1 // Nest[Wrap[Wrap[...]]]
}

Start(): int {
    return Nest(1, 3) // <- E0708
}
```

### Compiler errors for generics and interfaces

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | A method body inside an `interface`, a `where` clause ([interfaces](#interfaces)), a bound in a method's type prefix ([generic types](#generic-types)) |
| E0301 | type-mismatch | An interface used as a type, `T.New()` on a type parameter ([interfaces](#interfaces)) |
| E0402 | not-consumed | A value of an unbounded `T` dropped ([built-in bounds](#built-in-bounds)) |
| E0608 | flow-mismatch | An `impl` method that borrows or stores more than its interface says ([ownership](#ownership-in-interface-methods)) |
| E0609 | literal-captures | A capturing literal passed to a `const` parameter that is not of `fn` type ([const parameters](#const-parameters)) |
| E0701 | bound-unmet | A type argument that does not meet a bound ([bounds](#bounds), [generic types](#generic-types)) |
| E0702 | no-impl | An interface method called on a type with no `impl` ([`impl`](#impl)) |
| E0703 | needs-bound | An operation the bounds do not give ([bounds](#bounds)) |
| E0704 | impl-conflict | A duplicate, orphan or blanket `impl`, or an `impl` of `Plain` or `Fields[B]` ([`impl`](#impl)) |
| E0705 | not-const | A const argument that is not constant, a misused `const` `fn` parameter ([const parameters](#const-parameters)) |
| E0706 | requires-failed | A compile-time `requires` that is false or not restated ([requirements](#compile-time-requirements)) |
| E0707 | impl-signature | An `impl` method missing, extra or with another signature, or one that fails where its interface method cannot, or the reverse ([`impl`](#impl)) |
| E0708 | instantiation-limit | Instantiation without end ([runaway instantiation](#runaway-instantiation)) |
| E0709 | const-arithmetic | Arithmetic in a const argument inside a generic ([const arguments](#const-arguments-in-a-generic)) |
| E0711 | cannot-infer | A type argument nothing fixes, or a generic value without its arguments ([generic functions](#generic-functions), [values](#generic-functions-as-values)) |
| E1101 | needs-unsafe | `impl Send` or `impl Share` without `unsafe` ([built-in bounds](#built-in-bounds)) |

See also: [the tour of generics](overview.md#generics), [write a generic container](cookbook.md#write-a-generic-container), [generators](#generators) for `derive` and `dyn`, [kinds](#kinds).

## 11. Compile time

Mk runs ordinary functions while it compiles. There is no separate language for constants and no marker on functions: a `const` initializer calls the same functions the program calls at run time, and the result is stored in the executable.

```mk
// verdict: ok
Squares(): [8]int {
    var t: [8]int = [0; 8]
    for i in 0..8 {
        t[i] = i * i
    }
    return t
}

const Table = Squares() // runs once, while compiling

export Main(sys: &System): WriteError!void {
    for i in 0..8 {
        try sys.Print("{v} ", (v: Table[i]))
    }
    try sys.Print("\n{p}-byte pointers\n", (p: Target.pointer_bytes))
}
```

```text
0 1 4 9 16 25 36 49
8-byte pointers
```

| Form | Use |
|---|---|
| `const X = F(a)` | Run `F` at compile time and keep the value ([evaluating constants](#evaluating-constants)) |
| `const X = F(a) fuel n` | The same, with a larger step budget ([fuel](#fuel)) |
| `EvalArena()` | An arena that lasts for one evaluation ([compile-time arenas](#compile-time-arenas-and-static-slices)) |
| `Target` | Facts about the target, fixed at compile time ([the target](#branching-on-the-target)) |
| `if` or `match` on a constant | Code for one target or profile; the other branches emit nothing |
| `for name, value in fields(&x)` | A loop over a struct's fields ([`fields`](#fields)) |
| `gen m.G(T)` | Run a generator that writes code for `T` ([generators](#generators)) |
| `quote { ... }`, `$(e)` | Code a generator emits, and a value spliced into it |
| `const` parameters | Arguments known at compile time ([section 10](#const-parameters)) |

Compile-time evaluation is deterministic: it has no clock, no environment and no network, and reads no files except the package's own data files (`build.File`, `g.Input`). A build gives the same result every time. To try an expression at compile time, use [`mk repl`](tools.md#8-the-repl).

### Evaluating constants

`const` binds a name to a value computed at compile time. The initializer may call any function that takes no `Io` and calls none that does: the compiler runs it, and the executable holds only the result.

```mk
// verdict: ok
Fib(n: int): int {
    var a := 0
    var b := 1
    for _ in 0..n {
        next := a + b
        a = b
        b = next
    }
    return a
}

const N = 4
const Fib40: int = Fib(40) // a typed const

struct Packet {
    header: [N * 2]u8 // consts may size an array
    body: [16]u8
}

const PacketSize = SizeOf[Packet]()

var Answer: int = Fib(9) + 8 // a var global's initializer runs at compile time too

export Main(sys: &System): WriteError!void {
    try sys.Print("{f} {s} {a}\n", (f: Fib40, s: PacketSize, a: Answer))
}
```

```text
102334155 24 42
```

- A const holds plain data: numbers, `bool`, `char`, arrays, records, structs and enums of them, and `str`s and slices over static data. Its only references are to other consts.
- A const cannot hold a container that owns memory, such as a `List`: that is E1002.
- The same evaluation runs a `var` global's initializer, a compile-time `requires` ([section 10](#compile-time-requirements)) and a generator.
- A panic during an evaluation, such as an index out of bounds, is a compile error, E0901. The report shows the calls that led to it.

```mk
// verdict: reject E0901
Pick(i: int): int {
    xs := [1, 2, 3]
    return xs[i]
}

const Bad = Pick(5) // <- E0901
```

```text
main.mk:7:7: E0901 const-failed: compile-time evaluation panicked: bounds
    | const Bad = Pick(5)
    |       ^^^
    panic: bounds
    trace: [{"name":"Pick","at":"main.mk:4:12"},{"name":"Bad","at":"main.mk:7:13"}]
```

### What an evaluation may do

An evaluation runs Mk code as a program would, but it has no process around it. It cannot reach the outside world or memory it did not allocate.

| Operation | At compile time |
|---|---|
| Arithmetic, control flow, calls, generics | Runs, with the same checks as at run time |
| Allocation from `EvalArena()` | Runs; the memory lasts for the evaluation |
| Reading another const | Runs |
| An `extern` function with an Mk body | Runs like any function |
| A call of a bodiless `extern` (a C function) | E0901 |
| Reading or writing a `var` global's memory | E0901 (`refused: global`); taking its address is allowed |
| Raw pointers in `unsafe` code | Only inside memory the evaluation allocated, including its read-only copies of the consts it reads. A raw write to such a copy, reading a pointer's bytes as an integer, and any other raw access are E0901 |
| `asm`, a call of a `naked` function, `std.mmio` | E0901 ([section 14](#14-unsafe-and-c)) |
| A call of a function that takes an `Io` (any `Io` operation, `sys.Syscall`) | Refused with E0901 (`refused: io`): compile-time code does no I/O |

```mk
// verdict: ok
// an extern with an Mk body runs at compile time
export extern Twice(n: i32): i32 {
    return n * 2
}

const Four: i32 = Twice(2)
```

A `var` global holds run-time state, so an evaluation cannot read it:

```mk
// verdict: reject E0901
var Hits: int = 0

Read(): int {
    return Hits
}

const H = Read() // <- E0901
```

### Fuel

An evaluation is bounded by fuel, counted in steps. The default is 4 million steps. To allow more, write `fuel` and a number after the initializer:

```mk
// verdict: ok
Spin(): int {
    var n := 0
    for i in 0..10_000_000 {
        n += i % 7
    }
    return n
}

const Total = Spin() fuel 200_000_000

export Main(sys: &System): WriteError!void {
    try sys.Print("{t}\n", (t: Total))
}
```

```text
29999994
```

Without the `fuel` clause the evaluation stops after 4 million steps:

```mk
// verdict: reject E0902
Spin(): int {
    var n := 0
    for i in 0..10_000_000 {
        n += i % 7
    }
    return n
}

const Total = Spin() // <- E0902
```

```text
main.mk:10:7: E0902 fuel-exhausted: compile-time evaluation ran out of fuel after 4000000 steps
    | const Total = Spin()
    |       ^^^^^
    steps: 4000000
    fuel: 4000000
    trace: [{"name":"Spin","at":"main.mk:6:5"},{"name":"Total","at":"main.mk:10:15"}]
    try: raise the limit: fuel 8000000
```

- Each basic operation costs one step. Moving data costs one more step per 16 bytes, rounded up: what an assignment writes, what a call or a return passes, and what an allocation, a frame, a copy, a fill or a compare touches.
- Each call of the generator API costs one step. A generic function costs what its instance for the given types costs.
- The count is fixed for a compiler version, so a build that fits on one machine fits on every machine.
- Running out is E0902, reported before the operation that does not fit, with the calls that led there.
- `fuel` also follows a `gen` line: `gen derive.Eq(Big) fuel 10_000_000`.
- The fuel is a number literal or a named constant, `fuel Limit`; any other expression is E0705. A gen line runs before its module's consts are evaluated, so its named constant comes from another module (E0908, [the stage rule](#the-stage-rule)).
- An evaluation's frames, measured at the target's layout, may hold 1 MiB, or a quarter of the stated fuel in bytes when that is more, up to 256 MiB. Deeper recursion is the `stack limit` panic, reported as E0901.

### The stage rule

Some evaluations run before the module's own functions and types are ready: the length in an array type, a const argument, a compile-time `requires`, a `gen` argument, and a const named in a match pattern. Such an evaluation may not call a function of the same module, or need the layout of a type of the same module (E0908). The report names the consts and functions the evaluation entered, with the trace.

```mk
// verdict: reject E0908
Size(): int {
    return 4
}

struct Buf {
    data: [Size()]u8 // <- E0908
}
```

- Literals, const parameters, `Target`, `SizeOf` of an imported or generic type, and integer consts computed from literals and other such consts, as `N` is, are always allowed in these places.
- `SizeOf` of a same-module type is allowed too, though it reads that type's layout. In a function signature or a type declaration, passed to a const parameter of an integer type other than `int`, it is E0301: the size is not known yet, so it cannot be judged to fit. Written in a same-module `const` that a type then names (`const S = SizeOf[In]()`, `data: [S]u8`), it is E0908: write the `SizeOf` in the type itself.
- A `const` may call functions of its own module, as `Fib40` does above. Such a `const` cannot then appear in a type.
- To use a helper in a type of the same module, move the helper to a submodule.

Two cycles are errors too. A const whose evaluation reads itself is E0909, and a type that contains itself by value is E0907:

```mk
// verdict: reject E0909
const A: int = B + 1 // <- E0909
const B: int = A + 1
```

```mk
// verdict: reject E0907
struct Node { // <- E0907
    next: Node
}
```

A list of nodes holds the next node through a `Box`, or an index into a slice.

### Compile-time arenas and static slices

`EvalArena()` gives an evaluation an arena that lasts as long as the evaluation. Pass it to a function that takes `a: &Arena`. Nothing frees it: `str`s and slices in the result become static data in the executable.

```mk
// verdict: ok
// Primes lists the primes below n, in a.
Primes(a: &Arena, n: int): OutOfMemory![]int {
    var xs := List[int].New(a)
    for i in 2..n {
        var prime := true
        for j in 2..i {
            if i % j == 0 {
                prime = false
                break
            }
        }
        if prime {
            try xs.Push(i)
        }
    }
    return xs.Done()[..]
}

const SmallPrimes: []int = Primes(EvalArena(), 30).Must()

export Main(sys: &System): WriteError!void {
    for p in SmallPrimes {
        try sys.Print("{p} ", (p))
    }
    try sys.Print("\n{n} primes\n", (n: SmallPrimes.Len()))
}
```

```text
2 3 5 7 11 13 17 19 23 29
10 primes
```

`EvalArena()` exists only at compile time: in a `const` or global initializer, a compile-time `requires` or a generator. Anywhere else it is E0705.

```mk
// verdict: reject E0705
Scratch(): &Arena {
    return EvalArena() // <- E0705
}
```

**Static slices.** In a `const` initializer, an array literal whose expected type is `[]T` is a `[]T` over static data, as a string literal is a `str` over static bytes. `[]` there is the empty `[]T`.

```mk
// verdict: ok
// array literals are static slices in a const initializer
struct Route {
    path: str
    methods: []str
}

const Routes: []Route = [
    Route(path: "/", methods: ["GET"]),
    Route(path: "/notes", methods: ["GET", "POST"]),
    Route(path: "/health", methods: []),
]

Second(): str {
    return Routes[1].path
}
```

- The rule covers the initializer's own expressions, including nested constructor and call arguments, but not the bodies an evaluation runs.
- Elsewhere an array literal is a `[N]T` and needs an array type. Against a `[]T` it is E0301, and `[]` with no expected array type is E0711.
- A package manifest is written this way ([the manifest](packages.md#2-the-manifest)).

```mk
// verdict: reject E0301
// outside a const initializer an array literal is an array
First(xs: []int): int {
    return xs[0]
}

One(): int {
    return First([1, 2]) // <- E0301
}
```

### Branching on the target

`Target` is a constant of type `TargetInfo` that describes the target the build is for. An `if` or a `match` on a constant keeps only the branch that applies: the others emit no code.

```mk
// verdict: ok
OsName(): str {
    return match Target.os {
        .Windows { "windows" }
        .Linux { "linux" }
        .Freestanding { "none" }
        _ { "other" } // Os is open: later releases may add systems
    }
}

HasOs(): bool {
    if Target.os == Os.Freestanding {
        return false // compiled only for a freestanding target
    }
    return true
}

const Wide = Target.pointer_bytes == 8

export Main(sys: &System): WriteError!void {
    try sys.Print("{os} {has} {w}\n", (os: OsName(), has: HasOs(), w: Wide))
}
```

On x86-64 Windows:

```text
windows true true
```

| Field | Type | Holds |
|---|---|---|
| `arch` | `Arch` | `X64`, `Arm64` or `Riscv64` |
| `os` | `Os` | `Linux`, `Windows`, `Macos`, `Freestanding` or `Nutshell` |
| `pointer_bytes` | `int` | 8 on every target Mk supports |
| `vector_bits` | `int` | The SIMD register width, 128 on x86-64 and arm64. `simd.Lanes` divides it |

- Each field is fixed by the target, never by `--cpu` or the build profile.
- Both branches of the `if` are type-checked for the target, so a mistake in code for another system is still reported. A const expression in a dead branch is evaluated only where a type needs its value.
- `Arch` and `Os` are open enums: a `match` on them needs a `_` arm.
- For code that differs by build profile, `if` on [`build.Profile`](std/build.md#profiles). For whole modules that differ by target, see [per-target modules](#per-target-modules).

### `fields`

`for name, value in fields(&x)` runs its body once for each field of `x`, in declaration order for a struct, and in name order for a record ([record types](#record-types)). `name` is the field's name as a `str`, and `value` a reference to the field. A generic function uses it under a `Fields[B]` bound, which requires every field to implement `B`.

```mk
// verdict: ok
struct Config {
    export port: int
    export host: str
    export verbose: bool
}

// Dump prints each field of a struct or record whose fields all have Format.
Dump[A: Fields[Format] + Plain](w: mut Writer, x: A): WriteError!void {
    for name, value in fields(&x) {
        try w.Str(name)
        try w.Str(" = ")
        try value.Format(w)
        try w.Str("\n")
    }
}

export Main(sys: &System): WriteError!void {
    c := Config(port: 8080, host: "localhost", verbose: false)
    var w := sys.Stdout()
    try Dump(mut w, c)
    try Dump(mut w, (x: 1, y: 2)) // a record works too
}
```

```text
port = 8080
host = localhost
verbose = false
x = 1
y = 2
```

- The loop is unrolled at compile time: each field's `value` has that field's type.
- `fields` takes a reference, `fields(&x)`. `fields(x)` is E0301.
- `Fields[B]` and generic bodies see exported fields only, in every module.
- A `fields` loop over a concrete type in its own module sees all its fields. To reach private fields from elsewhere, use a [generator](#generators).
- A type argument with a field that does not implement `B` is E0701 at the call.
- A `fn` literal in the loop body cannot capture a name the loop declares (E0609).

```mk
// verdict: reject E0609
Apply(x: int, const f: fn(n: int): int): int {
    return f(x)
}

NameLengths[A: Fields[Format] + Plain](x: A): int {
    var n := 0
    for name, _ in fields(&x) {
        n += Apply(1, fn(k: int): int { return k + name.Len() }) // <- E0609
    }
    return n
}
```

### Generators

A generator is an ordinary function that writes code. It takes `g: mut Gen` and data about declarations, such as a `Type`, and it emits Mk source built with `quote` blocks. A `gen` line runs it while the module compiles, and the emitted code becomes part of the module.

Use one when code follows mechanically from a declaration: comparing, hashing or printing every field, encoding to JSON, parsing command-line flags. The standard library's generators are [`derive.Eq`, `derive.Hash` and `derive.Format`](std/derive.md), [`json.Codec`](std/json.md), [`csv.Codec`](std/csv.md), [`cli.Args`](std/cli.md), [`regex.Matcher`](std/regex.md) and [`dyn.Of`](std/dyn.md).

```mk
// verdict: ok
import std.derive

struct Tile {
    row: int
    col: int
}

gen derive.Eq(Tile)     // writes impl Eq for Tile
gen derive.Format(Tile) // writes impl Format for Tile

export Main(sys: &System): WriteError!void {
    a := Tile(row: 1, col: 2)
    b := Tile(row: 1, col: 3)
    try sys.Print("{a} {same}\n", (a, same: a.Eq(&b)))
}
```

```text
Tile(row: 1, col: 2) false
```

#### Writing a generator

A generator lives in its own module, and the module with the `gen` line imports it. This one, in `src/show/show.mk`, writes a `Describe` method that prints each field as `name=value`:

```mk
// verdict: ok
// Describe writes T.Describe(&self, w: mut Writer).
export Describe(g: mut Gen, t: Type) {
    var body := g.Block()
    for f in t.Fields() {
        name := g.Str(f.Name()) // the field's name as a string literal
        body.Add(quote {
            try w.Str($name)
            try w.Str("=")
            try self.$(f.Name()).Format(w)
            try w.Str(" ")
        })
    }
    g.Emit(quote {
        $(t.Name()).Describe(&self, w: mut Writer): WriteError!void {
            $body
        }
    })
}
```

`src/main.mk` uses it:

```mk
// verdict: parse
import src.show

struct Point { // all fields private: a gen line in this module sees them
    x: int
    y: int
    label: str
}

gen show.Describe(Point)

export Main(sys: &System): WriteError!void {
    p := Point(x: 3, y: 4, label: "corner")
    var w := sys.Stdout()
    try p.Describe(mut w)
    try sys.Print("\n")
}
```

```text
$ mk run .
x=3 y=4 label=corner
```

The compiler writes what the generator emitted to `gen/<target>/gen.mk` in the module's directory, here `src/gen/x86_64-windows/gen.mk`, so you can read it. A single file given by name (`mk run main.mk`) has no module directory of its own and gets no view; name its directory (`mk run .`) to get one:

```text
// gen show.Describe#1 for Point
Point.Describe(&self, w: mut Writer): WriteError!void {
    try w.Str("x")
    try w.Str("=")
    try self.x.Format(w)
    try w.Str(" ")
    ...
}
```

Each emitted item opens with a comment that names its `gen` line, as `// gen show.Describe#1 for Point` does. The compiler never reads that file, and you do not commit it. A hand edit to it is E0904 at the next build; delete the file and the next `mk` command writes it again.

#### Quotes and splices

`quote { ... }` is a value of type `Code`: a run of Mk tokens. Inside it, `$x` splices the value of the local `x`, and `$(e)` the value of any expression. What a splice produces depends on its type:

| Operand | Splices as |
|---|---|
| `str` | One identifier, such as a field name. A `str` that is not an identifier is E0901 |
| `Code` | Its tokens. `g.Str(s)` makes the string literal of `s`, and `g.Ident(s)` an identifier |
| `Block` | Its statements, one per line |
| `Type` | The type's name, resolved where the code lands, with its generic arguments |
| An integer, `bool`, `char` | A literal of that value |
| A float | The shortest decimal that reads back to the same bits |
| A `fn` value | Its function's qualified name |

Any other operand type is E0301 at the splice.

#### The generator API

These types are in the prelude, so a generator module needs no import for them. Every method runs only at compile time.

| Method | Gives |
|---|---|
| `g.Emit(c)` | Appends the items of `c` to the output. They may not include `gen` or `import` (E0905) |
| `g.Block()` | An empty `Block`; `b.Add(c)` appends a statement |
| `g.Str(s)`, `g.Ident(s)` | `s` as a string literal, or as an identifier (a keyword or non-identifier is E0901) |
| `g.Fresh(base)` | An identifier unique in the module, built from `base` |
| `g.Arena()` | An arena for the run, to build text and lists with the standard library |
| `g.Input(path)` | The bytes of a file in the package ([data files](std/build.md#data-files)) |
| `g.Fail(msg)`, `g.FailAt(f, msg)`, `g.FailOn(t, msg)` | Report E0903 at the `gen` line, at field `f`, or at type `t`. The run continues; its output is discarded |
| `c.Then(d)` | A `Code` of `c`'s tokens followed by `d`'s |
| `t.Name()`, `t.Text()` | The type as a resolved `Code`, or its bare name as a `str` |
| `t.Kind()` | A `TypeKind`: `Struct`, `Enum`, `Error`, `Interface`, `Record`, `Scalar`, `Ref`, `Slice`, `Array`, `Optional`, `Fallible`, `Fn`, `RawPtr` or `Opaque` |
| `t.Exported()`, `t.Same(u)`, `t.Args()`, `t.Elem()` | Whether it is exported; type identity; generic arguments; element type |
| `t.Fields()` | The fields of a struct or error in declaration order, or of a record in name order. For a type of another module, its exported fields only |
| `t.Variants()` | An enum's variants; each has `Name()` and `Fields()` |
| `t.Methods()` | An interface's methods, or a struct's or enum's own functions, in declaration order, read from their signatures |
| `f.Name()`, `f.Type()`, `f.Exported()`, `f.Doc()` | A field's name, type, visibility and `///` comment |
| `m.Name()`, `m.Receiver()`, `m.Params()`, `m.Result()`, `m.Generic()`, `m.Signature()` | A method's signature. `m.Flows()` and `m.HasFlows()` give its `from`, `into` and `ends` clauses |
| `fl.Kind()`, `fl.Param()`, `fl.Path()`, `fl.Field()` | One `Flow` of `m.Flows()`: its `FlowKind` (`From`, `Into`, `Ends`, or `Store` for `*q into p`); the destination (`result` for `From`, the receiving parameter for `Into` and `Store`, the ended parameter for `Ends`, `self` for the receiver); the source as written, `""` for `Ends`; the result field of a per-field clause, else `""`. `m.Flows()` lists one entry per source, by kind, field, parameter and path |
| `p.Name()`, `p.Type()`, `p.Const()` | A parameter |

A generator reports bad input with `Fail`. This one refuses a type that is not a struct:

```mk
// verdict: ok
export Count(g: mut Gen, t: Type) {
    if t.Kind() != TypeKind.Struct {
        g.FailOn(t, "Count needs a struct")
        return
    }
    n := t.Fields().Len()
    g.Emit(quote {
        $(t.Name()).FieldCount(): int {
            return $n
        }
    })
}
```

With `Count` in `src/show`, a `src/main.mk` that declares `enum Color { Red Green }` on lines 3 to 6 and runs `gen show.Count(Color)` on line 8 gets the error at the enum:

```text
src/main.mk:3:6: E0903 generator-error: Count needs a struct
    | enum Color {
    |      ^^^^^
    generator: show.Count
    gen: src/main.mk:8:1
```

The standard library's generators report the same way:

```mk
// verdict: reject E0903
import std.derive

gen derive.Eq(int) // <- E0903
```

#### What a generator sees

A generator reads declarations, never function bodies. That keeps its output a function of the module's declarations, so it reruns only when they change.

| Rule | Detail |
|---|---|
| Sees | The types it is given and their fields, with their `///` comments; for a type of the `gen` line's module, private fields too; for a type of another module, exported fields only. Also signatures, interfaces, and files read by `build.File` and `g.Input` |
| Does not see | Function bodies, the output of other `gen` lines of the same module, the clock, the environment, the network, randomness |
| Generated types | It sees the module as if no code were generated. A type that only a `gen` line declares shows a name and no structure (`TypeKind.Opaque`). Reading its fields is E0906 |
| Arguments | Handwritten or imported types, interfaces, literals, and non-generic functions, which it reads by signature; a function argument gives a `Method` whose `Receiver()` is `Static` |
| Where | `gen` is a top-level line. The generator is in another module, which this one imports. Its output holds no `gen` |
| Generic code | A `gen` line names concrete types only, so a generic body cannot run a generator on its `T`. Generate at the concrete type, and bound the generic code on the interface the generator implements |
| Names in a quote | As listed below the table |
| Reruns | A `gen` line runs again when anything it read or called changes, including a lookup that found nothing, and not otherwise |

Names in a `quote`:

- A spliced `Type` needs no import where the code lands.
- A name written in a `quote` resolves in the generator module's imports, then the prelude, then the universe names.
- A bare name never finds the generator module's own items. Name them as an importer does, by the last segment of the module's path (`json.Value` in a quote of `std.json`), unless one of the generator module's imports takes that name.
- Such a name reaches only exported items, and needs no import where the code lands.

#### Errors in generated code

Generated code is checked as if it were handwritten, `export` included: a method the generator emits without it is private to the module of the `gen` line. std's generators export the methods they write when the type is exported. An error in generated code is E0905, and the report cites the generated line, the line of the generator that emitted it, and the `gen` line.

```mk
// verdict: reject E0905
import std.derive

struct Inner {
    n: int
}

struct Outer {
    inner: Inner
}

gen derive.Eq(Outer) // <- E0905
```

```text
main.mk:12:1: E0905 generated-invalid: generated code is invalid: `Inner` has no impl of `Eq`, which declares `Eq`
    | gen derive.Eq(Outer)
    | ^^^^^^^^^^^^^^^^^^^^
    output: gen/x86_64-windows/gen.mk:6:24
    emitter: std/derive/derive.mk:52:5
    gen: main.mk:12:1
    inner: {"code":"E0702","name":"no-impl","message":"`Inner` has no impl of `Eq`, which declares `Eq`"}
```

Add `gen derive.Eq(Inner)` and it compiles.

An emitted `impl` obeys the same rules as a handwritten one. Two `gen` lines that emit one `impl` conflict, and an `impl` for an interface and a type that the module declares neither of is an orphan. Both are E0704 at the `gen` line. The report of a conflict also cites the first `gen` line:

```mk
// verdict: reject E0704
import std.derive

struct P {
    n: int
}

gen derive.Eq(P)
gen derive.Eq(P) // <- E0704
```

```mk
// verdict: reject E0704
import std.derive
import std.time

gen derive.Eq(time.Duration) // <- E0704
```

See also: [`std.derive`](std/derive.md), [`std.build`](std/build.md) for embedded files and the build profile, [generating at a concrete site](codebase.md#5-generating-at-a-concrete-site).

### Compiler errors for compile time

| Code | Name | Reported for |
|---|---|---|
| E0301 | type-mismatch | An array literal passed as a `[]T` outside a const; `fields(x)` without `&`; a splice of a type that cannot splice; a same-module `SizeOf` passed to a non-`int` integer const parameter in a signature or type ([the stage rule](#the-stage-rule)) |
| E0306 | non-exhaustive | A `match` on `Target.os` or `Target.arch` without `_` ([the target](#branching-on-the-target)) |
| E0609 | literal-captures | A `fn` literal in a `fields` loop that captures the loop's names ([`fields`](#fields)) |
| E0701 | bound-unmet | A `Fields[B]` argument with a field that lacks `B` |
| E0704 | impl-conflict | A `gen` line whose `impl` is a duplicate or an orphan ([errors in generated code](#errors-in-generated-code)) |
| E0705 | not-const | `EvalArena()` outside a compile-time evaluation ([arenas](#compile-time-arenas-and-static-slices)) |
| E0711 | cannot-infer | `[]` with no expected array type |
| E0901 | const-failed | A panic or a refused operation during an evaluation ([what an evaluation may do](#what-an-evaluation-may-do)) |
| E0902 | fuel-exhausted | An evaluation out of fuel ([fuel](#fuel)) |
| E0903 | generator-error | A generator's `Fail` ([the generator API](#the-generator-api)) |
| E0904 | generated-edited | A hand edit to a file under `gen/` |
| E0905 | generated-invalid | Generated code that does not compile ([errors in generated code](#errors-in-generated-code)) |
| E0906 | forbidden-input | A generator that reads the structure of a generated type ([what a generator sees](#what-a-generator-sees)) |
| E0907 | layout-cycle | A type that contains itself ([the stage rule](#the-stage-rule)) |
| E0908 | stage-violation | A type, const argument, `requires`, `gen` argument or const pattern that needs its own module's functions or layouts ([the stage rule](#the-stage-rule)) |
| E0909 | const-cycle | A const that reads itself ([the stage rule](#the-stage-rule)) |
| E0910 | file-input | A bad path for `build.File` or `g.Input` ([data files](std/build.md#data-files)) |
| E1002 | not-share | A const that holds a `Cell` or a raw pointer, such as a `List` ([evaluating constants](#evaluating-constants)) |

## 12. Concurrency

A task is a function running at the same time as the code that started it. Tasks start inside a `scope` block, and the block does not end until every task in it has finished. So a task may borrow its parent's locals, and the compiler checks that no two tasks race on the same memory.

```mk
// verdict: ok
// Primes counts the primes in [from, to) and adds the count to out
Primes(from: int, to: int, out: mut int) {
    for n in from..to {
        var prime := n >= 2
        for d in 2..n {
            if d * d > n {
                break
            }
            if n % d == 0 {
                prime = false
                break
            }
        }
        if prime {
            *out += 1
        }
    }
}

export Main(sys: &System): WriteError!void {
    var low := 0
    var high := 0
    scope s := sys.io.Scope() { // a scope waits for every task started in it
        s.go Primes(0, 50000, mut low) // each task borrows its own counter
        s.go Primes(50000, 100000, mut high)
    } // both tasks have finished here
    total := low + high
    try sys.Print("{low} + {high} = {total}\n", (low, high, total))
}
```

```text
5133 + 4459 = 9592
```

| Form | Meaning |
|---|---|
| `scope s := io.Scope() { ... }` | Open a scope. The block waits for all its tasks before it ends |
| `scope s := io.ScopeWith(l) { ... }` | The same, with a limit on tasks and their stack size |
| `try scope s := io.ScopeOf[E]() { ... }` | A scope whose tasks can fail with errors from `E` |
| `s.go F(args)` | Start a task that runs `F(args)` |
| `s.Cancel()` | Cancel the scope: its body, its tasks and the scopes inside it |
| `s.CancelAfter(d)` | Cancel the scope when the duration `d` has passed |
| `s.Expired()` | Whether the scope's own deadline cancelled it |
| `io.NoCancel()` | An `Io` that ignores the cancellation of the enclosing scopes |
| `T: Send`, `T: Share` | Bounds: a `T` may move into a task; a `&T` may be shared between tasks |

Mk has no `async` and `await`, and no task handles. A task is started in a scope and joined by it.

### `Io` and waiting

A function can wait only through an `Io` it is given, or a value that holds one, such as a `Reader` or a `Writer`. Sleeping, reading a file, receiving from a channel and taking a lock all wait. Any function can call any other: a call that waits is an ordinary call.

```mk
// verdict: ok
import std.time

// Tick waits, so it takes an Io. Count takes none, so it cannot wait.
Tick(io: Io, n: int): Canceled!int {
    for _ in 0..n {
        try io.Sleep(time.Millis(1)) // the task waits; its thread runs other tasks
    }
    return n
}

Count(xs: []int): int {
    return xs.Len()
}

export Main(sys: &System): Canceled|WriteError!void {
    ticks := try Tick(sys.io, 3) // an ordinary call
    try sys.io.Yield() // let other tasks run
    xs := [4, 5, 6]
    n := Count(xs[..])
    try sys.Print("{ticks} ticks, {n} items\n", (ticks, n))
}
```

```text
3 ticks, 3 items
```

- `Main` gets its `Io` as `sys.io`. Pass it down to the functions that need it.
- Most operations that wait have `Canceled` in their error set, so the signature shows that a scope can [cancel](#cancellation) them. A `Reader` or `Writer` reports a cancel as `ReadError` or `WriteError` instead.
- Tasks run on a pool of operating-system threads, one per processor by default (`MK_CARRIERS` sets the number). A thread runs a task until the task waits; it never preempts it. A task that spins in a loop waiting for another, with no `Io` call, can keep that task from running.
- `io.Yield()` lets other tasks run. `io.Sleep(d)` waits for a duration.

See also: [`Io` and the system handle](std/README.md#4-io-and-the-system-handle), [carriers and stacks](std/runtime.md#carriers-tasks-and-stacks).

### Scopes

`scope s := e { ... }` opens a scope from `e`, an `Io` call that makes one, and binds it to `s` inside the block. Tasks started with `s.go` belong to the scope. When the block ends, by any exit, it waits for every one of them.

```mk
// verdict: ok
Fill(out: mut int, v: int) {
    *out = v
}

export Main(sys: &System): WriteError!void {
    var a := 0
    var b := 0
    var c := 0
    scope outer := sys.io.Scope() {
        outer.go Fill(mut a, 1)
        scope inner := sys.io.Scope() { // scopes nest
            inner.go Fill(mut b, 2)
        } // waits for inner's task only
        c = b + 10 // b is ready here
    } // waits for outer's task
    try sys.Print("{a} {b} {c}\n", (a, b, c))
}
```

```text
1 2 12
```

- `io.Scope()`, `io.ScopeWith(limits)` and `io.ScopeOf[E]()` make a scope. Each is valid only as the value of a `scope` statement: a `Scope` cannot be stored in a local, returned or kept past its block (E1003).
- Inside the block, `&s` can be passed to helpers and to tasks, which may start more tasks in it ([helpers](#helpers-that-start-tasks)).
- A scope opened inside another belongs to it: cancelling the outer one cancels the inner one too.
- The binding must be a name. `scope _ := ...` does not parse. A binding that is never used is E0208.

A `Scope` lives only in its `scope` statement:

```mk
// verdict: reject E1003
Keep(io: Io) {
    s := io.Scope() // <- E1003
    s.Cancel()
}
```

### Starting a task: `s.go`

`s.go F(args)` starts a task that runs the call `F(args)`. The call may be a function, a method or a `fn` value. The parent goes on at once; the task runs at the same time.

```mk
// verdict: ok
struct Tally {
    total: int
}

Tally.Add(mut self, n: int) {
    self.total += n
}

Square(n: int, out: mut int) {
    *out = n * n
}

export Main(sys: &System): WriteError!void {
    var t := Tally(total: 0)
    var sq := 0
    f := Square
    scope s := sys.io.Scope() {
        s.go t.Add(5) // a method call
        s.go f(3 + 4, mut sq) // a fn value; 3 + 4 is computed here, in the parent
    }
    total := t.total
    try sys.Print("{total} {sq}\n", (total, sq))
}
```

```text
5 49
```

- The arguments are evaluated in the parent, at the `s.go`, before the task starts.
- Every argument's type must be `Send` (E1001, E1002), as [below](#send-and-share).
- What an argument borrows stays borrowed until the scope's block ends. Until then, the parent cannot touch what a task borrowed `mut`, nor write what a task borrowed with `&` (E0502, E0504).
- A task under `io.Scope()` returns `void` (E0301). It hands results back through `mut` arguments, as above, or a [channel](#channels). An error can come back the same way: the task stores it in a `mut ?E` slot that the parent reads after the block ([recipe](cookbook.md#handle-errors-inside-tasks)). Use `ScopeOf[E]` ([below](#tasks-that-fail-scopeofe)) when the first error should cancel the rest.
- `&System` and `Writer` cannot cross into a task, so a task cannot call `sys.Print`. It reports back and the parent prints, or it logs through a `log.Sink`, which is `Send` and `Share` ([`std.log`](std/log.md), [recipe](cookbook.md#report-from-many-tasks)).
- `s.go` is a statement. Its call cannot carry `else`, and `s.go` cannot appear in a `defer` or `onerr` anywhere inside the scope's block, nested blocks included (E1003).

A task returns nothing to its parent:

```mk
// verdict: reject E0301
Double(n: int): int {
    return n * 2
}

Run(io: Io) {
    scope s := io.Scope() {
        s.go Double(3) // <- E0301
    }
}
```

The parent cannot write what a task borrowed until the block ends:

```mk
// verdict: reject E0502
Bump(out: mut int) {
    *out += 1
}

Reset(io: Io): int {
    var n := 0
    scope s := io.Scope() {
        s.go Bump(mut n)
        n = 5 // <- E0502
    }
    return n
}
```

Fix: move the write after the block, where the task has finished.

A task cannot start in a `defer` or `onerr` inside the scope's block:

```mk
// verdict: reject E1003
Work() {
}

Run(io: Io) {
    scope s := io.Scope() {
        defer {
            s.go Work() // <- E1003
        }
    }
}
```

See also: [tasks in the memory model](memory.md#r17-tasks), which has the loan rules of `s.go`, and [process a slice in parallel](cookbook.md#process-a-slice-in-parallel).

### Tasks that fail: `ScopeOf[E]`

A task cannot return an error to its parent directly. Open the scope with `io.ScopeOf[E]()` instead: each task in it returns `E!void`, the first error cancels the scope, and `try scope` passes that error to the caller.

```mk
// verdict: ok
import std.time

error Bad(n: int)
error Late

Check(io: Io, n: int, ms: i64): Bad|Canceled!void {
    try io.Sleep(time.Millis(ms))
    fail Bad(n: n)
}

Both(io: Io): Bad|Canceled!void {
    try scope s := io.ScopeOf[Bad|Canceled]() {
        s.go Check(io, 1, 30)
        s.go Check(io, 2, 10) // fails first and cancels the other
    }
}

BodyFails(io: Io): Bad|Late|Canceled!void {
    try scope s := io.ScopeOf[Bad|Canceled]() {
        s.go Check(io, 3, 1000)
        fail Late // the body's own error cancels the task too
    }
}

export Main(sys: &System): WriteError!void {
    Both(sys.io) else e {
        try sys.Print("both: {e}\n", (e))
    }
    BodyFails(sys.io) else e {
        try sys.Print("body: {e}\n", (e))
    }
}
```

```text
both: Bad(n: 2)
body: Late
```

- A task in a `Scope[E]` returns `E!void`, or `F!void` where every error of `F` is in `E` (E0801). A task returning `void` there is E0301, and so is a call that [cannot fail](#results-et) and returns an `E!void` as a value, which the scope would drop.
- The first error a task returns cancels the scope: the body and every other task. Those tasks see `Canceled` at their next cancellable operation, and their `Canceled` is not reported. After the join, `try scope` fails with the first error.
- A `scope` over a `Scope[E]` must be written `try scope` (E0802). `try scope` over a plain `Scope` is E0804. There is no `else` form: to handle the error, put the scope in a function of its own and handle that function's error, as `Main` does above.
- The function's error set must hold `E` (E0801), as for any `try`.
- An error from the block's body leaves as it would from any block: the scope cancels its tasks, waits for them, then passes the body's error on. So a body that passes on the `Canceled` of its own wait (`try io.Sleep(d)`) makes `try scope` fail with `Canceled`, not with the task's error. Handle `Canceled` in the body, or do the waiting in tasks.

A scope whose tasks can fail must pass their error on:

```mk
// verdict: reject E0802
error Bad

Work(): Bad!void {
    fail Bad
}

Run(io: Io): Bad!void {
    scope s := io.ScopeOf[Bad]() { // <- E0802
        s.go Work()
    }
}
```

Every error a task can return must be in the scope's set:

```mk
// verdict: reject E0801
error Bad
error Other

Odd(): Other!void {
    fail Other
}

Run(io: Io): Bad!void {
    try scope s := io.ScopeOf[Bad]() {
        s.go Odd() // <- E0801
    }
}
```

A task in a `Scope[E]` returns `E!void`:

```mk
// verdict: reject E0301
import std.fs

Tick() {
}

Clear(io: Io): fs.Error!void {
    try fs.Remove(io, "stamp")
}

Run(io: Io): fs.Error!void {
    try scope s := io.ScopeOf[fs.Error]() {
        s.go Clear(io)
        s.go Tick() // <- E0301
    }
}
```

A plain `Scope` has no error to pass on:

```mk
// verdict: reject E0804
Work() {
}

Run(io: Io) {
    try scope s := io.Scope() { // <- E0804
        s.go Work()
    }
}
```

See also: [errors](#9-errors), [errors in tests and tasks](#errors-in-tests-and-tasks).

### Helpers that start tasks

A function that starts tasks in its caller's scope takes `s: &Scope`. Each argument it hands to a task is marked `into s`, because the task keeps the loan until the scope ends. A task may also take `&s` and start more tasks.

```mk
// verdict: ok
Leaf(n: int, out: mut int) {
    *out = n
}

// Pair starts two tasks in the caller's scope
Pair(s: &Scope, a: mut int into s, b: mut int into s) {
    s.go Leaf(1, a)
    s.go Leaf(2, b)
}

// Spawner is a task that starts another task in the scope it was given
Spawner(s: &Scope, out: mut int into s) {
    s.go Leaf(99, out)
}

export Main(sys: &System): WriteError!void {
    var a := 0
    var b := 0
    var late := 0
    scope s := sys.io.Scope() {
        Pair(&s, mut a, mut b)
        s.go Spawner(&s, mut late)
    } // the join also waits for the task Spawner started
    try sys.Print("{a} {b} {late}\n", (a, b, late))
}
```

```text
1 2 99
```

- Without `into s`, the helper's `s.go` is E0604: the signature does not say the loan goes into the scope.
- The join waits until no task remains, including tasks started while it waits.
- `Scope` is `Share`, so `&s` may cross into a task.
- Under `io.ScopeOf[E]()`, the helper takes `s: &Scope[E]`. A `&Scope[E]` is not a `&Scope` (E0301).

See also: [`into`](memory.md#r11-into-and-q-into-p), and the E0604 example in [tasks](memory.md#r17-tasks).

### When a scope ends

A scope's block ends in a fixed order, whichever way it exits:

1. The block's `defer` and `onerr` blocks run, in reverse order.
2. On an early exit (a `return`, `break` or `continue`, a `fail`, or a `try` that passes an error on), the scope is cancelled.
3. The scope waits for every task (the join).
4. The loans its tasks held end.
5. The block's locals end.

```mk
// verdict: ok
import std.time

error Bad

// Note logs a line without waiting.
// TrySend takes no Io, so it works under a cancelled scope; else { none } ignores Closed.
Note(log: &Chan[str], line: str into log) {
    _ = log.TrySend(line) else { none }
}

Work(): Bad!void {
    fail Bad
}

Slow(io: Io, log: &Chan[str]) {
    defer Note(log, "slow ends")
    io.Sleep(time.Seconds(5)) else {
        Note(log, "slow: Canceled")
    }
}

Body(io: Io, log: &Chan[str]): Bad!void {
    scope s := io.Scope() {
        defer Note(log, "body defer")
        s.go Slow(io, log)
        try Work() // an early exit: the scope is cancelled
        Note(log, "not reached")
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    log := try Chan[str].New(sys.heap.Alloc(), 8)
    Body(sys.io, &log) else {
        Note(&log, "body failed")
    }
    log.Close()
    for {
        line := log.Recv(sys.io) else {
            break
        }
        try sys.Print("{line}\n", (line))
    }
    log.Free(sys.heap.Alloc())
}
```

```text
body defer
slow: Canceled
slow ends
body failed
```

The program ends at once rather than after five seconds: the early exit cancelled `Slow`'s sleep. Because the `defer` runs before the join, a `defer` in the block cannot wait for the tasks' results; read them after the block.

### `Send` and `Share`

`Send` and `Share` are built-in bounds that the compiler computes for every type. A value whose type is `Send` may move into another task. A type is `Share` when tasks may hold `&T` to the same value at once.

| Type | `Send` | `Share` |
|---|---|---|
| Numbers, `bool`, `char`, `str`, `fn` values, plain structs and enums of them | yes | yes |
| `&T`, `[]T` | if `T` is `Share` | if `T` is `Share` |
| `Cell[T]` | if `T` is `Send` | no |
| `List`, `Map`, `Set`, `Text` (in an `Arena`) | no | no |
| `Box[T]`, `HeapList[T]`, `HeapMap[K, V]`, `TextMap[V]`, `HeapText` | if their contents are | if their contents are |
| `sync.Shared[T]` | if `T` is `Send` and `Share` | if `T` is `Send` and `Share` |
| `fs.File`, `net.Stream` | yes | no |
| `net.Listener` | yes | yes |
| `Guard[T]` | no | no |
| `Mutex[T]`, `Chan[T]` | if `T` is `Send` | if `T` is `Send` |
| `atomic.AtomicInt`, `atomic.Atomic[T]` | yes | yes |
| `Heap`, `Scope`, `Io` | yes | yes |
| `Arena`, `Alloc`, `System`, `Writer`, `Reader` | no | no |
| `*T` (a raw pointer) | no | no |
| A struct, enum or record | if every field is | if every field is |

So a `&Cell[int]` cannot reach another task, but a `&Mutex[int]` can.

```mk
// verdict: ok
Show[T: Plain + Send](x: T) {
}

// Each runs Show(x) as a task; it needs T: Send to hand x over
Each[T: Plain + Send](io: Io, xs: []T) {
    scope s := io.Scope() {
        for x in xs {
            s.go Show(x)
        }
    }
}

Read[T](x: &T) {
}

// ShareAll lets every task read the same value, so T must be Share
ShareAll[T: Share](io: Io, x: &T) {
    scope s := io.Scope() {
        s.go Read(x)
        s.go Read(x)
    }
}

Run(io: Io) {
    nums := [1, 2, 3]
    Each(io, nums[..])
    m := Mutex[int].New(0)
    ShareAll(io, &m) // a Mutex is Share
}
```

- A `mut T` argument is `Send` when `T` is.
- In a generic function, `s.go` with an argument of type `T` needs `T: Send` in the bounds (E1001); one of type `&T` needs `T: Share` (E1002).
- A global `var` must be `Share`, since every task can read it (E1002, [globals](memory.md#r16-globals)).
- A type built on raw pointers is neither. Its module can assert a bound with `unsafe impl` ([`unsafe impl`](#unsafe-impl)).

A task cannot take the system handle or an arena:

```mk
// verdict: reject E1002
Report(sys: &System) {
}

Scratch(a: &Arena) {
}

Run(sys: &System, a: &Arena) {
    scope s := sys.io.Scope() {
        s.go Report(sys) // <- E1002
        s.go Scratch(a) // <- E1002
    }
}
```

A generic task argument needs the bound:

```mk
// verdict: reject E1001
Keep[T: Plain](x: T) {
}

Each[T: Plain](io: Io, x: T) {
    scope s := io.Scope() {
        s.go Keep(x) // <- E1001
    }
}
```

See also: [tasks in the memory model](memory.md#r17-tasks), with the `Cell` and `Arena` examples, and [kinds of types](#kinds).

### Channels

A `Chan[T]` carries values of `T` from task to task, in order. `Send` waits for room and `Recv` waits for a value. A channel's capacity is how many values it holds before `Send` waits; a capacity of 0 makes each `Send` wait for a `Recv`.

```mk
// verdict: ok
// a producer, two workers and the main task, joined by channels
Produce(io: Io, jobs: &Chan[int]) {
    defer jobs.Close() // the workers stop once the channel is drained
    for n in 1..7 {
        jobs.Send(io, n) else {
            return
        }
    }
}

Square(io: Io, jobs: &Chan[int], results: &Chan[int]) {
    for {
        n := jobs.Recv(io) else {
            return // Closed: no more jobs
        }
        results.Send(io, n * n) else {
            return
        }
    }
}

Workers(io: Io, jobs: &Chan[int], results: &Chan[int]) {
    defer results.Close() // runs once both workers have finished
    scope w := io.Scope() {
        w.go Square(io, jobs, results)
        w.go Square(io, jobs, results)
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    jobs := try Chan[int].New(a, 2) // room for two values
    results := try Chan[int].New(a, 0) // each Send waits for a Recv
    var total := 0
    scope s := sys.io.Scope() {
        s.go Produce(sys.io, &jobs)
        s.go Workers(sys.io, &jobs, &results)
        for {
            v := results.Recv(sys.io) else {
                break // Closed: every worker is done
            }
            total += v
        }
    }
    try sys.Print("total {total}\n", (total))
    jobs.Free(a)
    results.Free(a)
}
```

```text
total 91
```

- `Chan[T].New(a, capacity)` allocates from `a`, an `&Alloc` such as `sys.heap.Alloc()`. `Free(a)` releases it.
- Tasks share a channel by `&Chan[T]`. `T` must be `Send`.
- After `Close`, `Recv` returns the values still buffered, then `Closed`.
- To wait on several channels at once, use [`sync.Select`](std/sync.md#select).

See also: [channels](std/core.md#chan) for every method, [`std.sync`](std/sync.md) for `Select`, semaphores and notifications.

### Mutexes

A `Mutex[T]` holds a `T` that one task at a time may use. `Lock` waits until the mutex is free and returns a `Guard[T]`, through which you read the value (`g.Ref()`) and write it (`g.Mut()`). `Unlock` releases it.

```mk
// verdict: ok
Add(io: Io, m: &Mutex[int]) {
    for _ in 0..1000 {
        var g := m.Lock(io) else {
            return // Canceled
        }
        defer g.Unlock() // runs at the end of each iteration
        *g.Mut() += 1
    }
}

export Main(sys: &System): WriteError!void {
    m := Mutex[int].New(0)
    scope s := sys.io.Scope() {
        for _ in 0..4 {
            s.go Add(sys.io, &m)
        }
    }
    total := m.Into() // the value, once no task holds the mutex
    try sys.Print("{total}\n", (total))
}
```

```text
4000
```

- A `Guard` is linear: every path must `Unlock` it (E0402). Write `defer g.Unlock()` after the `Lock`.
- `Lock` can wait, so it takes an `Io` and can fail with `Canceled`.
- `m.Into()` consumes the mutex and gives back its value.
- To wait for a condition under a lock, see [`sync.Notify`](std/sync.md#notify).

A guard that is never unlocked:

```mk
// verdict: reject E0402
Add(io: Io, m: &Mutex[int]) {
    var g := m.Lock(io) else { // <- E0402
        return
    }
    *g.Mut() += 1
}
```

See also: [`Mutex` and `Guard`](std/core.md#mutex-and-guard), [`RwLock`](std/sync.md#rwlock), [atomics](std/atomic.md).

### Cancellation

Cancelling a scope stops the work under it. `s.Cancel()` cancels the scope's body, every task in it and every scope nested inside it. From then on, the cancellable operations under the scope fail, most of them with `Canceled`.

```mk
// verdict: ok
import std.time

// Wait sleeps until its scope is cancelled, and reports how it ended
Wait(io: Io, out: mut str) {
    io.Sleep(time.Seconds(10)) else {
        *out = "canceled"
        return
    }
    *out = "slept"
}

export Main(sys: &System): WriteError!void {
    var a := ""
    var b := ""
    scope s := sys.io.Scope() {
        s.go Wait(sys.io, mut a)
        s.Cancel()
        s.go Wait(sys.io, mut b) // starts anyway, and sees Canceled at once
    }
    try sys.Print("{a} {b}\n", (a, b))

    // a cancelled scope fails an operation even when it would not wait
    m := Mutex[int].New(0)
    var lock := "locked"
    scope s := sys.io.Scope() {
        s.Cancel()
        match m.Lock(sys.io) { // the mutex is free
            ok(g) {
                g.Unlock()
            }
            err(_) {
                lock = "Canceled"
            }
        }
    }
    try sys.Print("{lock}\n", (lock))
}
```

```text
canceled canceled
Canceled
```

- Most operations that can be cancelled have `Canceled` in their error set. Some report a cancel through their own error: a `Reader` or `Writer` fails with `ReadError` or `WriteError`. `s.Expired()` tells whether a deadline caused it. `Canceled` is an ordinary error: handle it or pass it on.
- Under a cancelled scope, such an operation keeps failing until the scope's block exits. It checks before it starts, so a `Lock` of a free mutex, a `Send` with room and a `Recv` with a value buffered all fail and change nothing.
- An operation the cancel finds already done succeeds: a `Recv` handed a value, or a `Send` whose value was taken.
- A task always starts, even under a cancelled scope. It sees `Canceled` at its first cancellable operation. Code that never waits is never interrupted.
- A scope is also cancelled when a task in a `Scope[E]` fails, and when the block exits early ([when a scope ends](#when-a-scope-ends)).

See also: [cancellation in the standard library](std/README.md#6-cancellation). The `std.fs`, `std.net`, `std.proc` and `std.websocket` pages say what a cancel leaves behind, for example [`std.fs`](std/fs.md#cancellation).

### Deadlines

`s.CancelAfter(d)` cancels the scope when the duration `d` has passed, as `s.Cancel()` would. A timeout on any operation is a scope with a deadline around it; no operation takes a timeout argument.

```mk
// verdict: ok
import std.time

Wait(io: Io, out: mut str) {
    io.Sleep(time.Seconds(10)) else {
        *out = "canceled"
        return
    }
    *out = "slept"
}

export Main(sys: &System): WriteError!void {
    var c := ""
    var expired := false
    scope s := sys.io.Scope() {
        s.CancelAfter(time.Millis(20))
        s.go Wait(sys.io, mut c)
        sys.io.Sleep(time.Seconds(10)) else {} // the body is cancelled too
        expired = s.Expired() // the deadline, not Cancel, ended it
    }
    try sys.Print("{c}, expired: {expired}\n", (c, expired))

    var first := false
    var left: i64 = 0
    scope s := sys.io.Scope() {
        first = s.Remaining().IsNone() // no deadline yet
        s.CancelAfter(time.Seconds(60))
        s.CancelAfter(time.Seconds(2)) // the earliest deadline counts
        if r := s.Remaining() {
            left = (r.Nanos() + 999999999) / 1000000000 // whole seconds, rounded up
        }
    }
    try sys.Print("{first} {left}\n", (first, left))
}
```

```text
canceled, expired: true
true 2
```

- Of several deadlines on a scope, the earliest counts. A zero or negative duration cancels at once.
- `s.Expired()` is true when the scope's own deadline fired before anything else cancelled it. A scope cancelled first by `Cancel()`, or by an enclosing scope, never expires.
- `s.Remaining()` is the time left before the nearest deadline of the scope and the scopes around it, or `none`.
- The block's exit disarms the deadline.
- Under a `Scope[E]`, a deadline adds no error of its own. The scope fails only if a task returns an error, such as the `Canceled` the deadline caused.
- For a deadline at an instant `t`, write `s.CancelAfter(t.Since(time.Now(io)))`.

See also: [`std.time`](std/time.md), and the recipe [run tasks with a deadline](cookbook.md#run-tasks-with-a-deadline).

### Masking cancellation: `NoCancel`

`io.NoCancel()` returns an `Io` that ignores the cancellations and deadlines of the scopes around it. Use it for work that must finish, such as cleanup or a final write.

```mk
// verdict: ok
import std.time

Flush(io: Io, out: mut str) {
    io.Sleep(time.Millis(5)) else {
        *out = "canceled"
        return
    }
    *out = "done"
}

export Main(sys: &System): WriteError!void {
    var plain := ""
    var masked := ""
    var bounded := ""
    scope s := sys.io.Scope() {
        s.Cancel()
        Flush(sys.io, mut plain) // cancelled with s
        m := sys.io.NoCancel()
        Flush(m, mut masked) // ignores s
        scope t := m.Scope() { // a new boundary under the mask
            t.CancelAfter(time.Millis(1))
            Flush(m, mut bounded) // t's own deadline applies
        }
    }
    try sys.Print("{plain} {masked} {bounded}\n", (plain, masked, bounded))
}
```

```text
canceled done canceled
```

- A scope opened from the masked `Io` is a new boundary: operations on that `Io` under it are cancelled by its own `Cancel` and deadline, as `t` shows.
- Mask only the operations that must finish. Code that keeps the unmasked `Io` is still cancelled by the enclosing scopes and their deadlines, as `plain` shows ([recipe](cookbook.md#finish-a-write-during-shutdown-with-a-time-limit)).

See also: [masking in the standard library](std/README.md#6-cancellation).

### Limits and stacks

`io.ScopeWith(Limits(tasks: n, stack: size))` opens a scope that runs at most `n` tasks at once, each with a stack of the given size. `s.go` waits while the scope is full. `tasks` must be at least 1, or opening the scope panics.

```mk
// verdict: ok
struct Load {
    live: int
    peak: int
}

Enter(io: Io, m: &Mutex[Load]) {
    var g := m.Lock(io) else {
        return
    }
    defer g.Unlock()
    var l := g.Mut()
    l.live += 1
    if l.live > l.peak {
        l.peak = l.live
    }
}

Leave(io: Io, m: &Mutex[Load]) {
    var g := m.Lock(io) else {
        return
    }
    defer g.Unlock()
    g.Mut().live -= 1
}

Job(io: Io, m: &Mutex[Load]) {
    Enter(io, m)
    io.Yield() else {}
    Leave(io, m)
}

export Main(sys: &System): WriteError!void {
    m := Mutex[Load].New(Load(live: 0, peak: 0))
    l := Limits(tasks: 3, stack: StackSize.Bytes(n: 65536))
    scope s := sys.io.ScopeWith(l) {
        for _ in 0..20 {
            s.go Job(sys.io, &m) // waits while three tasks run
        }
    }
    peak := m.Into().peak
    try sys.Print("at most {peak} at once\n", (peak))
}
```

```text
at most 3 at once
```

| `StackSize` | Stack |
|---|---|
| `StackSize.Default` | 1 MiB, the size under `io.Scope()` |
| `StackSize.Large` | 16 MiB, the size `Main` runs on |
| `StackSize.Bytes(n: n)` | `n` bytes, rounded up to a power of two, at least 64 KiB. Above 512 MiB, opening the scope panics |

- A stack is address space reserved up front. A task uses only the pages it touches. A stack never moves, and never grows past its size.
- `s.go` also waits when the process has no free task slot of that size.
- A task that runs out of stack panics with `stack limit`. So does an `s.go` whose arguments do not fit in half the new task's stack, at the `s.go`.

```mk
// verdict: ok
// Deep recurses n times, with 512 bytes of locals per call
Deep(n: int): int {
    var pad: [512]u8 = [0; 512]
    pad[0] = Cast[u8](n % 200)
    if n == 0 {
        return 0
    }
    return Deep(n - 1) + int(pad[0])
}

Run(n: int, out: mut int) {
    *out = Deep(n)
}

export Main(sys: &System): WriteError!void {
    var v := 0
    small := Limits(tasks: 1, stack: StackSize.Bytes(n: 65536))
    scope s := sys.io.ScopeWith(small) {
        s.go Run(100000, mut v) // about 50 MiB of stack: far past 64 KiB
    }
    try sys.Print("{v}\n", (v))
}
```

```text
panic: stack limit in task 2 at main.mk:3:1
backtrace:
  Deep (main.mk)
  Deep (main.mk)
  ...
```

See also: [carriers, tasks and stacks](std/runtime.md#carriers-tasks-and-stacks).

### Panics in tasks

A panic in a task ends the whole process, with status 101, as a panic in `Main` does. The other tasks stop with it, and no `defer` runs.

```mk
// verdict: ok
Get(xs: []int, i: int, out: mut int) {
    *out = xs[i]
}

export Main(sys: &System): WriteError!void {
    xs := [1, 2, 3]
    var v := 0
    scope s := sys.io.Scope() {
        s.go Get(xs[..], sys.args.Len() + 5, mut v)
    }
    try sys.Print("{v}\n", (v))
}
```

```text
panic: bounds at main.mk:3:12
backtrace:
  Get (main.mk)
```

Errors are for failures a task should report; use a `Scope[E]` for them. See [panics](#panics).

### Compiler errors for concurrency

| Code | Name | Reported for |
|---|---|---|
| E0301 | type-mismatch | [a task's return type](#starting-a-task-sgo), [under `Scope[E]`](#tasks-that-fail-scopeofe) |
| E0402 | not-consumed | [a `Guard` never unlocked](#mutexes) |
| E0502 | write-while-borrowed | [writing what a task borrowed](#starting-a-task-sgo) |
| E0504 | access-while-exclusive | [reading what a task borrowed `mut`](memory.md#r17-tasks) |
| E0604 | store-undeclared | [a helper without `into s`](#helpers-that-start-tasks) |
| E0801 | error-not-in-set | [a task's error outside the scope's set](#tasks-that-fail-scopeofe) |
| E0802 | error-unhandled | [`scope` over a `Scope[E]` without `try`](#tasks-that-fail-scopeofe) |
| E0804 | try-on-non-error | [`try scope` over a plain `Scope`](#tasks-that-fail-scopeofe) |
| E1001 | not-send | [a task argument that is not `Send`](#send-and-share) |
| E1002 | not-share | [a shared reference to a type that is not `Share`](#send-and-share), [a global](memory.md#r16-globals) |
| E1003 | scope-escape | [a `Scope` outside its block](#scopes), [`s.go` in a cleanup](#starting-a-task-sgo) |

See also: [tasks in the memory model](memory.md#r17-tasks), [`Io` and cancellation in the standard library](std/README.md#4-io-and-the-system-handle), [`std.sync`](std/sync.md), [`std.atomic`](std/atomic.md).

## 13. Modules and build

A module is a directory of `.mk` files. A package is a tree of modules with a manifest, `pkg.mk`, at its root. A file names the modules it uses with `import`, and reaches what they export through the last segment of their path.

```text
notes/
    pkg.mk
    src/
        main.mk       module src
        count.mk      module src
        text/
            title.mk  module src.text
```

```mk
// verdict: parse
// src/text/title.mk
export Title(s: str): str { // export: other modules may call it
    return if s.Len() == 0 { Untitled } else { s }
}

const Untitled = "untitled" // private to src.text
```

```mk
// verdict: parse
// src/count.mk: the same module as main.mk, so Main sees Count with no import
Count(): int {
    return Limit
}

const Limit = 3
```

```mk
// verdict: parse
// src/main.mk
import src.text

export Main(sys: &System): WriteError!void {
    t := text.Title("notes")
    n := Count()
    try sys.Print("{t} ({n})\n", (t, n))
}
```

`mk run` in `notes/`:

```text
notes (3)
```

| Form | Meaning |
|---|---|
| `import std.fs` | Use the module `std.fs` in this file, as `fs.X` |
| `import f = std.fs` | The same, under the name `f` |
| `import src.text` | A module of this package, by its directory |
| `import c.sqlite3` | A C import the manifest declares ([section 14](#c-imports)) |
| `export` before an item or a field | Visible outside the module |
| `pkg.Select(os: ..., modules: [...])` | A module whose directory depends on the target ([below](#per-target-modules)) |
| `--target name` | The target to check or build for ([below](#targets)) |

The manifest, dependencies, versions and workspaces are in [packages](packages.md). The `mk` commands and their flags are in [the `mk` command](tools.md).

### Modules and files

Every file of a module sees every item of the module, exported or not, in any order. Files need not import each other, and there are no header files.

- A module is one directory. A subdirectory is another module, with no special access to its parent.
- The files of a module share their items but not their imports. Each file imports what it uses. A name from an import in another file is E0201.
- A file named on the command line, as in `mk run hello.mk` or `mk check src/main.mk`, is checked on its own as the module `main`, even inside a package: the manifest does not apply. It may import std, but not write `unsafe` (E1102). Run `mk check` with no file to check the package.
- A bin's `root` in the manifest names the directory of the module that holds `Main` ([packages](packages.md#2-the-manifest)).

A module holds declarations only: functions, types, errors, interfaces, constants and global variables ([section 4](#4-declarations)), and tests ([section 15](#15-contracts-and-tests)). A `var` at module level is one instance for the whole program; see [global variables](#global-variables).

### Import paths

A module's import path is its directory relative to the package root, with `.` for `/`.

| Directory | Import path |
|---|---|
| `src` | `src` |
| `src/text` | `src.text` |
| `src/os/windows` | `src.os.windows` |
| The standard library's `fs` | `std.fs` |
| The directory `src/x` of the dependency `textutil` | `textutil.src.x` ([packages](packages.md#importing-a-dependency)) |

- The first segments `std`, `c` and `pkg` are reserved. A package directory whose path would start with one forms no module and is E0202, `reason: reserved`.
- Two directories with one import path, such as `src/a.b` and `src/a/b`, are E0206, reported at the later one in byte order.
- A directory that holds its own `pkg.mk` is another package. Nothing under it is a module of this one. The same holds for `vendor/`.
- A module with a segment named `internal` in its path can be imported only from its own package (E0214, [packages](packages.md#10-internal-modules)).
- Some std modules hold the library's internals, and only std imports them (E0202, `reason: std-internal`). The exception is `std.raw` ([who may write unsafe code](#who-may-write-unsafe-code)). The list is in [the module index](std/README.md#8-module-index).

The diagnostic for a reserved directory, from a package with a directory `pkg/`:

```text
-: E0202 unknown-module: directory pkg cannot be a module: its import path pkg begins with pkg, which is reserved for the manifest module; rename the directory
    module: pkg
    reason: reserved
```

### Imports

`import` names one module. It binds the path's last segment in the file, so `import std.db.sqlite` is used as `sqlite.Open`. `import x = path` binds another name.

```mk
// verdict: ok
import std.fs
import t = std.text // used as t.X

Kind(e: fs.Error): str {
    return match e {
        fs.NotFound { "missing" }
        _ { "other" }
    }
}

Width(s: str): int {
    return t.Width(s) // columns s takes in a terminal
}
```

- There are no wildcard imports and no way to bring a module's names into the file unprefixed. `import std.fs.*` is E0101.
- An import nothing in the file uses is E0208.
- Two imports that bind one name are E0206. The diagnostic suggests a rename such as `import std_text = std.text`.
- An import of a path that names no module is E0202, `reason: not-found`.
- The prelude, `std.core`, needs no import ([section 17](#17-module-map)).

```mk
// verdict: reject E0208
import std.fs // <- E0208

Twice(n: int): int {
    return n * 2
}
```

```mk
// verdict: reject E0206
import std.fs
import fs = std.mem // <- E0206

Kind(e: fs.Error): str {
    return "error"
}
```

### Exports and visibility

`export` makes a function, method, type, constant, error, global variable or field visible outside its module. Without it, a free function, method, type, constant, error, global variable or field is private to the module, and the module's files share it.

```mk
// verdict: parse
// src/shape/shape.mk
export struct Point { // every field exported: other modules build and read it
    export x: int
    export y: int
}

export struct Counter { // a private field: only this module builds one
    n: int
}

export Counter.New(): Counter {
    return Counter(n: 0)
}

export Counter.Bump(&self): int {
    return self.n + 1
}
```

```mk
// verdict: parse
// src/main.mk
import src.shape

export Main(sys: &System): WriteError!void {
    p := shape.Point(x: 1, y: 2)
    c := shape.Counter.New() // the module's own constructor
    x := p.x
    b := c.Bump()
    try sys.Print("{x} {b}\n", (x, b))
}
```

```text
1 1
```

- Another module may not name a private free function, type, constant, error or global variable, nor call a private method, as `c.M()`, `Counter.M(&c)`, `Counter.M` as a value or a loop's `Next` (E0204, "is not exported by").
- A struct with a private field cannot be built outside its module (E0204). Give it an exported constructor, as `Counter.New` above. Its private fields cannot be read or written outside either (E0204).
- An exported function, field or type alias may not name a private type (E0412). Export the type, or keep the item private. `Main` is the exception ([errors across modules](#errors-across-modules)).
- An `impl` takes no `export` (E0101). It is visible wherever both its type and its interface are.

From another module, building `Counter` and reading `n`:

```text
src/main2.mk:4:10: E0204 not-exported: `Counter` has private fields and cannot be constructed outside its module
src/main2.mk:6:14: E0204 not-exported: field `n` of `Counter` is private to its module
```

A private type in an exported signature:

```mk
// verdict: reject E0412
struct Inner {
    v: int
}

export Make(): Inner { // <- E0412
    return Inner(v: 1)
}
```

### Exported kinds

A struct's kind follows from its fields ([kinds](#kinds)). Another module cannot see private fields. So an exported struct whose private fields make it linear must say `linear`, one they make interior-writable for loans (such as holding a private `mut view`, or a `Cell`, `Mutex` or `Chan` of a carrier) must say `mut view`, one whose private fields are all `Plain` and `Freeze` carriers must say `plain view` (or `view`), and one they make unique must say `view` (E0410). Its kind is then visible in the declaration.

```mk
// verdict: reject E0410
export struct Window { // <- E0410
    data: []u8 // a slice borrows, so Window is a carrier
}
```

Write `export plain view struct Window`, `export view struct Window`, or export the field. A struct that is both linear and interior-writable writes `export linear mut view struct`.

- A private field of type `std.raw.Atomic` counts as exported for this rule. That is why `Mutex` has the kind [section 3](#3-types-and-kinds) gives it.
- `mk apidiff` reports a type that gains or loses `Send` or `Share` between two versions ([tools](tools.md#6-api-diff)).

### Import cycles

The imports between modules may not form a cycle (E0205). The diagnostic gives the cycle:

```text
src/a/a.mk:1:1: E0205 import-cycle: import cycle: src.a -> src.b -> src.a
    | import src.b
    | ^^^^^^^^^^^^
    cycle: ["src.a","src.b","src.a"]
```

Move what both modules need into a third module that both import, or merge the two. Files within one module never form a cycle: they need no imports of each other.

### Targets

A target is the machine and system a program is built for. `--target name` picks one for `mk check`, `mk build`, `mk run` and `mk test`; the default is the host.

| Target | `mk check` | `mk build` |
|---|---|---|
| `x86_64-windows` | Yes | Yes |
| `x86_64-linux` | Yes | Yes: a static executable, or one that loads the system C library ([std.libc](std/libc.md#system-libc-mode-on-linux)) |
| `x86_64-freestanding` | Yes | Yes: a static ELF that runs with no operating system ([below](#freestanding)) |
| `x86_64-nutshell` | Yes | Yes ([below](#nutshell)) |
| `aarch64-linux` | Yes | Yes: a static executable, or one that loads the system C library ([std.libc](std/libc.md#system-libc-mode-on-linux)) |
| `aarch64-macos`, `x86_64-macos` | Yes | Yes: a Mach-O executable that loads libSystem, macOS 11 or later. Preliminary: never run by the project ([below](#macos-and-aarch64)) |
| `aarch64-nutshell` | Yes | No: E0202, `reason: not-built` |

`Target`, a constant in the prelude, describes the target the code is compiled for ([branching on the target](#branching-on-the-target)). Code that needs one system's own module, such as `std.linux`, goes in a per-target module: on any other target, importing that module is E0202, `reason: target-selected`.

### Per-target modules

`pkg.Select` gives one import name a different directory on each target. The manifest's `targets` field lists the selections.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "sel",
    bins: [pkg.Bin(name: "sel", root: "src", subsystem: "console")],
    targets: [
        pkg.Select(os: "windows", modules: [pkg.Module(name: "os.native", dir: "src/os/windows")]),
        pkg.Select(os: "linux", modules: [pkg.Module(name: "os.native", dir: "src/os/linux")]),
    ],
)
```

```mk
// verdict: parse
// src/os/windows/native.mk; src/os/linux/native.mk returns "linux"
export Name(): str {
    return "windows"
}
```

```mk
// verdict: parse
// src/main.mk
import os.native // the directory the target selects

export Main(sys: &System): WriteError!void {
    n := native.Name()
    try sys.Print("built for {n}\n", (n))
}
```

`mk run` on Windows:

```text
built for windows
```

- A selection's `os` is the last part of a target's name: `windows`, `linux`, `freestanding`, `nutshell` or `macos`.
- A directory that a selection names is a module only on its own target, and only under the selection's name.
- On a target that no selection covers, the import is E0202, `reason: not-found`. A package that builds for a target must select a directory for it.
- Each target is checked separately. Run `mk check --target x86_64-linux` to inspect a specific target, or `mk check --all-targets` to check every declared OS and verify exported `pkg.Select` API parity (E0213). Private helpers, private fields, type sizes, and constant values may differ per OS.
- In `unsafe_modules`, name the selected directory, `"src/os/windows"`, not the import name ([packages](packages.md#2-the-manifest)).

### Freestanding

`--target x86_64-freestanding` builds a static ELF executable that runs with no operating system: a kernel, a boot loader, firmware.

```mk
// verdict: ok
// src/main.mk of a freestanding bin. The entry is exported as MkEntry.
export extern MkEntry(): never {
    var n := 0
    for i in 0..10 {
        n += i
    }
    Assert(n == 45) // a failed check calls MkPanicHook if the program defines one, else traps
    for {} // the entry never returns
}
```

In a package whose bin is named `kernel`, `mk build --target x86_64-freestanding` writes the static executable `kernel`.

- There is no `Main` and no `System`. The entry is the function the bin exports as `export extern MkEntry`.
- `Target.os` is `Os.Freestanding`.
- A program may import std's layer 0, and layer 1 once it supplies the page hooks. It may also import `std.free`, the generator modules `derive` and `dyn`, with `std.dyn.erased`, and `std.raw` from a module listed in `unsafe_modules`. Any other std module is E0202, `reason: freestanding`. The layers are in [the library overview](std/README.md#1-layers).
- `std.free` decides what a panic does and supplies the one `Heap`, from pages the program hands it ([std.free](std/free.md)).
- No function uses the red zone below the stack pointer.
- `pkg.Bin`'s `link_script` names a linker script, package-relative. On any other target it is `""`.
- `pkg.Bin`'s `cpu` is `"general-regs-only"` by default ([general registers only](#general-registers-only)). On a hosted target it is `""` or `"baseline"`. Any other value is E0202 at the `Bin`, `reason: invalid`.

An import the target does not allow:

```text
src/f.mk:1:8: E0202 unknown-module: module std.fs is not available on x86_64-freestanding, which has std's layers 0 and 1, std.free, derive and dyn only
    module: std.fs
    reason: freestanding
```

### Nutshell

`--target x86_64-nutshell` builds for Nutshell, an operating system developed outside this repository, which supplies its own system layer through `MK_STD`. It is a hosted target, with `Main` and `System`. The target is Experimental ([stability](std/README.md#9-stability)).

- `Target.os` is `Os.Nutshell`, and std's system layer is the directory that `pkg.Select(os: "nutshell", ...)` names.
- The system layer that comes with Mk answers every system call with failure. Nutshell supplies its own, through the `MK_STD` environment variable.
- The executable is a static ELF of type `ET_EXEC` at a fixed address, with no dynamic section and no RELRO. Its loadable segments are page-aligned in the file and in memory, and none is both writable and executable.
- The CPU baseline, the red zone and the C ABI are those of `x86_64-linux`.
- `aarch64-nutshell` can be checked but not built.

### macOS and AArch64

`aarch64-linux`, `aarch64-macos` and `x86_64-macos` are built by the same compiler as the x86-64 targets, cross-compiled from any host. The project has no Mac and no ARM machine, so their status is stated per target:

| Target | What the project checks | Not yet |
|---|---|---|
| `aarch64-linux` | CI builds and runs the checks, std's test blocks, `tests/programs`, system-libc mode and libraries (`pkg.Lib`, called from a C host) natively on GitHub's `ubuntu-24.04-arm` runner; the programs also pass under qemu-user | `std.linux` (the raw system call) is x86_64-only, since its numbers are x86_64's |
| `aarch64-macos`, `x86_64-macos` | `mk check` of std, its tests included, and `mk corpus` for the target; `mk build` links every program of `tests/programs` but the x86- and Linux-specific ones. A small program's load commands, imports and reproducible bytes are checked by a test. None has run | Libraries, `pkg.Link`, `c_sources` and C imports; source-level debugging (no `.dSYM` is made); the parts of `std.sys` listed in [std.sys on macOS](#stdsys-on-macos) |

- The C ABI is AAPCS64 on `aarch64-linux`, Apple's arm64 variant of it on `aarch64-macos` (the caller extends small integers, and every variadic argument goes on the stack), and System V on `x86_64-macos`.
- A macOS executable loads `/usr/lib/libSystem.B.dylib`, the only stable interface to the macOS kernel, and nothing else. mk writes the stub of it that the link reads, so no macOS SDK is needed. dyld calls the program's `main` (`LC_MAIN`). `ld64.lld` signs arm64 executables ad hoc, which arm64 macOS requires.
- Pages are 16 KiB on arm64 macOS. A task's stack guard is one page there, so a stack overflow is detected 16 KiB before the slot's base rather than 4 KiB.
- `asm` on aarch64 takes the register classes `reg` (x0 to x30) and `vreg` (v0 to v31), the explicit registers `x0`, `w0`, `v0` and their numbered kin, and the template modifiers `w`, `x`, `b`, `h`, `s`, `d` and `q`. x18, x19, x29 and sp are reserved (E1104, `reason: reserved`).
- `aarch64-windows` is not a target: its system layer, import libraries and stack probes are not built.

#### std.sys on macOS

std's system layer for macOS calls libSystem. Arguments, the environment, memory, threads, clocks, random bytes, files, directories, sockets (TCP, UDP and Unix), the kqueue poller, child processes (`posix_spawn`), pipes, ptys and the terminal, signals, faults, descriptor passing and `std.dl` are implemented. What differs from Linux:

- `std.vm` code regions (`CodeRegion.New`, `CodeRegion.Reserve`, `CodeRegion.Edit`) work on both macOS architectures: on arm64 through a `MAP_JIT` mapping whose per-thread write protection switches via `pthread_jit_write_protect_np` during `CodeRegion.Edit` when the host supports it (requiring the `com.apple.security.cs.allow-jit` entitlement under the Hardened Runtime), falling back to a private anonymous mapping toggled between read-write and read-execute via `mprotect` (as on x86_64) when per-thread JIT write protection is unavailable (such as virtualized macOS guests) or `MAP_JIT` is refused by policy. Fault filters (`runtime.AddFaultFilter`) work as on Linux.
- A `proc.Pipe` end is a blocking end served by the thread pool, not one on the poller.
- Byte-range locks (`LockRange`) are POSIX record locks: closing any descriptor of a file drops the process's locks on it.
- A saved terminal mode is 64 bytes, not 36.
- A child started on a `Pty` (`Command.terminal`) gets it as its controlling terminal only when it is an Mk program: `posix_spawn` makes the child a session leader after it opens the terminal, and only the child's own `TIOCSCTTY` makes it controlling, which an Mk program's start does. Another program started on a `Pty` has no controlling terminal.
- A child that holds a `Pty` as its controlling terminal does not end until its output has been read, so read a `Pty` while you wait for its children. Output that a child without one leaves unread is discarded shortly after it exits. A child's terminal mode starts from the defaults each time a new child first opens the `Pty`; its size is kept.
- A child inherits the raised descriptor limit: `posix_spawn` runs nothing in the child that could restore the original one.
- A Unix-socket dial to a listener whose backlog is full is `SysErr.Refused`, not a wait. A send buffer (`SocketOption`) must be at least 1 byte.
- `runtime.WriteDump` and the platform certificate verifier answer `SysErr.Unsupported`, as on Linux; `std.x509` checks chains itself.

All of it is checked and linked here, and none of it has run on a Mac yet.

### Compiler errors for modules

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | A wildcard import, `export impl` ([imports](#imports), [exports](#exports-and-visibility)) |
| E0201 | unknown-name | A name from another file's import, or one a per-target module lacks ([files](#modules-and-files), [per-target](#per-target-modules)) |
| E0202 | unknown-module | An import of no module, a reserved directory, a std-internal module, a module the target lacks, a bad `pkg.Bin` `cpu` or `link_script` ([import paths](#import-paths), [freestanding](#freestanding)) |
| E0204 | not-exported | A private free function, type, constant, error, global or field, or a struct with private fields built outside its module ([exports](#exports-and-visibility)) |
| E0205 | import-cycle | Imports that form a cycle ([cycles](#import-cycles)) |
| E0206 | duplicate-name | Two imports binding one name, two directories with one import path ([imports](#imports), [import paths](#import-paths)) |
| E0208 | unused | An import the file never uses ([imports](#imports)) |
| E0214 | internal-module | An `internal` module imported from another package ([packages](packages.md#10-internal-modules)) |
| E0410 | must-declare-kind | An exported struct whose private fields need `view`, `mut view` or `linear` ([exported kinds](#exported-kinds)) |
| E0412 | private-in-signature | A private type in an exported item ([exports](#exports-and-visibility)) |

Run `mk explain E0205` for any code. The package errors, E0202 with its `reason` facts and E0214 to E0217, are listed in [packages](packages.md#17-package-errors).

See also: [packages](packages.md) for manifests and dependencies, [organising a codebase](codebase.md) for how to split a program into modules, [the `mk` command](tools.md) for `--target` and the other flags.

## 14. Unsafe and C

Some code does what the compiler cannot check: it follows raw pointers, calls C, runs assembly or touches hardware. That code goes in an `unsafe` block, whose reason says why it is correct.

```mk
// verdict: ok
import std.raw

// Sum adds the n ints that start at p.
Sum(p: *int, n: int): int {
    var s := 0
    for i in 0..n {
        unsafe "the caller passes n ints at p" {
            s += raw.Read(raw.Offset(p, i))
        }
    }
    return s
}

// Clear sets the n ints at p to zero.
Clear(p: *int, n: int) {
    for i in 0..n {
        unsafe "the caller passes n ints at p, and no loan covers them" {
            *raw.Offset(p, i) = 0
        }
    }
}

export Main(sys: &System): WriteError!void {
    var xs := [1, 2, 3, 4]
    a := Sum(raw.Data(xs[..]), xs.Len()) // taking an address is safe
    Clear(raw.DataMut(mut xs[..]), 2)
    b := Sum(raw.Data(xs[..]), xs.Len())
    try sys.Print("{a} {b}\n", (a, b))
}
```

```text
10 7
```

A module may write `unsafe` only if its package's manifest lists it in `unsafe_modules`. Every program in this section that uses `unsafe` runs from such a package, with `unsafe_modules: ["src"]` in its `pkg.mk`.

| Form | Meaning |
|---|---|
| `unsafe "reason" { ... }` | Run operations the compiler cannot check |
| `*T`, `*p` | A raw pointer, and the place it points at |
| `unsafe impl Send for T {}` | Assert a bound the compiler cannot prove |
| `extern F(x: i32): i32` | Declare a C function |
| `export extern F(x: i32): i32 { ... }` | Define a function C can call by its name |
| `import c.name` | Use the C headers the manifest's `pkg.CImport` names |
| `F(...): T cpu "avx2" { ... }` | Compile a body for CPU features the target may lack |
| `asm("template", operands)` | Inline assembly |
| `naked F() { ... }` | A function with no prologue or epilogue |
| `section "name"` | Place an item in a linker section |
| `extern var X: T`, `export extern var X: T` | A symbol that the linker or this program defines |

### `unsafe` blocks

`unsafe "reason" { ... }` is a statement. Inside it, the operations below are allowed; outside it, each is a compile error.

| Operation | Outside `unsafe` |
|---|---|
| Reading or writing through a raw pointer, `*p` | E1103 |
| Calling a bodiless `extern` function | E1101 |
| Calling or naming a `cpu` function whose features the caller lacks ([CPU features](#cpu-features)) | E1101 |
| Calling or naming a `naked` function | E1101 |
| `asm` | E1101 |
| Reading an `extern var` or taking its address | E1101 |
| Reading a function pointer out of a C import's struct | E1101 |
| Borrowing a field of a packed struct that sits below its type's alignment, `&w.len` ([layout](#layout)) | E1101 |
| The unsafe functions of `std.raw` ([below](#raw-pointers)), `str.FromC`, `fs.Map`, every function of `std.mmio`, `std.vm` and `std.linux`, and a few low-level runtime entries ([the E1101 entry](errors.md) lists them) | E1101 |

```mk
// verdict: reject E1103
First(p: *u8): u8 {
    return *p // <- E1103
}
```

```mk
// verdict: reject E1101
Name(p: *u8): str {
    return str.FromC(p) // <- E1101
}
```

- The reason is a string literal and may not be empty or only white space: `unsafe { }`, `unsafe "" { }` and `unsafe " " { }` are E0101. Write what makes the code correct, such as which check guards it or who owns the memory.
- `unsafe` is a statement, not an expression. To compute a value, assign to a `var` declared before the block, or `return` from inside it.
- An `unsafe` block may hold any code. Keep it to the lines that need it.
- `unsafe` does not turn off the other rules. Types, moves and loans are checked inside it as everywhere else.
- Asserting `Send` or `Share` is not an operation of an `unsafe` block: it needs `unsafe impl` ([below](#unsafe-impl)).

What `unsafe` code may assume and must keep is in [the memory model, R19](memory.md#r19-unsafe).

### Who may write unsafe code

A module may write `unsafe`, `unsafe impl`, `section`, `extern var` and an `ends` primitive ([R18](memory.md#r18-view-and-ends)) only if the manifest lists its directory in `unsafe_modules` (E1102). Only such a module may import `std.raw` (E0202, `reason: std-internal`).

```mk
// verdict: parse
const Package = pkg.Package(
    name: "cdemo",
    bins: [pkg.Bin(name: "cdemo", root: "src", subsystem: "console")],
    unsafe_modules: ["src"], // "." for the module at the package root
)
```

Without the `unsafe_modules` line, `mk check` in the package reports:

```text
src/main.mk:6:5: E1102 unsafe-not-permitted: an unsafe block in module src, which pkg.mk's unsafe_modules does not list
    module: src
    manifest: unsafe_modules
```

- A file named on the command line, `mk run hello.mk` or `mk check src/main.mk`, is checked without the manifest, so it can never write `unsafe` ([modules and files](#modules-and-files)).
- A package fetched from elsewhere can use its own `unsafe_modules`, C sources, C imports and links only when your manifest grants it in `unsafe_deps` (E0215, [packages](packages.md#11-grants-for-unsafe-code)).
- `mk query perimeter` lists them ([grants](packages.md#11-grants-for-unsafe-code)).

### Raw pointers

`*T` is an address with no checks ([raw pointer types](#raw-pointer-types-t)). Making, copying and passing one is safe. `*p` reads or writes the `T` at `p`, inside `unsafe` only. A raw pointer has no arithmetic, `p + 1` is E0301, and `std.raw` provides the operations.

```mk
// verdict: ok
import std.raw

export Main(sys: &System): WriteError!void {
    var xs := [10, 20, 30]
    p := raw.DataMut(mut xs[..]) // the address of the first element
    var second := 0
    unsafe "p points at three ints that xs owns" {
        q := raw.Offset(p, 1) // one element further on
        second = *q
        raw.Write(raw.Offset(p, 2), 99)
    }
    last := xs[2]
    try sys.Print("{second} {last}\n", (second, last))
}
```

```text
20 99
```

```mk
// verdict: reject E0301
import std.raw

Next(p: *u8): *u8 {
    unsafe "the caller passes a pointer into a buffer with a byte after it" {
        return p + 1 // <- E0301
    }
}

Second(p: *u8): *u8 {
    unsafe "the caller passes a pointer into a buffer with a byte after it" {
        return raw.Offset(p, 1)
    }
}
```

`std.raw` holds the operations on raw pointers. `raw.Of(&x)`, `raw.OfMut(mut x)`, `raw.Data(s)` and `raw.DataMut(s)` take an address, and are safe. `raw.Offset(p, n)` moves `p` by `n` elements, `raw.Read(p)` and `raw.Write(p, x)` go through it, and `raw.Slice(p, n)` and `raw.Str(p, n)` make a view. These need `unsafe` (E1101). [Safe and unsafe functions](std/raw.md#safe-and-unsafe-functions) lists every function, and [its page](std/raw.md) gives their costs and examples.

### `unsafe impl`

A type is `Send` or `Share` when its fields are ([kinds](#kinds)). A raw pointer is neither, so a struct that holds one is neither. When the code around the pointer makes sharing or sending correct, `unsafe impl` asserts the bound.

```mk
// verdict: ok
import std.raw

// Buf points into memory the program set aside for it.
struct Buf {
    base: *u8
    len: int
}

// Only the task that holds a Buf touches its bytes, so it may move.
unsafe impl Send for Buf {}

// The generic form: Wrap[T] is Send when T is.
struct Wrap[T] {
    p: *T
}

unsafe impl[T: Send] Send for Wrap[T] {}

Sendable[T: Send](x: &T): bool {
    return true
}

Check(): bool {
    b := Buf(base: raw.Null[u8](), len: 0)
    w := Wrap[int](p: raw.Null[int]())
    return Sendable(&b) && Sendable(&w)
}
```

- Without `unsafe`, `impl Send for Buf {}` is E1101. Without the `impl`, `Sendable(&b)` is E0701.
- The standard library's `Mutex[T]` is `Share` this way, as `unsafe impl[T: Send] Share for Mutex[T]`.

```mk
// verdict: reject E1101
struct Buf {
    base: *u8
    len: int
}

impl Send for Buf {} // <- E1101
```

### `view` structs and `ends self`

A struct built on raw pointers has no reference field to show that it borrows. `plain view struct` declares a copyable, `Freeze` carrier handle whose fields are all `Plain` and `Freeze`; `view struct` declares a unique carrier whose `&self` methods do not store loans behind raw pointers; `mut view struct` declares an interior-writable view whose `&self` methods do, so the borrow rules treat a shared step into it as writable and codegen marks it not `Freeze`. `ends self` on a release function, such as `Arena.Reset`, ends every loan on `self`. Both are rules of [the memory model, R18](memory.md#r18-view-and-ends), which shows a `view struct Iter[T]` over two raw pointers.

### Undefined behaviour

These eight things are undefined behaviour: the program may do anything, and no later check catches them. Safe code alone cannot cause them: each needs `unsafe` code, C, `asm` or another process.

1. Writing through a raw pointer, or from `asm`, to a place covered by a live loan, or to a by-value parameter that is not `var`.
2. Using a reference that a raw pointer or C made where the loan rules forbid a checked one. An example is two references to a common byte in one call, one of them `mut`.
3. Building, through a raw pointer, a slice or object larger than 2^56 bytes.
4. Producing an invalid `bool`, `char` or enum value, or a `Mask` lane that is neither all zeros nor all ones. This can happen through `extern`, `asm`, a raw read or memory that C wrote.
5. Running a `cpu` function on a CPU without its features.
6. Any process changing the bytes of a live `fs.Mapping`.
7. Mixed-width atomic accesses to overlapping bytes.
8. Running Mk code under a floating-point environment other than round-to-nearest-even without flush-to-zero or denormals-are-zero.

Two related facts:

- A write through a raw pointer to memory inside a `Cell`, an atomic, a `Mutex`'s value, a `Chan`'s state, or a `mut view`, while no exclusive loan covers it, is defined.
- A `const` or non-`var` by-value parameter has no address identity. Whether two `raw.Of` of it, or of a parameter and its argument, are equal is unspecified.

See also: [the memory model, R19](memory.md#r19-unsafe), which states each duty in full.

### Calling C: `extern`

`extern F(...)` with no body declares a function that C code defines. Calling it needs `unsafe`. The C code comes from a C source of the package, a library the manifest links, or the system.

`c/sum.c`, listed in the manifest as `c_sources: [pkg.CSource(path: "c/sum.c")]`:

```c
#include <stdint.h>
#include <string.h>

int64_t sum(const int64_t *xs, int64_t n) {
    int64_t s = 0;
    for (int64_t i = 0; i < n; i++) s += xs[i];
    return s;
}

int64_t apply(int64_t (*f)(int64_t), int64_t x) {
    return f(x);
}

size_t length(const char *s) {
    return strlen(s);
}
```

```mk
// verdict: ok
import std.raw

extern sum(xs: *int, n: int): int
extern apply(f: fn(int): int, x: int): int
extern length(s: *u8): uint

// Square is an ordinary Mk function. C calls it through a fn value.
Square(x: int): int {
    return x * x
}

export Main(sys: &System): WriteError!void {
    xs := [1, 2, 3, 4]
    name := "mk\x00" // C wants a NUL at the end
    var s := 0
    var q := 0
    var n: uint = 0
    unsafe "sum reads n ints at xs; apply calls Square once; name ends in a NUL" {
        s = sum(raw.Data(xs[..]), xs.Len())
        q = apply(Square, 7)
        n = length(raw.StrData(name))
    }
    try sys.Print("{s} {q} {n}\n", (s, q, n))
}
```

```text
10 49 2
```

- An `extern` function keeps its C name, which is its link symbol. It is exempt from Mk's casing rules.
- C reaches a Mk function only through its C ABI: an `extern` or `export extern` function, a function used as a value (passed, stored in a struct, a table or a `dyn` value), one an `asm` block names as a `sym` operand, one a `become` calls or ends in, a `naked` function and one with a `section` clause (boot code calls them by name). Any non-generic function can be passed to C as a callback, as `Square` is above. Every other function, `export` ones included, may be called another way: on `x86_64-windows`, outside `--debug`, it takes and returns a value of 9 to 16 bytes made of integers and pointers, such as a slice, a `str` or a `?int`, in two registers. Such a function's symbol cannot be called by name from C or assembly; pass it as a value instead.
- Write the C types exactly. Mk's `int` is 64 bits; C's `int` is 32 bits, and so is C's `long` on Windows. A wrong width reads the wrong bytes with no error. The table below gives the matches.
- A variadic C function is declared with `...`: `extern printf(fmt: *u8, ...): i32`. Pass each variadic argument at the C type the callee reads, such as `i32(1)`.
- A C string comes back as a Mk `str` through `str.FromC(p)`, inside `unsafe`. It panics if the bytes before the NUL are not UTF-8.
- A vector, `simd.Vec` or `simd.Mask`, or an aggregate holding one, cannot be a parameter or the result of an `extern` or `export extern` function (E1107). Its place in the C calling convention would depend on the callee's CPU features. Pass a pointer or an array.

| C type | Mk type |
|---|---|
| `int8_t`, `int16_t`, `int32_t`, `int64_t` | `i8`, `i16`, `i32`, `i64` |
| `uint8_t` ... `uint64_t`, `unsigned char` | `u8` ... `u64`, `u8` |
| `char`, `signed char` | `u8`, `i8` |
| `int` | `i32` |
| `long` | `i32` on Windows, `i64` on Linux |
| `long long`, `intptr_t`, `ptrdiff_t` | `i64`, `int`, `int` |
| `size_t`, `uintptr_t` | `uint` |
| `float`, `double` | `f32`, `f64` |
| `_Bool`, `bool` | `bool` |
| `T *`, `const T *`, `void *` | `*T`, `*T`, `*void` |
| A pointer to a function | `fn(...)`; in a C import, `?fn(...)` for a pointer that may be null |
| A struct | A struct with the same fields in the same order: every Mk struct has C layout ([layout](#layout)) |

```mk
// verdict: reject E1107
import std.simd

extern take_vec(v: simd.Vec[f32, 4]): f32 // <- E1107
```

**Linking.** An `extern` function is defined by a C source of the package, or by a library. A name that std does not define itself resolves to the first library that exports it:

1. the links of the bin's packages, in manifest order, the root package's first ([linking libraries](#linking-libraries));
2. when building for `x86_64-windows` on a Windows host, the system DLLs `kernel32`, `user32`, `gdi32`, `winspool`, `comdlg32`, `advapi32`, `shell32`, `ole32`, `oleaut32` and `ws2_32`.

A name nothing defines is E0001 at link, "undefined symbol". Built on another host, a system DLL's function needs a `pkg.Link` with a `.def` file. On a Windows host, it needs no manifest entry:

```mk
// verdict: ok
extern GetCurrentProcessId(): u32 // kernel32.dll

export Main(sys: &System): WriteError!void {
    var pid: u32 = 0
    unsafe "GetCurrentProcessId takes nothing and cannot fail" {
        pid = GetCurrentProcessId()
    }
    ok := pid > 0
    try sys.Print("pid > 0: {ok}\n", (ok))
}
```

```text
pid > 0: true
```

### Functions C can call: `export extern`

`export extern F(...) { ... }` defines a function whose symbol is its own name, `F`, so C code can call it.

```mk
// verdict: ok
export extern Twice(n: i32): i32 {
    return n * 2
}

export Main(sys: &System): WriteError!void {
    t := Twice(21) // Mk calls it as any function, with no unsafe
    try sys.Print("{t}\n", (t))
}
```

```text
42
```

- C may call it from any thread, also one the Mk runtime did not start. The call then runs as a task, so it may wait and do I/O ([calls from C](std/runtime.md#calls-from-c)).
- An `export extern` function of a library bin is exported from the library, and its types must have a C form (E1109, [libraries](packages.md#15-libraries)).
- A freestanding program's entry and hooks are `export extern` functions ([freestanding](#freestanding)).

**Unwinding.** Nothing in Mk unwinds, so nothing unwinds across C. A panic in a function C called aborts the process.

**The C stack.** A call of a bodiless `extern` function runs on the operating system thread's own stack, 8 MiB, not the task's. A call through a `fn` value does not: a C function's address and a Mk function's look the same there, so C code reached that way (a symbol from [`std.dl`](std/dl.md), a function pointer in a C import's struct, a COM method) runs on the calling task's stack. Give that task the stack the C code needs, `StackSize.Large` through `io.ScopeWith` for a library such as Direct3D ([limits and stacks](#limits-and-stacks)), or reach the C function through an `extern`. While C frames are live, the task stays on its thread. A C call that blocks also blocks the thread; [`runtime.Blocking`](std/runtime.md#blocking-foreign-calls) runs one off the threads that run tasks.

### C imports

`import c.name` translates C headers into Mk declarations. The manifest's `pkg.CImport` names the headers, and mk runs Clang on them.

`c/geom.h`:

```text
#include <stdint.h>

#define GEOM_VERSION 3
#define GEOM_MAX(a, b) ((a) > (b) ? (a) : (b))

typedef struct geom_point {
    int32_t x;
    int32_t y;
} geom_point;

typedef enum geom_kind { GEOM_KIND_LINE = 1, GEOM_KIND_BOX = 2 } geom_kind;

int32_t geom_dist2(geom_point a, geom_point b);
static inline int32_t geom_twice(int32_t v) { return v * 2; }
```

`c/geom.c`:

```text
#include "c/geom.h"

int32_t geom_dist2(geom_point a, geom_point b) {
    int32_t dx = b.x - a.x, dy = b.y - a.y;
    return dx * dx + dy * dy;
}
```

```mk
// verdict: parse
const Package = pkg.Package(
    name: "cdemo",
    bins: [pkg.Bin(name: "cdemo", root: "src", subsystem: "console")],
    c_sources: [pkg.CSource(path: "c/geom.c", flags: [])], // defines geom_dist2
    c_imports: [pkg.CImport(name: "geom", headers: ["c/geom.h"], defines: [], macros: ["int GEOM_MAX(int a, int b)"], vendor: false)],
    unsafe_modules: ["src"],
)
```

```mk
// verdict: parse
import c.geom

export Main(sys: &System): WriteError!void {
    a := geom.GeomPoint(x: 1, y: 2) // struct geom_point
    b := geom.GeomPoint(x: 4, y: 6)
    var d: i32 = 0
    var t: i32 = 0
    var m: i32 = 0
    unsafe "geom's functions take and return plain values" {
        d = geom.geom_dist2(a, b)
        t = geom.geom_twice(21) // a static inline function, through a shim
        m = geom.GEOM_MAX(3, 9) // a function-like macro, declared in pkg.mk
    }
    v := geom.GeomVersion // a #define constant
    k := geom.GeomKindBox // an enum constant
    try sys.Print("{d} {t} {m} {v} {k}\n", (d, t, m, v, k))
}
```

```text
25 42 9 3 2
```

How C declarations appear in Mk:

| C | Mk |
|---|---|
| A function | `export extern`, with its C name: `geom.geom_dist2` |
| `struct geom_point` | `struct GeomPoint`, its fields in snake_case: `maxCount` is `max_count` |
| `enum geom_kind` and its constants | `type GeomKind = i32` and `const GeomKindLine: GeomKind = 1` |
| `union num` | A struct of bytes with a getter and a setter per member: `Num.I`, `Num.SetI` |
| An incomplete `struct opaque` | An empty `struct Opaque`, used through `*Opaque` |
| A typedef | A type alias: `typedef int32_t` is `Int32T = i32` |
| `#define NAME 3` | `const Name: i32 = 3` |
| A `static inline` function | An `extern` that a generated shim defines in the same build |
| A function-like macro | Bound only when `macros` declares its prototype, also through a shim |
| A function-pointer field | `?fn(...)`. Reading it needs `unsafe` (E1101): calling it runs C code |

- Types and constants become PascalCase, and fields snake_case. Functions keep their C names. Each declaration's C form is in a comment above it.
- A struct is made by naming every field, or zeroed whole as C's `= {0}` does with [`mem.Zeroed`](#zero-valid-types): `mem.Zeroed[geom.GeomPoint]()`. A C struct's fields (integers, floats, raw pointers, `?fn`, arrays, nested structs, a union's bytes, bitfield units) are all zero-valid, so it needs no `unsafe`.
- `mk cimport` prints, for each import, how many declarations it bound and which it skipped and why. It writes the translation to `gen/<target>/c/<name>.mk`, which you can read; never edit it.
- `headers` are paths from the package root. `defines` are macros the headers are read with, `NAME` or `NAME=value`.
- `include_dirs` are directories of the package searched for headers after the package root, for a library whose headers include each other by a name under their own include directory: `pkg.CImport(name: "z", headers: ["third_party/z/include/z.h"], defines: [], macros: [], vendor: false, include_dirs: ["third_party/z/include"])`, where `z.h` has `#include "zconf.h"`. Each is a relative path with `/` separators inside the package, other than the root itself (E0202 otherwise). The field may be left out.
- All the C imports of a package share one translation unit. A dependency's C imports form its own, and only it can import them. So their `include_dirs` form one search path, in the order the imports list them, and the shim and the package's C sources are compiled with the same path.
- `mk check` runs Clang to translate the C imports, with a minimal environment. It compiles no C source.
- The bindings are made per target. `mk check --all-targets` translates them for both `x86_64-windows` and `x86_64-linux` and reports an item the package uses whose API differs (E0213). A constant's value is not part of the API. It needs each target's system headers, from `MK_SYSROOT_<target>`, or `vendor: true` to read the committed `gen/<target>/c/` files instead. Without either, it is E0213, `reason: no-sysroot`.

### C sources and assembly files

The manifest builds C and assembly files with the package:

| Field | Entry | Meaning |
|---|---|---|
| `c_sources` | `pkg.CSource(path: "c/sum.c", flags: ["-O2"])` | A C file compiled by Clang for the target, with these flags |
| `asm_sources` | `pkg.AsmSource(path: "src/boot.s", target: "x86_64-freestanding")` | An assembly file, `.s`, or `.S` through the preprocessor, for one target or `""` for every target. Its symbols are `extern` |
| `links` | `pkg.Link(...)` | A library ([below](#linking-libraries)) |

- C sources link against `std.libc`, the C library written in Mk, and the bin's links. They get no other C runtime. Without a sysroot, only `std.libc`'s functions are declared in the headers they see: a call to anything else fails to compile, or to link, with E0001. [std.libc](std/libc.md) lists the functions, the include path and the system-libc mode of Linux.
- A C source is compiled with the package root on its include path, so `#include "c/geom.h"` works, and then the C imports' `include_dirs`.
- A relative `-I` in `flags` names a directory under the build's own directory, not the package's. Include headers by their path from the package root, or list their directory in a C import's `include_dirs`, instead.

### Linking libraries

`pkg.Link` names one library for one target.

```mk
// verdict: parse
const Package = pkg.Package(
    name: "clock",
    bins: [pkg.Bin(name: "clock", root: "src", subsystem: "console")],
    unsafe_modules: ["src"],
    links: [pkg.Link(name: "winmm.dll", kind: "dynamic", path: "", target: "x86_64-windows")],
)
```

| Field | Values |
|---|---|
| `name` | A dynamic library's exact file name, as the loader looks it up, with no separator. A static library's name labels it in diagnostics |
| `kind` | `"dynamic"`: the OS loader opens it when the program starts. `"static"`: an archive linked into the executable |
| `path` | Package-relative. A Windows DLL: `""` for the host's System32 copy (on a Windows host only), a `.def` file listing one exported name per line (on any host), or the `.dll` itself, which mk copies beside the executable. A Linux library: `""`, or a `.def` whose names may carry `@VERSION`. An archive: the `.lib` or `.a` file |
| `target` | `"x86_64-windows"`, `"x86_64-linux"` or `"aarch64-linux"` |

- A fault in a `Link` is E0202 at the `Link`, `reason: link`. An import library (`.lib` for a DLL) is not an archive (E0202).
- A name an archive defines wins over any DLL. A library enters the executable only when a name it supplies is used.
- Only functions resolve through links. An `extern var` in a DLL or a shared object is E0001.
- Windows looks for a linked DLL only in KnownDLLs, the executable's directory and System32, never in the current directory or `PATH`. Ship a third-party DLL beside the executable. There is no delay-load.
- An archive must be built with no C runtime: `-ffreestanding -fno-stack-protector` for Clang or GCC, `/GS- /Zl` for MSVC. A symbol it needs that nothing supplies is E0001, naming the member and the symbol.
- An archive member that holds static constructors or thread-local storage is E0001: a Mk program never sets them up.
- A Linux bin with a dynamic link loads the system C library ([system-libc mode](std/libc.md#system-libc-mode-on-linux)).
- To open a library while the program runs, use [std.dl](std/dl.md).

### CPU features

`cpu "features"` after a signature compiles that function for CPU features the target's baseline lacks. Calling it needs `unsafe`, with a reason that names the run-time test guarding the call.

```mk
// verdict: ok
import std.simd

// Sum8 adds eight floats at a time. Its body is compiled for AVX2.
Sum8(xs: []f32): f32 cpu "avx2" {
    var acc := simd.Vec[f32, 8].Splat(0.0)
    var i := 0
    for i + 8 <= xs.Len() {
        acc = acc + simd.Vec[f32, 8].FromSlice(xs, i)
        i += 8
    }
    var s := acc.ReduceSum()
    for i < xs.Len() {
        s += xs[i]
        i += 1
    }
    return s
}

// SumAny adds one float at a time, on any x86_64 CPU.
SumAny(xs: []f32): f32 {
    var s: f32 = 0.0
    for x in xs {
        s += x
    }
    return s
}

Sum(xs: []f32): f32 {
    if simd.Cpu().Has("avx2") {
        unsafe "Cpu() reports avx2, which Sum8 needs" {
            return Sum8(xs)
        }
    }
    return SumAny(xs)
}

export Main(sys: &System): WriteError!void {
    var xs: [20]f32 = [0.0; 20]
    for i in 0..20 {
        xs[i] = Cast[f32](i)
    }
    s := Sum(xs[..])
    try sys.Print("{s}\n", (s))
}
```

```text
190
```

The `cpu` clause comes after any `requires` and `ensures` lines, and before a `section` clause (E0101 otherwise). The feature names are comma-separated, `cpu "avx2,fma"`:

| Architecture | Names |
|---|---|
| x86_64 | `sse`, `sse2`, `sse3`, `ssse3`, `sse4.1`, `sse4.2`, `popcnt`, `cx16`, `sahf`, `avx`, `avx2`, `bmi`, `bmi2`, `f16c`, `fma`, `lzcnt`, `movbe`, `xsave`, `pclmul`, `aes`, `sha`, `gfni`, `vpclmulqdq`, `avx512f`, `avx512bw`, `avx512cd`, `avx512dq`, `avx512vl`, `avx512vbmi` |
| x86_64 levels | `x86-64-v2` (`sse4.2`, `popcnt`, `cx16`, `sahf`), `x86-64-v3` (v2 and `avx2`, `bmi`, `bmi2`, `f16c`, `fma`, `lzcnt`, `movbe`, `xsave`), `x86-64-v4` (v3 and `avx512f`, `avx512bw`, `avx512cd`, `avx512dq`, `avx512vl`) |
| aarch64 | `neon`, `fp16`, `dotprod`, `sve`, `aes`, `sha2`, `pmull` |

- A name implies the features below it: `avx2` implies `avx`, which implies `sse4.2`, and so on down to `sse`. `avx512f` implies `avx2`, `fma` and `f16c`.
- An unknown name is E1106. A name of another architecture is ignored, so one clause can serve both.
- A function's features are the target's baseline plus its own clause. The baseline is `x86-64-v2` on hosted x86_64, `neon` on aarch64, and nothing on `x86_64-freestanding`.
- `mk build --cpu name` names the target CPU, as Clang names it, such as `znver4`. It never changes the baseline, so it never changes which calls need `unsafe`. A CPU below the baseline is E0001.
- Calling a function whose features the caller lacks is E1101 outside `unsafe`. So is naming it as a value: `f := Sum8`, `T.M`, `F[T]`. A call through such a value needs no `unsafe`.
- A `cpu` clause on a bodiless `extern` or on an `impl` method is E1106.
- A generic function that a `cpu` function calls, and every generic it calls in turn, is compiled with the `cpu` function's features for that use.
- `simd.Cpu()` reports the running CPU's features; `Has` takes the names of this table ([std.simd](std/simd.md)).

```mk
// verdict: reject E1101
// arch: x86_64
// naming a cpu function as a value needs unsafe, as calling it does
import std.simd

Double8(v: simd.Vec[f32, 8]): simd.Vec[f32, 8] cpu "avx2" {
    return v + v
}

Pick(): fn(simd.Vec[f32, 8]): simd.Vec[f32, 8] {
    return Double8 // <- E1101
}

Call(v: simd.Vec[f32, 8]): simd.Vec[f32, 8] {
    return Double8(v) // <- E1101
}
```

```mk
// verdict: reject E1106
Fast(x: int): int cpu "avx9" { // <- E1106
    return x
}
```

### General registers only

A bin built with `cpu: "general-regs-only"`, the default on `x86_64-freestanding`, gets no x87, MMX, SSE or AVX instruction. A kernel needs this when it does not save the FPU state on an interrupt.

- A float or vector operation, parameter, result or `asm` operand in any function the bin reaches is E1108. `mk check` and `mk build` report it with the call path from the entry.
- A function whose `cpu` clause holds `sse2` or wider may use them, and must save and restore the FPU state itself. Its parameters and result may not be floats or vectors (E1108).
- `cpu: "baseline"` on the bin allows the float and SSE instructions of plain `x86-64` everywhere. A function's own `cpu` clause still has to name `sse2` for the crypto intrinsics (E1106).

From a freestanding bin whose entry calls a function on `f64`:

```text
src/f.mk:1:19: E1108 general-regs-only: a value of type `f64` in a general-regs-only bin: the function has no SSE register to use
    instance: src.Half
    path: src.MkEntry -> src.Use -> src.Half
    bin: kernel
    try: give the function `cpu "sse2"` and save and restore the FPU state in it, or compute in integers
```

### Inline assembly

`asm("template", operands)` runs machine instructions in place. It is a statement, allowed only in `unsafe` (E1101), and it follows the operand model of Rust's `asm!`.

```mk
// verdict: ok
// arch: x86_64
// Add adds b to a with one instruction.
Add(a: u64, b: u64): u64 {
    var x := a
    unsafe "add touches only its two registers and the flags" {
        asm("add {x}, {b}", x: inout(reg) x, b: in(reg) b, options(pure, nomem, nostack))
    }
    return x
}

// Cpuid runs the cpuid instruction. cpuid writes rbx, which asm may not
// name, so the template keeps rbx in t and swaps it back.
Cpuid(leaf: u32): (a: u32, b: u32, c: u32, d: u32) {
    var a := leaf
    var b: u32 = 0
    var c: u32 = 0
    var d: u32 = 0
    unsafe "cpuid writes only eax to edx, and rbx is kept in t around it" {
        asm("mov {t:r}, rbx\ncpuid\nxchg {t:r}, rbx", t: out(reg) b, inout("eax") a, inout("ecx") c, out("edx") d, options(nomem, nostack, preserves_flags))
    }
    return (a: a, b: b, c: c, d: d)
}

export Main(sys: &System): WriteError!void {
    s := Add(40, 2)
    r := Cpuid(0)
    leaves := r.a >= 1 // the highest leaf this CPU supports
    try sys.Print("{s} {leaves}\n", (s, leaves))
}
```

```text
42 true
```

The template is a string literal, in Intel syntax unless `options(att)`. A hole `{x}` or `{x:mod}` names an operand by its label or by the local it passes. `{{` and `}}` are literal braces. Lines are separated by `\n`.

| Operand | Meaning |
|---|---|
| `in(class) e` | An input: the value of `e` in a register of the class |
| `out(class) p` | An output, assigned to the place `p` |
| `lateout(class) p` | An output assigned after all inputs are read, so it may share an input's register |
| `inout(class) p` | Read from `p`, then written back to it |
| `inlateout(class) p` | The same, and its register may be shared as `lateout`'s |
| `out(class) _` | An output that is discarded: the register is clobbered |
| `const e` | An integer constant, pasted into the template as text |
| `sym F` | The symbol of a non-generic function or global, which stays linked |
| `t: out(reg) x` | Any operand may take a label |
| `clobber_abi("C")` | Every register the target C ABI lets a callee change is clobbered |
| `options(...)` | `pure`, `nomem`, `readonly`, `preserves_flags`, `nostack`, `noreturn`, `att` and `raw`. With `noreturn`, the statement never ends |

| Class | Registers |
|---|---|
| `reg`, `reg_byte` | General registers, full width or one byte |
| `xmm_reg`, `ymm_reg`, `zmm_reg` | Vector registers |
| `kreg` | AVX-512 mask registers |
| `"eax"` | One register, written as a string |

Every operand form once:

```mk
// verdict: ok
// arch: x86_64
const Shift = 3

export extern Hook() {}

Forms(a: u64, b: u64, p: *u64): u64 {
    var x := a
    var y: u64 = 0
    var z: u64 = 0
    unsafe "the templates touch only their operands" {
        asm("add {x}, {b}", x: inout(reg) x, b: in(reg) b)
        asm("mov {y}, {x}", y: lateout(reg) y, x: in(reg) x)
        asm("shl {z}, {s}", z: inlateout(reg) z, s: const Shift)
        asm("mov {t}, [{p}]", t: out(reg) _, p: in(reg) p, options(readonly, nostack))
        asm("lea {t}, [rip + {h}]", t: out(reg) _, h: sym Hook)
        asm("call {h}", h: sym Hook, clobber_abi("C"))
        asm("movl $1, %eax", out("eax") _, options(att, nomem, nostack))
    }
    return x + y + z
}
```

- An operand's type is an integer, `int`, `uint`, `f32`, `f64`, `*T` or a `Vec`. An `in` operand may also be a `bool` or a `Mask`. A reference, slice, `str`, `fn` value, aggregate, or a type naming a type parameter is E1104; pass a raw pointer instead.
- An `out` place must be assignable.
- `rbx`, `rbp`, `rsp` and `rip`, and their parts, are reserved (E1104). Save `rbx` by hand, as `Cpuid` does.
- Without `nomem` or `readonly`, `asm` may read and write all memory its raw pointers reach. How it interacts with loans is in [the memory model, R19](memory.md#r19-unsafe).
- `mk check` checks operand shapes and types. `mk build` checks register names and classes in the code the target uses. On a target with no register names for `asm`, such code is E1104.
- `asm` cannot run at compile time (E0901, `refused: asm`).

```mk
// verdict: reject E1101
// asm runs only inside unsafe
Pause() {
    asm("pause", options(nomem, nostack, preserves_flags)) // <- E1101
}
```

```mk
// verdict: reject E1104
Bad(xs: []u8) {
    unsafe "a slice is two words; asm takes one" {
        asm("nop", x: in(reg) xs) // <- E1104
    }
}
```

```mk
// verdict: reject E0901
Pause(): int {
    unsafe "pause changes no state" {
        asm("pause", options(nomem, nostack, preserves_flags))
    }
    return 1
}

const P = Pause() // <- E0901
```

### `naked` functions

A `naked` function has no prologue, stack probe or epilogue, and is never inlined. It is for entry points that run before a stack frame can be set up, such as a kernel's first instruction. `naked` goes before any function with a body; an entry point is usually `export extern naked`.

```mk
// verdict: ok
export extern KernelMain(): never {
    for {}
}

// Start is where the boot code jumps. It sets up no frame of its own.
export extern naked Start() {
    unsafe "the boot code set up a stack, and KernelMain never returns" {
        asm("call {k}\nhlt", k: sym KernelMain)
    }
}
```

- Its body is one `unsafe` block holding one `asm`, with only `const` and `sym` operands and only the options `att` and `raw`. It never returns.
- Its parameters exist for the ABI and are never used by name.
- It takes no type parameters, contract or `cpu` clause.
- Each of these is E1105, except type parameters on an `extern`, which are E0101.
- Calling it, or naming it as a value, needs `unsafe` (E1101). It cannot run at compile time (E0901).

```mk
// verdict: reject E1105
export extern naked Bad(x: int) {
    unsafe "x arrives in rdi" {
        asm("mov rax, {x}", x: in(reg) x) // <- E1105
    }
}
```

### Sections, linker symbols and device registers

These are for kernels, boot code and firmware, which lay out memory for a linker script or a boot loader.

| Form | Meaning |
|---|---|
| `section "name"` | After a `const` or `var` initializer, or after a function's signature: places the item in that linker section, and keeps it linked whether it is used or not |
| `extern var X: T` | A symbol that a linker script or a `.s` file defines, of a plain type `T` |
| `export extern var X: T` | A symbol this program defines, zeroed, under its own name |
| `&X` in `unsafe` | The address of an `extern var` as a raw pointer `*T`: the one place where `&` gives a raw pointer |
| `std.mmio` | Volatile reads and writes of device registers ([std.mmio](std/mmio.md)) |

```mk
// verdict: ok
// Ticks is a symbol this program defines, zeroed, that C code or a
// debugger can find by name.
export extern var Ticks: u64

// Boot is placed in its own section and kept in the executable.
const Boot: [2]u32 = [0x1BADB002, 0] section ".mkboot"

Tick(): u64 {
    unsafe "Ticks is ours, and nothing else writes it" {
        p := &Ticks // a raw pointer, *u64
        *p = Ticks + 1 // read the symbol, then write through its address
        return Ticks
    }
}

export Main(sys: &System): WriteError!void {
    _ = Tick()
    t := Tick()
    n := Boot.Len()
    try sys.Print("{t} {n}\n", (t, n))
}
```

```text
2 2
```

- An `extern var` is not an Mk `var`. `X = v` and `mut X` are E0404, even in `unsafe`. Write through its address: `*(&X) = v`.
- An `extern var`'s type must be plain (E0701, `bound: Plain`): no references, slices or `str`.
- A section name is 1 to 64 printable ASCII bytes, with no space and no `"` (E0706).
- `section` and `extern var` are allowed only in a module listed in `unsafe_modules` (E1102).

The same constructs in a kernel: a page table, a boot header, a symbol the linker script defines and a device register.

```mk
// verdict: ok
import std.mmio

align(4096) struct PageTable {
    export entries: [512]u64
}

const Header: [3]u32 = [0x1BADB002, 0, 0xE4524FFE] section ".multiboot"

extern var KernelEnd: u8 // defined by the linker script

TableAlign(): int {
    return AlignOf[PageTable]()
}

KernelEndAddress(): *u8 {
    unsafe "the linker script defines KernelEnd" {
        return &KernelEnd
    }
}

ApicVersion(): u32 {
    unsafe "the local APIC is mapped uncached at 0xFEE00000" {
        reg := mmio.Reg[u32].New(0xFEE00030)
        return reg.Read() & 0xFF
    }
}
```

```mk
// verdict: reject E1101
// volatile access and linker symbols need unsafe
import std.mmio

extern var KernelEnd: u8

Status(p: *u32): u32 {
    return mmio.Load(p) // <- E1101
}

End(): *u8 {
    return &KernelEnd // <- E1101
}
```

```mk
// verdict: reject E0404
extern var KernelEnd: u8

Mark() {
    unsafe "the linker script defines KernelEnd" {
        KernelEnd = 1 // <- E0404
    }
}
```

```mk
// verdict: reject E0706
const Boot: u32 = 1 section "boot data" // <- E0706
```

### Compiler errors for unsafe and C

| Code | Name | Reported for |
|---|---|---|
| E0001 | toolchain | A C source that does not compile, an `extern` no library defines, a `--cpu` below the baseline ([`extern`](#calling-c-extern), [CPU features](#cpu-features)) |
| E0101 | syntax | `unsafe` with no reason or an empty one, a `cpu` clause before `requires` or after `section` ([`unsafe` blocks](#unsafe-blocks), [CPU features](#cpu-features)) |
| E0202 | unknown-module | `std.raw` imported outside `unsafe_modules`, a fault in a `pkg.Link` ([who may](#who-may-write-unsafe-code), [linking](#linking-libraries)) |
| E0213 | target-api-mismatch | With `--all-targets`, a used C item or `pkg.Select` module differs between targets, an unselected OS fails to check or lacks `pkg.Select`, or a C target has no sysroot (`reason: no-sysroot`) ([per-target modules](#per-target-modules), [C imports](#c-imports)) |
| E0215 | grant-missing | A fetched package's unsafe code without `unsafe_deps` ([who may](#who-may-write-unsafe-code)) |
| E0301 | type-mismatch | Arithmetic on a raw pointer ([raw pointers](#raw-pointers)) |
| E0404 | assign-immutable | Assigning to an `extern var` ([sections](#sections-linker-symbols-and-device-registers)) |
| E0701 | bound-unmet | An `extern var` whose type is not plain ([sections](#sections-linker-symbols-and-device-registers)) |
| E0706 | requires-failed | A bad section name ([sections](#sections-linker-symbols-and-device-registers)) |
| E0901 | const-failed | `asm`, a `naked` function or `std.mmio` reached at compile time ([inline assembly](#inline-assembly)) |
| E1101 | needs-unsafe | An operation of the [table](#unsafe-blocks) outside `unsafe`, or `impl Send` without `unsafe` ([`unsafe impl`](#unsafe-impl)) |
| E1102 | unsafe-not-permitted | `unsafe`, `unsafe impl`, `section`, `extern var` or an `ends` primitive in a module `unsafe_modules` does not list ([who may](#who-may-write-unsafe-code)) |
| E1103 | raw-outside-unsafe | `*p` outside `unsafe` ([`unsafe` blocks](#unsafe-blocks)) |
| E1104 | asm-operand | An `asm` operand of the wrong type or a reserved register ([inline assembly](#inline-assembly)) |
| E1105 | naked-body | A `naked` body other than one `unsafe` block with one `asm` ([`naked`](#naked-functions)) |
| E1106 | cpu-feature | An unknown feature, or a `cpu` clause on a bodiless `extern` or an `impl` method ([CPU features](#cpu-features)) |
| E1107 | vector-abi | A vector in an `extern` or `export extern` signature ([`extern`](#calling-c-extern)) |
| E1108 | general-regs-only | A float or vector in a `general-regs-only` bin ([general registers](#general-registers-only)) |
| E1109 | no-c-form | A library export whose types have no C form ([`export extern`](#functions-c-can-call-export-extern)) |

Run `mk explain E1101` for any code. The full list is in [the error catalogue](errors.md).

See also: [the memory model, R18 and R19](memory.md#r19-unsafe), [std.raw](std/raw.md), [std.libc](std/libc.md), [std.mmio](std/mmio.md), [std.free](std/free.md), [packages](packages.md) for `unsafe_modules` and grants, [freestanding](#freestanding) and [targets](#targets) in section 13.

## 15. Contracts and tests

A contract states what a function needs and what it promises: `requires` on its arguments, `ensures` on its result, and `invariant` on every value of a struct. A test is a `test` block that `mk test` runs. Contracts are checked while you develop and test, and cost nothing in a release build.

```mk
// verdict: ok
struct Span {
    lo: i32
    hi: i32
    invariant lo <= hi
}

Span.Len(&self): int
    ensures result >= 0
{
    return Cast[int](self.hi) - Cast[int](self.lo)
}

SafeDiv(a: i32, b: i32): i32
    requires b > 0
{
    return a / b
}

test "len" {
    Assert(Span(lo: 1, hi: 4).Len() == 3)
}

test "with memory" (sys: &System) {
    xs := HeapList[int].New(sys.heap.Alloc())
    defer xs.Free(sys.heap.Alloc())
    Assert(xs.Len() == 0)
}
```

| Form | Meaning |
|---|---|
| `requires c` | `c` must hold when the function is called |
| `ensures c` | `c` must hold when the function returns. `result` names the returned value |
| `invariant c` | A condition on the struct's fields, checked when a value is built and when the struct's own module returns or changes one ([invariants](#invariants-invariant)) |
| `test "name" { ... }` | A test, run by `mk test` |
| `test "name" (sys: &System) { ... }` | A test that uses the system: memory, files, output |
| `Assert(c)` | Panic unless `c` holds, in every build |

A broken contract is a [panic](#panics) with the message `contract`. It reports a bug in the program, not a condition to handle: use [errors](#9-errors) for input the outside world can get wrong.

### Preconditions: `requires`

`requires` states what a caller must pass. The clauses come after the signature and before the body, and are checked on entry, before the body runs.

```mk
// verdict: ok
SafeDiv(a: i32, b: i32): i32
    requires b > 0
{
    return a / b
}

export Main(sys: &System): WriteError!void {
    q := SafeDiv(7, 2)
    try sys.Print("{q}\n", (q))
    z := SafeDiv(7, Cast[i32](sys.args.Len()) - 1) // b is 0 when the program has no arguments
    try sys.Print("{z}\n", (z))
}
```

```text
3
panic: contract: requires at main.mk:3:5
backtrace:
  SafeDiv (main.mk)
  Main (main.mk)
```

- The report points at the clause that failed; the backtrace shows the caller.
- A function may have several `requires` clauses. Each is checked in order, and the report names the one that failed.
- A clause may call functions, such as `requires Sorted(xs)`, if they take no `Io`.
- A clause that reads only const parameters, consts and literals is checked at compile time, at each call, in every build: a false clause is E0706 at the call. See [compile-time requirements](#compile-time-requirements).
- In a release build a run-time clause is not checked. With `--release`, the program above divides by zero instead.

### Postconditions: `ensures`

`ensures` states what a function promises its caller. `result` is the returned value. The clauses are checked at each `return`, after the result is computed.

```mk
// verdict: ok
Abs(n: int): int
    ensures result >= 0
{
    if n < -5 {
        return n // a bug
    }
    return if n < 0 { -n } else { n }
}

export Main(sys: &System): WriteError!void {
    for n in [3, -2, -9] {
        a := Abs(n)
        try sys.Print("{a}\n", (a))
    }
}
```

```text
3
2
panic: contract: ensures at main.mk:6:9
backtrace:
  Abs (main.mk)
  Main (main.mk)
```

- The report points at the `return` that broke the promise.
- `result` exists only in `ensures`. In a `requires` it is an unknown name (E0201).
- For a function that returns `E!T`, `result` is the `T`, and the clauses are checked only when the function succeeds. A `fail` checks nothing.
- An `ensures` may also read the parameters. A `mut` parameter is read as it is when the function returns.
- A function with an `ensures` cannot end in a `become` tail call (E0312), because the check runs after the call.

### Invariants: `invariant`

`invariant` states a fact about every value of a struct. Inside it, a field is named bare (`lo`) or through `self` (`self.lo`). A struct may have several.

```mk
// verdict: ok
struct Span {
    lo: int
    hi: int
    invariant lo <= hi
}

Span.Grow(mut self, by: int) {
    self.hi += by
}

export Main(sys: &System): WriteError!void {
    var s := Span(lo: 1, hi: 4)
    s.Grow(2)
    try sys.Print("{lo} {hi}\n", (lo: s.lo, hi: s.hi))
    s.Grow(-10)
    try sys.Print("{lo} {hi}\n", (lo: s.lo, hi: s.hi))
}
```

```text
1 6
panic: contract: invariant at main.mk:10:1
backtrace:
  Span:Grow (main.mk)
  Main (main.mk)
```

An invariant is checked at these points:

- right after a value of the struct is constructed, in any module;
- when a function of the struct's own module returns a value of the struct;
- at every exit of a `mut self` method of the struct, in its own module.

The report points at the construction, or at the exit that broke the invariant: in the example, the closing `}` of `Grow`.

A direct field assignment, `s.hi = -1`, and a free function that takes the struct as a `mut` parameter, such as `Shrink(s: mut Span)`, are not checked. Keep the fields private and change them through `mut self` methods, and the invariant holds wherever the struct is used outside its module.

A function of the struct's module that returns the struct, and a `mut self` method of it, cannot end in a `become` tail call (E0312).

### What a contract may contain

A contract is a `bool` expression with no side effects. It may read the parameters, `result` and the fields, and call functions that take no `Io`.

| Not allowed in a contract | Error |
|---|---|
| `try` | E0101 |
| A call of a function that takes `Io` | E0101 |
| An assignment | E0101 (an assignment is a statement, not an expression) |
| A contract on an interface method | E0101: put it on the functions of each `impl` |

```mk
// verdict: reject E0101
Now(io: Io): int {
    return 1
}

After(io: Io, n: int): int
    requires Now(io) > 0 // <- E0101
{
    return n
}
```

```mk
// verdict: reject E0101
interface Shape {
    Area(&self): int
        ensures result >= 0 // <- E0101
}
```

### When contracts are checked

| Build | Run-time contracts | Compile-time `requires` | `Assert` |
|---|---|---|---|
| `mk build`, `mk run` (development) | Checked | Checked | Checked |
| `mk test`, `mk test --prop` | Checked | Checked | Checked |
| `--profile checked` | Checked | Checked | Checked |
| `--release` with any command, `mk test --release` included; `--profile size` | Not checked | Checked | Checked |

A contract is checked in the function that declares it, not at each call. The contracts are part of the function's public signature: [`mk apidiff`](tools.md#6-api-diff) reports a changed one.

### Tests

A `test` block is a named test. It lives in the module it tests, so it can call the module's private functions. `mk test` builds the package's tests and runs them ([`mk test`](tools.md#mk-test)).

```mk
// verdict: ok
Clamp(x: int, lo: int, hi: int): int
    requires lo <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return hi
    }
    return x
}

test "clamp inside" {
    Assert(Clamp(5, 0, 10) == 5)
}

test "clamp above" {
    Assert(Clamp(50, 0, 10) == 11)
}

test "prints" (sys: &System) {
    try sys.Print("only shown when the test fails\n")
}
```

```text
$ mk test clamp.mk
PASS main "clamp inside"
FAIL main "clamp above": panic (exit 101)
    panic: assert at clamp.mk:19:5
    backtrace:
      test "clamp above" (clamp.mk)
PASS main "prints"
2 passed, 1 failed
```

- Each line names the module and the test. A failing test shows its output, its panic or error, and its exit status.
- A test passes when it returns. It fails on a panic (`Assert`, a broken contract, an index out of bounds), on an error that leaves it (exit 1), on a leak, or on a timeout.
- A test may use `try`. An error that leaves the test fails it, and `mk test` prints the error with its trace ([errors in tests](#errors-in-tests-and-tasks)).
- A test may declare one parameter of type `&System`, usually `(sys: &System)`, for memory, output, files and tasks. A parameter of another type is E0301, and a second parameter E0101.
- In a test, the heap counts its allocations. Memory still allocated when the test ends fails it with `leak (exit 102)`.
- Test names are strings, unique in their module (E0206). No naming convention makes a function a test.
- A test is checked with its module by every command, but built only by `mk test`. `mk build` and `mk run` leave tests out.

```mk
// verdict: reject E0206
test "parse" {
    Assert(true)
}

test "parse" { // <- E0206
    Assert(true)
}
```

### Property tests: `mk test --prop`

`mk test --prop` tests contracts instead of running the `test` blocks. It calls each function that has a contract with inputs generated from its parameter types, and checks every clause on every call.

```mk
// verdict: ok
struct Span {
    lo: i32
    hi: i32
    invariant lo <= hi
}

// Len subtracts in i32, so a wide span overflows.
Span.Len(&self): int
    ensures result >= 0
{
    return Cast[int](self.hi - self.lo)
}

SafeDiv(a: i32, b: i32): i32
    requires b != 0
{
    return a / b
}
```

```text
$ mk test --prop span.mk
FAIL main "prop SafeDiv": panic: divide by zero
    SafeDiv(-2147483648, -1)  at span.mk:18:12
    clause: requires b != 0
    shrunk in 0 rounds; 11 cases, 7 discarded by requires or invariant
FAIL main "prop Span.Len": panic: overflow
    Span(lo: -2147483648, hi: 0).Len()  at span.mk:12:22
    clause: ensures result >= 0
    shrunk in 1 rounds; 2 cases, 2 discarded by requires or invariant
0 passed, 2 failed (prop seed 30798615365709680)
```

Both findings are real bugs. `-2147483648 / -1` does not fit in an `i32`, and `hi - lo` overflows in `i32` for a wide span. The first example of this section fixes both: `Len` subtracts in `int`, and `SafeDiv` requires `b > 0`. With those fixes, every case passes:

```text
PASS main "prop SafeDiv"
    100 cases, 108 discarded by requires or invariant
PASS main "prop Span.Len"
    117 cases, 83 discarded by requires or invariant
2 passed, 0 failed (prop seed 30798615365709680)
```

- An input that breaks a `requires`, or a struct argument that breaks its `invariant`, is discarded. A panic, or a broken `ensures` or invariant, is a finding: it is shrunk to one small call and printed with its clause.
- How inputs are chosen, saved findings, skipped functions and seeds are in [the property tester](tools.md#7-contracts-and-the-property-tester).

### Fuzzing, benchmarks and simulation

These are library features that run inside `test` blocks:

- `testing.Fuzz(f)` registers a fuzz target. `mk test` runs it over saved inputs, and `mk test --fuzz name` searches for new failing ones. See [fuzz targets](std/testing.md#fuzz-targets).
- `testing.Bench(f)` registers a benchmark that `mk bench` runs. See [benchmarks](std/testing.md#benchmarks).
- `std.sim` runs a test against a simulated disk, network and clock, from a seed that repeats the run exactly. `mk test --sim SEED` runs every test that way. See [`std.sim`](std/sim.md).

See also: [write a test and a property](cookbook.md#write-a-test-and-a-property), [tests beside code](codebase.md#6-tests-beside-code), [`mk test`](tools.md#mk-test).

### Compiler errors for contracts and tests

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | `try`, an `Io` call or an assignment in a contract; a contract on an interface method; a second test parameter ([what a contract may contain](#what-a-contract-may-contain), [tests](#tests)) |
| E0201 | unknown-name | `result` outside an `ensures` |
| E0206 | duplicate-name | Two tests of one module with the same name ([tests](#tests)) |
| E0301 | type-mismatch | A test parameter whose type is not `&System` ([tests](#tests)) |
| E0312 | tail-call-signature | A `become` in a function with an `ensures`, in a function that returns a struct with an invariant, or in a `mut self` method of such a struct |
| E0706 | requires-failed | A compile-time `requires` that is false at a call ([section 10](#compile-time-requirements)) |

## 16. Habits from other languages

Code written from habit in C, Go, Rust or Zig is often close to Mk but not quite Mk. The compiler recognises many such forms and says what to write instead. This section lists them by language, so you can look up the form you reached for.

```mk
// verdict: reject E1207
error Bad

Get(): Bad!int {
    return 1
}

Twice(): Bad!int {
    let x = Get()?; // <- E1207
    return x * 2
}
```

```text
$ mk check twice.mk
twice.mk:9:5: E1207 let-keyword: Mk binds with `x := e`; `var x := e` is mutable
    |     let x = Get()?; // <- E1207
    |     ^^^
    habits: ["E1207","E1208","E1206"]
    target:     x := try Get() // <- E1207
    fix (proven): rewrite the habit as Mk
    explain: mk explain E1207
1 error
```

The line holds three habits: `let`, a postfix `?` and a `;`. The compiler reports one diagnostic, lists all three under `habits`, and shows the line as Mk under `target`.

### How habits are reported

- A line with habits gets one habit diagnostic, with the code of its leftmost habit. It lists every habit on the line and shows the line rewritten. A syntax error that the rewrite would still leave, such as Go's `x int` parameter after `func`, is reported beside it.
- The compiler reports one phase at a time ([diagnostics](tools.md#3-diagnostics)). A habit seen only in a later phase, such as a misspelled member, shows once the earlier ones are fixed: `l.push(1);` reports the `;` (E1206) first and `.push` (E0207) after.
- A form the compiler recognises gets a habit code, E1202 to E1223. Some get a general code whose message names the Mk form: E0207 for a name spelled another way (`Some`, `.len()`), E0101 for syntax (`switch`, `x++`).
- Other forms get the plain error for what they are: E0201 for an unknown name such as `malloc`, E0203 for an unknown member such as `.unwrap()`.

`mk fix` applies the rewrites it can prove, round by round ([`mk fix`](tools.md#mk-fix)). Given this file:

```mk
// verdict: reject E1206
Fill(a: &Arena): OutOfMemory!void {
    var l := List[int].New(a)
    l.push(1); // <- E1206
}
```

```text
$ mk fix push.mk
round 1 (parse, 1 diagnostic):
  E1206 push.mk:4:14: rewrite the habit as Mk
round 2 (types, 2 diagnostics):
  E0207 push.mk:4:7: member names are exact; this is the declared spelling
wrote .../push.mk
stopped: no proven fix
push.mk:4:5: E0802 error-unhandled: the error of `OutOfMemory!void` is not handled
    |     l.Push(1) // <- E1206
    |     ^^^^^^^^^
    type: OutOfMemory!void
    fix (compiles): pass the error to the caller
    try: handle it here: `<value> else e { ... }`, or a `match`
    try: widen the function's error set to hold these errors, then pass it on with `try`
    explain: mk explain E0802
1 error
```

`l.push(1);` became `l.Push(1)`. Whether to pass the error on with `try` or handle it is your choice, so `mk fix` stops and the compiler suggests both.

To find the Mk name of a library function you know from elsewhere, ask `mk query find`. It matches synonyms. In a package that does not parse it answers from std alone, and says so:

```text
$ mk query find append
Int.Add  func  std.big
Block.Add  func  std.core
HeapList.Push  func  std.core
HeapText.Push  func  std.core
List.Push  func  std.core
Set.Add  func  std.core
Text.Push  func  std.core
...
```

### Common to several languages

In the tables below, the Code column is what the compiler reports for the habit. It is empty where the habit is a way of working rather than a form the compiler sees. It is also empty where the form compiles, as Go's `defer` and Rust's `&str` do.

| You wrote | Write in Mk | Code |
|---|---|---|
| `fn f()`, `func f()`, `def f()` | `F()`: a function is its signature | E1205 |
| `-> T` after the parameters | `: T` | E1211 |
| `pub` | `export` | E1215 |
| `;` at the end of a statement | A newline | E1206 |
| `while c { }`, `loop { }` | `for c { }`, `for { }` | E1216 |
| `switch x { case ... }` | `match x { ... }` ([`match`](#match)) | E0101 |
| `x++`, `x--` | `x += 1`, `x -= 1` | E0101 |
| `c ? a : b` | `if c { a } else { b }` ([`if` as a value](#if-match-and-else-as-values)) | E1208 |
| `null`, `nil` | `none` ([optionals](#optionals-t)) | E1209 |
| `panic(...)`, `panic!(...)` | `Panic("...")`, which returns `never` ([panics](#panics)) | E0207, E1210 |
| `assert x == y`, `assert!(...)` | `Assert(x == y)` | E1223 |
| `throw e`, `try { } catch e { }` | `fail e`, `x := F() else e { }` ([section 9](#9-errors)) | E1210 |
| `#include`, `#define`, `#[...]`, `@name(...)` | `import`, `const`, `gen`, a library function: Mk has no attributes | E0101 |
| `f<T>()`, `Option<T>` | `F[T]()`, `?T`: type arguments go in brackets | E0101 |
| An implicit conversion, `i64` from `i32` | `i64(x)`, or `Cast[T](x)` when the value may not fit ([conversions](#conversions)) | E0301 |
| A lower-case function or type name | `PascalCase` for items, `snake_case` for locals and fields ([names](#names)) | E0207 |
| An uninitialised local read before it is set | Assign it on every path first: `var x: int`, then `x = ...` | E0403 |
| `unreachable` | `Panic("unreachable")` | E0201 |
| `usize`, `isize` | `int` | E1221, E0201 |
| `P{x: 1}`, `P { x: 1 }`, `{1, 2}`, `.{ .x = 1 }` | `P(x: 1, y: 2)`: a struct literal names its fields in parentheses ([literals](#array-record-and-struct-literals)) | E0101 |
| `"{}"` or `"%d"` in a format string | A named hole, `"{x}"` with the record `(x)` ([printing](#printing)) | E0706 |
| `a + b` on strings | `try arena.Fmt("{a}{b}", (a, b))`, or `Push` onto a `Text` | E0308 |

The same function, written out of habit and in Mk:

```mk
// verdict: reject E1205
fn Sum(xs: []int): int { // <- E1205
    var s := 0
    for x in xs {
        s += x
    }
    return s
}
```

```mk
// verdict: ok
Sum(xs: []int): int {
    var s := 0
    for x in xs {
        s += x
    }
    return s
}
```

### From C

| You wrote | Write in Mk | Code |
|---|---|---|
| `int x = 0;` | `x: int = 0`, or `var x: int = 0` to change it later | E0101 |
| `struct P { int x; };` | `struct P { x: int }`, one field per line | E0102, E0101 |
| `for (i = 0; i < n; i++)` | `for i in 0..n` | E0101 |
| `p->x` | `p.x`, through any reference | E1211 |
| `T *p` as a parameter | `p: &T` to read, `p: mut T` to write ([borrowing](#8-ownership-and-borrowing)). `*T` is a raw pointer, for C | E0101 |
| `*p` on a raw pointer | `*p` inside `unsafe "reason" { }` ([section 14](#14-unsafe-and-c)) | E1103 |
| `p + 1` on a pointer | `raw.Offset(p, 1)` ([raw pointers](#raw-pointers)) | E0301 |
| `NULL` | `none`, in an optional `?T` | E0201 |
| `(int)x` | `int(x)` when no value can change; `Cast[int](x)`, which panics on a value that does not fit; `Trunc[int](x)`, which keeps the low bits as C does ([conversions](#conversions)) | E0101 |
| `sizeof(T)` | `SizeOf[T]()` | E0207 |
| `typedef int Id;` | `type Id = int` ([type aliases](#type-aliases)) | E0101 |
| `exit(1)` | Return the status from `Main` (`Main(sys: &System): int`), or `proc.Exit(1)` | E0201 |
| `malloc`, `free` | An arena, or `Box[T].New(a, x)` and `b.Free(a)` ([memory](std/README.md#3-memory-allocators-and-arenas)) | E0201, E0207 |
| `printf` | `sys.Print("{x}\n", (x))` ([printing](#printing)) | E0201 |
| `char *s` | `str` for text, `[]u8` for bytes. A `char` is one Unicode scalar | E0102, E0101 |
| An error code or `errno` | An error set and `E!T` ([section 9](#9-errors)) | |
| Unsigned overflow that wraps | `+%`, `-%`, `*%`. Plain `+` panics on overflow | |
| `goto` | A labelled `break`, `defer` or `onerr` | E0101 |
| A header file | Nothing: `export` marks what a module shares ([modules](#13-modules-and-build)) | |

```mk
// verdict: ok
const Limit = 4 // #define LIMIT 4

struct Rect {
    w: int
    h: int
}

enum Color {
    Red
    Green
}

// A pointer parameter in C is a borrow in Mk: &T reads, mut T writes.
Grow(r: mut Rect, by: int) {
    r.w += by // a field through a borrow: `.`, never `->`
}

Area(r: &Rect): int {
    return r.w * r.h
}

Name(c: Color): str {
    return match c { // switch
        Color.Red { "red" }
        Color.Green { "green" }
    }
}

// NULL becomes an optional: ?int is an int or none.
Find(xs: []int, v: int): ?int {
    for i in 0..xs.Len() { // for (i = 0; i < n; i++)
        if xs[i] == v {
            return some(i)
        }
    }
    return none
}

export Main(sys: &System): WriteError!void {
    var r := Rect(w: 2, h: 3)
    Grow(mut r, 1)
    a := Area(&r)
    big := if a > 8 { "big" } else { "small" } // a > 8 ? "big" : "small"
    nums: [4]int = [5, 7, 9, 11] // int nums[4]
    at := Find(nums[..], 9) else { -1 }
    var n := 0
    for n < Limit { // while (n < LIMIT)
        n += 1 // n++
    }
    x: i64 = 300
    b := Cast[u8](x - 100) // (unsigned char)(x - 100), checked
    h: u32 = 4000000000
    wrapped := h +% h // unsigned arithmetic that wraps on purpose
    c := Name(Color.Green)
    try sys.Print("{a} {big} {at} {n} {b} {wrapped} {c}\n", (a, big, at, n, b, wrapped, c))
}
```

```text
9 big 2 4 200 3705032704 green
```

Memory you would `malloc` comes from an allocator you are given. A `Box` is linear: forgetting to `Free` it is a compile error, not a leak (E0402 once the box is used).

```mk
// verdict: ok
struct Node {
    value: int
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    b := try Box[Node].New(a, Node(value: 7)) // malloc, and a check for NULL
    v := b.Ref().value
    b.Free(a) // free
    try sys.Print("{v}\n", (v))
}
```

```text
7
```

Integer overflow panics, and `+%`, `-%` and `*%` wrap ([arithmetic](#arithmetic)).

### From Go

| You wrote | Write in Mk | Code |
|---|---|---|
| `func f(x int) int` | `F(x: int): int` | E1205, E0101 |
| `func (p P) Get() int` | `P.Get(&self): int` ([methods](#functions-and-methods)) | E0101 |
| `type P struct { X int }` | `struct P { x: int }` | E0101 |
| Exported by an upper-case name | `export` on the item or field. Every item name is `PascalCase` | |
| `x := 1`, then `x = 2` | `var x := 1`: a binding made with `:=` does not change ([bindings](#bindings)) | E0404 |
| `x, err := f()` and `if err != nil { return err }` | `x := try F()` ([`try`](#try)) | E0101 |
| `return 0, err` | `fail err` ([`fail`](#fail)) | E0101 |
| `errors.New("empty")` | `error Empty` ([declaring errors](#declaring-errors)) | E0201 |
| Several results, `(int, int)` | A record, `: (lo: int, hi: int)`, unpacked with `(lo, hi) := F()` | E0101 |
| `for i, x := range xs` | `for i, x in xs` | E0101 |
| `len(xs)` | `xs.Len()` | E0201 |
| `append(xs, x)` | `try list.Push(x)`: it can run out of memory | E0201 |
| `make(map[string]int)` | `Map[str, int].New(arena)` | E0101 |
| `v, ok := m[k]` | `if v := m.Get(&k) { }` | E0101 |
| `interface{}`, `any` | A type parameter `[T]` with the bounds it needs ([generics](#10-generics-and-interfaces)) | E0102, E0101, E0201 |
| A type that satisfies an interface by its methods | `impl I for T { }`: an interface is implemented only by an `impl` | E0701 |
| `go f()` | `s.go F()` inside `scope s := io.Scope() { }` ([tasks](#starting-a-task-sgo)) | E0101 |
| `chan T`, `select` | `Chan[T]` ([channels](#channels)) and `sync.Select` ([select](std/sync.md#select)) | E0101 |
| `fmt.Println`, `fmt.Sprintf` | `sys.Print`, `try arena.Fmt(...)` | E1221, E0201 |
| `var x int`, read as its zero value | No zero values: `var x := 0`, or `var x: int` assigned before the first read | E0101; E0403 for a read before the assignment |
| `defer f()` | `defer F()`, which runs when its block exits, so once per pass in a loop ([`defer`](#defer)) | |

```mk
// verdict: ok
error Empty // var ErrEmpty = errors.New("empty")

interface Shape {
    Area(&self): int
}

struct Square {
    side: int
}

// A method: the receiver is written first, as Square.
Square.Perimeter(&self): int {
    return 4 * self.side
}

// A type implements an interface only through an impl.
impl Shape for Square {
    Area(&self): int {
        return self.side * self.side
    }
}

// Two results: return a record.
MinMax(xs: []int): Empty!(lo: int, hi: int) {
    if xs.Len() == 0 {
        fail Empty // return 0, 0, ErrEmpty
    }
    var lo := xs[0]
    var hi := xs[0]
    for x in xs {
        lo = if x < lo { x } else { lo }
        hi = if x > hi { x } else { hi }
    }
    return (lo, hi) // short for (lo: lo, hi: hi)
}

Count(xs: []int, out: mut int) {
    *out = xs.Len()
}

export Main(sys: &System): Empty|OutOfMemory|WriteError!void {
    var list := List[int].New(sys.arena) // make([]int, 0)
    for x in [3, 1, 4, 1, 5] {
        try list.Push(x) // list = append(list, x)
    }
    (lo, hi) := try MinMax(list[..]) // if err != nil { return err }
    var ages := Map[str, int].New(sys.arena) // make(map[string]int)
    try ages.Set("ada", 36)
    for i, x in list { // for i, x := range list
        if i == 2 {
            try sys.Print("list[{i}] = {x}\n", (i, x))
        }
    }
    sq := Square(side: 3)
    p := sq.Perimeter()
    ar := sq.Area()
    var n := 0
    scope s := sys.io.Scope() { // go Count(...), then wait for it
        s.go Count(list[..], mut n)
    }
    key := "ada"
    var age := 0
    if v := ages.Get(&key) { // v, ok := ages["ada"]
        age = *v
    }
    try sys.Print("{lo} {hi} {age} {p} {ar} {n}\n", (lo, hi, age, p, ar, n))
}
```

```text
list[2] = 4
1 5 36 12 9 5
```

A `scope` block cannot end until every task started in it has finished, so there is no `sync.WaitGroup` to forget.

A type with the right methods but no `impl` does not meet a bound:

```mk
// verdict: reject E0701
interface Shape {
    Area(&self): int
}

struct Tile {
    side: int
}

Tile.Area(&self): int {
    return self.side * self.side
}

Measure[T: Shape](x: &T): int {
    return x.Area()
}

Size(t: &Tile): int {
    return Measure(t) // <- E0701
}
```

Move `Area` into `impl Shape for Tile { Area(&self): int { ... } }`, in place of `Tile.Area`, and it compiles. A type cannot have a method and an `impl` method of one name (E0206).

### From Rust

| You wrote | Write in Mk | Code |
|---|---|---|
| `let x =`, `let mut x =` | `x :=`, `var x :=` | E1207 |
| `&mut x` | `mut x` | E1212 |
| `a::b`, `Shape::Circle` | `a.b`, `Shape.Circle` | E1213 |
| `use a::b;` | `import a.b` ([imports](#imports)) | E0101 |
| `mod util;` | Nothing: a module is a directory, and every file in it sees all of it | E0101 |
| `pat => expr` in a `match` | `pat { expr }` | E1214 |
| `if let Some(x) = e` | `if x := e` | E1217 |
| `Some`, `None`, `Ok`, `Err` | `some`, `none`, `ok`, `err` | E0207 |
| `Result<T, E>` | `E!T` | E0101 |
| `?` after a call | `try` before it | E1208 |
| The last expression as the result | `return e` | E0209, E0805 |
| `impl T { fn m(&self) }` | `T.M(&self)` | E1219, E1205 |
| `trait I { }` | `interface I { }`. `impl I for T { }` is the same | E0101 |
| `#[derive(PartialEq, Hash)]` | `gen derive.Eq(T)`, `gen derive.Hash(T)` ([generators](#11-compile-time)) | E0101 |
| `dyn I`, `&dyn I`, `&mut dyn I` | `IDyn`, the type `gen dyn.Of(I)` declares: `IDyn.Of(mut x)` borrows `x` ([`dyn.Of`](#values-of-an-interface-dynof), [std.dyn](std/dyn.md)) | E1221; E1212 for `&mut` |
| `Vec<Box<dyn I>>` | An `enum` with a variant per type, or a `List` of `IDyn` while the objects live elsewhere | E0101, E1221 |
| `'a` | Nothing, or a `from` or `into` clause ([section 8](#8-ownership-and-borrowing)) | E1203 |
| `a == b` on a struct | `a.Eq(&b)`, after `gen derive.Eq(T)`; `==` works on numbers, `bool`, `char`, `str` and enums without payloads | E0308 |
| `&[T]` | `[]T`, or `mut []T` to write | E0101 |
| `s: &str` | `s: str`: a `str` is already a borrowed view | |
| `0..=n` | `0..n + 1` | E1220 |
| `unsafe { }` | `unsafe "why this is correct" { }` | E0101 |
| `async fn`, `.await` | A plain function that takes an `Io` | E1204 |
| `assert_eq!(a, b)` | `Assert(a == b)` | E0101 |
| `\|x\| x + 1`, a closure that captures | A `fn` literal, or a struct and a `fn` ([function literals](#function-literals)) | E0609 |
| `.iter().map(...).filter(...).collect()` | A `for` loop | E1202 |
| `.len()`, `.push(x)` | `.Len()`, `try .Push(x)` | E0207 |
| `.unwrap_or(x)` | `e else { x }` ([`else`](#else)) | E0203 |
| `.unwrap()`, `.expect("...")` | `.Must()` | E1221 |
| `.clone()` | A plain value copies; `try arena.Dup(s)` copies text | E1221 |
| `.to_string()`, `format!` | `try arena.Fmt("{x}", (x))` | E1221 |
| `println!`, `vec!` | `sys.Print(...)`, `List` | E1221 |
| `Vec`, `String`, `HashMap` | `List`, `Text`, `Map` | E1221 |
| `Rc`, `Arc` | `sync.Shared` | E1221 |
| `x as T` | `T(x)`, `Cast[T](x)` or `Trunc[T](x)`; a narrowing `T(x)` is E0303 | E1222 |
| `one := 1` used as an `i32` | `one: i32 = 1`: a literal bound with no type is an `int` | E0301 |
| `Drop` | A `linear` type with a release method, called explicitly or with `defer` ([linear values](memory.md#r2-linear-values-are-consumed-exactly-once-on-every-path)) | E0402 |

```mk
// verdict: ok
import std.derive

enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
}

struct Person {
    name: str
    age: int
}

gen derive.Eq(Person) // #[derive(PartialEq)]

interface HasArea { // trait HasArea
    Area(&self): f64
}

impl HasArea for Shape {
    Area(&self): f64 {
        return match *self {
            Shape.Circle(r) { 3.0 * r * r }
            Shape.Rect(w, h) { w * h }
        }
    }
}

// fn longest<'a>(a: &'a str, b: &'a str) -> &'a str
Longest(a: str, b: str): str from a, b {
    return if a.Len() >= b.Len() { a } else { b }
}

Total[T: HasArea](xs: []T): f64 { // fn total<T: HasArea>(xs: &[T]) -> f64
    var sum := 0.0
    for x in &xs { // borrow each element: T may not be copyable
        sum += x.Area()
    }
    return sum
}

First(xs: []int): ?int { // Option<i64>
    if xs.Len() == 0 {
        return none // None
    }
    return some(xs[0]) // Some(xs[0])
}

export Main(sys: &System): WriteError!void {
    shapes: [2]Shape = [Shape.Circle(r: 1.0), Shape.Rect(w: 2.0, h: 3.0)]
    t := Total(shapes[..])
    nums: [3]int = [4, 5, 6]
    if n := First(nums[..]) { // if let Some(n) = first(&nums)
        try sys.Print("first {n}\n", (n))
    }
    f := First(nums[..0]) else { -1 } // .unwrap_or(-1)
    l := Longest("ferris", "gopher")
    a := Person(name: "ada", age: 36)
    b := a // Person is plain: a copy, not a move
    same := a.Eq(&b) // a == b
    try sys.Print("{t} {f} {l} {same}\n", (t, f, l, same))
}
```

```text
first 4
9 -1 ferris true
```

There is no `Clone` or `Copy` to derive. A struct of plain fields is plain and copies; a struct that owns something moves ([value kinds](memory.md#r1-value-kinds)). `.unwrap()` is reported with the Mk member, and the fix renames it:

```mk
// verdict: reject E1221
First(xs: ?int): int {
    return xs.unwrap() // <- E1221
}
```

```text
$ mk check first.mk
first.mk:3:15: E1221 foreign-name: `?int` has no member `unwrap`: Mk spells it `Must`
    |     return xs.unwrap() // <- E1221
    |               ^^^^^^
    type: ?int
    mk_name: Must
    habits: ["E1221"]
    fix (proven): the Mk spelling of the member
    explain: mk explain E1221
1 error
```

### From Zig

| You wrote | Write in Mk | Code |
|---|---|---|
| `const x = 1;` in a function | `x := 1`. A top-level `const X = 1` is Mk | E0101 |
| `var x: i32 = 0;` | `var x: i32 = 0` | E1206 |
| `pub fn f() T` | `export F(): T` | E1215 |
| `const P = struct { x: i32 };` | `struct P { x: i32 }` | E0101 |
| `error{ A, B }` | `error A`, `error B`, then `type E = A \| B` ([error sets](#error-sets)) | E0101 |
| `!T`, an inferred error set | `E!T`: every signature names its set | E0101 |
| `try f()` | `try F()`, the same; the callee's errors must fit the function's set, which a signature always names ([`try`](#try)) | E0801 |
| `f() catch 0`, `catch \|e\| { }` | `F() else { 0 }`, `F() else e { }` ([`else`](#else)) | E0101 |
| `x orelse 0` | `x else { 0 }` | E0101 |
| `x.?` | `x.Must()` | E0101, E1208 |
| `errdefer` | `onerr` ([`onerr`](#cleanup-on-the-error-path-onerr)) | E0101 |
| `comptime n: i64` | `const n: int` ([`const` parameters](#const-parameters)) | E0101 |
| `@intCast(x)`, `@as(T, x)` | `Cast[T](x)`, `T(x)` | E0101 |
| `@truncate(x)` | `Trunc[T](x)` | E0101 |
| `@import("std")` | `import std.fs`, one module at a time | E0101 |
| `@panic("x")` | `Panic("x")` | E0101 |
| `undefined` | `var x: T`, assigned before its first read | E0201 |
| `for (xs) \|x\|`, `for (xs, 0..) \|x, i\|` | `for x in xs`, `for i, x in xs` | E0101 |
| `std.debug.print("{}", .{x})` | `sys.Print("{x}\n", (x))` | E0101 |
| An `Allocator` parameter and `defer list.deinit()` | `&Arena` or `&Alloc`. A `List` lives in an arena and needs no release | |
| `[]const u8` | `str` for text, `[]u8` for bytes | E0102, E0101 |
| `+%`, `-%`, `*%` | The same | |

```mk
// verdict: ok
error Empty
error TooBig(n: int)

type ParseError = Empty | TooBig // error{ Empty, TooBig }

Digits(s: str): ParseError!int { // fn digits(s: []const u8) ParseError!i64
    if s.Len() == 0 {
        fail Empty // return error.Empty
    }
    var n := 0
    for b in s.AsBytes() { // for (s) |b|
        n = n * 10 + int(b - b'0')
    }
    if n > 999 {
        fail TooBig(n: n)
    }
    return n
}

Repeat(const times: int, x: int): int { // comptime times: i64
    var r := 0
    for _ in 0..times {
        r += x
    }
    return r
}

Lookup(xs: []int, i: int): ?int {
    if i < xs.Len() {
        return some(xs[i])
    }
    return none // null
}

Build(a: &Arena, n: int): OutOfMemory!List[int] from a { // the allocator is a parameter
    var out := List[int].New(a)
    for i in 0..n {
        try out.Push(i * i)
    }
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    d := Digits("42") else { 0 } // digits("42") catch 0
    e := Digits("") else e { // catch |e|
        try sys.Print("error: {e}\n", (e))
        0
    }
    x: i64 = 70
    small := Cast[i8](x) // @intCast(x), checked
    r := Repeat(3, 5)
    sq := try Build(sys.arena, 4)
    v := Lookup(sq[..], 2) else { -1 } // lookup(...) orelse -1
    w := Lookup(sq[..], 3).Must() // .?
    try sys.Print("{d} {e} {small} {r} {v} {w}\n", (d, e, small, r, v, w))
}
```

```text
error: Empty
42 0 70 15 4 9
```

### Compiler errors for habits

| Code | Name | Reported for |
|---|---|---|
| E0101 | syntax | A foreign form with no habit code; the message often names the Mk form ([common](#common-to-several-languages)) |
| E0102 | reserved-word | `interface{}` as a type, `int x` as a field, `char *s`, `[]const u8` ([C](#from-c), [Go](#from-go), [Zig](#from-zig)) |
| E0201 | unknown-name | `NULL`, `len`, `append`, `malloc`, `exit`, `printf`, `isize`, `undefined`, `unreachable` ([common](#common-to-several-languages), [C](#from-c), [Go](#from-go), [Zig](#from-zig)) |
| E0203 | unknown-member | `.unwrap()`, `.expect()`, `.clone()`, `.to_string()` ([Rust](#from-rust)) |
| E0206 | duplicate-name | A method declared both on the type and in an `impl` ([Go](#from-go)) |
| E0207 | naming | `Some` `None` `Ok` `Err`, `.len()`, `.push()`, `panic()`, `free()`, `sizeof()`, lower-case item names ([common](#common-to-several-languages), [C](#from-c), [Rust](#from-rust)) |
| E0209 | unused-result | A last expression used as the result ([Rust](#from-rust)) |
| E0301 | type-mismatch | An implicit conversion; an untyped literal binding used as another integer type ([common](#common-to-several-languages), [C](#from-c), [Rust](#from-rust)) |
| E0303 | lossy-conversion | `i32(x)` from an `i64` ([Rust](#from-rust)) |
| E0308 | no-operator | `==` on a struct, `+` on strings ([common](#common-to-several-languages), [Rust](#from-rust)) |
| E0402 | not-consumed | A linear value left to an implicit drop ([Rust](#from-rust)) |
| E0403 | use-before-assign | A local read before it is assigned ([common](#common-to-several-languages), [Go](#from-go)) |
| E0404 | assign-immutable | Assigning to a binding not declared with `var` ([Go](#from-go)) |
| E0609 | literal-captures | A closure ([Rust](#from-rust)) |
| E0701 | bound-unmet | A type with an interface's methods but no `impl` ([Go](#from-go)) |
| E0706 | requires-failed | A positional hole `{}`, a `%d` hole ([common](#common-to-several-languages)) |
| E0801 | error-not-in-set | `try` on errors the function's set does not hold ([Zig](#from-zig)) |
| E0802 | error-unhandled | An `E!T` left unhandled, as after `mk fix` turns `l.push(1);` into `l.Push(1)` ([reporting](#how-habits-are-reported)) |
| E0805 | missing-return | A body that ends without `return` ([Rust](#from-rust)) |
| E1103 | raw-outside-unsafe | `*p` on a raw pointer outside `unsafe` ([C](#from-c)) |
| E1202 | iterator-chain | `.map`, `.filter`, `.collect` and other adapters ([Rust](#from-rust)) |
| E1203 | lifetime-syntax | `'a` ([Rust](#from-rust)) |
| E1204 | async-syntax | `async`, `await` ([Rust](#from-rust)) |
| E1205 | fn-keyword | `fn`, `func`, `def` ([common](#common-to-several-languages)) |
| E1206 | semicolon | `;` ([common](#common-to-several-languages), [Zig](#from-zig)) |
| E1207 | let-keyword | `let`, `let mut` ([Rust](#from-rust)) |
| E1208 | postfix-question | `?` after an expression, `.?`, `c ? a : b` ([common](#common-to-several-languages), [Rust](#from-rust), [Zig](#from-zig)) |
| E1209 | null-word | `null`, `nil` ([common](#common-to-several-languages)) |
| E1210 | exception-words | `throw`, `try { } catch`, `panic!` ([common](#common-to-several-languages)) |
| E1211 | arrow-return | `->` ([common](#common-to-several-languages), [C](#from-c)) |
| E1212 | ampersand-mut | `&mut` ([Rust](#from-rust)) |
| E1213 | double-colon | `::` ([Rust](#from-rust)) |
| E1214 | fat-arrow | `=>` ([Rust](#from-rust)) |
| E1215 | pub-keyword | `pub` ([common](#common-to-several-languages)) |
| E1216 | loop-keywords | `while`, `loop` ([common](#common-to-several-languages)) |
| E1217 | if-let | `if let` ([Rust](#from-rust)) |
| E1219 | impl-block | `impl T { }` ([Rust](#from-rust)) |
| E1220 | inclusive-range | `..=` ([Rust](#from-rust)) |
| E1221 | foreign-name | `Vec`, `String`, `HashMap`, `Rc`, `Arc`, `usize`, `fmt.Println`, `println!`, `format!`, `vec!`, `dyn I`, `.unwrap()`, `.expect()`, `.clone()`, `.to_string()` ([common](#common-to-several-languages), [Go](#from-go), [Rust](#from-rust)) |
| E1222 | as-cast | `as` ([Rust](#from-rust)) |
| E1223 | assert-statement | `assert x`, `assert!` ([common](#common-to-several-languages)) |

See also: [the tour](overview.md), which teaches the Mk forms in order, and [the style guide](style.md) for names and layout.

## 17. Module map

The standard library is a set of modules under `std`. A small set of names, the prelude, is visible in every module without an import. Everything else comes from an `import std.x` line, and you name it through the module's last component: `fs.Open`, `time.Now`.

Prelude, no import needed: `Alloc Arch Arena Block Box Canceled Cell Chan Chunks ChunksMut Closed Code Eq Field Flow FlowKind Format Free Gen Guard Hash HashKey Hasher Heap HeapList HeapMap HeapText Io Limits Lines List Map MapIter Method Mutex Ord Os OutOfMemory Param ParseError Path Print ReadError Reader Receiver Replace Scalars Scope Set Slice Split StackSize Swap System TargetInfo Text TextMap TextMapIter Type TypeKind Utf8Error Variant Words WriteError Writer`. Of these, `Gen Type Code Block Field Variant Method Param TypeKind Receiver Flow FlowKind` are the generator API of section 11, so a generator module names them with no import.

The prelude is exactly these names, and `std.core` exports them; [std.core](std/core.md) documents them. `TargetInfo`, `Arch` and `Os` are declared in `std.target` and re-exported by `std.core` as aliases, so they are the same types. The built-in bounds `Plain`, `Send`, `Share` and `Fields` are not prelude names but universe names, like `Assert`: no module declares them.

This table lists the importable modules and the names each one exports. The [library guide](std/README.md#8-module-index) says what each module is for and links its page.

| Import | Names |
|---|---|
| `std.fs` | `File Dir Entry Info ReadText ReadBytes WriteAll Create Open Rename Remove MakeDir Stat Error OpenWith OpenPath Options DefaultOptions ReadOnly ReadWrite CreateNew Append Whence LockMode Kind Permissions Map Mapping Walk Walker WalkEntry MakeDirAll RemoveAll Copy CreateTemp TempDir Symlink ReadLink StatLink StatPath SetModified SetPermissions Cwd Absolute` |
| `std.net` | `Listen Dial Listener Stream Bind Datagram Addr ParseAddr Resolve Error ListenWith ListenOptions DefaultListenOptions ListenUnix DialUnix Shutdown Conn NewConn Transport ReserveConn ConnSlot Secure SecureFailure Pass Passed` |
| `std.time` | `Instant Duration Now Seconds Millis Minutes Hours Micros Nanos ParseDuration Time Wall FromUnix Date FromDate Zone Database LocalName ParseRfc3339 ParseHttpDate FormatHttpDate TimedOut Error` |
| `std.time.tzdata` | `Database`, the embedded zones, linked only into a binary that names it |
| `std.proc` | `Run Output Supervise WaitSignal Signal Exit Error Pipe PipeReader PipeWriter Spawn Command DefaultCommand Env Stdio Child Status Pty Pass Inherited` |
| `std.sync` | `RwLock ReadGuard Select Selectable Shared Semaphore Notify Barrier Once` |
| `std.atomic` | `AtomicInt AtomicBool Atomic Spin SpinGuard Order Relaxed Acquire Release AcqRel SeqCst Fence Unordered Pause` |
| `std.mmio` | `Load Store Fence Reg`, every call inside an unsafe block |
| `std.mem` | `Fixed FixedHeap Fallback Counting Limit Failing Null Pool PoolIter Handle AllocTable OffsetOf Prefetch Zeroed` |
| `std.coll` | `Deque HeapDeque PriorityQueue BTree HeapBTree BTreeIter Bitset` |
| `std.text` | `Graphemes Clusters Normalize Form FoldCase Width ToUtf16 FromUtf16 WordBounds SentenceBounds Bounds ParagraphBounds BidiLevels Levels Run Direction Mirror LineBreaks Breaks Capped Sink CategoryOf Category ScriptOf Script ScriptExtensions Has Property UnicodeVersion` |
| `std.math` | `Min Max Abs Sqrt Rng Integer AddOverflow SubOverflow MulOverflow WrapI32 WrapU32 CastSat CastSatF32 MulWide DivWide AddCarry SubBorrow`, the functions of section 18 in `f64` and with an `F32` suffix in `f32`, the integer bit operations with a width suffix, `Gcd CheckedPow CheckedPowU64 Pi E Ln2 Sqrt2 Inf Nan ShortestDigits ExactDigits Digits DigitMode` |
| `std.simd` | `Vec Mask Lane CpuFeatures Choice Cpu Lanes Lookup16 Dispatch IndexByte IndexAny Count Equal Compare ValidUtf8 ValidWtf8 IsAscii Utf16Len Utf16LenBytes LastIndexByte Dot Sum` |
| `std.free` | `PanicInfo`, `Heap`, on the freestanding target only |
| `std.libc` | No Mk names: C-callable functions that a package's C sources link against, written in Mk and exported with `export extern`: the memory and string functions, `malloc`, `calloc`, `realloc` and `free` over the `Heap`, and what sqlite needs as `SQLITE_OS_OTHER`; on `x86_64-windows` also `__C_specific_handler`, the language handler a C source's `__try`, `__except` and `__finally` need, forwarded to ntdll's |
| `std.hash` | `Xx Xx64 Crc32 Sip13 Sha256 Sha512 Blake3 Crc32State Xx64State Sip13State Sha256State Sha512State Blake3State Sha1 Sha1State Sha384 Sha384State` |
| `std.json` | `Parse Value Member Json Codec Error` |
| `std.http` | `Serve Fetch ReadRequest Dispatch Request Response Reply Header Route Error Client ClientRequest ClientLimits DefaultClientLimits Exchange ServeWith ServeOn ServeChan ServerOptions DefaultServerOptions Link Taken` |
| `std.regex` | `Compile Regex Match Matcher Error` |
| `std.crypto` | `Hmac256 ChaCha20Poly1305 X25519 Ed25519 Random Equal Error Hmac384 Hmac512 HkdfExtract HkdfExpand Prk HashKind AesGcm EcSecret Curve EcdhShared EcdsaSign EcdsaVerify SigFormat RsaPublic RsaVerifyPkcs1 RsaVerifyPss PublicKey PrivateKey ParsePublicKey ParsePrivateKey Wipe Argon2id Argon2idVerify Argon2Cost` |
| `std.compress` | `Inflate Gunzip Deflate Gzip Inflater Error Zlib Unzlib Adler32 Adler32State Encoder Framing ZstdDecoder Content GzipContent DeflateContent ZstdContent` |
| `std.cli` | `Args Error` |
| `std.log` | `Logger Level Sink Stderr Stdout ToFile` |
| `std.dyn`, `std.derive` | Generators: `dyn.Of`, `derive.Eq`, `derive.Hash`, `derive.Format` |
| `std.dyn` | `Ref` |
| `std.dyn.erased` | `Key` |
| `std.sim` | `Sim Op Fault Recorder Replayer` |
| `std.db.sqlite` | `Open Db Stmt Error` |
| `std.big` | `Int FromI64 FromBytes FromWords Parse Error` |
| `std.encoding` | `Base64Encode Base64Decode Base64EncodeTo Base64DecodeTo Base64Len Alphabet Base64 Base64Url Base64Raw Base64UrlRaw Base64Forgiving HexEncode HexDecode PercentEncode PercentDecode PercentSet Lookup Encoding Decoder Utf8 Utf16Le Utf16Be Replacement XUserDefined Ibm866 Iso88592 Iso88593 Iso88594 Iso88595 Iso88596 Iso88597 Iso88598 Iso88598I Iso885910 Iso885913 Iso885914 Iso885915 Iso885916 Koi8R Koi8U Macintosh Windows874 Windows1250 Windows1251 Windows1252 Windows1253 Windows1254 Windows1255 Windows1256 Windows1257 Windows1258 XMacCyrillic Codec ParsePem FormatPem PemBlock DerRead DerValue DerIter DerWriter Error` |
| `std.encoding.cjk` | `Lookup Gb18030 Gbk Big5 EucJp Iso2022Jp ShiftJis EucKr`, the multi-byte encodings, linked only into a binary that names them |
| `std.url` | `Parse Url Host Part ParseQuery FormatQuery DomainToAscii DomainToUnicode Error` |
| `std.websocket` | `Accept Dial Conn Options DefaultOptions Kind Message Error` |
| `std.term` | `Open IsTerminal UseColor Term Std Event Key Mods Style Color Sanitize MoveTo Clear AltScreen ShowCursor BracketedPaste MouseCapture KittyKeys Error` |
| `std.x509` | `Parse ParsePem Certificate Pool SystemRoots SystemPool Verify Error`, Experimental |
| `std.tls` | `Client Server Connector Acceptor Conn Config DefaultConfig Identity Version Error`, Experimental |
| `std.csv` | `Records Record Options DefaultOptions WriteRecord Codec Error` |
| `std.dl` | `Open Library Search Error`, Provisional |
| `std.runtime` | `Processors Carriers StackBounds StackRemaining Blocking Waker PanicInfo AddFaultFilter FaultContext FaultAction FaultKind FilterId RegCount ForeignIo Foreign ForeignContext IoTable IoResult SysErr IoStat IoAddr IoChild IoCallArgs IoWords InertTable Dedicated Thread Joined TaskIo Cycles Likely Unlikely Cold WriteDump Unsupported OsError Error`; `AddFaultFilter`, `ForeignIo` and `ForeignContext` only inside an unsafe block, and `ForeignIo` and the `Io` table Provisional |
| `std.vm` | `PageSize Reserve Region CodeRegion CodeWriter Prot Misaligned Unsupported Os Error`, every call inside an unsafe block |
| `std.linux` | `Syscall`, every call inside an unsafe block, on x86_64-linux only |
| `std.testing` | `Fuzz Bench` |
| `std.build` | `File Profile ProfileKind`, read at compile time only |

`std.db.sqlite` needs a C import of SQLite ([std.db.sqlite](std/sqlite.md)).

The modules `std.core`, `std.raw`, `std.rt`, `std.sys`, `std.task`, `std.target` and `std.simd.kernel` hold the library's own internals. Only standard library modules may import them, except `std.raw` ([who may write unsafe code](#who-may-write-unsafe-code)). Any other import of one is E0202.

Every public item of std has a stability level, which says how it may change from one release to the next. The [library guide](std/README.md#9-stability) lists the modules and items that are not Stable and what each level allows before and after 1.0.

## 18. Library surface

This section lists the signature of every public item of the standard library, module by module. What a module is for, what it costs, how it fails and how to use it is on the module's page, linked below, and in each item's doc comment; `mk doc --std std.<module>` prints both. `mk query members T` prints the lines of one type.

### 18.1 Reading the signatures

Each line of the listing declares one item:

| Form | Meaning |
|---|---|
| `// std.fs. File and Error below are fs.File and fs.Error` | Opens a module's part. Its own types are written unqualified below it. |
| `List[T].Push(mut self, x: T into self): OutOfMemory!void` | A method. `&self` borrows the receiver, `mut self` borrows it mutably, `self` takes it by value. |
| `str.Split(self, sep: str): Split from self, sep` | `from` names the parameters the result borrows from (section 8). |
| `fs.Rename(io: Io, src: str, dst: str): fs.Error!void` | A function of the module. |
| `// Kind may gain members` | The enum or error set is declared open. A `match` on it outside std needs a `_` arm. |

The listing leaves out these constants, since their values are std's:

| Module | Constants |
|---|---|
| `std.fs` | `DefaultOptions`, `ReadOnly` (the same), `ReadWrite`, `CreateNew`, `Append`, all `fs.Options` |
| `std.net`, `std.proc` | `net.DefaultListenOptions`, `proc.DefaultCommand` |
| `std.http`, `std.websocket`, `std.tls`, `std.csv` | `http.DefaultClientLimits`, `http.DefaultServerOptions`, `websocket.DefaultOptions`, `tls.DefaultConfig`, `csv.DefaultOptions` |
| `std.math` | `Pi`, `E`, `Ln2`, `Sqrt2`, `Inf`, `Nan`, all `f64` |
| `std.encoding` | `Base64`, `Base64Url`, `Base64Raw`, `Base64UrlRaw`, `Base64Forgiving` of `encoding.Alphabet` |
| `std.encoding` | `Utf8`, `Utf16Le`, `Utf16Be`, `Replacement`, `XUserDefined` and one per WHATWG single-byte encoding, of `encoding.Encoding` |
| `std.encoding.cjk` | `Gb18030`, `Gbk`, `Big5`, `EucJp`, `Iso2022Jp`, `ShiftJis`, `EucKr`, of `encoding.Encoding` |
| `std.compress` | `GzipContent`, `DeflateContent`, `ZstdContent`, of `compress.Content` |
| `std.time.tzdata` | `Database`, of `time.Database` |
| `std.runtime` | `InertTable`, of `runtime.IoTable` |

### 18.2 Module pages

The [library guide](std/README.md) holds the conventions every module shares. Each module's own page:

- [`std.core`](std/core.md), the prelude
- [`std.atomic`](std/atomic.md)
- [`std.big`](std/big.md)
- [`std.build`](std/build.md)
- [`std.cli`](std/cli.md)
- [`std.coll`](std/coll.md)
- [`std.compress`](std/compress.md)
- [`std.crypto`](std/crypto.md)
- [`std.csv`](std/csv.md)
- [`std.db.sqlite`](std/sqlite.md)
- [`std.derive`](std/derive.md)
- [`std.dl`](std/dl.md)
- [`std.dyn`](std/dyn.md)
- [`std.encoding`](std/encoding.md), with `std.encoding.cjk`
- [`std.free`](std/free.md)
- [`std.fs`](std/fs.md)
- [`std.hash`](std/hash.md)
- [`std.http`](std/http.md)
- [`std.json`](std/json.md)
- [`std.libc`](std/libc.md)
- [`std.linux`](std/linux.md)
- [`std.log`](std/log.md)
- [`std.math`](std/math.md)
- [`std.mem`](std/mem.md)
- [`std.mmio`](std/mmio.md)
- [`std.net`](std/net.md)
- [`std.proc`](std/proc.md)
- [`std.raw`](std/raw.md)
- [`std.regex`](std/regex.md)
- [`std.runtime`](std/runtime.md)
- [`std.sim`](std/sim.md)
- [`std.simd`](std/simd.md)
- [`std.sync`](std/sync.md)
- [`std.term`](std/term.md)
- [`std.testing`](std/testing.md)
- [`std.text`](std/text.md)
- [`std.time`](std/time.md), with `std.time.tzdata`
- [`std.tls`](std/tls.md)
- [`std.url`](std/url.md)
- [`std.vm`](std/vm.md)
- [`std.websocket`](std/websocket.md)
- [`std.x509`](std/x509.md)

### 18.3 Signatures

Every public item of std, grouped by module.


```mk
// verdict: parse
struct System {
    export args: []str
    export io: Io
    export arena: &Arena
    export heap: &Heap
}

System.Stdout(&self): Writer
System.Stderr(&self): Writer
System.Print[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
System.PrintErr[A: Fields[Format] + Plain](&self, const f: str, args: A): WriteError!void
System.Stdin(&self): Reader
System.Env(&self, name: str): ?str from self

str.Len(self): int
str.Byte(self, i: int): u8
str.AsBytes(self): []u8
str.Find(self, sub: str): ?int
str.Has(self, sub: str): bool
str.StartsWith(self, prefix: str): bool
str.EndsWith(self, suffix: str): bool
str.Trim(self): str
str.Split(self, sep: str): Split from self, sep
str.SplitOnce(self, sep: str): ?(before: str, after: str) from self
str.Lines(self): Lines
str.Words(self): Words
str.Scalars(self): Scalars
str.ParseInt(self): ParseError!i64
str.ParseFloat(self): ParseError!f64
str.ParseFloatPrefix(self): (value: f64, used: int)
str.From(bytes: []u8): Utf8Error!str
str.FromC(p: *u8): str
str.ToLower(self, a: &Arena): OutOfMemory!str from a
str.ToUpper(self, a: &Arena): OutOfMemory!str from a
str.Replace(self, a: &Arena, old: str, new: str): OutOfMemory!str from a
f64.Bits(self): u64
f64.FromBits(b: u64): f64
f32.Bits(self): u32
f32.FromBits(b: u32): f32

Split.Next(mut self): ?str from self.text
Lines.Next(mut self): ?str from self.text
Words.Next(mut self): ?str from self.text
Scalars.Next(mut self): ?char

Slice[T].Len(&self): int
Slice[T].Get(&self, i: int): ?&T
Slice[T].GetMut(mut self, i: int): ?mut T
Slice[T].Sort(mut self, const less: fn(&T, &T): bool)
Slice[T].SortStable(mut self, a: &Alloc, const less: fn(&T, &T): bool): OutOfMemory!void
Slice[T].SortStableInPlace(mut self, const less: fn(&T, &T): bool)
Slice[T].Search[K](&self, key: &K, const less: fn(&T, &K): bool): int
Slice[T].Swap(mut self, i: int, j: int)
Slice[T].Reverse(mut self)
Slice[T].Fill(mut self, x: T into self)
Slice[T].CopyFrom(mut self, src: []T) *src into self
Slice[T].Has(&self, x: &T): bool
Slice[T].Index(&self, x: &T): ?int
Slice[T].SplitAt(mut self, i: int): (left: mut []T, right: mut []T) from self
Slice[T].GetPair(mut self, i: int, j: int): ?(a: mut T, b: mut T) from self
Slice[T].Chunks(&self, n: int): Chunks[T] from self
Slice[T].ChunksMut(mut self, n: int): ChunksMut[T] from self
Chunks[T].Next(mut self): ?[]T from self.xs
ChunksMut[T].Next(mut self): ?mut []T from self.ptr

List[T].New(a: &Arena): List[T]
List[T].Push(mut self, x: T into self): OutOfMemory!void
List[T].Pop(mut self): ?T
List[T].Len(&self): int
List[T].Clear(mut self)
List[T].Truncate(mut self, n: int)
List[T].Resize(mut self, n: int, x: T into self): OutOfMemory!void
List[T].Insert(mut self, i: int, x: T into self): OutOfMemory!void
List[T].Remove(mut self, i: int): T
List[T].Done(self): mut []T

Map[K, V].New(a: &Arena): Map[K, V]
Map[K, V].NewKeyed(a: &Arena, key: HashKey): Map[K, V]
Map[K, V].Get(&self, k: &K): ?&V
Map[K, V].GetMut(mut self, k: &K): ?mut V
Map[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
Map[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
Map[K, V].Ensure(mut self, k: K into self, v: V into self): OutOfMemory!mut V from self
Map[K, V].Has(&self, k: &K): bool
Map[K, V].Remove(mut self, k: &K): ?V
Map[K, V].Len(&self): int
Map[K, V].Iter(&self): MapIter[K, V]
MapIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.map

Set[T].New(a: &Arena): Set[T]
Set[T].NewKeyed(a: &Arena, key: HashKey): Set[T]
Set[T].Add(mut self, x: T into self): OutOfMemory!bool
Set[T].Has(&self, x: &T): bool
Set[T].Remove(mut self, x: &T): bool

Text.New(a: &Arena): Text
Text.Push(mut self, s: str): OutOfMemory!void
Text.PushChar(mut self, c: char): OutOfMemory!void
Text.PushReplaced(mut self, s: str, old: str, new: str): OutOfMemory!void
Text.Str(&self): str
Text.Len(&self): int
Text.Done(self): str
Text.Clear(mut self)
Text.Drain(mut self, n: int)
Text.Writer(mut self): Writer

HeapList[T].New(a: &Alloc): HeapList[T]
HeapList[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
HeapList[T].Pop(mut self): ?T
HeapList[T].Len(&self): int
HeapList[T].Remove(mut self, i: int): T
HeapList[T].Insert(mut self, a: &Alloc, i: int, x: T into self): OutOfMemory!void
HeapList[T].Extend(mut self, a: &Alloc, xs: []T): OutOfMemory!void *xs into self
HeapList[T].Clear(mut self)
HeapList[T].Free(self, a: &Alloc)
HeapList[T].FreeEmpty(self, a: &Alloc)
HeapList[T].FreeEach(self, a: &Alloc, const release: fn(T))
HeapMap[K, V].New(a: &Alloc): HeapMap[K, V]
HeapMap[K, V].NewKeyed(a: &Alloc, key: HashKey): HeapMap[K, V]
HeapMap[K, V].Get(&self, k: &K): ?&V
HeapMap[K, V].GetMut(mut self, k: &K): ?mut V
HeapMap[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void
HeapMap[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V
HeapMap[K, V].Ensure(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!mut V from self
HeapMap[K, V].Remove(mut self, k: &K): ?V
HeapMap[K, V].Len(&self): int
HeapMap[K, V].Iter(&self): MapIter[K, V]
HeapMap[K, V].Free(self, a: &Alloc)
HeapMap[K, V].FreeEmpty(self, a: &Alloc)
HeapMap[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))
TextMap[V].New(a: &Alloc): TextMap[V]
TextMap[V].NewKeyed(a: &Alloc, key: HashKey): TextMap[V]
TextMap[V].Get(&self, k: str): ?&V
TextMap[V].GetMut(mut self, k: str): ?mut V
TextMap[V].Set(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!void
TextMap[V].Replace(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!?V
TextMap[V].Ensure(mut self, a: &Alloc, k: str, v: V into self): OutOfMemory!mut V from self
TextMap[V].Remove(mut self, a: &Alloc, k: str): ?V
TextMap[V].Len(&self): int
TextMap[V].Iter(&self): TextMapIter[V]
TextMapIter[V].Next(mut self): ?(key: str, value: &V) from self.map
TextMap[V].Free(self, a: &Alloc)
TextMap[V].FreeEmpty(self, a: &Alloc)
TextMap[V].FreeEach(self, a: &Alloc, const release: fn(V))
HeapText.New(a: &Alloc): HeapText
HeapText.Push(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Set(mut self, a: &Alloc, s: str): OutOfMemory!void
HeapText.Clear(mut self)
HeapText.Len(&self): int
HeapText.Str(&self): str
HeapText.Free(self, a: &Alloc)
Box[T].New(a: &Alloc, x: T): OutOfMemory!Box[T] from a, x
Box[T].Ref(&self): &T
Box[T].Mut(mut self): mut T
Box[T].Take(self, a: &Alloc): T
Box[T].Free(self, a: &Alloc)

Arena.New(h: &Heap): OutOfMemory!Arena
Arena.NewIn(a: &Alloc): OutOfMemory!Arena from a
Arena.Put[T](&self, x: T into self): OutOfMemory!mut T from self
Arena.Slice[T](&self, n: int): OutOfMemory!mut []T
Arena.SliceAligned[T](&self, n: int, align: int): OutOfMemory!mut []T
Arena.Dup(&self, s: str): OutOfMemory!str from self
Arena.Fmt[A: Fields[Format] + Plain](&self, const f: str, args: A): OutOfMemory!str from self
Arena.Sub(&self): OutOfMemory!Arena
Arena.Alloc(&self): &Alloc
Arena.Reset(mut self) ends self
Arena.Free(self) ends self
Heap.Alloc(&self): &Alloc
Alloc.Bytes(&self, n: int, align: int): *u8
Alloc.FreeBytes(&self, p: *u8, n: int, align: int)

Print[A: Fields[Format] + Plain](w: mut Writer, const f: str, args: A): WriteError!void
Swap[T](a: mut T, b: mut T) *a into b, *b into a
Replace[T](dst: mut T, x: T into dst): T
Writer.Str(mut self, s: str): WriteError!void
Writer.Bytes(mut self, b: []u8): WriteError!void
Writer.Flush(mut self): WriteError!void
Reader.Read(mut self, buf: mut []u8): ReadError!int
Reader.ReadAll(mut self, buf: mut []u8): ReadError!int
Reader.Line(mut self, a: &Arena): ReadError|OutOfMemory!?str from a
Reader.Until(mut self, delim: u8, buf: mut []u8): ReadError!int
Reader.Of(b: []u8): Reader from b

interface Format {
    Format(&self, w: mut Writer): WriteError!void
}

interface Eq {
    Eq(&self, other: &Self): bool
}

interface Ord {
    Less(&self, other: &Self): bool
}

interface Hash {
    Hash(&self, h: mut Hasher)
}

interface Free {
    Free(self, a: &Alloc)
}

Hasher.Bytes(mut self, b: []u8)
Hasher.U64(mut self, x: u64)
HashKey.New(io: Io): Canceled!HashKey
HashKey.Of(k0: u64, k1: u64): HashKey

interface Slice[T] {
    Items(&self): []T
    ItemsMut(mut self): mut []T
}

struct Cell[T: Plain] {
    value: T
}

Cell[T].New(x: T): Cell[T]
Cell[T].Load(&self): T
Cell[T].Store(&self, x: T into self)

Path.New(a: &Arena, bytes: []u8): OutOfMemory!Path from a
Path.AsBytes(&self): []u8 from self.bytes
Path.Str(&self): ?str from self.bytes
Path.Join(&self, a: &Arena, part: str): OutOfMemory!Path from a
Path.Base(&self): Path from self.bytes
Path.Parent(&self): ?Path from self.bytes
Path.Format(&self, w: mut Writer): WriteError!void

Io.Scope(self): Scope
Io.ScopeOf[E](self): Scope[E]
Io.ScopeWith(self, l: Limits): Scope
Io.NoCancel(self): Io
Io.Sleep(self, d: time.Duration): Canceled!void
Io.Random(self, buf: mut []u8): Canceled!void
Io.Yield(self): Canceled!void
Scope.Cancel(&self)
Scope.CancelAfter(&self, d: time.Duration)
Scope.Expired(&self): bool
Scope.Remaining(&self): ?time.Duration

struct Limits {
    export tasks: int
    export stack: StackSize
}

enum StackSize {
    Default
    Large
    Bytes(n: int)
}

// Target is a compile-time constant of this type, declared in std.target
struct TargetInfo {
    export arch: Arch
    export os: Os
    export pointer_bytes: int
    export vector_bits: int
}

// Arch and Os may gain a variant when a target is added, so a match on
// Target.arch or Target.os keeps a _ arm; std declares both open
enum Arch {
    X64
    Arm64
    Riscv64
}

enum Os {
    Linux
    Windows
    Macos
    Freestanding
    Nutshell
}

// Every std module's Error, each error set it names, and each enum
// marked "may gain members" below may gain members: std declares each open,
// and a match on one outside std keeps a _ arm

// std.fs. File and Error below are fs.File and fs.Error
error Os(code: i64)
error Unsupported
type Error = NotFound | Exists | Denied | IsDirectory | NotDirectory | NoSpace | InvalidPath | Utf8Error | Os | Canceled | Unsupported
fs.Rename(io: Io, src: str, dst: str): fs.Error!void
fs.Remove(io: Io, p: str): fs.Error!void
fs.ReadText(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a
fs.ReadBytes(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory![]u8 from a
fs.WriteAll(io: Io, p: str, data: []u8): fs.Error!void
fs.Open(io: Io, p: str): fs.Error!File
fs.Create(io: Io, p: str): fs.Error!File
File.Read(mut self, io: Io, buf: mut []u8): fs.Error!int
File.Write(mut self, io: Io, data: []u8): fs.Error!int
File.ReadAt(&self, io: Io, pos: i64, buf: mut []u8): fs.Error!int
File.WriteAt(&self, io: Io, pos: i64, data: []u8): fs.Error!int
File.WriteAll(mut self, io: Io, data: []u8): fs.Error!void
File.Sync(mut self, io: Io): fs.Error!void
File.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf
File.Reader(mut self, io: Io, buf: mut []u8): Reader from self, buf
File.Close(self, io: Io): fs.Error!void
File.Abandon(self)
// File.Adopt takes an rt.Passed, which no program can build: net.Passed.Adopt calls it
File.Adopt(p: rt.Passed): File
fs.MakeDir(io: Io, p: str): fs.Error!void
fs.Stat(io: Io, p: str): fs.Error!Info
Dir.Open(io: Io, p: str): fs.Error!Dir
Dir.Next(mut self, a: &Arena, io: Io): fs.Error|OutOfMemory!?Entry from a
Dir.Close(self)
fs.OpenWith(io: Io, p: str, o: Options): fs.Error!File
fs.OpenPath(io: Io, p: &Path, o: Options): fs.Error!File
File.Seek(mut self, io: Io, pos: i64, from: Whence): fs.Error!i64
File.Size(&self, io: Io): fs.Error!i64
File.Truncate(mut self, io: Io, size: i64): fs.Error!void
File.Lock(&self, io: Io, mode: LockMode): fs.Error!void
File.TryLock(&self, io: Io, mode: LockMode): fs.Error!bool
File.Unlock(&self, io: Io): fs.Error!void
fs.Map(io: Io, f: &File, offset: i64, len: int): fs.Error!Mapping
Mapping.Bytes(&self): []u8 from self
Mapping.Unmap(self)
fs.Walk(h: &Heap, io: Io, root: str): fs.Error|OutOfMemory!Walker from h
Walker.Next(mut self, a: &Arena, io: Io): fs.Error|OutOfMemory!?WalkEntry from a
Walker.SkipDir(mut self)
Walker.Close(self)
fs.MakeDirAll(io: Io, p: str): fs.Error!void
fs.RemoveAll(io: Io, p: str): fs.Error!void
fs.Copy(io: Io, src: str, dst: str): fs.Error!i64
fs.CreateTemp(a: &Arena, io: Io, dir: str, prefix: str): fs.Error|OutOfMemory!(file: File, path: str) from a
fs.TempDir(a: &Arena, io: Io): fs.Error|OutOfMemory!str from a
fs.Symlink(io: Io, target: str, link: str): fs.Error!void
fs.ReadLink(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a
fs.StatLink(io: Io, p: str): fs.Error!Info
fs.StatPath(io: Io, p: &Path): fs.Error!Info
fs.SetModified(io: Io, p: str, t: time.Time): fs.Error!void
fs.SetPermissions(io: Io, p: str, perm: Permissions): fs.Error!void
fs.Cwd(a: &Arena, io: Io): fs.Error|OutOfMemory!str from a
fs.Absolute(a: &Arena, io: Io, p: str): fs.Error|OutOfMemory!str from a

// An options type: DefaultOptions, or a preset, with fields assigned
struct Options {
    export read: bool
    export write: bool
    export append: bool
    export create: bool
    export truncate: bool
    export exclusive: bool
    export permissions: Permissions
}

enum Whence {
    Start
    Current
    End
}

enum LockMode {
    Shared
    Exclusive
}

// Kind may gain members
enum Kind {
    File
    Dir
    Link
    Other
}

struct Permissions {
    export mode: u32
}

struct WalkEntry {
    export path: Path
    export kind: Kind
    export depth: int
}

struct Entry {
    export name: Path
    export is_dir: bool
    export size: i64
}

struct Info {
    export size: i64
    export modified: time.Time
    export kind: Kind
    export permissions: Permissions
}

// std.net. Listener, Stream and Error below are net.Listener, net.Stream and net.Error
error Os(code: i64)
error Unsupported
error SecureFailed(why: SecureFailure)
type Error = Refused | Reset | AddrInUse | Unreachable | InvalidAddress | Os | Canceled | Closed | time.TimedOut | Unsupported | SecureFailed | OutOfMemory
net.Listen(io: Io, addr: str): net.Error!Listener
net.Dial(io: Io, addr: str): net.Error!Stream
Listener.Accept(&self, io: Io): net.Error!Stream
Listener.Port(&self): int
Listener.Close(self)
Stream.Read(&self, io: Io, buf: mut []u8): net.Error!int
Stream.Write(&self, io: Io, data: []u8): net.Error!int
Stream.WriteAll(&self, io: Io, data: []u8): net.Error!void
Stream.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
Stream.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Stream.Close(self)
net.ParseAddr(s: str): net.Error!Addr
net.Resolve(a: &Arena, io: Io, host: str): net.Error|OutOfMemory!List[Addr] from a
Addr.Format(&self, w: mut Writer): WriteError!void
net.Bind(io: Io, addr: str): net.Error!Datagram
Datagram.Send(&self, io: Io, data: []u8, to: Addr): net.Error!void
Datagram.Recv(&self, io: Io, buf: mut []u8): net.Error!(n: int, peer: Addr)
Datagram.Port(&self): int
Datagram.Close(self)
Stream.SetNoDelay(&self, on: bool): net.Error!void
Stream.SetKeepAlive(&self, idle: ?time.Duration): net.Error!void
Stream.Shutdown(&self, io: Io, how: Shutdown): net.Error!void
Stream.LocalAddr(&self): Addr
Stream.PeerAddr(&self): Addr
Stream.Conn(self): Conn
net.ListenWith(io: Io, addr: str, o: ListenOptions): net.Error!Listener
net.ListenUnix(io: Io, path: str): net.Error!Listener
net.DialUnix(io: Io, path: str): net.Error!Stream
Stream.SendHandles(&self, io: Io, data: []u8, handles: []Pass): net.Error!void
Stream.RecvHandles(&self, a: &Alloc, io: Io, buf: mut []u8, into: mut HeapList[Passed]): net.Error!int
Passed.Close(self)
// Passed.Adopt takes an rt.Passed, which no program can build: std.net's RecvHandles and proc.Inherited call it
Passed.Adopt(p: rt.Passed): Passed
Conn.Read(&self, io: Io, buf: mut []u8): net.Error!int
Conn.WriteAll(&self, io: Io, data: []u8): net.Error!void
Conn.Shutdown(&self, io: Io, how: Shutdown): net.Error!void
Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Conn.PeerAddr(&self): ?Addr
Conn.Close(self, io: Io): net.Error!void
Conn.Abandon(self)
// Provisional: NewConn, Transport, ReserveConn and ConnSlot
net.NewConn[T: Transport + Send + Share](h: &Heap, t: T): OutOfMemory!Conn from h, t
net.ReserveConn[T: Transport + Send + Share](h: &Heap): OutOfMemory!ConnSlot[T] from h
ConnSlot[T].Fill(self, t: T): Conn from self, t
ConnSlot[T].Free(self, a: &Alloc)

interface Transport {
    Read(&self, io: Io, buf: mut []u8): net.Error!int
    WriteAll(&self, io: Io, data: []u8): net.Error!void
    Shutdown(&self, io: Io, how: Shutdown): net.Error!void
    PeerAddr(&self): ?Addr
    Close(&self, io: Io): net.Error!void
    Abandon(self)
}

// net.Secure is made by std.tls and passed on, never built by a program; Of takes a core.ErasedCall, so only std calls it, and std.http and std.websocket call Connect
Secure.Of[T: Share](c: core.ErasedCall): Secure from c
Secure.Connect(&self, h: &Heap into conn, io: Io, conn: Conn, host: str): net.Error!Conn from h, conn

struct ListenOptions {
    export backlog: int
    export reuse_addr: bool
    export no_delay: bool
}

// Shutdown may gain members
enum Shutdown {
    Read
    Write
    Both
}

// Pass and Passed may gain members
enum Pass {
    File(f: &fs.File)
    Stream(s: &Stream)
    Listener(l: &Listener)
}

enum Passed {
    File(f: fs.File)
    Stream(s: Stream)
    Listener(l: Listener)
}

// SecureFailure may gain members
enum SecureFailure {
    Handshake
    BadCertificate
    UnknownAuthority
    Expired
    NameMismatch
    Protocol
}

struct Addr {
    export ip: [16]u8
    export port: u16
    export v6: bool
}

// std.proc. Signal below is proc.Signal
proc.WaitSignal(io: Io, s: Signal): Canceled!void
proc.Exit(code: int): never

// Signal may gain members
enum Signal {
    Interrupt
    Terminate
    Hangup
}

error NotFound
error Denied
error TooLarge
error Os(code: i64)
error Unsupported
type Error = NotFound | Denied | TooLarge | Os | Canceled | OutOfMemory | Unsupported
proc.Run(a: &Arena, io: Io, args: []str, input: []u8, limit: int): proc.Error!Output from a
proc.Supervise[E](sys: &System, workers: int, l: &net.Listener, const worker: fn(&System, &net.Listener): E!void): E|proc.Error!void
proc.IsWorker(sys: &System): bool
proc.Pipe(io: Io): proc.Error!(r: PipeReader, w: PipeWriter)
PipeReader.Read(&self, io: Io, buf: mut []u8): proc.Error!int
PipeReader.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
PipeReader.Close(self)
PipeWriter.WriteAll(&self, io: Io, data: []u8): proc.Error!void
PipeWriter.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
PipeWriter.Close(self)
proc.Spawn(a: &Arena, io: Io, c: &Command): proc.Error!(child: Child, stdin: ?PipeWriter, stdout: ?PipeReader, stderr: ?PipeReader)
proc.Inherited(a: &Alloc, io: Io, into: mut HeapList[net.Passed]): proc.Error!int
Child.Wait(self, io: Io): proc.Error!Status
Child.Signal(&self, s: Signal): proc.Error!void
Child.Kill(&self): proc.Error!void
Child.Pid(&self): i64
Child.Detach(self)
// Provisional: Pty and Command.terminal
Pty.Open(io: Io, cols: int, rows: int): proc.Error!Pty
Pty.Resize(&self, io: Io, cols: int, rows: int): proc.Error!void
Pty.Read(&self, io: Io, buf: mut []u8): proc.Error!int
Pty.WriteAll(&self, io: Io, data: []u8): proc.Error!void
Pty.Close(self)

struct Command {
    export args: []str
    export env: Env
    export dir: ?str
    export stdin: Stdio
    export stdout: Stdio
    export stderr: Stdio
    export group: bool
    export terminal: ?&Pty
    export handles: []Pass
}

// Pass may gain members
enum Pass {
    File(f: &fs.File)
    Stream(s: &net.Stream)
    Listener(l: &net.Listener)
    Reader(r: &PipeReader)
    Writer(w: &PipeWriter)
}

enum Env {
    Inherit
    Replace(vars: []str)
    Extend(vars: []str)
}

enum Stdio {
    Inherit
    Null
    Pipe
    File(f: &fs.File)
    Reader(r: &PipeReader)
    Writer(w: &PipeWriter)
    JoinStdout
}

struct Status {
    export code: ?int
    export signal: ?Signal
}

struct Output {
    export code: int
    export stdout: []u8
    export stderr: []u8
}

// std.sync. Shared below is sync.Shared
Shared[T].New(h: &Heap, x: T): OutOfMemory!Shared[T] from h, x
Shared[T].Clone(&self): Shared[T] from self.ptr
Shared[T].Ref(&self): &T
Shared[T].Free(self, h: &Heap)

Mutex[T].New(x: T): Mutex[T]
Mutex[T].Lock(&self, io: Io): Canceled!Guard[T]
Mutex[T].Into(self): T
Guard[T].Ref(&self): &T
Guard[T].Mut(mut self): mut T
Guard[T].Store(mut self, x: T into self)
Guard[T].Unlock(self)
Chan[T].New(a: &Alloc, capacity: int): OutOfMemory!Chan[T]
Chan[T].Send(&self, io: Io, x: T into self): Canceled|Closed!void
Chan[T].TrySend(&self, x: T into self): Closed!?T
Chan[T].Recv(&self, io: Io): Canceled|Closed!T
Chan[T].Close(&self)
Chan[T].Free(self, a: &Alloc)
Chan[T].FreeEmpty(self, a: &Alloc)
Chan[T].FreeEach(self, a: &Alloc, const release: fn(T))
RwLock[T].New(x: T): RwLock[T]
RwLock[T].Lock(&self, io: Io): Canceled!Guard[T]
RwLock[T].LockRead(&self, io: Io): Canceled!ReadGuard[T]
RwLock[T].Into(self): T
ReadGuard[T].Ref(&self): &T
ReadGuard[T].Unlock(self)
sync.Select[R: Fields[Selectable] + Plain](io: Io, chans: R): Canceled!int
Semaphore.New(permits: int): Semaphore
Semaphore.Acquire(&self, io: Io, n: int): Canceled!void
Semaphore.TryAcquire(&self, n: int): bool
Semaphore.Release(&self, n: int)
Notify.New(): Notify
Notify.NotifyOne(&self)
Notify.NotifyAll(&self)
Notify.Wait(&self, io: Io): Canceled!void
Barrier.New(n: int): Barrier
Barrier.Wait(&self, io: Io): Canceled!bool
Once[T].New(): Once[T]
Once[T].Get(&self, io: Io, const init: fn(): T): Canceled!&T from self
Once[T].Into(self): ?T

interface Selectable {
    Ready(&self): bool
}

// std.atomic
AtomicInt.New(n: i64): AtomicInt
AtomicInt.Add(&self, n: i64): i64
AtomicInt.Load(&self): i64
AtomicInt.Store(&self, n: i64)
AtomicBool.New(b: bool): AtomicBool
AtomicBool.Load(&self): bool
AtomicBool.Store(&self, b: bool)
AtomicBool.Swap(&self, b: bool): bool
Atomic[T].New(x: T): Atomic[T]
Atomic[T].Load(&self, const o: Order): T
Atomic[T].Store(&self, x: T, const o: Order)
Atomic[T].Swap(&self, x: T, const o: Order): T
Atomic[T].CompareExchange(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
Atomic[T].CompareExchangeWeak(&self, old: T, new: T, const success: Order, const failure: Order): (ok: bool, value: T)
Atomic[T].FetchAdd(&self, x: T, const o: Order): T
Atomic[T].FetchSub(&self, x: T, const o: Order): T
Atomic[T].FetchAnd(&self, x: T, const o: Order): T
Atomic[T].FetchOr(&self, x: T, const o: Order): T
Atomic[T].FetchXor(&self, x: T, const o: Order): T
// Wait and Wake: on any T of 4 bytes (futex and WaitOnAddress compare bits), else E0706
Atomic[T].Wait(&self, io: Io, expected: T): Canceled!void
Atomic[T].Wake(&self, all: bool)
Spin[T].New(x: T): Spin[T]
Spin[T].Lock(&self): SpinGuard[T]
Spin[T].Into(self): T
SpinGuard[T].Ref(&self): &T
SpinGuard[T].Mut(mut self): mut T
SpinGuard[T].Store(mut self, x: T into self)
SpinGuard[T].Unlock(self)
const Relaxed: Order = Order(v: 0)
const Acquire: Order = Order(v: 1)
const Release: Order = Order(v: 2)
const AcqRel: Order = Order(v: 3)
const SeqCst: Order = Order(v: 4)
Order.CanLoad(self): bool
Order.CanStore(self): bool
Order.CanFence(self): bool
Order.CanModify(self): bool
atomic.Fence(const o: Order)
const Unordered: Order = Order(v: 5)
atomic.Pause()

// std.time. Duration and Instant below are time.Duration and time.Instant
time.Seconds(n: i64): Duration
time.Millis(n: i64): Duration
Duration.Nanos(self): i64
time.Now(io: Io): Instant
Instant.Since(self, earlier: Instant): Duration
time.Minutes(n: i64): Duration
time.Hours(n: i64): Duration
time.Micros(n: i64): Duration
time.Nanos(n: i64): Duration
Duration.Format(&self, w: mut Writer): WriteError!void
time.ParseDuration(s: str): ParseError!Duration
time.Wall(io: Io): Time
time.FromUnix(seconds: i64, nanos: i64): Time
Time.Unix(self): i64
Time.UnixNanos(self): i64
Time.Add(self, d: Duration): Time
Time.Sub(self, t: Time): Duration
Time.In(self, z: &Zone): Date
Time.Format(&self, w: mut Writer): WriteError!void
Date.Time(self): Time
time.FromDate(year: i32, month: int, day: int, hour: int, minute: int, second: int, nanos: int, z: &Zone): time.Error!Time
Zone.Utc(): Zone
Zone.Fixed(offset_seconds: i32): Zone
Zone.FromTzif(a: &Arena, name: str, data: []u8): time.Error|OutOfMemory!Zone from a
Zone.Load(a: &Arena, io: Io, name: str, embedded: ?Database): time.Error|OutOfMemory!Zone from a
Zone.Local(a: &Arena, io: Io): time.Error|OutOfMemory!Zone from a
time.LocalName(a: &Arena, io: Io): ?str from a
time.ParseRfc3339(s: str): ParseError!Time
time.ParseHttpDate(s: str): ParseError!Time
time.FormatHttpDate(a: &Arena, t: Time): OutOfMemory!str from a

error TimedOut
error BadName
error BadData
error NotFound
error NoDatabase
error Skipped
error Repeated
error OutOfRange
type Error = BadName | BadData | NotFound | NoDatabase | Skipped | Repeated | OutOfRange | Canceled

struct Date {
    export year: i32
    export month: int
    export day: int
    export hour: int
    export minute: int
    export second: int
    export nanos: int
    export weekday: int
    export year_day: int
    export offset_seconds: i32
}

struct Database {
    export find: fn(str): ?[]u8
}

// std.mmio. Reg below is mmio.Reg
mmio.Load[T: Plain](p: *T): T
mmio.Store[T: Plain](p: *T, x: T)
mmio.Fence()
Reg[T].New(addr: uint): Reg[T]
Reg[T].Read(&self): T
Reg[T].Write(&self, x: T)

// std.math. Digits, DigitMode and Integer below are math's
math.AddOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
math.SubOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
math.MulOverflow[T: Integer](a: T, b: T): (value: T, overflow: bool)
math.WrapI32(x: f64): i32
math.WrapU32(x: f64): u32
math.CastSat[T: Integer](x: f64): T
math.CastSatF32[T: Integer](x: f32): T
math.MulWide(a: u64, b: u64): (hi: u64, lo: u64)
math.DivWide(hi: u64, lo: u64, d: u64): (q: u64, r: u64)
math.AddCarry(x: u64, y: u64, c: u64): (sum: u64, carry: u64)
math.SubBorrow(x: u64, y: u64, b: u64): (diff: u64, borrow: u64)
math.Min[T: Ord + Plain](a: T, b: T): T
math.Max[T: Ord + Plain](a: T, b: T): T
math.Abs(x: f64): f64
math.Sqrt(x: f64): f64
Rng.New(seed: u64): Rng
Rng.FromIo(io: Io): Canceled!Rng
Rng.U64(mut self): u64
Rng.Below(mut self, n: u64): u64
Rng.Float(mut self): f64
Rng.Fill(mut self, buf: mut []u8)
math.Floor(x: f64): f64
math.Ceil(x: f64): f64
math.Round(x: f64): f64
math.RoundEven(x: f64): f64
math.RoundToZero(x: f64): f64
math.Fract(x: f64): f64
math.Mod(x: f64, y: f64): f64
math.Pow(x: f64, y: f64): f64
math.Exp(x: f64): f64
math.Exp2(x: f64): f64
math.Expm1(x: f64): f64
math.Log(x: f64): f64
math.Log2(x: f64): f64
math.Log10(x: f64): f64
math.Log1p(x: f64): f64
math.Sin(x: f64): f64
math.Cos(x: f64): f64
math.Tan(x: f64): f64
math.Asin(x: f64): f64
math.Acos(x: f64): f64
math.Atan(x: f64): f64
math.Atan2(y: f64, x: f64): f64
math.Sinh(x: f64): f64
math.Cosh(x: f64): f64
math.Tanh(x: f64): f64
math.Asinh(x: f64): f64
math.Acosh(x: f64): f64
math.Atanh(x: f64): f64
math.Hypot(x: f64, y: f64): f64
math.Cbrt(x: f64): f64
math.Fma(x: f64, y: f64, z: f64): f64
math.CopySign(x: f64, sign: f64): f64
math.FrExp(x: f64): (frac: f64, exp: int)
math.LdExp(frac: f64, exp: int): f64
math.NextAfter(x: f64, toward: f64): f64
math.IsNan(x: f64): bool
math.IsInf(x: f64): bool
math.IsFinite(x: f64): bool
math.SqrtF32(x: f32): f32
math.FloorF32(x: f32): f32
math.CeilF32(x: f32): f32
math.RoundF32(x: f32): f32
math.RoundEvenF32(x: f32): f32
math.RoundToZeroF32(x: f32): f32
math.FractF32(x: f32): f32
math.ModF32(x: f32, y: f32): f32
math.PowF32(x: f32, y: f32): f32
math.ExpF32(x: f32): f32
math.Exp2F32(x: f32): f32
math.Expm1F32(x: f32): f32
math.LogF32(x: f32): f32
math.Log2F32(x: f32): f32
math.Log10F32(x: f32): f32
math.Log1pF32(x: f32): f32
math.SinF32(x: f32): f32
math.CosF32(x: f32): f32
math.TanF32(x: f32): f32
math.AsinF32(x: f32): f32
math.AcosF32(x: f32): f32
math.AtanF32(x: f32): f32
math.Atan2F32(y: f32, x: f32): f32
math.SinhF32(x: f32): f32
math.CoshF32(x: f32): f32
math.TanhF32(x: f32): f32
math.AsinhF32(x: f32): f32
math.AcoshF32(x: f32): f32
math.AtanhF32(x: f32): f32
math.HypotF32(x: f32, y: f32): f32
math.CbrtF32(x: f32): f32
math.FmaF32(x: f32, y: f32, z: f32): f32
math.CopySignF32(x: f32, sign: f32): f32
math.FrExpF32(x: f32): (frac: f32, exp: int)
math.LdExpF32(frac: f32, exp: int): f32
math.NextAfterF32(x: f32, toward: f32): f32
math.IsNanF32(x: f32): bool
math.IsInfF32(x: f32): bool
math.IsFiniteF32(x: f32): bool
math.LeadingZeros64(x: u64): int
math.LeadingZeros32(x: u32): int
math.LeadingZeros16(x: u16): int
math.LeadingZeros8(x: u8): int
math.TrailingZeros64(x: u64): int
math.TrailingZeros32(x: u32): int
math.TrailingZeros16(x: u16): int
math.TrailingZeros8(x: u8): int
math.OnesCount64(x: u64): int
math.OnesCount32(x: u32): int
math.OnesCount16(x: u16): int
math.OnesCount8(x: u8): int
math.RotateLeft64(x: u64, n: int): u64
math.RotateLeft32(x: u32, n: int): u32
math.RotateLeft16(x: u16, n: int): u16
math.RotateLeft8(x: u8, n: int): u8
math.RotateRight64(x: u64, n: int): u64
math.RotateRight32(x: u32, n: int): u32
math.RotateRight16(x: u16, n: int): u16
math.RotateRight8(x: u8, n: int): u8
math.ReverseBits64(x: u64): u64
math.ReverseBits32(x: u32): u32
math.ReverseBits16(x: u16): u16
math.ReverseBits8(x: u8): u8
math.ReverseBytes64(x: u64): u64
math.ReverseBytes32(x: u32): u32
math.ReverseBytes16(x: u16): u16
math.Gcd(a: u64, b: u64): u64
math.CheckedPow(x: i64, n: u32): ?i64
math.CheckedPowU64(x: u64, n: u32): ?u64
math.ShortestDigits(x: f64): Digits
math.ExactDigits(x: f64, n: int, mode: DigitMode): Digits

struct Digits {
    export digits: [768]u8
    export len: int
    export exp: int
    export neg: bool
}

enum DigitMode {
    Significant
    Fraction
    SignificantHalfUp
    FractionHalfUp
}

interface Integer {
}

// std.simd. Vec, Mask, Lane, CpuFeatures and Choice below are simd.Vec, simd.Mask, simd.Lane, simd.CpuFeatures and simd.Choice
Vec[T, N].Splat(x: T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Zero(): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Iota(): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].FromArray(a: [N]T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ToArray(self): [N]T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].FromSlice(xs: []T, i: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].FromSliceOr(xs: []T, i: int, fill: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].CopyTo(self, xs: mut []T, i: int) requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].CopyToMasked(self, xs: mut []T, i: int, m: Mask[T, N]) requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Gather[I: Lane](xs: []T, idx: Vec[I, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Lane(self, const i: int): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
Vec[T, N].With(self, const i: int, x: T): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
Vec[T, N].Shuffle(self, b: Vec[T, N], const idx: [N]int): Vec[T, N] from self, b requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Reverse(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Broadcast(self, const i: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires i >= 0 && i < N
Vec[T, N].Rotate(self, const k: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires k >= 0 && k < N
Vec[T, N].Slide(self, prev: Vec[T, N], const k: int): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires k >= 0 && k <= N
Vec[T, N].InterleaveLo(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].InterleaveHi(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].DeinterleaveEven(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].DeinterleaveOdd(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceSum(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceSumWrap(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceSumWide(self): i64 requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceMin(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceMax(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceAnd(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceOr(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ReduceXor(self): T requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Min(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Max(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Abs(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].AbsWrap(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].AddSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].SubSat(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].MulHigh(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].DivTrunc(self, b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ShlEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].ShrEach(self, k: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Fma(self, b: Vec[T, N], c: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Sqrt(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Floor(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Ceil(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].RoundEven(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].RoundToZero(self): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].WidenLo[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
Vec[T, N].WidenHi[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
Vec[T, N].MulWidenLo[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
Vec[T, N].MulWidenHi[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * 2 == N && M >= 2 && SizeOf[U]() == 2 * SizeOf[T]()
Vec[T, N].NarrowSat[U: Lane, const M: int](self, b: Vec[T, N]): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M == 2 * N && M <= 64 && 2 * SizeOf[U]() == SizeOf[T]()
Vec[T, N].Cast[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Trunc[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].CastSat[U: Lane](self): Vec[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Vec[T, N].Reinterpret[U: Lane, const M: int](self): Vec[U, M] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64 requires M * SizeOf[U]() == N * SizeOf[T]() && M >= 2 && M <= 64
Mask[T, N].FromBits(b: u64): Mask[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].Bits(self): u64 requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].Any(self): bool requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].All(self): bool requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].CountSet(self): int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].FirstSet(self): ?int requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].Select(self, a: Vec[T, N], b: Vec[T, N]): Vec[T, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
Mask[T, N].Cast[U: Lane](self): Mask[U, N] requires N == 2 || N == 4 || N == 8 || N == 16 || N == 32 || N == 64
simd.Lookup16[const N: int](idx: Vec[u8, N], table: Vec[u8, 16]): Vec[u8, N] requires N == 16 || N == 32 || N == 64
simd.Lanes[T: Lane](): int
simd.Cpu(): CpuFeatures
CpuFeatures.Of(const names: str): CpuFeatures
CpuFeatures.Has(&self, const name: str): bool
CpuFeatures.HasAll(&self, other: &CpuFeatures): bool
simd.Dispatch[F: Plain](choices: []Choice[F], base: F): F
simd.IndexByte(s: []u8, b: u8): ?int
simd.IndexAny(s: []u8, set: []u8): ?int
simd.Count(s: []u8, b: u8): int
simd.Equal(a: []u8, b: []u8): bool
simd.Compare(a: []u8, b: []u8): int
simd.ValidUtf8(s: []u8): bool
simd.ValidWtf8(s: []u8): bool
simd.IsAscii(s: []u8): bool
simd.Utf16Len(s: str): int
simd.Utf16LenBytes(b: []u8): int
simd.LastIndexByte(s: []u8, b: u8): ?int
simd.Dot[T: Lane](a: []T, b: []T): T
simd.Sum[T: Lane](xs: []T): T

interface Lane {
}

struct Choice[F] {
    export needs: CpuFeatures
    export f: F
}

// std.free, on x86_64-freestanding only
free.Heap(): &Heap

struct PanicInfo {
    export code: u32
    export message: str
    export file: str
    export line: int
    export column: int
}

// std.mem. AllocTable below is mem.AllocTable
type AllocTable = core.AllocTable
mem.OffsetOf[T](field: str): int
mem.Prefetch[T](p: &T, write: bool)
mem.Zeroed[T](): T
Fixed.New(buf: mut []u8): Fixed
Fixed.Alloc(&self): &Alloc from self
Fixed.Reset(mut self) ends self
FixedHeap.New(buf: mut []u8): FixedHeap
FixedHeap.Alloc(&self): &Alloc from self
FixedHeap.Reset(mut self) ends self
Fallback.New(inner: &Alloc, buf: mut []u8): Fallback from inner, buf
Fallback.Alloc(&self): &Alloc from self
Fallback.Reset(mut self) ends self
Counting.New(inner: &Alloc): Counting
Counting.Alloc(&self): &Alloc from self
Counting.Bytes(&self): int
Counting.Live(&self): int
Counting.Peak(&self): int
Limit.New(inner: &Alloc, max: int): Limit
Limit.Alloc(&self): &Alloc from self
Limit.Bytes(&self): int
Limit.Left(&self): int
Failing.New(inner: &Alloc, at: int): Failing
Failing.NewFrom(inner: &Alloc, at: int): Failing
Failing.Alloc(&self): &Alloc from self
Failing.Requests(&self): int
Failing.Failed(&self): int
mem.Null(): &Alloc
mem.Custom(ctx: *u8, table: &AllocTable): Alloc from table
Pool[T].New(a: &Arena): Pool[T]
Pool[T].Add(mut self, x: T into self): OutOfMemory!Handle
Pool[T].Get(&self, h: Handle): ?&T
Pool[T].GetMut(mut self, h: Handle): ?mut T
Pool[T].Free(mut self, h: Handle): bool
Pool[T].Len(&self): int
Pool[T].Iter(&self): PoolIter[T] from self
PoolIter[T].Next(mut self): ?(handle: Handle, value: &T) from self.pool
Pool[T].Sweep(mut self, const keep: fn(Handle, &T): bool): int
Pool[T].GetPair(mut self, h: Handle, k: Handle): ?(a: mut T, b: mut T) from self

// std.coll
Deque[T].New(a: &Arena): Deque[T]
Deque[T].Push(mut self, x: T into self): OutOfMemory!void
Deque[T].PushFront(mut self, x: T into self): OutOfMemory!void
Deque[T].Pop(mut self): ?T
Deque[T].PopFront(mut self): ?T
Deque[T].Get(&self, i: int): ?&T
Deque[T].GetMut(mut self, i: int): ?mut T
Deque[T].Len(&self): int
HeapDeque[T].New(a: &Alloc): HeapDeque[T]
HeapDeque[T].Push(mut self, a: &Alloc, x: T into self): OutOfMemory!void
HeapDeque[T].PushFront(mut self, a: &Alloc, x: T into self): OutOfMemory!void
HeapDeque[T].Pop(mut self): ?T
HeapDeque[T].PopFront(mut self): ?T
HeapDeque[T].Get(&self, i: int): ?&T
HeapDeque[T].Len(&self): int
HeapDeque[T].Free(self, a: &Alloc)
HeapDeque[T].FreeEmpty(self, a: &Alloc)
HeapDeque[T].FreeEach(self, a: &Alloc, const release: fn(T))
PriorityQueue[T].New(a: &Arena): PriorityQueue[T]
PriorityQueue[T].Push(mut self, x: T into self): OutOfMemory!void
PriorityQueue[T].Pop(mut self): ?T
PriorityQueue[T].Peek(&self): ?&T
PriorityQueue[T].Len(&self): int
BTree[K, V].New(a: &Arena): BTree[K, V]
BTree[K, V].Get(&self, k: &K): ?&V
BTree[K, V].GetMut(mut self, k: &K): ?mut V
BTree[K, V].Set(mut self, k: K into self, v: V into self): OutOfMemory!void
BTree[K, V].Replace(mut self, k: K into self, v: V into self): OutOfMemory!?V
BTree[K, V].Has(&self, k: &K): bool
BTree[K, V].Remove(mut self, k: &K): ?V
BTree[K, V].Len(&self): int
BTree[K, V].Iter(&self): BTreeIter[K, V]
BTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self
BTreeIter[K, V].Next(mut self): ?(key: &K, value: &V) from self.tree
HeapBTree[K, V].New(a: &Alloc): HeapBTree[K, V]
HeapBTree[K, V].Get(&self, k: &K): ?&V
HeapBTree[K, V].GetMut(mut self, k: &K): ?mut V
HeapBTree[K, V].Set(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!void
HeapBTree[K, V].Replace(mut self, a: &Alloc, k: K into self, v: V into self): OutOfMemory!?V
HeapBTree[K, V].Has(&self, k: &K): bool
HeapBTree[K, V].Remove(mut self, k: &K): ?V
HeapBTree[K, V].Len(&self): int
HeapBTree[K, V].Iter(&self): BTreeIter[K, V]
HeapBTree[K, V].Range(&self, lo: &K, hi: &K): BTreeIter[K, V] from self
HeapBTree[K, V].Free(self, a: &Alloc)
HeapBTree[K, V].FreeEmpty(self, a: &Alloc)
HeapBTree[K, V].FreeEach(self, a: &Alloc, const release: fn(K, V))
Bitset.New(a: &Arena, bits: int): OutOfMemory!Bitset
Bitset.Set(mut self, i: int)
Bitset.Remove(mut self, i: int)
Bitset.Has(&self, i: int): bool
Bitset.Count(&self): int
Bitset.Len(&self): int

// std.text. Category, Script and Property below are text's, generated from the UCD
text.Graphemes(s: str): Clusters
Clusters.Next(mut self): ?str from self.text
text.Normalize(a: &Arena, s: str, form: Form): OutOfMemory!str from a
text.FoldCase(a: &Arena, s: str): OutOfMemory!str from a
text.Width(s: str): int
text.ToUtf16(a: &Arena, s: str): OutOfMemory![]u16 from a
text.FromUtf16(a: &Arena, units: []u16): Utf8Error|OutOfMemory!str from a
text.WordBounds(s: str): Bounds
text.SentenceBounds(s: str): Bounds
Bounds.Next(mut self): ?int
text.ParagraphBounds(s: str): Bounds
text.BidiLevels(a: &Arena, s: str, dir: Direction): OutOfMemory!Levels from a, s
Levels.Base(&self): u8
Levels.At(&self, i: int): u8
Levels.Line(&self, a: &Arena, start: int, end: int): OutOfMemory![]Run from a
text.Mirror(c: char): ?char
text.LineBreaks(s: str): Breaks
Breaks.Next(mut self): ?(at: int, mandatory: bool)
Capped.New(buf: mut []u8): Capped from buf
Capped.Writer(mut self): Writer from self
Capped.Bytes(&self): []u8 from self
Capped.Dropped(&self): int
Capped.Reset(mut self)
Sink[S].New(write: fn(mut S, []u8): WriteError!void): Sink[S]
Sink[S].Writer(&self, s: mut S, buf: mut []u8): Writer from self, s, buf

enum Form {
    Nfc
    Nfd
    Nfkc
    Nfkd
}

// Direction may gain members
enum Direction {
    Auto
    Ltr
    Rtl
}

struct Run {
    export start: int
    export end: int
    export level: u8
}
text.CategoryOf(c: char): Category
text.ScriptOf(c: char): Script
text.ScriptExtensions(c: char): []Script
text.Has(c: char, p: Property): bool
text.UnicodeVersion(): str

// std.hash
hash.Xx(data: []u8): u64
hash.Xx64(data: []u8, seed: u64): u64
hash.Crc32(data: []u8): u32
hash.Sip13(data: []u8, key: HashKey): u64
hash.Sha256(data: []u8): [32]u8
hash.Sha512(data: []u8): [64]u8
hash.Blake3(data: []u8): [32]u8
Crc32State.New(): Crc32State
Crc32State.Bytes(mut self, data: []u8)
Crc32State.Sum(self): u32
Xx64State.New(seed: u64): Xx64State
Xx64State.Bytes(mut self, data: []u8)
Xx64State.Sum(self): u64
Sip13State.New(key: HashKey): Sip13State
Sip13State.Bytes(mut self, data: []u8)
Sip13State.Sum(self): u64
Sha256State.New(): Sha256State
Sha256State.Bytes(mut self, data: []u8)
Sha256State.Sum(self): [32]u8
Sha512State.New(): Sha512State
Sha512State.Bytes(mut self, data: []u8)
Sha512State.Sum(self): [64]u8
Blake3State.New(): Blake3State
Blake3State.Bytes(mut self, data: []u8)
Blake3State.Sum(self): [32]u8
hash.Sha1(data: []u8): [20]u8
hash.Sha384(data: []u8): [48]u8
Sha1State.New(): Sha1State
Sha1State.Bytes(mut self, data: []u8)
Sha1State.Sum(self): [20]u8
Sha384State.New(): Sha384State
Sha384State.Bytes(mut self, data: []u8)
Sha384State.Sum(self): [48]u8

// std.json. Value, Member and Error below are json.Value, json.Member and json.Error
error Syntax(offset: int)
error Mismatch(offset: int)
error Depth(offset: int)
type Error = Syntax | Mismatch | Depth | OutOfMemory
json.Parse(a: &Arena, text: str into a): json.Error!Value from a, text
json.Codec(g: mut Gen, t: Type)
Value.Get(&self, key: str): ?&Value
Value.Int(&self): ?i64
Value.Float(&self): ?f64
Value.Str(&self): ?str
Value.Format(&self, w: mut Writer): WriteError!void

enum Value {
    Null
    Bool(b: bool)
    Number(text: str)
    Text(s: str)
    Array(items: List[Value])
    Object(members: List[Member])
}

struct Member {
    export key: str
    export value: Value
}

interface Json {
    FormatJson(&self, w: mut Writer): WriteError!void
}

// std.http. Request, Response and Error below are http.Request, http.Response and http.Error
error BadRequest(offset: int)
error TooLarge
error Status(code: int)
error NoSecureTransport
error BodyNotReplayable
error TooManyRedirects
error BadUrl
error ProxyRefused(code: int)
type Error = BadRequest | TooLarge | Status | net.Error | OutOfMemory | ReadError | WriteError | NoSecureTransport | BodyNotReplayable | TooManyRedirects | BadUrl | ProxyRefused
http.ReadRequest(a: &Arena, io: Io, r: mut Reader, limit: int): http.Error!Request from a
http.Serve[C: Share](sys: &System, addr: str, limit: int, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): http.Error!void)): http.Error!void
http.Fetch(a: &Arena, io: Io, method: str, url: str, headers: []Header, body: []u8, limit: int): http.Error!Reply from a
Request.Header(&self, name: str): ?str from self
Request.Param(&self, name: str): ?str from self
Request.ParamInt(&self, name: str): ?i64
Response.New(a: &Arena): Response
Response.Status(mut self, code: int)
Response.Header(mut self, name: str, value: str): OutOfMemory!void
Response.Body(mut self, data: []u8): OutOfMemory!void
Response.Writer(mut self): Writer from self
Response.Send(&self, io: Io, w: mut Writer): http.Error!void
http.Dispatch[C](a: &Arena, io: Io, routes: []Route[C], c: &C, req: &Request, res: mut Response): http.Error!void *routes into a
Client.New(h: &Heap, l: ClientLimits): Client from h, l
Client.Do(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Exchange from a, self
Client.Fetch(&self, a: &Arena, io: Io, req: &ClientRequest): http.Error!Reply from a
Client.DoStreaming(&self, a: &Arena, io: Io, req: &ClientRequest, body: mut Reader, len: ?i64): http.Error!Exchange from a, self
Client.Close(self)
Exchange.Body(mut self, buf: mut []u8): Reader from self, buf
Exchange.Close(self, io: Io): http.Error!void
Exchange.Abandon(self)
http.ServeWith[C: Share](sys: &System, addr: str, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void)): http.Error!void
http.ServeOn[C: Share](h: &Heap, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, handle: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void)): http.Error!void
http.ServeChan[C: Share, T: Send](h: &Heap into ch, io: Io, l: &net.Listener, o: &ServerOptions, c: &C, ch: &Chan[T], handle: (fn(c: &C, h: &Heap into ch, ch: &Chan[T], a: &Arena, io: Io, req: &Request, res: mut Response, link: &Link): http.Error!void)): http.Error!void
Response.Flush(mut self, io: Io, link: &Link): http.Error!void
Link.Takeover(&self): ?Taken
Taken.Abandon(self)

struct ClientRequest {
    export method: str
    export url: &url.Url
    export headers: []Header
    export body: []u8
}

linear struct Exchange {
    export status: int
    export headers: []Header
}

struct ClientLimits {
    export connect: time.Duration
    export response_header: time.Duration
    export idle: time.Duration
    export close: time.Duration
    export header_bytes: int
    export body_bytes: i64
    export redirects: int
    export idle_connections: int
    export decoders: []compress.Content
    export allow: ?fn(&net.Addr): bool
    export secure: ?net.Secure
    export proxy: ?&url.Url
}

struct ServerOptions {
    export connections: int
    export header_bytes: int
    export body_bytes: int
    export request_header: time.Duration
    export keep_alive: time.Duration
    export secure: ?net.Secure
}

struct Header {
    export name: str
    export value: str
}

struct Request {
    export method: str
    export path: str
    export query: str
    export headers: []Header
    export params: []Header
    export body: []u8
}

struct Reply {
    export status: int
    export headers: []Header
    export body: []u8
}

linear struct Taken {
    export conn: net.Conn
    export rest: []u8
}

struct Route[C] {
    export method: str
    export pattern: str
    export handler: (fn(c: &C, a: &Arena, io: Io, req: &Request, res: mut Response): http.Error!void)
}

// std.regex. Regex and Error below are regex.Regex and regex.Error
error Syntax(offset: int)
error TooLarge
type Error = Syntax | TooLarge | OutOfMemory
regex.Compile(a: &Arena, pattern: str): regex.Error!Regex from a
regex.Matcher(g: mut Gen, t: Type, pattern: str)
Regex.IsMatch(&self, s: str): bool
Regex.Find(&self, s: str): ?Match
Regex.FindAll(&self, a: &Arena, s: str): OutOfMemory!List[Match] from a
Regex.Group(&self, s: str, m: Match, i: int): ?str from s
Regex.Replace(&self, a: &Arena, s: str, with: str): OutOfMemory!str from a

struct Match {
    export lo: int
    export hi: int
}

// std.crypto. Error below is crypto.Error
error BadTag
error BadLength
error BadKey
error Encrypted
error BadEncoding
error BadCost
type Error = BadTag | BadLength | BadKey | Encrypted | BadEncoding | BadCost
crypto.Hmac256(key: []u8, data: []u8): [32]u8
crypto.X25519(secret: &[32]u8, point: &[32]u8): [32]u8
crypto.Random(io: Io, buf: mut []u8): Canceled!void
crypto.Equal(a: []u8, b: []u8): bool
ChaCha20Poly1305.Seal(key: &[32]u8, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): crypto.Error!void
ChaCha20Poly1305.Open(key: &[32]u8, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): crypto.Error!void
Ed25519.Keypair(io: Io): Canceled![64]u8
Ed25519.FromSeed(seed: &[32]u8): [64]u8
Ed25519.Sign(secret: &[64]u8, msg: []u8): [64]u8
Ed25519.Verify(public: &[32]u8, msg: []u8, sig: &[64]u8): bool
crypto.Hmac384(key: []u8, data: []u8): [48]u8
crypto.Hmac512(key: []u8, data: []u8): [64]u8
crypto.HkdfExtract(salt: []u8, ikm: []u8, h: HashKind): Prk
crypto.HkdfExpand(prk: &Prk, info: []u8, out: mut []u8): crypto.Error!void
Prk.FromBytes(h: HashKind, key: []u8): crypto.Error!Prk
Prk.Wipe(mut self)
AesGcm.New(key: []u8): crypto.Error!AesGcm
AesGcm.Seal(&self, nonce: &[12]u8, aad: []u8, plain: []u8, out: mut []u8): crypto.Error!int
AesGcm.Open(&self, nonce: &[12]u8, aad: []u8, sealed: []u8, out: mut []u8): crypto.Error!int
AesGcm.Wipe(mut self)
EcSecret.Generate(io: Io, curve: Curve): Canceled|crypto.Error!EcSecret
EcSecret.FromBytes(curve: Curve, b: []u8): crypto.Error!EcSecret
EcSecret.Public(&self, out: mut []u8): crypto.Error!int
EcSecret.Wipe(mut self)
crypto.EcdhShared(curve: Curve, secret: &EcSecret, peer: []u8, out: mut []u8): crypto.Error!void
crypto.EcdsaSign(secret: &EcSecret, digest: []u8, format: SigFormat, out: mut []u8): crypto.Error!int
crypto.EcdsaVerify(public: []u8, curve: Curve, digest: []u8, format: SigFormat, sig: []u8): bool
RsaPublic.New(modulus: []u8, exponent: u64): crypto.Error!RsaPublic from modulus
crypto.RsaVerifyPkcs1(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool
crypto.RsaVerifyPss(public: &RsaPublic, h: HashKind, digest: []u8, sig: []u8): bool
crypto.ParsePublicKey(der: []u8): crypto.Error!PublicKey from der
crypto.ParsePrivateKey(der: []u8): crypto.Error!PrivateKey
crypto.Wipe(buf: mut []u8)
crypto.Argon2id(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, out: mut []u8): crypto.BadLength|crypto.BadCost|OutOfMemory!void
crypto.Argon2idVerify(h: &Heap, c: Argon2Cost, password: []u8, salt: []u8, secret: []u8, ad: []u8, tag: []u8): crypto.BadLength|crypto.BadCost|OutOfMemory!bool

struct Argon2Cost {
    export memory: int
    export passes: int
    export lanes: int
}

// Curve and HashKind may gain members
enum Curve {
    P256
    P384
}

enum HashKind {
    Sha256
    Sha384
    Sha512
}

enum SigFormat {
    Der
    Fixed
}

enum PublicKey {
    Ec(curve: Curve, point: []u8)
    Rsa(key: RsaPublic)
    Ed25519(key: [32]u8)
}

enum PrivateKey {
    Ec(secret: EcSecret)
    Ed25519(seed: [32]u8)
}

// std.compress. Inflater and Error below are compress.Inflater and compress.Error
error Corrupt(offset: int)
error Truncated
error TooLarge
type Error = Corrupt | Truncated | TooLarge | OutOfMemory
compress.Inflate(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a
compress.Gunzip(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a
compress.Deflate(a: &Arena, data: []u8): OutOfMemory![]u8 from a
compress.Gzip(a: &Arena, data: []u8): OutOfMemory![]u8 from a
Inflater.New(a: &Arena): Inflater
Inflater.NewGzip(a: &Arena): Inflater
Inflater.Read(mut self, r: mut Reader, buf: mut []u8): compress.Error|ReadError!int
Inflater.NewZlib(a: &Arena): Inflater
Inflater.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
Inflater.Cause(&self): ?compress.Error
compress.Zlib(a: &Arena, data: []u8): OutOfMemory![]u8 from a
compress.Unzlib(a: &Arena, data: []u8, limit: int): compress.Error![]u8 from a
compress.Adler32(data: []u8): u32
Adler32State.New(): Adler32State
Adler32State.Bytes(mut self, data: []u8)
Adler32State.Sum(self): u32
Encoder.New(a: &Arena, framing: Framing, level: int): OutOfMemory!Encoder from a
Encoder.Write(mut self, w: mut Writer, data: []u8): WriteError!void
Encoder.Flush(mut self, w: mut Writer): WriteError!void
Encoder.Finish(self, w: mut Writer): WriteError!void
Encoder.Abandon(self)
ZstdDecoder.New(a: &Arena, window_limit: int): ZstdDecoder
ZstdDecoder.NewLazy(a: &Arena, window_limit: int): ZstdDecoder
ZstdDecoder.Read(mut self, r: mut Reader, buf: mut []u8): compress.Error|ReadError!int
ZstdDecoder.Reader(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
ZstdDecoder.ReaderLazy(mut self, r: mut Reader, buf: mut []u8): Reader from self, r, buf
ZstdDecoder.Cause(&self): ?compress.Error

enum Framing {
    Raw
    Gzip
    Zlib
}

// std.cli. Error below is cli.Error
error Unknown(arg: int)
error Missing(field: int)
error Invalid(arg: int)
type Error = Unknown | Missing | Invalid | OutOfMemory
cli.Args(g: mut Gen, t: Type)

// std.log
Logger.New(w: mut Writer, level: Level): Logger from w
Logger.NewTimed(io: Io, w: mut Writer, level: Level): Logger from w
Logger.Log[A: Fields[Format] + Plain](mut self, level: Level, const f: str, args: A): WriteError!void
Logger.Debug[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
Logger.Info[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
Logger.Warn[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
Logger.Error[A: Fields[Format] + Plain](mut self, const f: str, args: A): WriteError!void
log.Stderr(level: Level): Sink
log.Stdout(level: Level): Sink
log.ToFile(f: &Mutex[fs.File], level: Level): Sink from f
struct Sink {
    export file: ?&Mutex[fs.File]
}
Sink.Log[A: Fields[Format] + Plain](&self, io: Io, level: Level, const f: str, args: A): WriteError!void
Sink.Debug[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Info[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Warn[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void
Sink.Error[A: Fields[Format] + Plain](&self, io: Io, const f: str, args: A): WriteError!void

enum Level {
    Debug
    Info
    Warn
    Error
}

// std.dyn
dyn.Of(g: mut Gen, i: Type)
Ref.Of[T](x: mut T): Ref from x
Ref.Mut[T](mut self): mut T from self
Ref.Get[T](&self): &T from self
Ref.Is[T](&self): bool

// std.dyn.erased
Key[T](): u64

// std.sim
Sim.New(a: &Arena, seed: u64): OutOfMemory!Sim from a
Sim.Io(&self): Io
Sim.Arena(&self): &Arena from self
Sim.Advance(mut self, d: time.Duration)
Sim.Run(mut self)
Sim.CrashAfterRandomOps(mut self)
Sim.Restart(mut self): Io
Sim.FailNext(mut self, op: Op, f: Fault)
Sim.TermInput(mut self, data: []u8, after: time.Duration): OutOfMemory!void
Sim.Free(self)
Recorder.New(inner: Io): OutOfMemory!Recorder
Recorder.Io(&self): Io
Recorder.Log(&self, a: &Arena): OutOfMemory![]u8 from a
Recorder.Free(self)
Replayer.New(log: []u8): OutOfMemory!Replayer
Replayer.Io(&self): Io
Replayer.Diverged(&self): bool
Replayer.Done(&self): bool
Replayer.Free(self)

enum Op {
    Read
    Write
    Sync
    Open
    Accept
    Connect
}

enum Fault {
    NoSpace
    Denied
    Reset
    Refused
}

// std.db.sqlite. Db, Stmt and Error below are sqlite.Db, sqlite.Stmt and sqlite.Error
error Sqlite(code: i32)
error Busy
error Misuse
type Error = Sqlite | Busy | Misuse | OutOfMemory | Canceled
sqlite.Open(a: &Arena, io: Io, path: str): sqlite.Error!Db
Db.Prepare(&self, a: &Arena, io: Io, sql: str): sqlite.Error!Stmt from self, a
Db.Exec(&self, io: Io, sql: str): sqlite.Error!void
Db.Close(self)
Stmt.BindInt(mut self, i: int, v: i64): sqlite.Error!void
Stmt.BindText(mut self, i: int, s: str): sqlite.Error!void
Stmt.Step(mut self, io: Io): sqlite.Error!bool
Stmt.Int(&self, col: int): i64
Stmt.Str(&self, col: int): str from self
Stmt.Reset(mut self)
Stmt.Close(self)

// std.big. Int and Error below are big.Int and big.Error
error TooLarge
type Error = ParseError | TooLarge | OutOfMemory
big.FromI64(a: &Arena, x: i64): OutOfMemory!Int from a
big.FromBytes(a: &Arena, b: []u8): OutOfMemory!Int from a
big.FromWords(words: []u64, negative: bool): Int from words
big.Parse(a: &Arena, s: str, base: int, max_digits: int): big.Error!Int from a
Int.Clone(&self, a: &Arena): OutOfMemory!Int from a
Int.Words(&self): []u64 from self
Int.Add(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
Int.Sub(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
Int.Mul(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
Int.DivMod(&self, a: &Arena, y: &Int): OutOfMemory!(q: Int, r: Int) from a
Int.Mod(&self, a: &Arena, m: &Int): OutOfMemory!Int from a
Int.ModPow(&self, a: &Arena, e: &Int, m: &Int): OutOfMemory!Int from a
Int.ModInverse(&self, a: &Arena, m: &Int): OutOfMemory!?Int from a
Int.Gcd(&self, a: &Arena, y: &Int): OutOfMemory!Int from a
Int.Sqrt(&self, a: &Arena): OutOfMemory!Int from a
Int.Shl(&self, a: &Arena, n: int): OutOfMemory!Int from a
Int.Shr(&self, a: &Arena, n: int): OutOfMemory!Int from a
Int.Neg(&self, a: &Arena): OutOfMemory!Int from a
Int.Cmp(&self, y: &Int): int
Int.Sign(&self): int
Int.BitLen(&self): int
Int.Bit(&self, i: int): bool
Int.ToI64(&self): ?i64
Int.ToF64(&self): f64
Int.Bytes(&self, a: &Arena): OutOfMemory![]u8 from a
Int.Text(&self, a: &Arena, base: int): OutOfMemory!str from a
Int.Format(&self, w: mut Writer): WriteError!void

// std.encoding. Encoding, Decoder and Error below are encoding.Encoding, encoding.Decoder and encoding.Error
error BadInput(offset: int)
error UnknownLabel
error Unsupported
error TooLarge
type Error = BadInput | UnknownLabel | Unsupported | TooLarge
encoding.Base64Encode(a: &Arena, data: []u8, alphabet: Alphabet): OutOfMemory!str from a
encoding.Base64Decode(a: &Arena, s: str, alphabet: Alphabet): encoding.Error|OutOfMemory![]u8 from a
encoding.Base64EncodeTo(dst: mut []u8, data: []u8, alphabet: Alphabet): int
encoding.Base64DecodeTo(dst: mut []u8, s: str, alphabet: Alphabet): encoding.Error!int
encoding.Base64Len(n: int, alphabet: Alphabet): int
encoding.HexEncode(a: &Arena, data: []u8): OutOfMemory!str from a
encoding.HexDecode(a: &Arena, s: str): encoding.Error|OutOfMemory![]u8 from a
encoding.PercentEncode(a: &Arena, s: []u8, set: PercentSet): OutOfMemory!str from a
encoding.PercentDecode(a: &Arena, s: str): OutOfMemory![]u8 from a
PercentSet.Has(self, b: u8): bool
encoding.Lookup(label: str): encoding.UnknownLabel|encoding.Unsupported!Encoding
Encoding.Name(self): str
Encoding.Decode(self, a: &Arena, bytes: []u8): OutOfMemory!str from a
Encoding.DecodeStrict(self, a: &Arena, bytes: []u8): encoding.BadInput|OutOfMemory!str from a
Encoding.DecodeKeepBom(self, a: &Arena, bytes: []u8): OutOfMemory!str from a
Encoding.Encode(self, a: &Arena, s: str): encoding.BadInput|encoding.Unsupported|OutOfMemory![]u8 from a
Encoding.Decoder(self): Decoder
Decoder.Push(mut self, a: &Arena, chunk: []u8, last: bool): OutOfMemory!str from a
Encoding.Of(codec: Codec): Encoding
encoding.ParsePem(a: &Arena, text: str into a): encoding.Error|OutOfMemory!List[PemBlock] from a, text
encoding.FormatPem(w: mut Writer, label: str, der: []u8): WriteError!void
encoding.DerRead(data: []u8): encoding.Error!(value: DerValue, rest: []u8) from data
DerValue.Children(&self): DerIter from self.content
DerIter.Next(mut self): encoding.Error!?DerValue from self.data
DerValue.Integer(&self): encoding.Error![]u8 from self.content
DerValue.BitString(&self): encoding.Error![]u8 from self.content
DerValue.Boolean(&self): encoding.Error!bool
DerWriter.New(a: &Arena): DerWriter from a
DerWriter.Begin(mut self, tag: u32): OutOfMemory!void
DerWriter.End(mut self): encoding.Error!void
DerWriter.Primitive(mut self, tag: u32, content: []u8): OutOfMemory!void
DerWriter.Integer(mut self, magnitude: []u8): OutOfMemory!void
DerWriter.Bytes(self): encoding.Error![]u8

enum PercentSet {
    C0Control
    Fragment
    Query
    SpecialQuery
    Path
    Userinfo
    Component
    FormUrlencoded
}

struct PemBlock {
    export label: str
    export der: []u8
}

struct DerValue {
    export tag: u32
    export class: u8
    export constructed: bool
    export content: []u8
}

struct Codec {
    export name: fn(): str
    export decode: fn([]u8, int, bool, u8): (n: int, c: u32, c2: u32, state: u8)
    export encode: fn(u32, u8, mut []u8): (n: int, state: u8)
}

// std.encoding.cjk
cjk.Lookup(label: str): encoding.UnknownLabel!encoding.Encoding

// std.url. Url, Host and Error below are url.Url, url.Host and url.Error
error BadUrl(offset: int)
error BadHost
error IdnaUnsupported
type Error = BadUrl | BadHost | IdnaUnsupported
url.Parse(a: &Arena, input: str into a, base: ?&Url): url.Error|OutOfMemory!Url from a, input
Url.Href(&self): str from self
Url.Scheme(&self): str from self
Url.Username(&self): str from self
Url.Password(&self): str from self
Url.Host(&self): Host from self
Url.Hostname(&self): str from self
Url.Port(&self): ?u16
Url.Path(&self): str from self
Url.Query(&self): str from self
Url.Fragment(&self): str from self
Url.Origin(&self, a: &Arena): OutOfMemory!str from a
Url.Set(&self, a: &Arena, part: Part, value: str): url.Error|OutOfMemory!Url from a
Url.Format(&self, w: mut Writer): WriteError!void
url.ParseQuery(a: &Arena, s: str): OutOfMemory!List[(name: str, value: str)] from a
url.FormatQuery(a: &Arena, pairs: [](name: str, value: str)): OutOfMemory!str from a
url.DomainToAscii(a: &Arena, domain: str, strict: bool): url.Error|OutOfMemory!str from a
url.DomainToUnicode(a: &Arena, domain: str): OutOfMemory!str from a

enum Host {
    Domain(name: str)
    Ipv4(addr: u32)
    Ipv6(addr: [16]u8)
    Opaque(text: str)
    Empty
}

enum Part {
    Href
    Protocol
    Username
    Password
    Host
    Hostname
    Port
    Pathname
    Search
    Hash
}

// std.websocket. Conn, Options and Error below are websocket.Conn, websocket.Options and websocket.Error
error Protocol(code: u16)
error TooLarge
error PeerClosed(code: u16)
error Handshake(status: int)
type Error = Protocol | TooLarge | PeerClosed | Handshake | net.Error | http.NoSecureTransport | OutOfMemory
websocket.Accept(h: &Heap, io: Io, req: &http.Request, t: http.Taken, protocols: []str, const allow: fn(origin: str): bool): websocket.Error!Conn from h, t
websocket.Dial(h: &Heap, a: &Arena, io: Io, u: &url.Url, o: &Options): websocket.Error!Conn from h
Conn.Send(&self, io: Io, kind: Kind, data: []u8): websocket.Error!void
Conn.Recv(&self, a: &Arena, io: Io): websocket.Error!Message from a
Conn.Ping(&self, io: Io, data: []u8): websocket.Error!void
Conn.Protocol(&self): ?str from self
Conn.Close(self, io: Io, code: u16, reason: str): websocket.Error!void
Conn.Abandon(self)

enum Kind {
    Text
    Binary
}

struct Message {
    export kind: Kind
    export data: []u8
}

struct Options {
    export headers: []http.Header
    export protocols: []str
    export secure: ?net.Secure
    export frame_bytes: int
    export message_bytes: int
    export pending_pongs: int
    export close: time.Duration
}

// std.term. Term, Event and Error below are term.Term, term.Event and term.Error
error NotTerminal
error Device(code: i64)
error Unsupported
type Error = NotTerminal | Device | Unsupported | Canceled | OutOfMemory
term.Open(io: Io): term.Error!Term
term.IsTerminal(io: Io, s: Std): bool
term.UseColor(io: Io, s: Std): bool
Term.Raw(mut self, io: Io): term.Error!void
Term.Restore(mut self, io: Io): term.Error!void
Term.Size(&self, io: Io): term.Error!(cols: int, rows: int)
Term.Read(mut self, a: &Arena, io: Io): term.Error!Event from a
Term.ReadSecret(mut self, a: &Arena, io: Io): term.Error!str from a
Term.Writer(mut self, io: Io, buf: mut []u8): Writer from self, buf
Term.Close(self, io: Io)
Style.Format(&self, w: mut Writer): WriteError!void
term.Sanitize(w: mut Writer, s: str): WriteError!void
term.MoveTo(w: mut Writer, row: int, col: int): WriteError!void
term.Clear(w: mut Writer): WriteError!void
term.AltScreen(w: mut Writer, on: bool): WriteError!void
term.ShowCursor(w: mut Writer, on: bool): WriteError!void
term.BracketedPaste(w: mut Writer, on: bool): WriteError!void
term.MouseCapture(w: mut Writer, on: bool): WriteError!void
term.KittyKeys(w: mut Writer, on: bool): WriteError!void

enum Std {
    Stdin
    Stdout
    Stderr
}

enum Event {
    Key(key: Key, ch: char, mods: Mods)
    Paste(text: str)
    Resize(cols: int, rows: int)
    Mouse(button: int, row: int, col: int, mods: Mods)
    Focus(on: bool)
}

// Key may gain members
enum Key {
    Char
    Enter
    Escape
    Backspace
    Tab
    Up
    Down
    Left
    Right
    Home
    End
    PageUp
    PageDown
    Insert
    Delete
    F(n: int)
}

struct Mods {
    export shift: bool
    export alt: bool
    export ctrl: bool
}

struct Style {
    export fg: ?Color
    export bg: ?Color
    export bold: bool
    export underline: bool
    export reverse: bool
}

enum Color {
    Ansi(n: u8)
    Rgb(r: u8, g: u8, b: u8)
}

// std.x509, Experimental. Certificate, Pool and Error below are x509.Certificate, x509.Pool and x509.Error
error Malformed(offset: int)
error UnknownAuthority
error Expired
error NameMismatch
error BadSignature
error BadUsage
error TooComplex
error Unsupported
type Error = Malformed | UnknownAuthority | Expired | NameMismatch | BadSignature | BadUsage | TooComplex | Unsupported | Canceled | OutOfMemory
x509.Parse(a: &Arena, der: []u8): x509.Error!Certificate from a, der
x509.ParsePem(a: &Arena, pem: str): x509.Error!List[Certificate] from a
x509.SystemRoots(a: &Arena, io: Io): x509.Error!Pool from a
x509.SystemPool(h: &Heap, io: Io): x509.Error!&Pool
x509.Verify(a: &Arena, io: Io, chain: []Certificate, roots: &Pool, now: time.Time, host: str): x509.Error!void
Pool.New(certs: []Certificate): Pool from certs
Certificate.Subject(&self, a: &Arena): OutOfMemory!str from a
Certificate.Issuer(&self, a: &Arena): OutOfMemory!str from a
Certificate.Names(&self, a: &Arena): OutOfMemory!List[str] from a, self *self into a
Certificate.NotBefore(&self): time.Time
Certificate.NotAfter(&self): time.Time
Certificate.Der(&self): []u8 from self
Certificate.PublicKey(&self): crypto.Error!crypto.PublicKey from self

struct Certificate {
    export data: []u8
}

struct Pool {
    export certs: []Certificate
}

// std.tls, Experimental. Config, Conn and Error below are tls.Config, tls.Conn and tls.Error
error Alert(code: u8)
error Protocol
error Unsupported
type Error = Alert | Protocol | Unsupported | x509.Error | net.Error
tls.Client(h: &Heap, io: Io, conn: net.Conn, c: &Config, host: str): tls.Error!Conn from h, conn
tls.Connector(c: &Config): net.Secure from c
Conn.Read(&self, io: Io, buf: mut []u8): tls.Error!int
Conn.WriteAll(&self, io: Io, data: []u8): tls.Error!void
Conn.Reader(&self, io: Io, buf: mut []u8): Reader from self, buf
Conn.Writer(&self, io: Io, buf: mut []u8): Writer from self, buf
Conn.Alpn(&self): ?str from self
Conn.Close(self, io: Io): tls.Error!void
Conn.Abandon(self)
tls.Server(h: &Heap, io: Io, conn: net.Conn, c: &Config): tls.Error!Conn from h, conn
tls.Acceptor(c: &Config): net.Secure from c

struct Config {
    export roots: ?&x509.Pool
    export identities: []Identity
    export alpn: []str
    export min_version: Version
    export handshake: time.Duration
    export close: time.Duration
}

struct Identity {
    export chain: []x509.Certificate
    export key: &crypto.PrivateKey
}

enum Version {
    Tls12
    Tls13
}

// std.csv. Records, Record, Options and Error below are csv.Records, csv.Record, csv.Options and csv.Error
error Quote(line: i64, column: int)
error TooLarge(line: i64)
type Error = Quote | TooLarge | ReadError
Records.New(r: mut Reader, buf: mut []u8, o: &Options): Records from r, buf
Records.Next(mut self): csv.Error!?Record from self
Record.Len(&self): int
Record.Field(&self, i: int): ?str from self
Record.Line(&self): i64
csv.WriteRecord(w: mut Writer, values: []str, o: &Options): WriteError!void
csv.Codec(g: mut Gen, t: Type)

struct Options {
    export delimiter: u8
    export quote_byte: u8
    export field_bytes: int
    export record_fields: int
}

// std.dl, Provisional. Library, Search and Error below are dl.Library, dl.Search and dl.Error
error NotFound
error Invalid
error InitFailed
error BadName
error Unsupported
error Os(code: i64)
type Error = NotFound | Invalid | InitFailed | BadName | Unsupported | Os
dl.Open(io: Io, name: str, search: Search): dl.Error!Library
Library.Symbol(&self, name: str): ?*u8
Library.Close(self, io: Io)
Library.Detach(self)

enum Search {
    System
    Application
    Path
}

// std.runtime. FaultContext, FaultAction, FaultKind, FilterId, Waker, PanicInfo, Foreign, Joined, IoTable, Error, Unsupported and OsError below are runtime's; IoTable is std.core's Io table, made public, Provisional
runtime.Processors(): int
runtime.Carriers(): int
runtime.StackBounds(): (lo: uint, hi: uint)
runtime.StackRemaining(): uint
runtime.Blocking[T: Send](io: Io, state: T, f: fn(T): T): Canceled!T
Waker.New(): Waker
Waker.Wait(&self, io: Io): Canceled!void
Waker.Wake(&self)
runtime.AddFaultFilter(f: fn(&FaultContext): FaultAction): FilterId
FaultContext.Kind(&self): FaultKind
FaultContext.Addr(&self): uint
FaultContext.Pc(&self): uint
FaultContext.Sp(&self): uint
FaultContext.Reg(&self, n: int): u64
FaultContext.SetPc(mut self, pc: uint)
FaultContext.SetSp(mut self, sp: uint)
FaultContext.SetReg(mut self, n: int, v: u64)
runtime.RegCount(): int
runtime.ForeignIo(ctx: *u8, table: &IoTable): Foreign
Foreign.Io(&self): Io
Foreign.Release(self)
runtime.ForeignContext(io: Io): *u8
runtime.Dedicated(io: Io): Io
runtime.Thread(io: Io, f: fn(Io)): Joined
Joined.Join(self, io: Io): Canceled!void
runtime.TaskIo(): ?Io
runtime.Cycles(): u64
runtime.Likely(cond: bool): bool
runtime.Unlikely(cond: bool): bool
runtime.Cold()
error Unsupported
error OsError(code: i64)
type Error = Unsupported | OsError
runtime.WriteDump(path: str): runtime.Error!void

enum FaultKind {
    Stack
    Access
    Illegal
}

enum FaultAction {
    Resume(ctx: FaultContext)
    Pass
}

struct PanicInfo {
    export code: u32
    export message: str
    export file: str
    export line: int
    export column: int
}

// std.vm, unsafe only. Region, CodeRegion, CodeWriter, Prot and Error below are vm's
error Misaligned
error Unsupported
error Os(code: i64)
type Error = OutOfMemory | Misaligned | Unsupported | Os
vm.PageSize(): int
vm.Reserve(size: int, align: int): vm.Error!Region
Region.Base(&self): uint
Region.Size(&self): int
Region.Commit(&self, offset: int, len: int): vm.Error!void
Region.Decommit(&self, offset: int, len: int)
Region.Protect(&self, offset: int, len: int, p: Prot): vm.Error!void
Region.Release(self)
CodeRegion.New(size: int): vm.Error!CodeRegion
CodeRegion.Reserve(size: int): vm.Error!CodeRegion
CodeRegion.Size(&self): int
CodeRegion.Entry(&self, offset: int): uint
CodeRegion.Commit(mut self, offset: int, len: int): vm.Error!void
CodeRegion.Edit(mut self): vm.Error!CodeWriter from self
CodeRegion.EditRange(mut self, offset: int, len: int): vm.Error!CodeWriter from self
CodeRegion.Release(self)
CodeWriter.Bytes(mut self): mut []u8 from self
CodeWriter.Flush(&self, offset: int, len: int)
CodeWriter.Done(self)

enum Prot {
    None
    Read
    ReadWrite
}

// std.linux, on x86_64-linux only, unsafe only
linux.Syscall(nr: uint, a0: uint, a1: uint, a2: uint, a3: uint, a4: uint, a5: uint): (r: uint, errno: int)

// std.testing
testing.Fuzz(f: fn([]u8))
testing.Bench(f: fn(n: int))

// std.build. ProfileKind below is build.ProfileKind; build.Profile is its constant, set by the driver
build.File(path: str): []u8

enum ProfileKind {
    Dev
    Release
    Test
    Checked
}

// std.core: the generator API's methods and flows
Type.Methods(&self): []Method
Method.Flows(&self): []Flow
Method.HasFlows(&self): bool
Flow.Kind(&self): FlowKind
Flow.Param(&self): str
Flow.Path(&self): str
Flow.Field(&self): str
Gen.Input(&self, path: str): []u8

enum FlowKind {
    From
    Into
    Ends
    Store
}
```

### 18.4 `std.raw`

`std.raw` has no lines in the listing. Its functions, in full ([std.raw](std/raw.md)):

**`std.raw`** is std-internal, except that a module listed in `unsafe_modules` may import it, on every target: it turns an address into a pointer and back, and reads and writes through one. Its functions are intrinsics, O(1) unless they copy: `Of(r: &T): *T` and `OfMut(r: mut T): *T`, `Null[T]()`, `IsNull`, `Same`, `Offset(p, n)` by `n` elements, `PtrCast[T, U](p): *U`, `Read(p): T`, `ReadUnaligned` (alignment 1, an integer `T`), `Write(p, x)` (the old value is not released), `Copy(dst, src, n)` and `Move` (which may overlap) of `n` elements, `Fill(dst, b, n)` and `Compare(a, b, n)` of bytes, `Slice(p, n): []T` and `SliceMut`, `Data(s): *T`, `DataMut` and `StrData(s)`, `Str(p, n): str` (the caller answers for UTF-8), `Addr(p): uint`, `FromAddr[T](a: uint): *T`, `Forget(x)`, which ends a value without releasing it, `Release(x, a)`, which frees it through `a` with `T`'s `Free`, else forgets it, `IsLinear[T]()` and `HasFree[T]()`, whether `T` is linear and whether it implements `Free`, and `Fail(code, msg): never`, which panics with that code. `Atomic[T: Plain]` is a word, of an integer type of 1, 2, 4 or 8 bytes, or a raw pointer, that tasks on any carrier read and write whole: `New(x)`, then `Load`, `Store`, `Add` (wrapping, an integer `T`), `Swap` and `Cas(old, new): bool`, sequentially consistent, and `LoadOrd`, `StoreOrd`, `SwapOrd`, `AddOrd`, `SubOrd`, `AndOrd`, `OrOrd`, `XorOrd` and `CasOrd(old, new, s, f, weak): (ok, value)`, each under a constant order `o`, `std.atomic`'s `Order` value (0 relaxed, 1 acquire, 2 release, 3 acquire-release, 4 sequentially consistent, 5 unordered, for a load or a store only); `Fence()` and `FenceOrd(o)` are fences. `Ctz64`, `Clz64` and `Popcount64` count bits, `RotL64(x, n)` rotates left, `Mul128(a, b): (hi, lo)` is the full product, `F64Bits`, `F64From`, `F32Bits` and `F32From` reinterpret a float's encoding, `SqrtF64` is the correctly rounded square root, and `Select[T: Plain](c, a, b): T` is `a` when `c` holds, else `b`: the compiler is told `c` is unpredictable, so for an integer, `bool`, pointer or record `T` the choice is a conditional move, not a branch (a float `T` may still branch on x86_64). Of these, `Of`, `OfMut`, `Null`, `IsNull`, `Same`, `Data`, `DataMut`, `StrData`, `Addr`, `IsLinear`, `Fail`, `Fence`, `FenceOrd`, `Ctz64`, `Clz64`, `Popcount64`, `RotL64`, `Mul128`, `F64Bits`, `F64From`, `F32Bits`, `F32From`, `SqrtF64`, `Select` and every method of `Atomic` are safe, and the rest are called inside `unsafe` (E1101).

## 19. Test prelude

The examples in this reference use these names without declaring them. They are declared for the examples only, and are not library names.

```mk
// verdict: parse
struct Point {
    x: i32
    y: i32
}

linear struct Res {
    id: i32
}

Res.Close(self)
Acquire(name: str): Res
MakePoint(): Point
Cond(): bool
Use[T](x: &T) {}
UseMut[T](x: mut T) {}
Take[T](x: T)
```
