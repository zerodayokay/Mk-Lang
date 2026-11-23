# A tour of Mk

This page is for programmers who know C, Go or Rust. Read it top to bottom: it walks through the whole language, one feature at a time, each with a short example, and the comments show what the programs print. For every rule, follow the links to [the language reference](reference.md) and [the memory model](memory.md).

## Hello world

Save [the README's hello world](../README.md#hello-world) as `hello.mk` and run it with `mk run hello.mk`. `mk run` compiles the file to a native executable and runs it; `mk build hello.mk` builds the executable. The program already shows a few things:

- A program starts at `export Main`. It receives `sys: &System`, which holds the program's arguments, its I/O handle, a heap and an arena. There are no globals for I/O or memory: a function can do only what its parameters let it.
- Printing can fail, so `Print` returns `WriteError!void`: an error, or nothing. `try` passes an error up to the caller.
- An error that leaves `Main` is printed, and the process exits with status 1. `Main` may return `void`, `int` (the exit status), `E!void` or `E!int`.

For anything larger than one file, make a package with `mk new`, as [the README](../README.md#hello-world) shows.

## Comments

```mk
// verdict: ok
// A line comment runs to the end of the line.

/// A doc comment documents the item or field below it.
/// `mk doc` and the language server show it.
struct Celsius {
    /// Degrees, in hundredths.
    centi: i64
}
```

There are no block comments.

See also: [comments](reference.md#comments).

## Variables and constants

A binding made with `:=` is fixed. `var` makes one that you can assign to, or borrow for writing.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    x := 10          // the type comes from the value: int
    var y := 20      // var: may be reassigned
    y += x
    z: i32 = 7       // with a type
    w: int           // declared now, assigned on every path before its first use
    if y > 25 {
        w = 1
    } else {
        w = 2
    }
    try sys.Print("{x} {y} {z} {w}\n", (x, y, z, w)) // prints 10 30 7 1
}
```

Assigning to a binding without `var` is an error (E0404). There is no shadowing: a local cannot reuse the name of another local in scope (E0206). Names follow one case rule:

- `PascalCase` for types, functions, methods, constants and enum variants.
- `snake_case` for locals, parameters and fields.

A `const` is a value known at compile time. Its initializer may call ordinary functions, which run while the program compiles (see [compile time](#compile-time-code)).

```mk
// verdict: ok
const MaxUsers: int = 1024
const Greeting = "hello"
const Table = Squares()

Squares(): [4]int {
    var t: [4]int = [0; 4]
    for i in 0..4 {
        t[i] = i * i
    }
    return t
}
```

See also: [bindings](reference.md#bindings), [constants](reference.md#constants), [global variables](reference.md#global-variables).

## Basic types

| Type | What it is |
|---|---|
| `i8 i16 i32 i64`, `u8 u16 u32 u64` | Fixed-width integers |
| `int`, `uint` | Pointer-sized integers. Lengths and indexes are `int` |
| `f32 f64` | IEEE floats |
| `bool` | `true` or `false` |
| `char` | A Unicode scalar value |
| `str` | A read-only view of valid UTF-8 |
| `void`, `never` | No value; never returns |

Integer literals take `_` separators and the prefixes `0x`, `0o` and `0b`: `1_000`, `0xFF`, `0b1010`. A literal takes the type that is expected, and is `int` (or `f64` for `1.5` and `1e9`) when nothing is expected.

There are no implicit conversions. Three forms convert explicitly:

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    a: i32 = 300
    b := i64(a)          // T(x) converts only when no value can change
    c := Cast[u8](a / 2) // Cast checks: it panics if the value does not fit
    d := Trunc[u8](a)    // Trunc keeps the low bits: 300 is 0x12C, so 0x2C
    e := Cast[f64](a) / 8.0
    try sys.Print("{b} {c} {d} {e}\n", (b, c, d, e)) // prints 300 150 44 37.5
}
```

`T(x)` on a conversion that could lose a value, such as `i32(a)` for an `i64`, does not compile (E0303).

Arithmetic is checked in every build, release builds included:

- `+`, `-` and `*` panic on overflow.
- `/` and `%` panic on a zero divisor.
- A shift panics when its amount is negative or not below the bit width.

When you want wrapping arithmetic, say so: `+%`, `-%` and `*%` wrap.

```mk
// verdict: ok
Hash(data: []u8): u32 {
    var h: u32 = 2166136261
    for b in data {
        h = (h ^ u32(b)) *% 16777619 // FNV-1a: the multiply wraps on purpose
    }
    return h
}
```

See also: [integers](reference.md#integers), [floats](reference.md#floats), [conversions](reference.md#conversions), [integer literals](reference.md#integer-literals).

## Strings and characters

A `str` is a view of UTF-8 bytes it does not own: a literal, a slice of a file you read, or text built in an arena. Its length and its slice offsets are in bytes.

| Literal | Type | Notes |
|---|---|---|
| `'a'`, `'\u{1F600}'` | `char` | One scalar |
| `b'a'` | `u8` | One byte |
| `"a\tb\n"` | `str` | With escapes ([string literals](reference.md#string-literals)) |
| `r"C:\temp"` | `str` | Raw: no escapes |
| `b"GET /"` | `[]u8` | Bytes |
| `"""` ... `"""` | `str` | Multi-line |

```mk
// verdict: ok
const Usage = """
    usage: tool [options] file
      -v    verbose
    """

export Main(sys: &System): OutOfMemory|WriteError!void {
    s := "naïve café"
    try sys.Print("{n} bytes\n", (n: s.Len())) // prints 12 bytes: ï and é take two each
    var scalars := 0
    for c in s.Scalars() {
        if c != ' ' {
            scalars += 1
        }
    }
    try sys.Print("{scalars} letters\n", (scalars)) // prints 9 letters
    first := s[0..6]                                // byte offsets: "naïve"
    if at := s.Find(" ") {
        try sys.Print("[{first}] space at {at}\n", (first, at)) // prints [naïve] space at 6
    }
    loud := try s.ToUpper(sys.arena)
    try sys.Print("{loud}\n", (loud)) // prints NAÏVE CAFÉ
    try sys.Print(Usage)
}
```

- A multi-line string drops the newline after the opening quotes, and removes the closing line's indentation from every line.
- A slice of a `str` must fall on a scalar boundary, or it panics.
- Strings compare with `==` and `<`, byte by byte.

`str` has no `+`. To build text, format it into an arena with `Fmt`, or append to a `Text`:

```mk
// verdict: ok
Label(a: &Arena, name: str, n: int): OutOfMemory!str from a {
    return try a.Fmt("{name} #{n}", (name, n))
}

Csv(a: &Arena, cells: []str): OutOfMemory!str from a {
    var t := Text.New(a)
    for i, f in cells {
        if i > 0 {
            try t.PushChar(',')
        }
        try t.Push(f)
    }
    return t.Done()
}
```

(`from a` says the result comes from the arena; [memory](#memory) explains it.)

See also: [string literals](reference.md#string-literals), [`str`](reference.md#strings-str), [strings and text in the prelude](std/core.md#strings-and-text).

### Formatting

`Print`, `PrintErr` and `Fmt` take a format string and a record whose field names match its `{holes}`. The compiler checks each hole against the record. `(x)` is short for `(x: x)`, and a format with no holes needs no record. After a colon, a hole may carry a spec: fill and alignment, width, sign, zero padding, precision and radix.

```mk
// verdict: ok
struct Item {
    export name: str
    export qty: int
    export price: f64
}

export Main(sys: &System): WriteError!void {
    items := [Item(name: "bolt", qty: 12, price: 0.25), Item(name: "washer", qty: 300, price: 0.05)]
    try sys.Print("{a:<8}{b:>5}{c:>8}\n", (a: "name", b: "qty", c: "price"))
    for it in items[..] {
        try sys.Print("{it.name:<8}{it.qty:>5}{it.price:>8.2}\n", (it)) // a hole may name a field path
    }
    flags := 42
    try sys.Print("{flags:08b} {flags:x} {flags:#>6}\n", (flags)) // prints 00101010 2a ####42
}
```

A value prints through the `Format` interface. Numbers, `bool`, `char`, `str`, errors and the library's types implement it, and you can implement it for your own types ([interfaces](#methods-and-interfaces)).

See also: [printing](reference.md#printing), [formatting and printing](std/core.md#formatting-and-printing).

## Arrays, slices and lists

`[N]T` is an array: `N` values stored inline, copied as a whole. `[]T` is a slice: a view of consecutive values that live elsewhere. `mut []T` is a slice you may write through. Indexing and slicing are bounds-checked in every build.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    primes := [2, 3, 5, 7, 11] // an array literal: [5]int
    var zeros: [4]int = [0; 4] // [value; N] repeats a value
    zeros[1] = 9
    head := primes[..2]        // a slice of the first two: []int
    tail := primes[2..]        // from index 2 to the end
    mid := primes[1..4]
    try sys.Print("{a} {b} {c} {d}\n", (a: head.Len(), b: tail[0], c: mid.Len(), d: zeros[1])) // prints 2 5 3 9
}
```

An array literal is an array, so pass `xs[..]` where a slice is expected. (In a `const` initializer, an array literal may also be a `[]T` over static data.)

`List[T]` is a growable list. Like most of the library's containers, it lives in an arena and is freed with it. `Push` can run out of memory, so it takes a `try`.

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    var xs := List[int].New(sys.arena)
    for i in 1..6 {
        try xs.Push(i * i)
    }
    xs.Reverse()
    xs.Sort(fn(a: &int, b: &int): bool { return *a < *b })
    last := xs.Pop().Must()
    try sys.Print("{n} items, last was {last}, first is {first}\n", (n: xs.Len(), last, first: xs[0]))
    // prints 4 items, last was 25, first is 1
}
```

Slices, arrays and lists share one set of operations, among them `Len`, `Get`, `Sort`, `SortStable`, `SortStableInPlace`, `Search`, `Reverse`, `Swap`, `Has`, `Index`, `Chunks` and `SplitAt`. `mk query members List` prints the methods of any type.

See also: [arrays](reference.md#arrays-nt), [slices](reference.md#slices-t-and-mut-t), [slices and lists](std/core.md#slices-and-lists).

## Maps

`Map[K, V]` is a hash map in an arena. Keys need `Hash` and `Eq`. `Get` returns an optional reference, and `Ensure` returns the value for a key, inserting a default first when the key is missing.

```mk
// verdict: ok
export Main(sys: &System): OutOfMemory|WriteError!void {
    var counts := Map[str, int].New(sys.arena)
    for word in "the cat and the hat and the bat".Words() {
        n := try counts.Ensure(word, 0)
        *n += 1
    }
    try counts.Set("dog", 0)
    key := "the"
    if n := counts.Get(&key) {
        try sys.Print("{key}: {n}\n", (key, n: *n)) // prints the: 3
    }
    cat := "cat"
    try sys.Print("{k} keys, has cat: {c}\n", (k: counts.Len(), c: counts.Has(&cat))) // prints 6 keys, has cat: true
}
```

`for (key, value) in counts.Iter() { ... }` visits every entry, in no fixed order. Other collections are `Set` (a hash set), `Deque`, `BTree` and `PriorityQueue` from `std.coll`, and the heap containers in [memory](#the-heap).

See also: [maps and sets](std/core.md#maps-and-sets).

## Control flow

### if

```mk
// verdict: ok
Sign(n: int): int {
    if n < 0 {
        return -1
    } else if n == 0 {
        return 0
    }
    return 1
}

Abs(n: int): int {
    return if n < 0 { -n } else { n } // an if with an else is a value
}
```

There are no parentheses around the condition, and the braces are required. The condition must be a `bool`.

### for

`for` is the only loop.

```mk
// verdict: ok
export Main(sys: &System): WriteError!void {
    xs := [3, 1, 4, 1, 5]
    var sum := 0
    for i in 0..xs.Len() {  // a half-open range: 0, 1, 2, 3, 4
        sum += xs[i]
    }
    for x in xs {           // each element, copied
        sum += x
    }
    for i, x in xs {        // with its index
        if i == 0 {
            sum += x
        }
    }
    var n := 27
    var steps := 0
    for n != 1 {            // a while loop
        n = if n % 2 == 0 { n / 2 } else { 3 * n + 1 }
        steps += 1
    }
    var found := 0
    outer: for a in 1..10 { // a label for break and continue
        for b in 1..10 {
            if a * b == 42 {
                found = a
                break outer
            }
        }
    }
    try sys.Print("{sum} {steps} {found}\n", (sum, steps, found)) // prints 31 111 6
}
```

`for x in &xs` gives a reference to each element, `for x in mut xs` lets you change them in place, `for { }` loops until `break` or `return`, and `for x := e { }` loops while `e` gives `some(x)`. [`for` loops](reference.md#for-loops) lists every form.

`for x in xs` copies, so it needs elements that can be copied (E0409); iterate over `&xs` otherwise. An iterator is an ordinary struct with a `Next` method:

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

Doubled(xs: mut []int) {
    for x in mut xs {
        *x *= 2
    }
}

Total(): int {
    var sum := 0
    for n in Countdown(left: 4) { // 3, 2, 1, 0
        sum += n
    }
    return sum
}
```

There are no `.map`, `.filter` or `.collect` chains: write the loop.

### match

`match` runs the first arm whose pattern fits, and it must cover every case (E0306). Each arm is a pattern and a block. Used as a value, a `match` gives the last line of the arm that ran.

```mk
// verdict: ok
Classify(n: int): str {
    return match n {
        0 { "zero" }
        1..10 { "small" }    // a half-open range
        10.. { "large" }     // an open end runs to the type's limit
        _ { "negative" }
    }
}

Method(name: str): int {
    match name {
        "GET" | "HEAD" {
            return 1
        }
        "POST" {
            return 2
        }
        other {              // a name binds the whole value
            return other.Len()
        }
    }
}
```

Patterns include literals, integer and `char` ranges, `A | B`, `_`, a binding name, enum variants (`.Circle(r)`, in [structs and enums](#structs-enums-and-records)) and the optional and result heads `some(x)`, `none`, `ok(v)` and `err(e)`.

### defer

`defer` runs a statement when its block exits, by any path. Deferred statements run in reverse order. A `defer` inside a loop body runs at the end of each iteration.

```mk
// verdict: ok
export Main(sys: &System) {
    sys.Print("one\n") else {}
    defer sys.Print("three\n") else {}
    defer sys.Print("two\n") else {}
    // prints one, two, three
}
```

A deferred statement cannot `try`, `fail` or `return`, so `else {}` here ignores a print error. `onerr` is the same as `defer`, but runs only when the block exits with an error; [errors](#optionals-and-errors) shows it.

See also: [`if`](reference.md#if), [`for` loops](reference.md#for-loops), [`match`](reference.md#match), [`defer`](reference.md#defer), [patterns](reference.md#6-patterns).

## Functions

A function is a name, parameters with their types, a result type after a colon, and a body. Every function returns with `return`; a body's last expression is not its value.

```mk
// verdict: ok
Add(a: int, b: int): int {
    return a + b
}

DivMod(a: int, b: int): (q: int, r: int) { // several results are one record
    return (q: a / b, r: a % b)
}

Steps(var n: int): int { // a by-value parameter is fixed unless marked var
    var steps := 0
    for n > 1 {
        n /= 2
        steps += 1
    }
    return steps
}

Apply(f: fn(int, int): int, x: int, y: int): int { // a function value
    return f(x, y)
}

Combined(): int {
    (q, r) := DivMod(17, 5) // destructure a record by its field names
    return Apply(Add, q, r) + Steps(8)
}
```

There is no overloading, no default argument, no variadic parameter and no named argument. By convention, parameters come in one order: the receiver, then an arena or allocator, then `io`, then the rest.

A `fn` literal is a function written inline. It is not a closure: a literal used as a value cannot capture the caller's locals (E0609). The exception is a literal passed directly to a `const` parameter of type `fn`, such as `Sort`'s `less`. That literal is compiled into the call, so it may read the enclosing locals and mutate `var` locals (or reborrow `mut` references and slices) for the duration of that call:

```mk
// verdict: ok
ByDistance(xs: mut []int, origin: int): int {
    var compares := 0
    xs.Sort(fn(a: &int, b: &int): bool {
        compares += 1
        return (*a - origin) * (*a - origin) < (*b - origin) * (*b - origin)
    })
    return compares
}
```

```mk
// verdict: reject E0609
Over(limit: int): fn(&int): bool {
    return fn(x: &int): bool { return *x > limit } // <- E0609
}
```

To keep a callback, store a `fn` value beside the state it needs, and pass that state in as a parameter. [The cookbook](cookbook.md) shows this under "Keep a callback for later".

See also: [functions and methods](reference.md#functions-and-methods), [function values](reference.md#function-values).

## Structs, enums and records

A `struct` groups named fields. You build one by calling its name with every field named; no field has a default.

```mk
// verdict: ok
struct Vec2 {
    x: f64
    y: f64
}

struct Particle {
    pos: Vec2
    vel: Vec2
    mass: f64
}

Step(p: Particle, dt: f64): Particle {
    next := Vec2(x: p.pos.x + p.vel.x * dt, y: p.pos.y + p.vel.y * dt)
    return Particle(pos: next, vel: p.vel, mass: p.mass)
}

Parts(v: Vec2): f64 {
    Vec2(x, y) := v // destructure a struct into locals named after its fields
    return x + y
}
```

Leaving out a field is an error (E0305). A struct is laid out as in C: its fields in order, each at its natural alignment. `packed struct` removes the padding, and `align(64) struct` raises the alignment.

An `enum` is a choice between variants, each with its own fields or none. A value names the type, as in `Shape.Empty`; a pattern may leave it out, as in `.Empty`.

```mk
// verdict: ok
enum Shape {
    Circle(r: f64)
    Rect(w: f64, h: f64)
    Empty
}

enum Level: u8 { // an explicit tag type and values, for C and file formats
    Debug = 1
    Info = 2
    Error = 3
}

Area(s: Shape): f64 {
    return match s {
        .Circle(r) { 3.14159 * r * r }
        .Rect(w, h) { w * h }
        .Empty { 0.0 }
    }
}

Unit(): f64 {
    return Area(Shape.Rect(w: 2.0, h: 3.0))
}
```

A match on an enum must name every variant or end with `_`. A variant's fields bind by name: `.Rect(w: width, h: height)` renames them, and `.Rect` alone ignores them.

A record is a struct with no name: `(name: str, age: int)`. Two records with the same fields are the same type. Records carry several results, and hold the arguments of `Print`. `type` gives any type a second name:

```mk
// verdict: ok
type Span = (lo: int, hi: int)

Widen(s: Span, by: int): Span {
    return (lo: s.lo - by, hi: s.hi + by)
}

Width(s: Span): int {
    (lo, hi) := s
    return hi - lo
}
```

See also: [structs](reference.md#structs), [enums](reference.md#enums), [record types](reference.md#record-types).

## Methods and interfaces

A function named `Type.Name` lives in that type's namespace. When its first parameter is `self`, you call it with a dot. The form of `self` says how the method takes its receiver:

| Receiver | Meaning |
|---|---|
| `self` | By value: a copy, or the value moved in |
| `&self` | A shared reference: the method reads |
| `mut self` | An exclusive reference: the method may write |

```mk
// verdict: ok
struct Counter {
    n: int
}

Counter.New(): Counter { // no self: called as Counter.New()
    return Counter(n: 0)
}

Counter.Add(mut self, k: int) {
    self.n += k
}

Counter.Get(&self): int {
    return self.n
}

export Main(sys: &System): WriteError!void {
    var c := Counter.New()
    c.Add(2) // borrows c for writing, so c must be var
    c.Add(3)
    try sys.Print("{n}\n", (n: c.Get())) // prints 5
}
```

There is no `impl` block for methods and no inheritance. You write methods next to the type, in its module.

An `interface` lists methods. A type implements it with an `impl`:

```mk
// verdict: ok
interface Shape {
    Area(&self): f64
}

struct Square {
    side: f64
}

impl Shape for Square {
    Area(&self): f64 {
        return self.side * self.side
    }
}

impl Format for Square { // how a Square prints in a {hole}
    Format(&self, w: mut Writer): WriteError!void {
        try Print(w, "square({s})", (s: self.side))
    }
}

Larger[T: Shape](a: &T, b: &T): bool {
    return a.Area() > b.Area()
}

export Main(sys: &System): WriteError!void {
    a := Square(side: 2.0)
    b := Square(side: 3.0)
    try sys.Print("{a} larger than {b}: {x}\n", (a, b, x: Larger(&a, &b))) // prints square(2) larger than square(3): false
}
```

An interface is a bound on a generic parameter, as in `Larger[T: Shape]`; it is not the type of a value. A type has at most one `impl` of each interface, written in the module of the type or of the interface. The library's interfaces include `Eq`, `Ord`, `Hash`, `Format` and `Free`.

To keep values of different types behind one interface, generate a boxed type with `gen dyn.Of(Shape)`. It writes a `ShapeDyn` struct that holds the object and a table of its methods:

```mk
// verdict: ok
import std.dyn

interface Shape {
    Area(&self): f64
}

gen dyn.Of(Shape)

struct Square {
    side: f64
}

struct Circle {
    r: f64
}

impl Shape for Square {
    Area(&self): f64 {
        return self.side * self.side
    }
}

impl Shape for Circle {
    Area(&self): f64 {
        return 3.0 * self.r * self.r
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.arena
    var shapes := List[ShapeDyn].New(a)
    try shapes.Push(ShapeDyn.Of(try a.Put(Square(side: 2.0))))
    try shapes.Push(ShapeDyn.Of(try a.Put(Circle(r: 1.0))))
    var total := 0.0
    for s in &shapes {
        total += s.Area()
    }
    try sys.Print("{total}\n", (total)) // prints 7
}
```

See also: [interfaces](reference.md#interfaces).

## Generics

Functions and types take type parameters in brackets. A parameter's bounds say what the body may do with it. The body is checked once, against those bounds, not again at each use.

```mk
// verdict: ok
Max[T: Ord + Plain](xs: []T): ?T {
    if xs.Len() == 0 {
        return none
    }
    var best := xs[0]
    for x in xs {
        if best.Less(&x) { // Ord's one method
            best = x
        }
    }
    return some(best)
}

struct Stack[T] {
    items: List[T]
}

Stack[T].New(a: &Arena): Stack[T] {
    return Stack[T](items: List[T].New(a))
}

Stack[T].Push(mut self, x: T into self): OutOfMemory!void {
    try self.items.Push(x)
}

Stack[T].Pop(mut self): ?T {
    return self.items.Pop()
}

Top(a: &Arena): OutOfMemory!int {
    var s := Stack[int].New(a)
    try s.Push(1)
    try s.Push(2)
    nums := [4, 9, 2]
    return s.Pop().Must() + Max(nums[..]).Must()
}
```

Type arguments are inferred from the arguments, then from the expected type. Otherwise you write them, as in `List[int].New(a)`. A body that uses an operation its bounds do not give is an error at the definition:

```mk
// verdict: reject E0703
Biggest[T](a: &T, b: &T): bool {
    return *a > *b // <- E0703
}
```

A bound is an interface, such as `T: Ord`, several joined with `+`, or one of the built-in bounds `Plain`, `Send`, `Share` and `Fields[B]`. Without `Plain`, `T` may be a type that must be released, so the body must consume each `T` it is given. [Bounds](reference.md#bounds) and [built-in bounds](reference.md#built-in-bounds) have the rules; [const parameters](reference.md#const-parameters) and [specialization](reference.md#specialization-if-t-is) cover `struct Matrix[const R: int, const C: int]` and `if T is u8 { }`.

See also: [generics and interfaces](reference.md#10-generics-and-interfaces).

## Optionals and errors

`?T` holds `some(x)` or `none`. There is no null: a reference `&T` always points at something, and `?&T` is the nullable one, still the size of a pointer.

```mk
// verdict: ok
Find(xs: []int, x: int): ?int {
    for i, y in xs {
        if y == x {
            return some(i)
        }
    }
    return none
}

Lookups(xs: []int): int {
    if i := Find(xs, 7) { // runs when the value is some, with i bound to its payload
        return i
    }
    j := Find(xs, 3) else { // runs the block on none; its last line is the value
        -1
    }
    return j + Find(xs, 0).Must() // Must panics on none
}
```

A function that can fail says so in its result type. `E!T` holds an error from the set `E`, or a `T`. You declare errors with `error` and combine them into sets with `|`. An error may carry plain data.

```mk
// verdict: ok
error NotFound(id: i64)
error Denied

type LoadError = NotFound | Denied

Load(id: i64): LoadError!str {
    if id < 0 {
        fail Denied            // return an error
    }
    if id == 0 {
        fail NotFound(id: id)
    }
    return "data"
}

Length(id: i64): LoadError!int {
    s := try Load(id)          // on error, return it; Load's set must fit in ours (E0801)
    return s.Len()
}

Quiet(id: i64): int {
    s := Load(id) else e {     // handle the error here; e is bound to it
        _ = e
        ""
    }
    return s.Len()
}

Describe(id: i64): str {
    return match Load(id) {    // or handle both cases with match
        ok(s) { s }
        err(e) {
            match e {
                NotFound { "missing" }
                Denied { "denied" }
            }
        }
    }
}

export Main(sys: &System): LoadError|WriteError!void {
    try sys.Print("{a} {b} {c}\n", (a: Quiet(-1), b: Describe(0), c: try Length(5)))
    // prints 0 missing 4
    _ = try Length(0) // leaves Main with the error: prints error: main.NotFound(id: 0), exits 1
}
```

A signature always names its error set; Mk never infers one. A result must be used (E0209): `_ = F()` discards a value, but never an error. `e.Must()` gives the value or panics, for errors as for optionals. Development builds record each `try` an error passes through, and print that trace with the error.

`onerr` runs a statement only when the block exits with an error. Use it to release something on the failure paths and hand it over on success: the recipe [write a file and report a close error](cookbook.md#write-a-file-and-report-a-close-error) shows it, and [`onerr`](reference.md#cleanup-on-the-error-path-onerr) has the rules.

### Panics

A panic is not an error. It prints a report with a backtrace and ends the process; nothing unwinds. Overflow, an index out of bounds, division by zero, a failed `Assert` or `Must`, and stack exhaustion all panic. `Panic("message")` panics on purpose.

See also: [errors](reference.md#9-errors), [`match` on a result](reference.md#match-on-a-result), [panics](reference.md#panics).

## Memory

Mk has no garbage collector and no hidden allocation. Memory comes from an allocator you pass in. The compiler checks that no reference outlives what it points at, and that nothing is released twice or forgotten. Most code needs no annotations for this. [The memory model](memory.md) has every rule.

### Arenas

Most allocation goes into an arena. An arena hands out memory quickly and frees all of it at once. `sys.arena` lives for the whole program. For work with a shorter life, such as one request, make your own arena and free it when the work ends.

```mk
// verdict: ok
Shout(a: &Arena, words: []str): OutOfMemory!List[str] from a {
    var out := List[str].New(a)
    for w in words {
        try out.Push(try w.ToUpper(a))
    }
    return out
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    var arena := try Arena.New(sys.heap)
    defer arena.Free() // every List, Map, Text and string made in it goes too
    words := ["arena", "memory"]
    loud := try Shout(&arena, words[..])
    for w in loud {
        try sys.Print("{w}\n", (w)) // prints ARENA, then MEMORY
    }
}
```

`List`, `Map`, `Set` and `Text` live in the arena they were made with, and need no release of their own. `arena.Reset()` empties an arena for reuse, and `a.Sub()` makes a child arena for scratch work. Nothing made in an arena can be used after the arena is freed or reset: the compiler rejects it.

### The heap

Data that must outlive any one arena lives on the heap. `sys.heap.Alloc()` gives an allocator. The heap containers `HeapList`, `HeapMap`, `TextMap`, `HeapText` and `Box` take it at each call that allocates. These containers are linear (below): the compiler makes you `Free` each one exactly once. The recipe [return heap-owned data that outlives an arena](cookbook.md#return-heap-owned-data-that-outlives-an-arena) builds a `HeapList` and hands it to the caller.

### Plain, unique and linear values

Every type has a kind, which says what happens when a value is used: a plain value is copied, a unique value is moved, and a linear value is moved and must be consumed on every path. [R1](memory.md#r1-value-kinds) gives which types are which.

A struct takes the strictest kind among its fields, so a struct holding a file is linear without saying so ([value kinds](memory.md#r1-value-kinds)). Declare a type `linear` when it holds no linear field but must still be released, such as a handle:

```mk
// verdict: ok
linear struct Ticket {
    id: int
}

Ticket.Redeem(self): int { // takes the ticket by value, which consumes it
    Ticket(id) := self
    return id
}

Redeemed(): int {
    t := Ticket(id: 7)
    return t.Redeem()
}
```

Using a value after it moved is an error, and so is a path where a linear value is never consumed:

```mk
// verdict: reject E0401 E0402
linear struct Ticket {
    id: int
}

Ticket.Redeem(self): int {
    Ticket(id) := self
    return id
}

Twice(): int {
    t := Ticket(id: 7)
    n := t.Redeem()
    return n + t.Redeem() // <- E0401
}

Maybe(c: bool): int {
    t := Ticket(id: 7) // <- E0402
    if c {
        return t.Redeem()
    }
    return 0
}
```

`defer t.Free(a)` and `onerr t.Free(a)` are the usual ways to consume a linear value on every path.

### Borrowing

`&x` borrows `x` to read it, and `mut x` borrows it to write. A borrow is called a loan.

- `mut x` needs `x` to be `var`.
- A loan lasts until the last use of anything that holds it, not until the end of a block.
- While a shared loan (`&x`) is live, `x` cannot be written or moved.
- While an exclusive loan (`mut x`) is live, `x` cannot be used at all.

```mk
// verdict: ok
Bump(n: mut int) {
    *n += 1
}

Sum(xs: &[3]int): int {
    return xs[0] + xs[1] + xs[2]
}

Borrows(): int {
    var xs: [3]int = [1, 2, 3]
    total := Sum(&xs)  // a shared loan, ended when Sum returns
    Bump(mut xs[0])    // an exclusive loan of one element
    first := &xs[0]
    n := *first        // first's last use: its loan ends here
    xs[1] = 10         // so this write is allowed
    return total + n + xs[1]
}
```

```mk
// verdict: reject E0502
Conflict(): int {
    var xs: [3]int = [1, 2, 3]
    first := &xs[0]
    xs[1] = 10 // <- E0502
    return *first
}
```

All the elements of one array or slice count as one place, so writing `xs[1]` conflicts with a loan on `xs[0]`. Fields are separate places. To change elements one by one, iterate with `for x in mut xs`, or split the slice with `SplitAt`. [The conflict table](memory.md#r5-the-conflict-table) and [disjoint elements](memory.md#r20-disjoint-elements) have the rules.

### Where results come from

When a function returns something that holds a loan, the caller must know what the loan is on. Usually the types tell: when only one parameter could have supplied the result, that is the answer. When several could, the signature names them with `from`. When a function stores a parameter inside another, it says so with `into`.

```mk
// verdict: ok
Longest(a: str, b: str): str from a, b {
    if a.Len() > b.Len() {
        return a
    }
    return b
}

struct Names {
    all: List[str]
}

Names.Add(mut self, name: str into self): OutOfMemory!void { // name is stored in self
    try self.all.Push(name)
}
```

Without `from a, b`, `Longest` is E0601: the compiler cannot tell which argument the result borrows. [Result sources](memory.md#r9-result-sources) has the rules.

The compiler checks each body against its signature, and checks each caller against the signature alone. So a change inside a function body never breaks its callers. (A body that runs at compile time to compute a constant is the one exception.)

### Views

A struct whose fields borrow, such as a `str` or a slice, holds those loans. A window that keeps `xs: []int` can hand out slices that point into the numbers: they need the numbers to stay alive, not the window.

```mk
// verdict: ok
struct Window {
    xs: []int
    pos: int
}

Window.Next(mut self): ?[]int {
    if self.pos + 2 > self.xs.Len() {
        return none
    }
    start := self.pos
    self.pos += 1
    return some(self.xs[start..start + 2]) // a view of xs, not of the window
}

Rises(xs: []int): int { // Rises([1, 3, 2, 5, 8][..]) is 3
    var n := 0
    for w in Window(xs: xs, pos: 0) {
        if w[1] > w[0] {
            n += 1
        }
    }
    return n
}
```

A struct that keeps its memory behind a private pointer, as `List` and `Map` do, is declared `view struct`. A view struct may borrow, and it is unique. An exported struct whose private fields borrow must also say `view` (E0410), so that its users can see that it borrows.

[The memory model](memory.md) covers the rest, each rule with accepted and rejected examples: per-field loans, reborrowing, temporaries, `Cell` for shared mutation, and `ends` for arenas holding cycles.

## Concurrency

Mk has no `async` and no `await`, and any function can call any other. A function can block or suspend only through an `Io` value it was given, so a function without one cannot do I/O. `Main` gets one in `sys.io`.

Tasks are structured: they start inside a `scope` block, and the block does not exit until every task in it has finished. `s.go F(args)` starts a task that runs `F`. Its arguments are evaluated at once, in the parent. They may borrow the parent's locals, because the scope outlives the task.

```mk
// verdict: ok
Sum(xs: []int, out: mut int) {
    for x in xs {
        *out += x
    }
}

export Main(sys: &System): WriteError!void {
    data := [1, 2, 3, 4, 5, 6, 7, 8]
    var left := 0
    var right := 0
    scope s := sys.io.Scope() {
        s.go Sum(data[..4], mut left) // each task gets its own exclusive loan
        s.go Sum(data[4..], mut right)
    }                                 // both tasks have finished here
    try sys.Print("{left} + {right} = {t}\n", (left, right, t: left + right)) // prints 10 + 26 = 36
}
```

A task returns `void`. Results come back through `mut` arguments, as here, or through a channel. Arguments must be `Send`. There are no data races: two tasks cannot hold exclusive loans on one place, and the compiler rejects a program that tries.

A `Chan[T]` passes values between tasks. It is shared, so it comes from a heap allocator, and you free it after the tasks that use it are done.

```mk
// verdict: ok
Produce(io: Io, ch: &Chan[int], n: int) {
    for i in 0..n {
        ch.Send(io, i * i) else {
            return
        }
    }
    ch.Close()
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    a := sys.heap.Alloc()
    ch := try Chan[int].New(a, 4) // room for four values
    defer ch.Free(a)
    var total := 0
    scope s := sys.io.Scope() {
        s.go Produce(sys.io, &ch, 10)
        for {
            x := ch.Recv(sys.io) else { // fails with Closed once the producer is done
                break
            }
            total += x
        }
    }
    try sys.Print("{total}\n", (total)) // prints 285
}
```

Shared state goes behind a `Mutex[T]`. `Lock` returns a guard, which is linear, so you cannot forget to unlock it:

```mk
// verdict: ok
Add(io: Io, total: &Mutex[int], k: int) {
    var g := total.Lock(io) else {
        return
    }
    *g.Mut() += k
    g.Unlock()
}

export Main(sys: &System): WriteError!void {
    total := Mutex.New(0)
    scope s := sys.io.Scope() {
        for k in 1..101 {
            s.go Add(sys.io, &total, k)
        }
    }
    try sys.Print("{n}\n", (n: total.Into())) // prints 5050
}
```

A scope can also collect task errors (`io.ScopeOf[E]()`), be cancelled or given a deadline (`s.Cancel()`, `s.CancelAfter(d)`), and limit its tasks and their stacks (`io.ScopeWith`). A cancelled operation fails with `Canceled`, an ordinary error. `std.sync` has more locks and `Select` over channels, and `std.atomic` has atomics. [The reference](reference.md#12-concurrency) has the rules, and the cookbook shows how to [serve each connection in its own task](cookbook.md#serve-each-connection-in-its-own-task), [time out a read, an idle connection and a retry](cookbook.md#time-out-a-read-an-idle-connection-and-a-retry) and [shut down on a signal](cookbook.md#shut-down-on-a-signal).

## Modules, packages and imports

A module is a directory of `.mk` files. Every file in it sees every declaration in the others. Everything is private to its module unless it is marked `export`, and that includes struct fields.

A package is a directory with a `pkg.mk` manifest. `mk new hello` makes `pkg.mk` and `src/main.mk`. Add a directory and you have a second module:

```text
hello/
  pkg.mk
  src/
    main.mk
    geom/
      geom.mk
```

```mk
// verdict: parse
const Package = pkg.Package(
    name: "hello",
    mk: "0.1",
    bins: [pkg.Bin(name: "hello", root: "src", subsystem: "console")],
)
```

The manifest is itself Mk: a constant built at compile time. A module's import path is its directory relative to the package root, with `.` for `/`, so `src/geom` is `import src.geom`. `src/geom/geom.mk` could hold:

```mk
// verdict: ok
export struct Vec2 {
    export x: f64
    export y: f64
}

export Vec2.Dot(&self, o: &Vec2): f64 {
    return self.x * o.x + self.y * o.y
}
```

and `src/main.mk` uses it through the last segment of its path:

```mk
// verdict: parse
import src.geom

export Main(sys: &System): WriteError!void {
    v := geom.Vec2(x: 3.0, y: 4.0)
    try sys.Print("{d}\n", (d: v.Dot(&v))) // prints 25
}
```

Each file imports what it uses, and names stay qualified: `geom.Vec2`, `fs.ReadText`. The standard library's modules are `std.<name>`. A small prelude, with `List`, `Map`, `Text`, `Arena`, `Io` and `Print`, needs no import. [Imports](reference.md#imports) and [exports](reference.md#exports-and-visibility) have the rules.

Dependencies are other packages, named in the manifest by path, archive or repository, with a version. `mk install`, `mk update` and `mk tree` manage them. [Packages](packages.md) covers manifests, versions and the lock file; [codebase](codebase.md) shows how to lay out a larger program.

## Testing

A `test` block is a named test, written beside the code it tests. `mk test` builds every test in the package and runs each in its own process, so a panic fails one test and not the run.

```mk
// verdict: ok
Clamp(x: int, lo: int, hi: int): int
    requires lo <= hi
    ensures result >= lo && result <= hi
{
    if x < lo {
        return lo
    }
    if x > hi {
        return hi
    }
    return x
}

test "clamp keeps a value inside the range" {
    Assert(Clamp(5, 0, 3) == 3)
    Assert(Clamp(-1, 0, 3) == 0)
}

test "a list on the heap" (sys: &System) {
    a := sys.heap.Alloc()
    var xs := HeapList[int].New(a)
    defer xs.Free(a)
    try xs.Push(a, 4)
    Assert(xs.Len() == 1)
}
```

```text
$ mk test
PASS src "clamp keeps a value inside the range"
PASS src "a list on the heap"
2 passed, 0 failed
```

- `Assert(cond)` panics when `cond` is false, which fails the test.
- A test may take `(sys: &System)` to get memory and I/O. It may use `try`: an error fails it.
- A test's heap reports leaks.

`requires`, `ensures` and a struct's `invariant` are contracts, checked in development and test builds and left out of release builds. `mk test --prop` calls each function that has one with generated inputs, and reports any input that breaks it, shrunk to a single call. `std.testing` adds fuzzing and benchmarks (`mk bench`), and `std.sim` replays a whole program, its disk, network and clock included, from a seed. See [contracts and tests](reference.md#15-contracts-and-tests).

## Compile-time code

There is no separate compile-time language. A `const` initializer runs ordinary Mk while the program compiles. Any function that takes no `Io` can run there, with no marker. The result is the same on every build, and an initializer that runs too long is a compile error, not a hang.

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

`Target` describes the platform being compiled for. A `fields` loop visits each field of a struct, by name and by reference, under a `Fields[B]` bound:

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

### Generators

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

The library's generators include `derive.Eq`, `derive.Hash` and `derive.Format`, `json.Codec`, `cli.Args` (a command-line parser from a struct), `regex.Matcher` (a regular expression compiled to code) and `dyn.Of`.

A generator of your own is an ordinary exported function in another module. It reads a type's fields and signatures, never function bodies, and emits Mk source with `quote` blocks. [The reference](reference.md#11-compile-time) shows how to write one.

## Unsafe code and C

Code that the compiler cannot check goes in an `unsafe` block, which states the reason it is sound. Inside one you can dereference a raw pointer `*T`, call a C function, and run inline assembly. A module may hold `unsafe` blocks only when the package's manifest lists it in `unsafe_modules` (E1102), so one line of the manifest shows every module that needs review.

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

To run it, list the module in `pkg.mk`: `unsafe_modules: ["src"]`.

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

`import c.sqlite3` turns the C headers named in the manifest (`pkg.CImport`) into Mk declarations. C sources listed in `c_sources` are compiled and linked against `std.libc`, a C library written in Mk. For kernels and boot code, Mk also has `asm`, `naked` functions, linker sections and a freestanding target. [The reference](reference.md#14-unsafe-and-c) covers all of it.

## Habits from other languages

The compiler knows the common forms of other languages, and its error names the Mk form: `let x = 1` is `x := 1`, and `x?` is `try x`. `mk fix` applies the rewrites it can prove, and `mk explain E0501` explains any error code. [The reference](reference.md#16-habits-from-other-languages) has the full table.

## What to read next

- [The language reference](reference.md): every rule of the language, and the signature of every library function.
- [The memory model](memory.md): ownership, borrowing and linear values, rule by rule.
- [The cookbook](cookbook.md): whole programs for common tasks: files, maps, servers, tasks, timeouts, JSON, processes.
- [Examples](examples.md): one larger program per domain.
- [The standard library](std/README.md) and [errors](errors.md): what each module offers, and every error code.
- [Tools](tools.md): `mk check`, `mk fix`, `mk query`, the language server and the rest of the command line.
- [Style](style.md) and [codebase](codebase.md): how Mk code is written and laid out.
