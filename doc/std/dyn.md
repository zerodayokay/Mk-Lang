# std.dyn

`std.dyn` gives you values of an interface: one type that holds any type implementing the interface and calls that type's methods at run time. Mk has no `dyn` type of its own; `gen dyn.Of(I)` writes the type for an interface `I` while your program compiles. Reach for it when a list or a field must hold values of several types behind one interface. When the set of types is fixed, an `enum` with a variant per type is simpler and needs no indirect call. When the code can be generic, a type parameter `T: I` needs none either.

[Generators](../reference.md#generators) in the reference explain how a `gen` line works.

```mk
// verdict: parse
import std.dyn
```

## Contents

- [The main pieces](#the-main-pieces)
- [What gen dyn.Of writes](#what-gen-dynof-writes)
- [Ownership and loans](#ownership-and-loans)
- [Interfaces it refuses](#interfaces-it-refuses)
- [Cost and safety](#cost-and-safety)
- [dyn.Ref by hand](#dynref-by-hand)
- [std.dyn.erased](#stddynerased)
- [Examples](#examples): [a list of shapes](#a-list-of-shapes), [methods that write and fail](#methods-that-write-and-fail), [a Ref by hand](#a-ref-by-hand)
- [API](#api)

## The main pieces

| Task | Use |
|---|---|
| Make a value type for interface `I` | `gen dyn.Of(I)`, which declares `IDyn` |
| Wrap an object as an `IDyn` | `IDyn.Of(x)`, with `x` a `mut T` for some `T: I` |
| Call a method | `d.M(...)`, as on any `I`: `IDyn` implements `I` |
| Erase a type by hand | `dyn.Ref.Of(x)` |
| Get the object back | `Ref.Get[T]()` to read, `Ref.Mut[T]()` to write |
| Ask which type it was | `Ref.Is[T]()` |

## What gen dyn.Of writes

For an interface `Shape`, `gen dyn.Of(Shape)` declares, in the module that holds the `gen` line:

- `mut view struct ShapeDyn`, with two fields: `obj`, a `dyn.Ref` holding the object's address and its type's key, and `table`, a record with one `fn` field per method of `Shape`.
- `ShapeDyn.Of[T: Shape](x: mut T): ShapeDyn from x`, which wraps `x`.
- `impl Shape for ShapeDyn`, one method per method of `Shape`. Each loads its `fn` from the table and calls it. So a `ShapeDyn` passes wherever a `Shape` does, generic code included.
- `ShapeDyn.AdaptArea[T]` and one such function per method: the functions the table points at. Each gets the `T` back from the `dyn.Ref` and calls `T`'s method.

When `Shape` is exported, `ShapeDyn` and `ShapeDyn.Of` are exported too. The fields `obj` and `table` stay private to the module with the `gen` line, so only that module can reach the `dyn.Ref`, as in `d.obj.Is[T]()`. The output names `dyn.Ref` itself, so it works under any import name, as with `import d = std.dyn` and `gen d.Of(Shape)`. `mk query members` shows what was declared; for the interface in the [first example](#a-list-of-shapes):

```text
$ mk query members ShapeDyn
obj: Ref
table: (area: fn(&Ref): f64, name: fn(&Ref): str)
AdaptArea[T: Shape](obj: &Ref): f64
AdaptName[T: Shape](obj: &Ref): str
Of[T: Shape](x: mut T): ShapeDyn from x
impl Shape
```

- A table field is the method's name in snake_case: `ReadAt` is `read_at`, `HTTPGet` is `http_get`. A name that is a keyword, or one of the contextual words `from`, `into`, `ends`, `fuel`, `packed`, `self`, `result` and `fields`, gets `_` after it. For example, `Error` is `error_` and `Result` is `result_`.
- A method with a `mut self` receiver gets a slot that takes a `mut dyn.Ref`; one with `&self` gets a slot that takes a `&dyn.Ref`. The method's own parameters follow.
- An adapter's first parameter is `obj`. When a parameter of the method has that name, ignoring case and underscores, it is `obj2` (or `obj3`, and so on), so your parameter names are kept.

## Ownership and loans

`IDyn.Of(x)` takes an exclusive loan of `x`: the `IDyn` holds it until its last use, and `x` cannot be read or written another way while it lives. `IDyn` is a `view` type, so it is unique: it moves and is never copied ([value kinds](../memory.md#r1-value-kinds)).

- An object must outlive every `IDyn` made from it. A list of `IDyn` values in an arena needs its objects in that arena too, or in memory that lives longer: put each with `a.Put(...)`. A local stored into a longer-lived list is E0505.
- Loop over such a list with `for d in &list`, or with `for d in mut list` when a method takes `mut self`. `for d in list` would copy each element out, which a unique value cannot do (E0409).
- Once the last use of the `IDyn` has passed, the object is free again.

## Interfaces it refuses

`gen dyn.Of(I)` reports E0903 at `I` when the interface cannot be called through an erased object:

| Refused | Why |
|---|---|
| Not an interface | Only an interface has methods to forward |
| An interface with no method | Nothing to forward |
| A method that takes `self` by value | It would move the object out of its owner |
| A method with no receiver | There is no object to call it on |
| A method with type parameters of its own | A `fn` value needs all its types |
| A method that declares a flow clause (`from`, `into`, `ends`) | The clause cannot be stated over the erased object |
| A method with a `const` parameter | A `fn` value cannot have one |
| A method that mentions `Self` | The erased object's type is unknown |
| A method named `Of`, `Obj` or `Table`, or starting with `Adapt` | The output declares those names. Names are compared as E0206 compares them, ignoring case and underscores |

```mk
// verdict: reject E0903
import std.dyn

interface Shape { // <- E0903
    Area(&self): f64
    Grow(self, by: f64): Self // by value, and mentions Self
}

gen dyn.Of(Shape)
```

## Cost and safety

A call through an `IDyn` loads one `fn` from the value itself, with no second load through a table pointer, and makes one indirect call. The adapter then compares the stored type key with its own `T`'s: one integer comparison.

That comparison runs on every call. The generated code is ordinary code in your module, with your module's rights. Code there could build an `IDyn` by hand and pair one type's object with another type's function. With the check, such a mistake panics with code 8 ([panic codes](runtime.md#panics)) instead of reading memory as the wrong type. Code written by `gen dyn.Of` never pairs them wrongly.

The code `gen dyn.Of` writes uses layer 0 only ([layers](README.md#1-layers)), so a freestanding program may use it. `std.dyn` allocates nothing at run time.

## dyn.Ref by hand

`dyn.Ref` is the erased object itself: a `mut view struct` holding an address and a type key. `Ref.Of(x)` makes one from a `mut T`, holding `x`'s exclusive loan. `Get[T]` and `Mut[T]` give the object back only as the type it was made from, and panic with code 8 for any other type; `Is[T]` tests first.

Use a `Ref` directly to keep a function and its state of any type together, without an interface. The cookbook's [Queue jobs whose states have different types](../cookbook.md#queue-jobs-whose-states-have-different-types) does that.

## std.dyn.erased

`std.dyn.erased` holds one function, `Key[T]()`: the `u64` type key a `Ref` stores and compares. The compiler computes it, so it is a constant, different for each distinct type of one program. If two types ever met one key, the compiler stops with an internal error naming both rather than build the program. Most code never calls it; `Ref.Is` is the test to use.

## Examples

### A list of shapes

```mk
// verdict: ok
import std.dyn

interface Shape {
    Area(&self): f64
    Name(&self): str
}

struct Square {
    side: f64
}

impl Shape for Square {
    Area(&self): f64 { return self.side * self.side }
    Name(&self): str { return "square" }
}

struct Rect {
    w: f64
    h: f64
}

impl Shape for Rect {
    Area(&self): f64 { return self.w * self.h }
    Name(&self): str { return "rect" }
}

gen dyn.Of(Shape) // ShapeDyn: one value type for every Shape

export Main(sys: &System): OutOfMemory|WriteError!void {
    var shapes := List[ShapeDyn].New(sys.arena)
    sq := try sys.arena.Put(Square(side: 3.0)) // the objects live as long as the list
    try shapes.Push(ShapeDyn.Of(sq))
    r := try sys.arena.Put(Rect(w: 2.0, h: 5.0))
    try shapes.Push(ShapeDyn.Of(r))
    var total := 0.0
    for s in &shapes { // a ShapeDyn is unique: iterate by reference
        try sys.Print("{name}: {area}\n", (name: s.Name(), area: s.Area()))
        total += s.Area()
    }
    try sys.Print("total: {total}\n", (total))
}
```

```text
$ mk run shapes.mk
square: 9
rect: 10
total: 19
```

- `ShapeDyn.Of(sq)` takes `sq`'s loan. With `var sq := Square(...)` on the stack instead, the push is E0505: the list in `sys.arena` would outlive the local.

### Methods that write and fail

```mk
// verdict: ok
import std.dyn

error Full

interface Sink {
    Put(mut self, x: int): Full!void // mut self: the slot takes a mut dyn.Ref
    Count(&self): int
}

struct Keep {
    n: int
    sum: int
}

impl Sink for Keep {
    Put(mut self, x: int): Full!void {
        self.n += 1
        self.sum += x
    }
    Count(&self): int { return self.n }
}

struct Small {
    n: int
}

impl Sink for Small {
    Put(mut self, x: int): Full!void {
        _ = x
        if self.n == 2 {
            fail Full
        }
        self.n += 1
    }
    Count(&self): int { return self.n }
}

gen dyn.Of(Sink)

// Fill works on any Sink; a SinkDyn is one, so it works on SinkDyn too.
Fill[T: Sink](s: mut T): Full!void {
    for x in 1..4 {
        try s.Put(x)
    }
}

export Main(sys: &System): OutOfMemory|WriteError!void {
    k := try sys.arena.Put(Keep(n: 0, sum: 0))
    sm := try sys.arena.Put(Small(n: 0))
    var sinks := [SinkDyn.Of(k), SinkDyn.Of(sm)]
    for i in 0..2 {
        var ok := true
        Fill(mut sinks[i]) else { ok = false }
        try sys.Print("sink {i}: {n} taken, full: {full}\n", (i, n: sinks[i].Count(), full: !ok))
    }
    try sys.Print("first is Keep: {a}, is Small: {b}\n", (a: sinks[0].obj.Is[Keep](), b: sinks[0].obj.Is[Small]()))
    // sinks holds k's loan until its last use above, so k is free again here
    try sys.Print("sum: {sum}\n", (sum: k.sum))
}
```

```text
$ mk run sinks.mk
sink 0: 3 taken, full: false
sink 1: 2 taken, full: true
first is Keep: true, is Small: false
sum: 6
```

- A method's error set passes through unchanged: `SinkDyn.Put` fails with `Full` when the object's `Put` does.
- Reading `k.sum` while `sinks` is still used later is E0504: the array holds `k`'s exclusive loan.

### A Ref by hand

```mk
// verdict: ok
import std.dyn

struct Celsius {
    deg: int
}

struct Count {
    n: int
}

export Main(sys: &System): WriteError!void {
    var c := Celsius(deg: 21)
    var r := dyn.Ref.Of(mut c) // r holds c's exclusive loan
    try sys.Print("Celsius? {a}  Count? {b}\n", (a: r.Is[Celsius](), b: r.Is[Count]()))
    r.Mut[Celsius]().deg += 1
    try sys.Print("deg: {d}\n", (d: r.Get[Celsius]().deg))
}
```

```text
$ mk run ref.mk
Celsius? true  Count? false
deg: 22
```

Adding `_ = r.Get[Count]()` at the end compiles, and panics when it runs:

```text
panic: dyn: an erased object recovered as a type it was not made from at ...
```

The location in std and a backtrace follow, cut here.

## API

<!-- api:begin -->
Stability: Stable.

### `Of`

```
export Of(g: mut Gen, i: Type)
```

The generator behind `gen dyn.Of(I)`: it writes IDyn, one value type
for every type that implements the interface I, which calls the right
type's methods at run time. For an interface Shape it emits, in the gen
line's module:

- `mut view struct ShapeDyn`, with `obj: dyn.Ref`, the object, and `table`,
  a record with one fn field per method;
- `ShapeDyn.Of[T: Shape](x: mut T): ShapeDyn from x`, which holds x's
  exclusive loan;
- `impl Shape for ShapeDyn`, whose methods each load one fn from the
  table and call it, so a ShapeDyn passes wherever a Shape does.

Each table field is its method's name in snake_case, with `_` after a
keyword or a contextual word such as from or result (ReadAt is
read_at, Error is error_). The table holds generic
functions named ShapeDyn.Adapt<M>[T], one per method M. When I is
exported, ShapeDyn and ShapeDyn.Of are too.

E0903 at I when I is not an interface or has no method, or when a
method takes self by value or has no receiver, has type parameters of
its own, declares a flow clause, takes a const parameter, or mentions
Self. Also E0903 for a method named Of, Obj or Table, or starting with
Adapt, compared as E0206 compares names: the output declares those.
Runs at compile time, O(methods and their parameters); allocates the
emitted code in the run's arena. Each call through the output costs
one key comparison and one indirect call.

### `Ref`

```
export mut view struct Ref
```

Ref is an object whose type is erased: the object's address and its
type's key. It holds the exclusive loan of the object it was made from,
and as a view it is unique: it cannot be copied.

### `Ref.Get`

```
export Ref.Get[T](&self): &T from self
```

- Flows: The result borrows from self.

Get gives the object back for reading, as the T it was made from. It
panics (code 8), as Mut does, when the Ref was made from another type.
O(1).

### `Ref.Is`

```
export Ref.Is[T](&self): bool
```

Is reports whether the Ref was made from a T, so a caller can test
before Get or Mut. O(1).

### `Ref.Mut`

```
export Ref.Mut[T](mut self): mut T from self
```

- Flows: The result borrows from self.

Mut gives the object back, writable, as the T it was made from. It
panics (code 8) when the Ref was made from another type. Code that
gen dyn.Of writes never does that; only code that pairs a Ref with the
wrong type by hand can. O(1).

### `Ref.Of`

```
export Ref.Of[T](x: mut T): Ref from x
```

- Flows: The result borrows from x.

Of erases x's type: the Ref holds x's exclusive loan, so x cannot be
used another way until the Ref's last use. O(1); allocates nothing.

### Module `std.dyn.erased`

Stability: Stable.

#### `Key`

```
export Key[T](): u64
```

Key is T's type key: a compile-time constant, different for each
distinct type of one program. If two types ever met one key, the
compiler would stop with an internal error naming both, rather than
build the program. O(1).

<!-- api:end -->
